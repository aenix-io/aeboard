package server

import (
	"net/http"
	"net/url"
)

// Identity is who a request comes from, as the SPA shows it: the login on
// the visitor's cursor and an avatar next to it.
type Identity struct {
	Login     string `json:"login"`
	Name      string `json:"name,omitempty"`
	AvatarURL string `json:"avatarUrl,omitempty"`
}

// IdentityFunc resolves a request's identity; ok=false means anonymous.
//
// Local mode (`aeboard serve` on a laptop) answers every request with the
// account the local gh CLI is logged in as, like `aeman serve`; the hosted
// mode will answer from the OAuth session instead.
type IdentityFunc func(r *http.Request) (Identity, bool)

// StaticIdentity answers every request with one identity.
func StaticIdentity(id Identity) IdentityFunc {
	return func(*http.Request) (Identity, bool) { return id, true }
}

// GitHubAvatarURL is GitHub's avatar image for a login (as in aeman).
func GitHubAvatarURL(login string) string {
	return "https://avatars.githubusercontent.com/" + url.PathEscape(login) + "?size=64"
}

// SetIdentity sets how requests are attributed (nil: everyone anonymous).
func (s *Server) SetIdentity(f IdentityFunc) { s.identity = f }

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	if s.identity != nil {
		if id, ok := s.identity(r); ok {
			writeJSON(w, http.StatusOK, id)
			return
		}
	}
	writeError(w, http.StatusUnauthorized, "anonymous")
}
