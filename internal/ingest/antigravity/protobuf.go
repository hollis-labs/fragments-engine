package antigravity

import (
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf8"
)

var errPayload = errors.New("unsupported or malformed Antigravity text payload")

// These are typed CortexStep fields, not a search for plausible text leaves.
// Decode only user input (19) and planner response (20), leaving tools, images,
// thinking, signatures and opaque metadata out of the imported conversation.
func stepText(stepType int, raw []byte) (string, error) {
	outer, err := fields(raw)
	if err != nil {
		return "", err
	}
	field := 19
	if stepType == 15 {
		field = 20
	}
	parts := outer[field]
	if len(parts) == 0 {
		return "", errPayload
	}
	if len(parts) != 1 || parts[0] == nil {
		return "", errPayload
	}
	inner, err := fields(parts[0])
	if err != nil {
		return "", err
	}
	if stepType == 15 {
		return stringField(inner, 1)
	}
	text, err := stringField(inner, 2)
	if err != nil || strings.TrimSpace(text) != "" {
		return text, err
	}
	var items []string
	for _, item := range inner[3] {
		if item == nil {
			return "", errPayload
		}
		itemFields, err := fields(item)
		if err != nil {
			return "", err
		}
		text, err := stringField(itemFields, 1)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) != "" {
			items = append(items, text)
		}
	}
	if len(items) > 0 {
		return strings.Join(items, "\n"), nil
	}
	return stringField(inner, 1)
}

func stringField(fields map[int][][]byte, field int) (string, error) {
	values := fields[field]
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || values[0] == nil || !validText(values[0]) {
		return "", errPayload
	}
	return string(values[0]), nil
}

func validText(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	for _, b := range raw {
		if b < 32 && b != '\n' && b != '\r' && b != '\t' || b == 127 {
			return false
		}
	}
	return true
}

// fields is a bounded, non-recursive protobuf wire reader. Only length-delimited
// fields are retained as slices of the input; unsupported groups are refused.
func fields(raw []byte) (map[int][][]byte, error) {
	if len(raw) > 10*1024*1024 {
		return nil, errPayload
	}
	out := make(map[int][][]byte)
	for count := 0; len(raw) > 0; count++ {
		if count >= 100000 {
			return nil, errPayload
		}
		tag, n := binary.Uvarint(raw)
		if n <= 0 || tag>>3 == 0 || tag>>3 > (1<<29)-1 {
			return nil, errPayload
		}
		raw = raw[n:]
		var size uint64
		switch tag & 7 {
		case 0:
			_, n = binary.Uvarint(raw)
			if n <= 0 {
				return nil, errPayload
			}
			size = uint64(n)
		case 1:
			size = 8
		case 2:
			size, n = binary.Uvarint(raw)
			if n <= 0 {
				return nil, errPayload
			}
			raw = raw[n:]
		case 5:
			size = 4
		default:
			return nil, errPayload
		}
		if size > uint64(len(raw)) {
			return nil, errPayload
		}
		if tag&7 == 2 {
			field := int(tag >> 3)
			out[field] = append(out[field], raw[:size])
		} else {
			// Preserve presence with the wrong wire type so a typed text field
			// cannot silently become an absent field or fall back to another.
			field := int(tag >> 3)
			out[field] = append(out[field], nil)
		}
		raw = raw[size:]
	}
	return out, nil
}
