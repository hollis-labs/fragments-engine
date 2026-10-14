package transcript

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest/codex"
)

const codexReg = "codex-test"

func codexPass(t *testing.T, st *Store, root string, rules map[string]any) (outcomes []Outcome, skips map[string]int, got []domain.PipelineFragment) {
	t.Helper()
	collector := &domain.SkipCollector{}
	ctx := domain.WithSkipCollector(context.Background(), collector)
	ctx = domain.WithCursorLookup(ctx, func(path string) (domain.TranscriptCursor, bool) {
		return st.Cursor(context.Background(), codexReg, path)
	})
	cfg := config.IngestConfig{Name: codexReg, Kind: "codex_sessions", Source: config.IngestSource{Root: root}, Rules: rules}
	fragments, err := (codex.Source{}).Collect(ctx, cfg)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, f := range fragments {
		out, err := st.Accept(context.Background(), codexReg, f)
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		outcomes = append(outcomes, out)
	}
	return outcomes, collector.ByClass(), fragments
}

const (
	cMeta   = `{"timestamp":"2026-10-12T10:00:00.000Z","type":"session_meta","payload":{"id":"T1","session_id":"T1","timestamp":"2026-10-12T10:00:00.000Z","cwd":"/synthetic/proj","cli_version":"0.1","source":"cli","base_instructions":{"text":"synthetic-instructions"}}}`
	cCtx    = `{"timestamp":"2026-10-12T10:00:00.500Z","type":"turn_context","payload":{"model":"synthetic-model","effort":"high","cwd":"/synthetic/proj"}}`
	cUser   = `{"timestamp":"2026-10-12T10:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic question password=synthetic-secret-value"}]}}`
	cReason = `{"timestamp":"2026-10-12T10:00:02Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"synthetic-private-thought"}],"encrypted_content":"synthetic-blob"}}`
	cAsst   = `{"timestamp":"2026-10-12T10:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer one"}]}}`
	cCall   = `{"timestamp":"2026-10-12T10:00:04Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"ls\",\"token\":\"synthetic-token-in-input\"}"}}`
	cOut    = `{"timestamp":"2026-10-12T10:00:05Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"file list password=synthetic-result"}}`
	cEnd    = `{"timestamp":"2026-10-12T10:00:05Z","type":"event_msg","payload":{"type":"exec_command_end","call_id":"c1","exit_code":1,"aggregated_output":"boom"}}`
	cPatch  = `{"timestamp":"2026-10-12T10:00:06Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c2","input":"*** Begin Patch\n*** Update File: /synthetic/a.go\n*** Add File: /synthetic/b.go\n*** End Patch"}}`
	cPatOut = `{"timestamp":"2026-10-12T10:00:07Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c2","output":"Success"}}`
	cTC1    = `{"timestamp":"2026-10-12T10:00:08Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":50,"total_tokens":1050},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":50,"total_tokens":1050}}}}`
	cAsst2  = `{"timestamp":"2026-10-12T11:00:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer two"}]}}`
	cTC2    = `{"timestamp":"2026-10-12T11:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":2200,"cached_input_tokens":1700,"output_tokens":90,"total_tokens":2290},"last_token_usage":{"input_tokens":1200,"cached_input_tokens":1100,"output_tokens":40,"total_tokens":1240}}}}`
)

func TestCodexDetailCapturesStructureOnceAndRedacted(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	// The count line repeats while the session idles; it is one request.
	writeLines(t, filepath.Join(root, "2026", "rollout-T1.jsonl"), cMeta, cCtx, cUser, cReason, cAsst, cCall, cOut, cEnd, cPatch, cPatOut, cTC1, cTC1, cTC1)

	out, _, _ := codexPass(t, st, root, nil)
	if len(out) != 1 || out[0] != Inserted {
		t.Fatalf("outcomes %v", out)
	}
	pk := sessionPK(codexReg, "codex", "T1", "rollout/2026/rollout-T1.jsonl")
	if n := count(t, st, `SELECT COUNT(*) FROM tx_turns WHERE session_pk = ?`, pk); n != 2 {
		t.Fatalf("turns = %d", n)
	}
	var tool, kind, model, cwd, entry string
	if err := st.db.QueryRow(`SELECT tool, kind, model, cwd, entrypoint FROM tx_sessions WHERE session_pk = ?`, pk).Scan(&tool, &kind, &model, &cwd, &entry); err != nil {
		t.Fatal(err)
	}
	if tool != "codex" || kind != "main" || model != "synthetic-model" || cwd != "/synthetic/proj" || entry != "cli" {
		t.Fatalf("session %s %s %s %s %s", tool, kind, model, cwd, entry)
	}
	// Reasoning: presence and the length of its readable summary; never the text.
	var thinking, thinkingBytes int
	if err := st.db.QueryRow(`SELECT has_thinking, thinking_bytes FROM tx_turns WHERE session_pk = ? AND role = 'assistant'`, pk).Scan(&thinking, &thinkingBytes); err != nil {
		t.Fatal(err)
	}
	if thinking != 1 || thinkingBytes != len("synthetic-private-thought") {
		t.Fatalf("thinking %d %d", thinking, thinkingBytes)
	}
	// One request, with cached tokens split out of the input count.
	var in, outTok, cacheRead int64
	if err := st.db.QueryRow(`SELECT input_tokens, output_tokens, cache_read_tokens FROM tx_usage WHERE session_pk = ?`, pk).Scan(&in, &outTok, &cacheRead); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tx_usage WHERE session_pk = ?`, pk); n != 1 || in != 400 || outTok != 50 || cacheRead != 600 {
		t.Fatalf("usage rows %d: %d %d %d", n, in, outTok, cacheRead)
	}
	// Tool calls: the exec call failed (exit 1), the patch names its files.
	var status string
	if err := st.db.QueryRow(`SELECT result_status FROM tx_tool_calls WHERE session_pk = ? AND tool_use_id = 'c1'`, pk).Scan(&status); err != nil || status != "error" {
		t.Fatalf("exec status %q %v", status, err)
	}
	var patchIn string
	if err := st.db.QueryRow(`SELECT input_redacted FROM tx_tool_calls WHERE session_pk = ? AND tool_use_id = 'c2'`, pk).Scan(&patchIn); err != nil || !strings.Contains(patchIn, "/synthetic/a.go") || !strings.Contains(patchIn, "/synthetic/b.go") || strings.Contains(patchIn, "Begin Patch") {
		t.Fatalf("patch input %q %v", patchIn, err)
	}
	facts, err := st.SessionFacts(context.Background(), pk)
	if err != nil || facts.ToolCalls != 2 || facts.ToolErrors != 1 || len(facts.FilesTouched) != 2 || facts.UserTurns != 1 || facts.AssistantTurns != 1 {
		t.Fatalf("facts %+v %v", facts, err)
	}
	// Nothing sensitive reached any derived table or the searchable text.
	for _, q := range []string{
		`SELECT group_concat(text_redacted) FROM tx_turns`,
		`SELECT group_concat(input_redacted) FROM tx_tool_calls`,
		`SELECT group_concat(content) FROM transcripts`,
	} {
		var all string
		if err := st.db.QueryRow(q).Scan(&all); err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"synthetic-secret-value", "synthetic-token-in-input", "synthetic-private-thought", "synthetic-blob", "synthetic-instructions"} {
			if strings.Contains(all, secret) {
				t.Fatalf("%q leaked into %s", secret, q)
			}
		}
	}
	// The searchable record is the same text the text-only importer stored, so
	// summaries made from it stay attached to the same session id.
	var content string
	if err := st.db.QueryRow(`SELECT content FROM transcripts WHERE id = ?`, pk).Scan(&content); err != nil || !strings.HasPrefix(content, "## user\n\nsynthetic question") || !strings.HasSuffix(content, "answer one") {
		t.Fatalf("content %q %v", content, err)
	}
}

func TestCodexIncrementalCursorKeepsIdentityAndCountsOnce(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	file := filepath.Join(root, "rollout-T1.jsonl")
	writeLines(t, file, cMeta, cCtx, cUser, cAsst, cTC1)
	codexPass(t, st, root, nil)
	pk := sessionPK(codexReg, "codex", "T1", "rollout/rollout-T1.jsonl")

	// Nothing appended: nothing to do.
	if out, _, _ := codexPass(t, st, root, nil); len(out) != 0 {
		t.Fatalf("unchanged file produced %v", out)
	}
	// A repeat of the same count, a new answer with its own count, and a half-written line.
	appendRaw(t, file, cTC1+"\n"+cAsst2+"\n"+cTC2+"\n"+`{"timestamp":"2026-10-12T11:00:02Z","type":"resp`)
	out, skips, _ := codexPass(t, st, root, nil)
	if len(out) != 1 || skips["incomplete_tail"] != 1 {
		t.Fatalf("append: %v %v", out, skips)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tx_turns WHERE session_pk = ?`, pk); n != 3 {
		t.Fatalf("turns = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tx_usage WHERE session_pk = ?`, pk); n != 2 {
		t.Fatalf("usage rows = %d (a repeated count must merge)", n)
	}
	var cwd string
	if err := st.db.QueryRow(`SELECT cwd FROM tx_sessions WHERE session_pk = ?`, pk).Scan(&cwd); err != nil || cwd != "/synthetic/proj" {
		t.Fatalf("identity lost on append: %q %v", cwd, err)
	}
	// The half line completes; only it is read.
	appendRaw(t, file, `onse_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"third"}]}}`+"\n")
	if out, skips, _ := codexPass(t, st, root, nil); len(out) != 1 || skips["incomplete_tail"] != 0 {
		t.Fatalf("completed tail: %v %v", out, skips)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tx_turns WHERE session_pk = ?`, pk); n != 4 {
		t.Fatalf("turns = %d", n)
	}
	// A rewritten file is read again from the start and replaces the old rows.
	writeLines(t, file, cMeta, cUser)
	codexPass(t, st, root, nil)
	if n := count(t, st, `SELECT COUNT(*) FROM tx_turns WHERE session_pk = ?`, pk); n != 1 {
		t.Fatalf("rewrite kept stale turns: %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tx_usage WHERE session_pk = ?`, pk); n != 0 {
		t.Fatalf("rewrite kept stale usage: %d", n)
	}
}

func TestCodexPerResponseUsageRecordsBeatRepeatedCounts(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	rec := func(id string, in, cached, out int) string {
		return `{"timestamp":"2026-10-12T10:00:09Z","type":"token_usage_record","payload":{"response_id":"` + id + `","usage":{"input_tokens":` + strconv.Itoa(in) + `,"cached_input_tokens":` + strconv.Itoa(cached) + `,"output_tokens":` + strconv.Itoa(out) + `,"total_tokens":` + strconv.Itoa(in+out) + `}}}`
	}
	writeLines(t, filepath.Join(root, "rollout-T1.jsonl"), cMeta, cUser, cAsst, rec("resp-1", 100, 40, 10), cTC1, rec("resp-2", 300, 250, 20), cTC2)
	codexPass(t, st, root, nil)
	pk := sessionPK(codexReg, "codex", "T1", "rollout/rollout-T1.jsonl")
	var n, in, out int64
	if err := st.db.QueryRow(`SELECT COUNT(*), SUM(input_tokens), SUM(output_tokens) FROM tx_usage WHERE session_pk = ?`, pk).Scan(&n, &in, &out); err != nil {
		t.Fatal(err)
	}
	if n != 2 || in != 60+50 || out != 30 {
		t.Fatalf("usage %d rows in=%d out=%d", n, in, out)
	}
}

func TestCodexSubagentAndForkAncestry(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	child := `{"timestamp":"2026-10-12T12:00:00Z","type":"session_meta","payload":{"id":"T2","timestamp":"2026-10-12T12:00:00Z","cwd":"/synthetic/proj","source":{"subagent":{"thread_spawn":{"parent_thread_id":"T1","depth":1,"agent_role":"explorer","agent_nickname":"Kant"}}},"parent_thread_id":"T1"}}`
	// The child's file sorts first, so its parent is not stored when it arrives.
	writeLines(t, filepath.Join(root, "a-child.jsonl"), child, cUser)
	writeLines(t, filepath.Join(root, "b-parent.jsonl"), cMeta, cUser, cAsst)
	codexPass(t, st, root, nil)
	parentPK := sessionPK(codexReg, "codex", "T1", "rollout/b-parent.jsonl")
	childPK := sessionPK(codexReg, "codex", "T2", "rollout/a-child.jsonl")
	var kind, agent, nativeParent string
	if err := st.db.QueryRow(`SELECT kind, agent_type, parent_native_id FROM tx_sessions WHERE session_pk = ?`, childPK).Scan(&kind, &agent, &nativeParent); err != nil || kind != "subagent" || agent != "explorer" || nativeParent != "T1" {
		t.Fatalf("child %s %s %s %v", kind, agent, nativeParent, err)
	}
	// A parent that arrives later is found by native id when the child is read again.
	appendRaw(t, filepath.Join(root, "a-child.jsonl"), cAsst+"\n")
	codexPass(t, st, root, nil)
	var got string
	if err := st.db.QueryRow(`SELECT parent_session_pk FROM tx_sessions WHERE session_pk = ?`, childPK).Scan(&got); err != nil || got != parentPK {
		t.Fatalf("parent pk %q want %q (%v)", got, parentPK, err)
	}
	// Only mains are enrichment candidates.
	refs, err := st.MainSessions(context.Background(), 0, time.Time{}, "")
	if err != nil || len(refs) != 1 || refs[0].NativeSessionID != "T1" {
		t.Fatalf("main sessions %+v %v", refs, err)
	}
}
