# StashJanitor (`stash-janitor`)

A small CLI for finding and resolving duplicate video scenes in a
[Stash](https://github.com/stashapp/stash) library, safely.

`stash-janitor` solves two related problems:

- **Workflow A — cross-scene duplicates.** Two or more separate Stash scenes
  whose perceptual hashes are similar (the same content stored as different
  files, possibly different resolutions or encodes).
- **Workflow B — within-scene multi-file cleanup.** A single Stash scene that
  has more than one file attached because Stash auto-attaches re-detected
  files instead of creating a new scene.

Both workflows fetch their data from Stash, score every candidate against a
configurable rule chain, persist the decisions to a local SQLite cache, and
let you review everything before committing any mutation.

> **Default behavior is paranoid.** `apply` is dry-run by default. The
> destructive `--action merge` requires both `--commit` AND an interactive
> `YES` confirmation. The within-scene file workflow is report-only in
> Phase 1.

See [PLAN.md](PLAN.md) for the full design and the rationale behind every
locked-in decision.

## Status

All three workflows work end-to-end against Stash v0.31.0:

| Command | Status |
|---|---|
| `stash-janitor config init` / `stash-janitor config show` | ✅ |
| `stash-janitor scenes scan` / `status` / `report` | ✅ |
| `stash-janitor scenes apply --action tag\|merge\|delete` | ✅ |
| `stash-janitor scenes mark` (persistent overrides) | ✅ |
| `stash-janitor files scan` / `status` / `report` | ✅ |
| `stash-janitor files apply --commit` (primary swap + deleteFiles) | ✅ |
| `stash-janitor files mark` (persistent overrides) | ✅ |
| `stash-janitor orphans scan` / `status` / `report` | ✅ |
| `stash-janitor orphans apply --commit` (link to stash-box) | ✅ |
| `--submit-fingerprints` (all apply commands) | ✅ |

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

# 2. Optional: if your Stash has an API key, export it. Otherwise leave
#    the env var unset.
export STASH_API_KEY=...

# 3. Scan workflow A — cross-scene duplicates.
./stash-janitor scenes scan

# 4. See what was found.
./stash-janitor scenes status
./stash-janitor scenes report --filter decided

# 5. Apply tags (always dry-run by default).
./stash-janitor scenes apply --action tag           # preview
./stash-janitor scenes apply --action tag --commit  # actually tag

# 6. Scan workflow B — multi-file scenes.
./stash-janitor files scan
./stash-janitor files status
./stash-janitor files report --filter decided

# 7. (Optional) merge cross-scene duplicates and prune the resulting files.
./stash-janitor scenes apply --action merge           # preview
./stash-janitor scenes apply --action merge --commit  # interactive YES prompt
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
      pattern: '^\d{4}[-._]\d{2}[-._]\d{2}_.+_(480|540|720|1080|1440|2160|4k)p?\.[A-Za-z0-9]+$'

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

Merge mode does the full cross-scene consolidation in five steps per group:

1. Fetch full metadata for keeper + losers from Stash.
2. Compute the metadata union per the policy in `merge.scene_level`
   (multi-value fields union, scalar fields prefer keeper, rating100 max,
   organized any-true).
3. Call `sceneMerge` with the union as the `values` field — Stash's
   sceneMerge does NOT auto-union scene metadata, so we have to compute
   it ourselves.
4. The merged keeper now has every loser's files attached. Run a
   post-merge file scorer (resolution → bitrate → filename_quality → file_size
   → mod_time) and pick the best one as primary, then `deleteFiles` the
   rest from disk.
5. Mark the group applied.

Per-group failures don't abort the run — they're captured in the per-group
report and the loop continues.

## Workflow B — within-scene multi-file cleanup

Stash attaches multi-files to a scene only by `oshash`/`md5` match (verified
in `pkg/scene/scan.go` in v0.31.0), so all files within a single scene are
**byte-equivalent**. The scoring rules consider only filename, path, and
mod_time — tech specs are guaranteed identical.

```sh
./stash-janitor files scan                    # paginated find_filter under the hood
./stash-janitor files status
./stash-janitor files report --filter decided
./stash-janitor files apply                   # report-only in Phase 1
```

The default `filename_quality` regex matches a `YYYY-MM-DD_<anything>_<resolution>.<ext>`
pattern. Tweak it in `config.yaml` to match your library convention.

> **Phase 1 limitation:** workflow B is report-only. The `--commit` path
> (`sceneUpdate(primary_file_id) + deleteFiles`) lands in Phase 1.5.

## Safety model

1. **`--dry-run` is the default.** You must pass `--commit` to mutate
   anything. This protection is never removed.
2. **Default action is `tag`** — fully reversible.
3. **Destructive actions (merge, delete, files commit) require an
   interactive `YES` prompt** with a summary of what will happen.
   `--yes` bypasses the prompt for scripted use, but `--commit` is still
   required separately, so a typo cannot trigger destruction.
4. **Idempotent.** Re-running `apply --commit` is safe — applied groups
   are skipped via `applied_at`.
5. **No filesystem touches.** Every destructive op goes through Stash's
   GraphQL API, so the container's view of paths is the only one that
   matters. Works seamlessly with NAS / Docker setups.
6. **API key never logged, never stored in config**, only read from env.
7. **`needs_review` groups are never applied automatically.**

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
internal/cli/          — cobra command tree
internal/config/       — YAML loader (embedded defaults)
internal/stash/        — GraphQL client + query catalog
internal/store/        — SQLite persistence
internal/scan/         — workflow A/B scan orchestration
internal/decide/       — scene + file scoring engines
internal/merge/        — metadata union for sceneMerge
internal/apply/        — tag, merge, files-prune actions
internal/report/       — text + JSON output
internal/confirm/      — interactive YES prompt utility
```

## Where to find help

- [PLAN.md](PLAN.md) — full design, locked-in decisions, phased roadmap
- [internal/config/default.yaml](internal/config/default.yaml) — every config
  field documented inline
- `./stash-janitor help` — cobra-generated command tree
- `./stash-janitor scenes help`, `./stash-janitor files help` — workflow-specific help
