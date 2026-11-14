-- One row per generation run. The error report and manual_tokens hold names only:
-- template ids, token names, part names and reasons. A value a person typed is never
-- stored. After Release removes a template or package, its runs keep their record with
-- the link cleared, so the check allows at most one of the two.
CREATE TABLE generation_runs (
  id TEXT PRIMARY KEY,
  matter_id TEXT NOT NULL REFERENCES core.matters (id) ON DELETE RESTRICT,
  package_id TEXT NULL REFERENCES packages (id) ON DELETE SET NULL,
  template_id TEXT NULL REFERENCES templates (id) ON DELETE SET NULL,
  actor_user_id TEXT NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
  principal_contact_id TEXT NULL REFERENCES core.contacts (id) ON DELETE RESTRICT,
  outcome TEXT NOT NULL CHECK (outcome IN ('succeeded', 'failed', 'pdf_failed')),
  error_report JSONB NOT NULL DEFAULT '[]',
  manual_tokens TEXT[] NOT NULL DEFAULT '{}',
  file_count INTEGER NOT NULL DEFAULT 0,
  started_at BIGINT NOT NULL,
  finished_at BIGINT NOT NULL,
  CHECK (package_id IS NULL OR template_id IS NULL)
);

CREATE INDEX generation_runs_matter ON generation_runs (matter_id, finished_at);

-- Extension of core.files: what made each generated draft.
CREATE TABLE generated_files (
  file_id TEXT PRIMARY KEY REFERENCES core.files (id) ON DELETE RESTRICT,
  run_id TEXT NOT NULL REFERENCES generation_runs (id) ON DELETE RESTRICT,
  template_id TEXT NULL REFERENCES templates (id) ON DELETE SET NULL,
  template_version_number INTEGER NOT NULL,
  component_versions JSONB NOT NULL DEFAULT '{}',
  pdf_file_id TEXT NULL REFERENCES core.files (id) ON DELETE RESTRICT,
  status_cache TEXT NOT NULL DEFAULT '',
  trashed BOOLEAN NOT NULL DEFAULT false,
  updated_at BIGINT NOT NULL
);

CREATE INDEX generated_files_run ON generated_files (run_id);
