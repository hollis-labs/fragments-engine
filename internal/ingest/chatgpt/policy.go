package chatgpt

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func ValidateArchivePolicy(sourceRoot string, rules config.ChatGPTExportRules) ([]string, error) {
	var warnings []string
	if !rules.CopyTextExports {
		if rules.DeleteCopiedSource {
			return warnings, fmt.Errorf("delete_copied_source requires copy_text_exports=true")
		}
		return warnings, nil
	}
	if strings.TrimSpace(rules.ArchiveRoot) == "" {
		return warnings, fmt.Errorf("archive_root is required when copy_text_exports=true")
	}

	sourceAbs, err := filepath.Abs(config.ExpandHome(sourceRoot))
	if err != nil {
		return warnings, fmt.Errorf("resolve source root: %w", err)
	}
	archiveAbs, err := filepath.Abs(config.ExpandHome(rules.ArchiveRoot))
	if err != nil {
		return warnings, fmt.Errorf("resolve archive root: %w", err)
	}
	expectedBase, err := filepath.Abs(config.ExpandHome("~/Documents/corpus/ai-chat-logs/chatgpt/logs"))
	if err != nil {
		return warnings, fmt.Errorf("resolve canonical archive root: %w", err)
	}
	if archiveAbs != expectedBase && !strings.HasPrefix(archiveAbs, expectedBase+string(filepath.Separator)) {
		return warnings, fmt.Errorf("archive_root must live under %s", expectedBase)
	}
	if sourceAbs == archiveAbs || strings.HasPrefix(sourceAbs, archiveAbs+string(filepath.Separator)) {
		return warnings, fmt.Errorf("source.root must not be inside archive_root")
	}
	if strings.HasPrefix(archiveAbs, sourceAbs+string(filepath.Separator)) {
		warnings = append(warnings, "archive_root is nested under source.root; keep exports and archive trees separate")
	}
	return warnings, nil
}
