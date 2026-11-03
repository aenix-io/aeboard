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

	"github.com/aenix-io/aeboard/internal/boards"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store, err := boards.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spa := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("js")},
	}
	ts := httptest.NewServer(New(store, spa, slog.New(slog.DiscardHandler)).Handler())
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
