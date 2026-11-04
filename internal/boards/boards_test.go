package boards

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reearth/ygo/crdt"

	"github.com/aenix-io/aenotes/internal/seed"
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

// countingBackend records every Save.
type countingBackend struct {
	mu    sync.Mutex
	saves [][]Snapshot
	// during, when set, runs inside Save (to race an edit against a write).
	during func()
	stored map[string]Snapshot
}

func (c *countingBackend) Load(context.Context) ([]Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Snapshot
	for _, s := range c.stored {
		out = append(out, s)
	}
	return out, nil
}

func (c *countingBackend) Save(_ context.Context, b []Snapshot) error {
	if c.during != nil {
		c.during()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saves = append(c.saves, b)
	if c.stored == nil {
		c.stored = map[string]Snapshot{}
	}
	for _, s := range b {
		c.stored[s.Meta.ID] = s
	}
	return nil
}

func (c *countingBackend) saveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.saves)
}

func open(t *testing.T, b Backend) *Store {
	t.Helper()
	s, err := Open(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateStartsFromSeedAndSurvivesRestartThroughTheDirBackend(t *testing.T) {
	dir := t.TempDir()
	s := open(t, DirBackend{Root: dir})
	m, err := s.Create(CreateInput{Title: "  Roadmap "}, "kvaps")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Roadmap" || !ValidID(m.ID) {
		t.Fatalf("meta = %+v", m)
	}
	st, _ := s.LoadState(m.ID)
	upd := editOf(t, st, 7, "shape-1", "rect")
	if _, err := s.ApplyUpdate(m.ID, upd, "kvaps"); err != nil {
		t.Fatal(err)
	}
	// Nothing reaches the backend before a flush.
	if got := open(t, DirBackend{Root: dir}).List(); len(got) != 0 {
		t.Fatalf("written before flush: %+v", got)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	re := open(t, DirBackend{Root: dir})
	got, err := re.Get(m.ID)
	if err != nil || got.Title != "Roadmap" {
		t.Fatalf("reopened meta = %+v, %v", got, err)
	}
	st, _ = re.LoadState(m.ID)
	if keys := blockKeys(t, st); len(keys) != 3 || !slices.Contains(keys, "shape-1") {
		t.Fatalf("reopened blocks = %v", keys)
	}
}

// An empty title stays empty (the SPA shows "Untitled"); a new document
// is a board unless asked otherwise.
func TestEmptyTitleStaysEmptyAndModeDefaultsToBoard(t *testing.T) {
	s := open(t, &countingBackend{})
	m, _ := s.Create(CreateInput{Title: "   "}, "")
	if m.Title != "" || m.Folder != "" || m.Mode != ModeEdgeless {
		t.Fatalf("meta = %+v", m)
	}
	st, _ := s.LoadState(m.ID)
	if DocTitle(st) != "" {
		t.Fatalf("doc title = %q", DocTitle(st))
	}
}

// The title lives in the document: creating writes it there, an edit of the
// document's title text renames the catalog entry, and renaming through
// the API rewrites the text and hands back the update for live rooms.
func TestTitleLivesInTheDocument(t *testing.T) {
	s := open(t, &countingBackend{})
	m, _ := s.Create(CreateInput{Title: "Plan"}, "")
	st, _ := s.LoadState(m.ID)
	if DocTitle(st) != "Plan" {
		t.Fatalf("doc title = %q", DocTitle(st))
	}

	// A browser types into the title.
	upd, _ := titleUpdate(st, "Plan v2")
	if _, err := s.ApplyUpdate(m.ID, upd); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(m.ID); got.Title != "Plan v2" {
		t.Fatalf("catalog title = %q", got.Title)
	}

	// The API renames it.
	title := "Plan v3"
	got, live, err := s.Update(m.ID, Patch{Title: &title}, "")
	if err != nil || got.Title != "Plan v3" || live == nil {
		t.Fatalf("update = %+v %v live=%v", got, err, live != nil)
	}
	st, _ = s.LoadState(m.ID)
	if DocTitle(st) != "Plan v3" {
		t.Fatalf("doc title after rename = %q", DocTitle(st))
	}
	// A browser holding the v2 state catches up with the live update.
	doc := crdt.New()
	_ = crdt.ApplyUpdateV1(doc, st, nil)
	if _, live2, _ := s.Update(m.ID, Patch{Title: &title}, ""); live2 != nil {
		t.Fatalf("renaming to the same title produced an update")
	}
}

func TestPagesAndClientChosenIDs(t *testing.T) {
	s := open(t, &countingBackend{})
	m, err := s.Create(CreateInput{ID: "abcdefghjk", Mode: ModePage, Title: "Notes"}, "")
	if err != nil || m.ID != "abcdefghjk" || m.Mode != ModePage {
		t.Fatalf("create page = %+v %v", m, err)
	}
	st, _ := s.LoadState(m.ID)
	if keys := blockKeys(t, st); len(keys) != 4 {
		t.Fatalf("page seed blocks = %v, want page, surface, note, paragraph", keys)
	}
	if _, err := s.Create(CreateInput{ID: "abcdefghjk"}, ""); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate id: %v", err)
	}
	for _, bad := range []CreateInput{{ID: "../x"}, {Mode: "spreadsheet"}} {
		if _, err := s.Create(bad, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("create %+v: %v", bad, err)
		}
	}
}

// Documents stored before titles lived in the document had theirs in the
// catalog only; loading writes it into the document (and dirties it).
func TestLegacyCatalogTitleMovesIntoTheDocument(t *testing.T) {
	be := &countingBackend{stored: map[string]Snapshot{}}
	be.stored["abcdefghjk"] = Snapshot{Meta: Meta{ID: "abcdefghjk", Title: "Old board"}, State: seed.Update()}
	s := open(t, be)
	st, _ := s.LoadState("abcdefghjk")
	if DocTitle(st) != "Old board" || s.Pending() != 1 {
		t.Fatalf("doc title = %q pending = %d", DocTitle(st), s.Pending())
	}
	if m, _ := s.Get("abcdefghjk"); m.Mode != ModeEdgeless {
		t.Fatalf("legacy mode = %q", m.Mode)
	}
}

// The point of write-behind: a burst of edits is one write, carrying every
// editor, not one write per stroke.
func TestManyEditsCoalesceIntoOneWrite(t *testing.T) {
	be := &countingBackend{}
	s := open(t, be)
	m, _ := s.Create(CreateInput{Title: "b"}, "alice")
	for i := range 100 {
		st, _ := s.LoadState(m.ID)
		who := []string{"alice", "bob"}[i%2]
		if _, err := s.ApplyUpdate(m.ID, editOf(t, st, crdt.ClientID(100+i%2), "k", strings.Repeat("x", i)), who); err != nil {
			t.Fatal(err)
		}
	}
	if s.Pending() != 1 {
		t.Fatalf("pending = %d, want 1 board", s.Pending())
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if be.saveCount() != 1 || len(be.saves[0]) != 1 {
		t.Fatalf("saves = %d", be.saveCount())
	}
	if eds := be.saves[0][0].Editors; !slices.Equal(eds, []string{"alice", "bob"}) {
		t.Fatalf("editors = %v", eds)
	}
	if s.Pending() != 0 {
		t.Fatalf("pending after flush = %d", s.Pending())
	}
	// A flush with nothing dirty writes nothing.
	_ = s.Flush(context.Background())
	if be.saveCount() != 1 {
		t.Fatalf("empty flush wrote")
	}
}

func TestRepeatedUpdateIsNotAChange(t *testing.T) {
	be := &countingBackend{}
	s := open(t, be)
	m, _ := s.Create(CreateInput{Title: "b"}, "")
	_ = s.Flush(context.Background())
	st, _ := s.LoadState(m.ID)
	upd := editOf(t, st, 7, "shape-1", "rect")
	if ch, _ := s.ApplyUpdate(m.ID, upd); !ch {
		t.Fatal("first apply did not change")
	}
	_ = s.Flush(context.Background())
	// The same update again — a reconnecting peer re-sending what the
	// server already has — must not dirty the board.
	if ch, _ := s.ApplyUpdate(m.ID, upd); ch || s.Pending() != 0 {
		t.Fatalf("repeat apply changed=%v pending=%d", ch, s.Pending())
	}
}

// An edit that lands while the board's write is in flight is not in that
// write, so the board must stay dirty for the next one.
func TestEditDuringFlushStaysDirty(t *testing.T) {
	be := &countingBackend{}
	s := open(t, be)
	m, _ := s.Create(CreateInput{Title: "b"}, "")
	be.during = func() {
		be.during = nil
		st, _ := s.LoadState(m.ID)
		_, _ = s.ApplyUpdate(m.ID, editOf(t, st, 9, "late", "y"))
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Pending() != 1 {
		t.Fatalf("pending = %d, want the late edit still pending", s.Pending())
	}
	_ = s.Flush(context.Background())
	if keys := blockKeys(t, be.stored[m.ID].State); !slices.Contains(keys, "late") {
		t.Fatalf("late edit never written: %v", keys)
	}
}

func TestConcurrentEditsFromDivergedReplicasMerge(t *testing.T) {
	s := open(t, &countingBackend{})
	m, _ := s.Create(CreateInput{Title: "b"}, "")
	base, _ := s.LoadState(m.ID)
	a := editOf(t, base, 11, "from-a", "x")
	b := editOf(t, base, 12, "from-b", "y")
	_, _ = s.ApplyUpdate(m.ID, a)
	_, _ = s.ApplyUpdate(m.ID, b)
	st, _ := s.LoadState(m.ID)
	if got := blockKeys(t, st); !slices.Contains(got, "from-a") || !slices.Contains(got, "from-b") || len(got) != 4 {
		t.Fatalf("blocks = %v", got)
	}
}

// Merging another replica's copy of a board — what a git fetch brings — is
// the conflict resolution: Yjs states merge, the newer metadata wins, and
// the caller gets exactly the update its live rooms are missing.
func TestMergeRemote(t *testing.T) {
	clock := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	s := open(t, &countingBackend{})
	s.now = func() time.Time { return clock }
	m, _ := s.Create(CreateInput{Title: "local title"}, "")
	_ = s.Flush(context.Background())
	base, _ := s.LoadState(m.ID)

	_, _ = s.ApplyUpdate(m.ID, editOf(t, base, 11, "ours", "x"))
	theirs, _ := MergeStates(base, editOf(t, base, 12, "theirs", "y"))
	// The other replica also renamed it — in the document, where titles live.
	rename, _ := titleUpdate(theirs, "remote title")
	theirs, _ = MergeStates(theirs, rename)
	remoteMeta := m
	remoteMeta.Title = "remote title"
	remoteMeta.Updated = clock.Add(time.Hour)

	other := Meta{ID: "zzzzzzzzzz", Title: "new elsewhere", Created: clock, Updated: clock}
	gained, err := s.MergeRemote([]Remote{{Meta: remoteMeta, State: theirs}, {Meta: other, State: base}})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.LoadState(m.ID)
	if keys := blockKeys(t, st); !slices.Contains(keys, "ours") || !slices.Contains(keys, "theirs") {
		t.Fatalf("merged blocks = %v", keys)
	}
	if got, _ := s.Get(m.ID); got.Title != "remote title" {
		t.Fatalf("newer remote title lost: %q", got.Title)
	}
	if _, err := s.Get(other.ID); err != nil {
		t.Fatalf("unknown remote board not adopted: %v", err)
	}
	// The diff brings a doc that only had our state up to the merge.
	doc := crdt.New()
	_ = crdt.ApplyUpdateV1(doc, base, nil)
	_ = crdt.ApplyUpdateV1(doc, editOf(t, base, 11, "ours", "x"), nil)
	if err := crdt.ApplyUpdateV1(doc, gained[m.ID], nil); err != nil {
		t.Fatal(err)
	}
	if keys := blockKeys(t, doc.EncodeStateAsUpdate()); !slices.Contains(keys, "theirs") {
		t.Fatalf("diff does not carry the remote edit: %v", keys)
	}
	// Merging the same remote again gains nothing.
	again, _ := s.MergeRemote([]Remote{{Meta: remoteMeta, State: theirs}})
	if len(again) != 0 {
		t.Fatalf("second merge gained %v", again)
	}
}

func TestOlderRemoteMetadataDoesNotWin(t *testing.T) {
	clock := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	s := open(t, &countingBackend{})
	s.now = func() time.Time { return clock }
	m, _ := s.Create(CreateInput{Title: "mine"}, "")
	old := m
	old.Title = "stale"
	old.Updated = clock.Add(-time.Hour)
	_, _ = s.MergeRemote([]Remote{{Meta: old}})
	if got, _ := s.Get(m.ID); got.Title != "mine" {
		t.Fatalf("title = %q", got.Title)
	}
}

func TestFlusherPolicy(t *testing.T) {
	be := &countingBackend{}
	s := open(t, be)
	var mu sync.Mutex
	clock := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }

	m, _ := s.Create(CreateInput{Title: "b"}, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.RunFlusher(ctx, FlushPolicy{Quiet: 5 * time.Second, MaxDelay: time.Minute, Tick: 5 * time.Millisecond}, time.Second, nil)
		close(done)
	}()
	wait := func(n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for be.saveCount() < n && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Still within the quiet window: nothing written.
	advance(2 * time.Second)
	time.Sleep(30 * time.Millisecond)
	if be.saveCount() != 0 {
		t.Fatalf("wrote inside the quiet window")
	}
	// Quiet for 5s: written.
	advance(4 * time.Second)
	wait(1)
	if be.saveCount() != 1 {
		t.Fatalf("not written after the quiet window")
	}

	// Constant editing: written once MaxDelay passes despite never going quiet.
	for range 70 {
		st, _ := s.LoadState(m.ID)
		_, _ = s.ApplyUpdate(m.ID, editOf(t, st, 5, "k", time.Now().String()))
		advance(time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	wait(2)
	if be.saveCount() < 2 {
		t.Fatalf("a board in constant use was never written")
	}

	// Shutdown flushes what is left, quiet or not.
	st, _ := s.LoadState(m.ID)
	_, _ = s.ApplyUpdate(m.ID, editOf(t, st, 5, "last", "z"))
	cancel()
	<-done
	if s.Pending() != 0 || !slices.Contains(blockKeys(t, be.stored[m.ID].State), "last") {
		t.Fatalf("shutdown lost the last edit")
	}
}

func TestListNewestFirst(t *testing.T) {
	s := open(t, &countingBackend{})
	clock := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	old, _ := s.Create(CreateInput{Title: "old"}, "")
	_, _ = s.Create(CreateInput{Title: "new"}, "")
	if list := s.List(); len(list) != 2 || list[0].Title != "new" {
		t.Fatalf("list = %+v", list)
	}
	root := ""
	_, _, _ = s.Update(old.ID, Patch{Folder: &root}, "")
	if s.List()[0].ID != old.ID {
		t.Fatalf("updated document not first")
	}
}

func TestUnknownBoard(t *testing.T) {
	s := open(t, &countingBackend{})
	if _, err := s.LoadState("0000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LoadState: %v", err)
	}
	if _, err := s.ApplyUpdate("0000000000", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if _, _, err := s.Update("0000000000", Patch{}, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update: %v", err)
	}
}

func TestDocJSONIsReadable(t *testing.T) {
	s := open(t, &countingBackend{})
	m, _ := s.Create(CreateInput{Title: "b"}, "")
	st, _ := s.LoadState(m.ID)
	out, err := DocJSON(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"sys:flavour": "affine:page"`) || !strings.Contains(string(out), `"sys:flavour": "affine:surface"`) {
		t.Fatalf("doc.json = %s", out)
	}
}

func TestValidID(t *testing.T) {
	for _, bad := range []string{"", "../etc/pw", "ABCDEFGHJK", "0123456789a", "abcdefghi!"} {
		if ValidID(bad) {
			t.Errorf("ValidID(%q) = true", bad)
		}
	}
}

// Boards first stored a binary state.bin. Such a board still loads, and its
// next write replaces the binary with the text state.
func TestLegacyBinaryStateIsReadThenReplacedByText(t *testing.T) {
	dir := t.TempDir()
	s := open(t, DirBackend{Root: dir})
	m, _ := s.Create(CreateInput{Title: "old"}, "")
	st, _ := s.LoadState(m.ID)
	st, _ = MergeStates(st, editOf(t, st, 9, "kept", "v"))
	board := filepath.Join(dir, BoardPath(m.ID))
	meta, _ := json.Marshal(m)
	if err := os.MkdirAll(board, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(board, "board.json"), meta, 0o600)
	_ = os.WriteFile(filepath.Join(board, "state.bin"), st, 0o600)

	re := open(t, DirBackend{Root: dir})
	got, _ := re.LoadState(m.ID)
	if !slices.Contains(blockKeys(t, got), "kept") {
		t.Fatalf("legacy state not loaded")
	}
	_, _, _ = re.Update(m.ID, Patch{}, "")
	if err := re.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(board, "state.bin")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("state.bin still there: %v", err)
	}
	text, err := os.ReadFile(filepath.Join(board, "state.jsonl"))
	if err != nil || !strings.HasPrefix(string(text), `{"format":"aeboard-yjs-v1"}`) {
		t.Fatalf("state.jsonl = %.80q, %v", text, err)
	}
	again, _ := open(t, DirBackend{Root: dir}).LoadState(m.ID)
	if !slices.Contains(blockKeys(t, again), "kept") {
		t.Fatalf("text state lost the edit")
	}
}

// A blank document (the editor builds its blocks) gets no seed — not even
// when it is loaded before the creator's first update arrives — and its
// title follows what the creator writes.
func TestBlankDocumentGetsNoSeed(t *testing.T) {
	dir := t.TempDir()
	s := open(t, DirBackend{Root: dir})
	if _, err := s.Create(CreateInput{Blank: true}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank without an id: %v", err)
	}
	m, err := s.Create(CreateInput{ID: "bbbbbbbbbb", Mode: ModePage, Blank: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.LoadState(m.ID)
	if keys := blockKeys(t, st); len(keys) != 0 {
		t.Fatalf("blank document has blocks %v", keys)
	}
	_ = s.Flush(context.Background())
	re := open(t, DirBackend{Root: dir})
	st, _ = re.LoadState(m.ID)
	if keys := blockKeys(t, st); len(keys) != 0 {
		t.Fatalf("blank document grew a seed after a restart: %v", keys)
	}
}
