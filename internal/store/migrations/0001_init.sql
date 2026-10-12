CREATE TABLE accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  login TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL CHECK (kind IN ('oauth', 'pat')),
  scopes TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('ok', 'needs_auth')),
  status_detail TEXT,
  created_at INTEGER NOT NULL,
  verified_at INTEGER
);

CREATE TABLE repos (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  revision INTEGER NOT NULL DEFAULT 0,
  account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
  owner TEXT NOT NULL COLLATE NOCASE,
  name TEXT NOT NULL COLLATE NOCASE,
  enabled INTEGER NOT NULL DEFAULT 1,
  rules TEXT NOT NULL DEFAULT '{}',
  added_at INTEGER NOT NULL,
  UNIQUE (account_id, owner, name)
);

CREATE TABLE repo_sync (
  repo_id INTEGER PRIMARY KEY REFERENCES repos (id) ON DELETE CASCADE,
  last_poll_at INTEGER,
  last_success_at INTEGER,
  last_error TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE prs (
  repo_id INTEGER NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
  number INTEGER NOT NULL,
  title TEXT NOT NULL,
  url TEXT NOT NULL,
  author TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('open', 'closed')),
  revision INTEGER NOT NULL DEFAULT 0,
  fingerprint TEXT,
  head_oid TEXT,
  head_changed_at INTEGER,
  record TEXT,
  record_at INTEGER,
  last_enrich_attempt_at INTEGER,
  enrich_failures INTEGER NOT NULL DEFAULT 0,
  retry_at INTEGER,
  error TEXT,
  first_seen_at INTEGER NOT NULL,
  closed_at INTEGER,
  PRIMARY KEY (repo_id, number)
);
CREATE INDEX prs_retry_at ON prs (retry_at) WHERE retry_at IS NOT NULL;

CREATE TABLE results (
  repo_id INTEGER NOT NULL,
  number INTEGER NOT NULL,
  bucket TEXT NOT NULL,
  reason TEXT NOT NULL,
  action TEXT NOT NULL,
  waiting_since INTEGER,
  also TEXT NOT NULL DEFAULT '[]',
  notes TEXT NOT NULL DEFAULT '[]',
  classified_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, number),
  FOREIGN KEY (repo_id, number) REFERENCES prs (repo_id, number) ON DELETE CASCADE
);

CREATE TABLE transitions (
  id INTEGER PRIMARY KEY,
  repo_id INTEGER NOT NULL,
  number INTEGER NOT NULL,
  at INTEGER NOT NULL,
  from_bucket TEXT,
  to_bucket TEXT NOT NULL,
  reason TEXT NOT NULL,
  FOREIGN KEY (repo_id, number) REFERENCES prs (repo_id, number) ON DELETE CASCADE
);
CREATE INDEX transitions_pr ON transitions (repo_id, number, at);
CREATE INDEX transitions_at ON transitions (at);

CREATE TABLE cache_diff (
  repo_id INTEGER NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
  base_oid TEXT NOT NULL,
  reviewed_oid TEXT NOT NULL,
  head_oid TEXT NOT NULL,
  changed INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, base_oid, reviewed_oid, head_oid)
);
CREATE INDEX cache_diff_created_at ON cache_diff (created_at);

CREATE TABLE cache_comment (
  repo_id INTEGER NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
  comment_id INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  is_command INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, comment_id, updated_at)
);
CREATE INDEX cache_comment_created_at ON cache_comment (created_at);

CREATE TABLE cache_policy (
  repo_id INTEGER NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
  base_ref TEXT NOT NULL,
  policy TEXT NOT NULL CHECK (policy IN ('required', 'absent', 'unavailable')),
  fetched_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, base_ref)
);
