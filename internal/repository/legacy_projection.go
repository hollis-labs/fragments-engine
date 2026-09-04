package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

// LegacyProjectionWrite contains only the mutable compatibility fields an old
// transport expects. Stable identity and immutable revision data are resolved
// from CaptureWrite.Fragment and cannot be supplied through this seam.
type LegacyProjectionWrite struct {
	Title         string
	Content       string
	SourceType    string
	Metadata      map[string]any
	CanonicalPath string
	Attachments   []domain.PipelineAttachment
	Entities      []domain.FragmentEntity
}

func applyLegacyProjectionOn(ctx context.Context, q MediaWriteConn, fragment domain.Fragment, projection LegacyProjectionWrite, now time.Time) error {
	if q == nil || strings.TrimSpace(fragment.ID) == "" || strings.TrimSpace(fragment.Revision.ID) == "" {
		return fmt.Errorf("fragment and resolved revision are required")
	}
	metadataJSON := "{}"
	if len(projection.Metadata) != 0 {
		raw, err := json.Marshal(projection.Metadata)
		if err != nil {
			return fmt.Errorf("encode metadata: %w", err)
		}
		metadataJSON = string(raw)
	}
	result, err := q.ExecContext(ctx, `
UPDATE fragments
SET title = ?, content = ?, source_type = ?, metadata_json = ?,
    canonical_path = ?, ingested_at = ?
WHERE id = ? AND current_revision_id = ?`,
		projection.Title, projection.Content, projection.SourceType, metadataJSON,
		projection.CanonicalPath, formatTime(now), fragment.ID, fragment.Revision.ID)
	if err != nil {
		return fmt.Errorf("update fragment projection: %w", err)
	}
	current, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read fragment projection result: %w", err)
	}
	if current != 1 {
		return fmt.Errorf("resolved revision is no longer current")
	}
	if _, err := replaceLegacyAttachmentRows(ctx, q, fragment.ID, fragment.Revision.ID, projection.Attachments, now); err != nil {
		return fmt.Errorf("replace attachment projection: %w", err)
	}
	if err := upsertFragmentEntities(ctx, q, fragment.ID, projection.Entities, now); err != nil {
		return fmt.Errorf("merge entity projection: %w", err)
	}
	return nil
}
