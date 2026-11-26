CREATE TABLE snoozes (
  repo_id INTEGER NOT NULL,
  number INTEGER NOT NULL,
  until_at INTEGER,
  bucket TEXT NOT NULL,
  head_oid TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, number),
  FOREIGN KEY (repo_id, number) REFERENCES prs (repo_id, number) ON DELETE CASCADE
);
