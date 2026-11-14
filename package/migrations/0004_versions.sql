-- Notes and labels on core file versions, and the record of each version copy.
CREATE TABLE version_notes (
  file_id TEXT NOT NULL REFERENCES core.files (id) ON DELETE RESTRICT,
  version_number INTEGER NOT NULL,
  label TEXT NOT NULL DEFAULT '' CHECK (label IN ('', 'sent', 'signed', 'filed', 'reviewed', 'other')),
  note TEXT NOT NULL DEFAULT '',
  updated_by TEXT NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (file_id, version_number)
);

CREATE TABLE version_copies (
  file_id TEXT PRIMARY KEY REFERENCES core.files (id) ON DELETE RESTRICT,
  source_file_id TEXT NOT NULL REFERENCES core.files (id) ON DELETE RESTRICT,
  source_version_number INTEGER NOT NULL,
  created_by TEXT NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
  created_at BIGINT NOT NULL
);
