package transcript

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest/claude"
)

const reg = "claude-test"

// pass runs one fire: collect with the store's cursors, then accept each result.
func pass(t *testing.T, st *Store, root string, rules map[string]any) (outcomes []Outcome, skips map[string]int, got []domain.PipelineFragment) {
	t.Helper()
	collector := &domain.SkipCollector{}
	ctx := domain.WithSkipCollector(context.Background(), collector)
	ctx = domain.WithCursorLookup(ctx, func(path string) (domain.TranscriptCursor, bool) {
		return st.Cursor(context.Background(), reg, path)
	})
	cfg := config.IngestConfig{Name: reg, Kind: "claude_code", Source: config.IngestSource{Root: root}, Rules: rules}
	fragments, err := (claude.Source{}).Collect(ctx, cfg)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, f := range fragments {
		out, err := st.Accept(context.Background(), reg, f)
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		outcomes = append(outcomes, out)
	}
	return outcomes, collector.ByClass(), fragments
}

func count(t *testing.T, st *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendRaw(t *testing.T, path, raw string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
}

const (
	lUser    = `{"type":"user","uuid":"u1","sessionId":"S1","slug":"synthetic-slug","cwd":"/synthetic","gitBranch":"main","timestamp":"2026-10-10T10:00:00Z","message":{"content":"synthetic question password=synthetic-secret-value"}}`
	lAsst1   = `{"type":"assistant","uuid":"a1","parentUuid":"u1","requestId":"r1","sessionId":"S1","timestamp":"2026-10-10T10:00:01Z","perTurnEffort":"high","message":{"model":"synthetic-model","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"synthetic-private-thought"},{"type":"text","text":"answer one"},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/synthetic/file","api_key":"synthetic-key-in-input"}}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":7}}}`
	lAsst2   = `{"type":"assistant","uuid":"a2","parentUuid":"a1","requestId":"r1","sessionId":"S1","timestamp":"2026-10-10T10:00:02Z","message":{"model":"synthetic-model","content":[{"type":"text","text":"answer two"}],"usage":{"input_tokens":10,"output_tokens":9,"cache_read_input_tokens":100,"cache_creation_input_tokens":7}}}`
	lResult  = `{"type":"user","uuid":"u2","parentUuid":"a2","sessionId":"S1","timestamp":"2026-10-10T10:00:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"result body password=synthetic-result","is_error":false}]}}`
	lTitle   = `{"type":"ai-title","sessionId":"S1"}`
	lAsst3   = `{"type":"assistant","uuid":"a3","requestId":"r2","sessionId":"S1","timestamp":"2026-10-10T11:00:00Z","message":{"model":"synthetic-model","content":[{"type":"text","text":"answer three"},{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":1,"output_tokens":2}}}`
	lResult2 = `{"type":"user","uuid":"u3","sessionId":"S1","timestamp":"2026-10-10T11:00:05Z","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"boom","is_error":true}]}}`
)

func openStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(canonicalTempDir(t), "private"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestClaudeDetailCapturesStructureOnceAndRedacted(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	file := filepath.Join(root, "proj", "S1.jsonl")
	writeLines(t, file, lUser, lAsst1, lAsst2, lResult, lTitle)

	out, _, _ := pass(t, st, root, nil)
	if len(out) != 1 || out[0] != Inserted {
		t.Fatalf("outcomes %v", out)
	}
	pk := sessionPK(reg, "claude", "S1", "")
	if n := count(t, st, `SELECT COUNT(*) FROM tx_turns WHERE session_pk = ?`, pk); n != 4 {
		t.Fatalf("turns = %d", n)
	}
	// One API response split over two lines is one usage row, merged by maximum.
	var in, outTok, cacheRead, cacheCreate int64
	var model, effort string
	if err := st.db.QueryRow(`SELECT input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, model, effort FROM tx_usage WHERE session_pk = ? AND request_id = 'r1'`, pk).
		Scan(&in, &outTok, &cacheRead, &cacheCreate, &model, &effort); err != nil {
		t.Fatal(err)
	}
	if in != 10 || outTok != 9 || cacheRead != 100 || cacheCreate != 7 || model != "synthetic-model" || effort != "high" {
		t.Fatalf("usage %d %d %d %d %s %s", in, outTok, cacheRead, cacheCreate, model, effort)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tx_usage`); n != 1 {
		t.Fatalf("usage rows = %d", n)
	}
	totals, err := st.SessionUsage(context.Background(), "S1")
	if err != nil || totals.Requests != 1 || totals.Output != 9 {
		t.Fatalf("totals %+v %v", totals, err)
	}
	// Thinking: presence and length only.
	var hasThinking, thinkingBytes int
	if err := st.db.QueryRow(`SELECT has_thinking, thinking_bytes FROM tx_turns WHERE event_uuid = 'a1'`).Scan(&hasThinking, &thinkingBytes); err != nil || hasThinking != 1 || thinkingBytes != len("synthetic-private-thought") {
		t.Fatalf("thinking metadata %d %d %v", hasThinking, thinkingBytes, err)
	}
	// Tool call: paired with its result, input redacted, result body not kept.
	var name, status, input, excerpt string
	var resultBytes int
	if err := st.db.QueryRow(`SELECT name, result_status, input_redacted, result_bytes, result_excerpt_redacted FROM tx_tool_calls WHERE tool_use_id = 't1'`).
		Scan(&name, &status, &input, &resultBytes, &excerpt); err != nil {
		t.Fatal(err)
	}
	if name != "Read" || status != "ok" || resultBytes != len("result body password=synthetic-result") || excerpt != "" || !strings.Contains(input, "/synthetic/file") {
		t.Fatalf("tool call %s %s %d %q %s", name, status, resultBytes, excerpt, input)
	}
	// Nothing sensitive reaches any derived table or the search text.
	for _, table := range []string{"tx_sessions", "tx_turns", "tx_tool_calls", "tx_usage", "tx_cursors", "transcripts", "transcript_search"} {
		rows, err := st.db.Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				text := fmt.Sprint(v)
				if b, ok := v.([]byte); ok {
					text = string(b)
				}
				for _, secret := range []string{"synthetic-secret-value", "synthetic-key-in-input", "synthetic-private-thought", "synthetic-result"} {
					if strings.Contains(text, secret) {
						t.Fatalf("%s leaks %q", table, secret)
					}
				}
			}
		}
		rows.Close()
	}
	var content string
	if err := st.db.QueryRow(`SELECT content FROM transcripts`).Scan(&content); err != nil || !strings.Contains(content, "answer one") || !strings.Contains(content, "answer two") || !strings.Contains(content, "synthetic question") {
		t.Fatalf("search text %q %v", content, err)
	}
	// Queryable by tool name, session, time and error status.
	calls, err := st.QueryToolCalls(context.Background(), ToolCallFilter{Name: "Read", NativeSessionID: "S1", Status: "ok",
		Since: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)})
	if err != nil || len(calls) != 1 || calls[0].ToolUseID != "t1" || calls[0].SessionKind != "main" {
		t.Fatalf("query %+v %v", calls, err)
	}
	if calls, _ := st.QueryToolCalls(context.Background(), ToolCallFilter{Status: "error"}); len(calls) != 0 {
		t.Fatal("no errored call expected yet")
	}
}

func TestClaudeIncrementalCursorAndRewrite(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	file := filepath.Join(root, "proj", "S1.jsonl")
	writeLines(t, file, lUser, lAsst1, lAsst2, lResult)
	if out, _, _ := pass(t, st, root, nil); len(out) != 1 || out[0] != Inserted {
		t.Fatalf("first %v", out)
	}
	// Unchanged: no fragment, counted as a skip, no file read needed.
	out, skips, frags := pass(t, st, root, nil)
	if len(out) != 0 || len(frags) != 0 || skips["unchanged"] != 1 {
		t.Fatalf("unchanged %v %v", out, skips)
	}
	// Append two whole lines and half of a third: only the whole lines count.
	half := lResult2[:len(lResult2)/2]
	appendRaw(t, file, lAsst3+"\n"+half)
	out, _, frags = pass(t, st, root, nil)
	if len(out) != 1 || out[0] != Updated || !frags[0].Detail.Incremental || frags[0].Detail.Reset {
		t.Fatalf("append %v", out)
	}
	if n := len(frags[0].Detail.Turns); n != 1 {
		t.Fatalf("incremental parsed %d turns, want only the appended one", n)
	}
	pk := sessionPK(reg, "claude", "S1", "")
	if n := count(t, st, `SELECT COUNT(*) FROM tx_turns WHERE session_pk = ?`, pk); n != 5 {
		t.Fatalf("turns = %d", n)
	}
	var content, title string
	if err := st.db.QueryRow(`SELECT content, title FROM transcripts`).Scan(&content, &title); err != nil ||
		!strings.Contains(content, "answer one") || !strings.Contains(content, "answer three") || title != "Claude session: synthetic-slug" {
		t.Fatalf("incremental text/title %q %q %v", content, title, err)
	}
	// The half line is not consumed; finishing it is picked up next fire.
	appendRaw(t, file, lResult2[len(half):]+"\n")
	out, _, _ = pass(t, st, root, nil)
	if len(out) != 1 {
		t.Fatalf("finish half line %v", out)
	}
	var st2 string
	var bytes int
	if err := st.db.QueryRow(`SELECT result_status, result_bytes FROM tx_tool_calls WHERE tool_use_id = 't2'`).Scan(&st2, &bytes); err != nil || st2 != "error" || bytes != 4 {
		t.Fatalf("t2 %s %d %v", st2, bytes, err)
	}
	// Usage across batches still one row per request.
	if n := count(t, st, `SELECT COUNT(*) FROM tx_usage WHERE session_pk = ?`, pk); n != 2 {
		t.Fatalf("usage rows = %d", n)
	}
	// A later batch repeating an earlier request merges by maximum, never sums.
	appendRaw(t, file, `{"type":"assistant","uuid":"a4","requestId":"r1","sessionId":"S1","timestamp":"2026-10-10T12:00:00Z","message":{"model":"synthetic-model","content":[{"type":"text","text":"answer four"}],"usage":{"input_tokens":10,"output_tokens":12}}}`+"\n")
	if out, _, _ = pass(t, st, root, nil); len(out) != 1 {
		t.Fatalf("repeat request batch %v", out)
	}
	var merged int64
	if err := st.db.QueryRow(`SELECT output_tokens FROM tx_usage WHERE session_pk = ? AND request_id = 'r1'`, pk).Scan(&merged); err != nil || merged != 12 {
		t.Fatalf("cross-batch usage = %d %v", merged, err)
	}
	// A rewritten (not appended) file fails the tail check and is replaced.
	writeLines(t, file, strings.Replace(lUser, "synthetic question", "rewritten question", 1), lAsst2)
	out, _, frags = pass(t, st, root, nil)
	if len(out) != 1 || !frags[0].Detail.Reset || frags[0].Detail.Incremental {
		t.Fatalf("rewrite %v", out)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tx_turns WHERE session_pk = ?`, pk); n != 2 {
		t.Fatalf("turns after rewrite = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tx_tool_calls WHERE session_pk = ?`, pk); n != 0 {
		t.Fatalf("stale tool calls survived rewrite: %d", n)
	}
}

func TestClaudeBackfillKeepsExistingRecordIdentity(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	writeLines(t, filepath.Join(root, "proj", "S1.jsonl"), lUser, lAsst1, lAsst2, lResult)
	// A record stored by the pre-structure path.
	_, _, frags := pass(t, st, root, nil)
	legacy := frags[0]
	legacy.Detail = nil
	st2 := openStore(t)
	if out, err := st2.Accept(context.Background(), reg, legacy); err != nil || out != Inserted {
		t.Fatalf("legacy accept %v %v", out, err)
	}
	// The first structured pass over the same archive is a backfill: derived rows
	// appear, the existing record is recognised as unchanged.
	out, _, _ := pass(t, st2, root, nil)
	if len(out) != 1 || out[0] != Skipped {
		t.Fatalf("backfill outcome %v", out)
	}
	if n := count(t, st2, `SELECT COUNT(*) FROM transcripts`); n != 1 {
		t.Fatalf("records = %d", n)
	}
	if n := count(t, st2, `SELECT COUNT(*) FROM tx_turns`); n != 4 {
		t.Fatalf("turns = %d", n)
	}
}

func TestClaudeSubagentLinkageAndToolResultExcerpt(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	writeLines(t, filepath.Join(proj, "S1.jsonl"), lUser, lAsst1, lResult)
	sub := filepath.Join(proj, "S1", "subagents", "agent-x.jsonl")
	writeLines(t, sub, strings.ReplaceAll(lAsst3, `"a3"`, `"sa1"`))
	if err := os.WriteFile(strings.TrimSuffix(sub, ".jsonl")+".meta.json",
		[]byte(`{"agentType":"synthetic-type","toolUseId":"t1","spawnDepth":1,"isFork":true,"description":"synthetic password=synthetic-meta"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pass(t, st, root, map[string]any{"tool_result_excerpt_bytes": 24})
	var kind, parent, agent, spawn string
	var depth, fork int
	if err := st.db.QueryRow(`SELECT kind, parent_native_id, agent_type, spawn_tool_use_id, spawn_depth, is_fork FROM tx_sessions WHERE kind = 'subagent'`).
		Scan(&kind, &parent, &agent, &spawn, &depth, &fork); err != nil {
		t.Fatal(err)
	}
	if parent != "S1" || agent != "synthetic-type" || spawn != "t1" || depth != 1 || fork != 1 {
		t.Fatalf("subagent %s %s %s %s %d %d", kind, parent, agent, spawn, depth, fork)
	}
	// The spawn id joins to the parent's tool call, and the parent pk resolves.
	if n := count(t, st, `SELECT COUNT(*) FROM tx_sessions s JOIN tx_tool_calls c ON c.tool_use_id = s.spawn_tool_use_id
 JOIN tx_sessions p ON p.session_pk = c.session_pk AND p.session_pk = s.parent_session_pk WHERE s.kind = 'subagent'`); n != 1 {
		t.Fatalf("subagent not joinable to its spawning call: %d", n)
	}
	var excerpt string
	if err := st.db.QueryRow(`SELECT result_excerpt_redacted FROM tx_tool_calls WHERE tool_use_id = 't1'`).Scan(&excerpt); err != nil {
		t.Fatal(err)
	}
	if excerpt == "" || len(excerpt) > 24 || strings.Contains(excerpt, "synthetic-result") {
		t.Fatalf("excerpt %q", excerpt)
	}
}

func TestToolInputCapAndUnreadableLine(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	big := strings.Repeat("a", 5000)
	line := fmt.Sprintf(`{"type":"assistant","uuid":"a1","requestId":"r1","sessionId":"S1","timestamp":"2026-10-10T10:00:00Z","message":{"content":[{"type":"tool_use","id":"t1","name":"Write","input":{"content":"%s","path":"x"}}]}}`, big)
	writeLines(t, filepath.Join(root, "p", "S1.jsonl"), line)
	// A line over the cap is skipped with a class, and does not stop other files.
	hugePath := filepath.Join(root, "p", "S2.jsonl")
	writeLines(t, hugePath, strings.Repeat("x", 10<<20+10))
	_, skips, _ := pass(t, st, root, nil)
	if skips["unreadable_line"] != 1 {
		t.Fatalf("skips %v", skips)
	}
	var input string
	if err := st.db.QueryRow(`SELECT input_redacted FROM tx_tool_calls WHERE tool_use_id = 't1'`).Scan(&input); err != nil {
		t.Fatal(err)
	}
	if len(input) > maxToolInputBytes || !strings.Contains(input, `"truncated":true`) || strings.Contains(input, big[:100]) {
		t.Fatalf("input not capped to a fingerprint: %d bytes", len(input))
	}
}
