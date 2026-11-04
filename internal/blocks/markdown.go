package blocks

import (
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"strings"

	"github.com/reearth/ygo/crdt"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// LinkResolver names another document for a reference: its title and URL.
type LinkResolver func(docID string) (title, url string)

// Markdown renders the document's notes — the page's (onPage) or every note
// on the canvas — as Markdown.
func (d *Doc) Markdown(onPage bool, link LinkResolver) string {
	var b strings.Builder
	// Every top-level block ends with a blank line, so notes need no
	// separator of their own.
	for _, n := range d.Notes(onPage) {
		writeBlocks(&b, n.Children, "", link)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeBlocks(b *strings.Builder, blocks []*Block, indent string, link LinkResolver) {
	num := 0
	for i, bl := range blocks {
		typ, _ := bl.Props["type"].(string)
		if bl.Flavour != "affine:list" || typ != "numbered" {
			num = 0
		}
		line := inline(bl.Text, link)
		switch bl.Flavour {
		case "affine:paragraph":
			switch {
			case len(typ) == 2 && typ[0] == 'h' && typ[1] >= '1' && typ[1] <= '6':
				fmt.Fprintf(b, "%s%s %s\n\n", indent, strings.Repeat("#", int(typ[1]-'0')), line)
			case typ == "quote":
				fmt.Fprintf(b, "%s> %s\n\n", indent, line)
			default:
				if line != "" {
					fmt.Fprintf(b, "%s%s\n\n", indent, line)
				}
			}
			writeBlocks(b, bl.Children, indent+"  ", link)
		case "affine:list":
			mark := "-"
			switch typ {
			case "numbered":
				num++
				mark = fmt.Sprintf("%d.", num)
			case "todo":
				if c, _ := bl.Props["checked"].(bool); c {
					mark = "- [x]"
				} else {
					mark = "- [ ]"
				}
			}
			fmt.Fprintf(b, "%s%s %s\n", indent, mark, line)
			writeBlocks(b, bl.Children, indent+"  ", link)
			// A run of items with the same marker (- or 1.) is one list:
			// a blank line only after it.
			if indent == "" && (i+1 == len(blocks) || blocks[i+1].Flavour != "affine:list" || (blocks[i+1].Props["type"] == "numbered") != (typ == "numbered")) {
				b.WriteString("\n")
			}
		case "affine:code":
			lang, _ := bl.Props["language"].(string)
			fmt.Fprintf(b, "%s```%s\n%s%s\n%s```\n\n", indent, lang, indent, strings.ReplaceAll(plain(bl.Text), "\n", "\n"+indent), indent)
		case "affine:divider":
			fmt.Fprintf(b, "%s---\n\n", indent)
		case "affine:embed-linked-doc", "affine:embed-synced-doc":
			id, _ := bl.Props["pageId"].(string)
			title, url := id, "/b/"+id
			if link != nil {
				title, url = link(id)
			}
			fmt.Fprintf(b, "%s[%s](%s)\n\n", indent, title, url)
		case "affine:image":
			fmt.Fprintf(b, "%s![image]\n\n", indent)
		default:
			if line != "" {
				fmt.Fprintf(b, "%s%s\n\n", indent, line)
			}
			writeBlocks(b, bl.Children, indent, link)
		}
	}
}

func plain(delta []crdt.Delta) string {
	var b strings.Builder
	for _, op := range delta {
		if s, ok := op.Insert.(string); ok {
			b.WriteString(s)
		}
	}
	return b.String()
}

// inline renders a text delta as Markdown inline syntax.
func inline(delta []crdt.Delta, link LinkResolver) string {
	var b strings.Builder
	for _, op := range delta {
		s, ok := op.Insert.(string)
		if !ok {
			continue
		}
		a := op.Attributes
		if ref, ok := a["reference"].(map[string]any); ok {
			id, _ := ref["pageId"].(string)
			title, url := id, "/b/"+id
			if link != nil {
				title, url = link(id)
			}
			fmt.Fprintf(&b, "[%s](%s)", title, url)
			continue
		}
		if a["code"] == true {
			s = "`" + s + "`"
		}
		if a["bold"] == true {
			s = "**" + s + "**"
		}
		if a["italic"] == true {
			s = "*" + s + "*"
		}
		if a["strike"] == true {
			s = "~~" + s + "~~"
		}
		if u, ok := a["link"].(string); ok && u != "" {
			s = "[" + s + "](" + u + ")"
		}
		b.WriteString(s)
	}
	return b.String()
}

// Spec is a block to create.
type Spec struct {
	// Flavour is affine:paragraph, affine:list, affine:code or affine:divider.
	Flavour string
	// Type is a paragraph's (text, h1–h6, quote) or a list's (bulleted,
	// numbered, todo).
	Type     string
	Text     []crdt.Delta
	Checked  bool
	Language string
	Children []Spec
}

// Reference is a paragraph that holds a link to another document (an
// @-mention), the way AFFiNE adds a linked page.
func Reference(docID string) Spec {
	return Spec{Flavour: "affine:paragraph", Type: "text", Text: []crdt.Delta{{
		Op: crdt.DeltaOpInsert, Insert: " ",
		Attributes: crdt.Attributes{"reference": map[string]any{"type": "LinkedPage", "pageId": docID}},
	}}}
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// docLink matches a link to another aenotes document (/b/<id>, absolute or
// not), which becomes a reference rather than a plain link.
var docLink = regexp.MustCompile(`(?:^|/)b/([0-9a-hjkmnp-tv-z]{10})/?(?:\?.*)?$`)

// Parse turns Markdown into blocks.
func Parse(src string) []Spec {
	source := []byte(src)
	root := md.Parser().Parse(text.NewReader(source))
	return blocksOf(root, source)
}

func blocksOf(parent ast.Node, src []byte) []Spec {
	var out []Spec
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		out = append(out, blockOf(n, src)...)
	}
	return out
}

func blockOf(n ast.Node, src []byte) []Spec {
	switch v := n.(type) {
	case *ast.Heading:
		return []Spec{{Flavour: "affine:paragraph", Type: fmt.Sprintf("h%d", v.Level), Text: inlineOf(v, src)}}
	case *ast.Paragraph, *ast.TextBlock:
		return []Spec{{Flavour: "affine:paragraph", Type: "text", Text: inlineOf(v, src)}}
	case *ast.Blockquote:
		inner := blocksOf(v, src)
		for i := range inner {
			if inner[i].Flavour == "affine:paragraph" && inner[i].Type == "text" {
				inner[i].Type = "quote"
			}
		}
		return inner
	case *ast.List:
		var items []Spec
		for li := v.FirstChild(); li != nil; li = li.NextSibling() {
			items = append(items, listItem(li, v.IsOrdered(), src))
		}
		return items
	case *ast.FencedCodeBlock:
		return []Spec{{Flavour: "affine:code", Language: string(v.Language(src)), Text: textDelta(linesOf(v, src))}}
	case *ast.CodeBlock:
		return []Spec{{Flavour: "affine:code", Text: textDelta(linesOf(v, src))}}
	case *ast.ThematicBreak:
		return []Spec{{Flavour: "affine:divider"}}
	case *ast.HTMLBlock:
		return []Spec{{Flavour: "affine:paragraph", Type: "text", Text: textDelta(linesOf(v, src))}}
	case *east.Table:
		// Tables become one paragraph per row, cells joined by " | ".
		var rows []Spec
		for r := v.FirstChild(); r != nil; r = r.NextSibling() {
			var cells []string
			for c := r.FirstChild(); c != nil; c = c.NextSibling() {
				cells = append(cells, plain(inlineOf(c, src)))
			}
			rows = append(rows, Spec{Flavour: "affine:paragraph", Type: "text", Text: textDelta(strings.Join(cells, " | "))})
		}
		return rows
	}
	return blocksOf(n, src)
}

func listItem(li ast.Node, ordered bool, src []byte) Spec {
	s := Spec{Flavour: "affine:list", Type: "bulleted"}
	if ordered {
		s.Type = "numbered"
	}
	for c := li.FirstChild(); c != nil; c = c.NextSibling() {
		switch c.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			if s.Text == nil {
				if cb, ok := c.FirstChild().(*east.TaskCheckBox); ok {
					s.Type = "todo"
					s.Checked = cb.IsChecked
				}
				s.Text = inlineOf(c, src)
				continue
			}
			s.Children = append(s.Children, blockOf(c, src)...)
		default:
			s.Children = append(s.Children, blockOf(c, src)...)
		}
	}
	return s
}

func linesOf(n ast.Node, src []byte) string {
	var b strings.Builder
	lines := n.Lines()
	for i := range lines.Len() {
		seg := lines.At(i)
		b.Write(seg.Value(src))
	}
	return strings.TrimRight(b.String(), "\n")
}

func textDelta(s string) []crdt.Delta {
	if s == "" {
		return nil
	}
	return []crdt.Delta{{Op: crdt.DeltaOpInsert, Insert: s}}
}

// inlineOf flattens a block's inline content into a delta.
func inlineOf(n ast.Node, src []byte) []crdt.Delta {
	var out []crdt.Delta
	var walk func(n ast.Node, attrs crdt.Attributes)
	add := func(s string, attrs crdt.Attributes) {
		if s == "" {
			return
		}
		if k := len(out); k > 0 && reflect.DeepEqual(out[k-1].Attributes, attrs) {
			out[k-1].Insert = out[k-1].Insert.(string) + s
			return
		}
		out = append(out, crdt.Delta{Op: crdt.DeltaOpInsert, Insert: s, Attributes: attrs})
	}
	with := func(attrs crdt.Attributes, k string, v any) crdt.Attributes {
		next := crdt.Attributes{}
		maps.Copy(next, attrs)
		next[k] = v
		return next
	}
	walk = func(n ast.Node, attrs crdt.Attributes) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch v := c.(type) {
			case *ast.Text:
				add(string(v.Segment.Value(src)), attrs)
				if v.HardLineBreak() {
					add("\n", attrs)
				} else if v.SoftLineBreak() {
					add(" ", attrs)
				}
			case *ast.String:
				add(string(v.Value), attrs)
			case *ast.CodeSpan:
				var b strings.Builder
				for t := v.FirstChild(); t != nil; t = t.NextSibling() {
					if tx, ok := t.(*ast.Text); ok {
						b.Write(tx.Segment.Value(src))
					}
				}
				add(b.String(), with(attrs, "code", true))
			case *ast.Emphasis:
				if v.Level >= 2 {
					walk(v, with(attrs, "bold", true))
				} else {
					walk(v, with(attrs, "italic", true))
				}
			case *east.Strikethrough:
				walk(v, with(attrs, "strike", true))
			case *ast.Link:
				dest := string(v.Destination)
				if m := docLink.FindStringSubmatch(dest); m != nil {
					add(" ", with(attrs, "reference", map[string]any{"type": "LinkedPage", "pageId": m[1]}))
					continue
				}
				walk(v, with(attrs, "link", dest))
			case *ast.AutoLink:
				u := string(v.URL(src))
				add(u, with(attrs, "link", u))
			case *ast.Image:
				walk(v, attrs)
			case *east.TaskCheckBox:
				// The checkbox is the list item's type, not text.
			default:
				walk(c, attrs)
			}
		}
	}
	walk(n, nil)
	// A leading space left by a removed checkbox.
	if len(out) > 0 {
		if s, ok := out[0].Insert.(string); ok && out[0].Attributes == nil {
			out[0].Insert = strings.TrimLeft(s, " ")
			if out[0].Insert == "" {
				out = out[1:]
			}
		}
	}
	return out
}
