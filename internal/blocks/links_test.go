package blocks

import (
	"errors"
	"strings"
	"testing"

	"github.com/reearth/ygo/crdt"

	"github.com/aenix-io/aenotes/internal/seed"
)

// apply folds an update into a state.
func apply(t *testing.T, state, upd []byte) []byte {
	t.Helper()
	if upd == nil {
		return state
	}
	doc := crdt.New()
	_ = crdt.ApplyUpdateV1(doc, state, nil)
	if err := crdt.ApplyUpdateV1(doc, upd, nil); err != nil {
		t.Fatal(err)
	}
	return doc.EncodeStateAsUpdate()
}

func linksIn(t *testing.T, state []byte) string {
	t.Helper()
	d, err := Load(state)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(d.Links(), ",")
}

func place(t *testing.T, state []byte, child string, p Place) []byte {
	t.Helper()
	upd, err := PlaceLink(state, child, p)
	if err != nil {
		t.Fatal(err)
	}
	return apply(t, state, upd)
}

// Links are placed and reordered among the others; one already in place
// is left alone.
func TestPlaceLink(t *testing.T) {
	s := seed.For("page")
	s = place(t, s, "aaaaaaaaaa", Place{})
	s = place(t, s, "bbbbbbbbbb", Place{})
	s = place(t, s, "cccccccccc", Place{First: true})
	if got := linksIn(t, s); got != "cccccccccc,aaaaaaaaaa,bbbbbbbbbb" {
		t.Fatalf("after first: %s", got)
	}
	s = place(t, s, "cccccccccc", Place{After: "bbbbbbbbbb"})
	if got := linksIn(t, s); got != "aaaaaaaaaa,bbbbbbbbbb,cccccccccc" {
		t.Fatalf("moved last: %s", got)
	}
	s = place(t, s, "bbbbbbbbbb", Place{Before: "aaaaaaaaaa"})
	if got := linksIn(t, s); got != "bbbbbbbbbb,aaaaaaaaaa,cccccccccc" {
		t.Fatalf("moved first: %s", got)
	}
	if upd, err := PlaceLink(s, "aaaaaaaaaa", Place{After: "bbbbbbbbbb"}); err != nil || upd != nil {
		t.Fatalf("in place: %v %v", upd, err)
	}
	if upd, err := PlaceLink(s, "aaaaaaaaaa", Place{}); err != nil || upd != nil {
		t.Fatalf("already linked, no place asked: %v %v", upd, err)
	}
	// Moving keeps one line per child, not a copy.
	d, _ := Load(s)
	if n := len(d.Notes(true)[0].Children); n != 4 { // the seed's empty line + three
		t.Fatalf("blocks in the note: %d", n)
	}
}

// Unlinking takes out link lines and cards, never a mention in a sentence.
func TestUnlink(t *testing.T) {
	s := seed.For("page")
	s = place(t, s, "aaaaaaaaaa", Place{})
	upd, err := Unlink(s, "aaaaaaaaaa")
	if err != nil || upd == nil {
		t.Fatalf("unlink: %v", err)
	}
	if got := linksIn(t, apply(t, s, upd)); got != "" {
		t.Fatalf("left: %s", got)
	}
	if upd, err := Unlink(s, "zzzzzzzzzz"); err != nil || upd != nil {
		t.Fatalf("nothing to unlink: %v %v", upd, err)
	}
	// The sample mentions abcdefghjk on a line of its own and has a card for
	// mnpqrstvwx: both come out.
	for _, id := range []string{"abcdefghjk", "mnpqrstvwx"} {
		if _, err := Unlink(sample(t), id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	// A mention inside text stays.
	withText, err := AppendToPage(seed.For("page"), Parse("See [it](/b/aaaaaaaaaa) here."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Unlink(apply(t, seed.For("page"), withText), "aaaaaaaaaa"); !errors.Is(err, ErrInlineLink) {
		t.Fatalf("inline: %v", err)
	}
}
