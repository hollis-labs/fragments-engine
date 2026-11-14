package repository

import (
	"context"
	"fmt"
	"strings"
)

// ReaderChangeCursor is a position in the change feed. FragmentID breaks ties
// between fragments that share a sequence number; empty means every fragment
// at Seq has been passed.
type ReaderChangeCursor struct {
	Seq        int64
	FragmentID string
}

// readerChangesBaseSQL lists the fragments whose latest change visible to the
// principal lies after the cursor, oldest change first. A fragment appears
// once, at its latest change: the NOT EXISTS drops its older rows. Aliases
// and fragments that are not Reader items fall out of the joins.
const readerChangesBaseSQL = `
WITH ` + readerCaptureAggregate + `,
changed AS (
  SELECT c.fragment_id AS fragment_id, c.change_seq AS change_seq
  FROM reader_changes c
  WHERE c.principal_id IN ('', ?)
    AND (c.change_seq > ? OR (? <> '' AND c.change_seq = ? AND c.fragment_id > ?))
    AND NOT EXISTS (
      SELECT 1 FROM reader_changes newer
      WHERE newer.fragment_id = c.fragment_id
        AND newer.principal_id IN ('', ?)
        AND newer.change_seq > c.change_seq
    )
  GROUP BY c.fragment_id, c.change_seq
),
candidates AS (
  SELECT f.id AS fragment_id, COALESCE(ca.last_captured_at, f.ingested_at) AS sort_at,
    changed.change_seq AS change_seq
  FROM changed
  JOIN fragments f ON f.id = changed.fragment_id
  LEFT JOIN fragment_identity_aliases aliases ON aliases.alias_fragment_id = f.id
  LEFT JOIN capture_aggregate ca ON ca.fragment_id = f.id
  WHERE aliases.alias_fragment_id IS NULL
)
SELECT ` + readerBaseColumns + `, candidates.change_seq
FROM candidates
JOIN fragments f ON f.id = candidates.fragment_id
JOIN fragment_source_identities si ON si.fragment_id = f.id
JOIN fragment_revisions r ON r.id = f.current_revision_id AND r.fragment_id = f.id
LEFT JOIN inbox i ON i.fragment_id = f.id
LEFT JOIN capture_aggregate ca ON ca.fragment_id = f.id
LEFT JOIN reader_command_aggregates rca
  ON rca.fragment_id = f.id AND rca.principal_id = ?
LEFT JOIN reading_states rs
  ON rs.fragment_id = f.id AND rs.principal_id = ?
LEFT JOIN curated_notes cn ON cn.fragment_id = f.id
ORDER BY candidates.change_seq, f.id
LIMIT ?`

// ListChanges returns one page of the change feed for a principal: the
// fragments changed after the cursor, in sequence order, each as a complete
// projection at its current state. ChangeKinds names what changed since
// baseSeq. The feed's sequence is read before the page, so every change at or
// below ChangeSequence was visible to the page query.
//
// It runs nine SELECT statements whatever the page size: the sequence, the
// base page, the six set-based reads List uses, and the change kinds.
func (r *ReaderRepository) ListChanges(ctx context.Context, principalID string, after ReaderChangeCursor, baseSeq int64, limit int) (ReaderSnapshot, error) {
	if r == nil || r.db == nil {
		return ReaderSnapshot{}, fmt.Errorf("reader repository is unavailable")
	}
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return ReaderSnapshot{}, fmt.Errorf("reader principal ID is required")
	}
	if limit <= 0 {
		return ReaderSnapshot{}, fmt.Errorf("reader page limit must be positive")
	}
	var sequence int64
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM reader_change_sequence`).Scan(&sequence); err != nil {
		return ReaderSnapshot{}, fmt.Errorf("read reader change sequence: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, readerChangesBaseSQL, principalID,
		after.Seq, after.FragmentID, after.Seq, after.FragmentID,
		principalID, principalID, principalID, limit)
	if err != nil {
		return ReaderSnapshot{}, fmt.Errorf("list reader change bases: %w", err)
	}
	bases, err := scanReaderBases(rows, func(base *ReaderBase) any { return &base.ChangeSeq })
	if err != nil {
		return ReaderSnapshot{}, err
	}
	snapshot, err := r.loadBatches(ctx, bases, principalID)
	if err != nil {
		return ReaderSnapshot{}, err
	}
	snapshot.ChangeSequence = sequence
	fragmentIDs := []string{""}
	if len(bases) > 0 {
		fragmentIDs = fragmentIDs[:0]
		for _, base := range bases {
			fragmentIDs = append(fragmentIDs, base.FragmentID)
		}
	}
	if snapshot.ChangeKinds, err = r.loadChangeKinds(ctx, fragmentIDs, principalID, baseSeq); err != nil {
		return ReaderSnapshot{}, err
	}
	return snapshot, nil
}

func (r *ReaderRepository) loadChangeKinds(ctx context.Context, fragmentIDs []string, principalID string, baseSeq int64) (map[string][]string, error) {
	query := `SELECT DISTINCT fragment_id, kind FROM reader_changes
WHERE principal_id IN ('', ?) AND change_seq > ?
  AND fragment_id IN (` + queryPlaceholders(len(fragmentIDs)) + `)
ORDER BY fragment_id, kind`
	args := append([]any{principalID, baseSeq}, stringsToAny(fragmentIDs)...)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load reader change kinds: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]string, len(fragmentIDs))
	for rows.Next() {
		var fragmentID, kind string
		if err := rows.Scan(&fragmentID, &kind); err != nil {
			return nil, fmt.Errorf("scan reader change kind: %w", err)
		}
		out[fragmentID] = append(out[fragmentID], kind)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reader change kinds: %w", err)
	}
	return out, nil
}
