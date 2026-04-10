# StashJanitor

A small CLI for finding and resolving duplicate video scenes in a
[Stash](https://github.com/stashapp/stash) library, safely.

StashJanitor solves three related problems:

- **Workflow A — cross-scene duplicates.** Two or more separate Stash scenes
  whose perceptual hashes are similar (the same content stored as different
  files, possibly different resolutions or encodes).
- **Workflow B — within-scene multi-file cleanup.** A single Stash scene that
  has more than one file attached because Stash auto-attaches re-detected
  files instead of creating a new scene.
- **Workflow C — orphan metadata recovery.** Scenes with no stash-box
  metadata (no `stash_ids`). stash-janitor queries stash-box by phash, finds matches,
  and links them back so Stash's Scene Tagger can pull the full metadata.

All workflows fetch their data from Stash, score/match every candidate,
persist decisions to a local SQLite cache, and let you review everything
before committing any mutation.

> **Default behavior is paranoid.** `apply` is dry-run by default. All
> destructive actions require both `--commit` AND an interactive `YES`
> confirmation. `--yes` bypasses the prompt for scripted use.

See [PLAN.md](PLAN.md) for the full design and the rationale behind every
locked-in decision.

## Status

All three workflows work end-to-end against Stash v0.31.0:

| Command | Status |
|---|---|
| `stash-janitor config init` / `stash-janitor config show` | ✅ |
| `stash-janitor stats` (library dashboard) | ✅ |
| `stash-janitor scenes scan` / `status` / `report` | ✅ |
| `stash-janitor scenes apply --action tag\|merge\|delete` | ✅ |
| `stash-janitor scenes mark --group N` or `--signature` | ✅ |
| `stash-janitor files scan` / `status` / `report` | ✅ |
| `stash-janitor files apply --commit` (primary swap + deleteFiles) | ✅ |
| `stash-janitor files mark --scene-id` | ✅ |
| `stash-janitor orphans scan` / `status` / `report` | ✅ |
| `stash-janitor orphans scan --endpoint all` (multi-endpoint) | ✅ |
| `stash-janitor orphans apply --commit` (link to stash-box) | ✅ |
| `--submit-fingerprints` (all apply commands) | ✅ |
| `stash-janitor review` (interactive TUI) | ✅ |
| Per-loser "kept by: ..." explanations in reports | ✅ |
| Filename-info-loss safety net (workflow A) | ✅ |
| Rename-on-merge (preserve loser filename info) | ✅ |
| Filename-to-metadata extraction on merge | ✅ |
| Stale-cache detection in `stash-janitor stats` | ✅ |
| Orphans scan progress indicator with ETA | ✅ |
| `stash-janitor organize scan` / `report` / `apply` | ✅ |

See [MANUAL.md](MANUAL.md) for usage instructions and
[PLAN.md](PLAN.md) for the design and rationale.

## Requirements

- Go 1.22 or newer (verified on 1.24)
- A reachable Stash instance running v0.31.0 (other 0.31.x patch releases
  should work; older or newer minor versions are untested)
- A few hundred MB of free disk for the SQLite cache (proportional to
  library size — at 60k scenes the DB is ~50 MB)

## Build

```sh
git clone <repo>
cd StashJanitor
go build -o stash-janitor ./cmd/stash-janitor
```

The binary is statically linkable: the SQLite driver is the pure-Go
[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite), so there is
no cgo step and no runtime dependency on libsqlite.

## Quick start

```sh
# 1. Generate a config file (you can edit it after).
./stash-janitor config init

# 2. Optional: if your Stash has an API key, export it.
export STASH_API_KEY=...

# 3. See where you're at.
./stash-janitor stats

# 4. Scan and review cross-scene duplicates.
./stash-janitor scenes scan
./stash-janitor scenes report --filter decided | less

# 5. Apply (safest first pass: just tag).
./stash-janitor scenes apply --action tag --commit

# 6. Or go full merge (preserves metadata, renames files, reclaims disk):
./stash-janitor scenes apply --action merge --commit

# 7. Clean up multi-file scenes.
./stash-janitor files scan
./stash-janitor files report | less
./stash-janitor files apply --commit

# 8. Find metadata for orphan scenes via stash-box.
./stash-janitor orphans scan --max-scenes 500
./stash-janitor orphans report --filter matched
./stash-janitor orphans apply --commit
```

## Configuration

`stash-janitor config init` writes a fully-commented `config.yaml` next to the binary.
The most important sections:

```yaml
stash:
  url: http://your-stash:9999
  api_key_env: STASH_API_KEY     # name of the env var to read

scan:
  phash_distance: 4              # 0 = byte-equivalent, 4 = re-encodes,
                                 # >8 produces false positives
  duration_diff_seconds: 1.0     # critical false-positive filter

scoring:
  scenes:
    rules:
      - has_stash_id      # scene has stash-box metadata → wins
      - organized
      - resolution
      - bitrate
      - codec_preference
      - file_size
      - tag_count
      - path_priority
  files:
    rules:
      - filename_quality  # regex match → wins
      - path_priority
      - mod_time
    filename_quality:
      pattern: '^\d{4}[-._]\d{2}[-._]\d{2}_.+_(480|540|720|1080|1440|2160|2880|4320|[2468][kK])p?(_\d+)?\.[A-Za-z0-9]+$'

apply:
  scenes:
    default_action: tag           # safe default
    loser_tag: _dedupe_loser
    keeper_tag: _dedupe_keeper
```

The full default file is at
[`internal/config/default.yaml`](internal/config/default.yaml) — every
field is documented inline.

`stash-janitor config show` prints the *effective* config (defaults merged with your
overrides), which is helpful when debugging "is my override being applied?".

## Workflow A — cross-scene duplicates

Stash exposes
`findDuplicateScenes(distance, duration_diff)` which returns groups of
perceptually-similar scenes server-side. `stash-janitor scenes scan` consumes those
groups, scores each one with the configured rule chain, and writes the
results into the local SQLite cache.

```sh
./stash-janitor scenes scan --distance 4 --duration-diff 1.0
```

Useful flags:

- `--distance N` — phash hamming distance (default 4)
- `--duration-diff S` — max duration delta in seconds (default 1.0)
- `--max-groups N` — stop after N groups, useful for chunked iteration at
  large library scale (60k+ scenes)

Once scanned, the local cache is offline-readable:

```sh
./stash-janitor scenes status                              # summary + reclaimable bytes
./stash-janitor scenes report --filter all                 # every group
./stash-janitor scenes report --filter needs-review        # groups the scorer couldn't decide
./stash-janitor scenes report --filter decided --json      # machine-readable output
```

### Apply: tag mode (safe)

```sh
./stash-janitor scenes apply --action tag           # dry run
./stash-janitor scenes apply --action tag --commit  # actually tag in Stash
```

Tag mode finds-or-creates two tags (default `_dedupe_loser` and
`_dedupe_keeper`), uses `bulkSceneUpdate` to add them to the scenes,
and marks the local groups as applied. **No scenes are deleted.** You then
go to Stash's own UI, filter by `_dedupe_loser`, eyeball the suggestions,
and bulk-delete the ones you agree with.

### Apply: merge mode (destructive)

```sh
./stash-janitor scenes apply --action merge           # dry run
./stash-janitor scenes apply --action merge --commit  # interactive YES prompt
./stash-janitor scenes apply --action merge --commit --yes  # bypass prompt for cron
```

Merge mode does the full cross-scene consolidation per group:

1. Fetch full metadata for keeper + losers from Stash.
2. Compute the metadata union per the policy in `merge.scene_level`
   (multi-value fields union, scalar fields prefer keeper, rating100 max,
   organized any-true).
3. **Filename metadata extraction**: if the union still has empty title/date
   but a loser file has a structured filename (e.g. `2024-12-15_Performer-
   Title_1080p.mp4`), parse it and fill in the keeper's empty fields.
4. Call `sceneMerge` with the union as the `values` field — Stash's
   sceneMerge does NOT auto-union scene metadata, so we compute it.
5. The merged keeper now has every loser's files attached. Pick the best
   file as primary (resolution → bitrate → filename → file_size → mod_time).
6. **Rename-on-merge**: if the winning file has a junk basename but a loser
   had a structured one, rename the winner via `moveFiles` to use the
   loser's filename (with resolution token swapped to match the winner's
   actual height).
7. `deleteFiles` on the rest from disk. Mark the group applied.

Per-group failures don't abort the run — they're captured in the per-group
report and the loop continues.

## Workflow B — within-scene multi-file cleanup

Stash attaches multi-files to a scene only by `oshash`/`md5` match (verified
in `pkg/scene/scan.go` in v0.31.0), so all files within a single scene are
**byte-equivalent**. The scoring rules consider only filename, path, and
mod_time — tech specs are guaranteed identical.

```sh
./stash-janitor files scan
./stash-janitor files status
./stash-janitor files report --filter decided   # each loser shows WHY it lost
./stash-janitor files apply                     # dry run
./stash-janitor files apply --commit            # swap primary + delete losers
```

The default `filename_quality` regex matches
`YYYY-MM-DD_<anything>_<resolution>[_N].<ext>` including import suffixes
like `_1080p_1.mp4` and resolutions up to 8K.

## Workflow C — orphan metadata recovery

Scenes with no `stash_ids` (55% of a typical library) are "orphans". stash-janitor
queries stash-box by phash to find matches, then links them back to Stash.

```sh
./stash-janitor orphans scan --max-scenes 500     # start small (stash-box is slow)
./stash-janitor orphans report --filter matched   # review proposed matches
./stash-janitor orphans apply --commit            # write stash_ids back
./stash-janitor orphans scan                      # full library (re-runs skip cache)
./stash-janitor orphans scan --endpoint all       # query every configured stash-box
```

After applying, run Stash's built-in Scene Tagger to pull full metadata
(title, performers, tags, studio) from stash-box for the newly-linked scenes.

## Safety model

1. **`--dry-run` is the default.** You must pass `--commit` to mutate
   anything. This protection is never removed.
2. **Default action is `tag`** — fully reversible.
3. **Destructive actions (merge, delete, files commit) require an
   interactive `YES` prompt** with a summary of what will happen.
   `--yes` bypasses the prompt for scripted use, but `--commit` is still
   required separately, so a typo cannot trigger destruction.
4. **Filename-info-loss safety net.** When the scorer picks a keeper with
   a junk filename but a loser has a date-bearing structured name, the
   group is flagged `needs_review` instead of auto-decided. This prevents
   silently losing information encoded only in filenames.
5. **Idempotent.** Re-running `apply --commit` is safe — applied groups
   are skipped via `applied_at`.
6. **No filesystem touches.** Every destructive op goes through Stash's
   GraphQL API, so the container's view of paths is the only one that
   matters. Works seamlessly with NAS / Docker setups.
7. **API key never logged, never stored in config**, only read from env.
8. **`needs_review` groups are never applied automatically.**
9. **Stale-cache detection.** `stash-janitor stats` samples cached scene IDs and
   warns when they no longer exist in Stash.

## Tests

```sh
go test ./...                                    # unit tests, no Stash needed
SFX_INT_STASH_URL=http://your-stash:9999 \
  go test ./internal/stash/... -run Integration  # read-only integration
```

The integration tests are env-gated so `go test ./...` stays hermetic.

## Architecture

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

Module layout:

```
cmd/stash-janitor/main.go        — entry point
internal/cli/          — cobra command tree (scenes, files, orphans, stats, config)
internal/config/       — YAML loader (embedded defaults)
internal/stash/        — GraphQL client + query catalog (v0.31.0)
internal/store/        — SQLite persistence (schema v4 with migrations)
internal/scan/         — workflow A/B/C scan orchestration
internal/decide/       — scene + file scoring engines
internal/merge/        — metadata union + filename parser for sceneMerge
internal/apply/        — tag, merge, delete, files-prune, orphans-link, rename
internal/report/       — text + JSON output for all workflows
internal/confirm/      — interactive YES prompt utility
```

## Workflow D — file organization

Moves and renames files on disk (via Stash's `moveFiles` API) so your
library has a consistent, browsable directory structure based on metadata.

```sh
./stash-janitor organize scan                     # compute ideal paths for every scene
./stash-janitor organize report                   # show proposed moves
./stash-janitor organize report --filter conflict # naming collisions
./stash-janitor organize apply --commit           # actually move files via Stash
```

The default template produces your existing convention:
```
/data/<Performer>/<date>_<Performer>-<Title>_<resolution>.<ext>
```

For Whisparr compatibility, switch to a studio-first template in `config.yaml`:
```yaml
organize:
  path_template: "{studio}/{date} - {title}/{date}_{performer}-{title}_{resolution}.{ext}"
```

Scenes without the required metadata (performer + date by default) are
skipped and left where they are. Run `stash-janitor orphans scan` + Stash Scene
Tagger first to maximize metadata coverage, then organize.

## Interactive review (TUI)

`stash-janitor review` launches a terminal UI for walking through duplicate groups:

```sh
./stash-janitor review                          # decided + needs_review groups
./stash-janitor review --filter all             # everything
./stash-janitor review --filter needs-review    # focus on the hard cases
```

- **List mode**: `j`/`k` or arrows to navigate, `Enter` for detail, `q` to quit
- **Detail mode**: color-coded KEEP/drop roles, per-loser "kept by" explanations
  - `a` = accept auto-pick, `o` = override keeper, `n` = not_duplicate, `d` = dismiss
  - Override mode: arrow-select a different scene, `Enter` to confirm

Decisions save immediately to `stash-janitor.sqlite`.

## Where to find help

- [PLAN.md](PLAN.md) — full design, locked-in decisions, phased roadmap
- [MANUAL.md](MANUAL.md) — step-by-step user guide
- [internal/config/default.yaml](internal/config/default.yaml) — every config
  field documented inline
- `./stash-janitor help` — cobra-generated command tree
- `./stash-janitor scenes help`, `./stash-janitor files help`, `./stash-janitor orphans help` — workflow-specific help
