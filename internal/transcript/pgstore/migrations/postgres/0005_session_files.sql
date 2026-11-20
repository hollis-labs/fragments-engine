-- +goose Up
-- Files a conversation's working folder holds (Antigravity brain: the artifacts an agent
-- wrote, uploads, system files, scratch work). Content is stored whole and once per
-- distinct content; session_files says where it came from. Text is redacted like every
-- other stored string; a file that is not valid text (or holds NUL) is kept as bytes and
-- flagged not_redactable. read_policy works as on tool_results.
CREATE TABLE file_contents (
  content_sha256  text PRIMARY KEY,
  size_bytes      bigint NOT NULL CHECK (size_bytes >= 0),
  text_redacted   text,
  bytes_redacted  bytea,
  redaction_state text NOT NULL DEFAULT 'redacted' CHECK (redaction_state IN ('redacted','not_redactable')),
  CONSTRAINT one_payload CHECK ((text_redacted IS NOT NULL) <> (bytes_redacted IS NOT NULL)),
  CONSTRAINT binary_is_not_redactable CHECK (bytes_redacted IS NULL OR redaction_state = 'not_redactable')
);

CREATE TABLE session_files (
  file_id           text PRIMARY KEY,
  source_id         text NOT NULL REFERENCES sources (source_id),
  harness           text NOT NULL,
  native_session_id text NOT NULL,
  rel_path          text NOT NULL,
  area              text NOT NULL CHECK (area IN ('artifact','upload','system','scratch')),
  media_type        text NOT NULL,
  size_bytes        bigint NOT NULL CHECK (size_bytes >= 0),
  content_sha256    text NOT NULL REFERENCES file_contents (content_sha256),
  file_mtime        timestamptz,
  first_seen_at     timestamptz NOT NULL,
  last_changed_at   timestamptz NOT NULL,
  revision          bigint NOT NULL DEFAULT 1,
  read_policy       text NOT NULL DEFAULT 'open' CHECK (read_policy IN ('open','withheld')),
  UNIQUE (source_id, rel_path)
);
CREATE INDEX session_files_session ON session_files (harness, native_session_id, area);
CREATE INDEX session_files_content ON session_files (content_sha256);

-- +goose Down
DROP TABLE session_files, file_contents;
