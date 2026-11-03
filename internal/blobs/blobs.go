// Package blobs stores board attachments — images, stickers, files —
// outside the board's Yjs state.
//
// A blob is addressed by its content, the way BlockSuite names it: the
// SHA-256 of the bytes as padded base64url (44 characters). The store checks
// the hash on every write, so a key can only ever hold its own content, and
// identical files used on several boards are kept once. Blobs never change
// and are never deleted by a client: a key may be shared by many boards and
// by undo history.
//
// Layout: <dir>/blobs/<ab>/<key> plus <key>.type holding the media type the
// uploader declared (an SVG sniffs as text/xml, which an <img> refuses).
package blobs

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// MaxSize is the largest blob accepted.
const MaxSize = 32 << 20

var (
	// ErrNotFound is returned for a key the store does not hold.
	ErrNotFound = errors.New("blob not found")
	// ErrHashMismatch is returned when the content does not hash to the key.
	ErrHashMismatch = errors.New("blob content does not match its key")
	// ErrTooLarge is returned for content over MaxSize.
	ErrTooLarge = errors.New("blob too large")
)

// Store is a directory of content-addressed blobs.
type Store struct{ dir string }

// Open returns the blob store under dir/blobs.
func Open(dir string) (*Store, error) {
	d := filepath.Join(dir, "blobs")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: d}, nil
}

// Key returns the key BlockSuite gives content.
func Key(content []byte) string {
	sum := sha256.Sum256(content)
	return base64.URLEncoding.EncodeToString(sum[:])
}

// ValidKey reports whether key has the shape of a content key.
func ValidKey(key string) bool {
	if len(key) != 44 || !strings.HasSuffix(key, "=") {
		return false
	}
	b, err := base64.URLEncoding.DecodeString(key)
	return err == nil && len(b) == sha256.Size
}

func (s *Store) path(key string) string {
	// Two-character fan-out keeps directories small. Base64url keys are
	// case-sensitive, so the fan-out is lower-cased only for the directory;
	// the file name keeps the exact key.
	return filepath.Join(s.dir, strings.ToLower(key[:2]), key)
}

// Put stores content under key after checking that it hashes to key.
// Storing a key that already exists is a no-op.
func (s *Store) Put(key, mediaType string, r io.Reader) error {
	if !ValidKey(key) {
		return ErrHashMismatch
	}
	content, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return err
	}
	if len(content) > MaxSize {
		return ErrTooLarge
	}
	if Key(content) != key {
		return ErrHashMismatch
	}
	p := s.path(key)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if mt := cleanType(mediaType); mt != "" {
		if err := writeAtomic(p+".type", []byte(mt)); err != nil {
			return err
		}
	}
	return writeAtomic(p, content)
}

// Open returns the blob's content and its media type ("" when the uploader
// declared none).
func (s *Store) Open(key string) (*os.File, string, error) {
	if !ValidKey(key) {
		return nil, "", ErrNotFound
	}
	f, err := os.Open(s.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	mt, _ := os.ReadFile(s.path(key) + ".type")
	return f, string(mt), nil
}

// cleanType keeps a well-formed media type and drops parameters other than
// charset; anything unparsable becomes "".
func cleanType(mt string) string {
	t, params, err := mime.ParseMediaType(mt)
	if err != nil || !strings.Contains(t, "/") {
		return ""
	}
	if cs, ok := params["charset"]; ok {
		return mime.FormatMediaType(t, map[string]string{"charset": cs})
	}
	return t
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("store blob: %w", err)
	}
	return nil
}
