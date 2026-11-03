// Package seed holds the state every new board starts from.
//
// seed.bin is a Yjs V1 update with BlockSuite's root blocks (affine:page and
// affine:surface), written by clientID 0 with fixed block ids by
// web/scripts/gen-seed.ts. Handing the same bytes to every empty board makes
// a concurrent first open safe: two browsers that both start from the seed
// hold identical Yjs items, so their states merge into one root, not two.
package seed

import _ "embed"

//go:embed seed.bin
var update []byte

// Update returns a copy of the seed update.
func Update() []byte {
	return append([]byte(nil), update...)
}
