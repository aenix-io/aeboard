// Package server is aeboard's HTTP surface: the board REST API, the Yjs sync
// WebSocket (ygo) and the embedded SPA.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/reearth/ygo/provider/websocket"

	"github.com/aenix-io/aeboard/internal/boards"
)

// Server wires the board store to HTTP.
type Server struct {
	store *boards.Store
	sync  *websocket.Server
	spa   fs.FS
	log   *slog.Logger
}

// New returns a server over store, serving the SPA from spa.
func New(store *boards.Store, spa fs.FS, log *slog.Logger) *Server {
	s := &Server{store: store, spa: spa, log: log}
	s.sync = websocket.NewServerWithPersistence(persistence{store: store, log: log})
	s.sync.Logger = log
	// A room is a board: refuse the upgrade for an id that names none, so a
	// typo in a URL never conjures an empty board out of thin air.
	s.sync.Authorize = func(r *http.Request) (websocket.ConnectionConfig, bool) {
		id := r.PathValue("room")
		if !boards.ValidID(id) {
			return websocket.ConnectionConfig{}, false
		}
		_, err := store.Get(id)
		return websocket.ConnectionConfig{}, err == nil
	}
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/boards", s.listBoards)
	mux.HandleFunc("POST /api/v1/boards", s.createBoard)
	mux.HandleFunc("GET /api/v1/boards/{id}", s.getBoard)
	mux.HandleFunc("PATCH /api/v1/boards/{id}", s.patchBoard)
	mux.Handle("/api/v1/sync/{room}", s.sync)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", spaHandler(s.spa))
	return mux
}

// Shutdown flushes every room's pending (coalesced) updates to the store.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.sync.Shutdown(ctx)
}

type boardList struct {
	Boards []boards.Meta `json:"boards"`
	Tags   []string      `json:"tags"`
}

func (s *Server) listBoards(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, boardList{Boards: s.store.List(), Tags: s.store.Tags()})
}

func (s *Server) createBoard(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string   `json:"title"`
		Tags  []string `json:"tags"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	m, err := s.store.Create(in.Title, in.Tags)
	if err != nil {
		s.log.Error("create board", "err", err)
		writeError(w, http.StatusInternalServerError, "could not create the board")
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) getBoard(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) patchBoard(w http.ResponseWriter, r *http.Request) {
	var p boards.Patch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	m, err := s.store.Update(r.PathValue("id"), p)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, boards.ErrNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "storage error")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// spaHandler serves the built SPA; any path that is not a file falls back to
// index.html so client-side routes (/b/<id>) load the app.
func spaHandler(dist fs.FS) http.Handler {
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" {
			if st, err := fs.Stat(dist, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, dist, "index.html")
	})
}

// persistence adapts the board store to ygo. ygo already coalesces a burst
// of updates into one StoreUpdate per debounce window (2s, capped at 10s)
// and flushes on room unload and on Shutdown.
type persistence struct {
	store *boards.Store
	log   *slog.Logger
}

func (p persistence) LoadDoc(room string) ([]byte, error) {
	return p.store.LoadState(room)
}

func (p persistence) StoreUpdate(room string, update []byte) error {
	changed, err := p.store.ApplyUpdate(room, update)
	if err != nil {
		p.log.Error("store update", "board", room, "err", err)
		return err
	}
	if changed {
		p.log.Debug("stored update", "board", room, "bytes", len(update))
	}
	return nil
}
