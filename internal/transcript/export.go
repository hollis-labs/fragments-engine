package transcript

import (
	"encoding/json"
	"strings"
)

// The Postgres corpus (package pgstore) shares identity and redaction with this
// package. These wrappers expose exactly that, so both derive the same keys.

// SessionPK is the stable session key for a source segment.
func SessionPK(registration, source, sourceID, segment string) string {
	return sessionPK(registration, source, sourceID, segment)
}

// CursorID is the stable key of a source file's cursor.
func CursorID(registration, path string) string { return cursorID(registration, path) }

// ToolTarget is the short target (a path, a command head, a query) of a call.
func ToolTarget(name, inputRedacted string) string { return toolTarget(name, inputRedacted) }

// RedactedInputFull redacts a tool call's JSON input and returns it whole. Unlike
// the SQLite store it never replaces a large input with a size fingerprint.
func RedactedInputFull(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		// Not JSON: keep the text, redacted, rather than dropping the call's input.
		return Redact(raw)
	}
	clean, err := json.Marshal(redactValue(value))
	if err != nil {
		return ""
	}
	return string(clean)
}

// RedactJSONValue redacts every string inside a JSON document and returns it
// re-encoded. It reports false when raw is not valid JSON.
func RedactJSONValue(raw string) (string, bool) {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return "", false
	}
	clean, err := json.Marshal(redactValue(value))
	if err != nil {
		return "", false
	}
	return string(clean), true
}
