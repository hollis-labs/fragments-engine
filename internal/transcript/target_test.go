package transcript

import (
	"context"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestToolTarget(t *testing.T) {
	for _, c := range []struct{ name, input, want string }{
		{"Read", `{"file_path":"/repo/a.go"}`, "/repo/a.go"},
		{"Edit", `{"file_path":"/repo/b.go","old_string":"x"}`, "/repo/b.go"},
		{"Bash", `{"command":"go test ./...\necho done"}`, "go test ./..."},
		{"exec_command", `{"cmd":["git","status"]}`, "git status"},
		{"WebFetch", `{"url":"https://example.com/a/b?x=1"}`, "example.com"},
		{"WebSearch", `{"query":"sqlite fts5"}`, "sqlite fts5"},
		{"Grep", `{"pattern":"TODO","path":"src"}`, "TODO in src"},
		{"Task", `{"description":"audit the cache"}`, "audit the cache"},
		{"mcp__thing", `{"path":"/x"}`, "/x"},
		{"Read", `{"nothing":"here"}`, ""},
		{"Read", ``, ""},
		{"Read", `not json`, ""},
	} {
		if got := toolTarget(c.name, c.input); got != c.want {
			t.Errorf("%s %s: %q want %q", c.name, c.input, got, c.want)
		}
	}
	if got := toolTarget("Bash", `{"command":"`+strings.Repeat("é", 300)+`"}`); len(got) > maxTargetBytes || !strings.HasPrefix(got, "é") {
		t.Errorf("target not bounded on a rune boundary: %d", len(got))
	}
}

func TestBackfillTargetsIsIdempotent(t *testing.T) {
	st := openStore(t)
	if _, err := st.db.Exec(`INSERT INTO tx_tool_calls (tool_use_id, session_pk, name, input_redacted) VALUES ('t1','s1','Read','{"file_path":"/a"}'), ('t2','s1','Read','')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		conn, err := st.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		err = backfillTargets(context.Background(), conn)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	var a, b string
	_ = st.db.QueryRow(`SELECT target_redacted FROM tx_tool_calls WHERE tool_use_id='t1'`).Scan(&a)
	_ = st.db.QueryRow(`SELECT target_redacted FROM tx_tool_calls WHERE tool_use_id='t2'`).Scan(&b)
	if a != "/a" || b != "" {
		t.Fatalf("%q %q", a, b)
	}
}

func TestExcerptPolicyKeepsErrorsOnly(t *testing.T) {
	d := &domain.TranscriptDetail{Tools: []domain.TranscriptToolCall{
		{ToolUseID: "ok", HasResult: true, ResultExcerpt: "fine", MaxExcerpt: 1024},
		{ToolUseID: "bad", HasResult: true, IsError: true, ResultExcerpt: "boom", MaxExcerpt: 1024},
	}}
	d.ApplyExcerptPolicy(0, 1024)
	if d.Tools[0].ResultExcerpt != "" || d.Tools[0].MaxExcerpt != 0 || d.Tools[1].ResultExcerpt != "boom" || d.Tools[1].MaxExcerpt != 1024 {
		t.Fatalf("%+v", d.Tools)
	}
	d.Tools[0].ResultExcerpt, d.Tools[0].MaxExcerpt = "fine", 1024
	d.ApplyExcerptPolicy(0, 0)
	if d.Tools[0].ResultExcerpt != "" || d.Tools[1].ResultExcerpt != "" {
		t.Fatal("default must keep no excerpt")
	}
	if domain.ExcerptCapture(0, 512) != 512 || domain.ExcerptCapture(2048, 512) != 2048 {
		t.Fatal("capture limit")
	}
}
