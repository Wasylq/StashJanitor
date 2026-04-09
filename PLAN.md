# StashJanitor — Plan v1

Status: **draft, awaiting user sign-off**
Author: collaborative planning session, 2026-04-09

## 1. Goal

A single static Go binary that finds duplicate video scenes in your Stash
library and helps you reclaim disk space safely. It runs anywhere (NAS sidecar
or desktop), talks to Stash purely over GraphQL with an optional API key, and
**never touches the filesystem directly** — all destructive actions go through
Stash's own API so the container's view of paths is the only one that matters.

## 2. Non-goals (v1)

- Identifying metadata-less orphans via stash-box (Phase 3 — use Stash's
  built-in Scene Tagger in the meantime)
- Interactive TUI / web UI (Phase 2 / Phase 3)
- Any direct filesystem operations (we always go through Stash mutations)
- Integration with other media servers (you don't have any)
- Cross-scene hard delete via `scenesDestroy(delete_file: true)` — Phase 2
  power-user mode. With merge available in Phase 1, hard delete is rarely
  needed because merge already reclaims the disk space (via the post-merge
  file pruning step) without losing metadata or play history.

## 2a. Two distinct workflows in v1

The tool addresses **two related but distinct deduplication problems**, each
with its own command, query, scoring rules, and apply mutations:

**A. Cross-scene duplicate detection** — two or more separate Stash scenes that
are perceptually the same content (identical or close phash). Found via
`findDuplicateScenes`.

**B. Within-scene multi-file cleanup** — a single Stash scene with multiple
attached files (Stash auto-attaches re-detected files instead of creating
duplicate scenes). Found via `findScenes(scene_filter: { file_count: { modifier:
GREATER_THAN, value: 1 } })`.

**Conceptual note 1 (workflow B safety):** within a single Stash scene, all
files share the same scene-level metadata (title, performers, tags, studio,
stash_ids). Files only carry technical attributes. So cleaning up a multi-file
scene never risks losing metadata — we just switch the primary file to the
winner and delete the loser files.

**Conceptual note 1a (workflow B technical equivalence — verified in
v0.31.0 source):** Stash attaches a newly-scanned file to an existing scene
**only** by `oshash` or `md5` match, never by phash. See `pkg/scene/scan.go`:

```go
// only try to match by data fingerprints, _not_ perceptual fingerprints
matchableFingerprintTypes = []string{models.FingerprintTypeOshash, models.FingerprintTypeMD5}
```

This means **all files within a single Stash scene are byte-equivalent** (or
oshash-equivalent in the rare edge case of partial-hash collision). Resolution,
bitrate, codec, frame rate, file size, duration — all guaranteed identical.
Only `path`, `basename`, `mod_time`, and `parent_folder` differ across files
in the same scene. Workflow B scoring uses only those fields; tech specs are
captured purely as snapshot data for the report.

**Conceptual note 2 (workflow A merge gotcha — verified in v0.31.0 source):**
Stash's `sceneMerge` mutation does NOT auto-union scene-level metadata. It
moves files from sources to destination, merges scene markers, and merges
play/o history if requested — but tags, performers, studio, stash_ids, title,
urls, etc. are *only* set on the destination via the `values` field
(`SceneUpdateInput`), which the caller has to compute. **Our tool does that
union itself** before calling sceneMerge, otherwise the loser scenes' metadata
would be silently lost. See section 7 (config) for the union policy and
section 11 (apply pipeline) for the full merge sequence.

## 3. Locked-in decisions

These were settled during planning and should not be re-litigated without
explicit reason:

| Topic | Decision |
|---|---|
| Runtime | Single static Go binary, runs anywhere |
| Stash auth | API key via `ApiKey` header from env var; **optional** (your instance has none today, but the tool supports it) |
| Default `apply` action | **`tag`** — adds `_dedupe_loser` / `_dedupe_keeper` tags, no deletion |
| Default `apply` mode | **`--dry-run` is the default**; you must pass `--commit` to actually mutate Stash |
| Cross-scene merge (`--action merge`) | **In scope for Phase 1.** Tool computes the metadata union (tags/performers/studio/stash_ids/etc.) per a configurable policy, then calls `sceneMerge`, then prunes the resulting multi-file scene via workflow B. **`--commit` requires interactive `YES` confirmation** showing count + reclaimable bytes; bypass with `--yes` for scripted use. |
| Interface (v1) | CLI with `scan` / `report` / `apply` / `status` / `config` / `mark` |
| Scope (v1) | Strictly deduplication |
| Tie-break on metadata-vs-quality conflict | **Flag for human review**, do NOT auto-decide |
| Default phash strictness | `--distance 4 --duration-diff 1.0` |
| Other media servers | None — clean Stash-only deletion |
| Library scale | Tens of thousands of scenes → SQLite caching is essential |
| Multi-file scenes (workflow B) | **In scope for Phase 1**, parallel command. Report-only by default; `--commit` deletes loser files via `deleteFiles` |
| Filename quality (workflow B) | Configurable regex grants a strong scoring bonus; default regex matches `YYYY-MM-DD_<name>_<resolution>.<ext>` |
| Submit fingerprints to stash-box | Opt-in flag `--submit-fingerprints`; applies to both workflows after a successful `--commit`; tracked in SQLite to avoid re-submission |

## 4. Target environment

- **Stash URL:** `http://localhost:9999`
- **Stash version:** `v0.31.0` (we develop and test against this schema, not `develop`)
- **Auth:** no API key currently set; tool reads `STASH_API_KEY` from env and only sends the `ApiKey` header if present.

## 5. Architecture overview

```
┌──────────────┐  GraphQL   ┌──────────┐
│ stash-janitor (binary) │ ─────────► │  Stash   │ ── manages files on disk
└──────┬───────┘            └──────────┘
       │
       ▼
┌──────────────┐
│ stash-janitor.sqlite   │  ← scan cache, decisions, history
└──────────────┘
```

**Key idea:** Stash's GraphQL does the heavy lifting for both workflows. For
workflow A, `findDuplicateScenes(distance, duration_diff): [[Scene!]!]!`
returns groups of perceptual-hash duplicates server-side — we don't reinvent
phash logic. For workflow B, a `findScenes` filter on `file_count > 1`
enumerates multi-file scenes for us. Our job is the **policy layer**: score
candidates, pick winners, act safely.

### Tech choices

| Concern | Choice | Rationale |
|---|---|---|
| Language | Go 1.22+ | per request |
| GraphQL client | hand-rolled `net/http` + `encoding/json` | only ~6 operations needed; avoids genqlient codegen |
| SQLite driver | `modernc.org/sqlite` | **pure Go**, no cgo → static single binary |
| CLI framework | `github.com/spf13/cobra` | clean subcommand UX |
| Config | `gopkg.in/yaml.v3` | human-edited |
| Logging | stdlib `log/slog` | structured, zero deps |

### Project layout

```
StashJanitor/
├── cmd/stash-janitor/main.go              # entry point + cobra wiring
├── internal/
│   ├── stash/                   # GraphQL client
│   │   ├── client.go            # http transport, optional ApiKey header, error handling
│   │   ├── queries.go           # findDuplicateScenes, findScene, tag ops, mutations
│   │   └── types.go             # Scene, VideoFile, DuplicateGroup
│   ├── store/                   # SQLite persistence
│   │   ├── store.go             # CRUD
│   │   ├── schema.sql           # embedded via //go:embed
│   │   └── migrate.go           # idempotent migrations
│   ├── scan/
│   │   ├── scenes.go            # workflow A: findDuplicateScenes → store
│   │   └── files.go             # workflow B: findScenes(file_count>1) → store
│   ├── decide/
│   │   ├── scorer.go            # rule-chain scoring engine, shared
│   │   ├── rules_scenes.go      # scene-level rules (workflow A)
│   │   └── rules_files.go       # file-level rules (workflow B), incl. filename regex
│   ├── apply/
│   │   ├── tagger.go            # workflow A, Phase 1
│   │   ├── files.go             # workflow B: sceneUpdate(primary_file_id) + deleteFiles
│   │   ├── merger.go            # workflow A, Phase 2 (sceneMerge)
│   │   ├── deleter.go           # workflow A, Phase 2 (scenesDestroy)
│   │   └── fingerprints.go      # submitStashBoxFingerprints, both workflows
│   ├── config/config.go         # YAML loader, env-var resolution
│   └── report/report.go         # human-readable text + JSON output
├── go.mod / go.sum
├── PLAN.md                      # this file
└── README.md
```

## 6. Data model

### SQLite schema (initial)

```sql
CREATE TABLE scan_runs (
  id            INTEGER PRIMARY KEY,
  started_at    TEXT NOT NULL,
  finished_at   TEXT,
  distance      INTEGER NOT NULL,
  duration_diff REAL NOT NULL,
  group_count   INTEGER
);

CREATE TABLE groups (
  id              INTEGER PRIMARY KEY,
  scan_run_id     INTEGER NOT NULL REFERENCES scan_runs(id),
  signature       TEXT NOT NULL,        -- sorted scene IDs joined; stable across runs
  status          TEXT NOT NULL,        -- pending|decided|needs_review|applied|dismissed
  decision_reason TEXT,                 -- e.g. "metadata vs quality conflict"
  decided_at      TEXT,
  applied_at      TEXT,
  UNIQUE(scan_run_id, signature)
);

CREATE TABLE group_scenes (
  group_id         INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  scene_id         TEXT NOT NULL,       -- Stash scene ID
  role             TEXT NOT NULL,       -- keeper|loser|undecided
  -- snapshot of scoring inputs at scan time so reports work without Stash being up
  width            INTEGER,
  height           INTEGER,
  bitrate          INTEGER,
  framerate        REAL,
  codec            TEXT,
  file_size        INTEGER,
  duration         REAL,
  organized        INTEGER,
  has_stash_id     INTEGER,
  tag_count        INTEGER,
  performer_count  INTEGER,
  primary_path     TEXT,
  PRIMARY KEY (group_id, scene_id)
);

-- Workflow B: each multi-file scene becomes a "file group".
-- Reuses the same status state machine as `groups` for code reuse.
CREATE TABLE file_groups (
  id              INTEGER PRIMARY KEY,
  scan_run_id     INTEGER NOT NULL REFERENCES scan_runs(id),
  scene_id        TEXT NOT NULL,        -- the Stash scene that owns these files
  status          TEXT NOT NULL,        -- pending|decided|needs_review|applied|dismissed
  decision_reason TEXT,
  decided_at      TEXT,
  applied_at      TEXT,
  UNIQUE(scan_run_id, scene_id)
);

CREATE TABLE file_group_files (
  file_group_id   INTEGER NOT NULL REFERENCES file_groups(id) ON DELETE CASCADE,
  file_id         TEXT NOT NULL,        -- Stash file ID
  role            TEXT NOT NULL,        -- keeper|loser|undecided
  is_primary      INTEGER NOT NULL,     -- was primary at scan time
  -- scoring inputs (the only fields that actually differ within a scene)
  basename        TEXT NOT NULL,
  path            TEXT NOT NULL,
  mod_time        TEXT,
  filename_quality INTEGER,             -- 0/1 for regex match
  -- snapshot of tech specs purely for the report (guaranteed equal across
  -- files in a single scene since Stash matches by oshash/md5)
  width           INTEGER,
  height          INTEGER,
  bitrate         INTEGER,
  framerate       REAL,
  codec           TEXT,
  file_size       INTEGER,
  PRIMARY KEY (file_group_id, file_id)
);

-- Persists user overrides ACROSS scan runs, keyed by group signature.
-- "I already told you these aren't actually duplicates, stop suggesting them."
-- Used for workflow A. Workflow B uses scene_id as the natural key.
CREATE TABLE user_decisions (
  signature       TEXT PRIMARY KEY,
  decision        TEXT NOT NULL,        -- not_duplicate|force_keeper|dismiss
  keeper_scene_id TEXT,
  decided_at      TEXT NOT NULL,
  notes           TEXT
);

-- Tracks which scenes we've already submitted fingerprints for, so re-runs
-- don't spam stash-box.
CREATE TABLE fingerprint_submissions (
  scene_id        TEXT NOT NULL,
  stash_box_index INTEGER NOT NULL,     -- which configured stash-box endpoint
  submitted_at    TEXT NOT NULL,
  PRIMARY KEY (scene_id, stash_box_index)
);
```

The `signature` column (sorted joined scene IDs) is the key to making re-runs
sane — if you `scan` next month and the same group of scenes shows up again,
we recognize it and apply your previous overrides automatically.

## 7. Configuration (YAML)

Default file path: `./config.yaml` (overridable with `--config`).

```yaml
stash:
  url: http://localhost:9999
  api_key_env: STASH_API_KEY     # always read from env, never stored in config

scan:
  phash_distance: 4
  duration_diff_seconds: 1.0

scoring:
  # === Workflow A: cross-scene scoring (rules apply to whole scenes) ===
  scenes:
    # Lexicographic — first non-tied rule wins. If all tie → needs_review.
    rules:
      - has_stash_id        # bool, true wins (scene has stash-box metadata)
      - organized           # bool, true wins
      - resolution          # int (w*h), higher wins
      - bitrate             # int, higher wins
      - codec_preference    # ranked list below
      - file_size           # int, higher wins (tiebreaker)
      - tag_count           # int, more wins
      - path_priority       # see below

  # === Workflow B: within-scene file scoring ===
  # NOTE: tech specs (resolution/bitrate/codec/size) are deliberately NOT in
  # this rule chain. Stash attaches files to a scene only by oshash/md5 match
  # (verified in v0.31.0 pkg/scene/scan.go), so all files in a single scene
  # are byte-equivalent and tech specs are guaranteed identical. The only
  # meaningful differences are filename, path, and mod_time.
  files:
    rules:
      - filename_quality    # regex match — primary signal
      - path_priority       # secondary: which library folder
      - mod_time            # tiebreaker: newer wins
    filename_quality:
      # Default matches: YYYY-MM-DD_<anything>_<resolution>.<ext>
      # The pattern intentionally does NOT try to parse the structure — it
      # just matches the shape. So filenames where performers are omitted
      # (e.g. when there are too many) like `2023-10-24_-Group.Title_1080p.mp4`
      # still match because `.+` is permissive. Tweak per library if needed.
      pattern: '^\d{4}[-._]\d{2}[-._]\d{2}_.+_(480|540|720|1080|1440|2160|4k)p?\.[A-Za-z0-9]+$'

  codec_priority: [av1, hevc, h264, vp9]   # earlier = better
  path_priority:                            # earlier = better
    - /sorted
    - /library
    - /inbox

review_policy:
  # Mark group needs_review (don't auto-decide) when:
  flag_metadata_quality_conflict: true   # metadata-rich scene loses on quality

apply:
  scenes:
    default_action: tag
    loser_tag: _dedupe_loser
    keeper_tag: _dedupe_keeper
  files:
    # No safe "tag" analog for files. Phase 1 default is report-only.
    # `--commit` performs sceneUpdate(primary_file_id) + deleteFiles.
    default_action: report

stash_box_fingerprints:
  # When --submit-fingerprints is passed AND --commit is in effect, submit
  # the keeper scene's fingerprints to all matching stash-box endpoints.
  # Off by default. Tracked in SQLite to avoid duplicate submissions.
  enabled: false

merge:
  # Policy for computing the union of scene-level metadata before calling
  # sceneMerge. Stash does NOT do this for us in v0.31.0.
  scene_level:
    # Multi-value fields: union the loser values into the keeper.
    tags:        union
    performers:  union
    urls:        union
    stash_ids:   union
    galleries:   union
    groups:      union
    # Scalar fields: keeper wins; if keeper is empty, take from loser.
    title:       prefer_keeper_then_loser
    details:     prefer_keeper_then_loser
    director:    prefer_keeper_then_loser
    code:        prefer_keeper_then_loser
    studio_id:   prefer_keeper_then_loser
    date:        prefer_keeper_then_loser
    # Numeric / bool: take the "better" value.
    rating100:   max
    organized:   any_true
  history:
    play_history: true   # combine watch dates into keeper
    o_history:    true   # combine o dates into keeper
  post_merge_file_cleanup:
    # After sceneMerge, the keeper scene has multiple files. Run workflow B
    # on it to swap to the best file as primary and deleteFiles for the
    # losers. Strongly recommended — this is what actually reclaims disk space.
    enabled: true
```

## 8. CLI surface

Subcommands are split by workflow under `scenes` and `files` to keep the two
problems clearly distinct.

```
stash-janitor config init                         # write a default config.yaml

# === Workflow A: cross-scene duplicates ===
stash-janitor scenes scan [--distance 4] [--duration-diff 1.0]
                                        # populate sqlite from findDuplicateScenes
stash-janitor scenes status                       # groups, decided, needs_review, reclaimable bytes
stash-janitor scenes report [--needs-review|--decided|--all] [--json]
stash-janitor scenes apply --action tag                     # Phase 1; --dry-run is DEFAULT
stash-janitor scenes apply --action tag --commit            # mutate Stash (no prompt)
stash-janitor scenes apply --action merge                   # Phase 1; dry-run preview
stash-janitor scenes apply --action merge --commit          # Phase 1; INTERACTIVE PROMPT
stash-janitor scenes apply --action merge --commit --yes    # Phase 1; bypass prompt (scripted)
stash-janitor scenes apply --action delete --commit         # Phase 2 (also prompted)
stash-janitor scenes mark --signature <sig> --as not_duplicate|dismiss

# === Workflow B: within-scene multi-file cleanup ===
stash-janitor files scan                          # findScenes(file_count > 1) → sqlite
stash-janitor files status
stash-janitor files report [--needs-review|--decided|--all] [--json]
stash-janitor files apply                         # Phase 1: report only by default
stash-janitor files apply --commit                # Phase 1.5: sceneUpdate + deleteFiles
stash-janitor files mark --scene-id <id> --as keep_all|dismiss

# === Cross-cutting flags (apply to both workflows) ===
--submit-fingerprints                   # Opt-in. After successful --commit,
                                        # submitStashBoxFingerprints for any
                                        # keeper scene with a stash_id.
```

Global flags: `--config <path>`, `--db <path>`, `-v/-vv` for log verbosity.

## 9. Safety model

1. **Default action is `tag`** — irreversibility costs zero, you review in
   Stash's UI.
2. **`--dry-run` is the default** for `apply`. You must explicitly pass
   `--commit` to mutate Stash. This protection is **never** removed in any
   phase.
3. **Destructive actions require an interactive confirmation prompt** when
   committing. The prompt shows: action type, group count, scene count,
   reclaimable bytes, and a summary of what will happen. User must type `YES`
   verbatim to proceed. Triggered by:
   - `scenes apply --action merge --commit`
   - `scenes apply --action delete --commit` (Phase 2)
   - `files apply --commit` (Phase 1.5) when more than N files would be
     deleted (configurable threshold)
   The `--yes` flag bypasses the prompt for scripted/cron use, but `--commit`
   is still required separately, so a typo cannot trigger destruction.
4. **Idempotent** — re-running any apply with `--commit` is safe; tags are
   sets, applied groups are skipped via `applied_at`.
5. **No filesystem touches** — every destructive op is a Stash mutation, so
   the container's view of paths is the only one that matters.
6. **API key never logged, never stored in config**, only read from env.
7. **`needs_review` groups are never applied automatically** in any mode. You
   either resolve them via `stash-janitor mark` or wait for the Phase-2 TUI.
8. **Merge metadata union is computed by us, not by Stash.** Before each
   `sceneMerge` call, we fetch full metadata for keeper + losers, compute the
   union per the configured policy, and pass it as the `values` field to
   `sceneMerge`. Without this, loser metadata would be silently lost.

## 10. Phased roadmap

### Phase 1 — MVP (both workflows, tag + merge)
1. Project skeleton, `go.mod`, cobra wiring, slog setup
2. Config loader + `stash-janitor config init`
3. Stash GraphQL client: auth, error mapping, all queries/mutations needed for
   Phase 1 (`findDuplicateScenes`, `findScenes`+filter, `findScene` for full
   details, `findTags`+`tagCreate`, `bulkSceneUpdate`, `sceneUpdate`,
   `sceneMerge`, `deleteFiles`)
4. SQLite store: embedded schema, migrations, both group tables, user_decisions
5. **Workflow A — `stash-janitor scenes scan`** end-to-end with streaming JSON decode
6. Scoring engine (shared) + scene rules from config
7. `stash-janitor scenes status` + `stash-janitor scenes report` (text + `--json`)
8. `stash-janitor scenes apply --action tag` — dry-run default, `--commit`, idempotent
9. **Merge metadata-union engine** + `stash-janitor scenes apply --action merge` —
   dry-run default, `--commit` requires interactive `YES` prompt or `--yes`,
   chains into post-merge file pruning (workflow B logic on the merged scene)
10. **Workflow B — `stash-janitor files scan`** end-to-end (`findScenes` with file_count
    filter, paginated)
11. File rules from config, including the regex-based `filename_quality`
12. `stash-janitor files status` + `stash-janitor files report`
13. `stash-janitor files apply` — report-only mode (Phase 1 default)
14. README with setup, both workflows, merge walkthrough, sample `config.yaml`

### Phase 1.5 — within-scene commit + fingerprints + marks
15. `stash-janitor files apply --commit` — `sceneUpdate(primary_file_id)` + `deleteFiles`,
    idempotent, marks `applied_at`, threshold-based confirmation prompt
16. `submitStashBoxFingerprints` integration + `--submit-fingerprints` flag +
    `fingerprint_submissions` tracking table
17. `stash-janitor scenes mark` and `stash-janitor files mark` for persistent overrides

### Phase 2 — Cross-scene power tools + TUI
18. `stash-janitor scenes apply --action delete` (`scenesDestroy(delete_file: true)`) —
    last-resort hard delete for cases where merge is inappropriate
19. `stash-janitor review` interactive TUI (bubbletea) — covers both workflows
20. Per-group manual override of the auto-picked keeper

### Phase 3 — Stretch
21. Identify metadata-less orphans via stash-box phash lookup
22. Optional: Stash plugin / web UI

## 10a. Merge apply pipeline (Phase 1)

For each duplicate group with a clear keeper (status = `decided`), the merge
action does the following sequence in a single Stash transaction context:

1. **Fetch full metadata** for keeper + all losers (`findScene` per id, with
   files + relationships).
2. **Compute the union** per `merge.scene_level` policy. Build a
   `SceneUpdateInput`. Skip fields where keeper already has the right value
   to keep the partial update minimal.
3. **Dry-run path:** print a per-group preview showing
   - keeper id + path + key tech specs
   - loser ids + paths + key tech specs
   - the diff of metadata that will be added to the keeper
   - which files will end up attached to the keeper
   - the file that will become primary after post-merge cleanup
   - which files will be deleted after cleanup
   - bytes reclaimed
4. **Commit path** (only after `YES` confirmation):
   - Call `sceneMerge(source: [losers], destination: keeper, values: union,
     play_history: true, o_history: true)`. After this, the keeper scene has
     all the files attached and source scenes are gone.
   - If `merge.post_merge_file_cleanup.enabled` (default true): re-run the
     workflow B file scoring on the now-merged scene. Pick the winning file.
     If different from current primary, call
     `sceneUpdate(primary_file_id: winner)`. Then call `deleteFiles(ids:
     [losers...])` to delete the loser files from disk.
   - Mark the group `applied_at = now`.
5. On any error mid-pipeline, log the error, mark the group as failed in
   SQLite (with reason), and continue with the next group.

This is more involved than the tag flow but it's the actual answer for the
"two scenes with same content + slightly different fingerprints + maybe
different metadata" problem.

## 11. Open risks

- **`findDuplicateScenes` at 60k scenes.** v0.31.0's signature is
  `findDuplicateScenes(distance, duration_diff): [[Scene!]!]!` — it returns
  *all* duplicate groups in a single response, no pagination. At the user's
  library size (~60k scenes) this could be a multi-MB payload, depending on
  how many duplicate groups exist at `distance: 4`.
  Mitigations baked into the plan:
  1. **Streaming JSON decode** — `encoding/json.Decoder` over the response
     body, upserting each group into SQLite as it's parsed; never hold the
     whole result in memory.
  2. **`--max-groups N` flag on `scenes scan`** — fetches the response but
     stops processing after N groups, lets the user iterate
     scan/review/apply in chunks.
  3. **Long-request watchdog** — if the request hasn't started returning
     bytes within 30 seconds, log a warning suggesting the user retry with
     a smaller `--distance` or chunk by `--duration-diff`.
- **Tag creation race.** First run needs to create `_dedupe_loser` /
  `_dedupe_keeper` tags. Trivial but worth a unit test for the
  find-or-create logic.
- **`sceneMerge` does NOT auto-union scene metadata** — verified by reading
  `pkg/scene/merge.go` in v0.31.0. Mitigation: our merge action computes the
  union itself per the configured policy and passes it via the `values` field.
  Without this, loser metadata (tags/performers/stash_ids/etc.) is silently
  lost. See section 10a for the full pipeline.
- **Stash version drift.** We pin to `v0.31.0`'s schema. If you upgrade, we
  re-verify the queries we use.

## 12. TODO — Phase 1 (in order)

**Foundations**
1. [ ] `go mod init`, project skeleton, cobra root command, slog
2. [ ] Config types + YAML loader + `stash-janitor config init`
3. [ ] Stash GraphQL client: transport, optional `ApiKey` header, error mapping
4. [ ] Stash queries/mutations needed for Phase 1: `findDuplicateScenes`,
       `findScenes` (with `file_count` filter), `findScene` (full detail incl.
       `files`, tags, performers, stash_ids, urls, etc.), `findTags`+
       `tagCreate`, `bulkSceneUpdate`, `sceneUpdate`, `sceneMerge`,
       `deleteFiles`
5. [ ] SQLite store: embedded schema, migrations, scene-group + file-group
       CRUD, user_decisions
6. [ ] Interactive confirmation prompt utility (`pkg/confirm`) — used by
       merge `--commit` and any future destructive action; supports `--yes`
       bypass

**Workflow A — cross-scene scan + tag**
7. [ ] `stash-janitor scenes scan` — pull duplicate groups via `findDuplicateScenes`,
       streaming JSON decode, `--max-groups N` flag for chunked iteration,
       30s watchdog warning, upsert into store, apply user_decisions overrides
8. [ ] Scoring engine (shared) + scene rules from config
9. [ ] Decider: mark scene groups `decided` / `needs_review` with reason
10. [ ] `stash-janitor scenes status` + `stash-janitor scenes report` (text + `--json`)
11. [ ] `stash-janitor scenes apply --action tag` — dry-run default, `--commit` to
        mutate, idempotent, marks `applied_at`

**Workflow A — cross-scene merge (the big one)**
12. [ ] Metadata-union engine: `pkg/merge/union.go` — given keeper + losers
        full metadata + policy, build a `SceneUpdateInput`
13. [ ] Merge apply pipeline (`pkg/apply/merger.go`): per group → fetch full
        metadata → union → sceneMerge → post-merge file cleanup → mark applied
14. [ ] `stash-janitor scenes apply --action merge` — dry-run default with rich preview
        (see section 10a step 3); `--commit` triggers confirmation prompt;
        `--yes` bypasses prompt
15. [ ] Failure handling: per-group error capture into SQLite, continue on next

**Workflow B — within-scene**
16. [ ] `stash-janitor files scan` — paginated `findScenes(file_count > 1)` with
        `find_filter.per_page` (default 100), fetch each scene's full file
        list, upsert file-groups into store. Captures only the scoring
        inputs (filename, path, mod_time) plus a snapshot of tech specs for
        the report. Tech specs are NOT used in scoring — see section 7.
17. [ ] File scoring rules: `filename_quality` (regex), `path_priority`,
        `mod_time`. NOT tech specs — they're guaranteed equal within a scene.
18. [ ] `stash-janitor files status` + `stash-janitor files report`
19. [ ] `stash-janitor files apply` — Phase 1: report-only default. Prints proposed
        primary swap + file deletions. No mutations until Phase 1.5.

**Wrap-up**
20. [ ] README + setup walkthrough for both workflows + merge walkthrough +
        sample `config.yaml`
21. [ ] (optional) End-to-end smoke test against a throwaway Stash
        docker-compose

## 13. TODO — Phase 1.5

1. [ ] `stash-janitor files apply --commit` — `sceneUpdate(primary_file_id)` +
       `deleteFiles`, idempotent, marks `applied_at`, threshold-based
       confirmation prompt
2. [ ] `fingerprint_submissions` SQLite table + migration
3. [ ] `--submit-fingerprints` flag — after a successful `--commit` on either
       workflow, call `submitStashBoxFingerprints` for any keeper scene that
       has at least one `stash_id`. Skip scenes already in
       `fingerprint_submissions`. Record on success.
4. [ ] `stash-janitor scenes mark` and `stash-janitor files mark` — persistent user overrides.
