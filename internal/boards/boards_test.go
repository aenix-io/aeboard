package boards

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/reearth/ygo/crdt"
)

// editOf returns a Yjs update that, applied on top of state, sets
// blocks[key] = value — a stand-in for a browser edit.
func editOf(t *testing.T, state []byte, client crdt.ClientID, key, value string) []byte {
	t.Helper()
	doc := crdt.New(crdt.WithClientID(client))
	if err := crdt.ApplyUpdateV1(doc, state, nil); err != nil {
		t.Fatal(err)
	}
	sv := doc.StateVector()
	m := doc.GetMap("blocks")
	doc.Transact(func(txn *crdt.Transaction) { m.Set(txn, key, value) })
	return crdt.EncodeStateAsUpdateV1(doc, sv)
}

func blockKeys(t *testing.T, state []byte) []string {
	t.Helper()
	doc := crdt.New()
	if err := crdt.ApplyUpdateV1(doc, state, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := doc.GetMap("blocks").ToJSON()
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestCreateStartsFromSeedAndSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Create("  Roadmap ", []string{"q4", " Team ", "q4", ""})
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Roadmap" || !slices.Equal(m.Tags, []string{"q4", "Team"}) {
		t.Fatalf("meta = %+v", m)
	}
	if !ValidID(m.ID) {
		t.Fatalf("id %q is not valid", m.ID)
	}
	st, _ := s.LoadState(m.ID)
	if got := blockKeys(t, st); len(got) != 2 {
		t.Fatalf("new board blocks = %v, want the 2 seed blocks", got)
	}

	re, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := re.Get(m.ID)
	if err != nil || got.Title != "Roadmap" || !slices.Equal(got.Tags, m.Tags) {
		t.Fatalf("reopened meta = %+v, %v", got, err)
	}
}

func TestEmptyTitleBecomesUntitled(t *testing.T) {
	s, _ := Open(t.TempDir())
	m, _ := s.Create("   ", nil)
	if m.Title != "Untitled board" || m.Tags == nil {
		t.Fatalf("meta = %+v", m)
	}
}

func TestApplyUpdatePersistsAndIgnoresRepeats(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	m, _ := s.Create("b", nil)
	st, _ := s.LoadState(m.ID)
	upd := editOf(t, st, 7, "shape-1", "rect")

	changed, err := s.ApplyUpdate(m.ID, upd)
	if err != nil || !changed {
		t.Fatalf("first apply: changed=%v err=%v", changed, err)
	}
	// The same update again — a reconnecting peer re-sending what the
	// server already has — must not count as a change.
	changed, err = s.ApplyUpdate(m.ID, upd)
	if err != nil || changed {
		t.Fatalf("repeat apply: changed=%v err=%v", changed, err)
	}

	re, _ := Open(dir)
	st, _ = re.LoadState(m.ID)
	if got := blockKeys(t, st); !slices.Contains(got, "shape-1") || len(got) != 3 {
		t.Fatalf("reopened blocks = %v", got)
	}
}

func TestConcurrentEditsFromDivergedReplicasMerge(t *testing.T) {
	s, _ := Open(t.TempDir())
	m, _ := s.Create("b", nil)
	base, _ := s.LoadState(m.ID)
	// Two peers edit the same base without seeing each other.
	a := editOf(t, base, 11, "from-a", "x")
	b := editOf(t, base, 12, "from-b", "y")
	if _, err := s.ApplyUpdate(m.ID, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyUpdate(m.ID, b); err != nil {
		t.Fatal(err)
	}
	st, _ := s.LoadState(m.ID)
	got := blockKeys(t, st)
	if !slices.Contains(got, "from-a") || !slices.Contains(got, "from-b") || len(got) != 4 {
		t.Fatalf("blocks = %v, want both edits on top of the seed", got)
	}
}

func TestUnknownBoard(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, err := s.LoadState("0000000000"); err != ErrNotFound {
		t.Fatalf("LoadState: %v", err)
	}
	if _, err := s.ApplyUpdate("0000000000", nil); err != ErrNotFound {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if _, err := s.Update("0000000000", Patch{}); err != ErrNotFound {
		t.Fatalf("Update: %v", err)
	}
}

func TestListNewestFirstAndTagsUnion(t *testing.T) {
	s, _ := Open(t.TempDir())
	clock := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	old, _ := s.Create("old", []string{"ops"})
	_, _ = s.Create("new", []string{"design", "Ops"})
	list := s.List()
	if len(list) != 2 || list[0].Title != "new" {
		t.Fatalf("list = %+v", list)
	}
	// Touching the old board moves it to the top.
	tags := []string{"ops", "retro"}
	_, _ = s.Update(old.ID, Patch{Tags: &tags})
	if s.List()[0].ID != old.ID {
		t.Fatalf("updated board not first")
	}
	// "ops" and "Ops" are one tag, spelled as on the board created first.
	if got := s.Tags(); !slices.Equal(got, []string{"design", "ops", "retro"}) {
		t.Fatalf("tags = %v", got)
	}
}

func TestValidID(t *testing.T) {
	for _, bad := range []string{"", "../etc/pw", "ABCDEFGHJK", "0123456789a", "abcdefghi!"} {
		if ValidID(bad) {
			t.Errorf("ValidID(%q) = true", bad)
		}
	}
}
