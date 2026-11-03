// Ported from aeman internal/forge/github.go (Apache-2.0); the collaborator
// listing and the people lookup are dropped.

package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// github is GitHub: OAuth at github.com, REST at api.github.com, owner/repo
// slugs and a permissions block per repository.
type github struct {
	api string // REST base, https://api.github.com
}

const githubAPIBase = "https://api.github.com"

// NewGitHub is github.com.
func NewGitHub() Forge { return &github{api: githubAPIBase} }

// NewGitHubAt is GitHub with its REST base elsewhere — tests.
func NewGitHubAt(api string) Forge { return &github{api: strings.TrimRight(api, "/")} }

func (g *github) Kind() Kind    { return GitHub }
func (g *github) Label() string { return "GitHub" }

func (g *github) AuthorizeURL() string {
	return "https://github.com/login/oauth/authorize"
}

func (g *github) TokenURL() string {
	return "https://github.com/login/oauth/access_token" //nolint:gosec // OAuth endpoint, not a credential
}

// DefaultScopes: `repo` is what an OAuth App needs to see a private board
// repository; a GitHub App ignores the parameter (its permissions come from
// the installation) and is not sent one.
func (g *github) DefaultScopes() string { return "repo" }

func (g *github) ExchangeForm(clientID, secret, code, redirectURI string) url.Values {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	return form
}

// RefreshForm renews a GitHub App user token. The refresh token is
// single-use: the grant that comes back carries its replacement.
func (g *github) RefreshForm(clientID, secret, refresh, _ string) url.Values {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refresh)
	return form
}

func (g *github) get(ctx context.Context, client *http.Client, token, path string) (*http.Response, error) {
	// The path is the operator's repository under the forge's API base,
	// never anything a visitor supplied.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.api+path, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "aeboard")
	return client.Do(req) //nolint:gosec // see above
}

func (g *github) User(ctx context.Context, client *http.Client, token string) (User, error) {
	resp, err := g.get(ctx, client, token, "/user")
	if err != nil {
		return User{}, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		_ = resp.Body.Close()
		return User{}, ErrBadToken
	case resp.StatusCode != http.StatusOK:
		_ = resp.Body.Close()
		return User{}, fmt.Errorf("user endpoint returned %s", resp.Status)
	}
	var u struct {
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := decodeJSON(resp, &u); err != nil {
		return User{}, err
	}
	if u.Login == "" {
		return User{}, fmt.Errorf("user endpoint named no login")
	}
	return User{Login: u.Login, Name: u.Name, AvatarURL: u.AvatarURL}, nil
}

// RepoRef is owner/repo: the last two path segments.
func (g *github) RepoRef(repoURL string) (string, error) {
	parts := repoPath(repoURL)
	if len(parts) < 2 {
		return "", fmt.Errorf("repository url %q has no owner/repo", repoURL)
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
}

// Access asks GET /repos/{owner}/{repo} as the visitor: the permissions
// block says what they may do; a 404 is what GitHub says for a repository
// the visitor cannot see.
func (g *github) Access(ctx context.Context, client *http.Client, token, repoURL string) (read, write bool, err error) {
	slug, err := g.RepoRef(repoURL)
	if err != nil {
		return false, false, err
	}
	resp, err := g.get(ctx, client, token, "/repos/"+slug)
	if err != nil {
		return false, false, err
	}
	switch {
	case throttled(resp):
		_ = resp.Body.Close()
		return false, false, fmt.Errorf("%w: %s", ErrRateLimited, resp.Status)
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusForbidden:
		// A GitHub App's user token has no scopes by design — its reach
		// comes from the installations — so the scope check cannot apply.
		stale := !IsUserToServerToken(token) && staleScope(resp)
		_ = resp.Body.Close()
		if stale {
			// A token minted before this server asked for `repo` cannot see
			// a private repository, and GitHub says 404 either way: that is
			// an authorization that no longer covers what the board needs.
			return false, false, fmt.Errorf("%w: the sign-in predates the scopes this board needs", ErrBadToken)
		}
		return false, false, nil
	case resp.StatusCode == http.StatusUnauthorized:
		_ = resp.Body.Close()
		return false, false, ErrBadToken
	case resp.StatusCode/100 != 2:
		_ = resp.Body.Close()
		return false, false, fmt.Errorf("forge answered %s", resp.Status)
	}
	var body struct {
		Permissions struct {
			Admin    bool `json:"admin"`
			Maintain bool `json:"maintain"`
			Push     bool `json:"push"`
			Triage   bool `json:"triage"`
			Pull     bool `json:"pull"`
		} `json:"permissions"`
	}
	if err := decodeJSON(resp, &body); err != nil {
		return false, false, err
	}
	p := body.Permissions
	return p.Pull || p.Triage || p.Push || p.Maintain || p.Admin, p.Push || p.Maintain || p.Admin, nil
}

// staleScope reports whether a refusal is the token's scopes rather than
// the visitor's access: GitHub names an OAuth token's scopes in every
// answer, and one without `repo` cannot see a private repository at all. A
// forge that names no scopes (a fine-grained token, another server) tells
// us nothing, and the refusal is taken at face value.
func staleScope(resp *http.Response) bool {
	scopes, named := resp.Header["X-Oauth-Scopes"]
	if !named {
		return false
	}
	for _, s := range strings.Split(strings.Join(scopes, ","), ",") {
		if strings.TrimSpace(s) == "repo" {
			return false
		}
	}
	return true
}
