package gitstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// Sync is how a clone talks to its remote: clone (shallow), fetch what
// others pushed, push what we committed, and tidy the object store.
// Nothing here moves the branch on its own except cloning: Fetch reports
// the remote tip, the caller merges or adopts it.
//
// Clone and Fetch drive one upload-pack session by hand rather than going
// through go-git's Fetch: that one applies only the server's "shallow"
// lines and not its "unshallow" lines, and offers every local ref as a
// "have" — including commits the remote has never seen, which a strict
// server refuses. Here the only have is the tip the remote is known to
// hold. The session goes through go-git's transport registry, so file://
// (the git binaries) and https behave the same.

// Remote is where the repository lives: a URL and how to authenticate.
type Remote struct {
	URL  string
	Auth transport.AuthMethod
}

const remoteName = "origin"

var (
	// ErrEmptyRepository is returned by Clone when the remote has no
	// commits, or none on the branch.
	ErrEmptyRepository = errors.New("gitstore: remote has no commits on the branch")
	// ErrNoRemoteTip is returned by Fetch when the remote has no branch to
	// sync with.
	ErrNoRemoteTip = errors.New("gitstore: remote has no such branch")
	// ErrRejected is returned by Push when the remote's branch is not an
	// ancestor of ours: fetch, merge, push again.
	ErrRejected = errors.New("gitstore: push rejected: not a fast-forward")
)

// Clone clones the remote's branch into an empty storer. depth 1 is the
// current state — the cold start; 0 is the whole history. A server that
// does not speak shallow is cloned whole. When the remote is empty or
// lacks the branch the storer is left initialised (HEAD on the branch, no
// commits) and ErrEmptyRepository is returned with a nil Repo.
func Clone(ctx context.Context, s storage.Storer, remote Remote, opts Options, depth int) (*Repo, error) {
	r, err := Init(s, opts)
	if err != nil {
		return nil, err
	}
	if err := r.fill(ctx, remote, depth); err != nil {
		if errors.Is(err, ErrNoRemoteTip) {
			return nil, fmt.Errorf("%w: %s", ErrEmptyRepository, remote.URL)
		}
		return nil, fmt.Errorf("gitstore: clone %s: %w", remote.URL, err)
	}
	return r, nil
}

// fill fetches the remote's tip into an unborn branch and points the
// branch and the tracking ref at it.
func (r *Repo) fill(ctx context.Context, remote Remote, depth int) error {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	tip, err := r.uploadPack(ctx, remote, func(req *packp.UploadPackRequest, _ plumbing.Hash, caps *capability.List) bool {
		if depth > 0 && caps.Supports(capability.Shallow) {
			req.Depth = packp.DepthCommits(depth)
		}
		return true
	})
	if err != nil {
		return err
	}
	if err := r.s.SetReference(plumbing.NewHashReference(r.tracking(), tip)); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.s.SetReference(plumbing.NewHashReference(r.branch, tip))
}

// tracking is the remote-tracking ref for the branch.
func (r *Repo) tracking() plumbing.ReferenceName {
	return plumbing.NewRemoteReferenceName(remoteName, r.branch.Short())
}

// RemoteTip is the last known tip of the remote's branch (zero if never
// fetched or pushed).
func (r *Repo) RemoteTip() plumbing.Hash {
	ref, err := r.s.Reference(r.tracking())
	if err != nil {
		return plumbing.ZeroHash
	}
	return ref.Hash()
}

// Fetch brings the remote's branch into the tracking ref — no depth, so a
// shallow clone gains no new boundaries — and reports the remote tip and
// whether it moved since the last fetch or push. The local branch is not
// touched. A remote without the branch is ErrNoRemoteTip.
func (r *Repo) Fetch(ctx context.Context, remote Remote) (plumbing.Hash, bool, error) {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	before := r.RemoteTip()
	tip, err := r.uploadPack(ctx, remote, func(req *packp.UploadPackRequest, tip plumbing.Hash, _ *capability.List) bool {
		if tip == before || r.has(tip) {
			return false // nothing new to transfer
		}
		if !before.IsZero() && r.has(before) {
			req.Haves = []plumbing.Hash{before}
		}
		return true
	})
	if err != nil {
		return plumbing.ZeroHash, false, fmt.Errorf("gitstore: fetch: %w", err)
	}
	if tip != before {
		if err := r.s.SetReference(plumbing.NewHashReference(r.tracking(), tip)); err != nil {
			return plumbing.ZeroHash, false, err
		}
	}
	return tip, tip != before, nil
}

// uploadPack runs one upload-pack session for the branch: advertise, let
// build shape the request (haves, depth) — build returns false to skip
// the transfer — store the pack, apply shallow and unshallow lines. It
// returns the remote's tip.
func (r *Repo) uploadPack(ctx context.Context, remote Remote, build func(*packp.UploadPackRequest, plumbing.Hash, *capability.List) bool) (tip plumbing.Hash, err error) {
	ep, err := transport.NewEndpoint(remote.URL)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	cl, err := client.NewClient(ep)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	sess, err := cl.NewUploadPackSession(ep, remote.Auth)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	defer closeKeep(sess, &err)
	ar, err := sess.AdvertisedReferencesContext(ctx)
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return plumbing.ZeroHash, ErrNoRemoteTip
	}
	if err != nil {
		return plumbing.ZeroHash, err
	}
	refs, err := ar.AllReferences()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	ref, ok := refs[r.branch]
	if !ok {
		return plumbing.ZeroHash, ErrNoRemoteTip
	}
	tip = ref.Hash()
	req := packp.NewUploadPackRequestFromCapabilities(ar.Capabilities)
	req.Wants = []plumbing.Hash{tip}
	if !build(req, tip, ar.Capabilities) {
		return tip, nil
	}
	for _, cp := range []capability.Capability{capability.Shallow, capability.OFSDelta, capability.Sideband64k, capability.NoProgress} {
		if ar.Capabilities.Supports(cp) {
			if err := req.Capabilities.Set(cp); err != nil {
				return plumbing.ZeroHash, err
			}
		}
	}
	shallows, err := r.s.Shallow()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if len(shallows) > 0 {
		if !ar.Capabilities.Supports(capability.Shallow) {
			return plumbing.ZeroHash, errors.New("remote does not support shallow clients")
		}
		req.Shallows = shallows
	}
	resp, err := sess.UploadPack(ctx, req)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	defer closeKeep(resp, &err)
	var body io.Reader = resp
	if req.Capabilities.Supports(capability.Sideband64k) {
		body = sideband.NewDemuxer(sideband.Sideband64k, resp)
	}
	if err := packfile.UpdateObjectStorage(r.s, body); err != nil {
		return plumbing.ZeroHash, err
	}
	if len(resp.Shallows) > 0 || len(resp.Unshallows) > 0 {
		if err := r.applyShallows(resp.Shallows, resp.Unshallows); err != nil {
			return plumbing.ZeroHash, err
		}
	}
	return tip, nil
}

// has reports whether the object is in the local store.
func (r *Repo) has(h plumbing.Hash) bool {
	_, err := r.s.EncodedObject(plumbing.AnyObject, h)
	return err == nil
}

// Push sends the branch tip to the remote's branch, creating it when the
// remote lacks it. A non-fast-forward — which go-git reports from a
// shallow clone as a missing object, not as a rejection — is classified
// by looking at the remote's tip and returned as ErrRejected. On success
// the tracking ref is the pushed tip.
func (r *Repo) Push(ctx context.Context, remote Remote) error {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	head := r.Head()
	if head.IsZero() {
		return errors.New("gitstore: nothing to push")
	}
	rem := git.NewRemote(r.s, &config.RemoteConfig{Name: remoteName, URLs: []string{remote.URL}})
	err := rem.PushContext(ctx, &git.PushOptions{
		Auth: remote.Auth,
		// The hash, not the ref: a commit landing meanwhile is not sent
		// under a tracking ref that claims otherwise.
		RefSpecs: []config.RefSpec{config.RefSpec(head.String() + ":" + r.branch.String())},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		if r.rejected(ctx, rem, remote, head) {
			return fmt.Errorf("%w: %w", ErrRejected, err)
		}
		return fmt.Errorf("gitstore: push: %w", err)
	}
	return r.s.SetReference(plumbing.NewHashReference(r.tracking(), head))
}

// rejected reports whether the remote's branch holds a commit head does
// not contain.
func (r *Repo) rejected(ctx context.Context, rem *git.Remote, remote Remote, head plumbing.Hash) bool {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false
	}
	refs, err := rem.ListContext(ctx, &git.ListOptions{Auth: remote.Auth})
	if err != nil {
		return false
	}
	for _, ref := range refs {
		if ref.Name() != r.branch {
			continue
		}
		if ref.Hash() == head {
			return false
		}
		in, err := r.IsAncestor(ref.Hash(), head)
		return err == nil && !in
	}
	return false
}

// Unpushed counts the commits on the branch the remote has not seen: those
// not reachable from RemoteTip (within the loaded history) — all of them
// when the branch was never pushed.
func (r *Repo) Unpushed() (int, error) {
	known, err := r.remoteAncestors()
	if err != nil {
		return 0, err
	}
	n := 0
	err = r.walkUntil([]plumbing.Hash{r.Head()}, known, func(plumbing.Hash) bool { n++; return true }, false)
	return n, err
}

// remoteAncestors is the set of commits reachable from RemoteTip, kept
// across calls: a moved tip only walks down to what the old set holds.
func (r *Repo) remoteAncestors() (map[plumbing.Hash]bool, error) {
	r.setMu.Lock()
	defer r.setMu.Unlock()
	tip := r.RemoteTip()
	if tip == r.remoteSetTip && r.remoteSet != nil {
		return r.remoteSet, nil
	}
	set := r.remoteSet
	if set != nil {
		// Reuse only along a fast-forward: after a forced move the old
		// set holds commits the remote no longer has.
		if ff, err := r.IsAncestor(r.remoteSetTip, tip); err != nil || !ff {
			set = nil
		}
	}
	if set == nil {
		set = map[plumbing.Hash]bool{}
	}
	next := make(map[plumbing.Hash]bool, len(set))
	err := r.walkUntil([]plumbing.Hash{tip}, set, func(h plumbing.Hash) bool { next[h] = true; return true }, false)
	if err != nil {
		return nil, err
	}
	for h := range set {
		next[h] = true // ancestor-closed: everything the old tip reached stays known
	}
	r.remoteSet, r.remoteSetTip = next, tip
	return next, nil
}

// applyShallows sets the shallow list to old + added − removed.
func (r *Repo) applyShallows(added, removed []plumbing.Hash) error {
	old, err := r.s.Shallow()
	if err != nil {
		return err
	}
	drop := map[plumbing.Hash]bool{}
	for _, h := range removed {
		drop[h] = true
	}
	seen := map[plumbing.Hash]bool{}
	next := []plumbing.Hash{}
	for _, h := range append(old, added...) {
		if drop[h] || seen[h] {
			continue
		}
		seen[h] = true
		next = append(next, h)
	}
	return r.s.SetShallow(next)
}

func closeKeep(c io.Closer, err *error) {
	if cerr := c.Close(); cerr != nil && *err == nil {
		*err = cerr
	}
}

// Maintain repacks loose objects into one pack and prunes them — go-git
// never packs on its own, and a commit per action leaves a few loose
// objects each. A store that cannot pack (the in-memory one) is left
// alone. It holds the write and sync locks, so no object written by a
// commit in flight is pruned before its ref points at it.
func (r *Repo) Maintain() error {
	if _, ok := r.s.(storer.PackfileWriter); !ok {
		return nil
	}
	if _, ok := r.s.(storer.PackedObjectStorer); !ok {
		return nil
	}
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	repo, err := git.Open(r.s, nil)
	if err != nil {
		return err
	}
	if err := repo.RepackObjects(&git.RepackConfig{}); err != nil {
		return fmt.Errorf("gitstore: repack: %w", err)
	}
	err = repo.Prune(git.PruneOptions{OnlyObjectsOlderThan: time.Now(), Handler: repo.DeleteObject})
	if err != nil && !errors.Is(err, git.ErrLooseObjectsNotSupported) {
		return fmt.Errorf("gitstore: prune: %w", err)
	}
	return nil
}

// OpenDir opens the repository kept at dir — a bare repository in go-git's
// filesystem layout — with its unpushed commits, or clones remote into it
// (depth 1) when dir holds none. When the remote is empty or lacks the
// branch, an empty repository is left there whose first push creates the
// branch. created is true when the returned repository's branch is unborn
// — the caller writes the first content — and false when it has commits,
// reopened or cloned.
func OpenDir(ctx context.Context, dir string, remote Remote, opts Options) (*Repo, bool, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, false, fmt.Errorf("gitstore: data dir: %w", err)
	}
	s := filesystem.NewStorage(osfs.New(dir), cache.NewObjectLRUDefault())
	r := wrap(s, opts)
	if !r.Head().IsZero() {
		return r, false, nil
	}
	if _, err := git.Open(s, nil); errors.Is(err, git.ErrRepositoryNotExists) {
		if r, err = Init(s, opts); err != nil {
			return nil, false, err
		}
	} else if err != nil {
		return nil, false, fmt.Errorf("gitstore: open %s: %w", dir, err)
	}
	// Unborn — new, or initialised before against a then-empty remote:
	// take the remote's current state if it has one by now.
	switch err := r.fill(ctx, remote, 1); {
	case err == nil:
		return r, false, nil
	case errors.Is(err, ErrNoRemoteTip):
		return r, true, nil
	default:
		return nil, false, fmt.Errorf("gitstore: clone %s: %w", remote.URL, err)
	}
}
