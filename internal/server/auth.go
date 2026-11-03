// Ported from aeman internal/server/auth.go (Apache-2.0): the sign-in web
// flow, the sessions and their encrypted file, GitHub App user-token
// renewal. aeman's MCP authorization server (dynamic client registration,
// PKCE codes, the consent screen) is not part of aeboard.

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aenix-io/aeboard/internal/forge"
)

const (
	sessionCookie = "aeboard_session"
	stateCookie   = "aeboard_oauth_state"
	nextCookie    = "aeboard_oauth_next"
	sessionTTL    = 14 * 24 * time.Hour
	// stateTTL bounds a sign-in: the state cookie outlives no abandoned
	// round trip to the forge by more than this.
	stateTTL = 10 * time.Minute
	// tokenRenewAhead is how early a session's GitHub token is renewed. Well
	// inside the 8h lifetime, and long enough that a request never has to
	// use a token in its final seconds.
	tokenRenewAhead = 10 * time.Minute
)

// OAuthConfig enables the hosted (multi-user) mode: each visitor signs in
// with the forge, and their rights on the board repository decide what
// they may do.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	// BaseURL is the public origin (https://boards.example.com): the OAuth
	// redirect URI is built from it, and an https origin makes the cookies
	// Secure.
	BaseURL string
	// Scopes is a space-separated OAuth scope list (default: the forge's;
	// a GitHub App is sent none).
	Scopes string
	// Repo is the board repository; the visitor's permission on it is
	// their right on every board.
	Repo string
	// SessionFile persists the sessions, encrypted with SessionKey; without
	// a key nothing is written and a restart signs everyone out.
	SessionFile string
	// SessionKey encrypts the session file (AES-256-GCM, key = SHA-256 of
	// this value), so a leaked file alone exposes no token.
	SessionKey string
}

type oauthSession struct {
	token     string
	login     string
	name      string
	avatarURL string
	created   time.Time
	// refresh and tokenExpiry track a GitHub App user token, which expires
	// (8h by default) and renews with a SINGLE-USE refresh token. Zero
	// tokenExpiry means the token does not expire (a classic OAuth app, or
	// expiration opted out) and refresh stays empty.
	refresh     string
	tokenExpiry time.Time
}

// persistedState is the on-disk envelope: the sessions, sealed. A file
// written without a key carries nothing.
type persistedState struct {
	EncSessions string `json:"encSessions,omitempty"`
}

// persistedSession is the on-disk form of an oauthSession.
type persistedSession struct {
	Token       string    `json:"token"`
	Login       string    `json:"login"`
	Name        string    `json:"name,omitempty"`
	AvatarURL   string    `json:"avatarUrl,omitempty"`
	Created     time.Time `json:"created"`
	Refresh     string    `json:"refresh,omitempty"`
	TokenExpiry time.Time `json:"tokenExpiry,omitempty"`
}

// authManager runs the forge's OAuth web flow and keeps per-visitor
// sessions in memory, mirrored (encrypted) to SessionFile when a key is set.
type authManager struct {
	cfg    OAuthConfig
	secure bool
	log    *slog.Logger
	client *http.Client
	path   string
	// sessionKey is the AES-256 key sessions are encrypted with at rest (nil
	// when no SessionKey is configured, i.e. sessions are memory-only).
	sessionKey []byte

	// forge is the identity provider. The two URLs default to the forge's
	// and are overridden in tests to point at a stub server.
	forge        forge.Forge
	authorizeURL string
	tokenURL     string

	mu       sync.Mutex
	sessions map[string]oauthSession
	// refreshing single-flights token renewal per session: GitHub's refresh
	// token is SINGLE-USE, so two concurrent renewals would burn it — the
	// second gets "bad_refresh_token" and the session dies for no reason.
	refreshing map[string]chan struct{}
}

func newAuthManager(cfg OAuthConfig, f forge.Forge, log *slog.Logger) *authManager {
	if cfg.Scopes == "" {
		cfg.Scopes = f.DefaultScopes()
	}
	a := &authManager{
		cfg:          cfg,
		secure:       strings.HasPrefix(strings.ToLower(cfg.BaseURL), "https://"),
		log:          log,
		client:       &http.Client{Timeout: 15 * time.Second},
		path:         cfg.SessionFile,
		sessionKey:   deriveSessionKey(cfg.SessionKey),
		forge:        f,
		authorizeURL: f.AuthorizeURL(),
		tokenURL:     f.TokenURL(),
		sessions:     map[string]oauthSession{},
		refreshing:   map[string]chan struct{}{},
	}
	a.load()
	return a
}

// load restores the sessions when a key is configured and the file
// decrypts under it; a wrong key or a damaged file signs everyone out
// rather than trusting garbage.
func (a *authManager) load() {
	if a.path == "" || a.sessionKey == nil {
		return
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		if !os.IsNotExist(err) {
			a.log.Error("session store load failed", "err", err)
		}
		return
	}
	var in persistedState
	if err := json.Unmarshal(data, &in); err != nil {
		a.log.Error("session store load: parse failed", "err", err)
		return
	}
	if in.EncSessions == "" {
		return
	}
	sessions, err := decryptSessions(a.sessionKey, in.EncSessions)
	if err != nil {
		a.log.Error("session decrypt failed (wrong AEBOARD_SESSION_KEY?); signing everyone out", "err", err)
		return
	}
	now := time.Now()
	for sid, s := range sessions {
		if now.Sub(s.Created) <= sessionTTL {
			a.sessions[sid] = oauthSession{
				token: s.Token, login: s.Login, name: s.Name, avatarURL: s.AvatarURL, created: s.Created,
				refresh: s.Refresh, tokenExpiry: s.TokenExpiry,
			}
		}
	}
	a.log.Info("session store restored", "sessions", len(a.sessions))
}

// saveLocked persists the sessions, sealed with the session key — never a
// plaintext token. Without a key nothing is written. Callers hold a.mu.
func (a *authManager) saveLocked() {
	if a.path == "" || a.sessionKey == nil {
		return
	}
	out := persistedState{}
	if len(a.sessions) > 0 {
		sessions := make(map[string]persistedSession, len(a.sessions))
		for sid, s := range a.sessions {
			sessions[sid] = persistedSession{
				Token: s.token, Login: s.login, Name: s.name, AvatarURL: s.avatarURL, Created: s.created,
				Refresh: s.refresh, TokenExpiry: s.tokenExpiry,
			}
		}
		enc, err := encryptSessions(a.sessionKey, sessions)
		if err != nil {
			a.log.Error("session encrypt failed; not persisting sessions", "err", err)
			return
		}
		out.EncSessions = enc
	}
	data, err := json.Marshal(out)
	if err != nil {
		a.log.Error("session store save: marshal failed", "err", err)
		return
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		a.log.Error("session store save: write failed", "err", err)
		return
	}
	if err := os.Rename(tmp, a.path); err != nil {
		a.log.Error("session store save: rename failed", "err", err)
	}
}

// pruneLocked forgets sessions past their TTL. Caller holds a.mu.
func (a *authManager) pruneLocked() {
	for sid, s := range a.sessions {
		if time.Since(s.created) > sessionTTL {
			delete(a.sessions, sid)
		}
	}
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *authManager) redirectURI() string {
	return strings.TrimRight(a.cfg.BaseURL, "/") + "/auth/callback"
}

// dropSession forgets one session by id — what a forge 401 means: the
// token it was built on is dead. Returns the login it belonged to.
func (a *authManager) dropSession(id string) string {
	if id == "" {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[id]
	if !ok {
		return ""
	}
	delete(a.sessions, id)
	a.saveLocked()
	return s.login
}

// sessionID returns the caller's session cookie value ("" when absent). It
// does not validate the session.
func (a *authManager) sessionID(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// session resolves the request's session cookie to a live session, renewing
// its token when it is about to expire (a GitHub App user token lives 8h;
// the session is allowed to outlive it precisely because of this).
func (a *authManager) session(r *http.Request) (oauthSession, bool) {
	sid := a.sessionID(r)
	if sid == "" {
		return oauthSession{}, false
	}
	return a.sessionByID(r.Context(), sid)
}

func (a *authManager) sessionByID(ctx context.Context, sid string) (oauthSession, bool) {
	a.mu.Lock()
	s, ok := a.sessions[sid]
	if !ok {
		a.mu.Unlock()
		return oauthSession{}, false
	}
	if time.Since(s.created) > sessionTTL {
		delete(a.sessions, sid)
		a.saveLocked()
		a.mu.Unlock()
		return oauthSession{}, false
	}
	// The token is renewed a little AHEAD of its expiry, so requests keep
	// riding a valid token instead of queueing behind a renewal.
	if s.tokenExpiry.IsZero() || time.Until(s.tokenExpiry) > tokenRenewAhead || s.refresh == "" {
		a.mu.Unlock()
		return s, true
	}
	return a.renewSession(ctx, sid, s)
}

// renewNow renews a session's token on demand — the forge has just refused
// the one it holds, and the refresh token may still buy a fresh one. False
// when there is nothing to renew with, the refresh was refused, or the
// renewal kept the very token the forge refused.
func (a *authManager) renewNow(ctx context.Context, sid string) (oauthSession, bool) {
	a.mu.Lock()
	s, ok := a.sessions[sid]
	if !ok || s.refresh == "" {
		a.mu.Unlock()
		return oauthSession{}, false
	}
	renewed, ok := a.renewSession(ctx, sid, s)
	if !ok || renewed.token == s.token {
		return oauthSession{}, false
	}
	return renewed, true
}

// renewSession refreshes sid's token, single-flight. Called with a.mu
// HELD; returns with it released.
func (a *authManager) renewSession(ctx context.Context, sid string, s oauthSession) (oauthSession, bool) {
	if ch, busy := a.refreshing[sid]; busy {
		stillValid := time.Now().Before(s.tokenExpiry)
		a.mu.Unlock()
		if stillValid {
			// The old token still works; no reason to queue behind the renewal.
			return s, true
		}
		<-ch
		a.mu.Lock()
		s, ok := a.sessions[sid]
		a.mu.Unlock()
		return s, ok
	}
	ch := make(chan struct{})
	a.refreshing[sid] = ch
	a.mu.Unlock()

	grant, err := a.refreshGrant(ctx, s.refresh)

	a.mu.Lock()
	delete(a.refreshing, sid)
	close(ch)
	cur, ok := a.sessions[sid]
	if !ok {
		a.mu.Unlock()
		return oauthSession{}, false
	}
	if err != nil {
		// The refresh token was refused or unreachable. Keep the session as
		// long as its access token might still work — a network blip must
		// not sign anyone out — and only drop it once both are dead.
		if time.Now().After(cur.tokenExpiry) {
			delete(a.sessions, sid)
			a.saveLocked()
			a.mu.Unlock()
			a.log.Warn("token renewal failed; session dropped", "forge", a.forge.Kind(), "login", cur.login, "err", err)
			return oauthSession{}, false
		}
		a.mu.Unlock()
		a.log.Warn("token renewal failed; keeping the current token", "forge", a.forge.Kind(), "login", cur.login, "err", err)
		return cur, true
	}
	cur.token = grant.token
	cur.tokenExpiry = grant.expiry
	if grant.refresh != "" {
		cur.refresh = grant.refresh
	}
	a.sessions[sid] = cur
	a.saveLocked()
	a.mu.Unlock()
	a.log.Info("token renewed", "forge", a.forge.Kind(), "login", cur.login)
	return cur, true
}

func (a *authManager) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	// Secure follows the public scheme: true behind HTTPS, false only for
	// plain-HTTP local testing.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure set from the deployment scheme
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

// localPath is next if it is a path on this site ("/b/<id>"), else "/":
// the sign-in must not become an open redirect.
func localPath(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return next
}

// handleLogin starts the sign-in: a fresh state bound to this browser by a
// cookie, then off to the forge. ?next=/b/<id> brings the visitor back to
// the board they came from.
func (a *authManager) handleLogin(w http.ResponseWriter, r *http.Request) {
	state := randToken()
	a.setCookie(w, stateCookie, state, int(stateTTL/time.Second))
	a.setCookie(w, nextCookie, url.QueryEscape(localPath(r.URL.Query().Get("next"))), int(stateTTL/time.Second))
	q := url.Values{}
	q.Set("client_id", a.cfg.ClientID)
	q.Set("redirect_uri", a.redirectURI())
	q.Set("response_type", "code")
	// A GitHub App has no scopes: its permissions come from the
	// installation, and a scope in the URL would read, to anyone looking
	// at it, as the access being asked for.
	if !forge.IsAppClientID(a.cfg.ClientID) {
		q.Set("scope", a.cfg.Scopes)
	}
	q.Set("state", state)
	http.Redirect(w, r, a.authorizeURL+"?"+q.Encode(), http.StatusFound)
}

// handleCallback finishes the sign-in: the state must match this browser's
// cookie (a forged callback carries someone else's code), the code is
// traded for the visitor's token, and the forge says who they are.
func (a *authManager) handleCallback(w http.ResponseWriter, r *http.Request) {
	sc, err := r.Cookie(stateCookie)
	if err != nil || sc.Value == "" || sc.Value != r.URL.Query().Get("state") {
		writeError(w, http.StatusBadRequest, "invalid OAuth state")
		return
	}
	a.setCookie(w, stateCookie, "", -1)
	next := "/"
	if nc, err := r.Cookie(nextCookie); err == nil {
		if v, err := url.QueryUnescape(nc.Value); err == nil {
			next = localPath(v)
		}
	}
	a.setCookie(w, nextCookie, "", -1)
	if e := r.URL.Query().Get("error"); e != "" {
		// The visitor declined on the forge's page: back to the app, which
		// offers the sign-in again.
		a.log.Info("sign-in declined", "error", e)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing authorization code")
		return
	}
	grant, err := a.exchange(r.Context(), code)
	if err != nil {
		a.log.Error("oauth token exchange failed", "err", err)
		writeError(w, http.StatusBadGateway, "token exchange failed")
		return
	}
	user, err := a.forge.User(r.Context(), a.client, grant.token)
	if err != nil {
		a.log.Error("oauth user lookup failed", "forge", a.forge.Kind(), "err", err)
		writeError(w, http.StatusBadGateway, "could not read the "+a.forge.Label()+" user")
		return
	}
	sid := randToken()
	a.mu.Lock()
	a.pruneLocked()
	a.sessions[sid] = oauthSession{
		token: grant.token, login: user.Login, name: user.Name, avatarURL: user.AvatarURL, created: time.Now(),
		refresh: grant.refresh, tokenExpiry: grant.expiry,
	}
	a.saveLocked()
	a.mu.Unlock()
	a.setCookie(w, sessionCookie, sid, int(sessionTTL/time.Second))
	a.log.Info("user signed in", "login", user.Login)
	http.Redirect(w, r, next, http.StatusFound) //nolint:gosec // next passed localPath: a path on this site
}

// handleLogout ends the session. It is a POST, so a link on another site
// cannot sign anyone out.
func (a *authManager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if sid := a.sessionID(r); sid != "" {
		a.mu.Lock()
		delete(a.sessions, sid)
		a.saveLocked()
		a.mu.Unlock()
	}
	a.setCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// grant is what the token endpoint hands back. A GitHub App issues
// expiring user tokens (8h) with a single-use refresh token; a classic OAuth
// app leaves both empty and the token lives until it is revoked.
type grant struct {
	token   string
	refresh string
	expiry  time.Time // zero = does not expire
}

func (a *authManager) exchange(ctx context.Context, code string) (grant, error) {
	return a.tokenGrant(ctx, a.forge.ExchangeForm(a.cfg.ClientID, a.cfg.ClientSecret, code, a.redirectURI()))
}

func (a *authManager) refreshGrant(ctx context.Context, refresh string) (grant, error) {
	return a.tokenGrant(ctx, a.forge.RefreshForm(a.cfg.ClientID, a.cfg.ClientSecret, refresh, a.redirectURI()))
}

// tokenGrant posts a form to the forge's token endpoint and reads the grant
// (the RFC 6749 shape).
func (a *authManager) tokenGrant(ctx context.Context, form url.Values) (grant, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return grant{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req) //nolint:gosec // the token endpoint is the operator-configured forge's
	if err != nil {
		return grant{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int64  `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return grant{}, err
	}
	if out.AccessToken == "" {
		return grant{}, fmt.Errorf("no access token (%s: %s)", out.Error, out.ErrorDescription)
	}
	g := grant{token: out.AccessToken, refresh: out.RefreshToken}
	if out.ExpiresIn > 0 {
		g.expiry = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return g, nil
}
