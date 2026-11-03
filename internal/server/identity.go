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
// mode answers from the visitor's session (see EnableOAuth).
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

// meResponse is /api/v1/me: who the visitor is and what they may do.
type meResponse struct {
	Identity
	// Access is the visitor's right on the boards: "write" locally,
	// their permission on the board repository when hosted.
	Access Access `json:"access"`
	// Hosted says the visitor signed in (and can sign out).
	Hosted bool `json:"hosted,omitempty"`
	// Repo is the board repository the rights come from (hosted).
	Repo string `json:"repo,omitempty"`
}

// me answers who the visitor is. Anonymous is 401; when hosted the body
// says so, which is the SPA's cue for the sign-in screen rather than the
// name field of an anonymous local server.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		s.hostedMe(w, r)
		return
	}
	if s.identity != nil {
		if id, ok := s.identity(r); ok {
			writeJSON(w, http.StatusOK, meResponse{Identity: id, Access: AccessWrite})
			return
		}
	}
	writeError(w, http.StatusUnauthorized, "anonymous")
}

func (s *Server) hostedMe(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.auth.session(r)
	if !ok {
		s.writeSignedOut(w, "not signed in")
		return
	}
	acc, ok := s.visitorAccess(w, r, sess)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, meResponse{Identity: identityOf(sess), Access: acc, Hosted: true, Repo: s.auth.cfg.Repo})
}
