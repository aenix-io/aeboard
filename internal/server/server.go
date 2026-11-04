// Package server is aenotes's HTTP surface: the board REST API, the Yjs sync
// WebSocket (ygo), the MCP transport (/mcp) and the embedded SPA.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/reearth/ygo/provider/websocket"

	"github.com/aenix-io/aenotes/internal/blobs"
	"github.com/aenix-io/aenotes/internal/blocks"
	"github.com/aenix-io/aenotes/internal/boards"
	"github.com/aenix-io/aenotes/internal/mcpserver"
)

// Server wires the board store to HTTP.
type Server struct {
	store *boards.Store
	blobs *blobs.Store
	sync  *websocket.Server
	spa   fs.FS
	log   *slog.Logger

	identity      IdentityFunc
	backendStatus BackendStatus
	// auth and access are the hosted mode (nil locally): see EnableOAuth.
	auth    *authManager
	access  *forgeAccess
	editors editors

	mcpInfo MCPInfo
	// newMCP builds the MCP tool set (mcpserver.New; a test swaps it).
	newMCP func(mcpserver.Deps) *mcp.Server

	statusMu   sync.Mutex
	statusSubs map[chan struct{}]struct{}
	// streamsDone ends the status streams on shutdown (see CloseStreams).
	streamsDone chan struct{}
	streamsOnce sync.Once
}

// New returns a server over store and its attachments, serving the SPA from spa.
func New(store *boards.Store, blobStore *blobs.Store, spa fs.FS, log *slog.Logger) *Server {
	s := &Server{store: store, blobs: blobStore, spa: spa, log: log, statusSubs: map[chan struct{}]struct{}{}, streamsDone: make(chan struct{})}
	s.sync = websocket.NewServerWithPersistence(persistence{s: s})
	s.sync.Logger = log
	// A room is a board: refuse the upgrade for an id that names none, so a
	// typo in a URL never conjures an empty board out of thin air.
	s.sync.Authorize = func(r *http.Request) (websocket.ConnectionConfig, bool) {
		id := r.PathValue("room")
		if !boards.ValidID(id) {
			return websocket.ConnectionConfig{}, false
		}
		if _, err := store.Get(id); err != nil {
			return websocket.ConnectionConfig{}, false
		}
		// The hosted gate has already refused a visitor without access; one
		// who may only read gets a socket whose writes ygo drops, and is
		// no editor of the board's commits.
		if accessFrom(r.Context()) != AccessWrite {
			return websocket.ConnectionConfig{ReadOnly: true}, true
		}
		if s.identity != nil {
			if who, ok := s.identity(r); ok {
				s.editors.join(id, who.Login)
			}
		}
		return websocket.ConnectionConfig{}, true
	}
	// The last participant left and ygo has flushed the room's pending
	// updates: write the board now rather than at the next quiet tick.
	s.sync.OnUnloadDocument = func(hookCtx context.Context, room string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), time.Minute)
			defer cancel()
			if err := store.Flush(ctx, room); err != nil {
				s.log.Error("flush board on unload", "board", room, "err", err)
			}
			s.editors.clear(room)
		}()
	}
	store.Subscribe(s.NotifyStatus)
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	if s.auth != nil {
		mux.HandleFunc("GET /auth/login", s.auth.handleLogin)
		mux.HandleFunc("GET /auth/callback", s.auth.handleCallback)
		mux.HandleFunc("POST /auth/logout", s.auth.handleLogout)
		s.registerOAuthServer(mux)
	}
	s.registerMCP(mux)
	// Discovery a client probes for and this server does not offer is a
	// 404, never the SPA's index.html (a 200 an OAuth client would parse).
	for _, p := range []string{"/.well-known/", "/oauth/"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not found")
		})
	}
	mux.HandleFunc("GET /api/v1/me", s.me)
	mux.HandleFunc("GET /api/v1/status", s.getStatus)
	mux.HandleFunc("GET /api/v1/status/events", s.statusEvents)
	mux.HandleFunc("GET /api/v1/boards", s.listBoards)
	mux.HandleFunc("POST /api/v1/boards", s.createBoard)
	mux.HandleFunc("GET /api/v1/boards/{id}", s.getBoard)
	mux.HandleFunc("PATCH /api/v1/boards/{id}", s.patchBoard)
	mux.HandleFunc("DELETE /api/v1/boards/{id}", s.deleteBoard)
	mux.HandleFunc("POST /api/v1/boards/{id}/children", s.addChild)
	mux.HandleFunc("DELETE /api/v1/boards/{id}/children/{child}", s.removeChild)
	mux.HandleFunc("POST /api/v1/folders", s.createFolder)
	mux.HandleFunc("PATCH /api/v1/folders/{id}", s.patchFolder)
	mux.HandleFunc("DELETE /api/v1/folders/{id}", s.deleteFolder)
	mux.HandleFunc("GET /api/v1/boards/{id}/blobs/{key}", s.getBlob)
	mux.HandleFunc("PUT /api/v1/boards/{id}/blobs/{key}", s.putBlob)
	mux.Handle("/api/v1/sync/{room}", s.sync)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", spaHandler(s.spa))
	return s.gate(mux)
}

// Shutdown flushes every room's pending (coalesced) updates to the store.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.sync.Shutdown(ctx)
}

type boardList struct {
	Boards  []docEntry      `json:"boards"`
	Folders []boards.Folder `json:"folders"`
}

// docEntry is a document in the catalog: its metadata and the documents it
// links to (its children in the sidebar).
type docEntry struct {
	boards.Meta
	Links []string `json:"links,omitempty"`
}

func (s *Server) listBoards(w http.ResponseWriter, _ *http.Request) {
	links := s.store.Links()
	docs := []docEntry{}
	for _, m := range s.store.List() {
		// A document whose folder was deleted on another replica shows at
		// the top level.
		m.Folder = s.store.FolderOf(m)
		docs = append(docs, docEntry{Meta: m, Links: links[m.ID]})
	}
	writeJSON(w, http.StatusOK, boardList{Boards: docs, Folders: s.store.Folders()})
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string `json:"name"`
		Parent string `json:"parent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	f, err := s.store.CreateFolder(in.Name, in.Parent)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) patchFolder(w http.ResponseWriter, r *http.Request) {
	var p boards.FolderPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	f, err := s.store.UpdateFolder(r.PathValue("id"), p)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteFolder(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createBoard(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID     string `json:"id"`
		Mode   string `json:"mode"`
		Title  string `json:"title"`
		Folder string `json:"folder"`
		Blank  bool   `json:"blank"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	m, err := s.store.Create(boards.CreateInput{ID: in.ID, Mode: in.Mode, Title: in.Title, Folder: in.Folder, Blank: in.Blank}, s.login(r))
	if errors.Is(err, boards.ErrExists) {
		writeError(w, http.StatusConflict, "a document with this id exists")
		return
	}
	if errors.Is(err, boards.ErrInvalid) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
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
	m, upd, err := s.store.Update(r.PathValue("id"), p, s.login(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if upd != nil {
		// The title lives in the document: send it to whoever has it open.
		s.ApplyRemote(r.Context(), map[string][]byte{m.ID: upd})
	}
	writeJSON(w, http.StatusOK, m)
}

// deleteBoard deletes a trashed document for good (moving to the trash and
// back is a PATCH of "trashed").
func (s *Server) deleteBoard(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.PathValue("id"), s.login(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// addChild puts a document inside another one (AFFiNE's "add linked doc"),
// or moves it among the other one's children: a link line to it goes at the
// end of the parent's body, before or after the link to a sibling, or first.
func (s *Server) addChild(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Doc    string `json:"doc"`
		Before string `json:"before"`
		After  string `json:"after"`
		First  bool   `json:"first"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	parent := r.PathValue("id")
	upd, err := s.store.PlaceChild(parent, in.Doc, blocks.Place{Before: in.Before, After: in.After, First: in.First}, s.login(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.sendLive(r, parent, upd)
	w.WriteHeader(http.StatusNoContent)
}

// removeChild takes a document out of another one: its link lines go from
// the parent's body. A mention inside the text is the author's and stays
// (409).
func (s *Server) removeChild(w http.ResponseWriter, r *http.Request) {
	parent := r.PathValue("id")
	upd, err := s.store.Unlink(parent, r.PathValue("child"), s.login(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.sendLive(r, parent, upd)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sendLive(r *http.Request, id string, upd []byte) {
	if upd != nil {
		s.ApplyRemote(r.Context(), map[string][]byte{id: upd})
	}
}

// Attachments are addressed per board so access can follow the board's
// rights; the bytes themselves are shared by content (see package blobs).
func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.Get(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	f, mt, err := s.blobs.Open(r.PathValue("key"))
	if errors.Is(err, blobs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "blob not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage error")
		return
	}
	defer f.Close()
	if mt == "" {
		mt = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	// Uploaded content is served as data, never as a page of this origin.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Time{}, f)
}

func (s *Server) putBlob(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.Get(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	err := s.blobs.Put(r.PathValue("key"), r.Header.Get("Content-Type"), r.Body)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, blobs.ErrHashMismatch):
		writeError(w, http.StatusBadRequest, "content does not match the key")
	case errors.Is(err, blobs.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "attachment too large")
	default:
		s.log.Error("store blob", "err", err)
		writeError(w, http.StatusInternalServerError, "storage error")
	}
}

// login is the request's actor for commit attribution ("" when anonymous).
func (s *Server) login(r *http.Request) string {
	if s.identity == nil {
		return ""
	}
	id, _ := s.identity(r)
	return id.Login
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, boards.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, boards.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, boards.ErrNotEmpty):
		writeError(w, http.StatusConflict, "the folder is not empty")
		return
	case errors.Is(err, boards.ErrInlineLink):
		writeError(w, http.StatusConflict, "it is mentioned in the text there: remove the mention to take it out")
		return
	case errors.Is(err, boards.ErrNotTrashed):
		writeError(w, http.StatusConflict, err.Error())
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
//
// StoreUpdate only merges into the in-memory store; the store writes the
// board behind (see boards.Store.RunFlusher).
type persistence struct{ s *Server }

func (p persistence) LoadDoc(room string) ([]byte, error) {
	return p.s.store.LoadState(room)
}

func (p persistence) StoreUpdate(room string, update []byte) error {
	changed, err := p.s.store.ApplyUpdate(room, update, p.s.editors.of(room)...)
	if err != nil {
		p.s.log.Error("store update", "board", room, "err", err)
		return err
	}
	if changed {
		p.s.log.Debug("stored update", "board", room, "bytes", len(update))
	}
	return nil
}
