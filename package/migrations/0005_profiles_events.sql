-- Extension of core.users: what an attorney's signature block needs. The admin types
-- every value; the module never guesses a courtesy title.
CREATE TABLE attorney_profiles (
  user_id TEXT PRIMARY KEY REFERENCES core.users (id) ON DELETE RESTRICT,
  bar_number TEXT NOT NULL DEFAULT '',
  bar_state TEXT NOT NULL DEFAULT '' CHECK (bar_state = '' OR bar_state ~ '^[A-Z]{2}$'),
  courtesy_title TEXT NOT NULL DEFAULT '',
  signature_line TEXT NOT NULL DEFAULT '',
  updated_at BIGINT NOT NULL
);

-- Events the core delivered, so each one is handled once.
CREATE TABLE received_events (
  event_id TEXT PRIMARY KEY,
  action TEXT NOT NULL,
  record_id TEXT NOT NULL,
  received_at BIGINT NOT NULL
);

CREATE INDEX received_events_record ON received_events (action, record_id);
