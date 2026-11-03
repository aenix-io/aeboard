package main

import (
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aenix-io/aeboard/internal/forge"
	"github.com/aenix-io/aeboard/internal/server"
)

// hostedFlags configure the hosted (multi-user) mode. It is on when the
// GitHub OAuth client id and secret are set; the secrets themselves are
// environment-only, so they stay out of `ps`.
type hostedFlags struct {
	baseURL     *string
	clientID    *string
	sessionFile *string
}

func (h *hostedFlags) register(fl *flag.FlagSet) {
	h.baseURL = fl.String("base-url", os.Getenv("AEBOARD_BASE_URL"),
		"public origin of the hosted server (https://host); the OAuth callback is <base-url>/auth/callback")
	h.clientID = fl.String("github-client-id", os.Getenv("AEBOARD_GITHUB_CLIENT_ID"),
		"GitHub App (or OAuth App) client id; with AEBOARD_GITHUB_CLIENT_SECRET it turns on the hosted mode")
	h.sessionFile = fl.String("session-file", os.Getenv("AEBOARD_SESSION_FILE"),
		"where sessions are kept, encrypted with AEBOARD_SESSION_KEY (default <data>/sessions.json)")
}

// oauth is the hosted configuration, or nil for the local mode.
func (h *hostedFlags) oauth(repo, data string) (*server.OAuthConfig, error) {
	id := strings.TrimSpace(*h.clientID)
	secret := strings.TrimSpace(os.Getenv("AEBOARD_GITHUB_CLIENT_SECRET"))
	switch {
	case id == "" && secret == "":
		return nil, nil //nolint:nilnil // no client configured: the local mode, not an error
	case id == "" || secret == "":
		return nil, errors.New("the hosted mode needs both AEBOARD_GITHUB_CLIENT_ID and AEBOARD_GITHUB_CLIENT_SECRET")
	}
	if *h.baseURL == "" {
		return nil, errors.New("the hosted mode needs AEBOARD_BASE_URL (--base-url), the public origin the OAuth callback returns to")
	}
	if repo == "" {
		return nil, errors.New("the hosted mode needs the board repository (--repo https://github.com/<org>/<repo>): rights come from it")
	}
	// Before anything is cloned: rights come from the forge, so the
	// repository must live on one aeboard can ask.
	if _, err := forge.ForRepo(repo); err != nil {
		return nil, fmt.Errorf("the hosted mode: --repo: %w", err)
	}
	file := *h.sessionFile
	if file == "" {
		file = filepath.Join(data, "sessions.json")
	}
	return &server.OAuthConfig{
		ClientID:     id,
		ClientSecret: secret,
		BaseURL:      strings.TrimRight(*h.baseURL, "/"),
		Repo:         repo,
		SessionFile:  file,
		SessionKey:   os.Getenv("AEBOARD_SESSION_KEY"),
	}, nil
}

func sessionsNote(c server.OAuthConfig) string {
	if c.SessionKey == "" {
		return "memory only (set AEBOARD_SESSION_KEY to keep them across restarts)"
	}
	return c.SessionFile
}

// githubApp is the GitHub App credential the server pushes with, when
// AEBOARD_GITHUB_APP_ID is set. The key comes as base64 of the PEM (an env
// file cannot hold a multi-line value) or as the PEM itself.
func githubApp() (*forge.GitHubApp, error) {
	id := strings.TrimSpace(os.Getenv("AEBOARD_GITHUB_APP_ID"))
	raw := strings.TrimSpace(os.Getenv("AEBOARD_GITHUB_APP_KEY"))
	switch {
	case id == "" && raw == "":
		return nil, nil //nolint:nilnil // no app configured is not an error
	case id == "":
		return nil, errors.New("AEBOARD_GITHUB_APP_KEY is set without AEBOARD_GITHUB_APP_ID")
	case raw == "":
		return nil, errors.New("AEBOARD_GITHUB_APP_ID needs the app's private key in AEBOARD_GITHUB_APP_KEY (base64 of the PEM)")
	}
	if !strings.Contains(raw, "BEGIN") {
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("AEBOARD_GITHUB_APP_KEY is neither PEM nor base64 of it: %w", err)
		}
		raw = string(decoded)
	}
	return forge.NewGitHubApp(id, []byte(raw))
}
