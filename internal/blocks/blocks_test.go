package blocks

import (
	"os"
	"strings"
	"testing"

	"github.com/reearth/ygo/crdt"

	"github.com/aenix-io/aenotes/internal/seed"
)

func sample(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/sample.bin")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func link(id string) (string, string) { return "Other doc", "https://notes.example/b/" + id }

// The sample is a page BlockSuite wrote: every kind of block reads back as
// the Markdown a person would write for it.
func TestMarkdownOfABlockSuitePage(t *testing.T) {
	d, err := Load(sample(t))
	if err != nil {
		t.Fatal(err)
	}
	if d.Title() != "Sample" {
		t.Fatalf("title = %q", d.Title())
	}
	got := d.Markdown(true, link)
	want := "# Heading\n\n" +
		"plain **bold** [link](https://example.com)\n\n" +
		"- bullet\n\n" +
		"1. number\n\n" +
		"- [x] done\n\n" +
		"```go\nfmt.Println(1)\n```\n\n" +
		"> quoted\n\n" +
		"---\n\n" +
		"[Other doc](https://notes.example/b/abcdefghjk)\n"
	if got != want {
		t.Fatalf("markdown:\n%s\nwant:\n%s", got, want)
	}
}

func TestParseMarkdown(t *testing.T) {
	specs := Parse("## Plan\n\nSome *soft* and **hard** with `code` and ~~gone~~ and [a link](https://x.y) and [a doc](/b/abcdefghjk).\n\n" +
		"- one\n  - nested\n- [ ] open\n- [x] closed\n\n1. first\n2. second\n\n> said\n\n```sh\nls -la\n```\n\n---\n")
	kinds := []string{}
	for _, s := range specs {
		kinds = append(kinds, s.Flavour+"/"+s.Type)
	}
	want := "affine:paragraph/h2 affine:paragraph/text affine:list/bulleted affine:list/todo affine:list/todo affine:list/numbered affine:list/numbered affine:paragraph/quote affine:code/ affine:divider/"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("blocks = %v", kinds)
	}
	if !specs[3].Checked == false || !specs[4].Checked || specs[3].Checked {
		t.Fatalf("todo checks = %v %v", specs[3].Checked, specs[4].Checked)
	}
	if len(specs[2].Children) != 1 || plain(specs[2].Children[0].Text) != "nested" {
		t.Fatalf("nested list = %+v", specs[2].Children)
	}
	if specs[8].Language != "sh" || plain(specs[8].Text) != "ls -la" {
		t.Fatalf("code = %+v", specs[8])
	}
	marks := map[string]bool{}
	for _, op := range specs[1].Text {
		for k := range op.Attributes {
			marks[k] = true
		}
	}
	for _, k := range []string{"italic", "bold", "code", "strike", "link", "reference"} {
		if !marks[k] {
			t.Errorf("mark %s lost: %+v", k, specs[1].Text)
		}
	}
}

// Blocks written here read back as what was written, on a seeded page and on
// the BlockSuite sample (appended after its own blocks).
func TestAppendToPageRoundTrips(t *testing.T) {
	src := "# Agenda\n\n- ship **pages**\n- [ ] review\n\nSee [the board](/b/abcdefghjk).\n"
	for name, state := range map[string][]byte{"seed": seed.For("page"), "sample": sample(t)} {
		upd, err := AppendToPage(state, Parse(src))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		doc := crdt.New()
		_ = crdt.ApplyUpdateV1(doc, state, nil)
		if err := crdt.ApplyUpdateV1(doc, upd, nil); err != nil {
			t.Fatal(err)
		}
		d, err := Load(doc.EncodeStateAsUpdate())
		if err != nil {
			t.Fatal(err)
		}
		got := d.Markdown(true, link)
		if !strings.HasSuffix(got, "# Agenda\n\n- ship **pages**\n- [ ] review\n\nSee [Other doc](https://notes.example/b/abcdefghjk).\n") {
			t.Fatalf("%s: markdown after append:\n%s", name, got)
		}
	}
}

// New blocks carry the keys BlockSuite gives the same blocks, so the editor
// takes them as its own.
func TestWrittenBlocksHaveBlockSuitesKeys(t *testing.T) {
	ref, _ := Load(sample(t))
	keysOf := func(d *Doc, flavour string) string {
		var walk func(b *Block) string
		walk = func(b *Block) string {
			if b.Flavour == flavour {
				m, _ := d.ymap(b.ID)
				return strings.Join(sortedKeys(m), ",")
			}
			for _, c := range b.Children {
				if k := walk(c); k != "" {
					return k
				}
			}
			return ""
		}
		return walk(d.Root)
	}
	upd, err := AddBoardNote(seed.For("edgeless"), Parse("# t\n\ntext\n\n- l\n\n```\nc\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	doc := crdt.New()
	_ = crdt.ApplyUpdateV1(doc, seed.For("edgeless"), nil)
	_ = crdt.ApplyUpdateV1(doc, upd, nil)
	mine, err := Load(doc.EncodeStateAsUpdate())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"affine:note", "affine:paragraph", "affine:list", "affine:code"} {
		if a, b := keysOf(ref, f), keysOf(mine, f); a != b {
			t.Errorf("%s keys:\n BlockSuite %s\n ours       %s", f, a, b)
		}
	}
	notes := mine.Notes(false)
	if len(notes) != 1 || notes[0].Props["displayMode"] != "edgeless" || len(mine.Notes(true)) != 0 {
		t.Fatalf("board note = %+v", notes)
	}
}

func TestBoardNotesGoRightOfWhatIsThere(t *testing.T) {
	state := seed.For("edgeless")
	for range 2 {
		upd, err := AddBoardNote(state, Parse("note"))
		if err != nil {
			t.Fatal(err)
		}
		doc := crdt.New()
		_ = crdt.ApplyUpdateV1(doc, state, nil)
		_ = crdt.ApplyUpdateV1(doc, upd, nil)
		state = doc.EncodeStateAsUpdate()
	}
	d, _ := Load(state)
	notes := d.Notes(false)
	if len(notes) != 2 || notes[0].Props["xywh"] == notes[1].Props["xywh"] || notes[0].Props["index"] == notes[1].Props["index"] {
		t.Fatalf("notes overlap: %v %v", notes[0].Props, notes[1].Props)
	}
}

func sortedKeys(m *crdt.YMap) []string {
	k := m.Keys()
	for i := range k {
		for j := i + 1; j < len(k); j++ {
			if k[j] < k[i] {
				k[i], k[j] = k[j], k[i]
			}
		}
	}
	return k
}
