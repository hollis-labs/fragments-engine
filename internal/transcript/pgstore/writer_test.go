package pgstore

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
)

func frag(native string, d *domain.TranscriptDetail) domain.PipelineFragment {
	return domain.PipelineFragment{Source: "claude-code", SourceID: native, SourceIdentity: domain.SourceIdentity{}, Detail: d}
}

func openWriter(t *testing.T) (*Writer, *store.Store, func()) {
	t.Helper()
	cfg, _, cleanup := openScratch(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, cfg, Migrations()); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return NewWriter(s), s, func() { s.Close(); cleanup() }
}

func scalar(t *testing.T, s *store.Store, q string, args ...any) string {
	t.Helper()
	var v string
	if err := s.QueryRowContext(context.Background(), q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func TestWriterCapturesEverythingAndKeepsJournal(t *testing.T) {
	w, s, done := openWriter(t)
	defer done()
	ctx := context.Background()
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 123456789, time.UTC)
	bigInput := `{"content":"` + strings.Repeat("x", 10_000) + `"}`          // far past the old 4 KB cap
	bigResult := strings.Repeat("result line with token alphaomega\n", 4000) // ~136 KB, several chunks
	png := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}

	d1 := &domain.TranscriptDetail{
		Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "n1", Kind: "main", Model: "m", CWD: "/w", StartedAt: t0, EndedAt: t0},
		Turns: []domain.TranscriptTurn{
			{EventUUID: "e1", Role: "user", TS: t0, Text: "please plan"},
			{EventUUID: "e2", ParentUUID: "e1", Role: "assistant", TS: t0.Add(time.Second), Text: "ok", HasThinking: true, Thinking: "let me reason about quasar", ThinkingBytes: 26},
		},
		Tools: []domain.TranscriptToolCall{{ToolUseID: "c1", TurnUUID: "e2", Name: "Write", InputJSON: bigInput, StartedAt: t0, HasResult: true, ResultSize: len(bigResult),
			Results: []domain.TranscriptResultPart{
				{PartID: "0", Ordinal: 0, MediaType: "text/plain", Text: bigResult, Complete: true},
				{PartID: "1", Ordinal: 1, MediaType: "image/png", Bytes: png, Complete: true},
			}}},
		Usage:      []domain.TranscriptUsage{{RequestID: "r1", Model: "m", TS: t0, Input: 5, Output: 7}},
		Cursor:     domain.TranscriptCursor{FileSize: 100, ByteOffset: 90, LineNo: 4, TailDigest: "td", ModTime: t0, SourceID: "n1"},
		CursorPath: "p/n1.jsonl",
	}
	out, err := w.Accept(ctx, "mac-claude-archive", frag("n1", d1))
	if err != nil || out != "inserted" {
		t.Fatalf("first accept: %v %v", out, err)
	}
	if got := scalar(t, s, `SELECT length(input_text_redacted)::text FROM tool_calls WHERE call_id = 'c1'`); got != "10015" && got != "10014" {
		// "content" plus 10,000 x; redaction must not shorten it
		t.Logf("input length = %s", got)
	}
	if got := scalar(t, s, `SELECT (length(input_text_redacted) > 10000)::text FROM tool_calls WHERE call_id = 'c1'`); got != "true" {
		t.Errorf("tool input was truncated")
	}
	if got := scalar(t, s, `SELECT input_state FROM tool_calls WHERE call_id = 'c1'`); got != "complete" {
		t.Errorf("input_state = %s", got)
	}
	if got := scalar(t, s, `SELECT thinking_redacted FROM events WHERE event_id = 'e2'`); got != "let me reason about quasar" {
		t.Errorf("thinking not stored: %q", got)
	}
	if got := scalar(t, s, `SELECT (length(text_redacted) = ?)::text FROM tool_results WHERE call_id = 'c1' AND part_id = '0'`, len(bigResult)); got != "true" {
		t.Errorf("tool result text was cut")
	}
	if got := scalar(t, s, `SELECT (bytes_redacted = ?::bytea)::text FROM tool_results WHERE call_id = 'c1' AND part_id = '1'`, png); got != "true" {
		t.Errorf("image bytes not preserved")
	}
	if got := scalar(t, s, `SELECT redaction_state || '/' || read_policy FROM tool_results WHERE call_id = 'c1' AND part_id = '1'`); got != "not_redactable/open" {
		t.Errorf("image policy = %s", got)
	}
	if got := scalar(t, s, `SELECT redaction_state || '/' || read_policy FROM tool_results WHERE call_id = 'c1' AND part_id = '0'`); got != "redacted/open" {
		t.Errorf("text policy = %s", got)
	}
	if _, err := s.ExecContext(ctx, `UPDATE tool_results SET read_policy = 'withheld' WHERE call_id = 'c1' AND part_id = '1'`); err != nil {
		t.Fatal(err)
	}
	// the big result is searchable and chunked; thinking and tool input are searchable
	if n := scalar(t, s, `SELECT chunk_count::text FROM search_documents WHERE source_kind = 'tool_result'`); n == "0" || n == "1" {
		t.Errorf("large result was not chunked: %s", n)
	}
	for _, c := range []struct{ word, kind string }{{"quasar", "event"}, {"alphaomega", "tool_result"}} {
		if got := scalar(t, s, `SELECT count(*)::text FROM search_chunks c JOIN search_documents d USING (document_id) WHERE d.source_kind = ? AND c.chunk_fts_vec @@ plainto_tsquery('simple', ?)`, c.kind, c.word); got == "0" {
			t.Errorf("%s not searchable in %s", c.word, c.kind)
		}
	}
	// cursor round trip
	cur, ok := w.Cursor(ctx, "mac-claude-archive", "p/n1.jsonl")
	if !ok || cur.ByteOffset != 90 || cur.LineNo != 4 || cur.TailDigest != "td" || cur.FileSize != 100 {
		t.Errorf("cursor = %+v %v", cur, ok)
	}
	head1 := scalar(t, s, `SELECT head::text FROM feed_state`)

	// identical replay changes nothing and appends nothing to the journal
	if out, err = w.Accept(ctx, "mac-claude-archive", frag("n1", d1)); err != nil || out != "skipped" {
		t.Fatalf("replay: %v %v", out, err)
	}
	if h := scalar(t, s, `SELECT head::text FROM feed_state`); h != head1 {
		t.Errorf("replay moved the journal %s -> %s", head1, h)
	}

	// a withhold set by policy survives a re-parse of the same part
	if got := scalar(t, s, `SELECT read_policy FROM tool_results WHERE call_id = 'c1' AND part_id = '1'`); got != "withheld" {
		t.Errorf("withhold was reset: %s", got)
	}

	// a later batch: a new call without a result, then its result arrives in another batch
	d2 := &domain.TranscriptDetail{Incremental: true,
		Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "n1", Kind: "main"},
		Turns:   []domain.TranscriptTurn{{EventUUID: "e3", Role: "assistant", TS: t0.Add(2 * time.Second), Text: "next"}},
		Tools:   []domain.TranscriptToolCall{{ToolUseID: "c2", TurnUUID: "e3", Name: "Bash", InputJSON: `{"command":"ls"}`}},
	}
	if out, err = w.Accept(ctx, "mac-claude-archive", frag("n1", d2)); err != nil || out != "updated" {
		t.Fatalf("batch 2: %v %v", out, err)
	}
	if got := scalar(t, s, `SELECT status FROM tool_calls WHERE call_id = 'c2'`); got != "none" {
		t.Errorf("status before result = %s", got)
	}
	d3 := &domain.TranscriptDetail{Incremental: true,
		Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "n1", Kind: "main"},
		Tools: []domain.TranscriptToolCall{{ToolUseID: "c2", TurnUUID: "e3", Name: "Bash", HasResult: true, ResultSize: 9,
			Results: []domain.TranscriptResultPart{{PartID: "0", MediaType: "text/plain", Text: "file.txt\n", Complete: true}}}},
	}
	if _, err = w.Accept(ctx, "mac-claude-archive", frag("n1", d3)); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, s, `SELECT status || '|' || input_text_redacted FROM tool_calls WHERE call_id = 'c2'`); got != `ok|{"command":"ls"}` {
		t.Errorf("late result must merge without losing the input: %s", got)
	}
	// a call whose turn never arrived is held, not invented
	d4 := &domain.TranscriptDetail{Incremental: true, Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "n1", Kind: "main"},
		Tools: []domain.TranscriptToolCall{{ToolUseID: "orphan", TurnUUID: "nope", Name: "Read", InputJSON: `{}`}}}
	if _, err = w.Accept(ctx, "mac-claude-archive", frag("n1", d4)); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, s, `SELECT count(*)::text FROM pending_records WHERE native_key = 'orphan' AND state = 'pending'`); got != "1" {
		t.Errorf("orphan call not held: %s", got)
	}
	if got := scalar(t, s, `SELECT count(*)::text FROM tool_calls WHERE call_id = 'orphan'`); got != "0" {
		t.Errorf("orphan call was fabricated")
	}

	// the journal is contiguous 1..head and ordered by commit
	if got := scalar(t, s, `SELECT (count(*) = max(seq) AND min(seq) = 1)::text FROM changes`); got != "true" {
		t.Errorf("journal has gaps")
	}
	if got := scalar(t, s, `SELECT (max(seq) = (SELECT head FROM feed_state))::text FROM changes`); got != "true" {
		t.Errorf("head does not match the last change")
	}

	// reset starts a new generation and drops the old rows
	d5 := &domain.TranscriptDetail{Reset: true, Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "n1", Kind: "main"},
		Turns: []domain.TranscriptTurn{{EventUUID: "r1", Role: "user", Text: "fresh start"}}}
	if out, err = w.Accept(ctx, "mac-claude-archive", frag("n1", d5)); err != nil || out != "updated" {
		t.Fatalf("reset: %v %v", out, err)
	}
	if got := scalar(t, s, `SELECT generation::text || '|' || (SELECT count(*) FROM events)::text || '|' || (SELECT count(*) FROM tool_calls)::text FROM sessions`); got != "2|1|0" {
		t.Errorf("after reset generation|events|calls = %s", got)
	}
	if got := scalar(t, s, `SELECT count(*)::text FROM changes WHERE kind = 'session_reset'`); got != "1" {
		t.Errorf("session_reset not journaled: %s", got)
	}
}

func TestWriterConcurrentSessionsKeepJournalOrdered(t *testing.T) {
	w, s, done := openWriter(t)
	defer done()
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			native := string(rune('a' + i))
			d := &domain.TranscriptDetail{Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: native, Kind: "main"},
				Turns: []domain.TranscriptTurn{{EventUUID: "e1", Role: "user", Text: "hello " + native}, {EventUUID: "e2", Role: "assistant", Text: "hi"}}}
			_, err := w.Accept(ctx, "r", frag(native, d))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := scalar(t, s, `SELECT (count(*) = max(seq) AND min(seq) = 1 AND count(distinct seq) = count(*))::text FROM changes`); got != "true" {
		t.Errorf("concurrent writers produced gaps or duplicates in the journal")
	}
	if got := scalar(t, s, `SELECT (SELECT count(*) FROM changes WHERE kind = 'event_upsert')::text`); got != "24" {
		t.Errorf("event_upsert changes = %s", got)
	}
}

func TestWriterReplacesNULRatherThanFailing(t *testing.T) {
	w, s, done := openWriter(t)
	defer done()
	d := &domain.TranscriptDetail{Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "n", Kind: "main"},
		Turns: []domain.TranscriptTurn{{EventUUID: "e1", Role: "user", Text: "a\x00b"}}}
	if _, err := w.Accept(context.Background(), "r", frag("n", d)); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, s, `SELECT payload_redacted->>'nul_replaced' FROM events`); got != "true" {
		t.Errorf("NUL replacement not recorded: %q", got)
	}
}

// A record with no typed model is stored raw, flagged not redactable, and does
// not count as a turn.
func TestWriterKeepsUnsupportedStepsRaw(t *testing.T) {
	w, s, done := openWriter(t)
	defer done()
	raw := []byte{0x08, 0x96, 0x01, 0xff, 0x00, 0x42}
	d := &domain.TranscriptDetail{Session: domain.TranscriptSession{Tool: "antigravity", NativeSessionID: "n", Kind: "main"},
		Turns: []domain.TranscriptTurn{{EventUUID: "step-0", Role: "user", Text: "hi"}},
		Steps: []domain.TranscriptStep{{EventUUID: "step-1", Type: 17, Format: 0, Status: 3, Payload: raw}}}
	for i := 0; i < 2; i++ { // the replay must not duplicate it
		if _, err := w.Accept(context.Background(), "r", frag("n", d)); err != nil {
			t.Fatal(err)
		}
	}
	if got := scalar(t, s, `SELECT count(*) FROM events WHERE kind = 'step'`); got != "1" {
		t.Fatalf("steps stored = %s", got)
	}
	if got := scalar(t, s, `SELECT payload_redacted->>'step_type' || '/' || (payload_redacted->>'not_redactable') || '/' || (payload_redacted->>'payload_b64') FROM events WHERE kind = 'step'`); got != "17/true/"+base64.StdEncoding.EncodeToString(raw) {
		t.Errorf("payload = %s", got)
	}
	if got := scalar(t, s, `SELECT legacy_turn_count FROM sessions`); got != "1" {
		t.Errorf("turn count = %s", got)
	}
}

// History captured before thinking text was kept has the event but not the text.
// A replay that now carries it fills it in, once, and leaves ordinals alone.
func TestWriterBackfillsThinkingOnStoredTurns(t *testing.T) {
	w, s, done := openWriter(t)
	defer done()
	ctx := context.Background()
	d := func(thinking string) *domain.TranscriptDetail {
		return &domain.TranscriptDetail{Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "n", Kind: "main"},
			Turns: []domain.TranscriptTurn{
				{EventUUID: "e1", Role: "assistant", Text: "answer", HasThinking: true, ThinkingBytes: 11, Thinking: thinking},
				{EventUUID: "e2", Role: "user", Text: "next"},
			}}
	}
	if _, err := w.Accept(ctx, "r", frag("n", d(""))); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, s, `SELECT thinking_redacted FROM events WHERE event_id = 'e1'`); got != "" {
		t.Fatalf("setup: %q", got)
	}
	out, err := w.Accept(ctx, "r", frag("n", d("weigh A or B")))
	if err != nil {
		t.Fatal(err)
	}
	if out != transcript.Updated {
		t.Errorf("outcome = %v", out)
	}
	if got := scalar(t, s, `SELECT thinking_redacted FROM events WHERE event_id = 'e1'`); got != "weigh A or B" {
		t.Errorf("thinking not backfilled: %q", got)
	}
	if got := scalar(t, s, `SELECT string_agg(event_id || ':' || ordinal, ',' ORDER BY ordinal) FROM events`); got != "e1:0,e2:1" {
		t.Errorf("ordinals disturbed: %s", got)
	}
	if got := scalar(t, s, `SELECT count(*) FROM search_documents WHERE field = 'thinking' AND entity_key = 'e1'`); got != "1" {
		t.Errorf("thinking not searchable: %s", got)
	}
	head := scalar(t, s, `SELECT head FROM feed_state`)
	if out, _ := w.Accept(ctx, "r", frag("n", d("weigh A or B"))); out != transcript.Skipped {
		t.Errorf("replay = %v", out)
	}
	if got := scalar(t, s, `SELECT head FROM feed_state`); got != head {
		t.Errorf("replay journaled: %s -> %s", head, got)
	}
}

// The host comes from the ingest that produced the session; a session stored with
// an unknown host (imported history) learns it, and a known host is never changed.
func TestWriterRecordsAndLearnsHost(t *testing.T) {
	w, s, done := openWriter(t)
	defer done()
	ctx := context.Background()
	d := func() *domain.TranscriptDetail {
		return &domain.TranscriptDetail{Session: domain.TranscriptSession{Tool: "claude", NativeSessionID: "n", Kind: "main"},
			Turns: []domain.TranscriptTurn{{EventUUID: "e1", Role: "user", Text: "hi"}}}
	}
	if _, err := w.Accept(ctx, "r", frag("n", d())); err != nil { // no mapping: unknown
		t.Fatal(err)
	}
	if got := scalar(t, s, `SELECT host_id FROM sessions`); got != "unknown" {
		t.Fatalf("host = %s", got)
	}
	w.HostFor = func(string) string { return "mac" }
	if _, err := w.Accept(ctx, "r", frag("n", d())); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, s, `SELECT host_id FROM sessions`) + "/" + scalar(t, s, `SELECT host_id FROM sources`); got != "mac/mac" {
		t.Fatalf("host not learned: %s", got)
	}
	w.HostFor = func(string) string { return "other" }
	if _, err := w.Accept(ctx, "r", frag("n", d())); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, s, `SELECT host_id FROM sessions`); got != "mac" {
		t.Fatalf("known host changed: %s", got)
	}
}
