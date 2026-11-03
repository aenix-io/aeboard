package ytext

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reearth/ygo/crdt"
	"github.com/reearth/ygo/encoding"
)

// roundTrip is the contract: text back to the very same bytes.
func roundTrip(t *testing.T, name string, update []byte) []byte {
	t.Helper()
	text, err := Marshal(update)
	if err != nil {
		t.Fatalf("%s: marshal: %v", name, err)
	}
	back, err := Unmarshal(text)
	if err != nil {
		t.Fatalf("%s: unmarshal: %v\n%s", name, err, text)
	}
	if !bytes.Equal(back, update) {
		t.Fatalf("%s: round trip differs (%d vs %d bytes)\n%s", name, len(back), len(update), text)
	}
	return text
}

// Real BlockSuite boards: the seed, shapes, and a board with a sticker and
// a full board template (notes, text, connectors, frames, images).
func TestRoundTripRealBoards(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.bin")
	if len(files) < 4 {
		t.Fatalf("fixtures missing: %v", files)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := roundTrip(t, f, b)
		// And the text is what it claims: one line per struct, readable.
		if !strings.HasPrefix(string(text), `{"format":"aeboard-yjs-v1"}`) {
			t.Fatalf("%s: no format line", f)
		}
	}
}

func TestTextIsReadable(t *testing.T) {
	b, _ := os.ReadFile("testdata/templates.bin")
	text, _ := Marshal(b)
	for _, want := range []string{`"parent":"blocks"`, `"string":"5W2H`, `"type":{"ref":1}`} {
		if !strings.Contains(string(text), want) {
			t.Errorf("text lacks %s", want)
		}
	}
}

// Edits of every Yjs type through ygo, with deletions (a delete set and
// deleted content), formatting, embeds and nested types.
func TestRoundTripEveryTypeAndDeletes(t *testing.T) {
	doc := crdt.New(crdt.WithClientID(42))
	m := doc.GetMap("blocks")
	txt := doc.GetText("text")
	arr := doc.GetArray("arr")
	doc.Transact(func(txn *crdt.Transaction) {
		m.Set(txn, "s", "hello")
		m.Set(txn, "i", int64(7))
		m.Set(txn, "neg", int64(-12345678))
		m.Set(txn, "f", 0.5)
		m.Set(txn, "whole", 3.0)
		m.Set(txn, "big", 1e300)
		m.Set(txn, "nil", nil)
		m.Set(txn, "b", true)
		m.Set(txn, "bytes", []byte{0, 1, 2, 255})
		m.Set(txn, "obj", map[string]any{"z": 1, "a": []any{"x", 2.5, false}})
		m.Set(txn, "tagclash", map[string]any{"$y": "f32", "v": "1"})
		txt.Insert(txn, 0, "Привет, мир 👋 and more", nil)
		txt.Format(txn, 0, 6, crdt.Attributes{"bold": true})
		arr.Insert(txn, 0, []any{"a", "b", "c", int64(1)})
		nested := crdt.NewMapPrelim()
		m.Set(txn, "nested", nested)
	})
	doc.Transact(func(txn *crdt.Transaction) {
		txt.Delete(txn, 2, 3)
		arr.Delete(txn, 1, 2)
		m.Delete(txn, "s")
		m.Set(txn, "i", int64(8)) // overwrite: the old item is deleted
	})
	// A second client so there are several struct groups.
	other := crdt.New(crdt.WithClientID(7))
	_ = crdt.ApplyUpdateV1(other, doc.EncodeStateAsUpdate(), nil)
	om := other.GetMap("blocks")
	other.Transact(func(txn *crdt.Transaction) { om.Set(txn, "from7", "x") })
	_ = crdt.ApplyUpdateV1(doc, other.EncodeStateAsUpdate(), nil)

	roundTrip(t, "every type", doc.EncodeStateAsUpdate())

	// Also without GC: deleted items keep their content.
	nogc := crdt.New(crdt.WithGC(false))
	_ = crdt.ApplyUpdateV1(nogc, doc.EncodeStateAsUpdate(), nil)
	roundTrip(t, "no gc", nogc.EncodeStateAsUpdate())
}

// Values plain JSON cannot tell apart must keep their wire type.
func TestAnyKeepsWireTypes(t *testing.T) {
	cases := map[string]func(e *encoding.Encoder){
		"-0 float":  func(e *encoding.Encoder) { e.WriteUint8(anyFloat64); e.WriteFloat64(math.Copysign(0, -1)) },
		"NaN":       func(e *encoding.Encoder) { e.WriteUint8(anyFloat64); e.WriteFloat64(math.NaN()) },
		"+Inf":      func(e *encoding.Encoder) { e.WriteUint8(anyFloat64); e.WriteFloat64(math.Inf(1)) },
		"float32":   func(e *encoding.Encoder) { e.WriteUint8(anyFloat32); e.WriteFloat32(1.25) },
		"bigint":    func(e *encoding.Encoder) { e.WriteUint8(anyBigInt); e.WriteBigInt64(-9007199254740993) },
		"undefined": func(e *encoding.Encoder) { e.WriteUint8(anyUndefined) },
		"-0 int":    func(e *encoding.Encoder) { e.WriteUint8(anyInt); e.WriteUint8(0x40) },
		"1.0":       func(e *encoding.Encoder) { e.WriteUint8(anyFloat64); e.WriteFloat64(1) },
		"1e21":      func(e *encoding.Encoder) { e.WriteUint8(anyFloat64); e.WriteFloat64(1e21) },
	}
	for name, write := range cases {
		e := encoding.NewEncoder()
		// One group: 1 struct of client 1 at clock 0 — an Any item in root "m".
		e.WriteVarUint(1)
		e.WriteVarUint(1)
		e.WriteVarUint(1)
		e.WriteVarUint(0)
		e.WriteUint8(refAny | flagParentSub)
		e.WriteVarUint(1)
		e.WriteVarString("m")
		e.WriteVarString("k")
		e.WriteVarUint(1)
		write(e)
		e.WriteVarUint(0) // empty delete set
		roundTrip(t, name, e.Bytes())
	}
}

func TestUnmarshalRejectsGarbage(t *testing.T) {
	for _, in := range []string{
		"",
		`{"format":"something-else"}`,
		"{\"format\":\"aeboard-yjs-v1\"}\n{\"id\":[1,0]}\n",
		"{\"format\":\"aeboard-yjs-v1\"}\n{\"id\":[1,0],\"string\":\"a\"}\n",
		"{\"format\":\"aeboard-yjs-v1\"}\n{\"deleted\":[1,[[0,1]]]}\n{\"id\":[1,0],\"parent\":\"m\",\"string\":\"a\"}\n",
		"{\"format\":\"aeboard-yjs-v1\"}\n{\"id\":[1,0],\"parent\":\"m\",\"string\":\"ab\"}\n{\"id\":[1,5],\"left\":[1,1],\"string\":\"c\"}\n",
	} {
		if _, err := Unmarshal([]byte(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestMarshalRejectsTruncatedUpdate(t *testing.T) {
	b, _ := os.ReadFile("testdata/shapes.bin")
	for _, n := range []int{1, len(b) / 2, len(b) - 1} {
		if _, err := Marshal(b[:n]); err == nil {
			t.Errorf("accepted a %d-byte prefix", n)
		}
	}
	if _, err := Marshal(append(append([]byte{}, b...), 0)); err == nil {
		t.Error("accepted trailing bytes")
	}
}

// The text is stable JSON lines (git-friendly): every line parses alone.
func TestEveryLineIsJSON(t *testing.T) {
	b, _ := os.ReadFile("testdata/templates.bin")
	text, _ := Marshal(b)
	for i, line := range strings.Split(strings.TrimSuffix(string(text), "\n"), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("line %d is not JSON: %s", i+1, line)
		}
	}
}
