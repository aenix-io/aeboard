package boards

import (
	"encoding/json"
	"fmt"
	"path"

	"github.com/reearth/ygo/crdt"
)

// Dir is the directory every board lives under, in any backend.
const Dir = "boards"

const (
	metaFile  = "board.json"
	stateFile = "state.bin"
	docFile   = "doc.json"
)

// File is one file of a board, by its path from the backend's root.
type File struct {
	Path string
	Data []byte
}

// BoardPath is the directory of one board.
func BoardPath(id string) string { return path.Join(Dir, id) }

// StatePath, MetaPath and DocPath name a board's files.
func StatePath(id string) string { return path.Join(Dir, id, stateFile) }
func MetaPath(id string) string  { return path.Join(Dir, id, metaFile) }
func DocPath(id string) string   { return path.Join(Dir, id, docFile) }

// Encode renders a board as its files. doc.json is derived from state.bin
// and only ever read by people: the server never loads it.
func Encode(sn Snapshot) ([]File, error) {
	meta, err := json.MarshalIndent(sn.Meta, "", "  ")
	if err != nil {
		return nil, err
	}
	doc, err := DocJSON(sn.State)
	if err != nil {
		return nil, fmt.Errorf("board %s: %w", sn.Meta.ID, err)
	}
	return []File{
		{MetaPath(sn.Meta.ID), append(meta, '\n')},
		{StatePath(sn.Meta.ID), sn.State},
		{DocPath(sn.Meta.ID), doc},
	}, nil
}

// Decode reads a board back from its metadata and state files.
func Decode(id string, meta, state []byte) (Snapshot, error) {
	var m Meta
	if err := json.Unmarshal(meta, &m); err != nil {
		return Snapshot{}, fmt.Errorf("board %s: %s: %w", id, metaFile, err)
	}
	m.ID = id
	m.Tags = normTags(m.Tags)
	return Snapshot{Meta: m, State: state}, nil
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
