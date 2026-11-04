// The MCP transport: the tool set (internal/mcpserver) over streamable HTTP
// at /mcp. Ported from aeman internal/server/mcp.go (Apache-2.0): bearer
// middleware answering 401 with the resource-metadata URL, the token's
// session as the caller. Hosted, the caller's rights are decided the way
// the browser's are (sessionAccess); locally /mcp acts as the local
// identity, behind the same cross-origin check aeman's loopback daemon uses.

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aenix-io/aenotes/internal/mcpserver"
)

// mcpSessionTimeout closes an MCP session idle this long (as in aeman: long
// enough to outlast an editor left open over a working day).
const mcpSessionTimeout = 12 * time.Hour

// Keys of auth.TokenInfo.Extra. The verifier sets the first two; mcpAccess
// replaces them with the third, so no tool ever sees the forge token.
const (
	forgeTokenExtraKey = "aenotes_forge_token"
	sessionIDExtraKey  = "aenotes_session"
	accessExtraKey     = "aenotes_access"
)

// MCPInfo is what the MCP server tells about itself and links back to.
type MCPInfo struct {
	// Version is the server's version, as MCP clients see it.
	Version string
	// BaseURL is the public origin documents are linked at; hosted it
	// defaults to OAuthConfig.BaseURL.
	BaseURL string
}

// SetMCPInfo sets the MCP server's version and public origin.
func (s *Server) SetMCPInfo(i MCPInfo) { s.mcpInfo = i }

// mcpDeps is what the tool set gets from the server.
func (s *Server) mcpDeps() mcpserver.Deps {
	base := s.mcpInfo.BaseURL
	if base == "" && s.auth != nil {
		base = s.auth.baseURL()
	}
	return mcpserver.Deps{
		Store: s.store,
		// A tool's change is in the store already: send it to the people
		// who have the document open, as a fetched change is.
		Live: func(ctx context.Context, id string, update []byte) {
			s.ApplyRemote(ctx, map[string][]byte{id: update})
		},
		BaseURL: strings.TrimRight(base, "/"),
		Version: s.mcpInfo.Version,
	}
}

// registerMCP mounts /mcp. Hosted: behind the bearer token and the rights
// check. Locally: as the local identity, and only from no page but this
// server's own (a loopback port is no boundary against a browser).
func (s *Server) registerMCP(mux *http.ServeMux) {
	newMCP := s.newMCP
	if newMCP == nil {
		newMCP = mcpserver.New
	}
	srv := newMCP(s.mcpDeps())
	srv.AddReceivingMiddleware(s.stampCaller)
	opts := &mcp.StreamableHTTPOptions{SessionTimeout: mcpSessionTimeout}
	var h http.Handler
	if s.auth != nil {
		// The go-sdk refuses a loopback connection with a non-loopback Host
		// (DNS-rebinding protection): exactly what a reverse proxy on the
		// same machine sends. The bearer token is the protection here.
		opts.DisableLocalhostProtection = true
		streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, opts)
		h = auth.RequireBearerToken(s.auth.verifyToken, &auth.RequireBearerTokenOptions{
			ResourceMetadataURL: s.auth.resourceMetadataURL(),
		})(s.mcpAccess(streamable))
	} else {
		streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, opts)
		h = http.NewCrossOriginProtection().Handler(streamable)
	}
	// Clients disagree about the trailing slash, and a redirect would lose
	// the POST body.
	mux.Handle("/mcp", h)
	mux.Handle("/mcp/", h)
}

func (a *authManager) resourceMetadataURL() string {
	return a.baseURL() + "/.well-known/oauth-protected-resource"
}

// verifyToken is the auth.TokenVerifier of /mcp: the bearer token is a
// session id. The session is read through the same path the browser's is,
// which renews an expiring forge token in place. An MCP client's token
// also lapses at its accessExpiry, until the client refreshes it.
func (a *authManager) verifyToken(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	// An agent's access token; or, as in aeman, a browser session's id.
	sid, s, ok := a.sessionByAccess(ctx, token)
	if !ok {
		sid = token
		s, ok = a.sessionByID(ctx, token)
		if ok && s.client != "" {
			// An agent's session id is not its access token.
			ok = false
		}
	}
	if !ok {
		return nil, fmt.Errorf("unknown or expired session: %w", auth.ErrInvalidToken)
	}
	exp := s.created.Add(sessionTTL)
	if s.client != "" {
		exp = s.accessExpiry
		if exp.IsZero() || time.Now().After(exp) {
			return nil, fmt.Errorf("access token expired: %w", auth.ErrInvalidToken)
		}
	}
	return &auth.TokenInfo{
		UserID:     s.login,
		Expiration: exp,
		Extra:      map[string]any{forgeTokenExtraKey: s.token, sessionIDExtraKey: sid},
	}, nil
}

// mcpAccess decides the caller's right on every /mcp request, as the gate
// does for the browser: no access to the board repository is 403 before
// any MCP message is read; a session the forge no longer accepts is 401
// (invalid_token), so the client authorizes again. The answer rides the
// token info to stampCaller.
func (s *Server) mcpAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ti := auth.TokenInfoFromContext(r.Context())
		if ti == nil {
			writeError(w, http.StatusUnauthorized, "no bearer token")
			return
		}
		sid, _ := ti.Extra[sessionIDExtraKey].(string)
		tok, _ := ti.Extra[forgeTokenExtraKey].(string)
		acc, err := s.sessionAccess(r.Context(), sid, oauthSession{token: tok, login: ti.UserID})
		switch {
		case errors.Is(err, errSignedOut):
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error="invalid_token", resource_metadata=%q`, s.auth.resourceMetadataURL()))
			writeError(w, http.StatusUnauthorized, "your sign-in is no longer valid")
			return
		case err != nil:
			s.log.Warn("mcp access undecided", "login", ti.UserID, "err", err)
			writeError(w, http.StatusForbidden, "access could not be decided: "+err.Error())
			return
		case acc == AccessNone:
			writeError(w, http.StatusForbidden, "no access to the board repository")
			return
		}
		ti.Extra = map[string]any{accessExtraKey: acc}
		next.ServeHTTP(w, r)
	})
}

// stampCaller puts the caller on every MCP request's context, where the
// tools read it (mcpserver.CallerFrom). The streamable transport hands the
// verified token info over on the request's Extra, not on the context.
func (s *Server) stampCaller(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		return next(mcpserver.WithCaller(ctx, s.callerOf(req.GetExtra())), method, req)
	}
}

// callerOf is who an MCP request acts for. Hosted: the token's login, with
// write only for write access. Locally: the local identity, with write.
func (s *Server) callerOf(extra *mcp.RequestExtra) mcpserver.Caller {
	if extra != nil && extra.TokenInfo != nil {
		acc, _ := extra.TokenInfo.Extra[accessExtraKey].(Access)
		return mcpserver.Caller{Login: extra.TokenInfo.UserID, CanWrite: acc == AccessWrite}
	}
	if s.auth != nil {
		// Unreachable past the bearer middleware; and if it were, read-only.
		return mcpserver.Caller{}
	}
	r := &http.Request{Header: http.Header{}}
	if extra != nil && extra.Header != nil {
		r.Header = extra.Header
	}
	return mcpserver.Caller{Login: s.login(r), CanWrite: true}
}
