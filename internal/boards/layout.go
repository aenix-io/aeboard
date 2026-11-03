package boards

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"

	"github.com/reearth/ygo/crdt"

	"github.com/aenix-io/aeboard/internal/ytext"
)

// Dir is the directory every board lives under, in any backend.
const Dir = "boards"

const (
	metaFile  = "board.json"
	stateFile = "state.jsonl"
	docFile   = "doc.json"
	// legacyStateFile is the binary state boards were first stored in; it
	// is still read, and removed by the board's next write.
	legacyStateFile = "state.bin"
)

// File is one file of a board, by its path from the backend's root; nil
// Data removes the file.
type File struct {
	Path string
	Data []byte
}

// BoardPath is the directory of one board.
func BoardPath(id string) string { return path.Join(Dir, id) }

// StatePath, LegacyStatePath, MetaPath and DocPath name a board's files.
func StatePath(id string) string       { return path.Join(Dir, id, stateFile) }
func LegacyStatePath(id string) string { return path.Join(Dir, id, legacyStateFile) }
func MetaPath(id string) string        { return path.Join(Dir, id, metaFile) }
func DocPath(id string) string         { return path.Join(Dir, id, docFile) }

// Encode renders a board as its files. state.jsonl is the Yjs state as text
// (package ytext: every item with its id, so replicas still merge); it is
// checked to read back to the very same bytes before anything is written.
// doc.json is derived from it and only ever read by people.
func Encode(sn Snapshot) ([]File, error) {
	meta, err := json.MarshalIndent(sn.Meta, "", "  ")
	if err != nil {
		return nil, err
	}
	state, err := ytext.Marshal(sn.State)
	if err != nil {
		return nil, fmt.Errorf("board %s: state as text: %w", sn.Meta.ID, err)
	}
	back, err := ytext.Unmarshal(state)
	if err != nil {
		return nil, fmt.Errorf("board %s: the text state does not read back, not writing it: %w", sn.Meta.ID, err)
	}
	if !bytes.Equal(back, sn.State) {
		return nil, fmt.Errorf("board %s: the text state does not read back to the same bytes, not writing it", sn.Meta.ID)
	}
	doc, err := DocJSON(sn.State)
	if err != nil {
		return nil, fmt.Errorf("board %s: %w", sn.Meta.ID, err)
	}
	return []File{
		{MetaPath(sn.Meta.ID), append(meta, '\n')},
		{StatePath(sn.Meta.ID), state},
		{DocPath(sn.Meta.ID), doc},
		{LegacyStatePath(sn.Meta.ID), nil},
	}, nil
}

// Decode reads a board back from its metadata and state files: the text
// state, or — for a board not written since — the legacy binary one.
func Decode(id string, meta, state, legacy []byte) (Snapshot, error) {
	var m Meta
	if err := json.Unmarshal(meta, &m); err != nil {
		return Snapshot{}, fmt.Errorf("board %s: %s: %w", id, metaFile, err)
	}
	m.ID = id
	m.Tags = normTags(m.Tags)
	sn := Snapshot{Meta: m, State: legacy}
	if len(state) > 0 {
		bin, err := ytext.Unmarshal(state)
		if err != nil {
			return Snapshot{}, fmt.Errorf("board %s: %s: %w", id, stateFile, err)
		}
		sn.State = bin
	}
	return sn, nil
}

// DocJSON renders a Yjs state as indented JSON: BlockSuite keeps a board in
// the "blocks" map (blocks, surface elements, text), so that is what the dump
// holds. Keys are sorted, so two dumps diff line by line.
func DocJSON(state []byte) ([]byte, error) {
	doc := crdt.New()
	if err := crdt.ApplyUpdateV1(doc, state, nil); err != nil {
		return nil, err
	}
	raw, err := doc.GetMap("blocks").ToJSON()
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(map[string]any{"blocks": v}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
