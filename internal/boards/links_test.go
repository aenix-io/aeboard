package boards

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// Linking puts a reference at the end of the parent's body; the catalog
// then lists the child under it, survives a restart, and a second link
// changes nothing.
func TestLinkMakesAChild(t *testing.T) {
	dir := t.TempDir()
	s := open(t, DirBackend{Root: dir})
	parent, _ := s.Create(CreateInput{Mode: ModePage, Title: "Parent"}, "")
	board, _ := s.Create(CreateInput{Mode: ModeEdgeless, Title: "Board"}, "")
	child, _ := s.Create(CreateInput{Mode: ModePage, Title: "Child"}, "")

	v := s.CatalogVersion()
	for _, p := range []string{parent.ID, board.ID} {
		upd, err := s.Link(p, child.ID, "kvaps")
		if err != nil || upd == nil {
			t.Fatalf("link into %s: %v", p, err)
		}
	}
	if s.CatalogVersion() == v {
		t.Fatal("the catalog version did not move")
	}
	if upd, err := s.Link(parent.ID, child.ID, ""); err != nil || upd != nil {
		t.Fatalf("second link: %v, %v", upd, err)
	}
	if _, err := s.Link(child.ID, child.ID, ""); err == nil {
		t.Fatal("a document became its own child")
	}
	links := s.Links()
	if !slices.Equal(links[parent.ID], []string{child.ID}) || !slices.Equal(links[board.ID], []string{child.ID}) || links[child.ID] != nil {
		t.Fatalf("links = %v", links)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := open(t, DirBackend{Root: dir}).Links(); !slices.Equal(got[parent.ID], []string{child.ID}) {
		t.Fatalf("after restart: %v", got)
	}
}

// A trashed document is kept whole and comes back; one deleted for good
// leaves a tombstone that survives a restart and outlives a replica's
// older copy.
func TestTrashAndDelete(t *testing.T) {
	dir := t.TempDir()
	s := open(t, DirBackend{Root: dir})
	m, _ := s.Create(CreateInput{Mode: ModePage, Title: "Old"}, "")
	if err := s.Delete(m.ID, ""); !errors.Is(err, ErrNotTrashed) {
		t.Fatalf("delete before trash: %v", err)
	}
	yes, no := true, false
	got, _, err := s.Update(m.ID, Patch{Trashed: &yes}, "")
	if err != nil || got.Trashed.IsZero() {
		t.Fatalf("trash: %+v %v", got, err)
	}
	got, _, _ = s.Update(m.ID, Patch{Trashed: &no}, "")
	if !got.Trashed.IsZero() || got.Title != "Old" {
		t.Fatalf("restore: %+v", got)
	}
	_, _, _ = s.Update(m.ID, Patch{Trashed: &yes}, "")
	snap, _ := s.Snapshot(m.ID)
	if err := s.Delete(m.ID, "kvaps"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatalf("listed after delete: %+v", s.List())
	}
	// A replica that still has the document as it was before.
	if _, err := s.MergeRemote([]Remote{{Meta: snap.Meta, State: snap.State}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an older copy brought it back: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	re := open(t, DirBackend{Root: dir})
	if _, err := re.Get(m.ID); !errors.Is(err, ErrNotFound) || len(re.List()) != 0 {
		t.Fatalf("after restart: %v", err)
	}
	if _, err := re.Create(CreateInput{ID: m.ID, Mode: ModePage}, ""); !errors.Is(err, ErrExists) {
		t.Fatalf("the id was reused: %v", err)
	}
}
