// Package boards is the board catalogue: each board's metadata (title, tags)
// and its Yjs state, held in memory and written behind to a Backend.
//
// Every board is a directory in the backend:
//
//	boards/<id>/board.json   metadata (title, tags, timestamps)
//	boards/<id>/state.bin    the Yjs V1 state — the source of truth
//	boards/<id>/doc.json     the same document as JSON, for reading and diffs
//
// Edits are answered from memory at once and only marked dirty. A flush
// writes the dirty boards to the backend — after a quiet spell, at the
// latest after a maximum delay, when the last participant leaves a board and
// on shutdown — so a burst of strokes becomes one write, not hundreds.
package boards

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reearth/ygo/crdt"

	"github.com/aenix-io/aeboard/internal/seed"
)

// ErrNotFound is returned for an id that names no board.
var ErrNotFound = errors.New("board not found")

// Meta is a board's catalogue entry.
type Meta struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Tags    []string  `json:"tags"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// Patch changes a board's metadata; nil fields are left as they are.
type Patch struct {
	Title *string   `json:"title,omitempty"`
	Tags  *[]string `json:"tags,omitempty"`
}

// Snapshot is one board as a backend stores it.
type Snapshot struct {
	Meta  Meta
	State []byte
	// Editors are the logins whose changes this snapshot carries since the
	// last write (sorted); empty when unknown.
	Editors []string
}

// Backend persists boards.
type Backend interface {
	// Load returns every stored board.
	Load(ctx context.Context) ([]Snapshot, error)
	// Save writes the given boards.
	Save(ctx context.Context, boards []Snapshot) error
}

// Store is the in-memory catalogue over a Backend.
type Store struct {
	backend Backend
	now     func() time.Time

	mu     sync.Mutex
	boards map[string]*board
	// flushMu serialises flushes so two never write the same board at once.
	flushMu sync.Mutex
	// changed is signalled (non-blocking) on every new dirty mark.
	changed chan struct{}
	// listeners are told when the pending count changes.
	lmu       sync.Mutex
	listeners map[int]func()
	nextL     int
}

type board struct {
	meta  Meta
	state []byte
	// dirty is set while the board has changes the backend has not got;
	// dirtySince is when the oldest of them was made, lastEdit the newest.
	dirty      bool
	dirtySince time.Time
	lastEdit   time.Time
	editors    map[string]bool
	// gen counts changes, so a flush that raced an edit leaves it dirty.
	gen uint64
}

// Open loads every board from the backend.
func Open(ctx context.Context, backend Backend) (*Store, error) {
	snaps, err := backend.Load(ctx)
	if err != nil {
		return nil, err
	}
	s := &Store{
		backend:   backend,
		now:       time.Now,
		boards:    map[string]*board{},
		changed:   make(chan struct{}, 1),
		listeners: map[int]func(){},
	}
	for _, sn := range snaps {
		m := sn.Meta
		m.Tags = normTags(m.Tags)
		s.boards[m.ID] = &board{meta: m, state: sn.State}
	}
	return s, nil
}

// List returns every board, most recently updated first.
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meta, 0, len(s.boards))
	for _, b := range s.boards {
		out = append(out, cloneMeta(b.meta))
	}
	slices.SortFunc(out, func(a, b Meta) int {
		if c := b.Updated.Compare(a.Updated); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// Get returns one board's metadata.
func (s *Store) Get(id string) (Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boards[id]
	if !ok {
		return Meta{}, ErrNotFound
	}
	return cloneMeta(b.meta), nil
}

// Create adds a board that starts from the seed state. actor may be empty.
func (s *Store) Create(title string, tags []string, actor string) (Meta, error) {
	s.mu.Lock()
	id := newID()
	for s.boards[id] != nil {
		id = newID()
	}
	now := s.now().UTC()
	b := &board{
		meta:  Meta{ID: id, Title: strings.TrimSpace(title), Tags: normTags(tags), Created: now, Updated: now},
		state: seed.Update(),
	}
	if b.meta.Title == "" {
		b.meta.Title = "Untitled board"
	}
	s.boards[id] = b
	s.markLocked(b, actor)
	m := cloneMeta(b.meta)
	s.mu.Unlock()
	s.notify()
	return m, nil
}

// Update applies a metadata patch.
func (s *Store) Update(id string, p Patch, actor string) (Meta, error) {
	s.mu.Lock()
	b, ok := s.boards[id]
	if !ok {
		s.mu.Unlock()
		return Meta{}, ErrNotFound
	}
	m := b.meta
	if p.Title != nil {
		if t := strings.TrimSpace(*p.Title); t != "" {
			m.Title = t
		}
	}
	if p.Tags != nil {
		m.Tags = normTags(*p.Tags)
	}
	m.Updated = s.now().UTC()
	b.meta = m
	s.markLocked(b, actor)
	out := cloneMeta(m)
	s.mu.Unlock()
	s.notify()
	return out, nil
}

// Tags returns every tag in use, sorted. Tags are case-insensitive: "Ops"
// on one board and "ops" on another are one tag, listed in the spelling of
// the board created first.
func (s *Store) Tags() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := make([]*board, 0, len(s.boards))
	for _, b := range s.boards {
		all = append(all, b)
	}
	slices.SortFunc(all, func(a, b *board) int { return a.meta.Created.Compare(b.meta.Created) })
	var tags []string
	for _, b := range all {
		tags = append(tags, b.meta.Tags...)
	}
	return normTags(tags)
}

// LoadState returns a board's Yjs state (the seed for a board that never
// stored one).
func (s *Store) LoadState(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boards[id]
	if !ok {
		return nil, ErrNotFound
	}
	if len(b.state) == 0 {
		return seed.Update(), nil
	}
	return bytes.Clone(b.state), nil
}

// ApplyUpdate merges a Yjs update into a board's state. It reports whether
// the state actually changed: an update the state already contains (a peer
// re-sending what it synced) leaves the board untouched and clean. editors
// are the logins the change is attributed to.
func (s *Store) ApplyUpdate(id string, update []byte, editors ...string) (bool, error) {
	s.mu.Lock()
	b, ok := s.boards[id]
	if !ok {
		s.mu.Unlock()
		return false, ErrNotFound
	}
	cur := b.state
	if len(cur) == 0 {
		cur = seed.Update()
	}
	merged, err := MergeStates(cur, update)
	if err != nil {
		s.mu.Unlock()
		return false, err
	}
	if merged == nil {
		s.mu.Unlock()
		return false, nil
	}
	b.state = merged
	b.meta.Updated = s.now().UTC()
	if len(editors) == 0 {
		s.markLocked(b, "")
	}
	for _, e := range editors {
		s.markLocked(b, e)
	}
	s.mu.Unlock()
	s.notify()
	return true, nil
}

// Remote is a board as another replica wrote it (fetched from git).
type Remote struct {
	Meta  Meta
	State []byte
}

// MergeRemote folds boards written elsewhere into memory: Yjs states merge
// (a CRDT, so nothing is lost either way), metadata keeps the more recently
// updated side. It returns, per board whose state gained something, the
// update that brings this replica's previous state up to the merged one —
// what live rooms must be sent. A remote board this store does not know is
// adopted as is. Merged boards are not marked dirty: the remote has them.
func (s *Store) MergeRemote(remotes []Remote) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gained := map[string][]byte{}
	for _, r := range remotes {
		id := r.Meta.ID
		b, ok := s.boards[id]
		if !ok {
			m := r.Meta
			m.Tags = normTags(m.Tags)
			s.boards[id] = &board{meta: m, state: bytes.Clone(r.State)}
			continue
		}
		if r.Meta.Updated.After(b.meta.Updated) {
			m := r.Meta
			m.Tags = normTags(m.Tags)
			b.meta = m
		}
		if len(r.State) == 0 {
			continue
		}
		cur := b.state
		if len(cur) == 0 {
			cur = seed.Update()
		}
		merged, err := MergeStates(cur, r.State)
		if err != nil {
			return gained, fmt.Errorf("board %s: %w", id, err)
		}
		if merged == nil {
			continue
		}
		diff, err := diffFrom(cur, merged)
		if err != nil {
			return gained, fmt.Errorf("board %s: %w", id, err)
		}
		b.state = merged
		gained[id] = diff
	}
	return gained, nil
}

// diffFrom is the update that takes a doc holding `from` to `to`.
func diffFrom(from, to []byte) ([]byte, error) {
	sv, err := crdt.EncodeStateVectorFromUpdate(from)
	if err != nil {
		return nil, err
	}
	dsv, err := crdt.DecodeStateVectorV1(sv)
	if err != nil {
		return nil, err
	}
	return crdt.DiffUpdateV1(to, dsv)
}

// Snapshot returns a board as it is in memory now.
func (s *Store) Snapshot(id string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boards[id]
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	return snapshotOf(b), nil
}

func snapshotOf(b *board) Snapshot {
	state := b.state
	if len(state) == 0 {
		state = seed.Update()
	}
	eds := make([]string, 0, len(b.editors))
	for e := range b.editors {
		eds = append(eds, e)
	}
	sort.Strings(eds)
	return Snapshot{Meta: cloneMeta(b.meta), State: bytes.Clone(state), Editors: eds}
}

func (s *Store) markLocked(b *board, editor string) {
	now := s.now()
	if !b.dirty {
		b.dirty = true
		b.dirtySince = now
	}
	b.lastEdit = now
	b.gen++
	if editor != "" {
		if b.editors == nil {
			b.editors = map[string]bool{}
		}
		b.editors[editor] = true
	}
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// Pending is the number of boards with changes not yet written to the
// backend.
func (s *Store) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.boards {
		if b.dirty {
			n++
		}
	}
	return n
}

// Subscribe registers fn to be called whenever the pending count may have
// changed; the returned func unregisters it.
func (s *Store) Subscribe(fn func()) func() {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	id := s.nextL
	s.nextL++
	s.listeners[id] = fn
	return func() {
		s.lmu.Lock()
		defer s.lmu.Unlock()
		delete(s.listeners, id)
	}
}

func (s *Store) notify() {
	s.lmu.Lock()
	fns := make([]func(), 0, len(s.listeners))
	for _, fn := range s.listeners {
		fns = append(fns, fn)
	}
	s.lmu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// Flush writes dirty boards to the backend: the given ids, or every dirty
// board when none are given. A board edited while its write was in flight
// stays dirty for the next flush.
func (s *Store) Flush(ctx context.Context, ids ...string) error {
	return s.flush(ctx, func(string, *board) bool { return true }, ids...)
}

func (s *Store) flush(ctx context.Context, due func(string, *board) bool, ids ...string) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	type taken struct {
		id  string
		gen uint64
	}
	s.mu.Lock()
	var snaps []Snapshot
	var took []taken
	pick := func(id string, b *board) {
		if b.dirty && due(id, b) {
			snaps = append(snaps, snapshotOf(b))
			took = append(took, taken{id, b.gen})
		}
	}
	if len(ids) == 0 {
		for id, b := range s.boards {
			pick(id, b)
		}
	} else {
		for _, id := range ids {
			if b, ok := s.boards[id]; ok {
				pick(id, b)
			}
		}
	}
	s.mu.Unlock()
	if len(snaps) == 0 {
		return nil
	}
	slices.SortFunc(snaps, func(a, b Snapshot) int { return strings.Compare(a.Meta.ID, b.Meta.ID) })

	if err := s.backend.Save(ctx, snaps); err != nil {
		return err
	}

	s.mu.Lock()
	for _, t := range took {
		if b := s.boards[t.id]; b != nil && b.gen == t.gen {
			b.dirty = false
			b.editors = nil
		}
	}
	s.mu.Unlock()
	s.notify()
	return nil
}

// FlushPolicy says when a dirty board is written without being asked.
type FlushPolicy struct {
	// Quiet writes a board once nobody has changed it for this long.
	Quiet time.Duration
	// MaxDelay writes a board whose oldest unwritten change is this old,
	// even while it is still being edited.
	MaxDelay time.Duration
	// Tick is how often the policy is checked.
	Tick time.Duration
}

// DefaultFlushPolicy suits the git backend: a commit per board per pause in
// the editing, and at least one a minute on a board in constant use.
var DefaultFlushPolicy = FlushPolicy{Quiet: 5 * time.Second, MaxDelay: time.Minute, Tick: time.Second}

// RunFlusher writes dirty boards by policy until ctx ends, then flushes
// everything once more (with a fresh context bounded by final) so shutdown
// loses nothing. Errors are reported to onErr and retried on the next tick.
func (s *Store) RunFlusher(ctx context.Context, p FlushPolicy, final time.Duration, onErr func(error)) {
	t := time.NewTicker(p.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), final)
			if err := s.Flush(fctx); err != nil && onErr != nil {
				onErr(err)
			}
			cancel()
			return
		case <-t.C:
		case <-s.changed:
			continue
		}
		now := s.now()
		err := s.flush(ctx, func(_ string, b *board) bool {
			return now.Sub(b.lastEdit) >= p.Quiet || now.Sub(b.dirtySince) >= p.MaxDelay
		})
		if err != nil && onErr != nil {
			onErr(err)
		}
	}
}

// MergeStates folds update into state through a live doc and returns the
// compacted state, or nil when update brought nothing new.
func MergeStates(state, update []byte) ([]byte, error) {
	doc := crdt.New()
	if err := crdt.ApplyUpdateV1(doc, state, nil); err != nil {
		return nil, fmt.Errorf("apply state: %w", err)
	}
	before := doc.EncodeStateAsUpdate()
	if err := crdt.ApplyUpdateV1(doc, update, nil); err != nil {
		return nil, fmt.Errorf("apply update: %w", err)
	}
	// Yjs fires an update event even for an update the doc already holds, so
	// compare the encoded states instead.
	after := doc.EncodeStateAsUpdate()
	if bytes.Equal(before, after) {
		return nil, nil
	}
	return after, nil
}

// normTags trims, drops empties and duplicates, and sorts. Tags compare
// case-insensitively but keep the case they were first written with.
func normTags(tags []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		k := strings.ToLower(t)
		if t == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

func cloneMeta(m Meta) Meta {
	m.Tags = slices.Clone(m.Tags)
	return m
}

const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// newID returns a short random board id (10 Crockford base32 characters).
func newID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	for i := range b {
		b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
	}
	return string(b[:])
}

// ValidID reports whether id has the shape newID produces, so a request
// path can never name a file outside the board directory.
func ValidID(id string) bool {
	if len(id) != 10 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune(idAlphabet, r) {
			return false
		}
	}
	return true
}
