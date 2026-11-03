// Package boards is the board catalogue: each board's metadata (title, tags)
// and its Yjs state, kept in one directory per board:
//
//	boards/<id>/board.json   metadata (title, tags, timestamps)
//	boards/<id>/state.bin    the Yjs V1 state — the source of truth
//
// The layout is the one the git-backed store commits; this package keeps it
// on a plain directory and holds everything in memory once loaded.
package boards

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

type board struct {
	meta  Meta
	state []byte
}

// Store is a directory of boards.
type Store struct {
	dir string
	now func() time.Time

	mu     sync.Mutex
	boards map[string]*board
}

// Open loads every board under dir/boards.
func Open(dir string) (*Store, error) {
	s := &Store{dir: filepath.Join(dir, "boards"), now: time.Now, boards: map[string]*board{}}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := s.read(e.Name())
		if err != nil {
			return nil, fmt.Errorf("board %s: %w", e.Name(), err)
		}
		s.boards[e.Name()] = b
	}
	return s, nil
}

func (s *Store) read(id string) (*board, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, id, "board.json"))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m.ID = id
	m.Tags = normTags(m.Tags)
	state, err := os.ReadFile(filepath.Join(s.dir, id, "state.bin"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return &board{meta: m, state: state}, nil
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

// Create adds a board that starts from the seed state.
func (s *Store) Create(title string, tags []string) (Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if err := s.writeMeta(b.meta); err != nil {
		return Meta{}, err
	}
	if err := s.writeState(id, b.state); err != nil {
		return Meta{}, err
	}
	s.boards[id] = b
	return cloneMeta(b.meta), nil
}

// Update applies a metadata patch.
func (s *Store) Update(id string, p Patch) (Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boards[id]
	if !ok {
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
	if err := s.writeMeta(m); err != nil {
		return Meta{}, err
	}
	b.meta = m
	return cloneMeta(m), nil
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
	return append([]byte(nil), b.state...), nil
}

// ApplyUpdate merges a Yjs update into a board's state. It reports whether
// the state actually changed: an update the state already contains (a peer
// re-sending what it synced) leaves the board untouched.
func (s *Store) ApplyUpdate(id string, update []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boards[id]
	if !ok {
		return false, ErrNotFound
	}
	cur := b.state
	if len(cur) == 0 {
		cur = seed.Update()
	}
	merged, err := mergeState(cur, update)
	if err != nil {
		return false, err
	}
	if merged == nil {
		return false, nil
	}
	if err := s.writeState(id, merged); err != nil {
		return false, err
	}
	b.state = merged
	b.meta.Updated = s.now().UTC()
	if err := s.writeMeta(b.meta); err != nil {
		return true, err
	}
	return true, nil
}

// mergeState folds update into state through a live doc and returns the
// compacted state, or nil when update brought nothing new.
func mergeState(state, update []byte) ([]byte, error) {
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

func (s *Store) writeMeta(m Meta) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, m.ID, "board.json"), append(raw, '\n'))
}

func (s *Store) writeState(id string, state []byte) error {
	return writeAtomic(filepath.Join(s.dir, id, "state.bin"), state)
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
