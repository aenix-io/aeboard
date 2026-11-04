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
		_, _, hit := foldIndex(m.Title, q)
		snippet := ""
		if text := s.Text(m, nil); text != "" {
			if from, to, ok := foldIndex(text, q); ok {
				hit = true
				snippet = around(text, from, to-from)
			}
		}
		if hit {
			out = append(out, Hit{ID: m.ID, Snippet: snippet})
		}
	}
	return out
}

// Text is a document's content as Markdown: a page's text, a board's notes
// and the text on its shapes. link names the documents it links to (nil:
// their titles, with /b/<id>).
func (s *Store) Text(m Meta, link blocks.LinkResolver) string {
	state, err := s.LoadState(m.ID)
	if err != nil {
		return ""
	}
	d, err := blocks.Load(state)
	if err != nil {
		return ""
	}
	page := m.Mode == ModePage
	if link == nil {
		link = func(id string) (string, string) {
			t := "Untitled"
			if o, err := s.Get(id); err == nil && o.Title != "" {
				t = o.Title
			}
			return t, "/b/" + id
		}
	}
	md := d.Markdown(page, link)
	if !page {
		if shapes := d.Shapes(); len(shapes) > 0 {
			slices.Sort(shapes)
			md += "\n## On the canvas\n\n- " + strings.Join(shapes, "\n- ") + "\n"
		}
	}
	return md
}

// foldIndex finds q (already lower-cased) in s ignoring case, and returns
// where the match is in s itself. Lower-casing can change a letter's length
// in bytes (Ⱥ is 2 bytes, ⱥ is 3), so positions in strings.ToLower(s) are
// not positions in s: each byte of the lower-cased text remembers the byte
// of s it came from.
func foldIndex(s, q string) (from, to int, ok bool) {
	if q == "" {
		return 0, 0, false
	}
	var lower strings.Builder
	var origin []int
	for i, r := range s {
		l := strings.ToLower(string(r))
		lower.WriteString(l)
		for range len(l) {
			origin = append(origin, i)
		}
	}
	at := strings.Index(lower.String(), q)
	if at < 0 {
		return 0, 0, false
	}
	end := len(s)
	if next := at + len(q); next < len(origin) {
		end = origin[next]
	}
	return origin[at], end, true
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
