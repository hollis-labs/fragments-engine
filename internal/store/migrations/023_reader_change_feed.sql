-- Reader change feed. reader_changes holds, per fragment and per kind of
-- change, the sequence number of the latest change. An empty principal_id is
-- a change every principal sees. A non-empty one is private to that principal.
-- The triggers below are the write paths that advance the sequence. Each one
-- runs inside the transaction of the write it observes, so sequence order is
-- commit order. See docs/reader-change-feed.md.
--
-- reader_changes has no foreign keys on purpose. A trigger must never fail
-- the write that fired it, and rows for fragments that are not Reader items
-- are filtered out when the feed is read.
--
-- The migration runner splits statements on semicolons and tracks trigger
-- bodies by counting two keywords, so nothing here, comments included, may
-- contain a semicolon or either keyword outside a trigger body. That is why
-- the triggers use IIF and never a CASE expression.

CREATE TABLE IF NOT EXISTS reader_change_sequence (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    seq INTEGER NOT NULL CHECK (seq >= 0)
);

INSERT OR IGNORE INTO reader_change_sequence(id, seq) VALUES (1, 0);

CREATE TABLE IF NOT EXISTS reader_changes (
    fragment_id TEXT NOT NULL,
    principal_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    change_seq INTEGER NOT NULL,
    PRIMARY KEY(fragment_id, principal_id, kind)
);

CREATE INDEX IF NOT EXISTS idx_reader_changes_seq
ON reader_changes(change_seq, fragment_id);

-- The media triggers find an asset's fragments through attachment_refs by
-- media_asset_id. Without this index each media write scans the table.
CREATE INDEX IF NOT EXISTS idx_attachment_refs_media_asset
ON attachment_refs(media_asset_id, fragment_revision_id);

-- Every fragment that already exists starts in the feed, oldest first.
INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
SELECT id, '', 'capture', ROW_NUMBER() OVER (ORDER BY ingested_at, id) FROM fragments;

UPDATE reader_change_sequence
SET seq = (SELECT COALESCE(MAX(change_seq), 0) FROM reader_changes) WHERE id = 1;

-- capture: a fragment is created, captured again, or its source identity changes.

CREATE TRIGGER IF NOT EXISTS reader_changes_fragment_insert
AFTER INSERT ON fragments
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'capture' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'capture',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_capture_attempt_insert
AFTER INSERT ON capture_attempts
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'capture' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'capture',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_source_identity_insert
AFTER INSERT ON fragment_source_identities
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'capture' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'capture',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_source_identity_update
AFTER UPDATE ON fragment_source_identities
WHEN (OLD.provider IS NOT NEW.provider
  OR OLD.provider_item_id IS NOT NEW.provider_item_id
  OR OLD.source_item_key IS NOT NEW.source_item_key
  OR OLD.source_locator IS NOT NEW.source_locator
  OR OLD.segment_key IS NOT NEW.segment_key
  OR OLD.submitted_url IS NOT NEW.submitted_url
  OR OLD.canonical_url IS NOT NEW.canonical_url)
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'capture' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'capture',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

-- revision: the current revision moves, or a revision's shown fields are edited.

CREATE TRIGGER IF NOT EXISTS reader_changes_fragment_revision_switch
AFTER UPDATE OF current_revision_id ON fragments
WHEN OLD.current_revision_id IS NOT NEW.current_revision_id
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'revision' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'revision',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_revision_update
AFTER UPDATE ON fragment_revisions
WHEN (OLD.title IS NOT NEW.title
  OR OLD.description IS NOT NEW.description
  OR OLD.content IS NOT NEW.content
  OR OLD.content_format IS NOT NEW.content_format
  OR OLD.metadata_json IS NOT NEW.metadata_json
  OR OLD.ordered_media_digest IS NOT NEW.ordered_media_digest)
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'revision' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'revision',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

-- tags: tag observations, enrichment tag observations, and a principal's overlay.
-- A tag a principal added through a Reader command is shown only to them.

CREATE TRIGGER IF NOT EXISTS reader_changes_tag_insert
AFTER INSERT ON fragment_tag_observations
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'tags' AND principal_id = IIF(NEW.producer = 'reader-command', COALESCE(NEW.actor_id, ''), '')
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, IIF(NEW.producer = 'reader-command', COALESCE(NEW.actor_id, ''), ''), 'tags',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_tag_update
AFTER UPDATE ON fragment_tag_observations
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'tags' AND principal_id = IIF(NEW.producer = 'reader-command', COALESCE(NEW.actor_id, ''), '')
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, IIF(NEW.producer = 'reader-command', COALESCE(NEW.actor_id, ''), ''), 'tags',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_tag_delete
AFTER DELETE ON fragment_tag_observations
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'tags' AND principal_id = IIF(OLD.producer = 'reader-command', COALESCE(OLD.actor_id, ''), '')
    AND fragment_id IN (SELECT OLD.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, IIF(OLD.producer = 'reader-command', COALESCE(OLD.actor_id, ''), ''), 'tags',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT OLD.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_enrichment_tags_insert
AFTER INSERT ON enrichment_observations
WHEN NEW.capability = 'tags'
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'tags' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'tags',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_tag_overlay_insert
AFTER INSERT ON reader_tag_overlay_events
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'tags' AND principal_id = NEW.principal_id
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, NEW.principal_id, 'tags',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

-- annotations: capture notes and highlights.

CREATE TRIGGER IF NOT EXISTS reader_changes_annotation_insert
AFTER INSERT ON capture_annotations
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'annotations' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'annotations',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_annotation_update
AFTER UPDATE ON capture_annotations
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'annotations' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'annotations',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_annotation_delete
AFTER DELETE ON capture_annotations
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'annotations' AND principal_id = ''
    AND fragment_id IN (SELECT OLD.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'annotations',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT OLD.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

-- note: the curated note.

CREATE TRIGGER IF NOT EXISTS reader_changes_note_insert
AFTER INSERT ON curated_notes
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'note' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'note',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_note_update
AFTER UPDATE ON curated_notes
WHEN (OLD.body_markdown IS NOT NEW.body_markdown
  OR OLD.revision IS NOT NEW.revision)
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'note' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'note',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_note_delete
AFTER DELETE ON curated_notes
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'note' AND principal_id = ''
    AND fragment_id IN (SELECT OLD.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'note',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT OLD.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

-- reading_state: one principal's state, position and flags.

CREATE TRIGGER IF NOT EXISTS reader_changes_reading_state_insert
AFTER INSERT ON reading_states
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'reading_state' AND principal_id = NEW.principal_id
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, NEW.principal_id, 'reading_state',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_reading_state_update
AFTER UPDATE ON reading_states
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'reading_state' AND principal_id = NEW.principal_id
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, NEW.principal_id, 'reading_state',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_reading_state_delete
AFTER DELETE ON reading_states
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'reading_state' AND principal_id = OLD.principal_id
    AND fragment_id IN (SELECT OLD.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, OLD.principal_id, 'reading_state',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT OLD.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

-- media: attachments, the logical asset, and each variant's acquisition state.
-- An asset can be attached to several fragments, so these fan out.

CREATE TRIGGER IF NOT EXISTS reader_changes_attachment_insert
AFTER INSERT ON attachment_refs
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'media' AND principal_id = ''
    AND fragment_id IN (SELECT fr.fragment_id AS fragment_id FROM fragment_revisions fr WHERE fr.id = NEW.fragment_revision_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'media',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT fr.fragment_id AS fragment_id FROM fragment_revisions fr WHERE fr.id = NEW.fragment_revision_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_attachment_update
AFTER UPDATE ON attachment_refs
WHEN (OLD.media_asset_id IS NOT NEW.media_asset_id
  OR OLD.role IS NOT NEW.role
  OR OLD.position IS NOT NEW.position
  OR OLD.caption IS NOT NEW.caption)
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'media' AND principal_id = ''
    AND fragment_id IN (SELECT fr.fragment_id AS fragment_id FROM fragment_revisions fr WHERE fr.id = NEW.fragment_revision_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'media',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT fr.fragment_id AS fragment_id FROM fragment_revisions fr WHERE fr.id = NEW.fragment_revision_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_attachment_delete
AFTER DELETE ON attachment_refs
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'media' AND principal_id = ''
    AND fragment_id IN (SELECT fr.fragment_id AS fragment_id FROM fragment_revisions fr WHERE fr.id = OLD.fragment_revision_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'media',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT fr.fragment_id AS fragment_id FROM fragment_revisions fr WHERE fr.id = OLD.fragment_revision_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_asset_update
AFTER UPDATE ON media_assets
WHEN (OLD.kind IS NOT NEW.kind
  OR OLD.width IS NOT NEW.width
  OR OLD.height IS NOT NEW.height
  OR OLD.duration_seconds IS NOT NEW.duration_seconds
  OR OLD.page_count IS NOT NEW.page_count
  OR OLD.alt_text IS NOT NEW.alt_text
  OR OLD.provider IS NOT NEW.provider
  OR OLD.provider_media_id IS NOT NEW.provider_media_id
  OR OLD.source_locator IS NOT NEW.source_locator)
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'media' AND principal_id = ''
    AND fragment_id IN (SELECT fr.fragment_id AS fragment_id FROM attachment_refs ar
      JOIN fragment_revisions fr ON fr.id = ar.fragment_revision_id
      WHERE ar.media_asset_id = NEW.id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'media',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT fr.fragment_id AS fragment_id FROM attachment_refs ar
      JOIN fragment_revisions fr ON fr.id = ar.fragment_revision_id
      WHERE ar.media_asset_id = NEW.id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_variant_insert
AFTER INSERT ON asset_variants
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'media' AND principal_id = ''
    AND fragment_id IN (SELECT fr.fragment_id AS fragment_id FROM attachment_refs ar
      JOIN fragment_revisions fr ON fr.id = ar.fragment_revision_id
      WHERE ar.media_asset_id = NEW.media_asset_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'media',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT fr.fragment_id AS fragment_id FROM attachment_refs ar
      JOIN fragment_revisions fr ON fr.id = ar.fragment_revision_id
      WHERE ar.media_asset_id = NEW.media_asset_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_variant_update
AFTER UPDATE ON asset_variants
WHEN (OLD.kind IS NOT NEW.kind
  OR OLD.source_url IS NOT NEW.source_url
  OR OLD.source_expires_at IS NOT NEW.source_expires_at
  OR OLD.source_path IS NOT NEW.source_path
  OR OLD.mime_type IS NOT NEW.mime_type
  OR OLD.width IS NOT NEW.width
  OR OLD.height IS NOT NEW.height
  OR OLD.duration_seconds IS NOT NEW.duration_seconds
  OR OLD.byte_size IS NOT NEW.byte_size
  OR OLD.digest IS NOT NEW.digest
  OR OLD.blob_digest IS NOT NEW.blob_digest
  OR OLD.custody IS NOT NEW.custody
  OR OLD.acquisition_state IS NOT NEW.acquisition_state
  OR OLD.failure_code IS NOT NEW.failure_code
  OR OLD.failure_message IS NOT NEW.failure_message
  OR OLD.failure_retryable IS NOT NEW.failure_retryable
  OR OLD.legacy_storage_path IS NOT NEW.legacy_storage_path)
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'media' AND principal_id = ''
    AND fragment_id IN (SELECT fr.fragment_id AS fragment_id FROM attachment_refs ar
      JOIN fragment_revisions fr ON fr.id = ar.fragment_revision_id
      WHERE ar.media_asset_id = NEW.media_asset_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'media',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT fr.fragment_id AS fragment_id FROM attachment_refs ar
      JOIN fragment_revisions fr ON fr.id = ar.fragment_revision_id
      WHERE ar.media_asset_id = NEW.media_asset_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_variant_delete
AFTER DELETE ON asset_variants
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'media' AND principal_id = ''
    AND fragment_id IN (SELECT fr.fragment_id AS fragment_id FROM attachment_refs ar
      JOIN fragment_revisions fr ON fr.id = ar.fragment_revision_id
      WHERE ar.media_asset_id = OLD.media_asset_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'media',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT fr.fragment_id AS fragment_id FROM attachment_refs ar
      JOIN fragment_revisions fr ON fr.id = ar.fragment_revision_id
      WHERE ar.media_asset_id = OLD.media_asset_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_acquisition_command_update
AFTER UPDATE OF state ON reader_command_receipts
WHEN NEW.command = 'request_asset_acquisition' AND OLD.state IS NOT NEW.state
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'media' AND principal_id = NEW.principal_id
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, NEW.principal_id, 'media',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

-- enrichment: capability coverage, which selects the shown title, description
-- and summary.

CREATE TRIGGER IF NOT EXISTS reader_changes_coverage_insert
AFTER INSERT ON fragment_capability_coverage
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'enrichment' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'enrichment',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_coverage_update
AFTER UPDATE ON fragment_capability_coverage
WHEN (OLD.state IS NOT NEW.state
  OR OLD.selected_observation_id IS NOT NEW.selected_observation_id
  OR OLD.detail IS NOT NEW.detail)
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'enrichment' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'enrichment',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_coverage_delete
AFTER DELETE ON fragment_capability_coverage
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'enrichment' AND principal_id = ''
    AND fragment_id IN (SELECT OLD.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'enrichment',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT OLD.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

-- triage: the fragment is staged in, or leaves, the inbox.

CREATE TRIGGER IF NOT EXISTS reader_changes_inbox_insert
AFTER INSERT ON inbox
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'triage' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'triage',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_inbox_delete
AFTER DELETE ON inbox
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'triage' AND principal_id = ''
    AND fragment_id IN (SELECT OLD.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'triage',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT OLD.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

-- routing: route log facts, and one principal's route and materialize commands.

CREATE TRIGGER IF NOT EXISTS reader_changes_route_log_insert
AFTER INSERT ON route_log
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'routing' AND principal_id = ''
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, '', 'routing',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_effect_insert
AFTER INSERT ON reader_command_effects
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'routing' AND principal_id = NEW.principal_id
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, NEW.principal_id, 'routing',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_effect_update
AFTER UPDATE OF state ON reader_command_effects
WHEN OLD.state IS NOT NEW.state
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'routing' AND principal_id = NEW.principal_id
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, NEW.principal_id, 'routing',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

-- collections: membership, and a rename or kind change of a collection, which
-- shows on every member. Position changes do not show on a Reader item.

CREATE TRIGGER IF NOT EXISTS reader_changes_collection_item_insert
AFTER INSERT ON reader_collection_items
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'collections' AND principal_id = COALESCE((SELECT c.principal_id FROM reader_collections c WHERE c.id = NEW.collection_id), '')
    AND fragment_id IN (SELECT NEW.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, COALESCE((SELECT c.principal_id FROM reader_collections c WHERE c.id = NEW.collection_id), ''), 'collections',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT NEW.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

-- When a collection row is deleted directly, its members go by cascade after
-- the row is gone, so the member trigger can no longer tell whose collection
-- it was. The member trigger therefore records only while the collection still
-- exists, and reader_changes_collection_delete below records the members
-- under the owner before the row goes.
CREATE TRIGGER IF NOT EXISTS reader_changes_collection_item_delete
AFTER DELETE ON reader_collection_items
WHEN EXISTS (SELECT 1 FROM reader_collections c WHERE c.id = OLD.collection_id)
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'collections' AND principal_id = COALESCE((SELECT c.principal_id FROM reader_collections c WHERE c.id = OLD.collection_id), '')
    AND fragment_id IN (SELECT OLD.fragment_id AS fragment_id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, COALESCE((SELECT c.principal_id FROM reader_collections c WHERE c.id = OLD.collection_id), ''), 'collections',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT OLD.fragment_id AS fragment_id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_collection_update
AFTER UPDATE ON reader_collections
WHEN (OLD.name IS NOT NEW.name
  OR OLD.kind IS NOT NEW.kind)
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'collections' AND principal_id = NEW.principal_id
    AND fragment_id IN (SELECT m.fragment_id AS fragment_id FROM reader_collection_items m WHERE m.collection_id = NEW.id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, NEW.principal_id, 'collections',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT m.fragment_id AS fragment_id FROM reader_collection_items m WHERE m.collection_id = NEW.id) WHERE fragment_id IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS reader_changes_collection_delete
BEFORE DELETE ON reader_collections
BEGIN
  UPDATE reader_change_sequence SET seq = seq + 1 WHERE id = 1;
  DELETE FROM reader_changes
  WHERE kind = 'collections' AND principal_id = OLD.principal_id
    AND fragment_id IN (SELECT m.fragment_id AS fragment_id FROM reader_collection_items m WHERE m.collection_id = OLD.id);
  INSERT INTO reader_changes(fragment_id, principal_id, kind, change_seq)
  SELECT DISTINCT fragment_id, OLD.principal_id, 'collections',
    (SELECT seq FROM reader_change_sequence WHERE id = 1)
  FROM (SELECT m.fragment_id AS fragment_id FROM reader_collection_items m WHERE m.collection_id = OLD.id) WHERE fragment_id IS NOT NULL;
END;
