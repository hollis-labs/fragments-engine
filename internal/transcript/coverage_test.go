package transcript

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCoverageReportsMissingByToolAndHost(t *testing.T) {
	files := []CoverageFile{
		{Tool: "claude", Host: "a", ID: "s1"}, {Tool: "claude", Host: "a", ID: "s2"},
		{Tool: "claude", Host: "a", ID: "s2"}, {Tool: "claude", Host: "b", ID: ""},
		{Tool: "codex", Host: "a", ID: "c1"},
	}
	stored := map[string]map[string]bool{"claude": {"s1": true}, "codex": {"c1": true}}
	rows := Coverage(files, stored, time.Minute)
	want := []CoverageRow{
		{Tool: "claude", Host: "a", Files: 3, InStore: 1, Missing: []string{"s2"}},
		{Tool: "claude", Host: "b", Files: 1, Unidentified: 1, Missing: []string{}},
		{Tool: "codex", Host: "a", Files: 1, InStore: 1, Missing: []string{}},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %+v", rows)
	}
	if !Drifted(rows) || Drifted(rows[1:]) {
		t.Fatal("drift detection wrong")
	}
}

func TestCoverageTreatsRecentFilesAsPending(t *testing.T) {
	files := []CoverageFile{{Tool: "claude", Host: "a", ID: "new", Age: time.Second}, {Tool: "claude", Host: "a", ID: "old", Age: time.Hour}}
	rows := Coverage(files, nil, time.Minute)
	if rows[0].Pending != 1 || !reflect.DeepEqual(rows[0].Missing, []string{"old"}) {
		t.Fatalf("row = %+v", rows[0])
	}
}

func TestScanClaudeFilesUsesContentIDAndSkipsSubagents(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("projects/p/file-name.jsonl", `{"type":"user"}`+"\n"+`{"sessionId":"real-id"}`+"\n")
	write("projects/p/partial.jsonl", `{"sessionId":"x"}`) // unterminated: no id yet
	write("projects/p/real/subagents/a.jsonl", `{"sessionId":"sub"}`+"\n")
	files, err := ScanClaudeFiles(root, "h")
	if err != nil || len(files) != 2 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = f.ID
	}
	if got["projects/p/file-name.jsonl"] != "real-id" || got["projects/p/partial.jsonl"] != "" {
		t.Fatalf("ids = %v", got)
	}
}
