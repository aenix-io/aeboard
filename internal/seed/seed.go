// Package seed holds the states new documents start from.
//
// seed.bin (a board) and page.bin (a page) are Yjs V1 updates with
// BlockSuite's root blocks — affine:page and affine:surface, and for a page
// also one affine:note with an empty paragraph — written by clientID 0 with
// fixed block ids by web/scripts/gen-seed.ts. Handing the same bytes to every
// new document makes a concurrent first open safe: two browsers that both
// start from the seed hold identical Yjs items, so their states merge into
// one root, not two.
package seed

import _ "embed"

var (
	//go:embed seed.bin
	board []byte
	//go:embed page.bin
	page []byte
)

// Update returns a copy of the board seed.
func Update() []byte { return For("") }

// For returns a copy of the seed for a document mode: "page" or the board
// ("edgeless", or "" for documents stored before modes existed).
func For(mode string) []byte {
	if mode == "page" {
		return append([]byte(nil), page...)
	}
	return append([]byte(nil), board...)
}
