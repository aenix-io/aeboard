package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/aenix-io/aeboard/internal/blobs"
	"github.com/aenix-io/aeboard/internal/boards"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	store, err := boards.Open(dir)
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
	code, body := do(t, "POST", ts.URL+"/api/v1/boards", `{"title":"Retro","tags":["team","q4"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var m boards.Meta
	_ = json.Unmarshal([]byte(body), &m)

	code, body = do(t, "PATCH", ts.URL+"/api/v1/boards/"+m.ID, `{"tags":["q4","design"]}`)
	if code != http.StatusOK || !strings.Contains(body, `"Retro"`) {
		t.Fatalf("patch: %d %s", code, body)
	}
	code, body = do(t, "GET", ts.URL+"/api/v1/boards", "")
	var list boardList
	_ = json.Unmarshal([]byte(body), &list)
	if code != http.StatusOK || len(list.Boards) != 1 || strings.Join(list.Tags, ",") != "design,q4" {
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
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put: %v %v", err, resp.Status)
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
	store, _ := boards.Open(dir)
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
