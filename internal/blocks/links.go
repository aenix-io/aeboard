package blocks

import (
	"errors"
	"strings"

	"github.com/reearth/ygo/crdt"
)

// The tree in the sidebar is the documents' links, so moving a document in
// the tree edits its parent's body. What the tree may move is a link block:
// a line that holds nothing but references to the child (what "+" and a
// drop add) or a linked/synced doc card. A mention inside a sentence is a
// link too, but it is the author's text: the tree never cuts it out.

// Place is where a link goes among a document's links: before or after the
// link to another document, first, or (all empty) at the end.
type Place struct {
	Before, After string
	First         bool
}

// ErrInlineLink is returned for moving a child out of a document that
// mentions it inside its text: only the author can take that link out.
var ErrInlineLink = errors.New("blocks: the document mentions it in its text")

// at is a block's position: its parent block and index among the parent's
// children.
type at struct {
	parent *Block
	index  int
	block  *Block
}

// positions lists every block under root with where it sits, in document
// order.
func positions(root *Block) []at {
	var out []at
	var walk func(p *Block)
	walk = func(p *Block) {
		for i, c := range p.Children {
			out = append(out, at{parent: p, index: i, block: c})
			walk(c)
		}
	}
	walk(root)
	return out
}

// linkBlock reports whether b is a block the tree may move for child: a
// card for it, or a line of nothing but references to it.
func linkBlock(b *Block, child string) bool {
	switch b.Flavour {
	case "affine:embed-linked-doc", "affine:embed-synced-doc":
		id, _ := b.Props["pageId"].(string)
		return id == child
	case "affine:paragraph":
		if len(b.Children) > 0 {
			return false
		}
		refs := 0
		for _, op := range b.Text {
			if ref, ok := op.Attributes["reference"].(map[string]any); ok {
				if id, _ := ref["pageId"].(string); id != child {
					return false
				}
				refs++
				continue
			}
			if s, _ := op.Insert.(string); strings.TrimSpace(s) != "" {
				return false
			}
		}
		return refs > 0
	}
	return false
}

// mentions reports whether b's own text references child.
func mentions(b *Block, child string) bool {
	for _, op := range b.Text {
		if ref, ok := op.Attributes["reference"].(map[string]any); ok {
			if id, _ := ref["pageId"].(string); id == child {
				return true
			}
		}
	}
	return false
}

// PlaceLink puts the link to child at place: link blocks to it move there
// (merged into one), or a new link line is added. A document with no note
// to hold it gets one.
func PlaceLink(state []byte, child string, place Place) ([]byte, error) {
	d, err := Load(state)
	if err != nil {
		return nil, err
	}
	all := positions(d.Root)
	var old []at
	for _, p := range all {
		if linkBlock(p.block, child) {
			old = append(old, p)
		}
	}
	// Where the link goes: next to the anchor's link (its link block, or the
	// block that mentions it), first in the first note, or at the end.
	var parent *Block
	index := -1
	if anchor := place.Before + place.After; anchor != "" && anchor != child {
		found := false
		for pass := 0; pass < 2 && !found; pass++ {
			for _, p := range all {
				if (pass == 0 && linkBlock(p.block, anchor)) || (pass == 1 && mentions(p.block, anchor)) {
					parent, index, found = p.parent, p.index, true
					if place.After != "" {
						index++
					}
					break
				}
			}
		}
	}
	if parent == nil && place.First {
		notes := d.Notes(true)
		if len(notes) == 0 {
			notes = d.Notes(false)
		}
		if len(notes) > 0 {
			parent, index = notes[0], 0
		}
	}
	if parent == nil {
		if len(old) > 0 && place == (Place{}) {
			return nil, nil // already linked, and the end is any place
		}
		notes := d.Notes(true)
		if len(notes) == 0 {
			notes = d.Notes(false)
		}
		if len(notes) == 0 {
			// No note at all: one is made with the line in it.
			return d.addNote([]Spec{Reference(child)}, "both", "[0,0,800,92]")
		}
		parent = notes[len(notes)-1]
		index = len(parent.Children)
	}
	// The one link block that stays in place when it already is there.
	if len(old) == 1 && old[0].parent == parent && (old[0].index == index || old[0].index+1 == index) {
		return nil, nil
	}

	arrays := map[string]*crdt.YArray{}
	for _, p := range append(old, at{parent: parent}) {
		if _, ok := arrays[p.parent.ID]; ok {
			continue
		}
		arr, ok := d.childrenOf(p.parent.ID)
		if !ok {
			return nil, ErrNoRoot
		}
		arrays[p.parent.ID] = arr
	}
	return d.transact(func(txn *crdt.Transaction) {
		// Remove the old link blocks, last first so indexes hold; one
		// removed before the insertion point in the same parent moves it up.
		for i := len(old) - 1; i >= 0; i-- {
			p := old[i]
			arrays[p.parent.ID].Delete(txn, p.index, 1)
			d.blocks.Delete(txn, p.block.ID)
			if p.parent == parent && p.index < index {
				index--
			}
		}
		arrays[parent.ID].Insert(txn, index, []any{d.build(txn, Reference(child))})
	}), nil
}

// Unlink takes the link blocks to child out of the document. A mention
// inside the text stays, and makes it ErrInlineLink (the child is still
// linked from here). Nothing to take out is a nil update.
func Unlink(state []byte, child string) ([]byte, error) {
	d, err := Load(state)
	if err != nil {
		return nil, err
	}
	var old []at
	inline := false
	for _, p := range positions(d.Root) {
		switch {
		case linkBlock(p.block, child):
			old = append(old, p)
		case mentions(p.block, child):
			inline = true
		}
	}
	if inline {
		return nil, ErrInlineLink
	}
	if len(old) == 0 {
		return nil, nil
	}
	arrays := map[string]*crdt.YArray{}
	for _, p := range old {
		if _, ok := arrays[p.parent.ID]; ok {
			continue
		}
		arr, ok := d.childrenOf(p.parent.ID)
		if !ok {
			return nil, ErrNoRoot
		}
		arrays[p.parent.ID] = arr
	}
	return d.transact(func(txn *crdt.Transaction) {
		for i := len(old) - 1; i >= 0; i-- {
			p := old[i]
			arrays[p.parent.ID].Delete(txn, p.index, 1)
			d.blocks.Delete(txn, p.block.ID)
		}
	}), nil
}

// Retarget points the document's links to from at to instead: references
// in text and linked or synced doc cards. A shortcut turned into a copy of
// its own keeps its place this way.
func Retarget(state []byte, from, to string) ([]byte, error) {
	d, err := Load(state)
	if err != nil {
		return nil, err
	}
	type textRef struct {
		text      *crdt.YText
		at, n     int
		reference map[string]any
	}
	var texts []textRef
	var cards []*crdt.YMap
	for _, p := range positions(d.Root) {
		b := p.block
		m, ok := d.ymap(b.ID)
		if !ok {
			continue
		}
		if b.Flavour == "affine:embed-linked-doc" || b.Flavour == "affine:embed-synced-doc" {
			if id, _ := b.Props["pageId"].(string); id == from {
				cards = append(cards, m)
			}
			continue
		}
		v, _ := m.Get("prop:text")
		yt, ok := v.(*crdt.YText)
		if !ok {
			continue
		}
		pos := 0
		for _, op := range b.Text {
			// An embed in the text (not a string) takes one position.
			n := 1
			if s, ok := op.Insert.(string); ok {
				n = utf16Len(s)
			}
			if ref, ok := op.Attributes["reference"].(map[string]any); ok {
				if id, _ := ref["pageId"].(string); id == from {
					next := map[string]any{}
					for k, v := range ref {
						next[k] = v
					}
					next["pageId"] = to
					texts = append(texts, textRef{text: yt, at: pos, n: n, reference: next})
				}
			}
			pos += n
		}
	}
	if len(texts) == 0 && len(cards) == 0 {
		return nil, nil
	}
	return d.transact(func(txn *crdt.Transaction) {
		for _, r := range texts {
			r.text.Format(txn, r.at, r.n, crdt.Attributes{"reference": r.reference})
		}
		for _, m := range cards {
			m.Set(txn, "prop:pageId", to)
		}
	}), nil
}
