package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aenix-io/aenotes/internal/boards"
)

type harness struct {
	store *boards.Store
	live  map[string]int
	mu    sync.Mutex
}

// connect runs the tools as caller over an in-memory transport.
func connect(t *testing.T, caller Caller) (*mcp.ClientSession, *harness) {
	t.Helper()
	store, err := boards.Open(t.Context(), boards.DirBackend{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{store: store, live: map[string]int{}}
	srv := New(Deps{
		Store:   store,
		BaseURL: "https://notes.example",
		Version: "test",
		Live: func(_ context.Context, id string, _ []byte) {
			h.mu.Lock()
			h.live[id]++
			h.mu.Unlock()
		},
	})
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			return next(WithCaller(ctx, caller), method, req)
		}
	})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, h
}

// call runs a tool and decodes its structured result; it fails the test on
// a tool error unless wantErr.
func call(t *testing.T, cs *mcp.ClientSession, name string, args any, out any, wantErr bool) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if res.IsError != wantErr {
		t.Fatalf("%s: IsError=%v, want %v: %s", name, res.IsError, wantErr, text)
	}
	if out != nil && !wantErr {
		raw, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return text
}

func TestCreateReadAndAppend(t *testing.T) {
	cs, h := connect(t, Caller{Login: "alice", CanWrite: true})

	var f FolderInfo
	call(t, cs, "create_folder", map[string]any{"name": "Specs"}, &f, false)

	var other Document
	call(t, cs, "create_document", map[string]any{"title": "Other"}, &other, false)

	var page Document
	call(t, cs, "create_document", map[string]any{
		"title":    "Plan",
		"folder":   f.ID,
		"markdown": "# Goals\n\n- ship\n\nSee [other](" + other.URL + ").\n",
	}, &page, false)
	if page.Mode != boards.ModePage || page.Folder != f.ID || page.URL != "https://notes.example/b/"+page.ID {
		t.Fatalf("created: %+v", page)
	}

	call(t, cs, "append_to_document", map[string]any{"id": page.URL, "markdown": "Done **today**."}, nil, false)
	if h.live[page.ID] != 2 {
		t.Fatalf("live updates sent: %d, want 2", h.live[page.ID])
	}

	var got readOut
	call(t, cs, "read_document", map[string]any{"id": page.ID}, &got, false)
	want := "# Goals\n\n- ship\n\nSee [Other](" + other.URL + ").\n\nDone **today**.\n"
	if got.Markdown != want || got.Title != "Plan" || got.FolderPath != "Specs" {
		t.Fatalf("read: %+v\nmarkdown:\n%s", got, got.Markdown)
	}

	var found searchOut
	call(t, cs, "search_documents", map[string]any{"query": "TODAY"}, &found, false)
	if len(found.Matches) != 1 || found.Matches[0].ID != page.ID || !strings.Contains(found.Matches[0].Snippet, "today") {
		t.Fatalf("search: %+v", found)
	}
}

func TestBoardContentGoesIntoANote(t *testing.T) {
	cs, _ := connect(t, Caller{Login: "alice", CanWrite: true})
	var b Document
	call(t, cs, "create_document", map[string]any{"title": "Retro", "mode": "edgeless", "markdown": "- went well"}, &b, false)
	call(t, cs, "append_to_document", map[string]any{"id": b.ID, "markdown": "- to improve"}, nil, false)
	var got readOut
	call(t, cs, "read_document", map[string]any{"id": b.ID}, &got, false)
	if got.Markdown != "- went well\n\n- to improve\n" {
		t.Fatalf("board markdown:\n%s", got.Markdown)
	}
}

func TestUpdateAndFolders(t *testing.T) {
	cs, h := connect(t, Caller{Login: "alice", CanWrite: true})
	var a, b FolderInfo
	call(t, cs, "create_folder", map[string]any{"name": "A"}, &a, false)
	call(t, cs, "create_folder", map[string]any{"name": "B", "parent": a.ID}, &b, false)
	call(t, cs, "update_folder", map[string]any{"id": a.ID, "parent": b.ID}, nil, true) // into itself

	var d Document
	call(t, cs, "create_document", map[string]any{"title": "Doc", "folder": b.ID}, &d, false)
	call(t, cs, "delete_folder", map[string]any{"id": b.ID}, nil, true) // not empty

	call(t, cs, "update_document", map[string]any{"id": d.ID, "title": "Renamed", "folder": ""}, &d, false)
	if d.Title != "Renamed" || d.Folder != "" || h.live[d.ID] != 1 {
		t.Fatalf("updated: %+v (live %d)", d, h.live[d.ID])
	}
	call(t, cs, "delete_folder", map[string]any{"id": b.ID}, nil, false)

	var l listOut
	call(t, cs, "list_documents", nil, &l, false)
	if len(l.Folders) != 1 || len(l.Documents) != 1 || l.Documents[0].Title != "Renamed" {
		t.Fatalf("list: %+v", l)
	}
	call(t, cs, "read_document", map[string]any{"id": "nosuchdoc0"}, nil, true)
}

func TestReadOnlyCallerCannotWrite(t *testing.T) {
	cs, h := connect(t, Caller{Login: "bob"})
	if _, err := h.store.Create(boards.CreateInput{Mode: boards.ModePage, Title: "Seen"}, ""); err != nil {
		t.Fatal(err)
	}
	var l listOut
	call(t, cs, "list_documents", nil, &l, false)
	id := l.Documents[0].ID
	call(t, cs, "read_document", map[string]any{"id": id}, nil, false)
	for name, args := range map[string]map[string]any{
		"create_document":    {"title": "x"},
		"append_to_document": {"id": id, "markdown": "x"},
		"update_document":    {"id": id, "title": "x"},
		"create_folder":      {"name": "x"},
	} {
		if msg := call(t, cs, name, args, nil, true); !strings.Contains(msg, "read-only") {
			t.Errorf("%s: %s", name, msg)
		}
	}
}

// A document created with a parent is linked from it: its child.
func TestCreateInsideAParent(t *testing.T) {
	cs, h := connect(t, Caller{Login: "alice", CanWrite: true})
	var parent, child Document
	call(t, cs, "create_document", map[string]any{"title": "Parent"}, &parent, false)
	call(t, cs, "create_document", map[string]any{"title": "Child", "parent": parent.URL}, &child, false)
	if h.live[parent.ID] != 1 {
		t.Fatalf("parent live updates: %d", h.live[parent.ID])
	}
	var l listOut
	call(t, cs, "list_documents", nil, &l, false)
	for _, d := range l.Documents {
		if d.ID == parent.ID && (len(d.Children) != 1 || d.Children[0] != child.ID) {
			t.Fatalf("children: %+v", d)
		}
	}
	var got readOut
	call(t, cs, "read_document", map[string]any{"id": parent.ID}, &got, false)
	if got.Markdown != "[Child]("+child.URL+")\n" {
		t.Fatalf("parent markdown: %q", got.Markdown)
	}
}
