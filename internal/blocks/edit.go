package blocks

import (
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"

	"github.com/reearth/ygo/crdt"
)

// Edits produce a Yjs update against a document's state: merged into the
// store and sent to the live room, it lands next to whatever people are
// typing at the same moment (a CRDT, not a replace).

// AppendToPage adds blocks at the end of the page: into the last note the
// page shows, or a new page note when it shows none.
func AppendToPage(state []byte, specs []Spec) ([]byte, error) {
	d, err := Load(state)
	if err != nil {
		return nil, err
	}
	notes := d.Notes(true)
	if len(notes) == 0 {
		return d.addNote(specs, "both", "[0,0,800,92]")
	}
	// ygo: a read takes the document lock that Transact holds, so every
	// container is resolved before the transaction.
	arr, ok := d.childrenOf(notes[len(notes)-1].ID)
	if !ok {
		return nil, fmt.Errorf("blocks: note %s has no children list", notes[len(notes)-1].ID)
	}
	return d.transact(func(txn *crdt.Transaction) {
		for _, sp := range specs {
			arr.Push(txn, []any{d.build(txn, sp)})
		}
	}), nil
}

// AddBoardNote puts a new note with the blocks on the board's canvas, to
// the right of everything already there. It shows on the canvas only (not
// in the board's page view).
func AddBoardNote(state []byte, specs []Spec) ([]byte, error) {
	d, err := Load(state)
	if err != nil {
		return nil, err
	}
	x, y := d.freeSpot()
	return d.addNote(specs, "edgeless", fmt.Sprintf("[%d,%d,498,92]", x, y))
}

func (d *Doc) transact(fn func(*crdt.Transaction)) []byte {
	sv := d.doc.StateVector()
	d.doc.Transact(fn)
	return crdt.EncodeStateAsUpdateV1(d.doc, sv)
}

// childrenOf is a block's sys:children list (read outside a transaction).
func (d *Doc) childrenOf(id string) (*crdt.YArray, bool) {
	m, ok := d.ymap(id)
	if !ok {
		return nil, false
	}
	c, _ := m.Get("sys:children")
	arr, ok := c.(*crdt.YArray)
	return arr, ok
}

// addNote creates an affine:note under the root, with BlockSuite's
// defaults, holding the blocks.
func (d *Doc) addNote(specs []Spec, displayMode, xywh string) ([]byte, error) {
	rootChildren, ok := d.childrenOf(d.Root.ID)
	if !ok {
		return nil, ErrNoRoot
	}
	index := d.nextIndex()
	return d.transact(func(txn *crdt.Transaction) {
		id := newID()
		m := crdt.NewMapPrelim()
		m.Set(txn, "sys:id", id)
		m.Set(txn, "sys:flavour", "affine:note")
		m.Set(txn, "sys:version", int64(1))
		children := crdt.NewArrayPrelim()
		for _, sp := range specs {
			children.Push(txn, []any{d.build(txn, sp)})
		}
		m.Set(txn, "sys:children", children)
		m.Set(txn, "prop:xywh", xywh)
		bg := crdt.NewMapPrelim()
		bg.Set(txn, "dark", "#252525")
		bg.Set(txn, "light", "#ffffff")
		m.Set(txn, "prop:background", bg)
		m.Set(txn, "prop:index", index)
		m.Set(txn, "prop:lockedBySelf", false)
		m.Set(txn, "prop:hidden", false)
		m.Set(txn, "prop:displayMode", displayMode)
		style := crdt.NewMapPrelim()
		style.Set(txn, "borderRadius", int64(8))
		style.Set(txn, "borderSize", int64(4))
		style.Set(txn, "borderStyle", "none")
		style.Set(txn, "shadowType", "--affine-note-shadow-box")
		edgeless := crdt.NewMapPrelim()
		edgeless.Set(txn, "style", style)
		m.Set(txn, "prop:edgeless", edgeless)
		d.blocks.Set(txn, id, m)
		rootChildren.Push(txn, []any{id})
	}), nil
}

// build creates a block and its children (children first, into its still
// detached list) and returns its id.
func (d *Doc) build(txn *crdt.Transaction, sp Spec) string {
	id := newID()
	m := crdt.NewMapPrelim()
	m.Set(txn, "sys:id", id)
	m.Set(txn, "sys:flavour", sp.Flavour)
	m.Set(txn, "sys:version", int64(1))
	children := crdt.NewArrayPrelim()
	for _, c := range sp.Children {
		children.Push(txn, []any{d.build(txn, c)})
	}
	m.Set(txn, "sys:children", children)
	switch sp.Flavour {
	case "affine:paragraph":
		m.Set(txn, "prop:type", orDefault(sp.Type, "text"))
		m.Set(txn, "prop:text", textOf(txn, sp.Text))
		m.Set(txn, "prop:collapsed", false)
	case "affine:list":
		m.Set(txn, "prop:type", orDefault(sp.Type, "bulleted"))
		m.Set(txn, "prop:text", textOf(txn, sp.Text))
		m.Set(txn, "prop:checked", sp.Checked)
		m.Set(txn, "prop:collapsed", false)
		m.Set(txn, "prop:order", nil)
	case "affine:code":
		m.Set(txn, "prop:text", textOf(txn, sp.Text))
		m.Set(txn, "prop:language", sp.Language)
		m.Set(txn, "prop:wrap", false)
		m.Set(txn, "prop:caption", "")
	}
	d.blocks.Set(txn, id, m)
	return id
}

func textOf(txn *crdt.Transaction, delta []crdt.Delta) *crdt.YText {
	t := crdt.NewTextPrelim()
	at := 0
	var prev crdt.Attributes
	for _, op := range delta {
		str, ok := op.Insert.(string)
		if !ok || str == "" {
			continue
		}
		// An insert takes the formatting on its left unless told otherwise:
		// a mark the previous run had and this one does not is cleared.
		attrs := crdt.Attributes{}
		maps.Copy(attrs, op.Attributes)
		for k := range prev {
			if _, ok := attrs[k]; !ok {
				attrs[k] = nil
			}
		}
		t.Insert(txn, at, str, attrs)
		prev = op.Attributes
		at += utf16Len(str) // Yjs counts text in UTF-16 units
	}
	return t
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// nextIndex is a z-index key above every note's (BlockSuite's fractional
// keys: a key with a suffix sorts after the key itself).
func (d *Doc) nextIndex() string {
	maxKey := "a0"
	for _, n := range d.Notes(false) {
		if k, ok := n.Props["index"].(string); ok && k > maxKey {
			maxKey = k
		}
	}
	return maxKey + "V"
}

// freeSpot is a point right of every note on the canvas, at the topmost
// note's height (the origin on an empty board).
func (d *Doc) freeSpot() (int, int) {
	right, top := math.MinInt, math.MaxInt
	for _, n := range d.Notes(false) {
		xywh, _ := n.Props["xywh"].(string)
		f := strings.Split(strings.Trim(xywh, "[]"), ",")
		if len(f) != 4 {
			continue
		}
		x, _ := strconv.ParseFloat(strings.TrimSpace(f[0]), 64)
		y, _ := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
		w, _ := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
		right = max(right, int(x+w))
		top = min(top, int(y))
	}
	if right == math.MinInt {
		return 0, 0
	}
	return right + 40, top
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}
