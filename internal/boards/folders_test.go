package boards

import (
	"context"
	"errors"
	"testing"
	"time"
)

func folderNames(fs []Folder) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

func TestFoldersCreateMoveAndSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	s := open(t, DirBackend{Root: dir})
	work, err := s.CreateFolder(" Work ", "")
	if err != nil || work.Name != "Work" {
		t.Fatalf("create = %+v %v", work, err)
	}
	meetings, _ := s.CreateFolder("Meetings", work.ID)
	doc, err := s.Create(CreateInput{Title: "Notes", Folder: meetings.ID}, "")
	if err != nil || doc.Folder != meetings.ID {
		t.Fatalf("create in folder = %+v %v", doc, err)
	}
	if s.Pending() != 2 { // the document and the folder tree
		t.Fatalf("pending = %d", s.Pending())
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	re := open(t, DirBackend{Root: dir})
	if got := folderNames(re.Folders()); len(got) != 2 {
		t.Fatalf("folders after restart = %v", got)
	}
	if m, _ := re.Get(doc.ID); m.Folder != meetings.ID {
		t.Fatalf("document folder after restart = %q", m.Folder)
	}
}

func TestFolderRules(t *testing.T) {
	s := open(t, &countingBackend{})
	a, _ := s.CreateFolder("A", "")
	b, _ := s.CreateFolder("B", a.ID)
	// Names are required; parents must exist.
	if _, err := s.CreateFolder("  ", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name: %v", err)
	}
	if _, err := s.CreateFolder("X", "nosuchfold"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing parent: %v", err)
	}
	if _, err := s.Create(CreateInput{Folder: "nosuchfold"}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("document into a missing folder: %v", err)
	}
	// A folder cannot move into itself or below itself.
	for _, into := range []string{a.ID, b.ID} {
		if _, err := s.UpdateFolder(a.ID, FolderPatch{Parent: &into}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("move A into %s: %v", into, err)
		}
	}
	root := ""
	if f, err := s.UpdateFolder(b.ID, FolderPatch{Parent: &root}); err != nil || f.Parent != "" {
		t.Fatalf("move B to the top: %+v %v", f, err)
	}
	// Only an empty folder goes.
	doc, _ := s.Create(CreateInput{Folder: a.ID}, "")
	if err := s.DeleteFolder(a.ID); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("delete with a document: %v", err)
	}
	_, _, _ = s.Update(doc.ID, Patch{Folder: &root}, "")
	if err := s.DeleteFolder(a.ID); err != nil {
		t.Fatal(err)
	}
	if got := folderNames(s.Folders()); len(got) != 1 || got[0] != "B" {
		t.Fatalf("folders = %v", got)
	}
	if _, err := s.UpdateFolder(a.ID, FolderPatch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update a deleted folder: %v", err)
	}
}

// Two replicas changed the tree: per folder the later change wins, and a
// deletion (a tombstone) is not undone by a replica that still had the
// folder. A document left in a folder deleted elsewhere shows at the top.
func TestMergeFolders(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	ours := []Folder{
		{ID: "aaaaaaaaaa", Name: "Ours renamed", Updated: t0.Add(2 * time.Minute)},
		{ID: "bbbbbbbbbb", Name: "Kept", Updated: t0},
	}
	theirs := []Folder{
		{ID: "aaaaaaaaaa", Name: "Theirs older", Updated: t0.Add(time.Minute)},
		{ID: "bbbbbbbbbb", Name: "Kept", Updated: t0.Add(3 * time.Minute), Deleted: true},
		{ID: "cccccccccc", Name: "New there", Updated: t0},
	}
	merged, changed := MergeFolders(ours, theirs)
	if !changed || len(merged) != 3 {
		t.Fatalf("merged = %+v changed=%v", merged, changed)
	}
	byID := map[string]Folder{}
	for _, f := range merged {
		byID[f.ID] = f
	}
	if byID["aaaaaaaaaa"].Name != "Ours renamed" || !byID["bbbbbbbbbb"].Deleted || byID["cccccccccc"].Name != "New there" {
		t.Fatalf("merged = %+v", byID)
	}
	if _, again := MergeFolders(merged, theirs); again {
		t.Fatal("merging the same remote twice changed something")
	}

	s := open(t, &countingBackend{})
	f, _ := s.CreateFolder("Gone elsewhere", "")
	doc, _ := s.Create(CreateInput{Folder: f.ID}, "")
	gone := f
	gone.Deleted = true
	gone.Updated = f.Updated.Add(time.Hour)
	s.MergeRemoteFolders([]Folder{gone})
	m, _ := s.Get(doc.ID)
	if len(s.Folders()) != 0 || s.FolderOf(m) != "" {
		t.Fatalf("folders = %v, shows in %q", s.Folders(), s.FolderOf(m))
	}
}

func TestFoldersFileRoundTrip(t *testing.T) {
	in := []Folder{{ID: "bbbbbbbbbb", Name: "B"}, {ID: "aaaaaaaaaa", Name: "A", Parent: "bbbbbbbbbb", Deleted: true}}
	data, err := EncodeFolders(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeFolders(data)
	if err != nil || len(out) != 2 || out[0].ID != "aaaaaaaaaa" || !out[0].Deleted {
		t.Fatalf("round trip = %+v %v\n%s", out, err, data)
	}
	if none, err := DecodeFolders(nil); err != nil || none != nil {
		t.Fatalf("no file = %v %v", none, err)
	}
}
