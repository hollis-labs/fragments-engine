package antigravity

import (
	"encoding/binary"
	"time"
)

// A lenient protobuf wire reader for the structural facts around a step
// (timestamps, usage counters, ids). It is deliberately separate from fields():
// that reader is strict because it feeds the typed text extraction, where a
// wrong wire type must be an error. Here a malformed or unexpected field only
// means the fact is absent; it never aborts a conversation.

type pbField struct {
	num  int
	wire int
	v    uint64 // varint, fixed32 or fixed64 value
	b    []byte // length-delimited bytes
}

type pbMsg []pbField

// decodeMsg parses raw as one message. ok is false when it is not a well-formed
// message, in which case the caller treats every fact in it as absent.
func decodeMsg(raw []byte) (pbMsg, bool) {
	var out pbMsg
	for len(raw) > 0 {
		tag, n := binary.Uvarint(raw)
		if n <= 0 || tag>>3 == 0 || tag>>3 > (1<<29)-1 {
			return nil, false
		}
		raw = raw[n:]
		f := pbField{num: int(tag >> 3), wire: int(tag & 7)}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(raw)
			if n <= 0 {
				return nil, false
			}
			f.v, raw = v, raw[n:]
		case 1:
			if len(raw) < 8 {
				return nil, false
			}
			f.v, raw = binary.LittleEndian.Uint64(raw), raw[8:]
		case 5:
			if len(raw) < 4 {
				return nil, false
			}
			f.v, raw = uint64(binary.LittleEndian.Uint32(raw)), raw[4:]
		case 2:
			size, n := binary.Uvarint(raw)
			if n <= 0 || size > uint64(len(raw)-n) {
				return nil, false
			}
			f.b, raw = raw[n:n+int(size)], raw[n+int(size):]
		default:
			return nil, false
		}
		out = append(out, f)
	}
	return out, true
}

func (m pbMsg) first(num int) (pbField, bool) {
	for _, f := range m {
		if f.num == num {
			return f, true
		}
	}
	return pbField{}, false
}

func (m pbMsg) all(num int) []pbField {
	var out []pbField
	for _, f := range m {
		if f.num == num {
			out = append(out, f)
		}
	}
	return out
}

// sub descends through nested messages along path and returns the last one.
func (m pbMsg) sub(path ...int) (pbMsg, bool) {
	cur := m
	for _, num := range path {
		f, ok := cur.first(num)
		if !ok || f.wire != 2 {
			return nil, false
		}
		next, ok := decodeMsg(f.b)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func (m pbMsg) varint(num int) (uint64, bool) {
	f, ok := m.first(num)
	if !ok || f.wire != 0 {
		return 0, false
	}
	return f.v, true
}

// text returns a length-delimited field as a string when it is valid text.
func (m pbMsg) text(num int) (string, bool) {
	f, ok := m.first(num)
	if !ok || f.wire != 2 || !validText(f.b) {
		return "", false
	}
	return string(f.b), true
}

// stamp reads a {seconds, nanos} timestamp message at field num. Values outside
// 2000..2100 are treated as absent rather than trusted.
func (m pbMsg) stamp(num int) time.Time {
	ts, ok := m.sub(num)
	if !ok {
		return time.Time{}
	}
	sec, ok := ts.varint(1)
	if !ok || sec < 946684800 || sec > 4102444800 {
		return time.Time{}
	}
	nanos, _ := ts.varint(2)
	if nanos > 999999999 {
		nanos = 0
	}
	return time.Unix(int64(sec), int64(nanos)).UTC()
}
