package antigravity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func pb(field int, value []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(field<<3|2))
	out = binary.AppendUvarint(out, uint64(len(value)))
	return append(out, value...)
}

func fixtureDB(t *testing.T, root string, format int, user []byte) string {
	t.Helper()
	path := filepath.Join(root, "conversations", "synthetic.db")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE steps(idx INTEGER,step_type INTEGER,step_format INTEGER,step_payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		index, kind int
		payload     []byte
	}{
		{2, 15, pb(20, append(pb(1, []byte("assistant roadmap")), pb(3, []byte("omitted thinking"))...))},
		{0, 14, user},
		{1, 132, []byte("opaque tool bytes, not conversation text")},
	} {
		if _, err := db.Exec(`INSERT INTO steps VALUES(?,?,?,?)`, step.index, step.kind, format, step.payload); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestCollectConversationsAndBrainReadOnly(t *testing.T) {
	root := t.TempDir()
	dbPath := fixtureDB(t, root, 0, pb(19, pb(2, []byte("user roadmap password=synthetic-secret"))))
	brainPath := filepath.Join(root, "brain", "synthetic", "notes.md")
	if err := os.MkdirAll(filepath.Dir(brainPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brainPath, []byte("brain roadmap token=synthetic-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Source{}).Collect(context.Background(), config.IngestConfig{Source: config.IngestSource{Root: root}})
	if err != nil || len(got) != 2 {
		t.Fatalf("collect: %d %v", len(got), err)
	}
	if got[0].Source != "antigravity_db" || got[1].Source != "antigravity_brain" || got[0].SourceType != "transcript" || got[1].SourceType != "transcript" {
		t.Fatal("private transcript classification missing")
	}
	if !strings.Contains(got[0].Content, "## user") || strings.Contains(got[0].Content, "thinking") || strings.Contains(got[0].Content, "opaque tool") || strings.Index(got[0].Content, "user roadmap") > strings.Index(got[0].Content, "assistant roadmap") {
		t.Fatal("typed conversation extraction/order")
	}
	if got[0].SourceIdentity.SegmentKey != "conversations/synthetic.db" || got[1].SourceIdentity.SegmentKey != "brain/synthetic/notes.md" {
		t.Fatal("physical identities missing")
	}
	after, err := os.ReadFile(dbPath)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("source database changed")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(dbPath + suffix); !os.IsNotExist(err) {
			t.Fatal("source sidecar created")
		}
	}
}

func TestTypedPayloadsAndMalformedRefusal(t *testing.T) {
	items := append(pb(3, pb(1, []byte("first item"))), pb(3, pb(1, []byte("second item")))...)
	for _, tc := range []struct {
		name, want string
		kind       int
		raw        []byte
		fail       bool
	}{
		{"user-response", "response", 14, pb(19, append(pb(1, []byte("query")), pb(2, []byte("response"))...)), false},
		{"items", "first item\nsecond item", 14, pb(19, items), false},
		{"query", "query", 14, pb(19, pb(1, []byte("query"))), false},
		{"assistant", "answer", 15, pb(20, pb(1, []byte("answer"))), false},
		{"tool-only-planner", "", 15, pb(20, pb(7, []byte("tool"))), false},
		{"missing-typed-field", "", 14, pb(1, []byte("plausible text is not a user field")), true},
		{"wrong-envelope-wire", "", 14, []byte{0x98, 1, 1}, true},
		{"wrong-text-wire", "", 14, pb(19, []byte{0x10, 1}), true},
		{"truncated", "", 14, []byte{0x9a, 1, 10, 1}, true},
		{"overflow", "", 14, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 1}, true},
		{"group", "", 14, []byte{0x0b}, true},
		{"invalid-utf8", "", 14, pb(19, pb(2, []byte{0xff})), true},
		{"nul", "", 14, pb(19, pb(2, []byte{'a', 0})), true},
		{"duplicate-text", "", 14, pb(19, append(pb(2, []byte("a")), pb(2, []byte("b"))...)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := stepText(tc.kind, tc.raw)
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("typed extraction: %q %v", got, err)
			}
		})
	}
}

func TestUnsupportedFormatAndSchemaRefuse(t *testing.T) {
	root := t.TempDir()
	fixtureDB(t, root, 1, pb(19, pb(2, []byte("synthetic"))))
	if _, err := (Source{}).Collect(context.Background(), config.IngestConfig{Source: config.IngestSource{Root: root}}); !errors.Is(err, errPayload) {
		t.Fatalf("unknown format: %v", err)
	}
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "conversations"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conversations", "invalid.db"), []byte("synthetic secret payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Source{}).Collect(context.Background(), config.IngestConfig{Source: config.IngestSource{Root: root}}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("unsupported schema must refuse without payload diagnostics")
	}
}

func TestPendingWALRefusedWithoutCheckpoint(t *testing.T) {
	root := t.TempDir()
	path := fixtureDB(t, root, 0, pb(19, pb(2, []byte("synthetic"))))
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; INSERT INTO steps VALUES(3,14,0,x'00')`); err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(path + "-wal")
	if err != nil || len(wal) == 0 {
		t.Fatal("fixture did not create pending WAL")
	}
	if _, err := (Source{}).Collect(context.Background(), config.IngestConfig{Source: config.IngestSource{Root: root}}); !errors.Is(err, ErrSnapshotRequired) {
		t.Fatalf("pending WAL ignored: %v", err)
	}
	after, err := os.ReadFile(path + "-wal")
	if err != nil || sha256.Sum256(wal) != sha256.Sum256(after) {
		t.Fatal("reader modified WAL")
	}
}

func TestBrainBoundsSymlinksAndPartialTransfers(t *testing.T) {
	root := t.TempDir()
	brainRoot := filepath.Join(root, "brain")
	if err := os.MkdirAll(filepath.Join(brainRoot, ".rsync-partial"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"valid.jsonl": "{\"text\":\"synthetic\"}", ".rsync-partial/bad.txt": "\x00", "large.txt": strings.Repeat("x", 1024*1024+1), "opaque.bin": "\x00"} {
		if err := os.WriteFile(filepath.Join(brainRoot, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(brainRoot, "valid.jsonl"), filepath.Join(brainRoot, "link.txt")); err != nil {
		t.Fatal(err)
	}
	cfg := config.IngestConfig{Source: config.IngestSource{Root: root}, Rules: map[string]any{"max_file_size_mb": 1}}
	got, err := (Source{}).Collect(context.Background(), cfg)
	if err != nil || len(got) != 1 {
		t.Fatalf("bounds/discovery: %d %v", len(got), err)
	}
	if err := os.WriteFile(filepath.Join(brainRoot, "invalid.txt"), []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Source{}).Collect(context.Background(), cfg); err == nil {
		t.Fatal("binary text was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Source{}).Collect(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestCheckpointedWALDatabaseSupported(t *testing.T) {
	root := t.TempDir()
	path := fixtureDB(t, root, 0, pb(19, pb(2, []byte("synthetic checkpointed text"))))
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil || len(before) < 20 || before[18] != 2 || before[19] != 2 {
		t.Fatal("fixture is not checkpointed WAL format")
	}
	got, err := (Source{}).Collect(context.Background(), config.IngestConfig{Source: config.IngestSource{Root: root}})
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Content, "checkpointed text") {
		t.Fatalf("checkpointed WAL read: %d %v", len(got), err)
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("checkpointed source changed")
	}
}
