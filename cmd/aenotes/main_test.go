package main

import (
	"testing"

	"github.com/aenix-io/aenotes/internal/server"
)

// The URL the MCP tools link documents at: the hosted origin, or the listen
// address with an unspecified host made this machine's.
func TestPublicURL(t *testing.T) {
	hosted := &server.OAuthConfig{BaseURL: "https://notes.example.com"}
	if got := publicURL(hosted, ":8766"); got != "https://notes.example.com" {
		t.Errorf("hosted: %q", got)
	}
	for addr, want := range map[string]string{
		"127.0.0.1:8766": "http://127.0.0.1:8766",
		":8766":          "http://127.0.0.1:8766",
		"0.0.0.0:8766":   "http://127.0.0.1:8766",
		"[::]:8766":      "http://127.0.0.1:8766",
		"[::1]:8766":     "http://[::1]:8766",
		"localhost:9000": "http://localhost:9000",
	} {
		if got := publicURL(nil, addr); got != want {
			t.Errorf("%s: %q, want %q", addr, got, want)
		}
	}
}
