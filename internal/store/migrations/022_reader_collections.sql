-- Principal-owned, ordered sets of fragments (playlists, reading lists).
-- Membership is a reference: deleting a collection removes its membership
-- rows and never a fragment. See docs/reader-collections.md.

CREATE TABLE IF NOT EXISTS reader_collections (
    id TEXT PRIMARY KEY,
    principal_id TEXT NOT NULL CHECK (length(principal_id) > 0),
    name TEXT NOT NULL CHECK (length(name) > 0),
    -- name_key is the trimmed, lowercased name. It makes names unique per
    -- principal without regard to case.
    name_key TEXT NOT NULL CHECK (length(name_key) > 0),
    description TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT 'collection' CHECK (length(kind) > 0),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(principal_id, name_key)
);

CREATE TABLE IF NOT EXISTS reader_collection_items (
    collection_id TEXT NOT NULL,
    fragment_id TEXT NOT NULL,
    position INTEGER NOT NULL CHECK (position >= 0),
    added_at TEXT NOT NULL,
    PRIMARY KEY(collection_id, fragment_id),
    FOREIGN KEY(collection_id) REFERENCES reader_collections(id) ON DELETE CASCADE,
    FOREIGN KEY(fragment_id) REFERENCES fragments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_reader_collection_items_order
ON reader_collection_items(collection_id, position);

CREATE INDEX IF NOT EXISTS idx_reader_collection_items_fragment
ON reader_collection_items(fragment_id, collection_id);
