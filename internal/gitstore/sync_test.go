package gitstore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Sync against real bare repositories over file:// — go-git runs the git
// binaries for that transport, so shallow clones, the hand-driven
// upload-pack and pushes from shallow clones are exercised as against a
// forge. Every remote lives in t.TempDir().

// newRemote creates an empty bare repository and returns it as a Remote.
func newRemote(t *testing.T) (Remote, *git.Repository) {
	t.Helper()
	if _, err := exec.LookPath("git-upload-pack"); err != nil {
		if os.Getenv("AENOTES_TEST_REQUIRE_GIT") != "" {
			t.Fatal("git-upload-pack not found")
		}
		t.Skip("file:// transport needs the git binaries")
	}
	dir := filepath.Join(t.TempDir(), "remote.git")
	repo, err := git.PlainInit(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	return Remote{URL: "file://" + dir}, repo
}

func remoteHead(t *testing.T, repo *git.Repository) plumbing.Hash {
	t.Helper()
	ref, err := repo.Reference(plumbing.NewBranchReferenceName("main"), false)
	if err != nil {
		t.Fatalf("remote main: %v", err)
	}
	return ref.Hash()
}

// seed pushes a history of n commits (files f0..fn-1) and returns them.
func seed(t *testing.T, remote Remote, n int) []plumbing.Hash {
	t.Helper()
	r := newRepo(t)
	var hs []plumbing.Hash
	for i := range n {
		hs = append(hs, commit(t, r, "seed", file("f"+string(rune('0'+i)), "v")))
	}
	if err := r.Push(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	return hs
}

func openDir(t *testing.T, dir string, remote Remote) (*Repo, bool) {
	t.Helper()
	r, created, err := OpenDir(context.Background(), dir, remote, testOpts)
	if err != nil {
		t.Fatal(err)
	}
	return r, created
}

func TestCloneEmptyRemote(t *testing.T) {
	remote, _ := newRemote(t)
	r, err := Clone(context.Background(), memory.NewStorage(), remote, testOpts, 1)
	if !errors.Is(err, ErrEmptyRepository) || r != nil {
		t.Fatalf("clone of empty = %v, %v; want ErrEmptyRepository", r, err)
	}
}

// A remote with commits on another branch only is empty for us.
func TestCloneRemoteWithoutBranch(t *testing.T) {
	remote, _ := newRemote(t)
	other := Options{Committer: testOpts.Committer, Branch: "refs/heads/other"}
	src, _ := Init(memory.NewStorage(), other)
	if _, err := src.Commit(Action{Summary: "s"}, []FileWrite{file("x", "1")}); err != nil {
		t.Fatal(err)
	}
	if err := src.Push(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	if _, err := Clone(context.Background(), memory.NewStorage(), remote, testOpts, 1); !errors.Is(err, ErrEmptyRepository) {
		t.Fatalf("clone = %v, want ErrEmptyRepository", err)
	}
	if _, _, err := src.Fetch(context.Background(), remote); err != nil {
		t.Fatalf("other branch fetch = %v", err)
	}
	main := newRepo(t)
	if _, _, err := main.Fetch(context.Background(), remote); !errors.Is(err, ErrNoRemoteTip) {
		t.Fatalf("fetch = %v, want ErrNoRemoteTip", err)
	}
}

// Depth 1 is the tip only, marked shallow; depth 0 is the whole history.
func TestCloneDepth(t *testing.T) {
	remote, _ := newRemote(t)
	hs := seed(t, remote, 3)
	shallow, err := Clone(context.Background(), memory.NewStorage(), remote, testOpts, 1)
	if err != nil {
		t.Fatal(err)
	}
	if shallow.Head() != hs[2] || shallow.RemoteTip() != hs[2] {
		t.Fatalf("head %v tip %v, want %v", shallow.Head(), shallow.RemoteTip(), hs[2])
	}
	if sh, _ := shallow.s.Shallow(); !reflect.DeepEqual(sh, []plumbing.Hash{hs[2]}) {
		t.Fatalf("shallow = %v", sh)
	}
	if shallow.has(hs[1]) {
		t.Fatal("depth 1 fetched the parent")
	}
	if b, err := shallow.ReadFile("f0"); err != nil || string(b) != "v" {
		t.Fatalf("f0 = %q, %v", b, err)
	}
	full, err := Clone(context.Background(), memory.NewStorage(), remote, testOpts, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := full.IsAncestor(hs[0], hs[2]); err != nil || !ok {
		t.Fatalf("full clone ancestry = %v, %v", ok, err)
	}
	if n, _ := full.Unpushed(); n != 0 {
		t.Fatalf("fresh clone unpushed = %d", n)
	}
}

// IsAncestor answers false, not an error, when the answer lies beyond a
// shallow boundary.
func TestIsAncestorAcrossShallowBoundary(t *testing.T) {
	remote, _ := newRemote(t)
	hs := seed(t, remote, 3)
	r, err := Clone(context.Background(), memory.NewStorage(), remote, testOpts, 1)
	if err != nil {
		t.Fatal(err)
	}
	local := commit(t, r, "local", file("x", "1"))
	if ok, err := r.IsAncestor(hs[2], local); err != nil || !ok {
		t.Fatalf("fast-forward = %v, %v; want true", ok, err)
	}
	for _, h := range hs[:2] {
		if ok, err := r.IsAncestor(h, local); err != nil || ok {
			t.Fatalf("beyond the boundary = %v, %v; want false, nil", ok, err)
		}
	}
	// Even with the parent object present, the walk stops at the boundary.
	if err := r.s.SetShallow([]plumbing.Hash{local}); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.IsAncestor(hs[2], local); err != nil || ok {
		t.Fatalf("boundary at local = %v, %v; want false, nil", ok, err)
	}
}

// An empty remote gives an empty repository; its first push creates the
// branch on the remote.
func TestOpenDirEmptyRemoteThenPushCreatesBranch(t *testing.T) {
	remote, bare := newRemote(t)
	r, created := openDir(t, filepath.Join(t.TempDir(), "clone"), remote)
	if !created || !r.Head().IsZero() {
		t.Fatalf("created = %v, head %v", created, r.Head())
	}
	if n, _ := r.Unpushed(); n != 0 {
		t.Fatalf("unpushed on unborn = %d", n)
	}
	if err := r.Push(context.Background(), remote); err == nil {
		t.Fatal("pushing an unborn branch succeeded")
	}
	h := commit(t, r, "init board", file("board.json", "{}"))
	if n, _ := r.Unpushed(); n != 1 {
		t.Fatalf("unpushed = %d, want 1", n)
	}
	if err := r.Push(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	if got := remoteHead(t, bare); got != h {
		t.Fatalf("remote main = %v, want %v", got, h)
	}
	if r.RemoteTip() != h {
		t.Fatal("tracking ref not moved by push")
	}
	if n, _ := r.Unpushed(); n != 0 {
		t.Fatalf("unpushed after push = %d", n)
	}
	if err := r.Push(context.Background(), remote); err != nil {
		t.Fatalf("up-to-date push = %v", err)
	}
}

// Reopening a directory keeps what was committed but never pushed, and
// does not go to the remote at all.
func TestOpenDirReopenKeepsUnpushed(t *testing.T) {
	remote, _ := newRemote(t)
	hs := seed(t, remote, 2)
	dir := filepath.Join(t.TempDir(), "clone")
	r, created := openDir(t, dir, remote)
	if created || r.Head() != hs[1] {
		t.Fatalf("clone: created %v head %v", created, r.Head())
	}
	local := commit(t, r, "local", file("x", "1"))

	again, created := openDir(t, dir, Remote{URL: "file:///nonexistent/never/used"})
	if created || again.Head() != local || again.RemoteTip() != hs[1] {
		t.Fatalf("reopen: created %v head %v tip %v", created, again.Head(), again.RemoteTip())
	}
	if n, err := again.Unpushed(); err != nil || n != 1 {
		t.Fatalf("unpushed = %d, %v; want 1", n, err)
	}
	if b, _ := again.ReadFile("x"); string(b) != "1" {
		t.Fatalf("x = %q", b)
	}
}

// A directory initialised against an empty remote takes the remote's state
// on the next open once someone else has pushed — but only while it is
// still unborn.
func TestOpenDirUnbornTakesRemoteLater(t *testing.T) {
	remote, _ := newRemote(t)
	dir := filepath.Join(t.TempDir(), "clone")
	if _, created := openDir(t, dir, remote); !created {
		t.Fatal("want created")
	}
	hs := seed(t, remote, 1)
	r, created := openDir(t, dir, remote)
	if created || r.Head() != hs[0] {
		t.Fatalf("created %v head %v, want %v", created, r.Head(), hs[0])
	}
}

func TestOpenDirBadRemote(t *testing.T) {
	_, _, err := OpenDir(context.Background(), filepath.Join(t.TempDir(), "c"), Remote{URL: "file://" + filepath.Join(t.TempDir(), "missing.git")}, testOpts)
	if err == nil || errors.Is(err, ErrNoRemoteTip) {
		t.Fatalf("missing remote = %v, want a hard error", err)
	}
}

// Two replicas of one remote: B diverges, its push is rejected, it fetches
// and merges, and A receives the merge with both parents.
func TestTwoReplicasRejectFetchMerge(t *testing.T) {
	ctx := context.Background()
	remote, bare := newRemote(t)
	base := seed(t, remote, 1)[0]
	a, _ := openDir(t, filepath.Join(t.TempDir(), "a"), remote)
	b, _ := openDir(t, filepath.Join(t.TempDir(), "b"), remote)

	aHead := commit(t, a, "a edits", file("board", "A"))
	if err := a.Push(ctx, remote); err != nil {
		t.Fatal(err)
	}
	bHead := commit(t, b, "b edits", file("board", "B"), file("b-only", "b"))
	bHead2 := commit(t, b, "b edits more", file("b-only", "bb"))
	if n, _ := b.Unpushed(); n != 2 {
		t.Fatalf("b unpushed = %d, want 2", n)
	}

	err := b.Push(ctx, remote)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("b push = %v, want ErrRejected", err)
	}
	if remoteHead(t, bare) != aHead {
		t.Fatal("a rejected push moved the remote")
	}
	if b.RemoteTip() != base {
		t.Fatal("a rejected push moved the tracking ref")
	}

	tip, moved, err := b.Fetch(ctx, remote)
	if err != nil || !moved || tip != aHead {
		t.Fatalf("b fetch = %v, %v, %v; want a's head, moved", tip, moved, err)
	}
	if ok, err := b.IsAncestor(tip, bHead2); err != nil || ok {
		t.Fatalf("diverged: IsAncestor = %v, %v", ok, err)
	}
	if paths, _ := b.ChangedPaths(tip, bHead2); !reflect.DeepEqual(paths, []string{"b-only", "board"}) {
		t.Fatalf("changed = %v", paths)
	}
	m, err := b.Merge(tip, Action{Actor: "timur", Summary: "merge"}, []FileWrite{file("board", "AB"), file("b-only", "bb")})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := b.Unpushed(); n != 3 {
		t.Fatalf("b unpushed after merge = %d, want 3 (two edits and the merge)", n)
	}
	if err := b.Push(ctx, remote); err != nil {
		t.Fatalf("b push after merge = %v", err)
	}
	if remoteHead(t, bare) != m {
		t.Fatal("the remote is not at the merge")
	}
	if n, _ := b.Unpushed(); n != 0 {
		t.Fatalf("b unpushed after push = %d", n)
	}

	tip, moved, err = a.Fetch(ctx, remote)
	if err != nil || !moved || tip != m {
		t.Fatalf("a fetch = %v, %v, %v; want the merge", tip, moved, err)
	}
	mc := getCommit(t, a, tip)
	if !reflect.DeepEqual(mc.ParentHashes, []plumbing.Hash{bHead2, aHead}) {
		t.Fatalf("merge parents = %v, want [b's head, a's head]", mc.ParentHashes)
	}
	if !a.has(bHead) {
		t.Fatal("a did not receive b's first commit")
	}
	if ok, _ := a.IsAncestor(a.Head(), tip); !ok {
		t.Fatal("a's head must be an ancestor of the merge")
	}
	if err := a.ResetTo(tip); err != nil {
		t.Fatal(err)
	}
	if bs, _ := a.ReadFile("board"); string(bs) != "AB" {
		t.Fatalf("a board = %q", bs)
	}
	if n, _ := a.Unpushed(); n != 0 {
		t.Fatalf("a unpushed after adopting = %d", n)
	}

	// Nothing new: not moved, no transfer.
	if _, moved, err := a.Fetch(ctx, remote); err != nil || moved {
		t.Fatalf("idle fetch = %v, %v", moved, err)
	}
}

// Push errors that are not a rejection are not reported as one.
func TestPushToMissingRemoteIsNotRejected(t *testing.T) {
	r := newRepo(t)
	commit(t, r, "x", file("x", "1"))
	err := r.Push(context.Background(), Remote{URL: "file://" + filepath.Join(t.TempDir(), "missing.git")})
	if err == nil || errors.Is(err, ErrRejected) {
		t.Fatalf("push = %v, want a non-rejection error", err)
	}
}

// Maintain packs the loose objects; the repository reads the same after.
func TestMaintain(t *testing.T) {
	remote, _ := newRemote(t)
	r, _ := openDir(t, filepath.Join(t.TempDir(), "c"), remote)
	var last plumbing.Hash
	for i := range 5 {
		last = commit(t, r, "c", file("f", string(rune('a'+i))))
	}
	if err := r.Maintain(); err != nil {
		t.Fatal(err)
	}
	if r.Head() != last {
		t.Fatal("head moved")
	}
	if b, err := r.ReadFile("f"); err != nil || string(b) != "e" {
		t.Fatalf("f = %q, %v", b, err)
	}
	if err := r.Push(context.Background(), remote); err != nil {
		t.Fatalf("push after maintain = %v", err)
	}
	if err := newRepo(t).Maintain(); err != nil {
		t.Fatalf("in-memory maintain = %v", err)
	}
}
