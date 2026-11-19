package domain

import (
	"context"
	"time"
)

// TranscriptDetail is the structured, per-turn view of a transcript segment that
// a source parsed alongside its searchable text. It travels in memory from the
// source to the private transcript store, which redacts every string before
// persisting it. It is never marshalled and never leaves the owner-private
// store: it has no delivery, recall or destination path.
//
// Tokens only, never prices: cost and task/workstream dimensions belong to the
// consumer that joins on the native session id.
type TranscriptDetail struct {
	Session TranscriptSession
	Turns   []TranscriptTurn
	// Steps are source records without a typed model, kept as raw events.
	Steps []TranscriptStep
	Tools   []TranscriptToolCall
	Usage   []TranscriptUsage
	// Reset means the cursor was invalid (truncated or rewritten source), so the
	// store drops the segment's derived rows before applying this parse.
	Reset bool
	// Incremental means the parse started at a stored cursor, so the fragment's
	// own title/metadata describe only the appended lines. The store keeps the
	// segment's original record and replaces just its searchable text.
	Incremental bool
	Cursor      TranscriptCursor
	// CursorPath is the source-relative path the cursor is keyed by.
	CursorPath string
}

type TranscriptSession struct {
	Tool            string // "claude"
	NativeSessionID string
	Kind            string // "main" or "subagent"
	ParentNativeID  string
	AgentType       string
	SpawnDepth      int
	SpawnToolUseID  string
	IsFork          bool
	Model           string
	// Description is the subagent's purpose line from its .meta.json.
	Description string
	CWD         string
	GitBranch   string
	Entrypoint  string
	StartedAt   time.Time
	EndedAt     time.Time
}

type TranscriptTurn struct {
	EventUUID     string
	ParentUUID    string
	Role          string // "user" or "assistant"
	TS            time.Time
	RequestID     string
	IsSidechain   bool
	StopReason    string
	Text          string // searchable text blocks only
	HasThinking   bool
	ThinkingBytes int // length of thinking text in bytes
	// Thinking is the model's thinking text, kept in full (redacted by the store).
	Thinking string
}

// TranscriptStep is a source record this reader has no typed model for. It is
// kept, not dropped: its type, format, status and raw payload are stored as an
// event so a later parser can decode it without re-reading the source.
type TranscriptStep struct {
	EventUUID string
	Type      int
	Format    int
	Status    int
	TS        time.Time
	Payload   []byte // raw bytes; not redactable
}

type TranscriptToolCall struct {
	ToolUseID  string
	TurnUUID   string
	Name       string
	InputJSON  string // raw tool input; the store redacts and caps it
	StartedAt  time.Time
	EndedAt    time.Time
	HasResult  bool
	IsError    bool
	ResultSize int
	// ResultExcerpt is raw and bounded by the source; the store redacts it and
	// truncates to MaxExcerpt. Empty unless the ingest enabled excerpts.
	// Superseded by Results; only the retiring SQLite store reads it.
	ResultExcerpt string
	MaxExcerpt    int
	// Results is the complete tool result, one entry per content part, in full.
	// Nothing is truncated; large results are the store's to chunk for search.
	Results []TranscriptResultPart
}

// TranscriptResultPart is one part of a tool result. Exactly one of Text, JSON
// or Bytes is set. PartID is stable across re-parses of the same source.
type TranscriptResultPart struct {
	PartID    string
	Ordinal   int
	MediaType string // "text/plain", "application/json", "image/png", ...
	Text      string
	JSON      string
	Bytes     []byte
	// Complete is false only when the source itself was cut off (an unfinished tail).
	Complete bool
}

type TranscriptUsage struct {
	RequestID     string
	Model         string
	TS            time.Time
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
	Effort        string
}

// TranscriptCursor records how far a segment was consumed. ByteOffset sits just
// after the last complete line; TailDigest covers the bytes before it so a
// rewritten or truncated source is detected rather than trusted.
//
// Antigravity conversation databases are replaced wholesale by each snapshot, so
// there ByteOffset is the idx of the first step that has not been consumed (steps
// with a smaller idx are stored; a trailing run of running or aborted steps is
// left unconsumed so it is read again), LineNo is the number of steps parsed on
// the last pass, and TailDigest identifies the last consumed step by immutable
// facts. A snapshot whose digest at ByteOffset-1 differs was rewritten and is
// re-parsed whole.
type TranscriptCursor struct {
	FileSize   int64
	ModTime    time.Time
	ByteOffset int64
	LineNo     int
	TailDigest string
	// SourceID is the session identity the segment was first stored under, so an
	// incremental parse files appended lines under the same record.
	SourceID string
}

// CursorLookup returns the stored cursor for a source-relative path.
type CursorLookup func(path string) (TranscriptCursor, bool)

type cursorKey struct{}

// WithCursorLookup makes stored cursors visible to a source during Collect.
func WithCursorLookup(ctx context.Context, lookup CursorLookup) context.Context {
	return context.WithValue(ctx, cursorKey{}, lookup)
}

// LookupCursor returns the stored cursor for path, or false when there is none
// or no store is attached (a source then parses the whole file).
func LookupCursor(ctx context.Context, path string) (TranscriptCursor, bool) {
	lookup, _ := ctx.Value(cursorKey{}).(CursorLookup)
	if lookup == nil {
		return TranscriptCursor{}, false
	}
	return lookup(path)
}

// ApplyExcerptPolicy sets how much of each tool result is kept: all bytes for any result, onError more for failed
// ones. The sources capture up to the larger of the two; this trims what the policy does not allow.
func (d *TranscriptDetail) ApplyExcerptPolicy(all, onError int) {
	if d == nil {
		return
	}
	for i := range d.Tools {
		c := &d.Tools[i]
		limit := all
		if c.IsError && onError > limit {
			limit = onError
		}
		if limit <= 0 {
			c.ResultExcerpt, c.MaxExcerpt = "", 0
			continue
		}
		c.MaxExcerpt = limit
	}
}

// ExcerptCapture is the capture limit a source should use for a policy.
func ExcerptCapture(all, onError int) int {
	if onError > all {
		return onError
	}
	return all
}
