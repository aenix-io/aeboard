// Per-visitor rights in the hosted mode. Ported from aeman
// internal/server/access.go (Apache-2.0), narrowed from aeman's list of
// domains to aenotes's one board repository: the visitor's permission on
// it, asked with their own token, is their right on every board.

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aenix-io/aenotes/internal/forge"
)

// Access is what a visitor may do with the boards.
type Access string

// The rights, from the visitor's permission on the board repository.
const (
	// AccessNone: the repository is not theirs to see — every board is 403.
	AccessNone Access = "none"
	// AccessRead (pull, triage): boards open, the sync socket is read-only,
	// REST writes are 403.
	AccessRead Access = "read"
	// AccessWrite (push, maintain, admin): everything.
	AccessWrite Access = "write"
)

// accessTTL is how long a visitor's answer is trusted before it is asked
// again (behind the answer: see forgeAccess.rights).
const accessTTL = 60 * time.Second

// rightsRevalidateTimeout bounds a background rights check.
const rightsRevalidateTimeout = 30 * time.Second

type accessCtxKey struct{}

func withAccess(ctx context.Context, a Access) context.Context {
	return context.WithValue(ctx, accessCtxKey{}, a)
}

// accessFrom is the request's right. A request the hosted gate never saw —
// the local mode, where the one visitor is the gh account and the forge's
// own check happens on push — may do everything.
func accessFrom(ctx context.Context) Access {
	if a, ok := ctx.Value(accessCtxKey{}).(Access); ok {
		return a
	}
	return AccessWrite
}

// forgeAccess asks the forge, with the visitor's token, what they may do
// with the board repository, and caches the answer per visitor. A stale
// answer stands in while the forge is unreachable; without one the error is
// the visitor's.
type forgeAccess struct {
	forge  forge.Forge
	client *http.Client
	repo   string
	ttl    time.Duration
	now    func() time.Time
	logger *slog.Logger

	mu    sync.Mutex
	cache map[string]cachedAccess // by login
	// refreshing single-flights the background check per visitor: the
	// requests of one page load share the one they triggered.
	refreshing map[string]bool
}

type cachedAccess struct {
	access Access
	at     time.Time
}

func newForgeAccess(f forge.Forge, client *http.Client, repo string, log *slog.Logger) *forgeAccess {
	return &forgeAccess{forge: f, client: client, repo: repo, ttl: accessTTL, now: time.Now, logger: log,
		cache: map[string]cachedAccess{}, refreshing: map[string]bool{}}
}

// rights is the visitor's access. A fresh answer comes from the cache; a
// stale one is served at once and re-asked behind the answer, so a visitor
// coming back after a break waits for nothing.
func (f *forgeAccess) rights(ctx context.Context, token, login string) (Access, error) {
	f.mu.Lock()
	c, ok := f.cache[login]
	f.mu.Unlock()
	if ok && f.now().Sub(c.at) < f.ttl {
		return c.access, nil
	}
	if ok {
		f.revalidate(token, login)
		return c.access, nil
	}
	return f.probe(ctx, token, login)
}

// forget drops a visitor's cached answer: the token behind it is dead.
func (f *forgeAccess) forget(login string) {
	f.mu.Lock()
	delete(f.cache, login)
	f.mu.Unlock()
}

func (f *forgeAccess) revalidate(token, login string) {
	f.mu.Lock()
	if f.refreshing[login] {
		f.mu.Unlock()
		return
	}
	f.refreshing[login] = true
	f.mu.Unlock()
	go func() {
		// Detached from the request that noticed: it is already answered.
		ctx, cancel := context.WithTimeout(context.Background(), rightsRevalidateTimeout)
		defer cancel()
		if _, err := f.probe(ctx, token, login); err != nil && f.logger != nil {
			f.logger.Warn("rights revalidation failed", "login", login, "err", err)
		}
		f.mu.Lock()
		delete(f.refreshing, login)
		f.mu.Unlock()
	}()
}

// probe asks the forge and records the answer.
func (f *forgeAccess) probe(ctx context.Context, token, login string) (Access, error) {
	read, write, err := f.forge.Access(ctx, f.client, token, f.repo)
	if err != nil {
		f.mu.Lock()
		c, ok := f.cache[login]
		if errors.Is(err, forge.ErrBadToken) {
			// The authorization itself is gone: a stale answer must not
			// paper over that.
			delete(f.cache, login)
			ok = false
		}
		f.mu.Unlock()
		if ok {
			return c.access, nil // the forge is unreachable: the last answer stands
		}
		return AccessNone, err
	}
	a := AccessNone
	switch {
	case write:
		a = AccessWrite
	case read:
		a = AccessRead
	}
	f.mu.Lock()
	f.cache[login] = cachedAccess{access: a, at: f.now()}
	f.mu.Unlock()
	return a, nil
}

// errSignedOut is a session the forge no longer accepts: its token was
// refused, renewed and refused again, and the session is gone.
var errSignedOut = errors.New("the sign-in is no longer valid")

// sessionAccess resolves the right of session sid. A token the forge
// refuses is renewed once (the refresh token may still buy a fresh one);
// if that fails too, the session is dropped and the answer is errSignedOut.
// The browser (visitorAccess) and /mcp (mcpAccess) both decide through it.
func (s *Server) sessionAccess(ctx context.Context, sid string, sess oauthSession) (Access, error) {
	acc, err := s.access.rights(ctx, sess.token, sess.login)
	if errors.Is(err, forge.ErrBadToken) {
		if renewed, ok := s.auth.renewNow(ctx, sid); ok {
			acc, err = s.access.rights(ctx, renewed.token, sess.login)
			s.log.Info("session token renewed after the forge refused it", "login", sess.login)
		}
	}
	if errors.Is(err, forge.ErrBadToken) {
		s.access.forget(sess.login)
		if dropped := s.auth.dropSession(sid); dropped != "" {
			s.log.Warn("the forge rejected a session token; session dropped", "login", dropped)
		}
		return AccessNone, errSignedOut
	}
	return acc, err
}

// visitorAccess resolves a signed-in visitor's right (see sessionAccess); a
// dropped session signs the browser out. ok=false means the answer was
// already written.
func (s *Server) visitorAccess(w http.ResponseWriter, r *http.Request, sess oauthSession) (Access, bool) {
	acc, err := s.sessionAccess(r.Context(), s.auth.sessionID(r), sess)
	if err == nil {
		return acc, true
	}
	if errors.Is(err, errSignedOut) {
		s.auth.setCookie(w, sessionCookie, "", -1)
		s.writeSignedOut(w, "your sign-in is no longer valid")
		return AccessNone, false
	}
	s.log.Warn("access undecided", "login", sess.login, "err", err)
	writeError(w, http.StatusForbidden, "access could not be decided: "+err.Error())
	return AccessNone, false
}

// gate guards /api/v1 in the hosted mode: a visitor without a session is
// 401; without access to the board repository, 403; with read access, any
// write is 403 (the sync socket goes read-only instead, in Authorize). /me
// answers for itself, so the SPA can tell "sign in" from "no access".
//
// Only /api/v1 is the cookie's: the OAuth endpoints agents call (/oauth/*,
// /.well-known/*) and /mcp, which takes a bearer token and no cookie, are
// outside it, so neither the session nor the origin check applies to them.
func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil || !strings.HasPrefix(r.URL.Path, "/api/v1/") || r.URL.Path == "/api/v1/me" {
			next.ServeHTTP(w, r)
			return
		}
		unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		if unsafe && !s.sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		sess, ok := s.auth.session(r)
		if !ok {
			s.writeSignedOut(w, "not signed in")
			return
		}
		acc, ok := s.visitorAccess(w, r, sess)
		if !ok {
			return
		}
		switch {
		case acc == AccessNone:
			writeError(w, http.StatusForbidden, "no access to the board repository")
			return
		case acc == AccessRead && unsafe:
			writeError(w, http.StatusForbidden, "read-only access to the board repository")
			return
		}
		next.ServeHTTP(w, r.WithContext(withAccess(r.Context(), acc)))
	})
}

// sameOrigin backs the SameSite=Lax cookie for writes: a browser names the
// page a request comes from, and only the public origin may write. A
// request without an Origin (not a browser) is let through; it carries no
// ambient cookie of a visitor anyway unless it was given one.
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	base, err := url.Parse(s.auth.cfg.BaseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(origin, base.Scheme+"://"+base.Host)
}

// sessionIdentity is the IdentityFunc of the hosted mode: the signed-in
// visitor, as the forge described them at sign-in.
func (s *Server) sessionIdentity(r *http.Request) (Identity, bool) {
	sess, ok := s.auth.session(r)
	if !ok {
		return Identity{}, false
	}
	return identityOf(sess), true
}

func identityOf(sess oauthSession) Identity {
	avatar := sess.avatarURL
	if avatar == "" {
		avatar = GitHubAvatarURL(sess.login)
	}
	return Identity{Login: sess.login, Name: sess.name, AvatarURL: avatar}
}

// writeSignedOut is the hosted 401: the body says the server is hosted, so
// the SPA offers the sign-in (and with which forge).
func (s *Server) writeSignedOut(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": msg, "hosted": true, "forge": s.auth.forge.Label()})
}

// EnableOAuth switches the server to the hosted mode: sign-in with the
// forge, sessions, and rights from the visitor's permission on cfg.Repo.
// The identity of every request then comes from its session.
func (s *Server) EnableOAuth(cfg OAuthConfig, f forge.Forge) error {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return errors.New("hosted mode needs the OAuth client id and secret")
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return fmt.Errorf("hosted mode needs the public base URL (https://host), got %q", cfg.BaseURL)
	}
	if _, err := f.RepoRef(cfg.Repo); err != nil {
		return fmt.Errorf("hosted mode needs the board repository on %s: %w", f.Label(), err)
	}
	s.auth = newAuthManager(cfg, f, s.log)
	s.access = newForgeAccess(f, s.auth.client, cfg.Repo, s.log)
	s.identity = s.sessionIdentity
	return nil
}
