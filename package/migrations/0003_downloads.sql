-- The frozen manifest of each bulk download: what was asked for, and every entry with
-- its version and SHA-256, written before the zip streams.
CREATE TABLE bulk_downloads (
  id TEXT PRIMARY KEY,
  matter_id TEXT NOT NULL REFERENCES core.matters (id) ON DELETE RESTRICT,
  actor_user_id TEXT NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
  scope TEXT NOT NULL CHECK (scope IN ('matter', 'folder', 'selection')),
  folder_label TEXT NOT NULL DEFAULT '',
  status_filter TEXT NOT NULL DEFAULT '',
  category_filter TEXT NOT NULL DEFAULT '',
  versions TEXT NOT NULL CHECK (versions IN ('current', 'all')),
  with_markdown BOOLEAN NOT NULL DEFAULT false,
  layout TEXT NOT NULL CHECK (layout IN ('folders', 'flat')),
  zip_name TEXT NOT NULL,
  entry_count INTEGER NOT NULL,
  byte_count BIGINT NOT NULL DEFAULT 0,
  status TEXT NOT NULL CHECK (status IN ('started', 'complete', 'interrupted')),
  started_at BIGINT NOT NULL,
  finished_at BIGINT NULL
);

CREATE TABLE bulk_download_items (
  bulk_id TEXT NOT NULL REFERENCES bulk_downloads (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  file_id TEXT NULL REFERENCES core.files (id) ON DELETE RESTRICT,
  version_number INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('version', 'markdown')),
  entry_path TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  size_bytes BIGINT NOT NULL,
  PRIMARY KEY (bulk_id, position)
);

CREATE INDEX bulk_download_items_file ON bulk_download_items (file_id);
