CREATE TABLE cache_team (
  org TEXT NOT NULL COLLATE NOCASE,
  team TEXT NOT NULL COLLATE NOCASE,
  login TEXT NOT NULL COLLATE NOCASE,
  member INTEGER NOT NULL,
  fetched_at INTEGER NOT NULL,
  PRIMARY KEY (org, team, login)
);
CREATE INDEX cache_team_fetched_at ON cache_team (fetched_at);
