package domain

import "strings"

// IsTranscriptSource is the closed set of transcript producers. New transcript
// adapters must declare one of these names or SourceType "transcript". They
// never belong in the shared fragment/capture store or external recall.
func IsTranscriptSource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "claude", "claude_code", "chatgpt", "chatgpt_export", "codex", "codex_sessions", "antigravity", "antigravity_db", "antigravity_brain", "transcript":
		return true
	default:
		return false
	}
}

func IsTranscript(source, sourceType, provider string) bool {
	return IsTranscriptSource(source) || IsTranscriptSource(provider) || strings.EqualFold(strings.TrimSpace(sourceType), "transcript")
}
