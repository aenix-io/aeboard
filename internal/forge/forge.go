// Package forge is the code host behind the board repository: the identity
// provider the hosted server signs people in with, the authority on who may
// read or write the repository, and (for GitHub) the App credential the
// server pushes with. Everything the server does with a forge goes through
// the Forge interface, so GitLab can follow; only GitHub is implemented.
//
// Ported from aeman internal/forge (Apache-2.0), trimmed to what aenotes
// uses: sign-in, the visitor's access to one repository, the App token.
// aeman's member listing, people lookup and GitLab are left out.
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Kind names a forge.
type Kind string

// The forges aenotes knows.
const (
	GitHub Kind = "github"
)

// User is the signed-in person as the forge describes them.
type User struct {
	Login     string
	Name      string
	AvatarURL string
}

// Errors a forge reports.
var (
	// ErrBadToken is the forge refusing the token it was given (401).
	ErrBadToken = errors.New("forge: the token was rejected")
	// ErrRateLimited is the forge throttling us. It shares the 403 of "you
	// may not see this" and must never be read as one: a moment of
	// throttling would otherwise be remembered as a visitor's lack of
	// access to their own board.
	ErrRateLimited = errors.New("forge: rate limited")
)

// throttled reports whether a refusal is the forge throttling rather than
// answering: 429, or a 403 that names a spent budget or a retry delay.
// GitHub uses 403 for both its primary and secondary rate limits.
func throttled(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if resp.StatusCode != http.StatusForbidden {
		return false
	}
	return resp.Header.Get("Retry-After") != "" ||
		resp.Header.Get("X-RateLimit-Remaining") == "0" ||
		resp.Header.Get("RateLimit-Remaining") == "0"
}

// Forge is one code host.
type Forge interface {
	// Kind names the forge; Label is its display name for UI copy.
	Kind() Kind
	Label() string

	// AuthorizeURL and TokenURL are the OAuth endpoints the sign-in flow
	// uses; DefaultScopes the scope string the board needs when the
	// operator sets none.
	AuthorizeURL() string
	TokenURL() string
	DefaultScopes() string
	// ExchangeForm and RefreshForm are the token endpoint's request bodies
	// in the forge's dialect.
	ExchangeForm(clientID, secret, code, redirectURI string) url.Values
	RefreshForm(clientID, secret, refresh, redirectURI string) url.Values

	// User is who a token belongs to. A rejected token is ErrBadToken.
	User(ctx context.Context, client *http.Client, token string) (User, error)

	// RepoRef is a repository URL as the forge's API names the repository —
	// and the check that the URL names one at all.
	RepoRef(repoURL string) (string, error)
	// Access asks, as the visitor, what they may do with the repository:
	// read it, write it. A repository they cannot see is (false, false, nil);
	// a rejected token is ErrBadToken.
	Access(ctx context.Context, client *http.Client, token, repoURL string) (read, write bool, err error)
}

// ForRepo names the forge a repository lives on: github.com is GitHub.
// Anything else is refused rather than guessed — a hosted server whose
// forge cannot answer "who may read this" must not start.
func ForRepo(repoURL string) (Forge, error) {
	switch host := repoHost(repoURL); host {
	case "github.com":
		return NewGitHub(), nil
	case "":
		return nil, fmt.Errorf("forge: %q is not a repository URL on a forge", repoURL)
	default:
		return nil, fmt.Errorf("forge: %s is not supported (only github.com for now)", host)
	}
}

// repoHost is the host of a repository URL — https://host/…, git@host:…,
// or "" for a bare owner/repo or a local path.
func repoHost(repoURL string) string {
	s := repoURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.IndexAny(s, "/"); j >= 0 {
			s = s[:j]
		}
		if at := strings.LastIndex(s, "@"); at >= 0 {
			s = s[at+1:]
		}
		return strings.ToLower(s)
	}
	if i := strings.Index(s, ":"); i >= 0 && !strings.Contains(s[:i], "/") {
		h := s[:i]
		if at := strings.LastIndex(h, "@"); at >= 0 {
			h = h[at+1:]
		}
		return strings.ToLower(h)
	}
	return ""
}

// repoPath is the path of a repository URL, .git dropped, as its segments:
// https://host/a/b/c.git → [a b c]; git@host:a/b.git → [a b]; a/b → [a b].
func repoPath(repoURL string) []string {
	s := strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(repoURL, "/"), ".git"), "/")
	hosted := false
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		hosted = true
	}
	if i := strings.Index(s, ":"); i >= 0 && !strings.Contains(s[:i], "/") {
		s = s[i+1:]
		hosted = false
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if hosted && len(parts) > 0 {
		parts = parts[1:]
	}
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// decodeJSON reads a JSON body, closing it.
func decodeJSON(resp *http.Response, v any) error {
	defer func() { _ = resp.Body.Close() }()
	return jsonDecode(resp.Body, v)
}

func jsonDecode(r io.Reader, v any) error { return json.NewDecoder(r).Decode(v) }
