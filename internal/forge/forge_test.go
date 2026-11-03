package forge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The hosted server refuses to start on a forge it cannot ask: github.com
// is GitHub; another host, or a local path, is an error, not a guess.
func TestForRepoKnowsGitHubOnly(t *testing.T) {
	for _, u := range []string{"https://github.com/aenix-org/aeboard-db", "git@github.com:aenix-org/aeboard-db.git"} {
		f, err := ForRepo(u)
		if err != nil || f.Kind() != GitHub {
			t.Errorf("ForRepo(%q) = %v, %v; want GitHub", u, f, err)
		}
	}
	for _, u := range []string{"https://gitlab.com/x/y", "../boards.git", "/srv/boards.git"} {
		if _, err := ForRepo(u); err == nil {
			t.Errorf("ForRepo(%q): want an error", u)
		}
	}
}

func TestRepoRefIsOwnerSlashRepo(t *testing.T) {
	gh := NewGitHub()
	for in, want := range map[string]string{
		"https://github.com/acme/aeboard-db.git": "acme/aeboard-db",
		"https://github.com/acme/aeboard-db/":    "acme/aeboard-db",
		"git@github.com:acme/aeboard-db.git":     "acme/aeboard-db",
	} {
		if got, err := gh.RepoRef(in); err != nil || got != want {
			t.Errorf("RepoRef(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := gh.RepoRef("https://github.com/acme"); err == nil {
		t.Error("a URL without owner/repo must be refused")
	}
}

// Access reads GitHub's permissions block as the visitor: pull/triage read,
// push/maintain/admin write; 404 is "cannot see it"; 401 is a dead token;
// a rate limit is an error, never "no access".
func TestAccessMapsGitHubPermissions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/db" {
			http.NotFound(w, r)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer ghu_reader":
			_, _ = w.Write([]byte(`{"permissions":{"pull":true}}`))
		case "Bearer ghu_triager":
			_, _ = w.Write([]byte(`{"permissions":{"pull":true,"triage":true}}`))
		case "Bearer ghu_writer":
			_, _ = w.Write([]byte(`{"permissions":{"pull":true,"push":true}}`))
		case "Bearer ghu_admin":
			_, _ = w.Write([]byte(`{"permissions":{"admin":true}}`))
		case "Bearer ghu_outsider":
			http.NotFound(w, r)
		case "Bearer ghu_throttled":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
		case "Bearer gho_noscope":
			w.Header().Set("X-OAuth-Scopes", "read:user")
			http.NotFound(w, r)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	gh := NewGitHubAt(srv.URL)
	cases := []struct {
		token       string
		read, write bool
		err         error
	}{
		{"ghu_reader", true, false, nil},
		{"ghu_triager", true, false, nil},
		{"ghu_writer", true, true, nil},
		{"ghu_admin", true, true, nil},
		{"ghu_outsider", false, false, nil},
		{"ghu_throttled", false, false, ErrRateLimited},
		{"gho_noscope", false, false, ErrBadToken},
		{"ghu_revoked", false, false, ErrBadToken},
	}
	for _, tc := range cases {
		read, write, err := gh.Access(context.Background(), srv.Client(), tc.token, "https://github.com/acme/db")
		if read != tc.read || write != tc.write || !errors.Is(err, tc.err) {
			t.Errorf("%s: read=%v write=%v err=%v; want %v %v %v", tc.token, read, write, err, tc.read, tc.write, tc.err)
		}
	}
}

func TestUserReadsLoginNameAvatar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"login":"octocat","name":"The Octocat","avatar_url":"https://a/1"}`))
	}))
	defer srv.Close()
	gh := NewGitHubAt(srv.URL)
	u, err := gh.User(context.Background(), srv.Client(), "good")
	if err != nil || u.Login != "octocat" || u.Name != "The Octocat" || u.AvatarURL != "https://a/1" {
		t.Fatalf("User = %+v, %v", u, err)
	}
	if _, err := gh.User(context.Background(), srv.Client(), "bad"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("a rejected token: %v, want ErrBadToken", err)
	}
}
