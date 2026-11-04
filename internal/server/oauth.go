// The OAuth 2.1 authorization server in front of /mcp. Ported from aeman
// internal/server/oauth.go (Apache-2.0): RFC 8414 and RFC 9728 metadata,
// RFC 7591 dynamic client registration, /oauth/authorize with PKCE S256
// that sends the browser through the forge's sign-in and resumes in its
// callback, the consent screen for a non-loopback redirect, /oauth/token.
// As in aeman the access token IS a session (its id), so a token carries
// the person's forge token and their rights follow it. Unlike aeman, the
// token endpoint also grants refresh_token: an access token lives a day,
// and every refresh extends the session behind it by another sessionTTL.

package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/aenix-io/aenotes/internal/forge"
)

const (
	// authCodeTTL bounds how long an issued authorization code is redeemable.
	authCodeTTL = 5 * time.Minute
	// pendingAuthTTL bounds an authorization abandoned on the forge's page.
	pendingAuthTTL = 10 * time.Minute
	// consentTTL bounds an unanswered consent screen.
	consentTTL = 10 * time.Minute
	// mcpAccessTTL is an access token's lifetime: the client refreshes it,
	// and with it the session, at least this often.
	mcpAccessTTL = 24 * time.Hour
	// maxClients caps the registry: registration is public, so the cap
	// stops anyone growing it (and the session file) without bound.
	maxClients = 512
	// maxPending caps authorizations in flight for the same reason.
	maxPending = 4096
)

// oauthClient is a dynamically registered MCP client.
type oauthClient struct {
	redirectURIs []string
}

// pendingAuth is an authorization in flight on the forge's sign-in, keyed by
// the state sent there.
type pendingAuth struct {
	clientID      string
	redirectURI   string
	codeChallenge string
	clientState   string
	created       time.Time
}

// authCode is an issued authorization code: single-use, short-lived, and
// redeemable only by the client, redirect URI and PKCE verifier it was
// issued for.
type authCode struct {
	sid           string
	codeChallenge string
	redirectURI   string
	clientID      string
	expiry        time.Time
}

// pendingConsent is an authorization that passed the forge's sign-in and
// waits for the person to approve a non-loopback redirect: no code exists
// until they do, so a phished authorize link cannot silently hand one to a
// remote host.
type pendingConsent struct {
	grant         grant
	user          forge.User
	clientID      string
	redirectURI   string
	codeChallenge string
	clientState   string
	browserToken  string
	expiry        time.Time
}

// rateLimiter is a fixed-window per-key counter; the window map is reset
// each period, so it never grows across windows.
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	period time.Duration
	window time.Time
	hits   map[string]int
}

func newRateLimiter(limit int, period time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, period: period, hits: map[string]int{}}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if now.Sub(rl.window) >= rl.period {
		rl.hits = map[string]int{}
		rl.window = now
	}
	rl.hits[key]++
	return rl.hits[key] <= rl.limit
}

// clientIP keys the rate limit: the host of RemoteAddr (behind a proxy that
// is the proxy, which makes the limit global — still only a flood's limit).
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// baseURL is the public origin, no trailing slash.
func (a *authManager) baseURL() string {
	return strings.TrimRight(a.cfg.BaseURL, "/")
}

// registerOAuthServer mounts the authorization server (hosted mode only).
func (s *Server) registerOAuthServer(mux *http.ServeMux) {
	a := s.auth
	base := a.baseURL()
	// The resource's metadata, at both the root (what the 401 names) and the
	// RFC 9728 path-suffixed location some clients derive from /mcp.
	prm := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               base + "/mcp",
		AuthorizationServers:   []string{base},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "aenotes",
	})
	mux.Handle("/.well-known/oauth-protected-resource", prm)
	mux.Handle("/.well-known/oauth-protected-resource/mcp", prm)
	mux.HandleFunc("/.well-known/oauth-authorization-server", a.handleAuthServerMetadata)
	mux.HandleFunc("/oauth/register", a.handleRegister)
	mux.HandleFunc("/oauth/authorize", a.handleAuthorize)
	mux.HandleFunc("/oauth/consent", a.handleConsent)
	mux.HandleFunc("/oauth/token", a.handleToken)
}

// allowCORS opens an endpoint agents call to any origin: it is public, and
// takes no cookie.
func allowCORS(w http.ResponseWriter, methods string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", methods)
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// handleAuthServerMetadata is RFC 8414 metadata. PKCE S256 is advertised
// (MCP clients require it). No scopes are: what a token may do is the
// person's permission on the board repository, not a scope.
func (a *authManager) handleAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	allowCORS(w, "GET, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	base := a.baseURL()
	writeJSON(w, http.StatusOK, oauthex.AuthServerMeta{
		Issuer:                            base,
		AuthorizationEndpoint:             base + "/oauth/authorize",
		TokenEndpoint:                     base + "/oauth/token",
		RegistrationEndpoint:              base + "/oauth/register",
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
		CodeChallengeMethodsSupported:     []string{"S256"},
	})
}

// handleRegister is RFC 7591 dynamic client registration: public, CORS-open
// and rate-limited. Only https or loopback http redirect URIs register.
func (a *authManager) handleRegister(w http.ResponseWriter, r *http.Request) {
	allowCORS(w, "POST, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if !a.registerLimiter.allow(clientIP(r)) {
		writeOAuthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "registration rate limit exceeded; try again shortly")
		return
	}
	var meta oauthex.ClientRegistrationMetadata
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&meta); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid JSON body")
		return
	}
	if len(meta.RedirectURIs) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "redirect_uris is required")
		return
	}
	// https (remote clients) or loopback http (local ones, RFC 8252): no
	// javascript:/data:, no cleartext redirect that would leak a code.
	for _, uri := range meta.RedirectURIs {
		if !isAllowedRedirect(uri) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must be https or loopback http")
			return
		}
	}
	clientID := randToken()
	a.mu.Lock()
	a.pruneClientsLocked()
	a.clients[clientID] = oauthClient{redirectURIs: append([]string(nil), meta.RedirectURIs...)}
	a.saveLocked()
	a.mu.Unlock()

	resp := oauthex.ClientRegistrationResponse{
		ClientRegistrationMetadata: oauthex.ClientRegistrationMetadata{
			RedirectURIs:            meta.RedirectURIs,
			ClientName:              meta.ClientName,
			TokenEndpointAuthMethod: "none",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
		},
		ClientID:         clientID,
		ClientIDIssuedAt: time.Now(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(&resp)
}

// handleAuthorize starts an authorization: the client, its redirect URI and
// PKCE are checked, the request is kept, and the browser goes to the forge's
// sign-in; the forge's callback resumes it in handleMCPCallback.
func (a *authManager) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")

	a.mu.Lock()
	client, ok := a.clients[clientID]
	a.mu.Unlock()
	// A bad client or redirect URI is a plain 400: redirecting to an
	// unverified URI would be an open redirect.
	if !ok {
		// A registration lost (a server without the session file) heals for
		// a loopback redirect: the code can only land on the initiator's own
		// machine (RFC 8252), and the client never sees this error — it runs
		// in the browser. Anything else stays an error.
		if clientID == "" || !isLoopbackRedirect(redirectURI) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client", "unknown client_id — register via /oauth/register")
			return
		}
		client = oauthClient{redirectURIs: []string{redirectURI}}
		a.mu.Lock()
		a.pruneClientsLocked()
		a.clients[clientID] = client
		a.saveLocked()
		a.mu.Unlock()
		a.log.Info("oauth: re-registered an unknown loopback client", "client_id", clientID)
	}
	if !slices.Contains(client.redirectURIs, redirectURI) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "redirect_uri is not registered for this client")
		return
	}

	clientState := q.Get("state")
	if q.Get("response_type") != "code" {
		a.redirectError(w, r, redirectURI, clientState, "unsupported_response_type", "response_type must be code")
		return
	}
	if q.Get("code_challenge_method") != "S256" {
		a.redirectError(w, r, redirectURI, clientState, "invalid_request", "code_challenge_method must be S256")
		return
	}
	challenge := q.Get("code_challenge")
	if challenge == "" {
		a.redirectError(w, r, redirectURI, clientState, "invalid_request", "code_challenge is required")
		return
	}

	state := randToken()
	a.mu.Lock()
	a.pruneEphemeralLocked()
	if len(a.pendingAuth) >= maxPending {
		a.mu.Unlock()
		a.redirectError(w, r, redirectURI, clientState, "temporarily_unavailable", "too many authorizations in flight; try again shortly")
		return
	}
	a.pendingAuth[state] = pendingAuth{
		clientID: clientID, redirectURI: redirectURI, codeChallenge: challenge, clientState: clientState, created: time.Now(),
	}
	a.mu.Unlock()

	fq := url.Values{}
	fq.Set("client_id", a.cfg.ClientID)
	fq.Set("redirect_uri", a.redirectURI())
	fq.Set("response_type", "code")
	// As in handleLogin: a GitHub App is sent no scope.
	if !forge.IsAppClientID(a.cfg.ClientID) {
		fq.Set("scope", a.cfg.Scopes)
	}
	fq.Set("state", state)
	http.Redirect(w, r, a.authorizeURL+"?"+fq.Encode(), http.StatusFound)
}

// takePendingAuth pops the authorization a forge callback's state belongs
// to; false for a browser sign-in's state.
func (a *authManager) takePendingAuth(state string) (pendingAuth, bool) {
	if state == "" {
		return pendingAuth{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.pendingAuth[state]
	if ok {
		delete(a.pendingAuth, state)
	}
	if ok && time.Since(p.created) > pendingAuthTTL {
		return pendingAuth{}, false
	}
	return p, ok
}

// handleMCPCallback resumes an authorization after the forge's sign-in: the
// forge's code is traded for the person's token, and the forge says who
// they are. A loopback redirect completes at once (the code can only reach
// the initiator's machine); any other is first put to the person on the
// consent screen.
func (a *authManager) handleMCPCallback(w http.ResponseWriter, r *http.Request, p pendingAuth) {
	if e := r.URL.Query().Get("error"); e != "" {
		a.redirectError(w, r, p.redirectURI, p.clientState, "access_denied", "the sign-in was declined")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		a.redirectError(w, r, p.redirectURI, p.clientState, "invalid_request", "missing authorization code")
		return
	}
	g, err := a.exchange(r.Context(), code)
	if err != nil {
		a.log.Error("mcp oauth token exchange failed", "err", err)
		a.redirectError(w, r, p.redirectURI, p.clientState, "server_error", "token exchange failed")
		return
	}
	user, err := a.forge.User(r.Context(), a.client, g.token)
	if err != nil {
		a.log.Error("mcp oauth user lookup failed", "forge", a.forge.Kind(), "err", err)
		a.redirectError(w, r, p.redirectURI, p.clientState, "server_error", "could not read the "+a.forge.Label()+" user")
		return
	}
	if isLoopbackRedirect(p.redirectURI) {
		a.completeMCPAuth(w, r, g, user, p)
		return
	}
	a.promptConsent(w, g, user, p)
}

// completeMCPAuth opens the session, issues its single-use code and sends
// the browser back to the client's redirect URI.
func (a *authManager) completeMCPAuth(w http.ResponseWriter, r *http.Request, g grant, user forge.User, p pendingAuth) {
	now := time.Now()
	sid := randToken()
	code := randToken()
	a.mu.Lock()
	a.pruneLocked()
	a.sessions[sid] = oauthSession{
		token: g.token, login: user.Login, name: user.Name, avatarURL: user.AvatarURL, created: now,
		refresh: g.refresh, tokenExpiry: g.expiry, client: p.clientID,
	}
	a.saveLocked()
	a.authCodes[code] = authCode{
		sid: sid, codeChallenge: p.codeChallenge, redirectURI: p.redirectURI, clientID: p.clientID, expiry: now.Add(authCodeTTL),
	}
	a.mu.Unlock()
	a.log.Info("mcp client authorized", "login", user.Login, "client_id", p.clientID)

	dest, err := url.Parse(p.redirectURI)
	if err != nil {
		writeError(w, http.StatusBadGateway, "invalid redirect_uri")
		return
	}
	rq := dest.Query()
	rq.Set("code", code)
	if p.clientState != "" {
		rq.Set("state", p.clientState)
	}
	dest.RawQuery = rq.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound) //nolint:gosec // redirect_uri is registered for the client (handleAuthorize)
}

// promptConsent keeps the resolved authorization and shows the approval
// screen, bound to this browser by a cookie: only the person who just
// signed in can approve, and a cross-site POST (no Lax cookie) cannot.
func (a *authManager) promptConsent(w http.ResponseWriter, g grant, user forge.User, p pendingAuth) {
	consentID := randToken()
	browserToken := randToken()
	a.mu.Lock()
	a.pruneEphemeralLocked()
	a.pendingConsent[consentID] = pendingConsent{
		grant: g, user: user, clientID: p.clientID, redirectURI: p.redirectURI, codeChallenge: p.codeChallenge,
		clientState: p.clientState, browserToken: browserToken, expiry: time.Now().Add(consentTTL),
	}
	a.mu.Unlock()
	a.setCookie(w, consentCookie, browserToken, int(consentTTL/time.Second))
	renderConsentPage(w, consentID, user.Login, p.clientID, p.redirectURI, a.cfg.Repo)
}

// handleConsent finishes an authorization the person approved, or ends the
// one they denied. The consent id must match this browser's cookie.
func (a *authManager) handleConsent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_ = r.ParseForm()
	consentID := r.PostFormValue("consent_id")
	approve := r.PostFormValue("action") == "approve"

	cookie, err := r.Cookie(consentCookie)
	a.setCookie(w, consentCookie, "", -1)

	a.mu.Lock()
	pc, ok := a.pendingConsent[consentID]
	if ok {
		delete(a.pendingConsent, consentID)
	}
	a.mu.Unlock()

	if !ok || time.Now().After(pc.expiry) {
		writeError(w, http.StatusBadRequest, "consent request expired or unknown")
		return
	}
	if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(pc.browserToken)) != 1 {
		writeError(w, http.StatusBadRequest, "consent does not match this browser")
		return
	}
	if !approve {
		a.log.Info("mcp authorization denied", "login", pc.user.Login, "client_id", pc.clientID)
		a.redirectError(w, r, pc.redirectURI, pc.clientState, "access_denied", "the request was denied")
		return
	}
	a.completeMCPAuth(w, r, pc.grant, pc.user, pendingAuth{
		clientID: pc.clientID, redirectURI: pc.redirectURI, codeChallenge: pc.codeChallenge, clientState: pc.clientState,
	})
}

// tokenParams are the token request's fields, from a form or JSON body.
type tokenParams struct {
	grantType    string
	code         string
	codeVerifier string
	redirectURI  string
	clientID     string
	refreshToken string
}

func parseTokenParams(w http.ResponseWriter, r *http.Request) tokenParams {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var in struct {
			GrantType    string `json:"grant_type"`
			Code         string `json:"code"`
			CodeVerifier string `json:"code_verifier"`
			RedirectURI  string `json:"redirect_uri"`
			ClientID     string `json:"client_id"`
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		return tokenParams{in.GrantType, in.Code, in.CodeVerifier, in.RedirectURI, in.ClientID, in.RefreshToken}
	}
	_ = r.ParseForm()
	return tokenParams{
		grantType:    r.PostFormValue("grant_type"),
		code:         r.PostFormValue("code"),
		codeVerifier: r.PostFormValue("code_verifier"),
		redirectURI:  r.PostFormValue("redirect_uri"),
		clientID:     r.PostFormValue("client_id"),
		refreshToken: r.PostFormValue("refresh_token"),
	}
}

// handleToken is the token endpoint: authorization_code and refresh_token.
// The access token is the session id.
func (a *authManager) handleToken(w http.ResponseWriter, r *http.Request) {
	allowCORS(w, "POST, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	// Tokens are never cached (RFC 6749 §5.1).
	w.Header().Set("Cache-Control", "no-store")
	p := parseTokenParams(w, r)
	switch p.grantType {
	case "authorization_code":
		a.redeemCode(w, p)
	case "refresh_token":
		a.redeemRefresh(w, p)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

// redeemCode trades a code for the session's access and refresh tokens.
func (a *authManager) redeemCode(w http.ResponseWriter, p tokenParams) {
	// The code is popped first: any attempt, even a failing one, burns it,
	// which stops PKCE guessing and replays.
	a.mu.Lock()
	c, ok := a.authCodes[p.code]
	if ok {
		delete(a.authCodes, p.code)
	}
	a.mu.Unlock()
	switch {
	case !ok:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or already-used code")
		return
	case time.Now().After(c.expiry):
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code has expired")
		return
	case p.redirectURI != c.redirectURI:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	case p.clientID != c.clientID:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client_id mismatch")
		return
	case !verifyPKCE(p.codeVerifier, c.codeChallenge):
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	a.issueTokens(w, c.sid, tokenParams{})
}

// redeemRefresh trades the client's current refresh token for a new pair.
// The refresh token is single-use and bound to its client; each refresh
// restarts the session's lifetime, so a client in use never has to sign in
// again, and one idle for sessionTTL does.
func (a *authManager) redeemRefresh(w http.ResponseWriter, p tokenParams) {
	if p.refreshToken == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	a.mu.Lock()
	sid := ""
	for id, s := range a.sessions {
		if s.mcpRefresh != "" && subtle.ConstantTimeCompare([]byte(s.mcpRefresh), []byte(p.refreshToken)) == 1 {
			sid = id
			break
		}
	}
	a.mu.Unlock()
	if sid == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or already-used refresh token")
		return
	}
	a.issueTokens(w, sid, p)
}

// issueTokens answers with session sid's access token and a fresh refresh
// token. For a refresh (refresh.refreshToken set), the session must belong
// to the requesting client and still hold that refresh token — checked
// under the lock, so of two racing refreshes with one token only one wins.
func (a *authManager) issueTokens(w http.ResponseWriter, sid string, refresh tokenParams) {
	isRefresh := refresh.refreshToken != ""
	now := time.Now()
	a.mu.Lock()
	s, ok := a.sessions[sid]
	switch {
	case !ok || now.Sub(s.created) > sessionTTL:
		if ok {
			delete(a.sessions, sid)
			a.saveLocked()
		}
		a.mu.Unlock()
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "the session is no longer valid; authorize again")
		return
	case isRefresh && s.mcpRefresh != refresh.refreshToken:
		a.mu.Unlock()
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or already-used refresh token")
		return
	case isRefresh && s.client != refresh.clientID:
		a.mu.Unlock()
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client_id mismatch")
		return
	}
	if isRefresh {
		s.created = now
	}
	s.mcpRefresh = randToken()
	s.accessExpiry = now.Add(mcpAccessTTL)
	a.sessions[sid] = s
	a.saveLocked()
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  sid,
		"token_type":    "Bearer",
		"expires_in":    int(mcpAccessTTL / time.Second),
		"refresh_token": s.mcpRefresh,
	})
}

// verifyPKCE checks an S256 code_verifier against the stored challenge.
func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// redirectError sends an OAuth error back to a client's validated redirect URI.
func (a *authManager) redirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, desc string) {
	dest, err := url.Parse(redirectURI)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, code, desc)
		return
	}
	q := dest.Query()
	q.Set("error", code)
	if desc != "" {
		q.Set("error_description", desc)
	}
	if state != "" {
		q.Set("state", state)
	}
	dest.RawQuery = q.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound) //nolint:gosec // redirect_uri is registered for the client (handleAuthorize)
}

// writeOAuthError is an RFC 6749 error response.
func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// isLoopbackRedirect is a redirect to the local machine: http to
// localhost, 127.0.0.1 or [::1], any port and path.
func isLoopbackRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// isAllowedRedirect is a redirect URI that may be registered: https to any
// host, or loopback http.
func isAllowedRedirect(raw string) bool {
	if isLoopbackRedirect(raw) {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Fragment == ""
}

// pruneEphemeralLocked sweeps abandoned authorizations, expired codes (and
// the never-used sessions behind them) and unanswered consents. Caller
// holds a.mu.
func (a *authManager) pruneEphemeralLocked() {
	now := time.Now()
	for state, p := range a.pendingAuth {
		if now.Sub(p.created) > pendingAuthTTL {
			delete(a.pendingAuth, state)
		}
	}
	dropped := false
	for code, c := range a.authCodes {
		if now.After(c.expiry) {
			delete(a.authCodes, code)
			if s, ok := a.sessions[c.sid]; ok && s.accessExpiry.IsZero() {
				delete(a.sessions, c.sid)
				dropped = true
			}
		}
	}
	if dropped {
		a.saveLocked()
	}
	for id, pc := range a.pendingConsent {
		if now.After(pc.expiry) {
			delete(a.pendingConsent, id)
		}
	}
}

// pruneClientsLocked makes room in a full registry by evicting entries; an
// evicted client registers again on its next use. Caller holds a.mu.
func (a *authManager) pruneClientsLocked() {
	for id := range a.clients {
		if len(a.clients) < maxClients {
			return
		}
		delete(a.clients, id)
	}
}

// renderConsentPage is the approval screen. Every value is escaped: the
// client id and redirect URI come from the (attacker-controllable)
// authorize request, the login from the forge.
func renderConsentPage(w http.ResponseWriter, consentID, login, clientID, redirectURI, repo string) {
	page := `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Authorize MCP access — aenotes</title>
<style>
  body{font:15px/1.5 system-ui,sans-serif;max-width:34rem;margin:3rem auto;padding:0 1.25rem;color:#1a1a1a}
  h1{font-size:1.3rem}
  .card{border:1px solid #e2e2e2;border-radius:10px;padding:1.25rem 1.5rem;margin:1.5rem 0}
  dl{display:grid;grid-template-columns:auto 1fr;gap:.4rem 1rem;margin:0}
  dt{color:#666}
  dd{margin:0;word-break:break-all;font-family:ui-monospace,monospace;font-size:.9rem}
  .warn{background:#fff8e1;border:1px solid #ffe082;border-radius:8px;padding:.75rem 1rem;font-size:.9rem}
  .row{display:flex;gap:.75rem;margin-top:1.5rem}
  button{font:inherit;padding:.6rem 1.2rem;border-radius:8px;border:1px solid #ccc;cursor:pointer}
  .approve{background:#1f6feb;color:#fff;border-color:#1f6feb}
</style></head><body>
<h1>Authorize MCP access</h1>
<p>An application wants to use aenotes as <strong>@` + html.EscapeString(login) + `</strong>.</p>
<div class="card"><dl>
  <dt>Client</dt><dd>` + html.EscapeString(clientID) + `</dd>
  <dt>Redirects to</dt><dd>` + html.EscapeString(redirectURI) + `</dd>
</dl></div>
<p class="warn">Only approve if you started this from a tool you trust and recognize the address above. Approving lets it read and edit the documents in ` + html.EscapeString(repo) + ` as you, with your rights on that repository.</p>
<form method="POST" action="/oauth/consent" class="row">
  <input type="hidden" name="consent_id" value="` + html.EscapeString(consentID) + `">
  <button class="approve" type="submit" name="action" value="approve">Approve</button>
  <button type="submit" name="action" value="deny">Deny</button>
</form>
</body></html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The page is never framed: approving must be the person's own click.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(page))
}
