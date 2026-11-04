package boards

import (
	"slices"
	"strings"

	"github.com/aenix-io/aenotes/internal/blocks"
)

// Hit is a document a search found, with the text around the first match
// in its content (empty when only the title matched).
type Hit struct {
	ID      string `json:"id"`
	Snippet string `json:"snippet,omitempty"`
}

// Search finds the documents (not in the trash) whose title or text holds
// q, case-insensitively, most recently updated first.
func (s *Store) Search(q string) []Hit {
	q = strings.ToLower(strings.TrimSpace(q))
	out := []Hit{}
	if q == "" {
		return out
	}
	for _, m := range s.List() {
		if !m.Trashed.IsZero() {
			continue
		}
		hit := strings.Contains(strings.ToLower(m.Title), q)
		snippet := ""
		if text := s.Text(m); text != "" {
			if i := strings.Index(strings.ToLower(text), q); i >= 0 {
				hit = true
				snippet = around(text, i, len(q))
			}
		}
		if hit {
			out = append(out, Hit{ID: m.ID, Snippet: snippet})
		}
	}
	return out
}

// Text is a document's content as plain Markdown: a page's text, a board's
// notes and the text on its shapes. Links to documents read as their titles.
func (s *Store) Text(m Meta) string {
	state, err := s.LoadState(m.ID)
	if err != nil {
		return ""
	}
	d, err := blocks.Load(state)
	if err != nil {
		return ""
	}
	page := m.Mode == ModePage
	md := d.Markdown(page, func(id string) (string, string) {
		t := "Untitled"
		if o, err := s.Get(id); err == nil && o.Title != "" {
			t = o.Title
		}
		return t, "/b/" + id
	})
	if !page {
		if shapes := d.Shapes(); len(shapes) > 0 {
			slices.Sort(shapes)
			md += "\n" + strings.Join(shapes, "\n") + "\n"
		}
	}
	return md
}

// around cuts ~80 bytes either side of a hit, on rune boundaries, on one line.
func around(s string, at, n int) string {
	from, to := max(0, at-80), min(len(s), at+n+80)
	for from > 0 && s[from]&0xC0 == 0x80 {
		from--
	}
	for to < len(s) && s[to]&0xC0 == 0x80 {
		to++
	}
	out := strings.Join(strings.Fields(s[from:to]), " ")
	if from > 0 {
		out = "…" + out
	}
	if to < len(s) {
		out += "…"
	}
	return out
}
