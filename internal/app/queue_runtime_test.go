package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

// The queue drainer runs in processes that have no corpus credentials (the MCP server).
// It must not need the transcript store: with the Postgres store configured and its DSN
// variable empty, draining still works.
func TestDrainQueueOnceDoesNotNeedTranscriptStore(t *testing.T) {
	t.Setenv("FE_TX_DSN", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "fragments.yaml")
	cfg := config.Config{
		Database:    config.DatabaseConfig{Path: filepath.Join(dir, "fe.db")},
		Recall:      config.RecallConfig{Backend: "sqlite"},
		Queue:       config.QueueConfig{AutoDrain: true, BatchSize: 5},
		Ingests:     []config.IngestConfig{{Name: "x", Kind: "claude_code", Source: config.IngestSource{Root: dir}}},
		Transcripts: config.TranscriptConfig{Store: "postgres", Postgres: config.TranscriptPG{DSNEnv: "FE_TX_DSN"}},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := DrainQueueOnce(context.Background(), cfgPath); err != nil {
		t.Fatalf("drain: %v", err)
	}
}
