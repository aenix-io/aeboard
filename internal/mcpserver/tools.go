package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aenix-io/aenotes/internal/blocks"
	"github.com/aenix-io/aenotes/internal/boards"
)

// errReadOnly is what a write tool answers someone with read access only.
var errReadOnly = errors.New("you have read-only access to these notes")

type tools struct{ Deps }

func registerTools(s *mcp.Server, d Deps) {
	t := &tools{d}
	mcp.AddTool(s, &mcp.Tool{Name: "list_documents", Description: "List every folder and document: the folder tree (id, name, parent) and each document's id, title, mode (page = a rich-text document, edgeless = a whiteboard), folder, parent, URL and links. Every document has one home: a folder (or the top level) or a parent document it lives inside. A document's links to others show inside it as shortcuts; the documents whose parent it is are its own children. Start here to find what to read."}, t.list)
	mcp.AddTool(s, &mcp.Tool{Name: "search_documents", Description: "Find documents whose title or text contains the query (case-insensitive). Returns the matches with a snippet around the first hit in the text."}, t.search)
	mcp.AddTool(s, &mcp.Tool{Name: "read_document", Description: "Read one document as Markdown, with its title, mode, folder path and URL. For a page this is its text top to bottom; for a board it is the text of every note on the canvas, then the text written on shapes. Links to other documents are rendered as [Title](URL)."}, t.read)
	mcp.AddTool(s, &mcp.Tool{Name: "create_document", Description: "Create a page (mode=page, the default) or a board (mode=edgeless) with a title, optionally inside a folder, optionally with initial content as Markdown (headings, paragraphs, bold/italic/strike/code, links, bulleted/numbered/task lists, code blocks, quotes, dividers). A Markdown link to another document's URL becomes a live link to it (a shortcut to it in the tree). On a board, the content goes into a note on the canvas. Pass parent to create the document inside another one (a link to it is added at the end of the parent)."}, t.create)
	mcp.AddTool(s, &mcp.Tool{Name: "append_to_document", Description: "Add Markdown content to the end of a page, or as a new note on a board (placed to the right of what is already there). Existing content is never changed. People who have the document open see it at once."}, t.appendTo)
	mcp.AddTool(s, &mcp.Tool{Name: "update_document", Description: "Rename a document (title) and/or move it: into a folder (folder; \"\" is the top level) or inside another document (parent). Only the fields given change."}, t.update)
	mcp.AddTool(s, &mcp.Tool{Name: "trash_document", Description: "Move a document to the trash. It disappears from the tree and the lists but is kept whole: restore_document brings it back where it was. Links to it in other documents are left as they are. Only a person can delete a document for good, from the trash."}, t.trash(true))
	mcp.AddTool(s, &mcp.Tool{Name: "restore_document", Description: "Bring a document back from the trash."}, t.trash(false))
	mcp.AddTool(s, &mcp.Tool{Name: "create_folder", Description: "Create a folder, at the top level or inside another folder (parent)."}, t.createFolder)
	mcp.AddTool(s, &mcp.Tool{Name: "update_folder", Description: "Rename a folder (name) and/or move it inside another folder (parent; \"\" is the top level). A folder cannot go inside itself."}, t.updateFolder)
	mcp.AddTool(s, &mcp.Tool{Name: "delete_folder", Description: "Delete an EMPTY folder. One that still holds documents or folders is refused: move them out first."}, t.deleteFolder)
}

// Document is a document as the tools show it.
type Document struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Mode  string `json:"mode"`
	// Folder is the folder's id ("" for the top level).
	Folder  string    `json:"folder"`
	URL     string    `json:"url"`
	Updated time.Time `json:"updated"`
	// Trashed is set for a document in the trash.
	Trashed bool `json:"trashed,omitempty"`
	// Parent is the document this one lives inside ("" when its home is
	// a folder or the top level).
	Parent string `json:"parent,omitempty"`
	// Links are the documents this one links to; those living elsewhere
	// show inside it as shortcuts.
	Links []string `json:"links,omitempty"`
}

// FolderInfo is a folder as the tools show it.
type FolderInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Parent string `json:"parent"`
}

func (t *tools) url(id string) string { return t.BaseURL + "/b/" + id }

func (t *tools) doc(m boards.Meta) Document {
	return Document{ID: m.ID, Title: titleOr(m.Title), Mode: m.Mode, Folder: t.Store.FolderOf(m), URL: t.url(m.ID), Updated: m.Updated, Trashed: !m.Trashed.IsZero(), Parent: m.Parent}
}

func titleOr(s string) string {
	if s == "" {
		return "Untitled"
	}
	return s
}

func (t *tools) writer(ctx context.Context) (string, error) {
	c := CallerFrom(ctx)
	if !c.CanWrite {
		return "", errReadOnly
	}
	return c.Login, nil
}

type listIn struct{}

type listOut struct {
	Folders   []FolderInfo `json:"folders"`
	Documents []Document   `json:"documents"`
}

func (t *tools) list(_ context.Context, _ *mcp.CallToolRequest, _ listIn) (*mcp.CallToolResult, listOut, error) {
	out := listOut{Folders: []FolderInfo{}, Documents: []Document{}}
	for _, f := range t.Store.Folders() {
		out.Folders = append(out.Folders, FolderInfo{ID: f.ID, Name: f.Name, Parent: f.Parent})
	}
	links := t.Store.Links()
	for _, m := range t.Store.List() {
		if !m.Trashed.IsZero() {
			continue
		}
		d := t.doc(m)
		d.Links = links[m.ID]
		out.Documents = append(out.Documents, d)
	}
	return nil, out, nil
}

type searchIn struct {
	Query string `json:"query" jsonschema:"the text to look for"`
}

// Match is a search hit.
type Match struct {
	Document
	Snippet string `json:"snippet,omitempty"`
}

type searchOut struct {
	Matches []Match `json:"matches"`
}

func (t *tools) search(_ context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, searchOut{}, errors.New("query is empty")
	}
	out := searchOut{Matches: []Match{}}
	for _, h := range t.Store.Search(in.Query) {
		if m, err := t.Store.Get(h.ID); err == nil {
			out.Matches = append(out.Matches, Match{Document: t.doc(m), Snippet: h.Snippet})
		}
	}
	return nil, out, nil
}

// text is a document's content as Markdown (a board's notes and shapes).
func (t *tools) text(m boards.Meta) (string, error) {
	state, err := t.Store.LoadState(m.ID)
	if err != nil {
		return "", err
	}
	d, err := blocks.Load(state)
	if err != nil {
		return "", err
	}
	page := m.Mode == boards.ModePage
	md := d.Markdown(page, t.link)
	if !page {
		if shapes := d.Shapes(); len(shapes) > 0 {
			slices.Sort(shapes)
			md += "\n## On the canvas\n\n- " + strings.Join(shapes, "\n- ") + "\n"
		}
	}
	return md, nil
}

func (t *tools) link(id string) (string, string) {
	title := "Untitled"
	if m, err := t.Store.Get(id); err == nil {
		title = titleOr(m.Title)
	}
	return title, t.url(id)
}

// folderPath is a folder's names from the top, "/"-joined ("" at the top).
func (t *tools) folderPath(id string) string {
	byID := map[string]boards.Folder{}
	for _, f := range t.Store.Folders() {
		byID[f.ID] = f
	}
	var names []string
	for at, seen := id, 0; at != "" && seen < 64; seen++ {
		f, ok := byID[at]
		if !ok {
			break
		}
		names = append([]string{f.Name}, names...)
		at = f.Parent
	}
	return strings.Join(names, "/")
}

type readIn struct {
	ID string `json:"id" jsonschema:"the document's id (from list_documents or its URL /b/<id>)"`
}

type readOut struct {
	Document
	FolderPath string `json:"folder_path"`
	Markdown   string `json:"markdown"`
}

func (t *tools) read(_ context.Context, _ *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, readOut, error) {
	m, err := t.Store.Get(docID(in.ID))
	if err != nil {
		return nil, readOut{}, notFound(in.ID, err)
	}
	md, err := t.text(m)
	if err != nil {
		return nil, readOut{}, err
	}
	d := t.doc(m)
	return nil, readOut{Document: d, FolderPath: t.folderPath(d.Folder), Markdown: md}, nil
}

// docID accepts a bare id or a document URL.
func docID(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/b/"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

func notFound(id string, err error) error {
	if errors.Is(err, boards.ErrNotFound) {
		return fmt.Errorf("no document %q", id)
	}
	return err
}

type createIn struct {
	Title    string `json:"title"`
	Mode     string `json:"mode,omitempty" jsonschema:"page (default) or edgeless (a whiteboard)"`
	Folder   string `json:"folder,omitempty" jsonschema:"the folder's id; empty is the top level"`
	Parent   string `json:"parent,omitempty" jsonschema:"a document to create this one inside; a link to it is added at the end of that document"`
	Markdown string `json:"markdown,omitempty" jsonschema:"initial content"`
}

func (t *tools) create(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, Document, error) {
	login, err := t.writer(ctx)
	if err != nil {
		return nil, Document{}, err
	}
	if in.Mode == "" {
		in.Mode = boards.ModePage
	}
	parent := docID(in.Parent)
	if parent != "" {
		if _, err := t.Store.Get(parent); err != nil {
			return nil, Document{}, notFound(in.Parent, err)
		}
	}
	m, err := t.Store.Create(boards.CreateInput{Mode: in.Mode, Title: in.Title, Folder: in.Folder, Parent: parent}, login)
	if err != nil {
		return nil, Document{}, err
	}
	if strings.TrimSpace(in.Markdown) != "" {
		if err := t.write(ctx, m, in.Markdown, login); err != nil {
			return nil, Document{}, err
		}
	}
	if parent != "" {
		upd, err := t.Store.Link(parent, m.ID, login)
		if err != nil {
			return nil, Document{}, err
		}
		t.live(ctx, parent, upd)
	}
	return nil, t.doc(m), nil
}

// write appends Markdown to a document and sends it to its live room.
func (t *tools) write(ctx context.Context, m boards.Meta, markdown, login string) error {
	specs := blocks.Parse(markdown)
	if len(specs) == 0 {
		return errors.New("the markdown has no content")
	}
	upd, err := t.Store.Append(m.ID, specs, login)
	if err != nil {
		return err
	}
	t.live(ctx, m.ID, upd)
	return nil
}

func (t *tools) live(ctx context.Context, id string, upd []byte) {
	if upd != nil && t.Live != nil {
		t.Live(ctx, id, upd)
	}
}

type appendIn struct {
	ID       string `json:"id"`
	Markdown string `json:"markdown"`
}

func (t *tools) appendTo(ctx context.Context, _ *mcp.CallToolRequest, in appendIn) (*mcp.CallToolResult, Document, error) {
	login, err := t.writer(ctx)
	if err != nil {
		return nil, Document{}, err
	}
	m, err := t.Store.Get(docID(in.ID))
	if err != nil {
		return nil, Document{}, notFound(in.ID, err)
	}
	if err := t.write(ctx, m, in.Markdown, login); err != nil {
		return nil, Document{}, err
	}
	m, _ = t.Store.Get(m.ID)
	return nil, t.doc(m), nil
}

type updateIn struct {
	ID     string  `json:"id"`
	Title  *string `json:"title,omitempty"`
	Folder *string `json:"folder,omitempty" jsonschema:"the folder's id; empty is the top level"`
	Parent *string `json:"parent,omitempty" jsonschema:"a document to move this one inside"`
}

func (t *tools) update(ctx context.Context, _ *mcp.CallToolRequest, in updateIn) (*mcp.CallToolResult, Document, error) {
	login, err := t.writer(ctx)
	if err != nil {
		return nil, Document{}, err
	}
	id := docID(in.ID)
	m, upd, err := t.Store.Update(id, boards.Patch{Title: in.Title, Folder: in.Folder, Parent: in.Parent}, login)
	if err != nil {
		return nil, Document{}, notFound(in.ID, err)
	}
	t.live(ctx, id, upd)
	return nil, t.doc(m), nil
}

type trashIn struct {
	ID string `json:"id"`
}

func (t *tools) trash(on bool) mcp.ToolHandlerFor[trashIn, Document] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in trashIn) (*mcp.CallToolResult, Document, error) {
		login, err := t.writer(ctx)
		if err != nil {
			return nil, Document{}, err
		}
		m, _, err := t.Store.Update(docID(in.ID), boards.Patch{Trashed: &on}, login)
		if err != nil {
			return nil, Document{}, notFound(in.ID, err)
		}
		return nil, t.doc(m), nil
	}
}

type createFolderIn struct {
	Name   string `json:"name"`
	Parent string `json:"parent,omitempty" jsonschema:"the parent folder's id; empty is the top level"`
}

func (t *tools) createFolder(ctx context.Context, _ *mcp.CallToolRequest, in createFolderIn) (*mcp.CallToolResult, FolderInfo, error) {
	if _, err := t.writer(ctx); err != nil {
		return nil, FolderInfo{}, err
	}
	f, err := t.Store.CreateFolder(in.Name, in.Parent)
	if err != nil {
		return nil, FolderInfo{}, err
	}
	return nil, FolderInfo{ID: f.ID, Name: f.Name, Parent: f.Parent}, nil
}

type updateFolderIn struct {
	ID     string  `json:"id"`
	Name   *string `json:"name,omitempty"`
	Parent *string `json:"parent,omitempty" jsonschema:"the new parent folder's id; empty is the top level"`
}

func (t *tools) updateFolder(ctx context.Context, _ *mcp.CallToolRequest, in updateFolderIn) (*mcp.CallToolResult, FolderInfo, error) {
	if _, err := t.writer(ctx); err != nil {
		return nil, FolderInfo{}, err
	}
	f, err := t.Store.UpdateFolder(in.ID, boards.FolderPatch{Name: in.Name, Parent: in.Parent})
	if err != nil {
		return nil, FolderInfo{}, folderErr(in.ID, err)
	}
	return nil, FolderInfo{ID: f.ID, Name: f.Name, Parent: f.Parent}, nil
}

type deleteFolderIn struct {
	ID string `json:"id"`
}

type deleted struct {
	Deleted string `json:"deleted"`
}

func (t *tools) deleteFolder(ctx context.Context, _ *mcp.CallToolRequest, in deleteFolderIn) (*mcp.CallToolResult, deleted, error) {
	if _, err := t.writer(ctx); err != nil {
		return nil, deleted{}, err
	}
	if err := t.Store.DeleteFolder(in.ID); err != nil {
		return nil, deleted{}, folderErr(in.ID, err)
	}
	return nil, deleted{Deleted: in.ID}, nil
}

func folderErr(id string, err error) error {
	if errors.Is(err, boards.ErrNotFound) {
		return fmt.Errorf("no folder %q", id)
	}
	return err
}
