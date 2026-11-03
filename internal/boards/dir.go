package boards

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// DirBackend keeps boards as plain files under a directory — the layout of
// the git backend without the git.
type DirBackend struct{ Root string }

// Load reads every board directory under Root/boards.
func (d DirBackend) Load(context.Context) ([]Snapshot, error) {
	entries, err := os.ReadDir(filepath.Join(d.Root, Dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		id := e.Name()
		meta, err := os.ReadFile(filepath.Join(d.Root, MetaPath(id)))
		if err != nil {
			return nil, err
		}
		state, err := readOptional(filepath.Join(d.Root, StatePath(id)))
		if err != nil {
			return nil, err
		}
		legacy, err := readOptional(filepath.Join(d.Root, LegacyStatePath(id)))
		if err != nil {
			return nil, err
		}
		sn, err := Decode(id, meta, state, legacy)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, nil
}

// Save writes each board's files atomically (file by file).
func (d DirBackend) Save(_ context.Context, boards []Snapshot) error {
	for _, sn := range boards {
		files, err := Encode(sn)
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.Data == nil {
				if err := os.Remove(filepath.Join(d.Root, filepath.FromSlash(f.Path))); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				continue
			}
			if err := writeAtomic(filepath.Join(d.Root, filepath.FromSlash(f.Path)), f.Data); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readOptional(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}
