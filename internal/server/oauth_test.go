package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// The authorization server MCP clients sign in through (ported from
// aeman's, tests in its style): metadata, dynamic registration, the
// authorize round trip through the forge, consent for a remote redirect,
// the token endpoint. A fake GitHub plays the forge.

const (
	loopbackRedirect = "http://127.0.0.1:9999/callback"
	remoteRedirect   = "https://client.example/cb"
	testVerifier     = "pkce-verifier-sufficiently-long-0123456789"
)

var consentIDRe = regexp.MustCompile(`name="consent_id" value="([^"]+)"`)

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// call sends a request and reads the answer whole, without following redirects.
type reply struct {
	status  int
	header  http.Header
	body    string
	cookies []*http.Cookie
}

func (r reply) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("not JSON (%d): %s", r.status, r.body)
	}
	return out
}

func (r reply) location(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse(r.header.Get("Location"))
	if err != nil || r.header.Get("Location") == "" {
		t.Fatalf("no redirect (%d): %s", r.status, r.body)
	}
	return u
}

func call(t *testing.T, req *http.Request) reply {
	t.Helper()
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: string(b), cookies: resp.Cookies()}
}

func (h *hosted) get(t *testing.T, path string) reply {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.ts.URL+path, nil)
	return call(t, req)
}

func (h *hosted) postJSON(t *testing.T, path, body string) reply {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return call(t, req)
}

func (h *hosted) postForm(t *testing.T, path string, form url.Values, cookies ...*http.Cookie) reply {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return call(t, req)
}

func (h *hosted) register(t *testing.T, redirectURI string) string {
	t.Helper()
	r := h.postJSON(t, "/oauth/register", `{"redirect_uris":["`+redirectURI+`"],"client_name":"test"}`)
	if r.status != http.StatusCreated {
		t.Fatalf("register: %d %s", r.status, r.body)
	}
	var resp oauthex.ClientRegistrationResponse
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil || resp.ClientID == "" {
		t.Fatalf("register: %v %s", err, r.body)
	}
	return resp.ClientID
}

func authorizeQuery(clientID, redirectURI string) url.Values {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("code_challenge", pkceChallenge(testVerifier))
	q.Set("code_challenge_method", "S256")
	q.Set("state", "client-state")
	return q
}

// throughForge drives /oauth/authorize and the forge's sign-in as login,
// and returns the forge callback's answer.
func (h *hosted) throughForge(t *testing.T, login string, q url.Values) reply {
	t.Helper()
	az := h.get(t, "/oauth/authorize?"+q.Encode())
	if az.status != http.StatusFound {
		t.Fatalf("authorize: %d %s", az.status, az.body)
	}
	fl := az.location(t)
	if !strings.HasPrefix(fl.String(), h.gh.URL) {
		t.Fatalf("authorize went to %s, not the forge", fl)
	}
	return h.get(t, "/auth/callback?code="+login+"&state="+fl.Query().Get("state"))
}

// mintCode is register → authorize → the forge → a code on the loopback
// redirect.
func (h *hosted) mintCode(t *testing.T, login string) (code, clientID string) {
	t.Helper()
	clientID = h.register(t, loopbackRedirect)
	cb := h.throughForge(t, login, authorizeQuery(clientID, loopbackRedirect))
	if cb.status != http.StatusFound {
		t.Fatalf("callback: %d %s", cb.status, cb.body)
	}
	loc := cb.location(t)
	if !strings.HasPrefix(loc.String(), loopbackRedirect+"?") || loc.Query().Get("state") != "client-state" || loc.Query().Get("code") == "" {
		t.Fatalf("callback redirect: %s", loc)
	}
	return loc.Query().Get("code"), clientID
}

func (h *hosted) redeem(t *testing.T, code, clientID, redirectURI, verifier string) reply {
	t.Helper()
	return h.postForm(t, "/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier},
	})
}

type tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func tokensOf(t *testing.T, r reply) tokens {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("token: %d %s", r.status, r.body)
	}
	var tk tokens
	if err := json.Unmarshal([]byte(r.body), &tk); err != nil || tk.AccessToken == "" || tk.RefreshToken == "" {
		t.Fatalf("token: %v %s", err, r.body)
	}
	return tk
}

// mintToken is the whole flow for login, down to the tokens.
func (h *hosted) mintToken(t *testing.T, login string) (tokens, string) {
	t.Helper()
	code, clientID := h.mintCode(t, login)
	return tokensOf(t, h.redeem(t, code, clientID, loopbackRedirect, testVerifier)), clientID
}

func oauthError(t *testing.T, r reply, status int, want string) {
	t.Helper()
	if r.status != status || r.json(t)["error"] != want {
		t.Fatalf("want %d %s, got %d %s", status, want, r.status, r.body)
	}
}

func TestTheMetadataDocumentsNameTheEndpoints(t *testing.T) {
	h := newHosted(t, nil)
	base := h.srv.auth.baseURL()
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		r := h.get(t, path)
		if r.status != http.StatusOK || r.header.Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("%s: %d %v", path, r.status, r.header)
		}
		var prm oauthex.ProtectedResourceMetadata
		_ = json.Unmarshal([]byte(r.body), &prm)
		if prm.Resource != base+"/mcp" || len(prm.AuthorizationServers) != 1 || prm.AuthorizationServers[0] != base {
			t.Fatalf("%s: %+v", path, prm)
		}
	}
	r := h.get(t, "/.well-known/oauth-authorization-server")
	if r.status != http.StatusOK || r.header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("authorization server metadata: %d %v", r.status, r.header)
	}
	var asm oauthex.AuthServerMeta
	_ = json.Unmarshal([]byte(r.body), &asm)
	if asm.Issuer != base || asm.AuthorizationEndpoint != base+"/oauth/authorize" || asm.TokenEndpoint != base+"/oauth/token" ||
		asm.RegistrationEndpoint != base+"/oauth/register" {
		t.Fatalf("endpoints: %+v", asm)
	}
	if strings.Join(asm.CodeChallengeMethodsSupported, ",") != "S256" ||
		strings.Join(asm.GrantTypesSupported, ",") != "authorization_code,refresh_token" ||
		strings.Join(asm.TokenEndpointAuthMethodsSupported, ",") != "none" {
		t.Fatalf("capabilities: %+v", asm)
	}
	// Rights are the repository permission, not scopes: none are offered,
	// so a client asks for none.
	if len(asm.ScopesSupported) != 0 {
		t.Fatalf("scopes advertised: %v", asm.ScopesSupported)
	}
}

// Discovery this server does not offer is a 404, not the SPA's index.html
// that an OAuth client would try to parse.
func TestUnknownDiscoveryIsNotTheApp(t *testing.T) {
	h := newHosted(t, nil)
	for _, path := range []string{"/.well-known/openid-configuration", "/oauth/nope"} {
		if r := h.get(t, path); r.status != http.StatusNotFound || r.body == "app" {
			t.Errorf("%s: %d %q", path, r.status, r.body)
		}
	}
}

func TestRegistrationTakesHTTPSOrLoopbackRedirectsOnly(t *testing.T) {
	h := newHosted(t, nil)
	for _, uri := range []string{remoteRedirect, loopbackRedirect, "http://localhost/cb", "http://[::1]:5000/cb"} {
		h.register(t, uri)
	}
	refused := map[string]string{
		`{"redirect_uris":[]}`: "invalid_client_metadata",
		`{}`:                   "invalid_client_metadata",
		`not json`:             "invalid_client_metadata",
		`{"redirect_uris":["http://evil.example/cb"]}`:           "invalid_redirect_uri",
		`{"redirect_uris":["javascript:alert(1)"]}`:              "invalid_redirect_uri",
		`{"redirect_uris":["data:text/html,x"]}`:                 "invalid_redirect_uri",
		`{"redirect_uris":["https:///cb"]}`:                      "invalid_redirect_uri",
		`{"redirect_uris":["https://client.example/cb#x"]}`:      "invalid_redirect_uri",
		`{"redirect_uris":["` + remoteRedirect + `","ftp://x"]}`: "invalid_redirect_uri",
	}
	for body, want := range refused {
		oauthError(t, h.postJSON(t, "/oauth/register", body), http.StatusBadRequest, want)
	}
	h.srv.auth.mu.Lock()
	n := len(h.srv.auth.clients)
	h.srv.auth.mu.Unlock()
	if n != 4 {
		t.Fatalf("%d clients registered, want 4", n)
	}
	if r := h.get(t, "/oauth/register"); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET register: %d", r.status)
	}
}

func TestRegistrationIsRateLimited(t *testing.T) {
	h := newHosted(t, nil)
	h.srv.auth.registerLimiter = newRateLimiter(2, time.Hour)
	h.register(t, loopbackRedirect)
	h.register(t, loopbackRedirect)
	oauthError(t, h.postJSON(t, "/oauth/register", `{"redirect_uris":["`+loopbackRedirect+`"]}`), http.StatusTooManyRequests, "temporarily_unavailable")
}

// The client and redirect URI are checked before anything is redirected
// (an unverified URI would be an open redirect); everything after that is
// reported back to the client's redirect URI.
func TestAuthorizeRefusesBadClientsAndPKCE(t *testing.T) {
	h := newHosted(t, nil)
	clientID := h.register(t, loopbackRedirect)

	q := authorizeQuery("nobody", remoteRedirect)
	if r := h.get(t, "/oauth/authorize?"+q.Encode()); r.status != http.StatusBadRequest || r.json(t)["error"] != "invalid_client" {
		t.Fatalf("unknown client: %d %s", r.status, r.body)
	}
	q = authorizeQuery(clientID, "http://127.0.0.1:1/other")
	if r := h.get(t, "/oauth/authorize?"+q.Encode()); r.status != http.StatusBadRequest || r.header.Get("Location") != "" {
		t.Fatalf("unregistered redirect: %d %v", r.status, r.header)
	}

	cases := map[string]struct {
		mod  func(url.Values)
		want string
	}{
		"no challenge":    {func(q url.Values) { q.Del("code_challenge") }, "invalid_request"},
		"plain method":    {func(q url.Values) { q.Set("code_challenge_method", "plain") }, "invalid_request"},
		"no method":       {func(q url.Values) { q.Del("code_challenge_method") }, "invalid_request"},
		"implicit grant":  {func(q url.Values) { q.Set("response_type", "token") }, "unsupported_response_type"},
		"no response typ": {func(q url.Values) { q.Del("response_type") }, "unsupported_response_type"},
	}
	for name, c := range cases {
		q := authorizeQuery(clientID, loopbackRedirect)
		c.mod(q)
		r := h.get(t, "/oauth/authorize?"+q.Encode())
		if r.status != http.StatusFound {
			t.Errorf("%s: %d %s", name, r.status, r.body)
			continue
		}
		loc := r.location(t)
		if !strings.HasPrefix(loc.String(), loopbackRedirect) || loc.Query().Get("error") != c.want || loc.Query().Get("state") != "client-state" {
			t.Errorf("%s: %s", name, loc)
		}
	}
}

// A loopback client whose registration was lost heals itself: the code can
// only land on the initiator's machine. A remote one does not.
func TestAnUnknownLoopbackClientIsRegisteredAgain(t *testing.T) {
	h := newHosted(t, nil)
	cb := h.throughForge(t, "alice", authorizeQuery("lost-client", loopbackRedirect))
	if cb.status != http.StatusFound || cb.location(t).Query().Get("code") == "" {
		t.Fatalf("callback: %d %s", cb.status, cb.body)
	}
	if r := h.get(t, "/oauth/authorize?"+authorizeQuery("lost-remote", remoteRedirect).Encode()); r.status != http.StatusBadRequest {
		t.Fatalf("unknown remote client: %d", r.status)
	}
}

// A code is single-use and redeemable only with the client, redirect URI and
// verifier it was issued for; a failed attempt burns it too.
func TestTheCodeIsSingleUseAndBound(t *testing.T) {
	h := newHosted(t, nil)
	wrong := map[string]func(code, client string) reply{
		"other client":   func(code, _ string) reply { return h.redeem(t, code, "someone-else", loopbackRedirect, testVerifier) },
		"other redirect": func(code, c string) reply { return h.redeem(t, code, c, "http://127.0.0.1:1/x", testVerifier) },
		"wrong verifier": func(code, c string) reply { return h.redeem(t, code, c, loopbackRedirect, testVerifier+"x") },
		"no verifier":    func(code, c string) reply { return h.redeem(t, code, c, loopbackRedirect, "") },
		"unknown code":   func(_, c string) reply { return h.redeem(t, "made-up", c, loopbackRedirect, testVerifier) },
	}
	for name, try := range wrong {
		code, clientID := h.mintCode(t, "alice")
		oauthError(t, try(code, clientID), http.StatusBadRequest, "invalid_grant")
		// The right request, after the wrong one: the code is gone.
		if name != "unknown code" {
			oauthError(t, h.redeem(t, code, clientID, loopbackRedirect, testVerifier), http.StatusBadRequest, "invalid_grant")
		}
	}
	code, clientID := h.mintCode(t, "alice")
	tokensOf(t, h.redeem(t, code, clientID, loopbackRedirect, testVerifier))
	oauthError(t, h.redeem(t, code, clientID, loopbackRedirect, testVerifier), http.StatusBadRequest, "invalid_grant")
}

func TestAnExpiredCodeIsRefusedAndItsSessionSwept(t *testing.T) {
	h := newHosted(t, nil)
	code, clientID := h.mintCode(t, "alice")
	a := h.srv.auth
	a.mu.Lock()
	c := a.authCodes[code]
	c.expiry = time.Now().Add(-time.Second)
	a.authCodes[code] = c
	a.pruneEphemeralLocked()
	_, kept := a.sessions[c.sid]
	a.mu.Unlock()
	if kept {
		t.Fatal("the session of a code nobody redeemed outlived the code")
	}
	oauthError(t, h.redeem(t, code, clientID, loopbackRedirect, testVerifier), http.StatusBadRequest, "invalid_grant")
}

// The access token is the session id; it lives mcpAccessTTL and the
// refresh token (single-use, bound to the client) renews it.
func TestTokenExchangeAndRefresh(t *testing.T) {
	h := newHosted(t, nil)
	tk, clientID := h.mintToken(t, "alice")
	if tk.TokenType != "Bearer" || tk.ExpiresIn != int(mcpAccessTTL/time.Second) {
		t.Fatalf("token: %+v", tk)
	}
	a := h.srv.auth
	_, s, ok := sessionOfAccess(a, tk.AccessToken)
	if !ok || s.login != "alice" || s.client != clientID || s.mcpRefresh != tk.RefreshToken {
		t.Fatalf("the access token is not alice's session: %+v", s)
	}

	refresh := func(rt, client string) reply {
		return h.postForm(t, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {client}})
	}
	oauthError(t, refresh(tk.RefreshToken, "someone-else"), http.StatusBadRequest, "invalid_grant")
	oauthError(t, refresh("", clientID), http.StatusBadRequest, "invalid_request")
	next := tokensOf(t, refresh(tk.RefreshToken, clientID))
	// Both rotate: a leaked access token dies with the next refresh.
	if next.AccessToken == tk.AccessToken || next.RefreshToken == tk.RefreshToken {
		t.Fatalf("refresh: %+v after %+v", next, tk)
	}
	// Spent: the old refresh token is no longer anyone's.
	oauthError(t, refresh(tk.RefreshToken, clientID), http.StatusBadRequest, "invalid_grant")

	// JSON bodies are read too.
	r := h.postJSON(t, "/oauth/token", `{"grant_type":"refresh_token","refresh_token":"`+next.RefreshToken+`","client_id":"`+clientID+`"}`)
	last := tokensOf(t, r)
	if r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("token answer is cacheable: %v", r.header)
	}

	// A session past its lifetime cannot be refreshed back to life.
	sid, _, _ := sessionOfAccess(a, last.AccessToken)
	a.mu.Lock()
	s = a.sessions[sid]
	s.created = time.Now().Add(-sessionTTL - time.Minute)
	a.sessions[sid] = s
	a.mu.Unlock()
	oauthError(t, refresh(last.RefreshToken, clientID), http.StatusBadRequest, "invalid_grant")

	oauthError(t, h.postForm(t, "/oauth/token", url.Values{"grant_type": {"password"}}), http.StatusBadRequest, "unsupported_grant_type")
}

// consentFor drives a remote-redirect authorization to the consent screen.
func (h *hosted) consentFor(t *testing.T, login string) (consentID string, cookie *http.Cookie, clientID string) {
	t.Helper()
	clientID = h.register(t, remoteRedirect)
	cb := h.throughForge(t, login, authorizeQuery(clientID, remoteRedirect))
	if cb.status != http.StatusOK || cb.header.Get("Location") != "" {
		t.Fatalf("a remote redirect got no consent screen: %d %v", cb.status, cb.header)
	}
	m := consentIDRe.FindStringSubmatch(cb.body)
	if m == nil || !strings.Contains(cb.body, "@"+login) || !strings.Contains(cb.body, remoteRedirect) {
		t.Fatalf("consent page: %s", cb.body)
	}
	for _, c := range cb.cookies {
		if c.Name == consentCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("consent cookie: %+v", cookie)
	}
	return m[1], cookie, clientID
}

func TestARemoteRedirectNeedsConsent(t *testing.T) {
	h := newHosted(t, nil)

	// Approved: the code goes to the remote redirect and redeems.
	id, cookie, clientID := h.consentFor(t, "alice")
	r := h.postForm(t, "/oauth/consent", url.Values{"consent_id": {id}, "action": {"approve"}}, cookie)
	loc := r.location(t)
	if !strings.HasPrefix(loc.String(), remoteRedirect+"?") || loc.Query().Get("code") == "" || loc.Query().Get("state") != "client-state" {
		t.Fatalf("approve: %s", loc)
	}
	tokensOf(t, h.redeem(t, loc.Query().Get("code"), clientID, remoteRedirect, testVerifier))
	// A consent answers once.
	if r := h.postForm(t, "/oauth/consent", url.Values{"consent_id": {id}, "action": {"approve"}}, cookie); r.status != http.StatusBadRequest {
		t.Fatalf("second approve: %d", r.status)
	}

	// Another browser (no cookie, or someone else's) cannot approve it.
	id, _, _ = h.consentFor(t, "alice")
	if r := h.postForm(t, "/oauth/consent", url.Values{"consent_id": {id}, "action": {"approve"}}); r.status != http.StatusBadRequest || r.header.Get("Location") != "" {
		t.Fatalf("approve without the cookie: %d %v", r.status, r.header)
	}
	id, _, _ = h.consentFor(t, "alice")
	forged := &http.Cookie{Name: consentCookie, Value: "forged"}
	if r := h.postForm(t, "/oauth/consent", url.Values{"consent_id": {id}, "action": {"approve"}}, forged); r.status != http.StatusBadRequest {
		t.Fatalf("approve with a forged cookie: %d", r.status)
	}

	// Denied: the client hears access_denied, no code.
	id, cookie, _ = h.consentFor(t, "alice")
	loc = h.postForm(t, "/oauth/consent", url.Values{"consent_id": {id}, "action": {"deny"}}, cookie).location(t)
	if loc.Query().Get("error") != "access_denied" || loc.Query().Get("code") != "" {
		t.Fatalf("deny: %s", loc)
	}
	if r := h.get(t, "/oauth/consent"); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET consent: %d", r.status)
	}
}

// Declining on the forge's page reaches the client as access_denied.
func TestDecliningAtTheForgeReachesTheClient(t *testing.T) {
	h := newHosted(t, nil)
	clientID := h.register(t, loopbackRedirect)
	az := h.get(t, "/oauth/authorize?"+authorizeQuery(clientID, loopbackRedirect).Encode())
	state := az.location(t).Query().Get("state")
	loc := h.get(t, "/auth/callback?error=access_denied&state="+state).location(t)
	if !strings.HasPrefix(loc.String(), loopbackRedirect) || loc.Query().Get("error") != "access_denied" {
		t.Fatalf("decline: %s", loc)
	}
}

// Registered clients survive a restart with or without the session key;
// the MCP sessions (the tokens) only with it, sealed like the browser's.
func TestClientsAndTokensSurviveARestart(t *testing.T) {
	for _, key := range []string{"k", ""} {
		path := t.TempDir() + "/sessions.json"
		h := newHosted(t, func(c *OAuthConfig) { c.SessionFile = path; c.SessionKey = key })
		tk, clientID := h.mintToken(t, "alice")

		again := newAuthManager(h.srv.auth.cfg, h.srv.auth.forge, h.srv.log)
		if _, ok := again.clients[clientID]; !ok {
			t.Fatalf("key %q: the client was forgotten", key)
		}
		_, s, ok := sessionOfAccess(again, tk.AccessToken)
		if key == "" {
			if ok {
				t.Fatal("a session was written without a key")
			}
			continue
		}
		if !ok || s.client != clientID || s.mcpRefresh != tk.RefreshToken || s.accessExpiry.IsZero() {
			t.Fatalf("the MCP session did not survive: %+v", s)
		}
	}
}

// The endpoints agents call carry no cookie and come from anywhere: the
// cross-origin guard of the cookie API does not apply to them, and still
// applies to the API.
func TestTheOAuthEndpointsAreOpenToAgentsOfAnyOrigin(t *testing.T) {
	h := newHosted(t, nil)
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/oauth/register", strings.NewReader(`{"redirect_uris":["`+loopbackRedirect+`"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://claude.ai")
	if r := call(t, req); r.status != http.StatusCreated || r.header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("register from another origin: %d %s", r.status, r.body)
	}
	code, clientID := h.mintCode(t, "alice")
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {loopbackRedirect}, "code_verifier": {testVerifier}}
	req, _ = http.NewRequest(http.MethodPost, h.ts.URL+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://claude.ai")
	tokensOf(t, call(t, req))
	for _, path := range []string{"/oauth/register", "/oauth/token"} {
		req, _ = http.NewRequest(http.MethodOptions, h.ts.URL+path, nil)
		req.Header.Set("Origin", "https://claude.ai")
		if r := call(t, req); r.status != http.StatusNoContent || r.header.Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("preflight %s: %d", path, r.status)
		}
	}

	h.gh.set("alice", "write")
	c := h.signIn(t, "alice")
	req, _ = http.NewRequest(http.MethodPost, h.ts.URL+"/api/v1/folders", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Origin", "https://claude.ai")
	req.AddCookie(c)
	if r := call(t, req); r.status != http.StatusForbidden {
		t.Fatalf("a cross-origin cookie write: %d", r.status)
	}
}

// sessionOfAccess finds the session an MCP access token belongs to.
func sessionOfAccess(a *authManager, token string) (string, oauthSession, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, s := range a.sessions {
		if s.mcpAccess == token && token != "" {
			return id, s, true
		}
	}
	return "", oauthSession{}, false
}
