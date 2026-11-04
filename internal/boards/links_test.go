package boards

import (
	"context"
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
