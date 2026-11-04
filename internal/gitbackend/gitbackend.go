// Package gitbackend stores boards in a git repository and keeps it in step
// with a remote.
//
// Every write of a board is one commit (boards/<id>/{board.json,state.jsonl,
// doc.json}) authored by the people whose changes it carries. A background
// loop pushes new commits and fetches what other replicas pushed.
//
// There are no merge conflicts to resolve by hand: when both sides changed
// a board, its two Yjs states are merged (a CRDT — every edit from both
// sides survives) and recorded as a merge commit on top of the remote tip;
// metadata keeps the side updated last. A rejected push is therefore just
// fetch → merge → push again.
package gitbackend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/aenix-io/aenotes/internal/boards"
	"github.com/aenix-io/aenotes/internal/gitstore"
)

// Options configures the backend.
type Options struct {
	// FetchInterval is how often the remote is polled for others' commits.
	FetchInterval time.Duration
	// PushRetries bounds fetch-merge-push rounds after a rejected push.
	PushRetries int
	// Log receives sync events.
	Log *slog.Logger
}

// Backend implements boards.Backend over a gitstore.Repo.
type Backend struct {
	repo   *gitstore.Repo
	remote *gitstore.Remote // nil: commit locally, never sync
	opts   Options

	// mu serialises everything that moves the branch: a Save's commits and
	// an integration's merge must not interleave, or a merge computed from
	// an older head would drop the commit made in between.
	mu sync.Mutex

	kick chan struct{}

	smu           sync.Mutex
	lastSync      time.Time
	lastErr       error
	unpushedSince time.Time

	// OnRemote receives the boards another replica changed, after they are
	// merged into the branch; main wires it to the board store and the live
	// rooms.
	OnRemote func(ctx context.Context, remotes []boards.Remote)
	// OnRemoteFolders receives the remote's folder tree when it changed.
	OnRemoteFolders func(folders []boards.Folder)
	// OnStatus is called whenever Status may have changed.
	OnStatus func()
}

// New returns a backend over repo, syncing with remote when it is non-nil.
func New(repo *gitstore.Repo, remote *gitstore.Remote, opts Options) *Backend {
	if opts.FetchInterval <= 0 {
		opts.FetchInterval = 30 * time.Second
	}
	if opts.PushRetries <= 0 {
		opts.PushRetries = 5
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	return &Backend{repo: repo, remote: remote, opts: opts, kick: make(chan struct{}, 1)}
}

// Load reads every board at the branch tip.
func (b *Backend) Load(context.Context) ([]boards.Snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readAll(b.repo.Head())
}

func (b *Backend) readAll(at plumbing.Hash) ([]boards.Snapshot, error) {
	if at.IsZero() {
		return nil, nil
	}
	ids, err := b.repo.ListDir(at, boards.Dir)
	if err != nil {
		return nil, err
	}
	var out []boards.Snapshot
	for _, id := range ids {
		if !boards.ValidID(id) {
			continue
		}
		sn, ok, err := b.read(at, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, sn)
		}
	}
	return out, nil
}

// read returns board id at commit, ok=false when it has no metadata there.
func (b *Backend) read(at plumbing.Hash, id string) (boards.Snapshot, bool, error) {
	meta, err := b.repo.ReadFileAt(at, boards.MetaPath(id))
	if errors.Is(err, gitstore.ErrNotFound) {
		return boards.Snapshot{}, false, nil
	}
	if err != nil {
		return boards.Snapshot{}, false, err
	}
	state, err := b.readOptional(at, boards.StatePath(id))
	if err != nil {
		return boards.Snapshot{}, false, err
	}
	legacy, err := b.readOptional(at, boards.LegacyStatePath(id))
	if err != nil {
		return boards.Snapshot{}, false, err
	}
	sn, err := boards.Decode(id, meta, state, legacy)
	return sn, err == nil, err
}

func (b *Backend) readOptional(at plumbing.Hash, p string) ([]byte, error) {
	data, err := b.repo.ReadFileAt(at, p)
	if errors.Is(err, gitstore.ErrNotFound) {
		return nil, nil
	}
	return data, err
}

// Save commits each board, one commit per board.
func (b *Backend) Save(_ context.Context, snaps []boards.Snapshot) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	committed := false
	for _, sn := range snaps {
		files, err := boards.Encode(sn)
		if err != nil {
			return err
		}
		h, err := b.repo.Commit(actionFor(sn), writesOf(files))
		if err != nil {
			return fmt.Errorf("commit board %s: %w", sn.Meta.ID, err)
		}
		committed = committed || !h.IsZero()
	}
	if committed {
		b.smu.Lock()
		if b.unpushedSince.IsZero() {
			b.unpushedSince = time.Now()
		}
		b.smu.Unlock()
		b.status()
		b.Kick()
	}
	return nil
}

func actionFor(sn boards.Snapshot) gitstore.Action {
	a := gitstore.Action{
		Summary:  fmt.Sprintf("Edit board %q", sn.Meta.Title),
		Trailers: map[string]string{"Aenotes-Board": sn.Meta.ID},
	}
	if len(sn.Editors) > 0 {
		a.Actor = sn.Editors[0]
		a.Trailers["Aenotes-Editors"] = strings.Join(sn.Editors, " ")
	}
	return a
}

func writesOf(files []boards.File) []gitstore.FileWrite {
	out := make([]gitstore.FileWrite, len(files))
	for i, f := range files {
		out[i] = gitstore.FileWrite{Path: f.Path, Data: f.Data}
	}
	return out
}

// Kick asks the sync loop to push now.
func (b *Backend) Kick() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// Run is the sync loop: push on every kick, fetch on every interval, until
// ctx ends (then one last push, bounded by final).
func (b *Backend) Run(ctx context.Context, final time.Duration) {
	if b.remote == nil {
		<-ctx.Done()
		return
	}
	b.SyncOnce(ctx) // pick up what changed while we were down
	t := time.NewTicker(b.opts.FetchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), final)
			b.push(fctx)
			cancel()
			return
		case <-b.kick:
			b.push(ctx)
		case <-t.C:
			b.SyncOnce(ctx)
		}
	}
}

// SyncOnce fetches and integrates the remote, then pushes what is unpushed.
func (b *Backend) SyncOnce(ctx context.Context) {
	if b.remote == nil {
		return
	}
	if err := b.fetchAndIntegrate(ctx); err != nil {
		b.fail(err)
		return
	}
	b.push(ctx)
}

func (b *Backend) push(ctx context.Context) {
	if b.remote == nil {
		return
	}
	for attempt := 0; ; attempt++ {
		n, err := b.repo.Unpushed()
		if err != nil {
			b.fail(err)
			return
		}
		if n == 0 {
			b.ok()
			return
		}
		err = b.repo.Push(ctx, *b.remote)
		if err == nil {
			b.opts.Log.Info("pushed", "commits", n)
			b.ok()
			return
		}
		if !errors.Is(err, gitstore.ErrRejected) || attempt >= b.opts.PushRetries {
			b.fail(fmt.Errorf("push: %w", err))
			return
		}
		// Someone pushed first: take their commits in and try again.
		b.opts.Log.Info("push rejected, merging the remote", "attempt", attempt+1)
		if err := b.fetchAndIntegrate(ctx); err != nil {
			b.fail(err)
			return
		}
	}
}

func (b *Backend) fetchAndIntegrate(ctx context.Context) error {
	tip, _, err := b.repo.Fetch(ctx, *b.remote)
	if errors.Is(err, gitstore.ErrNoRemoteTip) {
		return nil // the branch does not exist remotely yet: our push makes it
	}
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	in, err := b.integrate(tip)
	if err != nil {
		return err
	}
	if len(in.boards) > 0 && b.OnRemote != nil {
		b.OnRemote(ctx, in.boards)
	}
	if in.folders != nil && b.OnRemoteFolders != nil {
		b.OnRemoteFolders(in.folders)
	}
	b.ok()
	return nil
}

// incoming is what the remote changed: its copy of every changed board, and
// its folder tree when that changed (nil otherwise).
type incoming struct {
	boards  []boards.Remote
	folders []boards.Folder
}

// integrate brings the remote tip into the branch — a fast-forward when we
// have nothing of our own on top, a merge commit otherwise — and returns
// what the remote changed (as the remote has it).
func (b *Backend) integrate(tip plumbing.Hash) (incoming, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	head := b.repo.Head()
	if tip.IsZero() || tip == head {
		return incoming{}, nil
	}
	if ok, err := b.repo.IsAncestor(tip, head); err != nil {
		return incoming{}, err
	} else if ok {
		return incoming{}, nil // we already contain it
	}
	ids, foldersChanged, err := b.changedBoards(head, tip)
	if err != nil {
		return incoming{}, err
	}
	ff := head.IsZero()
	if !ff {
		if ff, err = b.repo.IsAncestor(head, tip); err != nil {
			return incoming{}, err
		}
	}
	var remotes []boards.Remote
	var writes []gitstore.FileWrite
	var merged []string
	for _, id := range ids {
		theirs, inTheirs, err := b.read(tip, id)
		if err != nil {
			return incoming{}, err
		}
		if inTheirs {
			remotes = append(remotes, boards.Remote{Meta: theirs.Meta, State: theirs.State})
		}
		if ff {
			continue
		}
		ours, inOurs, err := b.read(head, id)
		if err != nil {
			return incoming{}, err
		}
		if !inOurs {
			continue // only theirs: their tree already has it
		}
		m, err := mergeBoard(ours, theirs, inTheirs)
		if err != nil {
			return incoming{}, fmt.Errorf("merge board %s: %w", id, err)
		}
		files, err := boards.Encode(m)
		if err != nil {
			return incoming{}, err
		}
		writes = append(writes, writesOf(files)...)
		merged = append(merged, id)
	}
	var theirFolders []boards.Folder
	if foldersChanged {
		if theirFolders, err = b.readFolders(tip); err != nil {
			return incoming{}, err
		}
		if theirFolders == nil {
			theirFolders = []boards.Folder{}
		}
		if !ff {
			ours, err := b.readFolders(head)
			if err != nil {
				return incoming{}, err
			}
			mergedFolders, _ := boards.MergeFolders(ours, theirFolders)
			data, err := boards.EncodeFolders(mergedFolders)
			if err != nil {
				return incoming{}, err
			}
			writes = append(writes, gitstore.FileWrite{Path: boards.FoldersFile, Data: data})
			merged = append(merged, boards.FoldersFile)
		}
	}
	in := incoming{boards: remotes, folders: theirFolders}
	if ff {
		if err := b.repo.ResetTo(tip); err != nil {
			return incoming{}, err
		}
		b.opts.Log.Info("fast-forwarded to the remote", "boards", len(remotes))
		return in, nil
	}
	a := gitstore.Action{
		Summary:  "Merge boards from the remote",
		Trailers: map[string]string{"Aenotes-Merged": strings.Join(merged, " ")},
	}
	if _, err := b.repo.Merge(tip, a, writes); err != nil {
		return incoming{}, fmt.Errorf("merge: %w", err)
	}
	b.opts.Log.Info("merged the remote", "boards", merged)
	return in, nil
}

// changedBoards lists the board ids whose files differ between two commits,
// and whether the folder tree does.
func (b *Backend) changedBoards(from, to plumbing.Hash) ([]string, bool, error) {
	paths, err := b.repo.ChangedPaths(from, to)
	if err != nil {
		return nil, false, err
	}
	seen := map[string]bool{}
	var ids []string
	folders := false
	for _, p := range paths {
		if path.Clean(p) == boards.FoldersFile {
			folders = true
			continue
		}
		parts := strings.Split(path.Clean(p), "/")
		if len(parts) < 3 || parts[0] != boards.Dir || !boards.ValidID(parts[1]) || seen[parts[1]] {
			continue
		}
		seen[parts[1]] = true
		ids = append(ids, parts[1])
	}
	slices.Sort(ids)
	return ids, folders, nil
}

func (b *Backend) readFolders(at plumbing.Hash) ([]boards.Folder, error) {
	if at.IsZero() {
		return nil, nil
	}
	data, err := b.readOptional(at, boards.FoldersFile)
	if err != nil {
		return nil, err
	}
	return boards.DecodeFolders(data)
}

// LoadFolders reads the folder tree at the branch tip.
func (b *Backend) LoadFolders(context.Context) ([]boards.Folder, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readFolders(b.repo.Head())
}

// SaveFolders commits the folder tree.
func (b *Backend) SaveFolders(_ context.Context, folders []boards.Folder) error {
	data, err := boards.EncodeFolders(folders)
	if err != nil {
		return err
	}
	b.mu.Lock()
	h, err := b.repo.Commit(gitstore.Action{Summary: "Edit folders"}, []gitstore.FileWrite{{Path: boards.FoldersFile, Data: data}})
	b.mu.Unlock()
	if err != nil {
		return fmt.Errorf("commit folders: %w", err)
	}
	if !h.IsZero() {
		b.smu.Lock()
		if b.unpushedSince.IsZero() {
			b.unpushedSince = time.Now()
		}
		b.smu.Unlock()
		b.status()
		b.Kick()
	}
	return nil
}

// mergeBoard combines our and their copy of a board: states merge, the
// later-updated metadata wins.
func mergeBoard(ours, theirs boards.Snapshot, inTheirs bool) (boards.Snapshot, error) {
	if !inTheirs {
		return ours, nil
	}
	out := ours
	if theirs.Meta.Updated.After(ours.Meta.Updated) {
		out.Meta = theirs.Meta
	}
	switch {
	case len(ours.State) == 0:
		out.State = theirs.State
	case len(theirs.State) > 0:
		st, err := boards.MergeStates(ours.State, theirs.State)
		if err != nil {
			return boards.Snapshot{}, err
		}
		if st != nil {
			out.State = st
		}
	}
	return out, nil
}

// Status is the backend's side of the sync badge.
type Status struct {
	Unpushed      int
	LastSync      time.Time
	UnpushedSince time.Time
	Err           error
}

// Status reports unpushed commits and the last sync outcome.
func (b *Backend) Status() Status {
	n, _ := b.repo.Unpushed()
	if b.remote == nil {
		n = 0
	}
	b.smu.Lock()
	defer b.smu.Unlock()
	st := Status{Unpushed: n, LastSync: b.lastSync, Err: b.lastErr}
	if n > 0 {
		st.UnpushedSince = b.unpushedSince
	}
	return st
}

func (b *Backend) ok() {
	b.smu.Lock()
	b.lastSync = time.Now()
	b.lastErr = nil
	if n, err := b.repo.Unpushed(); err == nil && n == 0 {
		b.unpushedSince = time.Time{}
	}
	b.smu.Unlock()
	b.status()
}

func (b *Backend) fail(err error) {
	b.opts.Log.Error("git sync", "err", err)
	b.smu.Lock()
	b.lastErr = err
	b.smu.Unlock()
	b.status()
}

func (b *Backend) status() {
	if b.OnStatus != nil {
		b.OnStatus()
	}
}
