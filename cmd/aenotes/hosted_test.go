package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"flag"
	"io"
	"path/filepath"
	"testing"
)

func parseHosted(t *testing.T, args ...string) *hostedFlags {
	t.Helper()
	fl := flag.NewFlagSet("serve", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var h hostedFlags
	h.register(fl)
	if err := fl.Parse(args); err != nil {
		t.Fatal(err)
	}
	return &h
}

// The hosted mode is on exactly when the client id AND secret are set; it
// then needs the public origin and the board repository, and the session
// file defaults under --data.
func TestHostedModeIsTheClientPair(t *testing.T) {
	t.Setenv("AENOTES_GITHUB_CLIENT_ID", "")
	t.Setenv("AENOTES_GITHUB_CLIENT_SECRET", "")
	if c, err := parseHosted(t).oauth("https://github.com/a/b", "/d"); c != nil || err != nil {
		t.Fatalf("no client: %+v %v, want the local mode", c, err)
	}
	if _, err := parseHosted(t, "--github-client-id", "Iv23x").oauth("https://github.com/a/b", "/d"); err == nil {
		t.Fatal("an id without a secret must be refused")
	}
	t.Setenv("AENOTES_GITHUB_CLIENT_SECRET", "s")
	if _, err := parseHosted(t, "--github-client-id", "Iv23x").oauth("https://github.com/a/b", "/d"); err == nil {
		t.Fatal("no base URL must be refused")
	}
	if _, err := parseHosted(t, "--github-client-id", "Iv23x", "--base-url", "https://b").oauth("", "/d"); err == nil {
		t.Fatal("no repo must be refused")
	}
	t.Setenv("AENOTES_SESSION_KEY", "k")
	c, err := parseHosted(t, "--github-client-id", "Iv23x", "--base-url", "https://b.example/").oauth("https://github.com/a/b", "/d")
	if err != nil || c.BaseURL != "https://b.example" || c.SessionFile != filepath.Join("/d", "sessions.json") || c.SessionKey != "k" || c.ClientSecret != "s" {
		t.Fatalf("hosted config: %+v %v", c, err)
	}
}

// The App key comes as base64 of the PEM (an env file holds no multi-line
// value) or as the PEM itself; half a configuration is an error.
func TestGitHubAppFromEnv(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	t.Setenv("AENOTES_GITHUB_APP_ID", "")
	t.Setenv("AENOTES_GITHUB_APP_KEY", "")
	if app, err := githubApp(); app != nil || err != nil {
		t.Fatalf("unset: %v %v", app, err)
	}
	t.Setenv("AENOTES_GITHUB_APP_ID", "123")
	if _, err := githubApp(); err == nil {
		t.Fatal("an id without a key must be refused")
	}
	for name, v := range map[string]string{"base64": base64.StdEncoding.EncodeToString(pemKey), "pem": string(pemKey)} {
		t.Setenv("AENOTES_GITHUB_APP_KEY", v)
		if app, err := githubApp(); app == nil || err != nil {
			t.Errorf("%s key: %v", name, err)
		}
	}
	t.Setenv("AENOTES_GITHUB_APP_KEY", "not base64 !")
	if _, err := githubApp(); err == nil {
		t.Fatal("garbage must be refused")
	}
}
