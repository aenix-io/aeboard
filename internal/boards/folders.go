package boards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Folders arrange documents in a tree. They are metadata, not directories:
// the documents stay in the flat boards/<id>/ and say which folder they are
// in (Meta.Folder); the tree itself is one file, folders.json. Moving a
// document or a whole folder is then a one-line change, and replicas merge
// folder by folder.
//
// A deleted folder stays in the file as a tombstone, so a merge with a
// replica that still has it cannot bring it back. Only an empty folder can
// be deleted; a document a replica moved into a folder another replica
// deleted shows at the top level (see FolderOf).

// FoldersFile is the folder tree's file, at the backend's root.
const FoldersFile = "folders.json"

// Folder is one folder.
type Folder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Parent is the containing folder; "" is the top level.
	Parent string `json:"parent"`
	// Rank orders the folder among its parent's contents (see ValidRank).
	Rank    string    `json:"rank,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Deleted bool      `json:"deleted,omitempty"`
}

// FolderPatch changes a folder; nil fields are left as they are.
type FolderPatch struct {
	Name   *string `json:"name,omitempty"`
	Parent *string `json:"parent,omitempty"`
	Rank   *string `json:"rank,omitempty"`
}

// FolderBackend is the optional part of a Backend that stores folders.
type FolderBackend interface {
	LoadFolders(ctx context.Context) ([]Folder, error)
	SaveFolders(ctx context.Context, folders []Folder) error
}

// ErrNotEmpty is returned when deleting a folder that still holds something.
var ErrNotEmpty = errors.New("folder is not empty")

// EncodeFolders renders the tree as folders.json (sorted by id, tombstones
// included).
func EncodeFolders(folders []Folder) ([]byte, error) {
	fs := slices.Clone(folders)
	slices.SortFunc(fs, func(a, b Folder) int { return strings.Compare(a.ID, b.ID) })
	out, err := json.MarshalIndent(struct {
		Folders []Folder `json:"folders"`
	}{fs}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// DecodeFolders reads folders.json.
func DecodeFolders(data []byte) ([]Folder, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var f struct {
		Folders []Folder `json:"folders"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", FoldersFile, err)
	}
	return f.Folders, nil
}

// MergeFolders combines two copies of the tree: per folder, the copy updated
// last wins (a tombstone included). It reports whether the result differs
// from ours.
func MergeFolders(ours, theirs []Folder) ([]Folder, bool) {
	byID := map[string]Folder{}
	for _, f := range ours {
		byID[f.ID] = f
	}
	changed := false
	for _, t := range theirs {
		o, ok := byID[t.ID]
		if !ok || t.Updated.After(o.Updated) {
			byID[t.ID] = t
			changed = true
		}
	}
	out := make([]Folder, 0, len(byID))
	for _, f := range byID {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b Folder) int { return strings.Compare(a.ID, b.ID) })
	return out, changed
}

// Folders returns the live folders, by name.
func (s *Store) Folders() []Folder {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Folder, 0, len(s.folders))
	for _, f := range s.folders {
		if !f.Deleted {
			out = append(out, f)
		}
	}
	slices.SortFunc(out, func(a, b Folder) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// liveFolderLocked reports whether id names a live folder ("" — the top
// level — always does).
func (s *Store) liveFolderLocked(id string) bool {
	if id == "" {
		return true
	}
	f, ok := s.folders[id]
	return ok && !f.Deleted
}

// CreateFolder adds a folder under parent ("" for the top level).
func (s *Store) CreateFolder(name, parent string) (Folder, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Folder{}, fmt.Errorf("%w: a folder needs a name", ErrInvalid)
	}
	s.mu.Lock()
	if !s.liveFolderLocked(parent) {
		s.mu.Unlock()
		return Folder{}, fmt.Errorf("%w: no folder %q", ErrInvalid, parent)
	}
	id := newID()
	for _, taken := s.folders[id]; taken || s.boards[id] != nil; _, taken = s.folders[id] {
		id = newID()
	}
	now := s.now().UTC()
	f := Folder{ID: id, Name: name, Parent: parent, Created: now, Updated: now}
	s.folders[id] = f
	s.markFoldersLocked()
	s.mu.Unlock()
	s.notify()
	return f, nil
}

// UpdateFolder renames or moves a folder. A folder cannot be moved into
// itself or into one of its own subfolders.
func (s *Store) UpdateFolder(id string, p FolderPatch) (Folder, error) {
	s.mu.Lock()
	f, ok := s.folders[id]
	if !ok || f.Deleted {
		s.mu.Unlock()
		return Folder{}, ErrNotFound
	}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		if name == "" {
			s.mu.Unlock()
			return Folder{}, fmt.Errorf("%w: a folder needs a name", ErrInvalid)
		}
		f.Name = name
	}
	if p.Rank != nil {
		if !ValidRank(*p.Rank) {
			s.mu.Unlock()
			return Folder{}, fmt.Errorf("%w: rank %q", ErrInvalid, *p.Rank)
		}
		f.Rank = *p.Rank
	}
	if p.Parent != nil {
		parent := *p.Parent
		if !s.liveFolderLocked(parent) {
			s.mu.Unlock()
			return Folder{}, fmt.Errorf("%w: no folder %q", ErrInvalid, parent)
		}
		for at := parent; at != ""; at = s.folders[at].Parent {
			if at == id {
				s.mu.Unlock()
				return Folder{}, fmt.Errorf("%w: a folder cannot go inside itself", ErrInvalid)
			}
		}
		f.Parent = parent
	}
	f.Updated = s.now().UTC()
	s.folders[id] = f
	s.markFoldersLocked()
	s.mu.Unlock()
	s.notify()
	return f, nil
}

// DeleteFolder removes an empty folder (no documents, no live subfolders).
func (s *Store) DeleteFolder(id string) error {
	s.mu.Lock()
	f, ok := s.folders[id]
	if !ok || f.Deleted {
		s.mu.Unlock()
		return ErrNotFound
	}
	// Documents in the trash do not count: the folder looks empty, and a
	// trashed one restored later shows at the top level (see FolderOf).
	for _, b := range s.boards {
		if b.meta.Folder == id && b.meta.Trashed.IsZero() && !b.meta.Deleted {
			s.mu.Unlock()
			return ErrNotEmpty
		}
	}
	for _, o := range s.folders {
		if o.Parent == id && !o.Deleted {
			s.mu.Unlock()
			return ErrNotEmpty
		}
	}
	f.Deleted = true
	f.Updated = s.now().UTC()
	s.folders[id] = f
	s.markFoldersLocked()
	s.mu.Unlock()
	s.notify()
	return nil
}

// FolderOf is where a document shows: its folder, or the top level when
// that folder is gone (deleted on another replica while the document moved
// into it here).
func (s *Store) FolderOf(m Meta) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.liveFolderLocked(m.Folder) {
		return m.Folder
	}
	return ""
}

// MergeRemoteFolders folds another replica's tree into ours (see
// MergeFolders). The merged tree is not dirty: the remote has it, and what
// we had on top is already in our own commits.
func (s *Store) MergeRemoteFolders(remote []Folder) {
	s.mu.Lock()
	ours := make([]Folder, 0, len(s.folders))
	for _, f := range s.folders {
		ours = append(ours, f)
	}
	merged, changed := MergeFolders(ours, remote)
	if changed {
		s.folders = map[string]Folder{}
		for _, f := range merged {
			s.folders[f.ID] = f
		}
		s.catalogVersion++
	}
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

func (s *Store) markFoldersLocked() {
	s.catalogVersion++
	s.foldersDirty = true
	s.foldersGen++
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// flushFolders writes the tree when it changed and the backend stores
// folders.
func (s *Store) flushFolders(ctx context.Context) error {
	fb, ok := s.backend.(FolderBackend)
	if !ok {
		return nil
	}
	s.mu.Lock()
	if !s.foldersDirty {
		s.mu.Unlock()
		return nil
	}
	gen := s.foldersGen
	all := make([]Folder, 0, len(s.folders))
	for _, f := range s.folders {
		all = append(all, f)
	}
	s.mu.Unlock()
	if err := fb.SaveFolders(ctx, all); err != nil {
		return err
	}
	s.mu.Lock()
	if s.foldersGen == gen {
		s.foldersDirty = false
	}
	s.mu.Unlock()
	return nil
}
