package transcript

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestRedactCredentials(t *testing.T) {
	cases := []struct{ name, input, secret string }{
		{"env", `OPENAI_API_KEY="a-synthetic-value"`, "a-synthetic-value"},
		{"json", `{"password":"synthetic-password"}`, "synthetic-password"},
		{"query", "https://example.invalid/?access_token=synthetic-query&ok=1", "synthetic-query"},
		{"bearer", "Authorization: Bearer synthetic-bearer", "synthetic-bearer"},
		{"basic", "Basic c3ludGhldGljOnNlY3JldA==", "c3ludGhldGljOnNlY3JldA=="},
		{"cookie", "Cookie: session=synthetic-cookie", "synthetic-cookie"},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nsynthetic-key-bytes\n-----END RSA PRIVATE KEY-----", "synthetic-key-bytes"},
		{"incomplete pem", "-----BEGIN PRIVATE KEY-----\nsynthetic-partial-key", "synthetic-partial-key"},
		{"openai", "sk-proj-syntheticCredentialValue", "sk-proj-syntheticCredentialValue"},
		{"github", "github_pat_syntheticCredentialValue", "github_pat_syntheticCredentialValue"},
		{"slack", "xoxb-synthetic-token-value", "xoxb-synthetic-token-value"},
		{"aws", "AKIAAAAAAAAAAAAAAAAA", "AKIAAAAAAAAAAAAAAAAA"},
		{"jwt", "eyJzeW50aGV0aWM.eyJ0b2tlbiI.c2lnbmF0dXJl", "eyJzeW50aGV0aWM.eyJ0b2tlbiI.c2lnbmF0dXJl"},
		{"url credential", "https://synthetic-user:synthetic-pass@example.invalid", "synthetic-pass"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact("safe prose\n" + tc.input + "\n")
			if strings.Contains(got, tc.secret) || !strings.Contains(got, redacted) || !strings.Contains(got, "safe prose") {
				t.Fatalf("credential was not redacted while preserving prose")
			}
			if Redact(got) != got {
				t.Fatal("redaction must be idempotent")
			}
		})
	}
	if Redact("token_count=42; roadmap and design") != "token_count=42; roadmap and design" {
		t.Fatal("ordinary text changed")
	}
}

func TestPrivateAcceptanceRedactsMaterialAndIndex(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := domain.PipelineFragment{Source: "claude", SourceID: "session-1", Title: "roadmap API_KEY=synthetic-title", Content: "roadmap password=synthetic-content", Metadata: map[string]any{
		"apiKey": "synthetic-metadata", "nested": []any{map[string]any{"message": "Bearer synthetic-nested"}},
	}, SourceIdentity: domain.SourceIdentity{SourceLocator: "https://user:synthetic-locator@example.invalid"}}
	for _, want := range []Outcome{Inserted, Skipped} {
		got, err := st.Accept(context.Background(), "configured-source", in)
		if err != nil || got != want {
			t.Fatalf("accept=%s err=%v", got, err)
		}
	}
	if in.Metadata["apiKey"] != "synthetic-metadata" || !strings.Contains(in.Content, "synthetic-content") {
		t.Fatal("source material was mutated")
	}
	var material, title, content string
	if err := st.db.QueryRow(`SELECT material_json,title,content FROM transcripts`).Scan(&material, &title, &content); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"synthetic-title", "synthetic-content", "synthetic-metadata", "synthetic-nested", "synthetic-locator"} {
		if strings.Contains(material+title+content, secret) {
			t.Fatal("raw credential persisted")
		}
	}
	var count int
	if err := st.db.QueryRow(`SELECT count(*) FROM transcript_search WHERE transcript_search MATCH 'roadmap'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("safe local search=%d %v", count, err)
	}
	if err := st.db.QueryRow(`SELECT count(*) FROM transcript_search WHERE transcript_search MATCH 'synthetic'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("credential indexed=%d %v", count, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "transcripts.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("synthetic-content")) {
		t.Fatal("raw credential reached database bytes")
	}
	in.Content = "roadmap revised password=synthetic-content"
	if got, err := st.Accept(context.Background(), "configured-source", in); err != nil || got != Updated {
		t.Fatalf("update=%s %v", got, err)
	}
	if err := st.db.QueryRow(`SELECT count(*) FROM transcript_search`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("index update duplicated %d %v", count, err)
	}
}

func TestPrivateStoreRefusesUnsafeDestinations(t *testing.T) {
	for _, kind := range []string{"public directory", "symlink directory", "public database", "symlink database", "hardlinked database", "writable ancestor", "foreign database", "symlink journal"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "private")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "public directory":
				if err := os.Chmod(root, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink directory":
				root = filepath.Join(parent, "linked")
				if err := os.Symlink(filepath.Join(parent, "private"), root); err != nil {
					t.Fatal(err)
				}
			case "public database":
				if err := os.WriteFile(filepath.Join(root, "transcripts.db"), nil, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink database":
				target := filepath.Join(parent, "target")
				if err := os.WriteFile(target, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, "transcripts.db")); err != nil {
					t.Fatal(err)
				}
			case "hardlinked database":
				target := filepath.Join(parent, "target")
				if err := os.WriteFile(target, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(target, filepath.Join(root, "transcripts.db")); err != nil {
					t.Fatal(err)
				}
			case "writable ancestor":
				// /tmp is an exposed sticky parent, unlike this session's
				// owner-only enclosing cache. Create just a synthetic fixture.
				exposed, err := os.MkdirTemp("/tmp", "fe-unsafe-parent-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(exposed) })
				if err := os.Chmod(exposed, 0777); err != nil {
					t.Fatal(err)
				}
				root = filepath.Join(exposed, "private")
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink journal":
				if err := os.WriteFile(filepath.Join(root, "transcripts.db"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(parent, "untouched")
				if err := os.WriteFile(target, []byte("synthetic"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, "transcripts.db-journal")); err != nil {
					t.Fatal(err)
				}
			case "foreign database":
				db, err := sql.Open("sqlite", filepath.Join(root, "transcripts.db"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TABLE raw_material(content TEXT)`); err != nil {
					t.Fatal(err)
				}
				db.Close()
				if err := os.Chmod(filepath.Join(root, "transcripts.db"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if st, err := Open(root); err == nil {
				st.Close()
				t.Fatal("unsafe destination accepted")
			}
		})
	}
}

func TestPrivateStoreChecksOwnershipAgainAndRefusesOpaqueMaterial(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := domain.PipelineFragment{Source: "claude", SourceID: "session", Content: "safe"}
	in.Attachments = []domain.PipelineAttachment{{Name: "raw.bin"}}
	if _, err := st.Accept(context.Background(), "source", in); !errors.Is(err, ErrAttachments) {
		t.Fatal("raw attachment accepted")
	}
	in.Attachments = nil
	in.Metadata = map[string]any{"bytes": []byte("synthetic-opaque-secret")}
	if _, err := st.Accept(context.Background(), "source", in); err == nil {
		t.Fatal("opaque bytes accepted")
	}
	in.Metadata = nil
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Accept(context.Background(), "source", in); err == nil {
		t.Fatal("ownership change accepted")
	}
	var count int
	if err := st.db.QueryRow(`SELECT count(*) FROM transcripts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed acceptance wrote %d %v", count, err)
	}
}
