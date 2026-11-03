// Package ytext writes a Yjs V1 update as text and reads it back byte for
// byte.
//
// A Yjs update is a list of structs per client — each with its id (client,
// clock), the neighbours it was inserted between, its parent and its content
// — followed by a delete set. Those ids are what lets two copies of a
// document merge without duplicating anything, so a board stored only as
// readable JSON would lose them. This package keeps them, in a form git can
// diff: one JSON line per struct, then one line per client of deleted ranges.
//
//	{"format":"aeboard-yjs-v1"}
//	{"id":[123,0],"parent":"blocks","key":"seed-0","type":{"ref":1}}
//	{"id":[123,1],"left":[123,0],"string":"Hello"}
//	{"deleted":[123,[[4,2]]]}
//
// Marshal followed by Unmarshal returns the exact input bytes; every value
// keeps its wire type (int and float, -0, undefined, bigint, bytes) and every
// object its key order.
package ytext

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Format is the first line of every document.
const Format = "aeboard-yjs-v1"

// Content refs of the Yjs V1 encoding (the low five bits of a struct's info
// byte), and the info flags.
const (
	refGC      = 0
	refDeleted = 1
	refJSON    = 2
	refBinary  = 3
	refString  = 4
	refEmbed   = 5
	refFormat  = 6
	refType    = 7
	refAny     = 8
	refDoc     = 9
	refSkip    = 10

	flagOrigin      = 0x80
	flagRightOrigin = 0x40
	flagParentSub   = 0x20
)

// Type refs (ContentType) that carry an extra string.
const (
	typeXMLElement = 3
	typeXMLHook    = 5
)

// Marshal renders a V1 update as text.
func Marshal(update []byte) ([]byte, error) {
	r := &reader{b: update}
	var out bytes.Buffer
	out.WriteString(`{"format":"` + Format + `"}` + "\n")

	groups, err := r.uint()
	if err != nil {
		return nil, err
	}
	for range groups {
		n, err := r.uint()
		if err != nil {
			return nil, err
		}
		client, err := r.uint()
		if err != nil {
			return nil, err
		}
		clock, err := r.uint()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			// A group with no structs cannot be written as struct lines.
			return nil, errors.New("ytext: empty struct group")
		}
		for range n {
			length, err := marshalStruct(r, &out, client, clock)
			if err != nil {
				return nil, fmt.Errorf("ytext: struct %d:%d: %w", client, clock, err)
			}
			clock += length
		}
	}

	ds, err := r.uint()
	if err != nil {
		return nil, err
	}
	for range ds {
		client, err := r.uint()
		if err != nil {
			return nil, err
		}
		n, err := r.uint()
		if err != nil {
			return nil, err
		}
		ranges := make([][2]uint64, n)
		for i := range ranges {
			if ranges[i][0], err = r.uint(); err != nil {
				return nil, err
			}
			if ranges[i][1], err = r.uint(); err != nil {
				return nil, err
			}
		}
		line, _ := json.Marshal([]any{client, ranges})
		out.WriteString(`{"deleted":` + string(line) + "}\n")
	}
	if len(r.b) != r.off {
		return nil, fmt.Errorf("ytext: %d trailing bytes", len(r.b)-r.off)
	}
	return out.Bytes(), nil
}

// marshalStruct writes one struct line and returns its length in clock
// units.
func marshalStruct(r *reader, out *bytes.Buffer, client, clock uint64) (uint64, error) {
	info, err := r.byte()
	if err != nil {
		return 0, err
	}
	w := &lineWriter{buf: out}
	w.open()
	w.field("id")
	w.raw(fmt.Sprintf("[%d,%d]", client, clock))

	switch info & 0x1f {
	case refGC, refSkip:
		if info&^0x1f != 0 {
			return 0, fmt.Errorf("flags %#x on a gc/skip struct", info)
		}
		n, err := r.uint()
		if err != nil {
			return 0, err
		}
		w.field(map[byte]string{refGC: "gc", refSkip: "skip"}[info&0x1f])
		w.raw(strconv.FormatUint(n, 10))
		w.close()
		return n, nil
	}

	hasOrigin := info&flagOrigin != 0
	hasRight := info&flagRightOrigin != 0
	if hasOrigin {
		if err := marshalID(r, w, "left"); err != nil {
			return 0, err
		}
	}
	if hasRight {
		if err := marshalID(r, w, "right"); err != nil {
			return 0, err
		}
	}
	if !hasOrigin && !hasRight {
		pi, err := r.uint()
		if err != nil {
			return 0, err
		}
		switch pi {
		case 1:
			name, err := r.str()
			if err != nil {
				return 0, err
			}
			w.field("parent")
			w.str(name)
		case 0:
			if err := marshalID(r, w, "parent"); err != nil {
				return 0, err
			}
		default:
			return 0, fmt.Errorf("parent info %d", pi)
		}
		if info&flagParentSub != 0 {
			key, err := r.str()
			if err != nil {
				return 0, err
			}
			w.field("key")
			w.str(key)
		}
	} else if info&flagParentSub != 0 {
		// The flag is set but the key is not on the wire: Yjs copies it from
		// the origin. Kept so the info byte comes back the same.
		w.field("sub")
		w.raw("true")
	}

	length, err := marshalContent(r, w, info&0x1f)
	if err != nil {
		return 0, err
	}
	w.close()
	return length, nil
}

func marshalID(r *reader, w *lineWriter, name string) error {
	c, err := r.uint()
	if err != nil {
		return err
	}
	k, err := r.uint()
	if err != nil {
		return err
	}
	w.field(name)
	w.raw(fmt.Sprintf("[%d,%d]", c, k))
	return nil
}

func marshalContent(r *reader, w *lineWriter, ref byte) (uint64, error) {
	switch ref {
	case refDeleted:
		n, err := r.uint()
		if err != nil {
			return 0, err
		}
		w.field("deleted")
		w.raw(strconv.FormatUint(n, 10))
		return n, nil
	case refJSON:
		n, err := r.uint()
		if err != nil {
			return 0, err
		}
		vals := make([]string, n)
		for i := range vals {
			if vals[i], err = r.str(); err != nil {
				return 0, err
			}
		}
		b, _ := json.Marshal(vals)
		w.field("json")
		w.raw(string(b))
		return n, nil
	case refBinary:
		b, err := r.bytes()
		if err != nil {
			return 0, err
		}
		w.field("binary")
		w.str(base64.StdEncoding.EncodeToString(b))
		return 1, nil
	case refString:
		s, err := r.str()
		if err != nil {
			return 0, err
		}
		w.field("string")
		w.str(s)
		return utf16Len(s), nil
	case refEmbed:
		s, err := r.str()
		if err != nil {
			return 0, err
		}
		w.field("embed")
		w.str(s)
		return 1, nil
	case refFormat:
		k, err := r.str()
		if err != nil {
			return 0, err
		}
		v, err := r.str()
		if err != nil {
			return 0, err
		}
		b, _ := json.Marshal([]string{k, v})
		w.field("format")
		w.raw(string(b))
		return 1, nil
	case refType, refAny, refDoc:
		return marshalNested(r, w, ref)
	}
	return 0, fmt.Errorf("unknown content ref %d", ref)
}

// marshalNested writes the content kinds that carry a type or lib0 values.
func marshalNested(r *reader, w *lineWriter, ref byte) (uint64, error) {
	switch ref {
	case refType:
		t, err := r.uint()
		if err != nil {
			return 0, err
		}
		w.field("type")
		if t == typeXMLElement || t == typeXMLHook {
			name, err := r.str()
			if err != nil {
				return 0, err
			}
			nb, _ := json.Marshal(name)
			w.raw(fmt.Sprintf(`{"ref":%d,"name":%s}`, t, nb))
		} else {
			w.raw(fmt.Sprintf(`{"ref":%d}`, t))
		}
		return 1, nil
	case refAny:
		n, err := r.uint()
		if err != nil {
			return 0, err
		}
		var b bytes.Buffer
		b.WriteByte('[')
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := marshalAny(r, &b); err != nil {
				return 0, err
			}
		}
		b.WriteByte(']')
		w.field("any")
		w.raw(b.String())
		return n, nil
	case refDoc:
		guid, err := r.str()
		if err != nil {
			return 0, err
		}
		var b bytes.Buffer
		if err := marshalAny(r, &b); err != nil {
			return 0, err
		}
		gb, _ := json.Marshal(guid)
		w.field("doc")
		w.raw(fmt.Sprintf(`{"guid":%s,"opts":%s}`, gb, b.String()))
		return 1, nil
	}
	return 0, fmt.Errorf("unknown content ref %d", ref)
}

// lib0 "any" tags.
const (
	anyUndefined = 127
	anyNull      = 126
	anyInt       = 125
	anyFloat32   = 124
	anyFloat64   = 123
	anyBigInt    = 122
	anyFalse     = 121
	anyTrue      = 120
	anyString    = 119
	anyObject    = 118
	anyArray     = 117
	anyBytes     = 116
)

// tagKey marks a JSON object that stands for a value plain JSON cannot hold.
const tagKey = "$y"

// marshalAny renders one lib0 value. Plain JSON where it is unambiguous: an
// integer is a number without a point, a float64 a number with one (or an
// exponent); everything else is tagged {"$y":kind,"v":...}.
func marshalAny(r *reader, b *bytes.Buffer) error {
	tag, err := r.byte()
	if err != nil {
		return err
	}
	switch tag {
	case anyUndefined:
		b.WriteString(`{"$y":"undefined"}`)
	case anyNull:
		b.WriteString("null")
	case anyTrue:
		b.WriteString("true")
	case anyFalse:
		b.WriteString("false")
	case anyInt:
		neg, mag, err := r.varint()
		if err != nil {
			return err
		}
		if neg && mag == 0 {
			b.WriteString(`{"$y":"int","v":"-0"}`)
			return nil
		}
		if neg {
			b.WriteByte('-')
		}
		b.WriteString(strconv.FormatUint(mag, 10))
	case anyFloat32:
		v, err := r.fixed(4)
		if err != nil {
			return err
		}
		fmt.Fprintf(b, `{"$y":"f32","v":"%08x"}`, uint32(v)) //nolint:gosec // v holds exactly 4 bytes
	case anyFloat64:
		bits, err := r.fixed(8)
		if err != nil {
			return err
		}
		b.WriteString(formatFloat64(math.Float64frombits(bits), bits))
	case anyBigInt:
		v, err := r.fixed(8)
		if err != nil {
			return err
		}
		fmt.Fprintf(b, `{"$y":"bigint","v":"%d"}`, int64(v)) //nolint:gosec // two's-complement reinterpretation, as on the wire
	case anyString:
		s, err := r.str()
		if err != nil {
			return err
		}
		sb, _ := json.Marshal(s)
		b.Write(sb)
	case anyBytes:
		raw, err := r.bytes()
		if err != nil {
			return err
		}
		fmt.Fprintf(b, `{"$y":"bytes","v":%q}`, base64.StdEncoding.EncodeToString(raw))
	case anyArray:
		n, err := r.uint()
		if err != nil {
			return err
		}
		b.WriteByte('[')
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := marshalAny(r, b); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case anyObject:
		return marshalObject(r, b)
	default:
		return fmt.Errorf("unknown any tag %d", tag)
	}
	return nil
}

// marshalObject writes an object in wire order. An object that itself has a
// "$y" key is written as {"$y":"object","v":[[k,v],...]} so it cannot be
// mistaken for a tag.
func marshalObject(r *reader, b *bytes.Buffer) error {
	n, err := r.uint()
	if err != nil {
		return err
	}
	keys := make([]string, n)
	vals := make([]string, n)
	tagged := false
	for i := range n {
		if keys[i], err = r.str(); err != nil {
			return err
		}
		if keys[i] == tagKey {
			tagged = true
		}
		var vb bytes.Buffer
		if err := marshalAny(r, &vb); err != nil {
			return err
		}
		vals[i] = vb.String()
	}
	if tagged {
		b.WriteString(`{"$y":"object","v":[`)
		for i := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(keys[i])
			b.WriteString("[" + string(kb) + "," + vals[i] + "]")
		}
		b.WriteString("]}")
		return nil
	}
	b.WriteByte('{')
	for i := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(keys[i])
		b.Write(kb)
		b.WriteByte(':')
		b.WriteString(vals[i])
	}
	b.WriteByte('}')
	return nil
}

// formatFloat64 renders a float so it reads back as a float, not an int:
// always with a point or an exponent; -0, NaN and the infinities (and any
// NaN payload) are tagged with their bits.
func formatFloat64(v float64, bits uint64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || (v == 0 && math.Signbit(v)) {
		return fmt.Sprintf(`{"$y":"f64","v":"%016x"}`, bits)
	}
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// Unmarshal reads text written by Marshal back into the V1 update.
func Unmarshal(text []byte) ([]byte, error) {
	sc := bufio.NewScanner(bytes.NewReader(text))
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	if !sc.Scan() {
		return nil, errors.New("ytext: empty document")
	}
	var head struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(sc.Bytes(), &head); err != nil || head.Format != Format {
		return nil, fmt.Errorf("ytext: not an %s document", Format)
	}

	type group struct {
		client, clock uint64
		structs       [][]byte
	}
	var groups []*group
	var deletes [][]byte
	var nextClock uint64
	lineNo := 1
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var probe struct {
			ID      *[2]uint64      `json:"id"`
			Deleted json.RawMessage `json:"deleted"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return nil, fmt.Errorf("ytext: line %d: %w", lineNo, err)
		}
		if probe.ID == nil {
			if probe.Deleted == nil {
				return nil, fmt.Errorf("ytext: line %d: neither a struct nor a delete set", lineNo)
			}
			enc, err := unmarshalDeletes(probe.Deleted)
			if err != nil {
				return nil, fmt.Errorf("ytext: line %d: %w", lineNo, err)
			}
			deletes = append(deletes, enc)
			continue
		}
		if len(deletes) > 0 {
			return nil, fmt.Errorf("ytext: line %d: struct after the delete set", lineNo)
		}
		client, clock := probe.ID[0], probe.ID[1]
		if len(groups) == 0 || groups[len(groups)-1].client != client {
			groups = append(groups, &group{client: client, clock: clock})
		} else if clock != nextClock {
			return nil, fmt.Errorf("ytext: line %d: clock %d, want %d", lineNo, clock, nextClock)
		}
		enc, length, err := unmarshalStruct(line)
		if err != nil {
			return nil, fmt.Errorf("ytext: line %d: %w", lineNo, err)
		}
		g := groups[len(groups)-1]
		g.structs = append(g.structs, enc)
		nextClock = clock + length
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	w := &writer{}
	w.uint(uint64(len(groups)))
	for _, g := range groups {
		w.uint(uint64(len(g.structs)))
		w.uint(g.client)
		w.uint(g.clock)
		for _, s := range g.structs {
			w.buf = append(w.buf, s...)
		}
	}
	w.uint(uint64(len(deletes)))
	for _, d := range deletes {
		w.buf = append(w.buf, d...)
	}
	return w.buf, nil
}

func unmarshalDeletes(raw json.RawMessage) ([]byte, error) {
	var d []json.RawMessage
	if err := json.Unmarshal(raw, &d); err != nil || len(d) != 2 {
		return nil, errors.New("delete set line must be [client,[[clock,len],...]]")
	}
	var client uint64
	var ranges [][2]uint64
	if err := json.Unmarshal(d[0], &client); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(d[1], &ranges); err != nil {
		return nil, err
	}
	w := &writer{}
	w.uint(client)
	w.uint(uint64(len(ranges)))
	for _, r := range ranges {
		w.uint(r[0])
		w.uint(r[1])
	}
	return w.buf, nil
}

// structLine is a struct line, decoded.
type structLine struct {
	GC      *uint64         `json:"gc"`
	Skip    *uint64         `json:"skip"`
	Left    *[2]uint64      `json:"left"`
	Right   *[2]uint64      `json:"right"`
	Parent  json.RawMessage `json:"parent"`
	Key     *string         `json:"key"`
	Sub     bool            `json:"sub"`
	Deleted *uint64         `json:"deleted"`
	JSON    []string        `json:"json"`
	Binary  *string         `json:"binary"`
	String  *string         `json:"string"`
	Embed   *string         `json:"embed"`
	Format  []string        `json:"format"`
	Type    *struct {
		Ref  uint64  `json:"ref"`
		Name *string `json:"name"`
	} `json:"type"`
	Any json.RawMessage `json:"any"`
	Doc *struct {
		GUID string          `json:"guid"`
		Opts json.RawMessage `json:"opts"`
	} `json:"doc"`
}

func unmarshalStruct(line []byte) ([]byte, uint64, error) {
	var s structLine
	if err := json.Unmarshal(line, &s); err != nil {
		return nil, 0, err
	}
	w := &writer{}
	if s.GC != nil || s.Skip != nil {
		ref, n := byte(refGC), s.GC
		if s.Skip != nil {
			ref, n = refSkip, s.Skip
		}
		w.byte(ref)
		w.uint(*n)
		return w.buf, *n, nil
	}

	content := &writer{}
	ref, length, err := unmarshalContent(&s, content)
	if err != nil {
		return nil, 0, err
	}
	info := ref
	if s.Left != nil {
		info |= flagOrigin
	}
	if s.Right != nil {
		info |= flagRightOrigin
	}
	if s.Key != nil || s.Sub {
		info |= flagParentSub
	}
	w.byte(info)
	if s.Left != nil {
		w.uint(s.Left[0])
		w.uint(s.Left[1])
	}
	if s.Right != nil {
		w.uint(s.Right[0])
		w.uint(s.Right[1])
	}
	if s.Left == nil && s.Right == nil {
		if len(s.Parent) == 0 {
			return nil, 0, errors.New("a struct without neighbours needs a parent")
		}
		var name string
		var id [2]uint64
		switch {
		case json.Unmarshal(s.Parent, &name) == nil:
			w.uint(1)
			w.str(name)
		case json.Unmarshal(s.Parent, &id) == nil:
			w.uint(0)
			w.uint(id[0])
			w.uint(id[1])
		default:
			return nil, 0, errors.New("parent must be a name or [client,clock]")
		}
		if s.Key != nil {
			w.str(*s.Key)
		}
	}
	w.buf = append(w.buf, content.buf...)
	return w.buf, length, nil
}

func unmarshalContent(s *structLine, w *writer) (byte, uint64, error) {
	switch {
	case s.Deleted != nil:
		w.uint(*s.Deleted)
		return refDeleted, *s.Deleted, nil
	case s.JSON != nil:
		w.uint(uint64(len(s.JSON)))
		for _, v := range s.JSON {
			w.str(v)
		}
		return refJSON, uint64(len(s.JSON)), nil
	case s.Binary != nil:
		b, err := base64.StdEncoding.DecodeString(*s.Binary)
		if err != nil {
			return 0, 0, err
		}
		w.bytes(b)
		return refBinary, 1, nil
	case s.String != nil:
		w.str(*s.String)
		return refString, utf16Len(*s.String), nil
	case s.Embed != nil:
		w.str(*s.Embed)
		return refEmbed, 1, nil
	case s.Format != nil:
		if len(s.Format) != 2 {
			return 0, 0, errors.New("format must be [key,value]")
		}
		w.str(s.Format[0])
		w.str(s.Format[1])
		return refFormat, 1, nil
	case s.Type != nil:
		w.uint(s.Type.Ref)
		if s.Type.Ref == typeXMLElement || s.Type.Ref == typeXMLHook {
			if s.Type.Name == nil {
				return 0, 0, errors.New("xml type needs a name")
			}
			w.str(*s.Type.Name)
		}
		return refType, 1, nil
	case s.Any != nil:
		dec := json.NewDecoder(bytes.NewReader(s.Any))
		dec.UseNumber()
		if t, err := dec.Token(); err != nil || t != json.Delim('[') {
			return 0, 0, errors.New("any must be an array")
		}
		var vals [][]byte
		for dec.More() {
			v := &writer{}
			if err := unmarshalAny(dec, v); err != nil {
				return 0, 0, err
			}
			vals = append(vals, v.buf)
		}
		w.uint(uint64(len(vals)))
		for _, v := range vals {
			w.buf = append(w.buf, v...)
		}
		return refAny, uint64(len(vals)), nil
	case s.Doc != nil:
		w.str(s.Doc.GUID)
		dec := json.NewDecoder(bytes.NewReader(s.Doc.Opts))
		dec.UseNumber()
		if err := unmarshalAny(dec, w); err != nil {
			return 0, 0, err
		}
		return refDoc, 1, nil
	}
	return 0, 0, errors.New("struct has no content")
}

// unmarshalAny reads one value from the token stream (keeping object key
// order) and writes its lib0 encoding.
func unmarshalAny(dec *json.Decoder, w *writer) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := t.(type) {
	case nil:
		w.byte(anyNull)
	case bool:
		if v {
			w.byte(anyTrue)
		} else {
			w.byte(anyFalse)
		}
	case string:
		w.byte(anyString)
		w.str(v)
	case json.Number:
		return writeNumber(w, string(v))
	case json.Delim:
		switch v {
		case '[':
			var items []*writer
			for dec.More() {
				iw := &writer{}
				if err := unmarshalAny(dec, iw); err != nil {
					return err
				}
				items = append(items, iw)
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			w.byte(anyArray)
			w.uint(uint64(len(items)))
			for _, iw := range items {
				w.buf = append(w.buf, iw.buf...)
			}
		case '{':
			return unmarshalObject(dec, w)
		default:
			return fmt.Errorf("unexpected %v", v)
		}
	}
	return nil
}

func writeNumber(w *writer, s string) error {
	if strings.ContainsAny(s, ".eE") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		w.byte(anyFloat64)
		w.fixed(math.Float64bits(f), 8)
		return nil
	}
	neg := strings.HasPrefix(s, "-")
	mag, err := strconv.ParseUint(strings.TrimPrefix(s, "-"), 10, 64)
	if err != nil {
		return err
	}
	w.byte(anyInt)
	w.varint(neg, mag)
	return nil
}

func unmarshalObject(dec *json.Decoder, w *writer) error {
	var keys []string
	var vals []*writer
	var tag string
	var tagVal json.RawMessage
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		k := kt.(string)
		if k == tagKey || (tag != "" && k == "v") {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return err
			}
			if k == tagKey {
				if err := json.Unmarshal(raw, &tag); err != nil {
					return err
				}
			} else {
				tagVal = raw
			}
			continue
		}
		if tag != "" {
			return fmt.Errorf("tagged value %q has extra key %q", tag, k)
		}
		vw := &writer{}
		if err := unmarshalAny(dec, vw); err != nil {
			return err
		}
		keys = append(keys, k)
		vals = append(vals, vw)
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if tag != "" {
		if len(keys) > 0 {
			return fmt.Errorf("tagged value %q has extra keys", tag)
		}
		return writeTagged(w, tag, tagVal)
	}
	w.byte(anyObject)
	w.uint(uint64(len(keys)))
	for i, k := range keys {
		w.str(k)
		w.buf = append(w.buf, vals[i].buf...)
	}
	return nil
}

func writeTagged(w *writer, tag string, raw json.RawMessage) error {
	var s string
	if tag != "undefined" && tag != "object" {
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("tagged %s: %w", tag, err)
		}
	}
	switch tag {
	case "undefined":
		w.byte(anyUndefined)
	case "int":
		if s != "-0" {
			return fmt.Errorf("tagged int %q", s)
		}
		w.byte(anyInt)
		w.varint(true, 0)
	case "f32":
		v, err := strconv.ParseUint(s, 16, 32)
		if err != nil {
			return err
		}
		w.byte(anyFloat32)
		w.fixed(v, 4)
	case "f64":
		v, err := strconv.ParseUint(s, 16, 64)
		if err != nil {
			return err
		}
		w.byte(anyFloat64)
		w.fixed(v, 8)
	case "bigint":
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		w.byte(anyBigInt)
		w.fixed(uint64(v), 8) //nolint:gosec // two's-complement reinterpretation, as on the wire
	case "bytes":
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return err
		}
		w.byte(anyBytes)
		w.bytes(b)
	case "object":
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if t, err := dec.Token(); err != nil || t != json.Delim('[') {
			return errors.New("tagged object must hold [[key,value],...]")
		}
		var keys []string
		var vals []*writer
		for dec.More() {
			if t, err := dec.Token(); err != nil || t != json.Delim('[') {
				return errors.New("tagged object entry must be [key,value]")
			}
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			k, ok := kt.(string)
			if !ok {
				return errors.New("tagged object key must be a string")
			}
			vw := &writer{}
			if err := unmarshalAny(dec, vw); err != nil {
				return err
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			keys = append(keys, k)
			vals = append(vals, vw)
		}
		w.byte(anyObject)
		w.uint(uint64(len(keys)))
		for i, k := range keys {
			w.str(k)
			w.buf = append(w.buf, vals[i].buf...)
		}
	default:
		return fmt.Errorf("unknown tag %q", tag)
	}
	return nil
}

// utf16Len is a string's length as Yjs counts it (UTF-16 code units).
func utf16Len(s string) uint64 {
	var n uint64
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// reader decodes the lib0 primitives of the V1 encoding.
type reader struct {
	b   []byte
	off int
}

var errShort = errors.New("ytext: unexpected end of update")

func (r *reader) byte() (byte, error) {
	if r.off >= len(r.b) {
		return 0, errShort
	}
	c := r.b[r.off]
	r.off++
	return c, nil
}

func (r *reader) uint() (uint64, error) {
	var v uint64
	for shift := uint(0); ; shift += 7 {
		c, err := r.byte()
		if err != nil {
			return 0, err
		}
		if shift >= 64 {
			return 0, errors.New("ytext: varuint overflow")
		}
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v, nil
		}
	}
}

// varint reads lib0's signed varint: the first byte holds a continuation
// bit, the sign bit and six bits; the sign is kept apart so -0 survives.
func (r *reader) varint() (neg bool, mag uint64, err error) {
	c, err := r.byte()
	if err != nil {
		return false, 0, err
	}
	mag = uint64(c & 0x3f)
	neg = c&0x40 != 0
	if c&0x80 == 0 {
		return neg, mag, nil
	}
	for shift := uint(6); ; shift += 7 {
		c, err := r.byte()
		if err != nil {
			return false, 0, err
		}
		if shift >= 64 {
			return false, 0, errors.New("ytext: varint overflow")
		}
		mag |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return neg, mag, nil
		}
	}
}

func (r *reader) bytes() ([]byte, error) {
	n, err := r.uint()
	if err != nil {
		return nil, err
	}
	rest := len(r.b) - r.off
	if n > uint64(rest) { //nolint:gosec // rest is never negative
		return nil, errShort
	}
	end := r.off + int(n) //nolint:gosec // n <= rest, so it fits an int
	b := r.b[r.off:end]
	r.off = end
	return b, nil
}

func (r *reader) str() (string, error) {
	b, err := r.bytes()
	return string(b), err
}

// fixed reads an n-byte big-endian value (lib0 floats and bigints).
func (r *reader) fixed(n int) (uint64, error) {
	if len(r.b)-r.off < n {
		return 0, errShort
	}
	var buf [8]byte
	copy(buf[8-n:], r.b[r.off:r.off+n])
	r.off += n
	return binary.BigEndian.Uint64(buf[:]), nil
}

// writer encodes the same primitives.
type writer struct{ buf []byte }

func (w *writer) byte(c byte) { w.buf = append(w.buf, c) }

func (w *writer) uint(v uint64) {
	for v >= 0x80 {
		w.buf = append(w.buf, byte(v)|0x80)
		v >>= 7
	}
	w.buf = append(w.buf, byte(v))
}

func (w *writer) varint(neg bool, mag uint64) {
	first := byte(mag & 0x3f)
	if neg {
		first |= 0x40
	}
	mag >>= 6
	if mag > 0 {
		first |= 0x80
	}
	w.buf = append(w.buf, first)
	for mag > 0 {
		c := byte(mag & 0x7f)
		mag >>= 7
		if mag > 0 {
			c |= 0x80
		}
		w.buf = append(w.buf, c)
	}
}

func (w *writer) bytes(b []byte) {
	w.uint(uint64(len(b)))
	w.buf = append(w.buf, b...)
}

func (w *writer) str(s string) { w.bytes([]byte(s)) }

func (w *writer) fixed(v uint64, n int) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	w.buf = append(w.buf, buf[8-n:]...)
}

// lineWriter builds one JSON object line in a fixed field order.
type lineWriter struct {
	buf   *bytes.Buffer
	first bool
}

func (l *lineWriter) open() { l.buf.WriteByte('{'); l.first = true }

func (l *lineWriter) field(name string) {
	if !l.first {
		l.buf.WriteByte(',')
	}
	l.first = false
	l.buf.WriteString(`"` + name + `":`)
}

func (l *lineWriter) raw(s string) { l.buf.WriteString(s) }

func (l *lineWriter) str(s string) {
	b, _ := json.Marshal(s)
	l.buf.Write(b)
}

func (l *lineWriter) close() { l.buf.WriteString("}\n") }
