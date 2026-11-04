package boards

import (
	"github.com/reearth/ygo/crdt"
)

// A document's title lives in its own state, as the text of the root
// block's prop:title (BlockSuite's doc title). The catalog copies it from
// there, so a title typed in the page's title line and one set through the
// API are the same thing.

// rootTitle finds the root block's title text in a loaded doc.
func rootTitle(doc *crdt.Doc) *crdt.YText {
	blocks := doc.GetMap("blocks")
	// Keys + Get, not Entries: Entries renders nested types to plain values.
	for _, k := range blocks.Keys() {
		v, _ := blocks.Get(k)
		b, ok := v.(*crdt.YMap)
		if !ok {
			continue
		}
		if f, _ := b.Get("sys:flavour"); f != "affine:page" {
			continue
		}
		if t, ok := b.Get("prop:title"); ok {
			if txt, ok := t.(*crdt.YText); ok {
				return txt
			}
		}
	}
	return nil
}

// DocTitle returns the title written in a state ("" when there is none).
func DocTitle(state []byte) string {
	doc := crdt.New()
	if err := crdt.ApplyUpdateV1(doc, state, nil); err != nil {
		return ""
	}
	if t := rootTitle(doc); t != nil {
		return t.ToString()
	}
	return ""
}

// titleUpdate returns the Yjs update that sets the title in state to title,
// or nil when it already reads so (or the state has no title text).
func titleUpdate(state []byte, title string) ([]byte, error) {
	doc := crdt.New()
	if err := crdt.ApplyUpdateV1(doc, state, nil); err != nil {
		return nil, err
	}
	t := rootTitle(doc)
	if t == nil || t.ToString() == title {
		return nil, nil
	}
	sv := doc.StateVector()
	doc.Transact(func(txn *crdt.Transaction) {
		if n := t.Len(); n > 0 {
			t.Delete(txn, 0, n)
		}
		if title != "" {
			t.Insert(txn, 0, title, nil)
		}
	})
	return crdt.EncodeStateAsUpdateV1(doc, sv), nil
}
