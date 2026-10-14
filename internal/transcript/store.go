package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	_ "modernc.org/sqlite"
)

// Store is deliberately separate from the shared canonical database. Its only
// write entry point is Accept. No raw material, raw attachment, embedding,
// delivery callback, or external destination is retained here.
// The process's effective OS UID is its owner; this is not a user identity or
// permission to publish to any other principal.
type Store struct {
	db   *sql.DB
	root string
	file os.FileInfo
}

type Outcome string

const (
	Inserted Outcome = "inserted"
	Updated  Outcome = "updated"
	Skipped  Outcome = "skipped"
)

// Open requires an explicit absolute root with an existing parent. It creates
// only that directory and its database. Existing unsafe paths are refused,
// never made trustworthy by chmod. Linux and Darwin enforce effective UID,
// no symlinks, private modes, and a single-link regular database file.
func Open(root string) (*Store, error) {
	if !filepath.IsAbs(root) || strings.TrimSpace(root) == "" {
		return nil, ErrPrivateStoreRequired
	}
	root = filepath.Clean(root)
	if err := checkParents(filepath.Dir(root)); err != nil {
		return nil, err
	}
	if err := os.Mkdir(root, 0o700); err != nil && !os.IsExist(err) {
		return nil, errors.New("transcript: cannot create private directory")
	}
	if err := checkPrivate(root, true); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "transcripts.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		if err := file.Close(); err != nil {
			return nil, err
		}
	} else if !os.IsExist(err) {
		return nil, errors.New("transcript: cannot create private database")
	}
	if err := checkPrivate(path, false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	// A connection URI is not accepted from config; the trusted filename is
	// fixed within the owner directory. DELETE avoids a separately permissioned
	// persistent WAL/SHM. SQLite journals inherit the 0600 database mode.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, errors.New("transcript: cannot open database")
	}
	db.SetMaxOpenConns(1)
	st := &Store{db: db, root: root, file: info}
	if err := st.checkOwnership(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// The busy timeout comes first: the very first statement (the schema
	// inspection) must wait for a writer's lock rather than fail on it.
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA busy_timeout=%d`, busyTimeoutMS)); err != nil {
		_ = db.Close()
		return nil, errors.New("transcript: cannot configure database")
	}
	var names map[string]bool
	var version string
	if err := retryBusy(func() (e error) { names, version, e = inspectSchema(db); return }); err != nil {
		_ = db.Close()
		return nil, errors.New("transcript: cannot inspect private schema")
	}
	allowed := map[string]bool{"transcripts": true, "transcript_search": true, "transcript_search_data": true, "transcript_search_idx": true, "transcript_search_content": true, "transcript_search_docsize": true, "transcript_search_config": true}
	for _, name := range derivedTables {
		allowed[name] = true
	}
	for name := range names {
		if !allowed[name] {
			db.Close()
			return nil, errors.New("transcript: destination is not a dedicated transcript database")
		}
	}
	// A current schema needs no write at all, so a concurrent writer is never
	// disturbed by an open. Only a real upgrade takes the write lock.
	if !schemaCurrent(names, version) {
		if err := retryBusy(func() error { return upgradeSchema(db) }); err != nil {
			_ = db.Close()
			return nil, errors.New("transcript: cannot initialize private database")
		}
	}
	return st, nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

// Accept redacts before beginning a transaction, and updates canonical material
// and the local text index atomically. IDs reveal no raw source locator. Updates
// retain only redacted material; retries compare only redacted content.
func (s *Store) Accept(ctx context.Context, registration string, in domain.PipelineFragment) (Outcome, error) {
	if s == nil {
		return "", ErrPrivateStoreRequired
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if in.Detail != nil {
		return s.acceptDetail(ctx, registration, in)
	}
	if !valid(in) || strings.TrimSpace(registration) == "" {
		return "", errors.New("transcript: source identity and text are required")
	}
	clean, material, err := sanitize(in)
	if err != nil {
		return "", err
	}
	if err := s.checkOwnership(); err != nil {
		return "", err
	}
	identity, _ := json.Marshal([]string{registration, in.Source, in.SourceID, in.SourceIdentity.SegmentKey})
	id := digest(identity)
	contentDigest := digest(material)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", errors.New("transcript: cannot begin private acceptance")
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT content_digest FROM transcripts WHERE id = ?`, id).Scan(&previous)
	outcome := Updated
	if errors.Is(err, sql.ErrNoRows) {
		outcome = Inserted
	} else if err != nil {
		return "", errors.New("transcript: cannot read private record")
	}
	if previous == contentDigest {
		return Skipped, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO transcripts VALUES (?, ?, 1, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET content_digest=excluded.content_digest, redaction_version=excluded.redaction_version,
material_json=excluded.material_json, title=excluded.title, content=excluded.content`, id, contentDigest, string(material), clean.Title, clean.Content)
	if err != nil {
		return "", errors.New("transcript: cannot persist redacted record")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_search WHERE id = ?`, id); err != nil {
		return "", errors.New("transcript: cannot update private index")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_search VALUES (?, ?, ?)`, id, clean.Title, clean.Content); err != nil {
		return "", errors.New("transcript: cannot index redacted record")
	}
	if err := tx.Commit(); err != nil {
		return "", errors.New("transcript: cannot commit private record")
	}
	return outcome, nil
}

func digest(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

func (s *Store) checkOwnership() error {
	if err := checkParents(filepath.Dir(s.root)); err != nil {
		return err
	}
	if err := checkPrivate(s.root, true); err != nil {
		return err
	}
	path := filepath.Join(s.root, "transcripts.db")
	if err := checkPrivate(path, false); err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(s.file, current) {
		return errors.New("transcript: private database was replaced")
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); os.IsNotExist(err) {
			continue
		}
		if err := checkPrivate(path+suffix, false); err != nil {
			return err
		}
	}
	return nil
}

func checkPrivate(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("transcript: private path is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("transcript: private path must belong to the process owner without symlinks")
	}
	wantMode := os.FileMode(0o600)
	if directory {
		wantMode = 0o700
	}
	if info.Mode().Perm() != wantMode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.IsDir() != directory || (!directory && (!info.Mode().IsRegular() || stat.Nlink != 1)) {
		return errors.New("transcript: private directory/file requires 0700/0600 and an unshared regular file")
	}
	return nil
}

func checkParents(path string) error {
	var parents []os.FileInfo
	for {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("transcript: destination ancestors must be existing directories without symlinks")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
			return errors.New("transcript: destination ancestor belongs to another user")
		}
		parents = append(parents, info)
		next := filepath.Dir(path)
		if next == path {
			break
		}
		path = next
	}
	// Once an owner-only ancestor prevents traversal, permissions on deeper
	// owner-controlled directories cannot grant another UID access to the root.
	protected := false
	for i := len(parents) - 1; i >= 0; i-- {
		info := parents[i]
		stat := info.Sys().(*syscall.Stat_t)
		if !protected && info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return errors.New("transcript: destination ancestor is writable by other users")
		}
		if stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0o077 == 0 {
			protected = true
		}
	}
	return nil
}

// acceptDetail persists a segment's structured turns, tool calls, usage and
// cursor together with its searchable text, in one transaction. The searchable
// text is regenerated from the stored turns, so an incremental parse that only
// saw appended lines still yields the whole session's text.
func (s *Store) acceptDetail(ctx context.Context, registration string, in domain.PipelineFragment) (Outcome, error) {
	if strings.TrimSpace(registration) == "" || strings.TrimSpace(in.Source) == "" || strings.TrimSpace(in.SourceID) == "" {
		return "", errors.New("transcript: source identity is required")
	}
	if len(in.Attachments) != 0 {
		return "", ErrAttachments
	}
	if err := s.checkOwnership(); err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", errors.New("transcript: cannot begin private acceptance")
	}
	defer tx.Rollback()
	segment := in.SourceIdentity.SegmentKey
	pk := sessionPK(registration, in.Source, in.SourceID, segment)
	var known int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tx_sessions WHERE session_pk = ?`, pk).Scan(&known); err != nil {
		return "", errors.New("transcript: cannot read private session")
	}
	if _, err := applyDetail(ctx, tx, registration, in.Source, in.SourceID, segment, in.Detail); err != nil {
		return "", errors.New("transcript: cannot persist derived transcript data")
	}
	content, truncated, err := rebuildContent(ctx, tx, pk)
	if err != nil {
		return "", errors.New("transcript: cannot rebuild searchable text")
	}
	outcome := Skipped
	if known == 0 {
		outcome = Inserted
	}
	if content != "" {
		if in.Detail.Incremental {
			// Appended lines carry no header facts; keep the record's original ones.
			var base domain.PipelineFragment
			var raw string
			if err := tx.QueryRowContext(ctx, `SELECT material_json FROM transcripts WHERE id = ?`, pk).Scan(&raw); err == nil {
				if json.Unmarshal([]byte(raw), &base) == nil {
					in.Title, in.Metadata, in.CreatedAt, in.CanonicalPath, in.SourceIdentity = base.Title, base.Metadata, base.CreatedAt, base.CanonicalPath, base.SourceIdentity
				}
			}
		}
		in.Content = content
		if truncated {
			meta := map[string]any{}
			for k, v := range in.Metadata {
				meta[k] = v
			}
			meta["search_text_truncated"] = true
			in.Metadata = meta
		}
		clean, material, err := sanitize(in)
		if err != nil {
			return "", err
		}
		contentDigest := digest(material)
		var previous string
		err = tx.QueryRowContext(ctx, `SELECT content_digest FROM transcripts WHERE id = ?`, pk).Scan(&previous)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			outcome = Inserted
		case err != nil:
			return "", errors.New("transcript: cannot read private record")
		case previous == contentDigest:
			outcome = Skipped
		default:
			outcome = Updated
		}
		if previous != contentDigest {
			if _, err := tx.ExecContext(ctx, `INSERT INTO transcripts VALUES (?, ?, 1, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET content_digest=excluded.content_digest, redaction_version=excluded.redaction_version,
material_json=excluded.material_json, title=excluded.title, content=excluded.content`, pk, contentDigest, string(material), clean.Title, clean.Content); err != nil {
				return "", errors.New("transcript: cannot persist redacted record")
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_search WHERE id = ?`, pk); err != nil {
				return "", errors.New("transcript: cannot update private index")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_search VALUES (?, ?, ?)`, pk, clean.Title, clean.Content); err != nil {
				return "", errors.New("transcript: cannot index redacted record")
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return "", errors.New("transcript: cannot commit private record")
	}
	return outcome, nil
}
