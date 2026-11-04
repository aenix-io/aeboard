package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	gws "github.com/gorilla/websocket"
	"github.com/reearth/ygo/crdt"

	"github.com/aenix-io/aenotes/internal/blobs"
	"github.com/aenix-io/aenotes/internal/boards"
	"github.com/aenix-io/aenotes/internal/forge"
)

// The hosted mode in one place: people sign in with GitHub, and their
// permission on the board repository — asked with their own token — is
// what they may do with every board. A fake GitHub plays the forge; no
// test reaches the real one.

const testRepo = "https://github.com/acme/db"

// fakeGitHub is GitHub's OAuth token endpoint, /user and the repository's
// permissions block. A token is "tok-<login>-<n>"; perms names each
// login's permission on acme/db: none, read, write, or revoked (401).
type fakeGitHub struct {
	*httptest.Server
	mu        sync.Mutex
	perms     map[string]string
	expiresIn int // seconds; 0 = tokens that do not expire
	minted    atomic.Int32
	refreshed atomic.Int32
	asked     atomic.Int32 // /repos/acme/db calls
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{perms: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostFormValue("client_secret") != "sec" {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "incorrect_client_credentials"})
			return
		}
		login := r.PostFormValue("code")
		if r.PostFormValue("grant_type") == "refresh_token" {
			f.refreshed.Add(1)
			login = strings.TrimPrefix(r.PostFormValue("refresh_token"), "refresh-")
		}
		if login == "" || login == "bad-code" {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		out := map[string]any{"access_token": fmt.Sprintf("tok-%s-%d", login, f.minted.Add(1))}
		if f.expiresIn > 0 {
			out["expires_in"] = f.expiresIn
			out["refresh_token"] = "refresh-" + login
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		login, ok := f.loginOf(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"login": login, "name": strings.ToUpper(login), "avatar_url": "https://avatars.test/" + login})
	})
	mux.HandleFunc("GET /repos/acme/db", func(w http.ResponseWriter, r *http.Request) {
		f.asked.Add(1)
		login, ok := f.loginOf(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		p := f.perms[login]
		f.mu.Unlock()
		switch p {
		case "read":
			_, _ = w.Write([]byte(`{"permissions":{"pull":true}}`))
		case "write":
			_, _ = w.Write([]byte(`{"permissions":{"pull":true,"push":true}}`))
		case "admin":
			_, _ = w.Write([]byte(`{"permissions":{"pull":true,"push":true,"admin":true}}`))
		case "revoked":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGitHub) set(login, perm string) {
	f.mu.Lock()
	f.perms[login] = perm
	f.mu.Unlock()
}

func (f *fakeGitHub) loginOf(r *http.Request) (string, bool) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(tok, "tok-") {
		return "", false
	}
	rest := strings.TrimPrefix(tok, "tok-")
	i := strings.LastIndex(rest, "-")
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}

type hosted struct {
	srv *Server
	ts  *httptest.Server
	gh  *fakeGitHub
}

func newHosted(t *testing.T, mod func(*OAuthConfig)) *hosted {
	t.Helper()
	return newHostedWith(t, mod, nil)
}

// newHostedWith is newHosted with a last word on the server before its
// handler is built (e.g. the MCP tool set a test observes).
func newHostedWith(t *testing.T, mod func(*OAuthConfig), prep func(*Server)) *hosted {
	t.Helper()
	gh := newFakeGitHub(t)
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
	// After the sockets and the HTTP server are gone (cleanups run last
	// first): let the rooms' final write land before the data dir goes.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		for store.Pending() > 0 && ctx.Err() == nil {
			time.Sleep(10 * time.Millisecond)
		}
	})
	h := &hosted{srv: srv, gh: gh}
	h.ts = httptest.NewUnstartedServer(nil)
	cfg := OAuthConfig{
		ClientID: "Iv23test", ClientSecret: "sec", Repo: testRepo,
		BaseURL: "http://" + h.ts.Listener.Addr().String(),
	}
	if mod != nil {
		mod(&cfg)
	}
	if err := srv.EnableOAuth(cfg, forge.NewGitHubAt(gh.URL)); err != nil {
		t.Fatal(err)
	}
	srv.auth.authorizeURL = gh.URL + "/login/oauth/authorize"
	srv.auth.tokenURL = gh.URL + "/login/oauth/access_token"
	if prep != nil {
		prep(srv)
	}
	h.ts.Config.Handler = srv.Handler()
	h.ts.Start()
	t.Cleanup(h.ts.Close)
	return h
}

// noFollow is a client that shows redirects instead of following them.
var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// answer is what a test keeps of a response whose body it does not read.
type answer struct {
	status   int
	location string
	cookies  []*http.Cookie
}

func (a answer) cookie(name string) *http.Cookie {
	for _, c := range a.cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// send performs req without following redirects.
func send(t *testing.T, req *http.Request) answer {
	t.Helper()
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return answer{status: resp.StatusCode, location: resp.Header.Get("Location"), cookies: resp.Cookies()}
}

func get(t *testing.T, u string) answer {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	return send(t, req)
}

// login drives /auth/login → (the forge) → /auth/callback for a login and
// returns the callback's answer.
func (h *hosted) login(t *testing.T, login, next string) answer {
	t.Helper()
	start := get(t, h.ts.URL+"/auth/login?next="+url.QueryEscape(next))
	loc, _ := url.Parse(start.location)
	req, _ := http.NewRequest("GET", h.ts.URL+"/auth/callback?code="+login+"&state="+loc.Query().Get("state"), nil)
	for _, c := range start.cookies {
		req.AddCookie(c)
	}
	return send(t, req)
}

// signIn is login reduced to the session cookie.
func (h *hosted) signIn(t *testing.T, login string) *http.Cookie {
	t.Helper()
	c := h.login(t, login, "/").cookie(sessionCookie)
	if c == nil || c.Value == "" {
		t.Fatalf("%s: no session cookie", login)
	}
	return c
}

func (h *hosted) do(t *testing.T, c *http.Cookie, method, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, h.ts.URL+path, strings.NewReader(body))
	if c != nil {
		req.AddCookie(c)
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestSignInLandsOnTheNextPageWithASessionCookie(t *testing.T) {
	h := newHosted(t, nil)
	h.gh.set("alice", "write")
	cb := h.login(t, "alice", "/b/abc")
	if cb.status != http.StatusFound || cb.location != "/b/abc" {
		t.Fatalf("callback: %d → %q", cb.status, cb.location)
	}
	c := cb.cookie(sessionCookie)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge <= 0 {
		t.Fatalf("session cookie: %+v", c)
	}
	// The one-shot state is spent: the callback clears it.
	if s := cb.cookie(stateCookie); s == nil || s.MaxAge >= 0 {
		t.Fatalf("state cookie not cleared: %+v", s)
	}
	code, body := h.do(t, c, "GET", "/api/v1/me", "")
	if code != http.StatusOK {
		t.Fatalf("me: %d %s", code, body)
	}
	var me meResponse
	_ = json.Unmarshal([]byte(body), &me)
	if me.Login != "alice" || me.Name != "ALICE" || me.AvatarURL != "https://avatars.test/alice" || me.Access != AccessWrite || !me.Hosted || me.Repo != testRepo {
		t.Fatalf("me: %+v", me)
	}
}

// The cookies are Secure exactly when the public origin is https: a
// plain-http origin is local testing, where a Secure cookie would never
// come back.
func TestCookiesAreSecureBehindHTTPS(t *testing.T) {
	h := newHosted(t, func(c *OAuthConfig) { c.BaseURL = "https://boards.example.com" })
	resp := get(t, h.ts.URL+"/auth/login")
	st := resp.cookie(stateCookie)
	if st == nil || !st.Secure || !st.HttpOnly || st.SameSite != http.SameSiteLaxMode {
		t.Fatalf("state cookie: %+v", st)
	}
	loc, _ := url.Parse(resp.location)
	if got := loc.Query().Get("redirect_uri"); got != "https://boards.example.com/auth/callback" {
		t.Fatalf("redirect_uri = %q", got)
	}
	// A GitHub App (Iv… client id) is sent no scope: its permissions come
	// from the installation.
	if loc.Query().Has("scope") {
		t.Fatalf("a GitHub App sign-in carries a scope: %s", loc)
	}
}

func TestAnOAuthAppAsksForRepoScope(t *testing.T) {
	h := newHosted(t, func(c *OAuthConfig) { c.ClientID = "0123456789abcdef" })
	loc, _ := url.Parse(get(t, h.ts.URL+"/auth/login").location)
	if loc.Query().Get("scope") != "repo" {
		t.Fatalf("scope = %q", loc.Query().Get("scope"))
	}
}

// A callback whose state does not match this browser's cookie is someone
// else's code being planted (login CSRF): refused, no session made.
func TestCallbackRefusesAStateThatIsNotThisBrowsers(t *testing.T) {
	h := newHosted(t, nil)
	st := get(t, h.ts.URL+"/auth/login").cookie(stateCookie)

	cases := map[string]func(*http.Request){
		"no cookie": func(*http.Request) {},
		"other state": func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: stateCookie, Value: "forged"})
		},
		"empty both": func(r *http.Request) {
			r.URL.RawQuery = "code=alice&state="
			r.AddCookie(&http.Cookie{Name: stateCookie, Value: ""})
		},
	}
	for name, mod := range cases {
		req, _ := http.NewRequest("GET", h.ts.URL+"/auth/callback?code=alice&state="+st.Value, nil)
		mod(req)
		cb := send(t, req)
		if cb.status != http.StatusBadRequest || cb.cookie(sessionCookie) != nil {
			t.Errorf("%s: %d, session cookie %v", name, cb.status, cb.cookie(sessionCookie))
		}
	}
	if n := len(h.srv.auth.sessions); n != 0 {
		t.Fatalf("%d sessions after refused callbacks", n)
	}
	// A bad code with the right state is the forge refusing: 502, no session.
	req, _ := http.NewRequest("GET", h.ts.URL+"/auth/callback?code=bad-code&state="+st.Value, nil)
	req.AddCookie(st)
	if cb := send(t, req); cb.status != http.StatusBadGateway || cb.cookie(sessionCookie) != nil {
		t.Fatalf("bad code: %d", cb.status)
	}
}

// ?next= only ever leads back into this site.
func TestNextIsALocalPathOnly(t *testing.T) {
	for in, want := range map[string]string{
		"/b/abc":               "/b/abc",
		"/":                    "/",
		"":                     "/",
		"//evil.example/x":     "/",
		"https://evil.example": "/",
		"/\\evil.example":      "/",
		"b/abc":                "/",
		"/b/abc\r\nSet-Cookie": "/",
	} {
		if got := localPath(in); got != want {
			t.Errorf("localPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	h := newHosted(t, nil)
	h.gh.set("alice", "write")
	c := h.signIn(t, "alice")
	// A GET cannot sign anyone out (a link on another site would).
	if code, _ := h.do(t, c, "GET", "/auth/logout", ""); code == http.StatusSeeOther {
		t.Fatal("GET /auth/logout signed out")
	}
	if code, _ := h.do(t, c, "POST", "/auth/logout", ""); code != http.StatusSeeOther {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := h.do(t, c, "GET", "/api/v1/boards", ""); code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", code)
	}
}

// Without a session the API is 401 and /me says the server is hosted, the
// SPA's cue for "Sign in with GitHub". The SPA itself and the health probe
// stay public.
func TestSignedOutIs401WithTheHostedHint(t *testing.T) {
	h := newHosted(t, nil)
	for _, p := range []string{"/api/v1/boards", "/api/v1/status", "/api/v1/boards/0000000000", "/api/v1/sync/0000000000"} {
		if code, _ := h.do(t, nil, "GET", p, ""); code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", p, code)
		}
	}
	code, body := h.do(t, nil, "GET", "/api/v1/me", "")
	if code != http.StatusUnauthorized || !strings.Contains(body, `"hosted":true`) || !strings.Contains(body, `"forge":"GitHub"`) {
		t.Fatalf("me: %d %s", code, body)
	}
	if code, _ := h.do(t, nil, "GET", "/api/healthz", ""); code != http.StatusOK {
		t.Fatalf("healthz: %d", code)
	}
	if code, body := h.do(t, nil, "GET", "/b/abc", ""); code != http.StatusOK || body != "app" {
		t.Fatalf("spa: %d %q", code, body)
	}
}

func TestAnExpiredSessionIsSignedOut(t *testing.T) {
	h := newHosted(t, nil)
	h.gh.set("alice", "write")
	c := h.signIn(t, "alice")
	h.srv.auth.mu.Lock()
	s := h.srv.auth.sessions[c.Value]
	s.created = time.Now().Add(-sessionTTL - time.Minute)
	h.srv.auth.sessions[c.Value] = s
	h.srv.auth.mu.Unlock()
	if code, _ := h.do(t, c, "GET", "/api/v1/boards", ""); code != http.StatusUnauthorized {
		t.Fatalf("expired session: %d", code)
	}
	if _, live := h.srv.auth.sessions[c.Value]; live {
		t.Fatal("the expired session was kept")
	}
}

// A GitHub App user token lives 8h; the session renews it with the
// single-use refresh token ahead of the expiry, transparently.
func TestAnExpiringUserTokenIsRenewed(t *testing.T) {
	h := newHosted(t, nil)
	h.gh.expiresIn = int(tokenRenewAhead/time.Second) - 1 // already inside the renewal window
	h.gh.set("alice", "write")
	c := h.signIn(t, "alice")
	before := h.srv.auth.sessions[c.Value].token
	if code, _ := h.do(t, c, "GET", "/api/v1/boards", ""); code != http.StatusOK {
		t.Fatalf("boards: %d", code)
	}
	if h.gh.refreshed.Load() == 0 || h.srv.auth.sessions[c.Value].token == before {
		t.Fatal("the expiring token was not renewed")
	}
}

// A visitor whose authorization the forge now refuses (revoked) is signed
// out — 401 and the cookie cleared — rather than told "no access".
func TestARevokedTokenSignsTheVisitorOut(t *testing.T) {
	h := newHosted(t, nil)
	h.gh.set("alice", "revoked")
	c := h.signIn(t, "alice")
	req, _ := http.NewRequest("GET", h.ts.URL+"/api/v1/boards", nil)
	req.AddCookie(c)
	resp := send(t, req)
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("revoked: %d", resp.status)
	}
	if cl := resp.cookie(sessionCookie); cl == nil || cl.MaxAge >= 0 {
		t.Fatalf("session cookie not cleared: %+v", cl)
	}
	if len(h.srv.auth.sessions) != 0 {
		t.Fatal("the session of a revoked token was kept")
	}
}

// The rights table: none sees nothing; read opens boards but writes
// nothing; write and admin do everything. Every REST route and the sync
// socket follow the same answer.
func TestRightsFollowThePermissionOnTheBoardRepository(t *testing.T) {
	h := newHosted(t, nil)
	for login, perm := range map[string]string{"writer": "write", "admin": "admin", "reader": "read", "outsider": "none"} {
		h.gh.set(login, perm)
	}
	writer := h.signIn(t, "writer")
	code, body := h.do(t, writer, "POST", "/api/v1/boards", `{"title":"Retro"}`)
	if code != http.StatusCreated {
		t.Fatalf("writer create: %d %s", code, body)
	}
	var m boards.Meta
	_ = json.Unmarshal([]byte(body), &m)
	svg := `<svg xmlns="http://www.w3.org/2000/svg"/>`
	blob := "/api/v1/boards/" + m.ID + "/blobs/" + blobs.Key([]byte(svg))
	if code, _ := h.do(t, writer, "PUT", blob, svg); code != http.StatusNoContent {
		t.Fatalf("writer blob put: %d", code)
	}

	type call struct{ method, path, body string }
	reads := []call{
		{"GET", "/api/v1/boards", ""},
		{"GET", "/api/v1/boards/" + m.ID, ""},
		{"GET", blob, ""},
		{"GET", "/api/v1/status", ""},
	}
	writes := []call{
		{"POST", "/api/v1/boards", `{"title":"x"}`},
		{"PATCH", "/api/v1/boards/" + m.ID, `{"title":"y"}`},
		{"PUT", blob, svg},
	}
	want := map[string]struct {
		read, write int
		access      Access
	}{
		"outsider": {http.StatusForbidden, http.StatusForbidden, AccessNone},
		"reader":   {http.StatusOK, http.StatusForbidden, AccessRead},
		"admin":    {http.StatusOK, 0, AccessWrite},
	}
	for login, w := range want {
		c := h.signIn(t, login)
		for _, r := range reads {
			if code, body := h.do(t, c, r.method, r.path, r.body); code != w.read {
				t.Errorf("%s %s %s: %d %s, want %d", login, r.method, r.path, code, body, w.read)
			}
		}
		for _, r := range writes {
			code, body := h.do(t, c, r.method, r.path, r.body)
			if w.write != 0 && code != w.write || w.write == 0 && code >= 400 {
				t.Errorf("%s %s %s: %d %s", login, r.method, r.path, code, body)
			}
		}
		_, body := h.do(t, c, "GET", "/api/v1/me", "")
		var me meResponse
		_ = json.Unmarshal([]byte(body), &me)
		if me.Access != w.access {
			t.Errorf("%s: me.access = %q, want %q", login, me.Access, w.access)
		}
	}
	// The outsider cannot even open the socket.
	if code, _ := h.do(t, h.signIn(t, "outsider"), "GET", "/api/v1/sync/"+m.ID, ""); code != http.StatusForbidden {
		t.Errorf("outsider sync: %d, want 403", code)
	}
}

// The sync socket: a reader's updates are dropped (and they are not named
// on the board's commit); a writer's land in the room and name them.
func TestTheSyncSocketIsReadOnlyForReaders(t *testing.T) {
	h := newHosted(t, nil)
	h.gh.set("writer", "write")
	h.gh.set("reader", "read")
	writer := h.signIn(t, "writer")
	_, body := h.do(t, writer, "POST", "/api/v1/boards", `{"title":"Retro"}`)
	var m boards.Meta
	_ = json.Unmarshal([]byte(body), &m)

	reader := h.dial(t, h.signIn(t, "reader"), m.ID)
	sendUpdate(t, reader, "from", "reader")
	time.Sleep(300 * time.Millisecond)
	if got := probe(h.srv, m.ID); got != "" {
		t.Fatalf("a reader's update reached the room: %q", got)
	}
	if eds := h.srv.editors.of(m.ID); len(eds) != 0 {
		t.Fatalf("a reader is an editor: %v", eds)
	}

	w := h.dial(t, writer, m.ID)
	sendUpdate(t, w, "from", "writer")
	deadline := time.Now().Add(3 * time.Second)
	for probe(h.srv, m.ID) != "writer" {
		if time.Now().After(deadline) {
			t.Fatal("a writer's update did not reach the room")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eds := h.srv.editors.of(m.ID); strings.Join(eds, ",") != "writer" {
		t.Fatalf("editors = %v, want the writer", eds)
	}
}

func (h *hosted) dial(t *testing.T, c *http.Cookie, board string) *gws.Conn {
	t.Helper()
	hdr := http.Header{}
	hdr.Set("Cookie", c.String())
	conn, resp, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(h.ts.URL, "http")+"/api/v1/sync/"+board, hdr)
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	resp.Body.Close()
	t.Cleanup(func() { conn.Close() })
	// Drain what the server sends (sync step 1, awareness).
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	return conn
}

// sendUpdate writes one y-websocket "sync update" frame setting
// probe[key] = value, from a fresh Yjs client.
func sendUpdate(t *testing.T, conn *gws.Conn, key, value string) {
	t.Helper()
	doc := crdt.New()
	m := doc.GetMap("probe") // not inside Transact: GetMap takes the doc lock
	doc.Transact(func(txn *crdt.Transaction) { m.Set(txn, key, value) })
	upd := crdt.EncodeStateAsUpdateV1(doc, nil)
	msg := []byte{0, 2} // messageSync, syncUpdate
	msg = appendVarUint(msg, uint64(len(upd)))
	msg = append(msg, upd...)
	if err := conn.WriteMessage(gws.BinaryMessage, msg); err != nil {
		t.Fatal(err)
	}
}

func appendVarUint(b []byte, n uint64) []byte {
	for n >= 0x80 {
		b = append(b, byte(n)|0x80)
		n >>= 7
	}
	return append(b, byte(n))
}

// probe reads probe["from"] off the live room.
func probe(s *Server, board string) string {
	doc := s.sync.GetDoc(board)
	if doc == nil {
		return ""
	}
	v, _ := doc.GetMap("probe").Get("from")
	str, _ := v.(string)
	return str
}

// Writes from another site are refused even with the visitor's cookie.
func TestCrossOriginWritesAreRefused(t *testing.T) {
	h := newHosted(t, nil)
	h.gh.set("alice", "write")
	c := h.signIn(t, "alice")
	req, _ := http.NewRequest("POST", h.ts.URL+"/api/v1/boards", strings.NewReader(`{"title":"x"}`))
	req.AddCookie(c)
	req.Header.Set("Origin", "https://evil.example")
	if resp := send(t, req); resp.status != http.StatusForbidden {
		t.Fatalf("cross-origin create: %d", resp.status)
	}
	req, _ = http.NewRequest("POST", h.ts.URL+"/api/v1/boards", strings.NewReader(`{"title":"x"}`))
	req.AddCookie(c)
	req.Header.Set("Origin", h.srv.auth.cfg.BaseURL)
	if resp := send(t, req); resp.status != http.StatusCreated {
		t.Fatalf("same-origin create: %d", resp.status)
	}
}

// The forge is asked once per visitor per TTL; a stale answer is served at
// once and re-asked behind it.
func TestRightsAreCachedBriefly(t *testing.T) {
	h := newHosted(t, nil)
	h.gh.set("alice", "write")
	c := h.signIn(t, "alice")
	for range 5 {
		if code, _ := h.do(t, c, "GET", "/api/v1/boards", ""); code != http.StatusOK {
			t.Fatalf("boards: %d", code)
		}
	}
	if n := h.gh.asked.Load(); n != 1 {
		t.Fatalf("forge asked %d times, want 1", n)
	}
	// Demoted on GitHub: the cached "write" stands until the TTL, then the
	// re-ask lands and writes are refused.
	h.gh.set("alice", "read")
	h.srv.access.mu.Lock()
	e := h.srv.access.cache["alice"]
	e.at = time.Now().Add(-accessTTL - time.Second)
	h.srv.access.cache["alice"] = e
	h.srv.access.mu.Unlock()
	if code, _ := h.do(t, c, "GET", "/api/v1/boards", ""); code != http.StatusOK {
		t.Fatalf("stale read: %d", code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, _ := h.do(t, c, "POST", "/api/v1/boards", `{"title":"x"}`)
		if code == http.StatusForbidden {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the demotion never landed (last %d)", code)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// With a key the session file survives a restart and holds no plaintext
// token; with another key — or none — nothing comes back.
func TestTheSessionFileIsEncryptedAndKeyBound(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	h := newHosted(t, func(c *OAuthConfig) { c.SessionFile, c.SessionKey = path, "the-key" })
	h.gh.set("alice", "write")
	c := h.signIn(t, "alice")
	token := h.srv.auth.sessions[c.Value].token

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) || strings.Contains(string(data), "alice") {
		t.Fatalf("the session file holds plaintext: %s", data)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode %v", st.Mode().Perm())
	}

	f := forge.NewGitHubAt(h.gh.URL)
	restart := func(key string) *authManager {
		return newAuthManager(OAuthConfig{ClientID: "Iv23test", ClientSecret: "sec", BaseURL: "http://x", SessionFile: path, SessionKey: key}, f, slog.New(slog.DiscardHandler))
	}
	if s, ok := restart("the-key").sessions[c.Value]; !ok || s.token != token || s.login != "alice" || s.name != "ALICE" {
		t.Fatalf("not restored with the right key: %+v", s)
	}
	if n := len(restart("wrong-key").sessions); n != 0 {
		t.Fatalf("a wrong key restored %d sessions", n)
	}
	if n := len(restart("").sessions); n != 0 {
		t.Fatalf("no key restored %d sessions", n)
	}
}

// Without a key sessions live in memory only: nothing is written.
func TestNoKeyWritesNoSessionFile(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	h := newHosted(t, func(c *OAuthConfig) { c.SessionFile = path })
	h.gh.set("alice", "write")
	h.signIn(t, "alice")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a session file was written without a key: %v", err)
	}
}

func TestSessionEncryptRoundTrip(t *testing.T) {
	key := deriveSessionKey("a-strong-secret")
	in := map[string]persistedSession{"sid1": {Token: "tok-1", Login: "alice", Created: time.Now().UTC().Truncate(time.Second)}}
	enc, err := encryptSessions(key, in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := decryptSessions(key, enc)
	if err != nil || out["sid1"].Token != "tok-1" || out["sid1"].Login != "alice" {
		t.Fatalf("round trip: %+v %v", out, err)
	}
	if _, err := decryptSessions(deriveSessionKey("other"), enc); err == nil {
		t.Fatal("a wrong key must fail to decrypt")
	}
	if _, err := decryptSessions(key, enc[:len(enc)-4]+"AAAA"); err == nil {
		t.Fatal("a tampered file must fail to decrypt")
	}
}

// The hosted mode does not start half-configured.
func TestEnableOAuthRefusesAnIncompleteConfig(t *testing.T) {
	store, err := boards.Open(context.Background(), boards.DirBackend{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(store, nil, fstest.MapFS{}, slog.New(slog.DiscardHandler))
	ok := OAuthConfig{ClientID: "Iv23x", ClientSecret: "s", BaseURL: "https://b.example", Repo: testRepo}
	for name, mod := range map[string]func(*OAuthConfig){
		"no secret":     func(c *OAuthConfig) { c.ClientSecret = "" },
		"no base":       func(c *OAuthConfig) { c.BaseURL = "" },
		"bad base":      func(c *OAuthConfig) { c.BaseURL = "boards.example" },
		"no repo":       func(c *OAuthConfig) { c.Repo = "" },
		"repo w/o slug": func(c *OAuthConfig) { c.Repo = "https://github.com/acme" },
	} {
		cfg := ok
		mod(&cfg)
		if err := srv.EnableOAuth(cfg, forge.NewGitHub()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
