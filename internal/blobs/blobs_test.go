package blobs

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestKeyMatchesBlockSuite(t *testing.T) {
	// BlockSuite's sha(): base64 of SHA-256 with + → - and / → _, padded.
	// sha256("hello") = LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ=
	if got := Key([]byte("hello")); got != "LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ=" {
		t.Fatalf("Key = %s", got)
	}
}

func TestPutThenOpenKeepsContentAndType(t *testing.T) {
	s, _ := Open(t.TempDir())
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	key := Key(svg)
	if err := s.Put(key, "image/svg+xml", bytes.NewReader(svg)); err != nil {
		t.Fatal(err)
	}
	f, mt, err := s.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, svg) || mt != "image/svg+xml" {
		t.Fatalf("got %q type %q", got, mt)
	}
}

// A key can only hold its own content: that is what makes it safe to accept
// uploads from anyone who can edit, and to keep one copy across boards.
func TestPutRejectsContentThatDoesNotHashToKey(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Put(Key([]byte("a")), "", strings.NewReader("b")); err != ErrHashMismatch {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := s.Open(Key([]byte("a"))); err != ErrNotFound {
		t.Fatalf("mismatched content was stored: %v", err)
	}
}

func TestPutIsIdempotentAndFirstTypeWins(t *testing.T) {
	s, _ := Open(t.TempDir())
	k := Key([]byte("x"))
	_ = s.Put(k, "image/png", strings.NewReader("x"))
	if err := s.Put(k, "text/html", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	f, mt, _ := s.Open(k)
	f.Close()
	if mt != "image/png" {
		t.Fatalf("type changed to %q", mt)
	}
}

func TestInvalidKeysNeverTouchTheDisk(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, k := range []string{"", "../../etc/passwd", strings.Repeat("A", 43) + "=", "short="} {
		if ValidKey(k) && k != strings.Repeat("A", 43)+"=" {
			t.Errorf("ValidKey(%q) = true", k)
		}
		if _, _, err := s.Open("../" + k); err != ErrNotFound {
			t.Errorf("Open(../%s) = %v", k, err)
		}
	}
	if err := s.Put("../x", "", strings.NewReader("")); err != ErrHashMismatch {
		t.Fatalf("Put(../x) = %v", err)
	}
}

func TestTooLarge(t *testing.T) {
	s, _ := Open(t.TempDir())
	big := bytes.Repeat([]byte{0}, MaxSize+1)
	if err := s.Put(Key(big), "", bytes.NewReader(big)); err != ErrTooLarge {
		t.Fatalf("err = %v", err)
	}
}
