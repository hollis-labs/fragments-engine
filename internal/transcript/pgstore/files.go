package pgstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
	"github.com/hollis-labs/libs/core/store"
)

// acceptFile stores one file from a conversation's working folder. Content is kept
// whole and once per distinct content (session_files points at file_contents), text is
// redacted like every other stored string, and a file that is not valid text is kept
// as bytes and flagged not_redactable. A file whose size and mtime match its checkpoint
// is not read at all.
func (w *Writer) acceptFile(ctx context.Context, registration string, in domain.PipelineFragment) (Outcome, error) {
	f := in.File
	if f.Load == nil || strings.TrimSpace(f.RelPath) == "" || strings.TrimSpace(f.ConversationID) == "" {
		return "", fmt.Errorf("%w: file identity and content are required", transcript.ErrRecordRejected)
	}
	raw, err := f.Load()
	if err != nil {
		return "", errors.New("transcript: cannot read file")
	}
	var text *string
	var blob []byte
	if utf8.Valid(raw) && strings.IndexByte(string(raw), 0) < 0 {
		t := transcript.Redact(string(raw))
		text = &t
	} else {
		blob = raw
	}
	h := sha256.New()
	if text != nil {
		h.Write([]byte(*text))
	} else {
		h.Write(blob)
	}
	digest := hex.EncodeToString(h.Sum(nil))
	size := int64(len(raw))
	if text != nil {
		size = int64(len(*text))
	}
	fileID := sha(registration + "\x00" + f.RelPath)
	harness := transcript.Redact(strings.SplitN(in.Source, "_", 2)[0]) // antigravity_brain -> antigravity
	var outcome Outcome
	err = w.st.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := w.ensureSource(ctx, tx, registration, harness); err != nil {
			return err
		}
		if text != nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO file_contents (content_sha256, size_bytes, text_redacted, redaction_state) VALUES (?,?,?,'redacted') ON CONFLICT (content_sha256) DO NOTHING`, digest, size, *text)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO file_contents (content_sha256, size_bytes, bytes_redacted, redaction_state) VALUES (?,?,?,'not_redactable') ON CONFLICT (content_sha256) DO NOTHING`, digest, size, blob)
		}
		if err != nil {
			return err
		}
		var prev string
		err := tx.QueryRowContext(ctx, `SELECT content_sha256 FROM session_files WHERE file_id = ?`, fileID).Scan(&prev)
		now := w.now().UTC()
		switch {
		case err == nil && prev == digest:
			outcome = transcript.Skipped
			_, err = tx.ExecContext(ctx, `UPDATE session_files SET file_mtime = ? WHERE file_id = ?`, tsPtr(f.ModTime), fileID)
		case err == nil:
			outcome = transcript.Updated
			_, err = tx.ExecContext(ctx, `UPDATE session_files SET content_sha256 = ?, size_bytes = ?, media_type = ?, file_mtime = ?, last_changed_at = ?, revision = revision + 1 WHERE file_id = ?`,
				digest, size, mediaTypeOf(f.RelPath, raw), tsPtr(f.ModTime), now, fileID)
		default:
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			outcome = transcript.Inserted
			_, err = tx.ExecContext(ctx, `INSERT INTO session_files (file_id, source_id, harness, native_session_id, rel_path, area, media_type, size_bytes, content_sha256, file_mtime, first_seen_at, last_changed_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, fileID, registration, harness, transcript.Redact(f.ConversationID), transcript.Redact(f.RelPath), f.Area, mediaTypeOf(f.RelPath, raw), size, digest, tsPtr(f.ModTime), now, now)
		}
		if err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]any{"file_size": f.Size, "mtime_ns": f.ModTime.UnixNano(), "file_id": fileID})
		_, err = tx.ExecContext(ctx, `INSERT INTO ingest_checkpoints (source_id, source_file_key, file_generation, byte_offset, line_no, tail_digest, parser_version, checkpoint_at, metadata)
VALUES (?,?,?,?,0,?,?,?, ?::jsonb)
ON CONFLICT (source_id, source_file_key) DO UPDATE SET file_generation = excluded.file_generation, byte_offset = excluded.byte_offset, tail_digest = excluded.tail_digest,
 parser_version = excluded.parser_version, checkpoint_at = excluded.checkpoint_at, metadata = excluded.metadata`,
			registration, f.RelPath, fmt.Sprint(f.Size), f.Size, digest, MappingVersion, now, string(meta))
		return err
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}

func mediaTypeOf(rel string, raw []byte) string {
	if t := mime.TypeByExtension(strings.ToLower(path.Ext(rel))); t != "" {
		return t
	}
	n := len(raw)
	if n > 512 {
		n = 512
	}
	return http.DetectContentType(raw[:n])
}
