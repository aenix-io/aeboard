package gitbackend

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/reearth/ygo/crdt"

	"github.com/aenix-io/aeboard/internal/boards"
	"github.com/aenix-io/aeboard/internal/gitstore"
)

// replica is one aeboard server: its own clone on disk, store and backend.
type replica struct {
	dir     string
	backend *Backend
	store   *boards.Store
	// gained collects what OnRemote handed the store (live-room updates).
	gained map[string][]byte
}

func newRemote(t *testing.T) gitstore.Remote {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatal(err)
	}
	return gitstore.Remote{URL: "file://" + dir}
}

func newReplica(t *testing.T, remote gitstore.Remote, dir string) *replica {
	t.Helper()
	ctx := context.Background()
	repo, _, err := gitstore.OpenDir(ctx, dir, remote, gitstore.Options{
		Committer: gitstore.Identity{Name: "aeboard", Email: "aeboard@localhost"},
		Branch:    plumbing.NewBranchReferenceName("test/boards"),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &replica{dir: dir, gained: map[string][]byte{}}
	r.backend = New(repo, &remote, Options{})
	r.store, err = boards.Open(ctx, r.backend)
	if err != nil {
		t.Fatal(err)
	}
	r.backend.OnRemote = func(_ context.Context, remotes []boards.Remote) {
		g, err := r.store.MergeRemote(remotes)
		if err != nil {
			t.Error(err)
		}
		for id, u := range g {
			r.gained[id] = u
		}
	}
	return r
}

func (r *replica) edit(t *testing.T, id string, client crdt.ClientID, key string) {
	t.Helper()
	st, err := r.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	doc := crdt.New(crdt.WithClientID(client))
	_ = crdt.ApplyUpdateV1(doc, st, nil)
	sv := doc.StateVector()
	m := doc.GetMap("blocks")
	doc.Transact(func(txn *crdt.Transaction) { m.Set(txn, key, "v") })
	if _, err := r.store.ApplyUpdate(id, crdt.EncodeStateAsUpdateV1(doc, sv), "user-"+key); err != nil {
		t.Fatal(err)
	}
}

func (r *replica) keys(t *testing.T, id string) []string {
	t.Helper()
	st, err := r.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	doc := crdt.New()
	_ = crdt.ApplyUpdateV1(doc, st, nil)
	raw, _ := doc.GetMap("blocks").ToJSON()
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (r *replica) flushAndSync(t *testing.T) {
	t.Helper()
	if err := r.store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.backend.SyncOnce(context.Background())
	if st := r.backend.Status(); st.Err != nil {
		t.Fatalf("sync: %v", st.Err)
	}
}

func TestBoardsReachTheOtherReplica(t *testing.T) {
	remote := newRemote(t)
	a := newReplica(t, remote, t.TempDir())
	b := newReplica(t, remote, t.TempDir())

	m, _ := a.store.Create(boards.CreateInput{Title: "Roadmap", Tags: []string{"q4"}}, "alice")
	a.edit(t, m.ID, 11, "from-a")
	a.flushAndSync(t)
	if st := a.backend.Status(); st.Unpushed != 0 {
		t.Fatalf("unpushed after sync = %d", st.Unpushed)
	}

	b.backend.SyncOnce(context.Background())
	got, err := b.store.Get(m.ID)
	if err != nil || got.Title != "Roadmap" {
		t.Fatalf("B does not see the board: %+v %v", got, err)
	}
	if !slices.Contains(b.keys(t, m.ID), "from-a") {
		t.Fatalf("B lacks A's edit: %v", b.keys(t, m.ID))
	}
}

// Both replicas edit the same board between syncs; B's push is rejected,
// B merges the two Yjs states and pushes a merge commit; after A syncs,
// both have both edits — and A's live rooms are handed B's edit.
func TestConcurrentEditsOfOneBoardMerge(t *testing.T) {
	remote := newRemote(t)
	a := newReplica(t, remote, t.TempDir())
	m, _ := a.store.Create(boards.CreateInput{Title: "Shared", Tags: nil}, "alice")
	a.flushAndSync(t)
	b := newReplica(t, remote, t.TempDir())
	b.backend.SyncOnce(context.Background())

	a.edit(t, m.ID, 11, "from-a")
	b.edit(t, m.ID, 12, "from-b")
	a.flushAndSync(t)
	b.flushAndSync(t) // rejected, merged, pushed

	a.backend.SyncOnce(context.Background())
	for name, r := range map[string]*replica{"A": a, "B": b} {
		if k := r.keys(t, m.ID); !slices.Contains(k, "from-a") || !slices.Contains(k, "from-b") {
			t.Fatalf("%s: %v", name, k)
		}
	}
	if _, ok := a.gained[m.ID]; !ok {
		t.Fatalf("A's rooms were not handed B's edit")
	}
	if st := b.backend.Status(); st.Unpushed != 0 {
		t.Fatalf("B unpushed = %d", st.Unpushed)
	}
}

// Different boards edited on each side merge without touching each other.
func TestConcurrentEditsOfDifferentBoards(t *testing.T) {
	remote := newRemote(t)
	a := newReplica(t, remote, t.TempDir())
	b := newReplica(t, remote, t.TempDir())
	ma, _ := a.store.Create(boards.CreateInput{Title: "A's", Tags: nil}, "")
	mb, _ := b.store.Create(boards.CreateInput{Title: "B's", Tags: nil}, "")
	a.flushAndSync(t)
	b.flushAndSync(t)
	a.backend.SyncOnce(context.Background())
	for _, r := range []*replica{a, b} {
		if len(r.store.List()) != 2 {
			t.Fatalf("boards = %+v", r.store.List())
		}
	}
	_, _ = ma, mb
}

// A restart keeps commits that never reached the remote, and pushes them.
func TestUnpushedCommitsSurviveRestart(t *testing.T) {
	remote := newRemote(t)
	dir := t.TempDir()
	a := newReplica(t, remote, dir)
	m, _ := a.store.Create(boards.CreateInput{Title: "Offline", Tags: nil}, "")
	if err := a.store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := a.backend.Status(); st.Unpushed != 1 {
		t.Fatalf("unpushed = %d", st.Unpushed)
	}

	re := newReplica(t, remote, dir)
	if _, err := re.store.Get(m.ID); err != nil {
		t.Fatalf("board lost on restart: %v", err)
	}
	re.backend.SyncOnce(context.Background())
	if st := re.backend.Status(); st.Unpushed != 0 || st.Err != nil {
		t.Fatalf("after restart sync: %+v", st)
	}
	fresh := newReplica(t, remote, t.TempDir())
	if _, err := fresh.store.Get(m.ID); err != nil {
		t.Fatalf("remote lacks the board: %v", err)
	}
}

func TestCommitsAreAttributed(t *testing.T) {
	remote := newRemote(t)
	a := newReplica(t, remote, t.TempDir())
	m, _ := a.store.Create(boards.CreateInput{Title: "b", Tags: nil}, "alice")
	a.edit(t, m.ID, 3, "x") // by user-x
	_ = a.store.Flush(context.Background())
	c, err := a.backend.repo.CommitObject(a.backend.repo.Head())
	if err != nil {
		t.Fatal(err)
	}
	if c.Author.Name != "alice" || !containsLine(c.Message, "Aeboard-Editors: alice user-x") || !containsLine(c.Message, "Aeboard-Board: "+m.ID) {
		t.Fatalf("commit author=%q message=%q", c.Author.Name, c.Message)
	}
}

func containsLine(msg, line string) bool {
	for _, l := range splitLines(msg) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
