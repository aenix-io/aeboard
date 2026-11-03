// Package gitstore keeps aeboard's state in a git repository through
// go-git's object store alone — blobs, trees and commits written directly,
// never a worktree — and syncs it with one remote branch.
//
// The commit path (tree editing, one commit per action), the sync layer
// (manual upload-pack fetch offering only the known remote tip as a have,
// shallow-boundary handling, push, maintenance) and the on-disk open/clone
// are copied from aeman's pkg/gitstore (Apache-2.0) with its domain parts
// removed. Instead of aeman's field-wise rebase, a diverged replica records
// a merge commit whose content the caller has already merged.
package gitstore

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
)

// Identity is a name/email pair for author or committer.
type Identity struct {
	Name  string
	Email string
}

// Options configures a Repo.
type Options struct {
	// Committer is the server's identity; it is also the author of
	// unattributed actions.
	Committer Identity
	// AuthorEmail renders an actor's login as an email; nil gives
	// <login>@users.noreply.github.com.
	AuthorEmail func(login string) string
	// Branch is the ref commits land on; empty means refs/heads/main.
	Branch plumbing.ReferenceName
}

// Repo is one repository: a go-git object store and the branch the state
// lives on. Writes are serialised — one writer per repository.
type Repo struct {
	s      storage.Storer
	opts   Options
	branch plumbing.ReferenceName
	// mu serialises writes to the branch and the object store's layout
	// (commit, merge, reset, maintenance).
	mu sync.Mutex
	// syncMu serialises network operations against the remote.
	syncMu sync.Mutex
	// remoteSet caches the ancestors of remoteSetTip for Unpushed.
	setMu        sync.Mutex
	remoteSet    map[plumbing.Hash]bool
	remoteSetTip plumbing.Hash
}

// Action is what one commit records.
type Action struct {
	// Actor is the login; empty means the server acted on its own and is
	// the author.
	Actor string
	// Summary is the first line of the message.
	Summary string
	// At is the commit date. Zero means now.
	At time.Time
	// Trailers are extra "Aeboard-Key: value" lines, written sorted by
	// key. Keys must start with "Aeboard-".
	Trailers map[string]string
}

// FileWrite is one file's new content; nil Data deletes the file, and any
// directory left empty by it.
type FileWrite struct {
	Path string
	Data []byte
}

// ErrNotFound is returned by readers for a missing path.
var ErrNotFound = errors.New("gitstore: not found")

const trailerPrefix = "Aeboard-"

// Init creates the repository in an empty storer, HEAD on the branch.
func Init(s storage.Storer, opts Options) (*Repo, error) {
	if _, err := git.Init(s, nil); err != nil {
		return nil, fmt.Errorf("gitstore: init: %w", err)
	}
	r := wrap(s, opts)
	if err := s.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, r.branch)); err != nil {
		return nil, err
	}
	return r, nil
}

// Open wraps an existing repository.
func Open(s storage.Storer, opts Options) *Repo {
	return wrap(s, opts)
}

func wrap(s storage.Storer, opts Options) *Repo {
	if opts.Branch == "" {
		opts.Branch = plumbing.NewBranchReferenceName("main")
	}
	if opts.AuthorEmail == nil {
		opts.AuthorEmail = func(login string) string { return login + "@users.noreply.github.com" }
	}
	return &Repo{s: s, opts: opts, branch: opts.Branch}
}

// Branch is the ref the state lives on.
func (r *Repo) Branch() plumbing.ReferenceName { return r.branch }

// Head is the branch tip, or the zero hash when the branch is unborn.
func (r *Repo) Head() plumbing.Hash {
	ref, err := r.s.Reference(r.branch)
	if err != nil {
		return plumbing.ZeroHash
	}
	return ref.Hash()
}

// Commit applies the writes on top of the branch tip and records them as
// one commit. It returns the zero hash — and makes no commit — when the
// writes change nothing: the same bytes again, or a delete of a file that
// is not there.
func (r *Repo) Commit(a Action, writes []FileWrite) (plumbing.Hash, error) {
	msg, err := a.message()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	head := r.Head()
	tree, changed, err := r.buildTree(head, writes)
	if err != nil || !changed {
		return plumbing.ZeroHash, err
	}
	var parents []plumbing.Hash
	if !head.IsZero() {
		parents = []plumbing.Hash{head}
	}
	return r.writeCommit(a, msg, tree, parents)
}

// Merge records a merge of theirs into the branch: a commit with parents
// [Head(), theirs] whose tree is theirs' tree with writes applied on top —
// the caller passes the merged content of every path that differs.
//
// When the branch is unborn, or theirs already contains it (a fast-forward
// would do), the commit sits on theirs alone; if the writes then change
// nothing, the branch simply moves to theirs and theirs is returned.
// theirs must be a non-zero commit present in the local store and not
// already part of the branch.
func (r *Repo) Merge(theirs plumbing.Hash, a Action, writes []FileWrite) (plumbing.Hash, error) {
	if theirs.IsZero() {
		return plumbing.ZeroHash, errors.New("gitstore: merge: theirs is the zero hash")
	}
	msg, err := a.message()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := object.GetCommit(r.s, theirs); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("gitstore: merge: %s: %w", theirs, err)
	}
	head := r.Head()
	parents := []plumbing.Hash{head, theirs}
	if head.IsZero() {
		parents = []plumbing.Hash{theirs}
	} else {
		ff, err := r.IsAncestor(head, theirs)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if ff {
			parents = []plumbing.Hash{theirs}
		} else if in, err := r.IsAncestor(theirs, head); err != nil {
			return plumbing.ZeroHash, err
		} else if in {
			return plumbing.ZeroHash, fmt.Errorf("gitstore: merge: %s is already in the branch", theirs)
		}
	}
	tree, changed, err := r.buildTree(theirs, writes)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if !changed && len(parents) == 1 {
		return theirs, r.s.SetReference(plumbing.NewHashReference(r.branch, theirs))
	}
	return r.writeCommit(a, msg, tree, parents)
}

// buildTree applies writes to base's tree and returns the new root tree and
// whether anything changed.
func (r *Repo) buildTree(base plumbing.Hash, writes []FileWrite) (plumbing.Hash, bool, error) {
	root, err := r.loadRoot(base)
	if err != nil {
		return plumbing.ZeroHash, false, err
	}
	changed := false
	for _, w := range writes {
		ch, err := r.apply(root, w)
		if err != nil {
			return plumbing.ZeroHash, false, err
		}
		changed = changed || ch
	}
	if !changed {
		return root.hash, false, nil
	}
	h, err := r.writeTree(root)
	if err != nil {
		return plumbing.ZeroHash, false, err
	}
	if h.IsZero() {
		// Everything was deleted: git still needs a tree object.
		if h, err = r.putObject(&object.Tree{}); err != nil {
			return plumbing.ZeroHash, false, err
		}
	}
	return h, true, nil
}

func (r *Repo) writeCommit(a Action, msg string, tree plumbing.Hash, parents []plumbing.Hash) (plumbing.Hash, error) {
	when := a.At
	if when.IsZero() {
		when = time.Now()
	}
	committer := object.Signature{Name: r.opts.Committer.Name, Email: r.opts.Committer.Email, When: when}
	author := committer
	if a.Actor != "" {
		author = object.Signature{Name: a.Actor, Email: r.opts.AuthorEmail(a.Actor), When: when}
	}
	h, err := r.putObject(&object.Commit{
		Author:       author,
		Committer:    committer,
		Message:      msg,
		TreeHash:     tree,
		ParentHashes: parents,
	})
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if err := r.s.SetReference(plumbing.NewHashReference(r.branch, h)); err != nil {
		return plumbing.ZeroHash, err
	}
	return h, nil
}

type encoder interface {
	Encode(plumbing.EncodedObject) error
}

func (r *Repo) putObject(v encoder) (plumbing.Hash, error) {
	o := r.s.NewEncodedObject()
	if err := v.Encode(o); err != nil {
		return plumbing.ZeroHash, err
	}
	return r.s.SetEncodedObject(o)
}

// message renders the summary and the trailer block.
func (a Action) message() (string, error) {
	if strings.ContainsAny(a.Summary, "\r\n") {
		return "", fmt.Errorf("gitstore: summary %q spans lines", a.Summary)
	}
	keys := make([]string, 0, len(a.Trailers))
	for k, v := range a.Trailers {
		if !strings.HasPrefix(k, trailerPrefix) || len(k) == len(trailerPrefix) || strings.ContainsAny(k, ": \t\r\n") {
			return "", fmt.Errorf("gitstore: bad trailer key %q (want %s<Name>)", k, trailerPrefix)
		}
		if strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("gitstore: trailer %s spans lines", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys) // a stable order keeps identical actions byte-identical
	var b strings.Builder
	b.WriteString(a.Summary)
	b.WriteString("\n")
	if len(keys) > 0 {
		b.WriteString("\n")
	}
	for _, k := range keys {
		b.WriteString(k + ": " + a.Trailers[k] + "\n")
	}
	return b.String(), nil
}

// ResetTo moves the branch to a commit present in the local store — the
// adoption of a fetched remote tip.
func (r *Repo) ResetTo(h plumbing.Hash) error {
	if _, err := object.GetCommit(r.s, h); err != nil {
		return fmt.Errorf("gitstore: reset to %s: %w", h, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.s.SetReference(plumbing.NewHashReference(r.branch, h))
}

// IsAncestor reports whether a is b or one of its ancestors. History cut by
// a shallow boundary, or not present locally, is not walked: the answer is
// then false, not an error. The zero hash is an ancestor of everything.
func (r *Repo) IsAncestor(a, b plumbing.Hash) (bool, error) {
	if a.IsZero() {
		return true, nil
	}
	if b.IsZero() {
		return false, nil
	}
	found := false
	err := r.walk(b, func(h plumbing.Hash) bool {
		if h == a {
			found = true
		}
		return !found
	})
	return found, err
}

// walk visits b and its ancestors once each, breadth-first, without
// crossing shallow boundaries or missing objects; visit returns false to
// stop the whole walk.
func (r *Repo) walk(b plumbing.Hash, visit func(plumbing.Hash) bool) error {
	return r.walkUntil([]plumbing.Hash{b}, nil, visit, true)
}

// walkUntil walks from starts, skipping (and not expanding) commits in
// stop; visit returning false ends the walk when abort is set, otherwise it
// only stops the expansion of that commit.
func (r *Repo) walkUntil(starts []plumbing.Hash, stop map[plumbing.Hash]bool, visit func(plumbing.Hash) bool, abort bool) error {
	shallowList, err := r.s.Shallow()
	if err != nil {
		return err
	}
	shallow := make(map[plumbing.Hash]bool, len(shallowList))
	for _, h := range shallowList {
		shallow[h] = true
	}
	seen := map[plumbing.Hash]bool{}
	queue := append([]plumbing.Hash(nil), starts...)
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if h.IsZero() || seen[h] || stop[h] {
			continue
		}
		seen[h] = true
		c, err := object.GetCommit(r.s, h)
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			continue // cut off: not in the local history
		}
		if err != nil {
			return err
		}
		if !visit(h) {
			if abort {
				return nil
			}
			continue
		}
		if !shallow[h] {
			queue = append(queue, c.ParentHashes...)
		}
	}
	return nil
}

// ---- reads ------------------------------------------------------------------

// ReadFile returns a file's bytes at the branch tip.
func (r *Repo) ReadFile(p string) ([]byte, error) {
	return r.ReadFileAt(r.Head(), p)
}

// ReadFileAt returns a file's bytes at a commit; ErrNotFound when the path
// is not a file there (or the commit is zero).
func (r *Repo) ReadFileAt(commit plumbing.Hash, p string) ([]byte, error) {
	t, err := r.treeOf(commit)
	if err != nil || t == nil {
		if err == nil {
			err = ErrNotFound
		}
		return nil, err
	}
	f, err := t.File(p)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) || errors.Is(err, object.ErrDirectoryNotFound) || errors.Is(err, object.ErrEntryNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rd, err := f.Reader()
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rd); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ListDir lists the entry names — files and directories — directly under
// dir at a commit, sorted; "" is the root. A missing directory (or a zero
// commit) lists as empty.
func (r *Repo) ListDir(commit plumbing.Hash, dir string) ([]string, error) {
	t, err := r.treeOf(commit)
	if err != nil || t == nil {
		return nil, err
	}
	dir = strings.Trim(path.Clean("/"+dir), "/")
	if dir != "" {
		t, err = t.Tree(dir)
		if errors.Is(err, object.ErrDirectoryNotFound) || errors.Is(err, object.ErrEntryNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(t.Entries))
	for _, e := range t.Entries {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out, nil
}

// ChangedPaths lists the file paths whose content differs between two
// commits' trees, sorted. A zero from means every file in to.
func (r *Repo) ChangedPaths(from, to plumbing.Hash) ([]string, error) {
	fromTree, err := r.treeOf(from)
	if err != nil {
		return nil, err
	}
	toTree, err := r.treeOf(to)
	if err != nil {
		return nil, err
	}
	changes, err := object.DiffTree(fromTree, toTree)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, ch := range changes {
		name := ch.To.Name
		if name == "" {
			name = ch.From.Name
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// treeOf returns a commit's tree, or nil for the zero hash.
func (r *Repo) treeOf(h plumbing.Hash) (*object.Tree, error) {
	if h.IsZero() {
		return nil, nil //nolint:nilnil // nil tree is "nothing", the diff handles it
	}
	c, err := object.GetCommit(r.s, h)
	if err != nil {
		return nil, err
	}
	return c.Tree()
}

// ---- tree editing -------------------------------------------------------------

// dir is a directory being edited: its entries, with subdirectories loaded
// only along the paths that change. dirty marks a directory whose tree
// object must be rewritten.
type dir struct {
	entries map[string]*entry
	dirty   bool
	loaded  bool
	hash    plumbing.Hash // the tree object this dir came from, if any
}

type entry struct {
	mode filemode.FileMode
	hash plumbing.Hash
	sub  *dir // non-nil once a subdirectory has been loaded for editing
}

func (r *Repo) loadRoot(head plumbing.Hash) (*dir, error) {
	if head.IsZero() {
		return &dir{entries: map[string]*entry{}, loaded: true}, nil
	}
	c, err := object.GetCommit(r.s, head)
	if err != nil {
		return nil, err
	}
	d := &dir{hash: c.TreeHash}
	return d, r.load(d)
}

// load reads a directory's entries from its tree object.
func (r *Repo) load(d *dir) error {
	if d.loaded {
		return nil
	}
	d.entries = map[string]*entry{}
	d.loaded = true
	if d.hash.IsZero() {
		return nil
	}
	t, err := object.GetTree(r.s, d.hash)
	if err != nil {
		return err
	}
	for _, e := range t.Entries {
		d.entries[e.Name] = &entry{mode: e.Mode, hash: e.Hash}
	}
	return nil
}

// apply performs one write on the tree model and reports whether anything
// actually changed.
func (r *Repo) apply(root *dir, w FileWrite) (bool, error) {
	clean := path.Clean(w.Path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") {
		return false, fmt.Errorf("gitstore: bad path %q", w.Path)
	}
	parts := strings.Split(clean, "/")
	d := root
	chain := []*dir{root}
	for _, name := range parts[:len(parts)-1] {
		e := d.entries[name]
		if e == nil {
			if w.Data == nil {
				return false, nil // deleting under a directory that does not exist
			}
			e = &entry{mode: filemode.Dir, sub: &dir{entries: map[string]*entry{}, loaded: true}}
			d.entries[name] = e
		}
		if e.mode != filemode.Dir {
			return false, fmt.Errorf("gitstore: %q is a file in the way of %q", name, w.Path)
		}
		if e.sub == nil {
			e.sub = &dir{hash: e.hash}
			if err := r.load(e.sub); err != nil {
				return false, err
			}
		}
		d = e.sub
		chain = append(chain, d)
	}
	name := parts[len(parts)-1]
	cur := d.entries[name]
	if w.Data == nil {
		if cur == nil {
			return false, nil
		}
		delete(d.entries, name)
	} else {
		if cur != nil && cur.mode == filemode.Dir {
			return false, fmt.Errorf("gitstore: %q is a directory", w.Path)
		}
		h, err := r.putBlob(w.Data)
		if err != nil {
			return false, err
		}
		if cur != nil && cur.mode == filemode.Regular && cur.hash == h {
			return false, nil
		}
		d.entries[name] = &entry{mode: filemode.Regular, hash: h}
	}
	for _, x := range chain {
		x.dirty = true
	}
	return true, nil
}

func (r *Repo) putBlob(data []byte) (plumbing.Hash, error) {
	o := r.s.NewEncodedObject()
	o.SetType(plumbing.BlobObject)
	o.SetSize(int64(len(data)))
	w, err := o.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(data); err != nil {
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return r.s.SetEncodedObject(o)
}

// writeTree writes every dirty directory bottom-up and returns the root
// hash. A directory left empty is pruned from its parent (zero hash).
func (r *Repo) writeTree(d *dir) (plumbing.Hash, error) {
	if !d.dirty {
		return d.hash, nil
	}
	t := &object.Tree{}
	for name, e := range d.entries {
		if e.sub != nil && e.sub.dirty {
			h, err := r.writeTree(e.sub)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			if h.IsZero() {
				delete(d.entries, name) // pruned: nothing left underneath
				continue
			}
			e.hash = h
		}
		t.Entries = append(t.Entries, object.TreeEntry{Name: name, Mode: e.mode, Hash: e.hash})
	}
	if len(t.Entries) == 0 {
		return plumbing.ZeroHash, nil
	}
	sortTreeEntries(t.Entries)
	h, err := r.putObject(t)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	d.hash = h
	d.dirty = false
	return h, nil
}

// sortTreeEntries puts entries in git's canonical order: byte order of the
// name, with a directory compared as if its name ended in "/".
func sortTreeEntries(es []object.TreeEntry) {
	key := func(e object.TreeEntry) string {
		if e.Mode == filemode.Dir {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(es, func(i, j int) bool { return key(es[i]) < key(es[j]) })
}

// CommitObject reads a commit (for callers that show history).
func (r *Repo) CommitObject(h plumbing.Hash) (*object.Commit, error) {
	return object.GetCommit(r.s, h)
}
