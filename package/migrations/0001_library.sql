-- The template library. Template and component bodies are core library files; these
-- rows hold only the module's metadata and point at the file by id.
CREATE TABLE templates (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  doc_class TEXT NOT NULL CHECK (doc_class IN ('pleading', 'letter', 'estate', 'other')),
  topics TEXT[] NOT NULL DEFAULT '{}',
  description TEXT NOT NULL DEFAULT '',
  body_file_id TEXT NOT NULL UNIQUE REFERENCES core.files (id) ON DELETE RESTRICT,
  output TEXT NOT NULL CHECK (output IN ('docx', 'docx_and_pdf')),
  attorney_only BOOLEAN NOT NULL DEFAULT false,
  category_id TEXT NULL,
  status TEXT NOT NULL CHECK (status IN ('active', 'archived')),
  created_by TEXT NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);

-- No rows for a template means every practice area.
CREATE TABLE template_practice_areas (
  template_id TEXT NOT NULL REFERENCES templates (id) ON DELETE CASCADE,
  practice_area_id TEXT NOT NULL REFERENCES core.practice_areas (id) ON DELETE RESTRICT,
  PRIMARY KEY (template_id, practice_area_id)
);

CREATE TABLE components (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  category TEXT NOT NULL CHECK (category IN ('caption', 'signature', 'certificate', 'letterhead', 'other')),
  description TEXT NOT NULL DEFAULT '',
  body_file_id TEXT NOT NULL UNIQUE REFERENCES core.files (id) ON DELETE RESTRICT,
  status TEXT NOT NULL CHECK (status IN ('active', 'archived')),
  created_by TEXT NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);

CREATE TABLE template_slots (
  template_id TEXT NOT NULL REFERENCES templates (id) ON DELETE CASCADE,
  slot TEXT NOT NULL CHECK (slot ~ '^[a-z0-9_]+$'),
  component_id TEXT NOT NULL REFERENCES components (id) ON DELETE CASCADE,
  PRIMARY KEY (template_id, slot)
);

CREATE TABLE packages (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('active', 'archived')),
  created_by TEXT NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);

CREATE TABLE package_practice_areas (
  package_id TEXT NOT NULL REFERENCES packages (id) ON DELETE CASCADE,
  practice_area_id TEXT NOT NULL REFERENCES core.practice_areas (id) ON DELETE RESTRICT,
  PRIMARY KEY (package_id, practice_area_id)
);

CREATE TABLE package_templates (
  package_id TEXT NOT NULL REFERENCES packages (id) ON DELETE CASCADE,
  position INTEGER NOT NULL CHECK (position >= 0),
  template_id TEXT NOT NULL REFERENCES templates (id) ON DELETE CASCADE,
  PRIMARY KEY (package_id, position),
  UNIQUE (package_id, template_id)
);
