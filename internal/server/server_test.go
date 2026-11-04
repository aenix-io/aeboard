package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aenix-io/aenotes/internal/blobs"
	"github.com/aenix-io/aenotes/internal/boards"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	store, err := boards.Open(context.Background(), boards.DirBackend{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	blobStore, err := blobs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	spa := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("js")},
	}
	ts := httptest.NewServer(New(store, blobStore, spa, slog.New(slog.DiscardHandler)).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestBoardLifecycle(t *testing.T) {
	ts := newTestServer(t)
	code, body := do(t, "POST", ts.URL+"/api/v1/boards", `{"title":"Retro"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var m boards.Meta
	_ = json.Unmarshal([]byte(body), &m)

	code, body = do(t, "PATCH", ts.URL+"/api/v1/boards/"+m.ID, `{"title":"Retro 2"}`)
	if code != http.StatusOK || !strings.Contains(body, `"Retro 2"`) {
		t.Fatalf("patch: %d %s", code, body)
	}
	code, body = do(t, "GET", ts.URL+"/api/v1/boards", "")
	var list boardList
	_ = json.Unmarshal([]byte(body), &list)
	if code != http.StatusOK || len(list.Boards) != 1 || list.Boards[0].Title != "Retro 2" {
		t.Fatalf("list: %d %s", code, body)
	}
}

func TestUnknownBoardIs404AndCannotBeSynced(t *testing.T) {
	ts := newTestServer(t)
	if code, _ := do(t, "GET", ts.URL+"/api/v1/boards/0000000000", ""); code != http.StatusNotFound {
		t.Fatalf("get unknown: %d", code)
	}
	if code, _ := do(t, "PATCH", ts.URL+"/api/v1/boards/0000000000", `{}`); code != http.StatusNotFound {
		t.Fatalf("patch unknown: %d", code)
	}
	// The sync socket refuses a board that does not exist (before upgrade).
	if code, _ := do(t, "GET", ts.URL+"/api/v1/sync/0000000000", ""); code != http.StatusUnauthorized {
		t.Fatalf("sync unknown: %d", code)
	}
	if code, _ := do(t, "GET", ts.URL+"/api/v1/nope", ""); code != http.StatusNotFound {
		t.Fatalf("unknown api path: %d", code)
	}
}

func TestSPAFallback(t *testing.T) {
	ts := newTestServer(t)
	for _, p := range []string{"/", "/b/abcdefghjk", "/anything"} {
		if code, body := do(t, "GET", ts.URL+p, ""); code != 200 || body != "<html>app</html>" {
			t.Fatalf("%s: %d %q", p, code, body)
		}
	}
	if _, body := do(t, "GET", ts.URL+"/assets/app.js", ""); body != "js" {
		t.Fatalf("asset: %q", body)
	}
}

func TestBlobRoundTripAndHashCheck(t *testing.T) {
	ts := newTestServer(t)
	_, body := do(t, "POST", ts.URL+"/api/v1/boards", `{"title":"b"}`)
	var m boards.Meta
	_ = json.Unmarshal([]byte(body), &m)
	svg := `<svg xmlns="http://www.w3.org/2000/svg"/>`
	key := blobs.Key([]byte(svg))
	url := ts.URL + "/api/v1/boards/" + m.ID + "/blobs/" + key

	req, _ := http.NewRequest("PUT", url, strings.NewReader(svg))
	req.Header.Set("Content-Type", "image/svg+xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put: %v", resp.Status)
	}
	resp, _ = http.Get(url)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != svg || resp.Header.Get("Content-Type") != "image/svg+xml" || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("get: %q %v", got, resp.Header)
	}
	if code, _ := do(t, "PUT", url, "other"); code != http.StatusBadRequest {
		t.Fatalf("mismatched put: %d", code)
	}
	if code, _ := do(t, "GET", ts.URL+"/api/v1/boards/0000000000/blobs/"+key, ""); code != http.StatusNotFound {
		t.Fatalf("blob of unknown board: %d", code)
	}
}

func TestMe(t *testing.T) {
	ts := newTestServer(t)
	if code, _ := do(t, "GET", ts.URL+"/api/v1/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous me: %d", code)
	}
	dir := t.TempDir()
	store, _ := boards.Open(context.Background(), boards.DirBackend{Root: dir})
	blobStore, _ := blobs.Open(dir)
	srv := New(store, blobStore, fstest.MapFS{}, slog.New(slog.DiscardHandler))
	srv.SetIdentity(StaticIdentity(Identity{Login: "octocat", AvatarURL: GitHubAvatarURL("octocat")}))
	ts2 := httptest.NewServer(srv.Handler())
	defer ts2.Close()
	code, body := do(t, "GET", ts2.URL+"/api/v1/me", "")
	if code != http.StatusOK || !strings.Contains(body, `"login":"octocat"`) || !strings.Contains(body, "avatars.githubusercontent.com/octocat") {
		t.Fatalf("me: %d %s", code, body)
	}
}

func TestStatusCountsPendingBoards(t *testing.T) {
	ts := newTestServer(t)
	_, _ = do(t, "POST", ts.URL+"/api/v1/boards", `{"title":"b"}`)
	code, body := do(t, "GET", ts.URL+"/api/v1/status", "")
	if code != http.StatusOK || !strings.Contains(body, `"pending":1`) || !strings.Contains(body, `"backend":"dir"`) {
		t.Fatalf("status: %d %s", code, body)
	}
}

func TestFolderAPI(t *testing.T) {
	ts := newTestServer(t)
	code, body := do(t, "POST", ts.URL+"/api/v1/folders", `{"name":"Work"}`)
	if code != http.StatusCreated {
		t.Fatalf("create folder: %d %s", code, body)
	}
	var f boards.Folder
	_ = json.Unmarshal([]byte(body), &f)
	code, body = do(t, "POST", ts.URL+"/api/v1/boards", `{"title":"Plan","folder":"`+f.ID+`"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"folder":"`+f.ID+`"`) {
		t.Fatalf("create in folder: %d %s", code, body)
	}
	if code, _ := do(t, "DELETE", ts.URL+"/api/v1/folders/"+f.ID, ""); code != http.StatusConflict {
		t.Fatalf("delete non-empty: %d", code)
	}
	if code, _ := do(t, "POST", ts.URL+"/api/v1/boards", `{"folder":"nosuchfold"}`); code != http.StatusBadRequest {
		t.Fatalf("create into a missing folder: %d", code)
	}
	if code, body := do(t, "PATCH", ts.URL+"/api/v1/folders/"+f.ID, `{"name":"Work 2"}`); code != 200 || !strings.Contains(body, "Work 2") {
		t.Fatalf("rename: %d %s", code, body)
	}
	_, body = do(t, "GET", ts.URL+"/api/v1/boards", "")
	if !strings.Contains(body, `"folders":[{`) || !strings.Contains(body, "Work 2") {
		t.Fatalf("list: %s", body)
	}
}

// An open status stream must not hold up a graceful stop: Shutdown waits for
// active requests, and the stream would otherwise run until its client left.
func TestShutdownEndsStatusStreams(t *testing.T) {
	dir := t.TempDir()
	store, _ := boards.Open(context.Background(), boards.DirBackend{Root: dir})
	blobStore, _ := blobs.Open(dir)
	srv := New(store, blobStore, fstest.MapFS{}, slog.New(slog.DiscardHandler))
	hs := httptest.NewUnstartedServer(srv.Handler())
	hs.Config.RegisterOnShutdown(srv.CloseStreams)
	hs.Start()
	resp, err := http.Get(hs.URL + "/api/v1/status/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 256)
	if _, err := resp.Body.Read(buf); err != nil { // the first event
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := hs.Config.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown with an open stream: %v", err)
	}
}

// A document put inside another shows in the catalog as its link.
func TestAddChild(t *testing.T) {
	ts := newTestServer(t)
	var parent, child boards.Meta
	_, body := do(t, "POST", ts.URL+"/api/v1/boards", `{"mode":"page","title":"Parent"}`)
	_ = json.Unmarshal([]byte(body), &parent)
	_, body = do(t, "POST", ts.URL+"/api/v1/boards", `{"mode":"page","title":"Child"}`)
	_ = json.Unmarshal([]byte(body), &child)

	if code, body := do(t, "POST", ts.URL+"/api/v1/boards/"+parent.ID+"/children", `{"doc":"`+child.ID+`"}`); code != http.StatusNoContent {
		t.Fatalf("add child: %d %s", code, body)
	}
	if code, _ := do(t, "POST", ts.URL+"/api/v1/boards/"+parent.ID+"/children", `{"doc":"nosuchdoc0"}`); code != http.StatusNotFound {
		t.Fatalf("missing child: %d", code)
	}
	_, body = do(t, "GET", ts.URL+"/api/v1/boards", "")
	var list struct {
		Boards []struct {
			ID    string   `json:"id"`
			Links []string `json:"links"`
		} `json:"boards"`
	}
	_ = json.Unmarshal([]byte(body), &list)
	for _, b := range list.Boards {
		if b.ID == parent.ID && (len(b.Links) != 1 || b.Links[0] != child.ID) {
			t.Fatalf("parent links: %v", b.Links)
		}
	}
}
