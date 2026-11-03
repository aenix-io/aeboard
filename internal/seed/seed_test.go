package seed

import (
	"encoding/json"
	"testing"

	"github.com/reearth/ygo/crdt"
)

// The seed must decode into exactly the two BlockSuite root blocks: one
// affine:page whose only child is one affine:surface. Anything else (a note,
// a second root) would show up on every new board.
func TestSeedHoldsOnePageWithOneSurface(t *testing.T) {
	doc := crdt.New()
	if err := crdt.ApplyUpdateV1(doc, Update(), nil); err != nil {
		t.Fatalf("apply seed: %v", err)
	}
	raw, err := doc.GetMap("blocks").ToJSON()
	if err != nil {
		t.Fatalf("blocks to JSON: %v", err)
	}
	var blocks map[string]map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("decode blocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d: %s", len(blocks), raw)
	}
	var page, surface map[string]any
	for _, b := range blocks {
		switch b["sys:flavour"] {
		case "affine:page":
			page = b
		case "affine:surface":
			surface = b
		}
	}
	if page == nil || surface == nil {
		t.Fatalf("want one affine:page and one affine:surface, got %s", raw)
	}
	children, _ := page["sys:children"].([]any)
	if len(children) != 1 || children[0] != surface["sys:id"] {
		t.Fatalf("page children = %v, want [%v]", page["sys:children"], surface["sys:id"])
	}
}

// Applying the seed twice must change nothing: that is what makes two
// replicas that both started from it converge on a single root.
func TestSeedIsIdempotent(t *testing.T) {
	once, twice := crdt.New(), crdt.New()
	_ = crdt.ApplyUpdateV1(once, Update(), nil)
	_ = crdt.ApplyUpdateV1(twice, Update(), nil)
	_ = crdt.ApplyUpdateV1(twice, Update(), nil)
	a, _ := once.GetMap("blocks").ToJSON()
	b, _ := twice.GetMap("blocks").ToJSON()
	if string(a) != string(b) {
		t.Fatalf("seed applied twice differs:\n%s\n%s", a, b)
	}
}
