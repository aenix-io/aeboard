// Command aeboard — compatibility spike: BlockSuite edgeless over a ygo
// y-websocket server with a naive file persistence.
package main

import (
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/reearth/ygo/crdt"
	"github.com/reearth/ygo/provider/websocket"

	"github.com/aenix-io/aeboard/web"
)

type filePersistence struct {
	dir string
	mu  sync.Mutex
}

func (p *filePersistence) path(room string) string { return filepath.Join(p.dir, room+".bin") }

func (p *filePersistence) LoadDoc(room string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b, err := os.ReadFile(p.path(room))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	slog.Info("load", "room", room, "bytes", len(b))
	return b, err
}

func (p *filePersistence) StoreUpdate(room string, update []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur, err := os.ReadFile(p.path(room))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	merged := update
	if len(cur) > 0 {
		if merged, err = crdt.MergeUpdatesV1(cur, update); err != nil {
			return err
		}
	}
	slog.Info("store", "room", room, "update", len(update), "state", len(merged))
	tmp := p.path(room) + ".tmp"
	if err := os.WriteFile(tmp, merged, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path(room))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8766", "listen address")
	data := flag.String("data", "./data", "data directory")
	flag.Parse()
	if err := os.MkdirAll(*data, 0o700); err != nil {
		panic(err)
	}
	srv := websocket.NewServerWithPersistence(&filePersistence{dir: *data})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/sync/{room}", srv)
	dist, err := web.DistFS()
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServerFS(dist))
	slog.Info("listening", "addr", *addr)
	panic(http.ListenAndServe(*addr, mux))
}
