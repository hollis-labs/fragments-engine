package claude

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func writeSession(t *testing.T, lines ...string) (root, path, rel string) {
	t.Helper()
	root = t.TempDir()
	path = filepath.Join(root, "proj", "S1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, path, "proj/S1.jsonl"
}

func collect(t *testing.T, ctx context.Context, root, path, rel string) (domain.PipelineFragment, string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	f, skip, err := collectSegment(ctx, root, path, rel, info)
	if err != nil {
		t.Fatal(err)
	}
	return f, skip
}

const (
	asst = `{"type":"assistant","uuid":"a1","requestId":"r1","sessionId":"S1","timestamp":"2026-10-10T10:00:01Z","message":{"model":"m","content":[{"type":"thinking","thinking":"first I weigh the options"},{"type":"text","text":"Here is the plan"},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/x"}}]}}`
	user = `{"type":"user","uuid":"u1","sessionId":"S1","timestamp":"2026-10-10T10:00:00Z","message":{"content":"hello"}}`
)

func TestThinkingAndToolResultsAreCapturedInFull(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	long := strings.Repeat("result line\n", 5000) // 60 KB, far past the old 2 KB excerpt
	result := fmt.Sprintf(`{"type":"user","uuid":"u2","sessionId":"S1","timestamp":"2026-10-10T10:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[`+
		`{"type":"text","text":%q},{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}},{"type":"tool_reference","tool_name":"Grep"}]}]}}`,
		long, base64.StdEncoding.EncodeToString(png))
	root, path, rel := writeSession(t, user, asst, result)
	f, skip := collect(t, context.Background(), root, path, rel)
	if skip != "" {
		t.Fatalf("skip %q", skip)
	}
	var thinking string
	for _, turn := range f.Detail.Turns {
		if turn.EventUUID == "a1" {
			thinking = turn.Thinking
			if turn.Text != "Here is the plan" || !turn.HasThinking || turn.ThinkingBytes != len(thinking) {
				t.Fatalf("turn %+v", turn)
			}
		}
	}
	if thinking != "first I weigh the options" {
		t.Fatalf("thinking = %q", thinking)
	}
	call := f.Detail.Tools[0]
	if !call.HasResult || len(call.Results) != 3 {
		t.Fatalf("call %+v", call)
	}
	if call.Results[0].Text != long || call.Results[0].MediaType != "text/plain" {
		t.Errorf("text part cut: %d bytes", len(call.Results[0].Text))
	}
	if string(call.Results[1].Bytes) != string(png) || call.Results[1].MediaType != "image/png" {
		t.Errorf("image part: %+v", call.Results[1])
	}
	if !strings.Contains(call.Results[2].JSON, "tool_reference") || call.Results[2].MediaType != "application/json" {
		t.Errorf("unknown block was dropped: %+v", call.Results[2])
	}
	if call.ResultSize != len(long)+len(png)+len(call.Results[2].JSON) {
		t.Errorf("ResultSize = %d", call.ResultSize)
	}
}

func TestStringToolResultIsOnePart(t *testing.T) {
	result := `{"type":"user","uuid":"u2","sessionId":"S1","timestamp":"2026-10-10T10:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"boom: file not found"}]}}`
	root, path, rel := writeSession(t, user, asst, result)
	f, _ := collect(t, context.Background(), root, path, rel)
	c := f.Detail.Tools[0]
	if !c.IsError || len(c.Results) != 1 || c.Results[0].Text != "boom: file not found" {
		t.Fatalf("%+v", c)
	}
}

// A line over the old 10 MB limit used to be rejected as unreadable_line.
func TestVeryLongLineIsIngested(t *testing.T) {
	huge := strings.Repeat("z", 12<<20)
	result := fmt.Sprintf(`{"type":"user","uuid":"u2","sessionId":"S1","timestamp":"2026-10-10T10:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":%q}]}}`, huge)
	root, path, rel := writeSession(t, user, asst, result)
	f, skip := collect(t, context.Background(), root, path, rel)
	if skip != "" || len(f.Detail.Tools) != 1 || len(f.Detail.Tools[0].Results[0].Text) != len(huge) {
		t.Fatalf("skip=%q tools=%d", skip, len(f.Detail.Tools))
	}
}

// A file bigger than one pass is read in several, each resuming at the stored
// cursor, and nothing is skipped or read twice.
func TestLargeFileIsReadInBoundedPassesWithoutLoss(t *testing.T) {
	old := passBytes
	passBytes = 4 << 10
	defer func() { passBytes = old }()
	var lines []string
	lines = append(lines, user)
	for i := 0; i < 60; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","uuid":"a%d","sessionId":"S1","timestamp":"2026-10-10T10:00:%02dZ","message":{"content":[{"type":"text","text":%q}]}}`, i, i%60, strings.Repeat("w", 300)))
	}
	root, path, rel := writeSession(t, lines...)

	var cursor domain.TranscriptCursor
	have := false
	seen := map[string]bool{}
	passes := 0
	for {
		skips := &domain.SkipCollector{}
		ctx := domain.WithSkipCollector(context.Background(), skips)
		ctx = domain.WithCursorLookup(ctx, func(p string) (domain.TranscriptCursor, bool) { return cursor, have && p == rel })
		f, skip := collect(t, ctx, root, path, rel)
		if skip == "unchanged" {
			break
		}
		passes++
		for _, turn := range f.Detail.Turns {
			if seen[turn.EventUUID] {
				t.Fatalf("turn %s read twice", turn.EventUUID)
			}
			seen[turn.EventUUID] = true
		}
		cursor, have = f.Detail.Cursor, true
		if skips.ByClass()["batch_continues"] == 0 {
			break
		}
		if passes > 100 {
			t.Fatal("did not converge")
		}
	}
	if passes < 3 {
		t.Errorf("expected several passes, got %d", passes)
	}
	if len(seen) != 61 {
		t.Errorf("read %d turns, want 61", len(seen))
	}
}
