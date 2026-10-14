package antigravity

import (
	"context"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

func pbv(field int, v uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(field<<3)), v)
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func pbts(field int, sec int64) []byte { return pb(field, cat(pbv(1, uint64(sec)), pbv(2, 500))) }

const (
	t0 = int64(1790000000) // 2026-09
)

type synthStep struct {
	idx    int
	typ    int
	status int
	uuid   string
	ts     int64
	// planner
	text, thinking string
	calls          [][3]string // id, name, args
	resp           string
	in, out, cache uint64
	model          string
	// tool step
	callID string
	isErr  bool
}

func (s synthStep) payload() []byte {
	switch s.typ {
	case 14:
		return pb(19, pb(2, []byte(s.text)))
	case 15:
		var inner []byte
		if s.text != "" {
			inner = cat(inner, pb(1, []byte(s.text)))
		}
		if s.thinking != "" {
			inner = cat(inner, pb(3, []byte(s.thinking)))
		}
		for _, c := range s.calls {
			inner = cat(inner, pb(7, cat(pb(1, []byte(c[0])), pb(2, []byte(c[1])), pb(3, []byte(c[2])))))
		}
		inner = cat(inner, pbv(12, 2))
		return pb(20, inner)
	}
	return []byte("opaque step payload that is not conversation text")
}

func (s synthStep) metadata() []byte {
	m := cat(pbts(1, s.ts), pb(12, []byte(s.uuid)))
	if s.typ == 15 && s.resp != "" {
		m = cat(m, pb(9, cat(pbv(1, 1318), pbv(2, s.in), pbv(3, s.out), pbv(5, s.cache), pb(7, []byte(s.resp)))))
	}
	if s.typ == 132 {
		m = cat(m, pb(4, pb(1, []byte(s.callID))), pbts(8, s.ts+3))
	}
	return m
}

// writeConv replaces conversations/<id>.db with a snapshot holding steps.
func writeConv(t *testing.T, root, id string, steps []synthStep, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(root, "conversations", id+".db")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		"CREATE TABLE steps(idx INTEGER PRIMARY KEY, step_type INTEGER NOT NULL DEFAULT 0, status INTEGER NOT NULL DEFAULT 0, has_subtrajectory NUMERIC NOT NULL DEFAULT 0, metadata BLOB, error_details BLOB, permissions BLOB, task_details BLOB, render_info BLOB, step_payload BLOB, step_format INTEGER NOT NULL DEFAULT 0)",
		"CREATE TABLE gen_metadata(idx INTEGER PRIMARY KEY, data BLOB, size INTEGER NOT NULL DEFAULT 0)",
		"CREATE TABLE trajectory_meta(trajectory_id TEXT PRIMARY KEY, cascade_id TEXT, trajectory_type INTEGER, source INTEGER)",
		"CREATE TABLE trajectory_metadata_blob(id TEXT DEFAULT 'main' PRIMARY KEY, data BLOB)",
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO trajectory_meta VALUES('traj-'||?, ?, 4, 17)`, id, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO trajectory_metadata_blob VALUES('main', ?)`, pb(7, []byte("/work/synthetic-project"))); err != nil {
		t.Fatal(err)
	}
	gen := 0
	for _, s := range steps {
		var errBlob any
		if s.isErr {
			errBlob = []byte("synthetic tool error")
		}
		if _, err := db.Exec(`INSERT INTO steps(idx, step_type, status, metadata, error_details, step_payload, step_format) VALUES(?,?,?,?,?,?,0)`,
			s.idx, s.typ, s.status, s.metadata(), errBlob, s.payload()); err != nil {
			t.Fatal(err)
		}
		if s.typ == 15 && s.resp != "" {
			if _, err := db.Exec(`INSERT INTO gen_metadata(idx, data) VALUES(?, ?)`, 1000+gen, pb(1, cat(pb(4, pb(7, []byte(s.resp))), pb(19, []byte(s.model))))); err != nil {
				t.Fatal(err)
			}
			gen++
		}
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func baseSteps() []synthStep {
	return []synthStep{
		{idx: 0, typ: 14, status: 3, uuid: "00000000-0000-0000-0000-000000000001", ts: t0, text: "user asks about the roadmap password=hunter2hunter2"},
		{idx: 1, typ: 15, status: 3, uuid: "00000000-0000-0000-0000-000000000001", ts: t0 + 1, text: "assistant answer", thinking: "SECRET-THINKING-TEXT-must-not-be-stored", resp: "resp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", in: 1200, out: 80, cache: 900, model: "gemini-3.8-flash",
			calls: [][3]string{{"call-1", "run_command", `{"cmd":"ls","token":"sk-synthetic-secret-token-abcdef123456"}`}, {"call-2", "view_file", `{"path":"/etc/hosts"}`}}},
		{idx: 2, typ: 132, status: 3, uuid: "00000000-0000-0000-0000-000000000003", ts: t0 + 2, callID: "call-1"},
		{idx: 3, typ: 132, status: 7, uuid: "00000000-0000-0000-0000-000000000004", ts: t0 + 2, callID: "call-2", isErr: true},
		{idx: 4, typ: 17, status: 3, uuid: "00000000-0000-0000-0000-000000000005", ts: t0 + 3},
		{idx: 5, typ: 17, status: 3, uuid: "00000000-0000-0000-0000-000000000006", ts: t0 + 3},
		{idx: 6, typ: 101, status: 3, uuid: "00000000-0000-0000-0000-000000000007", ts: t0 + 3},
	}
}

// conversationFragment returns the structured conversation fragment, ignoring
// the brain artifacts that sit beside it.
func conversationFragment(t *testing.T, got []domain.PipelineFragment) domain.PipelineFragment {
	t.Helper()
	var found []domain.PipelineFragment
	for _, f := range got {
		if f.Source == "antigravity_db" {
			found = append(found, f)
		}
	}
	if len(found) != 1 || found[0].Detail == nil {
		t.Fatalf("expected one conversation fragment with detail, got %d of %d", len(found), len(got))
	}
	return found[0]
}

func collectOne(t *testing.T, root string, ctx context.Context, rules map[string]any) ([]domain.PipelineFragment, *domain.SkipCollector) {
	t.Helper()
	skips := &domain.SkipCollector{}
	got, err := (Source{}).Collect(domain.WithSkipCollector(ctx, skips), config.IngestConfig{Name: "agy", Source: config.IngestSource{Root: root}, Rules: rules})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return got, skips
}

func TestCaptureBuildsStructuredDetail(t *testing.T) {
	root := t.TempDir()
	writeConv(t, root, "conv-1", baseSteps(), time.Unix(t0+10, 0))
	// A tool output file: size is recorded, content is not.
	outDir := filepath.Join(root, "brain", "conv-1", ".system_generated", "steps", "2")
	if err := os.MkdirAll(outDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "output.txt"), []byte("0123456789tool output body"), 0600); err != nil {
		t.Fatal(err)
	}
	got, skips := collectOne(t, root, context.Background(), nil)
	frag := conversationFragment(t, got)
	d := frag.Detail
	if d.Session.Tool != "antigravity" || d.Session.NativeSessionID != "conv-1" || d.Session.Kind != "main" || d.Session.Model != "gemini-3.8-flash" || d.Session.CWD != "/work/synthetic-project" {
		t.Fatalf("session: %+v", d.Session)
	}
	if d.Session.StartedAt.Unix() != t0 || d.Session.EndedAt.Unix() < t0+3 {
		t.Fatalf("session times: %v %v", d.Session.StartedAt, d.Session.EndedAt)
	}
	if len(d.Turns) != 2 || d.Turns[0].Role != "user" || d.Turns[1].Role != "assistant" || d.Turns[1].TS.Unix() != t0+1 {
		t.Fatalf("turns: %+v", d.Turns)
	}
	if !d.Turns[1].HasThinking || d.Turns[1].ThinkingBytes != len("SECRET-THINKING-TEXT-must-not-be-stored") || d.Turns[1].StopReason != "2" {
		t.Fatalf("thinking/stop reason: %+v", d.Turns[1])
	}
	if len(d.Usage) != 1 || d.Usage[0].Input != 1200 || d.Usage[0].Output != 80 || d.Usage[0].CacheRead != 900 || d.Usage[0].Model != "gemini-3.8-flash" || d.Usage[0].RequestID != "resp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("usage: %+v", d.Usage)
	}
	if len(d.Tools) != 2 {
		t.Fatalf("tools: %+v", d.Tools)
	}
	c1, c2 := d.Tools[0], d.Tools[1]
	if c1.ToolUseID != "call-1" || c1.Name != "run_command" || !c1.HasResult || c1.IsError || c1.ResultSize != len("0123456789tool output body") || c1.ResultExcerpt != "" {
		t.Fatalf("call-1: %+v", c1)
	}
	if c2.ToolUseID != "call-2" || c2.Name != "view_file" || !c2.HasResult || !c2.IsError || c2.ResultSize != 0 {
		t.Fatalf("call-2: %+v", c2)
	}
	if c1.TurnUUID != d.Turns[1].EventUUID || c1.StartedAt.Unix() != t0+1 || c1.EndedAt.Unix() != t0+2+3 {
		t.Fatalf("call timing/turn link: %+v", c1)
	}
	if by := skips.ByClass(); by["unsupported_step_17"] != 2 || by["unsupported_step_101"] != 1 || len(by) != 2 {
		t.Fatalf("unsupported step accounting: %v", by)
	}
	if strings.Contains(frag.Content, "SECRET-THINKING") || !strings.Contains(frag.Content, "assistant answer") {
		t.Fatalf("search text: %q", frag.Content)
	}
	if d.Cursor.ByteOffset != 7 || d.Cursor.TailDigest == "" || d.CursorPath != "conversations/conv-1.db" {
		t.Fatalf("cursor: %+v path=%q", d.Cursor, d.CursorPath)
	}
}

func TestToolResultStatusNeedsAKnownStepStatus(t *testing.T) {
	root := t.TempDir()
	steps := baseSteps()[:2]
	steps = append(steps, synthStep{idx: 2, typ: 132, status: 5, uuid: "00000000-0000-0000-0000-000000000003", ts: t0 + 2, callID: "call-1"})
	writeConv(t, root, "conv-1", steps, time.Unix(t0+10, 0))
	got, _ := collectOne(t, root, context.Background(), nil)
	for _, c := range conversationFragment(t, got).Detail.Tools {
		if c.HasResult {
			t.Fatalf("status 5 is not a known result, call %s has one", c.ToolUseID)
		}
	}
}

func TestToolResultExcerptIsOptInAndBounded(t *testing.T) {
	root := t.TempDir()
	writeConv(t, root, "conv-1", baseSteps(), time.Unix(t0+10, 0))
	outDir := filepath.Join(root, "brain", "conv-1", ".system_generated", "steps", "2")
	if err := os.MkdirAll(outDir, 0700); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", 5000)
	if err := os.WriteFile(filepath.Join(outDir, "output.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	got, _ := collectOne(t, root, context.Background(), map[string]any{"tool_result_excerpt_bytes": 99999})
	c := conversationFragment(t, got).Detail.Tools[0]
	if c.MaxExcerpt != maxExcerpt || len(c.ResultExcerpt) != maxExcerpt || c.ResultSize != 5000 {
		t.Fatalf("excerpt cap: max=%d len=%d size=%d", c.MaxExcerpt, len(c.ResultExcerpt), c.ResultSize)
	}
}

// privateStore opens a throwaway owner-private store.
func privateStore(t *testing.T) (*transcript.Store, string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "private")
	st, err := transcript.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dir
}

func runThroughStore(t *testing.T, st *transcript.Store, root string) (inserted, updated, skipped int, by map[string]int) {
	t.Helper()
	ctx := domain.WithCursorLookup(context.Background(), func(path string) (domain.TranscriptCursor, bool) {
		return st.Cursor(context.Background(), "agy", path)
	})
	got, skips := collectOne(t, root, ctx, nil)
	for _, f := range got {
		out, err := st.Accept(context.Background(), "agy", f)
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		switch out {
		case transcript.Inserted:
			inserted++
		case transcript.Updated:
			updated++
		default:
			skipped++
		}
	}
	return inserted, updated, skipped, skips.ByClass()
}

func counts(t *testing.T, dir string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "transcripts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string]int{}
	for _, table := range []string{"transcripts", "tx_sessions", "tx_turns", "tx_tool_calls", "tx_usage", "tx_cursors"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func TestCaptureIsIdempotentAndIncremental(t *testing.T) {
	st, dir := privateStore(t)
	root := t.TempDir()
	steps := baseSteps()
	writeConv(t, root, "conv-1", steps, time.Unix(t0+10, 0))

	ins, upd, skip, _ := runThroughStore(t, st, root)
	if ins != 1 || upd != 0 || skip != 0 {
		t.Fatalf("first run: %d %d %d", ins, upd, skip)
	}
	first := counts(t, dir)
	if first["tx_turns"] != 2 || first["tx_tool_calls"] != 2 || first["tx_usage"] != 1 || first["tx_cursors"] != 1 || first["tx_sessions"] != 1 {
		t.Fatalf("first counts: %v", first)
	}

	// Same snapshot again: nothing is read, nothing changes.
	ins, upd, skip, by := runThroughStore(t, st, root)
	if ins+upd+skip != 0 || by["unchanged"] != 1 {
		t.Fatalf("repeat: %d %d %d %v", ins, upd, skip, by)
	}
	if got := counts(t, dir); !equalCounts(got, first) {
		t.Fatalf("repeat changed rows: %v vs %v", got, first)
	}

	// A new snapshot with appended steps (a user turn and an assistant turn with a
	// tool result for an old call): only the new steps are parsed.
	more := append(append([]synthStep{}, steps...),
		synthStep{idx: 7, typ: 14, status: 3, uuid: "00000000-0000-0000-0000-000000000008", ts: t0 + 20, text: "second question"},
		synthStep{idx: 8, typ: 15, status: 3, uuid: "00000000-0000-0000-0000-000000000009", ts: t0 + 21, text: "second answer", resp: "resp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", in: 50, out: 5, cache: 10, model: "gemini-3.8-flash"},
	)
	writeConv(t, root, "conv-1", more, time.Unix(t0+30, 0))
	ins, upd, skip, by = runThroughStore(t, st, root)
	if ins != 0 || upd != 1 {
		t.Fatalf("append run: %d %d %d", ins, upd, skip)
	}
	if by["unsupported_step_17"] != 0 {
		t.Fatalf("incremental pass re-read old steps: %v", by)
	}
	appended := counts(t, dir)
	if appended["tx_turns"] != 4 || appended["tx_usage"] != 2 || appended["tx_tool_calls"] != 2 || appended["tx_sessions"] != 1 {
		t.Fatalf("append counts: %v", appended)
	}

	// Unchanged again, then a full re-read from scratch gives the same totals.
	if ins, upd, skip, _ = runThroughStore(t, st, root); ins+upd+skip != 0 {
		t.Fatalf("repeat after append: %d %d %d", ins, upd, skip)
	}
	st2, dir2 := privateStore(t)
	runThroughStore(t, st2, root)
	if full := counts(t, dir2); !equalCounts(full, appended) {
		t.Fatalf("incremental result differs from a full parse: %v vs %v", appended, full)
	}
}

func equalCounts(a, b map[string]int) bool {
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return len(a) == len(b)
}

func TestRewrittenSnapshotResetsDerivedRows(t *testing.T) {
	st, dir := privateStore(t)
	root := t.TempDir()
	steps := baseSteps()
	writeConv(t, root, "conv-1", steps, time.Unix(t0+10, 0))
	runThroughStore(t, st, root)

	// The producer re-created the conversation: earlier steps now have other ids.
	rewritten := []synthStep{
		{idx: 0, typ: 14, status: 3, uuid: "11111111-0000-0000-0000-000000000001", ts: t0 + 100, text: "a different first question"},
		{idx: 1, typ: 15, status: 3, uuid: "11111111-0000-0000-0000-000000000002", ts: t0 + 101, text: "a different answer", resp: "resp-cccccccccccccccccccccccccccccccccccc", in: 7, out: 3, cache: 1, model: "gemini-pro-default"},
	}
	for i := 2; i < 9; i++ {
		rewritten = append(rewritten, synthStep{idx: i, typ: 17, status: 3, uuid: "11111111-0000-0000-0000-00000000010" + string(rune('0'+i)), ts: t0 + 102})
	}
	writeConv(t, root, "conv-1", rewritten, time.Unix(t0+200, 0))
	if ins, upd, _, _ := runThroughStore(t, st, root); ins != 0 || upd != 1 {
		t.Fatalf("reset run: %d %d", ins, upd)
	}
	c := counts(t, dir)
	if c["tx_turns"] != 2 || c["tx_tool_calls"] != 0 || c["tx_usage"] != 1 || c["tx_sessions"] != 1 {
		t.Fatalf("a rewritten snapshot must replace derived rows, got %v", c)
	}
}

func TestRunningTailIsReadAgain(t *testing.T) {
	st, dir := privateStore(t)
	root := t.TempDir()
	steps := baseSteps()[:3]
	// call-2 has no result yet and the last step is still running (status 2).
	steps = append(steps, synthStep{idx: 3, typ: 132, status: 2, uuid: "00000000-0000-0000-0000-000000000004", ts: t0 + 2, callID: "call-2"})
	writeConv(t, root, "conv-1", steps, time.Unix(t0+10, 0))
	runThroughStore(t, st, root)
	cur, ok := st.Cursor(context.Background(), "agy", "conversations/conv-1.db")
	if !ok || cur.ByteOffset != 3 {
		t.Fatalf("a running tail step must stay unconsumed: %+v ok=%v", cur, ok)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "transcripts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRow(`SELECT result_status FROM tx_tool_calls WHERE tool_use_id = 'call-2'`).Scan(&status); err != nil || status != "none" {
		t.Fatalf("call-2 before completion: %q %v", status, err)
	}

	// The producer syncs the finished step: same idx, now done with a result.
	steps[3].status = 3
	writeConv(t, root, "conv-1", steps, time.Unix(t0+60, 0))
	runThroughStore(t, st, root)
	if err := db.QueryRow(`SELECT result_status FROM tx_tool_calls WHERE tool_use_id = 'call-2'`).Scan(&status); err != nil || status != "ok" {
		t.Fatalf("call-2 after completion: %q %v", status, err)
	}
	if c := counts(t, dir); c["tx_turns"] != 2 || c["tx_tool_calls"] != 2 {
		t.Fatalf("re-reading the tail duplicated rows: %v", c)
	}
}

func TestEveryStoredStringIsRedactedAndThinkingNeverStored(t *testing.T) {
	st, dir := privateStore(t)
	root := t.TempDir()
	writeConv(t, root, "conv-1", baseSteps(), time.Unix(t0+10, 0))
	runThroughStore(t, st, root)
	db, err := sql.Open("sqlite", filepath.Join(dir, "transcripts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"transcripts", "tx_sessions", "tx_turns", "tx_tool_calls", "tx_usage"} {
		rows, err := db.Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				var s string
				switch x := v.(type) {
				case string:
					s = x
				case []byte:
					s = string(x)
				}
				for _, secret := range []string{"hunter2hunter2", "sk-synthetic-secret-token-abcdef123456", "SECRET-THINKING"} {
					if strings.Contains(s, secret) {
						t.Fatalf("%s.%s holds %q", table, cols[i], secret)
					}
				}
			}
		}
		rows.Close()
	}
}

func TestPendingWALStillSkipsAStructuredDatabase(t *testing.T) {
	root := t.TempDir()
	path := writeConv(t, root, "conv-1", baseSteps(), time.Unix(t0+10, 0))
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; INSERT INTO steps(idx, step_type, step_payload) VALUES(99, 14, x'00')`); err != nil {
		t.Fatal(err)
	}
	got, skips := collectOne(t, root, context.Background(), nil)
	if len(got) != 0 || skips.ByClass()["pending_wal"] != 1 {
		t.Fatalf("pending WAL must skip the database: %d %v", len(got), skips.ByClass())
	}
}

func TestLargeConversationIsReadInBoundedBatches(t *testing.T) {
	old := maxBatchSteps
	maxBatchSteps = 3
	defer func() { maxBatchSteps = old }()

	st, dir := privateStore(t)
	root := t.TempDir()
	var steps []synthStep
	for i := 0; i < 10; i++ {
		typ := 14
		if i%2 == 1 {
			typ = 15
		}
		s := synthStep{idx: i, typ: typ, status: 3, uuid: "same-uuid-for-every-step", ts: t0 + int64(i), text: "turn " + string(rune('a'+i))}
		if typ == 15 {
			s.resp, s.in, s.out, s.model = "resp-"+string(rune('a'+i))+"-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 10, 2, "gemini-3.8-flash"
		}
		steps = append(steps, s)
	}
	writeConv(t, root, "conv-big", steps, time.Unix(t0+10, 0))

	var turnsAfter []int
	for pass := 1; pass <= 6; pass++ {
		_, _, _, by := runThroughStore(t, st, root)
		turnsAfter = append(turnsAfter, counts(t, dir)["tx_turns"])
		if by["unchanged"] == 1 {
			break
		}
		if pass <= 3 && by["batch_continues"] != 1 {
			t.Fatalf("pass %d should continue: %v", pass, by)
		}
	}
	want := []int{3, 6, 9, 10, 10}
	if len(turnsAfter) < 4 || turnsAfter[0] != want[0] || turnsAfter[1] != want[1] || turnsAfter[2] != want[2] || turnsAfter[3] != want[3] {
		t.Fatalf("turns after each pass: %v, want prefix %v", turnsAfter, want)
	}
	c := counts(t, dir)
	if c["tx_turns"] != 10 || c["tx_usage"] != 5 || c["tx_sessions"] != 1 || c["tx_cursors"] != 1 {
		t.Fatalf("final counts: %v", c)
	}
	cur, _ := st.Cursor(context.Background(), "agy", "conversations/conv-big.db")
	if cur.ByteOffset != 10 || cur.FileSize == 0 {
		t.Fatalf("a finished conversation must record the real file size and offset 10: %+v", cur)
	}
}
