package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/reearth/ygo/crdt"
)

// SyncStatus is how far the server's boards are from their backing store,
// as the header badge shows it (the same badge aeman shows for its queue).
type SyncStatus struct {
	// Backend is "dir" (plain files) or "git".
	Backend string `json:"backend"`
	// Remote is where the git backend pushes (empty for dir, or a local repo).
	Remote string `json:"remote,omitempty"`
	// Pending counts boards with changes not yet committed.
	Pending int `json:"pending"`
	// Unpushed counts commits not yet on the remote.
	Unpushed int `json:"unpushed"`
	// LastSync is when the remote last confirmed a push or a fetch.
	LastSync *time.Time `json:"lastSync,omitempty"`
	// UnpushedSince is how long the oldest unpushed change has waited.
	UnpushedSince *time.Time `json:"unpushedSince,omitempty"`
	// Error is the last sync failure, cleared by the next success.
	Error string `json:"error,omitempty"`
	// Catalog grows with every change to the document list (documents,
	// folders, order, titles): a browser that sees it move fetches the list
	// again, so the structure updates for everyone at once.
	Catalog uint64 `json:"catalog"`
}

// BackendStatus reports the backend's side of SyncStatus (everything but
// Pending, which the board store knows).
type BackendStatus func() SyncStatus

// SetBackendStatus wires the backend's status into /api/v1/status.
func (s *Server) SetBackendStatus(f BackendStatus) { s.backendStatus = f }

// Status is the current sync status.
func (s *Server) Status() SyncStatus {
	st := SyncStatus{Backend: "dir"}
	if s.backendStatus != nil {
		st = s.backendStatus()
	}
	st.Pending = s.store.Pending()
	st.Catalog = s.store.CatalogVersion()
	return st
}

// NotifyStatus tells /api/v1/status/events listeners the status may have
// changed (the backend calls it after a push, a fetch or a failure).
func (s *Server) NotifyStatus() {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	for ch := range s.statusSubs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Server) getStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Status())
}

// statusEvents streams the status as server-sent events: one at once, then
// one per change (at most every 250ms), plus a comment every 25s so proxies
// keep the stream open.
func (s *Server) statusEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := make(chan struct{}, 1)
	s.statusMu.Lock()
	s.statusSubs[ch] = struct{}{}
	s.statusMu.Unlock()
	defer func() {
		s.statusMu.Lock()
		delete(s.statusSubs, ch)
		s.statusMu.Unlock()
	}()

	var last []byte
	send := func() bool {
		b, _ := json.Marshal(s.Status())
		if string(b) == string(last) {
			return true
		}
		last = b
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if !send() {
		return
	}
	keep := time.NewTicker(25 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.streamsDone:
			// The server is stopping: end the stream so http.Server.Shutdown
			// does not wait on it (EventSource reconnects to the next one).
			return
		case <-ch:
			time.Sleep(250 * time.Millisecond) // fold a burst into one event
			if !send() {
				return
			}
		case <-keep.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// CloseStreams ends every open status stream. Register it with
// http.Server.RegisterOnShutdown: Shutdown waits for active requests, and a
// stream only ends when its client goes.
func (s *Server) CloseStreams() {
	s.streamsOnce.Do(func() { close(s.streamsDone) })
}

// ApplyRemote brings boards changed elsewhere (fetched from git) into the
// rooms people have open right now; boards nobody has open just load the
// merged state next time. updates maps board id to the Yjs update the
// store's MergeRemote returned.
func (s *Server) ApplyRemote(ctx context.Context, updates map[string][]byte) {
	for id, upd := range updates {
		doc := s.sync.GetDoc(id)
		if doc == nil {
			continue
		}
		if err := crdt.ApplyUpdateV1(doc, upd, remoteOrigin{}); err != nil {
			s.log.Error("apply remote update", "board", id, "err", err)
			continue
		}
		if err := s.sync.BroadcastUpdate(ctx, id, upd); err != nil {
			s.log.Error("broadcast remote update", "board", id, "err", err)
		}
	}
}

type remoteOrigin struct{}

// editors tracks who is in each room, so a board's commit names the people
// whose changes it carries.
type editors struct {
	mu    sync.Mutex
	rooms map[string]map[string]bool
}

func (e *editors) join(room, login string) {
	if login == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.rooms == nil {
		e.rooms = map[string]map[string]bool{}
	}
	if e.rooms[room] == nil {
		e.rooms[room] = map[string]bool{}
	}
	e.rooms[room][login] = true
}

func (e *editors) of(room string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.rooms[room]))
	for l := range e.rooms[room] {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func (e *editors) clear(room string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.rooms, room)
}
