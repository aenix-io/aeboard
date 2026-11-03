package gitstore

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// The commit path: one action, one commit, written through the object
// store. These tests pin what a commit holds and when there is none.

var testOpts = Options{Committer: Identity{Name: "aeboard", Email: "aeboard@localhost"}}

func newRepo(t *testing.T) *Repo {
	t.Helper()
	r, err := Init(memory.NewStorage(), testOpts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func commit(t *testing.T, r *Repo, summary string, writes ...FileWrite) plumbing.Hash {
	t.Helper()
	h, err := r.Commit(Action{Summary: summary}, writes)
	if err != nil {
		t.Fatal(err)
	}
	if h.IsZero() {
		t.Fatalf("%s: no commit made", summary)
	}
	return h
}

func file(p, data string) FileWrite { return FileWrite{Path: p, Data: []byte(data)} }

func del(p string) FileWrite { return FileWrite{Path: p} }

func getCommit(t *testing.T, r *Repo, h plumbing.Hash) *object.Commit {
	t.Helper()
	c, err := object.GetCommit(r.s, h)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHeadIsZeroWhenUnborn(t *testing.T) {
	r := newRepo(t)
	if !r.Head().IsZero() {
		t.Fatal("a new repository has no head")
	}
	if r.Branch() != "refs/heads/main" {
		t.Fatalf("default branch = %s", r.Branch())
	}
	if _, err := r.ReadFile("x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read on unborn = %v, want ErrNotFound", err)
	}
}

// Writing the same bytes again, deleting a missing file, or deleting under
// a missing directory changes nothing and makes no commit.
func TestCommitNoOpMakesNoCommit(t *testing.T) {
	r := newRepo(t)
	if h, err := r.Commit(Action{Summary: "empty"}, nil); err != nil || !h.IsZero() {
		t.Fatalf("empty commit on unborn = %v, %v", h, err)
	}
	first := commit(t, r, "first", file("boards/a/state.bin", "v1"))
	for name, writes := range map[string][]FileWrite{
		"same bytes":         {file("boards/a/state.bin", "v1")},
		"delete missing":     {del("boards/a/other.bin")},
		"delete missing dir": {del("nope/x")},
		"nothing":            nil,
	} {
		h, err := r.Commit(Action{Summary: name}, writes)
		if err != nil || !h.IsZero() {
			t.Fatalf("%s: got %v, %v; want zero hash", name, h, err)
		}
		if r.Head() != first {
			t.Fatalf("%s: head moved", name)
		}
	}
}

// Deleting the last file of a directory removes the directory, up the
// chain; deleting the last file of the repository leaves an empty tree.
func TestDeleteRemovesEmptyDirs(t *testing.T) {
	r := newRepo(t)
	commit(t, r, "add", file("a/b/c.txt", "c"), file("a/keep.txt", "k"))
	h := commit(t, r, "rm c", del("a/b/c.txt"))
	if got, _ := r.ListDir(h, "a"); !reflect.DeepEqual(got, []string{"keep.txt"}) {
		t.Fatalf("a/ = %v, want [keep.txt]", got)
	}
	h = commit(t, r, "rm keep", del("a/keep.txt"))
	if got, _ := r.ListDir(h, ""); len(got) != 0 {
		t.Fatalf("root = %v, want empty", got)
	}
	tree, err := getCommit(t, r, h).Tree()
	if err != nil || len(tree.Entries) != 0 {
		t.Fatalf("tree = %v, %v; want the empty tree", tree, err)
	}
}

func TestCommitRejectsBadPaths(t *testing.T) {
	r := newRepo(t)
	commit(t, r, "add", file("a/b.txt", "b"))
	for _, p := range []string{"", ".", "/abs", "../up", "a/b.txt/c"} {
		if _, err := r.Commit(Action{Summary: "bad"}, []FileWrite{file(p, "x")}); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
	if _, err := r.Commit(Action{Summary: "dir"}, []FileWrite{file("a", "x")}); err == nil {
		t.Error("overwriting a directory with a file accepted")
	}
}

// The actor is the author, the server the committer; trailers follow the
// summary after a blank line, sorted.
func TestAuthorCommitterAndTrailers(t *testing.T) {
	r := newRepo(t)
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	h, err := r.Commit(Action{
		Actor:    "kvaps",
		Summary:  "move sticker",
		At:       at,
		Trailers: map[string]string{"Aeboard-Board": "b1", "Aeboard-Action": "move"},
	}, []FileWrite{file("x", "1")})
	if err != nil {
		t.Fatal(err)
	}
	c := getCommit(t, r, h)
	if c.Author.Name != "kvaps" || c.Author.Email != "kvaps@users.noreply.github.com" {
		t.Fatalf("author = %v", c.Author)
	}
	if c.Committer.Name != "aeboard" || c.Committer.Email != "aeboard@localhost" || !c.Committer.When.Equal(at) {
		t.Fatalf("committer = %v", c.Committer)
	}
	want := "move sticker\n\nAeboard-Action: move\nAeboard-Board: b1\n"
	if c.Message != want {
		t.Fatalf("message = %q, want %q", c.Message, want)
	}

	// Unattributed: the committer is the author; no trailers, no blank line.
	h = commit(t, r, "sweep", file("x", "2"))
	c = getCommit(t, r, h)
	if c.Author.Name != "aeboard" || c.Message != "sweep\n" || len(c.ParentHashes) != 1 {
		t.Fatalf("unattributed commit = %v %q %v", c.Author, c.Message, c.ParentHashes)
	}

	// A custom email template.
	o := testOpts
	o.AuthorEmail = func(l string) string { return l + "@example.org" }
	r2, _ := Init(memory.NewStorage(), o)
	h, _ = r2.Commit(Action{Actor: "timur", Summary: "s"}, []FileWrite{file("x", "1")})
	if c := getCommit(t, r2, h); c.Author.Email != "timur@example.org" {
		t.Fatalf("author email = %s", c.Author.Email)
	}
}

func TestBadTrailersAndSummaryRefused(t *testing.T) {
	r := newRepo(t)
	for name, a := range map[string]Action{
		"foreign key":   {Summary: "s", Trailers: map[string]string{"Signed-off-by": "x"}},
		"bare prefix":   {Summary: "s", Trailers: map[string]string{"Aeboard-": "x"}},
		"space in key":  {Summary: "s", Trailers: map[string]string{"Aeboard-A B": "x"}},
		"newline value": {Summary: "s", Trailers: map[string]string{"Aeboard-A": "x\ny"}},
		"two lines":     {Summary: "s\nmore"},
	} {
		if _, err := r.Commit(a, []FileWrite{file("x", "1")}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if !r.Head().IsZero() {
		t.Fatal("a refused action committed")
	}
}

func TestListDirAndReadFileAt(t *testing.T) {
	r := newRepo(t)
	c1 := commit(t, r, "1", file("boards/b1/state.bin", "s1"), file("boards/b1/meta.json", "{}"), file("README", "r"))
	c2 := commit(t, r, "2", file("boards/b1/state.bin", "s2"), file("boards/b2/state.bin", "x"))

	for _, tc := range []struct {
		commit plumbing.Hash
		dir    string
		want   []string
	}{
		{c1, "", []string{"README", "boards"}},
		{c1, "boards", []string{"b1"}},
		{c2, "boards", []string{"b1", "b2"}},
		{c2, "boards/", []string{"b1", "b2"}},
		{c2, "/boards", []string{"b1", "b2"}},
		{c1, "boards/b1", []string{"meta.json", "state.bin"}},
		{c1, "boards/b2", nil},
		{c1, "missing/deep", nil},
		{c1, "README", nil},
		{plumbing.ZeroHash, "", nil},
	} {
		got, err := r.ListDir(tc.commit, tc.dir)
		if err != nil {
			t.Fatalf("ListDir(%s): %v", tc.dir, err)
		}
		if len(got) != len(tc.want) || (len(got) > 0 && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("ListDir(%.7s, %q) = %v, want %v", tc.commit, tc.dir, got, tc.want)
		}
	}

	if b, err := r.ReadFileAt(c1, "boards/b1/state.bin"); err != nil || string(b) != "s1" {
		t.Fatalf("at c1 = %q, %v", b, err)
	}
	if b, err := r.ReadFile("boards/b1/state.bin"); err != nil || string(b) != "s2" {
		t.Fatalf("at head = %q, %v", b, err)
	}
	for _, p := range []string{"boards/b2/state.bin", "boards", "nope"} {
		if _, err := r.ReadFileAt(c1, p); !errors.Is(err, ErrNotFound) {
			t.Errorf("ReadFileAt(c1, %q) = %v, want ErrNotFound", p, err)
		}
	}
	if _, err := r.ReadFileAt(plumbing.ZeroHash, "README"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadFileAt(zero) = %v, want ErrNotFound", err)
	}
}

func TestChangedPaths(t *testing.T) {
	r := newRepo(t)
	c1 := commit(t, r, "1", file("a/x", "1"), file("b", "1"))
	c2 := commit(t, r, "2", file("a/x", "2"), del("b"), file("c/d", "1"))
	got, err := r.ChangedPaths(c1, c2)
	if err != nil || !reflect.DeepEqual(got, []string{"a/x", "b", "c/d"}) {
		t.Fatalf("c1..c2 = %v, %v", got, err)
	}
	got, err = r.ChangedPaths(plumbing.ZeroHash, c1)
	if err != nil || !reflect.DeepEqual(got, []string{"a/x", "b"}) {
		t.Fatalf("zero..c1 = %v, %v", got, err)
	}
	if got, _ := r.ChangedPaths(c2, c2); len(got) != 0 {
		t.Fatalf("c2..c2 = %v", got)
	}
}

func TestIsAncestorLocal(t *testing.T) {
	r := newRepo(t)
	c1 := commit(t, r, "1", file("x", "1"))
	c2 := commit(t, r, "2", file("x", "2"))
	for _, tc := range []struct {
		a, b plumbing.Hash
		want bool
	}{
		{c1, c2, true}, {c2, c1, false}, {c2, c2, true},
		{plumbing.ZeroHash, c1, true}, {c1, plumbing.ZeroHash, false},
		{plumbing.NewHash(strings.Repeat("ab", 20)), c2, false},
	} {
		got, err := r.IsAncestor(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Errorf("IsAncestor(%.7s, %.7s) = %v, %v; want %v", tc.a, tc.b, got, err, tc.want)
		}
	}
}

func TestResetTo(t *testing.T) {
	r := newRepo(t)
	c1 := commit(t, r, "1", file("x", "1"))
	commit(t, r, "2", file("x", "2"))
	if err := r.ResetTo(c1); err != nil || r.Head() != c1 {
		t.Fatalf("reset = %v, head %v", err, r.Head())
	}
	if err := r.ResetTo(plumbing.NewHash(strings.Repeat("ab", 20))); err == nil {
		t.Fatal("reset to a missing commit accepted")
	}
}

// Merge's tree is theirs plus the writes; its parents are ours and theirs.
func TestMergeTreeIsTheirsPlusWrites(t *testing.T) {
	r := newRepo(t)
	base := commit(t, r, "base", file("shared", "0"), file("ours", "0"))
	theirs := commit(t, r, "theirs", file("shared", "T"), file("theirs-only", "t"))
	if err := r.ResetTo(base); err != nil {
		t.Fatal(err)
	}
	ours := commit(t, r, "ours", file("shared", "O"), file("ours", "1"))

	m, err := r.Merge(theirs, Action{Actor: "kvaps", Summary: "merge"}, []FileWrite{file("shared", "OT"), file("ours", "1")})
	if err != nil {
		t.Fatal(err)
	}
	c := getCommit(t, r, m)
	if !reflect.DeepEqual(c.ParentHashes, []plumbing.Hash{ours, theirs}) {
		t.Fatalf("parents = %v, want [ours theirs]", c.ParentHashes)
	}
	if r.Head() != m {
		t.Fatal("head is not the merge")
	}
	for p, want := range map[string]string{"shared": "OT", "ours": "1", "theirs-only": "t"} {
		if b, err := r.ReadFile(p); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", p, b, err, want)
		}
	}

	// A merge that writes nothing still records both parents: their tree.
	if err := r.ResetTo(ours); err != nil {
		t.Fatal(err)
	}
	m2, err := r.Merge(theirs, Action{Summary: "take theirs"}, nil)
	if err != nil || len(getCommit(t, r, m2).ParentHashes) != 2 {
		t.Fatalf("empty merge = %v, %v", m2, err)
	}
	if b, _ := r.ReadFile("ours"); string(b) != "0" {
		t.Fatalf("empty merge kept ours %q, want their 0", b)
	}
}

// An unborn branch merges onto theirs as a single-parent commit; with no
// writes it just adopts theirs.
func TestMergeHeadZero(t *testing.T) {
	src := newRepo(t)
	theirs := commit(t, src, "theirs", file("a", "1"))
	r := Open(src.s, Options{Committer: testOpts.Committer, Branch: "refs/heads/other"})
	if !r.Head().IsZero() {
		t.Fatal("other branch is unborn")
	}
	m, err := r.Merge(theirs, Action{Summary: "first"}, []FileWrite{file("b", "2")})
	if err != nil {
		t.Fatal(err)
	}
	if c := getCommit(t, r, m); !reflect.DeepEqual(c.ParentHashes, []plumbing.Hash{theirs}) {
		t.Fatalf("parents = %v, want [theirs]", c.ParentHashes)
	}
	if b, _ := r.ReadFile("a"); string(b) != "1" {
		t.Fatalf("a = %q", b)
	}

	r3 := Open(src.s, Options{Committer: testOpts.Committer, Branch: "refs/heads/third"})
	h, err := r3.Merge(theirs, Action{Summary: "adopt"}, nil)
	if err != nil || h != theirs || r3.Head() != theirs {
		t.Fatalf("no-op merge on unborn = %v, %v, head %v; want theirs", h, err, r3.Head())
	}
}

func TestMergeRefusals(t *testing.T) {
	r := newRepo(t)
	c1 := commit(t, r, "1", file("x", "1"))
	commit(t, r, "2", file("x", "2"))
	if _, err := r.Merge(plumbing.ZeroHash, Action{Summary: "m"}, nil); err == nil {
		t.Error("merge of zero accepted")
	}
	if _, err := r.Merge(plumbing.NewHash(strings.Repeat("ab", 20)), Action{Summary: "m"}, nil); err == nil {
		t.Error("merge of a missing commit accepted")
	}
	if _, err := r.Merge(c1, Action{Summary: "m"}, nil); err == nil {
		t.Error("merge of an ancestor accepted")
	}
}

// Theirs ahead of ours: the merge is a commit on theirs alone.
func TestMergeFastForward(t *testing.T) {
	r := newRepo(t)
	c1 := commit(t, r, "1", file("x", "1"))
	c2 := commit(t, r, "2", file("x", "2"))
	if err := r.ResetTo(c1); err != nil {
		t.Fatal(err)
	}
	m, err := r.Merge(c2, Action{Summary: "m"}, []FileWrite{file("y", "1")})
	if err != nil {
		t.Fatal(err)
	}
	if c := getCommit(t, r, m); !reflect.DeepEqual(c.ParentHashes, []plumbing.Hash{c2}) {
		t.Fatalf("parents = %v, want [c2]", c.ParentHashes)
	}
}
