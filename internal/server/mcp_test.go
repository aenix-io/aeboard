package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/reearth/ygo/crdt"

	"github.com/aenix-io/aenotes/internal/blobs"
	"github.com/aenix-io/aenotes/internal/boards"
	"github.com/aenix-io/aenotes/internal/mcpserver"
)

// /mcp: the bearer token is a session, the session's rights on the board
// repository reach every tool call as its Caller, and a local server acts
// as the local identity without a token. The tool set is swapped for one
// tool that reports the Caller it sees.

type whoami struct {
	Login    string `json:"login"`
	CanWrite bool   `json:"canWrite"`
}

// whoamiServer is a tool set of one tool, whoami, answering with the
// Caller on its context; deps records what the server handed it.
func whoamiServer(deps *mcpserver.Deps) func(mcpserver.Deps) *mcp.Server {
	return func(d mcpserver.Deps) *mcp.Server {
		if deps != nil {
			*deps = d
		}
		s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: d.Version}, nil)
		mcp.AddTool(s, &mcp.Tool{Name: "whoami"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, whoami, error) {
			c := mcpserver.CallerFrom(ctx)
			return nil, whoami{Login: c.Login, CanWrite: c.CanWrite}, nil
		})
		return s
	}
}

// bearer adds a token to every request.
type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func dialMCP(t *testing.T, endpoint, token string) (*mcp.ClientSession, error) {
	t.Helper()
	hc := http.DefaultClient
	if token != "" {
		hc = &http.Client{Transport: bearer{token: token, base: http.DefaultTransport}}
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc, DisableStandaloneSSE: true}, nil)
	if cs != nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

func callWhoami(t *testing.T, cs *mcp.ClientSession) whoami {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil || res.IsError {
		t.Fatalf("whoami: %v %+v", err, res)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var w whoami
	_ = json.Unmarshal(b, &w)
	return w
}

// rawInitialize posts an initialize with the token and returns the answer.
func rawInitialize(t *testing.T, endpoint, token string) reply {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return call(t, req)
}

func TestMCPWithoutATokenIs401WithTheResourceMetadata(t *testing.T) {
	h := newHosted(t, nil)
	want := `resource_metadata="` + h.srv.auth.baseURL() + `/.well-known/oauth-protected-resource"`
	for _, token := range []string{"", "made-up"} {
		r := rawInitialize(t, h.ts.URL+"/mcp", token)
		if r.status != http.StatusUnauthorized || !strings.Contains(r.header.Get("WWW-Authenticate"), want) {
			t.Fatalf("token %q: %d %q", token, r.status, r.header.Get("WWW-Authenticate"))
		}
	}
	// A browser's session cookie is no MCP credential.
	h.gh.set("alice", "write")
	c := h.signIn(t, "alice")
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/mcp", strings.NewReader(`{}`))
	req.AddCookie(c)
	if r := call(t, req); r.status != http.StatusUnauthorized {
		t.Fatalf("cookie on /mcp: %d", r.status)
	}
}

// The real tool set answers initialize and tools/list for a signed-in caller.
func TestMCPWithATokenServesTheToolSet(t *testing.T) {
	h := newHosted(t, nil)
	h.gh.set("alice", "read")
	tk, _ := h.mintToken(t, "alice")
	cs, err := dialMCP(t, h.ts.URL+"/mcp", tk.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if info := cs.InitializeResult().ServerInfo; info.Name != "aenotes" {
		t.Fatalf("server info: %+v", info)
	}
	if cs.InitializeResult().Capabilities.Tools != nil {
		if _, err := cs.ListTools(t.Context(), nil); err != nil {
			t.Fatalf("tools/list: %v", err)
		}
	}
}

// The caller's permission on the board repository is their Caller on every
// tool call: none is refused at the door (403), read is read-only, write
// may write.
func TestRightsReachTheToolContext(t *testing.T) {
	var deps mcpserver.Deps
	h := newHostedWith(t, nil, func(s *Server) {
		s.SetMCPInfo(MCPInfo{Version: "v-test"})
		s.newMCP = whoamiServer(&deps)
	})
	if deps.Store != h.srv.store || deps.Version != "v-test" || deps.BaseURL != h.srv.auth.baseURL() || deps.Live == nil {
		t.Fatalf("deps: %+v", deps)
	}
	h.gh.set("reader", "read")
	h.gh.set("writer", "admin")

	none, _ := h.mintToken(t, "stranger")
	if r := rawInitialize(t, h.ts.URL+"/mcp", none.AccessToken); r.status != http.StatusForbidden {
		t.Fatalf("no access: %d %s", r.status, r.body)
	}
	for login, canWrite := range map[string]bool{"reader": false, "writer": true} {
		tk, _ := h.mintToken(t, login)
		cs, err := dialMCP(t, h.ts.URL+"/mcp", tk.AccessToken)
		if err != nil {
			t.Fatalf("%s: %v", login, err)
		}
		if got := callWhoami(t, cs); got != (whoami{Login: login, CanWrite: canWrite}) {
			t.Fatalf("%s: caller %+v", login, got)
		}
	}
}

// Deps.Live sends a tool's change to the people who have the document open.
func TestLiveReachesTheOpenRoom(t *testing.T) {
	var deps mcpserver.Deps
	h := newHostedWith(t, nil, func(s *Server) { s.newMCP = whoamiServer(&deps) })
	h.gh.set("alice", "write")
	alice := h.signIn(t, "alice")
	_, body := h.do(t, alice, "POST", "/api/v1/boards", `{"title":"Notes"}`)
	var m boards.Meta
	_ = json.Unmarshal([]byte(body), &m)
	h.dial(t, alice, m.ID)
	deadline := time.Now().Add(3 * time.Second)
	for h.srv.sync.GetDoc(m.ID) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the room never opened")
		}
		time.Sleep(10 * time.Millisecond)
	}

	doc := crdt.New()
	pm := doc.GetMap("probe")
	doc.Transact(func(txn *crdt.Transaction) { pm.Set(txn, "from", "agent") })
	deps.Live(t.Context(), m.ID, crdt.EncodeStateAsUpdateV1(doc, nil))
	if got := probe(h.srv, m.ID); got != "agent" {
		t.Fatalf("the open room has %q", got)
	}
	// Nobody has this one open: nothing to send, nothing breaks.
	deps.Live(t.Context(), "not-open", crdt.EncodeStateAsUpdateV1(doc, nil))
}

// A token whose access lapsed is 401 until the client refreshes it.
func TestAnExpiredAccessTokenWorksAgainAfterARefresh(t *testing.T) {
	h := newHostedWith(t, nil, func(s *Server) { s.newMCP = whoamiServer(nil) })
	h.gh.set("alice", "write")
	tk, clientID := h.mintToken(t, "alice")
	a := h.srv.auth
	sid, _, _ := sessionOfAccess(a, tk.AccessToken)
	a.mu.Lock()
	s := a.sessions[sid]
	s.accessExpiry = time.Now().Add(-time.Second)
	a.sessions[sid] = s
	a.mu.Unlock()
	if r := rawInitialize(t, h.ts.URL+"/mcp", tk.AccessToken); r.status != http.StatusUnauthorized {
		t.Fatalf("expired: %d", r.status)
	}
	fresh := tokensOf(t, h.postForm(t, "/oauth/token", map[string][]string{
		"grant_type": {"refresh_token"}, "refresh_token": {tk.RefreshToken}, "client_id": {clientID},
	}))
	// A refresh mints a new access token; the old one is dead.
	if fresh.AccessToken == tk.AccessToken {
		t.Fatal("the refresh kept the access token")
	}
	if r := rawInitialize(t, h.ts.URL+"/mcp", tk.AccessToken); r.status != http.StatusUnauthorized {
		t.Fatalf("old token after refresh: %d", r.status)
	}
	// An agent's session is not a browser's cookie.
	if _, ok := a.session(cookieRequest(sid)); ok {
		t.Fatal("an MCP session worked as a cookie")
	}
	cs, err := dialMCP(t, h.ts.URL+"/mcp", fresh.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if got := callWhoami(t, cs); got.Login != "alice" {
		t.Fatalf("caller %+v", got)
	}
}

// A forge token revoked behind the session's back is 401 invalid_token (so
// the client authorizes again), and the session is gone.
func TestARevokedForgeTokenEndsTheMCPSession(t *testing.T) {
	h := newHostedWith(t, nil, func(s *Server) { s.newMCP = whoamiServer(nil) })
	h.gh.set("alice", "revoked")
	tk, _ := h.mintToken(t, "alice")
	r := rawInitialize(t, h.ts.URL+"/mcp", tk.AccessToken)
	if r.status != http.StatusUnauthorized || !strings.Contains(r.header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("revoked: %d %q", r.status, r.header.Get("WWW-Authenticate"))
	}
	h.srv.auth.mu.Lock()
	_, ok := h.srv.auth.sessions[tk.AccessToken]
	h.srv.auth.mu.Unlock()
	if ok {
		t.Fatal("the session outlived its revoked token")
	}
}

// A request with a foreign Origin still reaches /mcp hosted: the bearer
// token, not the origin, is what authenticates an agent.
func TestHostedMCPTakesAnyOrigin(t *testing.T) {
	h := newHostedWith(t, nil, func(s *Server) { s.newMCP = whoamiServer(nil) })
	h.gh.set("alice", "write")
	tk, _ := h.mintToken(t, "alice")
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+tk.AccessToken)
	req.Header.Set("Origin", "https://claude.ai")
	if r := call(t, req); r.status != http.StatusOK {
		t.Fatalf("initialize from another origin: %d %s", r.status, r.body)
	}
}

func newLocal(t *testing.T, id IdentityFunc) *httptest.Server {
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
	srv := New(store, blobStore, fstest.MapFS{"index.html": {Data: []byte("app")}}, slog.New(slog.DiscardHandler))
	srv.SetIdentity(id)
	srv.newMCP = whoamiServer(nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// Locally /mcp needs no token and acts as the local identity, with write;
// a web page (another Origin) or a rebound DNS name cannot drive it.
func TestLocalMCPActsAsTheLocalIdentity(t *testing.T) {
	ts := newLocal(t, StaticIdentity(Identity{Login: "octocat"}))
	cs, err := dialMCP(t, ts.URL+"/mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := callWhoami(t, cs); got != (whoami{Login: "octocat", CanWrite: true}) {
		t.Fatalf("caller %+v", got)
	}
	// No authorization server locally: discovery is a 404, so a client
	// does not go looking for a sign-in.
	if r := call(t, mustGet(ts.URL+"/.well-known/oauth-protected-resource")); r.status != http.StatusNotFound {
		t.Fatalf("local discovery: %d", r.status)
	}

	cross := map[string]func(*http.Request){
		"another origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"cross-site":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"rebound host":   func(r *http.Request) { r.Host = "evil.example" },
	}
	for name, mod := range cross {
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		mod(req)
		if r := call(t, req); r.status != http.StatusForbidden {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
}

// Anonymous locally (--identity none): the caller has no login, and may still write.
func TestLocalMCPWithoutAnIdentityIsAnonymous(t *testing.T) {
	ts := newLocal(t, nil)
	cs, err := dialMCP(t, ts.URL+"/mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := callWhoami(t, cs); got != (whoami{CanWrite: true}) {
		t.Fatalf("caller %+v", got)
	}
}

func mustGet(u string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	return req
}

func cookieRequest(sid string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sid})
	return r
}
