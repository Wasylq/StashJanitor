-- stash-space-fixer SQLite schema.
--
-- Phase 1 schema. Future migrations should ADD tables/columns rather than
-- mutating existing ones. The schema_version table is the single source of
-- truth for which migration step the file is at.

PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;

CREATE TABLE IF NOT EXISTS schema_version (
  version INTEGER PRIMARY KEY
);

-- One row per `sfx scenes scan` or `sfx files scan` invocation. Lets us
-- attribute groups to a specific scan and report on scan history.
CREATE TABLE IF NOT EXISTS scan_runs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  workflow      TEXT NOT NULL,                 -- 'scenes' | 'files'
  started_at    TEXT NOT NULL,
  finished_at   TEXT,
  distance      INTEGER,                       -- only for 'scenes' workflow
  duration_diff REAL,                          -- only for 'scenes' workflow
  group_count   INTEGER,
  notes         TEXT
);

-- Workflow A: cross-scene duplicate groups.
CREATE TABLE IF NOT EXISTS scene_groups (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  scan_run_id     INTEGER NOT NULL REFERENCES scan_runs(id) ON DELETE CASCADE,
  signature       TEXT NOT NULL,               -- sorted joined scene IDs; stable across runs
  status          TEXT NOT NULL,               -- pending|decided|needs_review|applied|dismissed
  decision_reason TEXT,
  decided_at      TEXT,
  applied_at      TEXT,
  UNIQUE(scan_run_id, signature)
);

CREATE INDEX IF NOT EXISTS idx_scene_groups_status ON scene_groups(status);
CREATE INDEX IF NOT EXISTS idx_scene_groups_signature ON scene_groups(signature);

CREATE TABLE IF NOT EXISTS scene_group_scenes (
  group_id         INTEGER NOT NULL REFERENCES scene_groups(id) ON DELETE CASCADE,
  scene_id         TEXT NOT NULL,
  role             TEXT NOT NULL,              -- keeper|loser|undecided
  -- Snapshot of scoring inputs at scan time. Lets the report run offline.
  width            INTEGER,
  height           INTEGER,
  bitrate          INTEGER,
  framerate        REAL,
  codec            TEXT,
  file_size        INTEGER,
  duration         REAL,
  organized        INTEGER NOT NULL DEFAULT 0,
  has_stash_id     INTEGER NOT NULL DEFAULT 0,
  tag_count        INTEGER NOT NULL DEFAULT 0,
  performer_count  INTEGER NOT NULL DEFAULT 0,
  primary_path     TEXT,
  PRIMARY KEY (group_id, scene_id)
);

-- Workflow B: a single Stash scene that has more than one file attached.
-- Files within a single scene are byte-equivalent (Stash matches by oshash/md5),
-- so the scoring inputs are limited to filename, path, and mod_time. The
-- tech-spec columns are snapshot data for the report only.
CREATE TABLE IF NOT EXISTS file_groups (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  scan_run_id     INTEGER NOT NULL REFERENCES scan_runs(id) ON DELETE CASCADE,
  scene_id        TEXT NOT NULL,
  status          TEXT NOT NULL,               -- pending|decided|needs_review|applied|dismissed
  decision_reason TEXT,
  decided_at      TEXT,
  applied_at      TEXT,
  UNIQUE(scan_run_id, scene_id)
);

CREATE INDEX IF NOT EXISTS idx_file_groups_status ON file_groups(status);
CREATE INDEX IF NOT EXISTS idx_file_groups_scene_id ON file_groups(scene_id);

CREATE TABLE IF NOT EXISTS file_group_files (
  file_group_id    INTEGER NOT NULL REFERENCES file_groups(id) ON DELETE CASCADE,
  file_id          TEXT NOT NULL,
  role             TEXT NOT NULL,              -- keeper|loser|undecided
  is_primary       INTEGER NOT NULL DEFAULT 0,
  -- The fields that actually differ within a single scene; used for scoring.
  basename         TEXT NOT NULL,
  path             TEXT NOT NULL,
  mod_time         TEXT,
  filename_quality INTEGER NOT NULL DEFAULT 0, -- 0/1 for regex match
  -- Snapshot of tech specs purely for display. Guaranteed equal across files
  -- in a single scene since Stash matches by oshash/md5.
  width            INTEGER,
  height           INTEGER,
  bitrate          INTEGER,
  framerate        REAL,
  codec            TEXT,
  file_size        INTEGER,
  PRIMARY KEY (file_group_id, file_id)
);

-- Persists user overrides ACROSS scan runs, keyed by group signature
-- (workflow A) or scene_id (workflow B). "I already told you these aren't
-- duplicates, stop suggesting them."
CREATE TABLE IF NOT EXISTS user_decisions (
  key             TEXT PRIMARY KEY,            -- signature for scenes, "scene:<id>" for files
  workflow        TEXT NOT NULL,               -- 'scenes' | 'files'
  decision        TEXT NOT NULL,               -- not_duplicate | force_keeper | dismiss | keep_all
  keeper_id       TEXT,                        -- scene_id or file_id depending on decision
  decided_at      TEXT NOT NULL,
  notes           TEXT
);
