// Package boards is the document catalogue: each document's metadata (title, folder)
// and its Yjs state, held in memory and written behind to a Backend.
//
// Every board is a directory in the backend:
//
//	boards/<id>/board.json   metadata (mode, title, folder, timestamps)
//	boards/<id>/state.jsonl  the Yjs state as text (package ytext) — the source of truth
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

	"github.com/aenix-io/aenotes/internal/blocks"
	"github.com/aenix-io/aenotes/internal/seed"
)

// ErrNotFound is returned for an id that names no board.
var ErrNotFound = errors.New("board not found")

// Meta is a board's catalogue entry.
type Meta struct {
	ID string `json:"id"`
	// Mode is how the document opens: "edgeless" (a board) or "page".
	Mode string `json:"mode"`
	// Title mirrors the title written in the document itself (its root
	// block's title); the store keeps the two in step.
	Title string `json:"title"`
	// Folder is the folder the document is in; "" is the top level.
	Folder string `json:"folder"`
	// Parent is the document this one lives inside, when it does (then
	// Folder is ""): every document has one home, a folder or a document.
	// Links to it from other documents are shortcuts.
	Parent string `json:"parent,omitempty"`
	// Rank orders the document among its folder's contents (see ValidRank);
	// "" sorts after the ranked ones, by title.
	Rank    string    `json:"rank,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// Trashed is when the document was moved to the trash (zero: it is
	// not). A trashed document is kept whole and can be restored.
	Trashed time.Time `json:"trashed,omitzero"`
	// Deleted marks a document deleted for good: only this metadata stays,
	// as a tombstone, so a merge with a replica that still has the document
	// does not bring it back.
	Deleted bool `json:"deleted,omitempty"`
}

// Document modes.
const (
	ModeEdgeless = "edgeless"
	ModePage     = "page"
)

// ErrExists is returned when a document is created under an id in use.
var ErrExists = errors.New("document already exists")

// ErrInvalid is returned for a create request that names a bad id or mode.
var ErrInvalid = errors.New("invalid document")

// CreateInput is a new document.
type CreateInput struct {
	// ID is chosen by the client when it needs the id before the round trip
	// (a page created from the editor's link menu); empty mints one.
	ID     string
	Mode   string
	Title  string
	Folder string
	// Parent creates the document inside another one.
	Parent string
	// Blank starts the document with no blocks at all instead of the seed:
	// the editor's "new doc" path (BlockSuite's createDefaultDoc) builds the
	// root blocks itself in the creating browser, and a seed under them
	// would make two roots. Only safe for a client-chosen, brand-new id that
	// nobody else can be opening yet.
	Blank bool
}

// emptyUpdate is a Yjs V1 update with nothing in it: no structs, no deletes.
var emptyUpdate = []byte{0, 0}

// Patch changes a board's metadata; nil fields are left as they are.
type Patch struct {
	Title *string `json:"title,omitempty"`
	// Folder moves the document into a folder ("" is the top level), out of
	// any parent document.
	Folder *string `json:"folder,omitempty"`
	// Parent moves the document inside another one ("" takes it out, to
	// the folder level).
	Parent *string `json:"parent,omitempty"`
	// Rank places it among its folder's contents.
	Rank *string `json:"rank,omitempty"`
	// Trashed moves the document to the trash (true) or restores it.
	Trashed *bool `json:"trashed,omitempty"`
	// Mode is how the document opens by default (edgeless or page).
	Mode *string `json:"mode,omitempty"`
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
	// folders is the folder tree (tombstones included); foldersDirty is set
	// while it has changes the backend has not got (see folders.go).
	folders      map[string]Folder
	foldersDirty bool
	foldersGen   uint64
	// catalogVersion grows with every change to the catalog — documents
	// created, moved, ordered, renamed, folders — so browsers know to fetch
	// the list again (it rides on the status stream).
	catalogVersion uint64
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
	// links are the documents this one links to (see blocks.Doc.Links),
	// derived from the state and kept in memory only.
	links []string
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
		folders:   map[string]Folder{},
		changed:   make(chan struct{}, 1),
		listeners: map[int]func(){},
	}
	for _, sn := range snaps {
		b := &board{meta: sn.Meta, state: sn.State}
		normMeta(&b.meta)
		// Documents stored before titles lived in the document carry their
		// title only in the catalog: write it into the document once.
		if b.meta.Title != "" && len(b.state) > 0 && DocTitle(b.state) == "" {
			if upd, err := titleUpdate(b.state, b.meta.Title); err == nil && upd != nil {
				if merged, err := MergeStates(b.state, upd); err == nil && merged != nil {
					b.state = merged
					s.markLocked(b, "")
				}
			}
		}
		b.links = linksOf(b.meta.ID, b.state)
		s.boards[b.meta.ID] = b
	}
	if fb, ok := backend.(FolderBackend); ok {
		fs, err := fb.LoadFolders(ctx)
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			s.folders[f.ID] = f
		}
	}
	return s, nil
}

func normMeta(m *Meta) {
	if m.Mode != ModePage {
		m.Mode = ModeEdgeless
	}
}

// List returns every board, most recently updated first.
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meta, 0, len(s.boards))
	for _, b := range s.boards {
		if !b.meta.Deleted {
			out = append(out, cloneMeta(b.meta))
		}
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
	b, ok := s.liveLocked(id)
	if !ok {
		return Meta{}, ErrNotFound
	}
	return cloneMeta(b.meta), nil
}

// Create adds a document that starts from its mode's seed state, with the
// title written into it. actor may be empty.
func (s *Store) Create(in CreateInput, actor string) (Meta, error) {
	if in.Mode == "" {
		in.Mode = ModeEdgeless
	}
	if in.Mode != ModeEdgeless && in.Mode != ModePage {
		return Meta{}, fmt.Errorf("%w: mode %q", ErrInvalid, in.Mode)
	}
	if in.ID != "" && !ValidID(in.ID) {
		return Meta{}, fmt.Errorf("%w: id %q", ErrInvalid, in.ID)
	}
	title := strings.TrimSpace(in.Title)
	state := seed.For(in.Mode)
	if in.Blank {
		if in.ID == "" {
			return Meta{}, fmt.Errorf("%w: a blank document needs a client-chosen id", ErrInvalid)
		}
		state = bytes.Clone(emptyUpdate)
	} else if title != "" {
		upd, err := titleUpdate(state, title)
		if err != nil {
			return Meta{}, err
		}
		if upd != nil {
			merged, err := MergeStates(state, upd)
			if err != nil {
				return Meta{}, err
			}
			if merged != nil {
				state = merged
			}
		}
	}

	s.mu.Lock()
	if !s.liveFolderLocked(in.Folder) {
		s.mu.Unlock()
		return Meta{}, fmt.Errorf("%w: no folder %q", ErrInvalid, in.Folder)
	}
	if in.Parent != "" {
		if _, ok := s.liveLocked(in.Parent); !ok {
			s.mu.Unlock()
			return Meta{}, fmt.Errorf("%w: no document %q", ErrInvalid, in.Parent)
		}
		in.Folder = ""
	}
	id := in.ID
	if id == "" {
		id = newID()
		for s.boards[id] != nil {
			id = newID()
		}
	} else if s.boards[id] != nil {
		s.mu.Unlock()
		return Meta{}, ErrExists
	}
	now := s.now().UTC()
	b := &board{
		meta:  Meta{ID: id, Mode: in.Mode, Title: title, Folder: in.Folder, Parent: in.Parent, Created: now, Updated: now},
		state: state,
	}
	b.links = linksOf(id, state)
	s.boards[id] = b
	s.catalogVersion++
	s.markLocked(b, actor)
	m := cloneMeta(b.meta)
	s.mu.Unlock()
	s.notify()
	return m, nil
}

// Update applies a metadata patch. A new title is written into the
// document; the returned update (nil when the title did not change) is what
// the document's live room must be sent.
func (s *Store) Update(id string, p Patch, actor string) (Meta, []byte, error) {
	s.mu.Lock()
	b, ok := s.liveLocked(id)
	if !ok {
		s.mu.Unlock()
		return Meta{}, nil, ErrNotFound
	}
	m := b.meta
	var upd []byte
	if p.Title != nil {
		title := strings.TrimSpace(*p.Title)
		cur := b.state
		if len(cur) == 0 {
			cur = seed.For(m.Mode)
		}
		var err error
		if upd, err = titleUpdate(cur, title); err != nil {
			s.mu.Unlock()
			return Meta{}, nil, err
		}
		if upd != nil {
			merged, err := MergeStates(cur, upd)
			if err != nil {
				s.mu.Unlock()
				return Meta{}, nil, err
			}
			if merged != nil {
				b.state = merged
			}
		}
		m.Title = title
	}
	if p.Rank != nil {
		if !ValidRank(*p.Rank) {
			s.mu.Unlock()
			return Meta{}, nil, fmt.Errorf("%w: rank %q", ErrInvalid, *p.Rank)
		}
		m.Rank = *p.Rank
	}
	if p.Mode != nil {
		if *p.Mode != ModeEdgeless && *p.Mode != ModePage {
			s.mu.Unlock()
			return Meta{}, nil, fmt.Errorf("%w: mode %q", ErrInvalid, *p.Mode)
		}
		m.Mode = *p.Mode
	}
	if p.Trashed != nil {
		if *p.Trashed {
			if m.Trashed.IsZero() {
				m.Trashed = s.now().UTC()
			}
		} else {
			m.Trashed = time.Time{}
		}
	}
	if p.Folder != nil {
		if !s.liveFolderLocked(*p.Folder) {
			s.mu.Unlock()
			return Meta{}, nil, fmt.Errorf("%w: no folder %q", ErrInvalid, *p.Folder)
		}
		m.Folder = *p.Folder
		m.Parent = ""
	}
	if p.Parent != nil && *p.Parent != "" {
		if err := s.canHoldLocked(*p.Parent, id); err != nil {
			s.mu.Unlock()
			return Meta{}, nil, err
		}
		m.Parent = *p.Parent
		m.Folder = ""
	} else if p.Parent != nil {
		m.Parent = ""
	}
	m.Updated = s.now().UTC()
	b.meta = m
	s.catalogVersion++
	s.markLocked(b, actor)
	out := cloneMeta(m)
	s.mu.Unlock()
	s.notify()
	return out, upd, nil
}

// LoadState returns a board's Yjs state (the seed for a board that never
// stored one).
func (s *Store) LoadState(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.liveLocked(id)
	if !ok {
		return nil, ErrNotFound
	}
	if len(b.state) == 0 {
		return seed.For(b.meta.Mode), nil
	}
	return bytes.Clone(b.state), nil
}

// ApplyUpdate merges a Yjs update into a board's state. It reports whether
// the state actually changed: an update the state already contains (a peer
// re-sending what it synced) leaves the board untouched and clean. editors
// are the logins the change is attributed to.
func (s *Store) ApplyUpdate(id string, update []byte, editors ...string) (bool, error) {
	s.mu.Lock()
	b, ok := s.liveLocked(id)
	if !ok {
		s.mu.Unlock()
		return false, ErrNotFound
	}
	cur := b.state
	if len(cur) == 0 {
		cur = seed.For(b.meta.Mode)
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
	// One reading of the state gives both the title and the links.
	if t := s.relinkLocked(b); t != b.meta.Title {
		b.meta.Title = t
		s.catalogVersion++
	}
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
			normMeta(&m)
			s.boards[id] = &board{meta: m, state: bytes.Clone(r.State), links: linksOf(id, r.State)}
			s.catalogVersion++
			continue
		}
		newer := r.Meta.Updated.After(b.meta.Updated)
		switch {
		case b.meta.Deleted && !newer:
			// Deleted here after what the remote has: it stays deleted.
			continue
		case r.Meta.Deleted && newer:
			// Deleted there after what we have.
			b.meta = r.Meta
			b.state = nil
			b.links = nil
			s.catalogVersion++
			continue
		case r.Meta.Deleted:
			// A remote tombstone older than our edits: ours stands.
			continue
		}
		if newer {
			m := r.Meta
			normMeta(&m)
			b.meta = m
			s.catalogVersion++
		}
		if len(r.State) == 0 {
			continue
		}
		cur := b.state
		if len(cur) == 0 {
			cur = seed.For(b.meta.Mode)
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
		if t := s.relinkLocked(b); t != "" && t != b.meta.Title {
			b.meta.Title = t
			s.catalogVersion++
		}
		gained[id] = diff
	}
	return gained, nil
}

// Append adds blocks at the end of a document: a page gets them below its
// text, a board as a new note on the canvas. It returns the update, for the
// document's live room.
func (s *Store) Append(id string, specs []blocks.Spec, actor string) ([]byte, error) {
	m, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	state, err := s.LoadState(id)
	if err != nil {
		return nil, err
	}
	var upd []byte
	if m.Mode == ModePage {
		upd, err = blocks.AppendToPage(state, specs)
	} else {
		upd, err = blocks.AddBoardNote(state, specs)
	}
	if err != nil {
		return nil, err
	}
	// The update merges as a CRDT, so an edit that lands in between is kept.
	if _, err := s.ApplyUpdate(id, upd, actor); err != nil {
		return nil, err
	}
	return upd, nil
}

// Link makes child a child of parent the way AFFiNE does: a line with a
// reference to it at the end of parent's body. A child it already links to
// is left alone (nil update).
func (s *Store) Link(parent, child, actor string) ([]byte, error) {
	return s.PlaceChild(parent, child, blocks.Place{}, actor)
}

// PlaceChild puts child inside parent at place among parent's children —
// moving its link line there when it has one, adding one when not.
func (s *Store) PlaceChild(parent, child string, place blocks.Place, actor string) ([]byte, error) {
	if parent == child {
		return nil, fmt.Errorf("%w: a document cannot hold itself", ErrInvalid)
	}
	if _, err := s.Get(child); err != nil {
		return nil, err
	}
	return s.editState(parent, actor, func(state []byte) ([]byte, error) {
		return blocks.PlaceLink(state, child, place)
	})
}

// Copy makes a separate document from id, inside into, with the same
// content, and points into's links to id at the copy: a shortcut becomes a
// document of its own there. It returns the copy and the update for into.
func (s *Store) Copy(id, into, actor string) (Meta, []byte, error) {
	// Where the copy goes is checked first: a copy left behind by a failed
	// call would be a stray document.
	if into == "" || into == id {
		return Meta{}, nil, fmt.Errorf("%w: a copy goes inside another document", ErrInvalid)
	}
	if _, err := s.Get(into); err != nil {
		return Meta{}, nil, err
	}
	src, err := s.Get(id)
	if err != nil {
		return Meta{}, nil, err
	}
	state, err := s.LoadState(id)
	if err != nil {
		return Meta{}, nil, err
	}
	m, err := s.Create(CreateInput{Mode: src.Mode, Parent: into, Blank: false}, actor)
	if err != nil {
		return Meta{}, nil, err
	}
	// The copy's own state is the source's: same blocks, same title.
	s.mu.Lock()
	b := s.boards[m.ID]
	b.state = bytes.Clone(state)
	b.meta.Title = src.Title
	b.links = linksOf(m.ID, b.state)
	s.catalogVersion++
	s.markLocked(b, actor)
	m = cloneMeta(b.meta)
	s.mu.Unlock()
	s.notify()
	upd, err := s.editState(into, actor, func(st []byte) ([]byte, error) {
		return blocks.Retarget(st, id, m.ID)
	})
	return m, upd, err
}

// ErrInlineLink is returned for taking a child out of a document that
// mentions it in its text: that link is the author's to remove.
var ErrInlineLink = blocks.ErrInlineLink

// Unlink takes child out of parent: the link lines and cards for it go
// from parent's body (nil update when there are none).
func (s *Store) Unlink(parent, child, actor string) ([]byte, error) {
	return s.editState(parent, actor, func(state []byte) ([]byte, error) {
		return blocks.Unlink(state, child)
	})
}

// editState applies what edit makes of a document's state (nil: nothing).
func (s *Store) editState(id, actor string, edit func([]byte) ([]byte, error)) ([]byte, error) {
	state, err := s.LoadState(id)
	if err != nil {
		return nil, err
	}
	upd, err := edit(state)
	if err != nil || upd == nil {
		return nil, err
	}
	// The update merges as a CRDT, so an edit that lands in between is kept.
	if _, err := s.ApplyUpdate(id, upd, actor); err != nil {
		return nil, err
	}
	return upd, nil
}

// ErrNotTrashed is returned for deleting a document for good that is not
// in the trash.
var ErrNotTrashed = errors.New("only a document in the trash can be deleted")

// Delete removes a trashed document for good. What stays is a tombstone
// (its metadata, marked deleted): replicas that still have the document
// learn it is gone instead of bringing it back. Links to it in other
// documents are left as they are.
func (s *Store) Delete(id, actor string) error {
	s.mu.Lock()
	b, ok := s.liveLocked(id)
	if !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	if b.meta.Trashed.IsZero() {
		s.mu.Unlock()
		return ErrNotTrashed
	}
	b.meta = Meta{ID: id, Mode: b.meta.Mode, Created: b.meta.Created, Updated: s.now().UTC(), Trashed: b.meta.Trashed, Deleted: true}
	b.state = nil
	b.links = nil
	s.catalogVersion++
	s.markLocked(b, actor)
	s.mu.Unlock()
	s.notify()
	return nil
}

// canHoldLocked reports whether parent may hold doc: it is there, and it
// is neither doc nor inside doc (no document goes inside itself).
func (s *Store) canHoldLocked(parent, doc string) error {
	if _, ok := s.liveLocked(parent); !ok {
		return fmt.Errorf("%w: no document %q", ErrInvalid, parent)
	}
	seen := map[string]bool{}
	for at := parent; at != "" && !seen[at]; {
		if at == doc {
			return fmt.Errorf("%w: a document cannot go inside itself", ErrInvalid)
		}
		seen[at] = true
		b := s.boards[at]
		if b == nil {
			break
		}
		at = b.meta.Parent
	}
	return nil
}

// liveLocked is a document that is there (not deleted for good).
func (s *Store) liveLocked(id string) (*board, bool) {
	b, ok := s.boards[id]
	if !ok || b.meta.Deleted {
		return nil, false
	}
	return b, true
}

// Links returns, per document, the documents it links to in order of
// appearance (documents that link to nothing are left out).
func (s *Store) Links() map[string][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]string{}
	for id, b := range s.boards {
		if len(b.links) > 0 {
			out[id] = slices.Clone(b.links)
		}
	}
	return out
}

// relinkLocked rederives a board's links after its state changed (a change
// bumps the catalog version, so clients redraw the tree) and returns the
// title written in the state, read on the same pass.
func (s *Store) relinkLocked(b *board) string {
	title, l := docInfo(b.meta.ID, b.state)
	if !slices.Equal(l, b.links) {
		b.links = l
		s.catalogVersion++
	}
	return title
}

// linksOf is what the document id links to, a link to itself left out.
func linksOf(id string, state []byte) []string {
	_, l := docInfo(id, state)
	return l
}

// docInfo reads a state once for its title and its links.
func docInfo(id string, state []byte) (string, []string) {
	if len(state) == 0 {
		return "", nil
	}
	d, err := blocks.Load(state)
	if err != nil {
		return "", nil
	}
	return d.Title(), slices.DeleteFunc(d.Links(), func(l string) bool { return l == id })
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
		state = seed.For(b.meta.Mode)
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

// CatalogVersion grows whenever the catalog changes (see catalogVersion).
func (s *Store) CatalogVersion() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalogVersion
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
	if s.foldersDirty {
		n++
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
	// The folder tree goes with every full flush (a flush of named boards —
	// a room closing — leaves it to the next tick).
	if len(ids) == 0 {
		if err := s.flushFolders(ctx); err != nil {
			return err
		}
		defer s.notify()
	}
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

func cloneMeta(m Meta) Meta { return m }

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

// ValidRank reports whether a rank key is well formed: digits and lower
// case letters (base 36, compared bytewise), not ending in '0' (so there is
// always room for another key between two), at most 64 characters; "" is
// "no rank".
func ValidRank(r string) bool {
	if r == "" {
		return true
	}
	if len(r) > 64 || strings.HasSuffix(r, "0") {
		return false
	}
	for _, c := range r {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
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
