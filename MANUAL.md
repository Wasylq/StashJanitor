# stash-janitor User Manual

## Install

```sh
git clone <repo>
cd StashJanitor
go build -o stash-janitor ./cmd/stash-janitor

# Optional: move to your PATH
sudo mv stash-janitor /usr/local/bin/
```

No runtime dependencies. The binary is self-contained (pure-Go SQLite).

## First-time setup

```sh
# Generate a config file. Edit the URL to point at your Stash.
stash-janitor config init

# If your Stash uses an API key:
export STASH_API_KEY=your-key-here
```

The default `config.yaml` points at `http://localhost:9999`.
Edit `stash.url` if your Stash is elsewhere.

To verify connectivity and see your library at a glance:

```sh
stash-janitor stats
```

This shows Stash version, total scenes, library size, percentage with
metadata, orphan count, and the state of each workflow's local cache.
It also warns if cached scenes no longer exist in Stash (stale cache).

## The three workflows

stash-janitor has three independent workflows. Run whichever ones apply to you —
they don't depend on each other.

---

### Workflow A: Cross-scene duplicates

**Problem:** Two separate Stash scenes contain the same video, maybe at
different resolutions or bitrates.

**What stash-janitor does:** Asks Stash for perceptually-similar scene groups (via
phash), scores each group to pick a keeper, and lets you tag, merge, or
delete the losers.

#### 1. Scan

```sh
stash-janitor scenes scan
```

This calls `findDuplicateScenes` on your Stash. At distance=4 (default)
it catches re-encodes; at distance=0 it only catches exact phash matches.

Takes ~2 seconds even at 60k scenes. All results are stored locally in
`stash-janitor.sqlite` — no need to re-scan unless your library changes.

Useful flags:
- `--distance 0` — strictest (byte-equivalent phash only)
- `--distance 8` — aggressive (more matches, more false positives)
- `--max-groups 100` — process only the first 100 groups (useful for testing)

#### 2. Review

```sh
# One-line summary.
stash-janitor scenes status

# Detailed per-group report. Each loser shows WHY it lost.
stash-janitor scenes report --filter decided | less

# Only groups the tool couldn't auto-decide:
stash-janitor scenes report --filter needs-review

# Machine-readable:
stash-janitor scenes report --json > groups.json
```

Example output:
```
#2  status=decided
    signature: 22871|83186
      KEEP  scene 22871     3840x2160  h264    3.90 GiB  [stashID,tags=12,perf=1] /data/.../4k.mp4
      drop  scene 83186     1920x1080  h264    528.48 MiB  [stashID,tags=12,perf=1] /data/.../1080p.mp4
                ↳ kept by: higher resolution: 3840x2160 vs 1920x1080
    reclaimable: 528.48 MiB
```

#### 3. Override (optional)

If the tool picked wrong, tell it. You can use the group `#` from the
report (easier) or the full signature:

```sh
# By group number from the report:
stash-janitor scenes mark --group 224 --as not_duplicate

# Or by full signature:
stash-janitor scenes mark --signature "22871|83186" --as not_duplicate

# Pin a specific keeper (overrides the scorer's pick):
stash-janitor scenes mark --group 224 --as force_keeper --keeper 83186

# Then re-scan to apply the override:
stash-janitor scenes scan
```

#### 4. Apply

Three action modes, from safest to most aggressive:

**Tag (safest — no data loss possible):**
```sh
stash-janitor scenes apply --action tag              # preview (dry run)
stash-janitor scenes apply --action tag --commit     # actually tag in Stash
```
Adds `_dedupe_loser` / `_dedupe_keeper` tags. Then go to Stash UI, filter
by `_dedupe_loser`, eyeball, bulk-delete what you agree with.

**Merge (recommended — preserves all metadata):**
```sh
stash-janitor scenes apply --action merge              # preview
stash-janitor scenes apply --action merge --commit     # interactive YES prompt
stash-janitor scenes apply --action merge --commit --yes  # scripted/cron
```
Per group, the merge pipeline:
1. Computes a metadata union (tags, performers, stash_ids, urls, play
   history, rating, organized).
2. **Filename metadata extraction**: if the keeper has no title/date but a
   loser file has a structured filename like
   `2024-12-15_Performer-Title_1080p.mp4`, parses the date and title and
   fills them in before merging.
3. Calls `sceneMerge` with the computed union as `values`.
4. Picks the best file as primary (resolution → bitrate → filename → size).
5. **Rename-on-merge**: if the winning file has a junk basename but a loser
   had a structured one, renames the winner via `moveFiles` to use the
   loser's filename (with the resolution token swapped to match).
6. Deletes the loser files from disk.

**Delete (destructive — use only when losers have nothing to preserve):**
```sh
stash-janitor scenes apply --action delete --commit
```
Calls `scenesDestroy(delete_file: true)` on every loser. No metadata
preservation. No rename. You see a warning explaining why merge is
better in most cases.

**Submit fingerprints to stash-box (optional, any mode):**
```sh
stash-janitor scenes apply --action tag --commit --submit-fingerprints
```
After a successful commit, sends the keeper scenes' fingerprints to
stash-box so future users who have the same files get automatic matches.

---

### Workflow B: Within-scene multi-file cleanup

**Problem:** A single Stash scene has 2+ files attached (Stash auto-
attaches files with the same oshash/md5). Only one file is needed.

**What stash-janitor does:** Scores the files by filename quality, path priority,
and modification time. Picks the best one as primary, deletes the rest.

```sh
stash-janitor files scan
stash-janitor files status
stash-janitor files report | less                # each loser shows WHY it lost
stash-janitor files apply                        # preview
stash-janitor files apply --commit               # interactive YES → delete losers
stash-janitor files apply --commit --yes         # scripted
```

The filename scoring uses a configurable regex. The default matches your
`YYYY-MM-DD_Performer-Title_Resolution.ext` convention, including import
suffixes (`_1080p_1.mp4`) and resolutions up to 8K. Files matching the
convention win over files that don't. Each loser line in the report shows
`↳ kept by: matches filename convention (loser does not)` or whichever
rule decided.

Override a specific scene:
```sh
stash-janitor files mark --scene-id 56297 --as keep_all
```

---

### Workflow C: Orphans — stash-box metadata lookup

**Problem:** Many scenes have no stash-box metadata (no stash_ids). The
filename is the only clue to what the scene is.

**What stash-janitor does:** Enumerates orphan scenes (stash_id_count = 0), queries
your configured stash-box by phash, stores matches locally for review.
Applying writes the stash_id link back to Stash so Stash's built-in
Scene Tagger can then pull the full metadata.

```sh
# Start small — this is the slow workflow.
stash-janitor orphans scan --max-scenes 500

# See what stash-box recognized.
stash-janitor orphans report --filter matched

# Link the matches back to Stash.
stash-janitor orphans apply --commit

# Then in Stash UI: run the Scene Tagger to pull metadata
# (title, performers, tags, studio, date) from stash-box.

# Later, scan the rest. Re-runs skip already-queried scenes.
stash-janitor orphans scan --max-scenes 5000
stash-janitor orphans scan                       # full library (may take 30+ min)
```

Useful flags:
- `--endpoint URL` — query a specific stash-box (default: first one in Stash config)
- `--endpoint all` — query **every** configured stash-box in one pass
- `--batch-size 50` — bigger batches (faster, heavier on stash-box)
- `--batch-delay 100ms` — less sleep between batches
- `--rescan` — re-query previously-looked-up orphans

The endpoint is auto-discovered from Stash's configuration if not passed.

During the scan, a progress line shows processed/total, matched count,
and estimated time remaining:
```
  progress: 500/33249 (2%)  matched: 5  no_match: 395  skipped: 100  elapsed: 42s  eta: 35m
```

---

## The local cache (stash-janitor.sqlite)

Every scan stores its results in a SQLite file (default `./stash-janitor.sqlite`).
This serves three purposes:

1. **Speed.** Reports and apply commands read from SQLite, not Stash.
   You can scan once and review over multiple days.

2. **Resume.** `orphans scan --max-scenes 500` processes 500, then stops.
   Next run starts where you left off. `scenes scan` re-uses your `mark`
   overrides across runs.

3. **Idempotency.** Applied groups are recorded. Re-running `apply` is
   safe — already-applied groups are skipped.

You can delete `stash-janitor.sqlite` at any time and re-scan. All source-of-truth
data lives in Stash; the SQLite is a read cache and decision journal.

**Stale-cache detection:** `stash-janitor stats` samples a few cached scene IDs
and warns if any no longer exist in Stash. If you see the warning, just
re-scan (`stash-janitor scenes scan`, `stash-janitor files scan`).

## Configuration reference

`stash-janitor config init` generates a fully-commented `config.yaml`. Key sections:

| Section | What it controls |
|---|---|
| `stash.url` | Your Stash instance URL |
| `stash.api_key_env` | Name of env var holding the API key (not the key itself) |
| `scan.phash_distance` | How aggressively to match (0=exact, 4=re-encodes, 8=aggressive) |
| `scan.duration_diff_seconds` | Max duration delta — strong false-positive filter |
| `scoring.scenes.rules` | Ordered rule chain for picking keepers in workflow A |
| `scoring.files.rules` | Ordered rule chain for picking the best file in workflow B |
| `scoring.files.filename_quality.pattern` | Regex for "good" filenames |
| `scoring.codec_priority` | Codec ranking (default: av1 > hevc > h264 > vp9) |
| `scoring.path_priority` | Path prefix ranking (e.g. /sorted > /inbox) |
| `apply.scenes.loser_tag` | Tag name for losers (default: `_dedupe_loser`) |
| `merge.scene_level.*` | How to union metadata during merge (tags: union, title: prefer_keeper, etc.) |
| `merge.post_merge_file_cleanup.rename_winner_filename` | Rename the winner to the loser's structured basename (default: on) |
| `organize.base_dir` | Root directory Stash sees (e.g. `/data`) |
| `organize.path_template` | Template for ideal paths (see Workflow D section) |
| `organize.space_char` | Space replacement in filenames (default: `.`) |
| `organize.folder_space_char` | Space replacement in folders (default: ` `) |
| `organize.performer_strategy` | How to pick primary performer: `first_alphabetical` or `first_listed` |
| `organize.required_fields` | Fields a scene must have to be moved (default: `[performer, date]`) |
| `organize.rename_in_place` | Also rename files already in the right folder (default: `true`) |
| `orphans.write_stash_id_on_apply` | Auto-link stash_ids during orphans apply (default: `false`) |
| `orphans.write_metadata_on_apply` | Also write title/date from scrape (default: `false`) |

Run `stash-janitor config show` to see the *effective* config after merging defaults
with your overrides.

---

### Workflow D: Organize — move and rename files by metadata

**Problem:** Files are scattered across performer folders and tmp dirs
with inconsistent names. You want a clean, browsable structure based
on the metadata in Stash.

**What stash-janitor does:** Computes an ideal path for every scene using a
configurable template + Stash metadata. Compares ideal vs actual, then
proposes moves/renames via Stash's `moveFiles` API.

**Recommended order:** run the other workflows first to maximize metadata:
```sh
stash-janitor orphans scan          # find stash-box matches for orphans
# (link matches in Stash UI via Scene Tagger)
stash-janitor scenes scan           # find and resolve duplicates
stash-janitor scenes apply --action merge --commit
stash-janitor files scan            # clean up multi-file scenes
stash-janitor files apply --commit
# NOW organize:
stash-janitor organize scan
stash-janitor organize report | less
stash-janitor organize apply --commit
```

#### Scan

```sh
stash-janitor organize scan                    # all scenes (can take a while at 60k)
stash-janitor organize scan --max-scenes 500   # test with a small sample first
```

For each scene, stash-janitor checks: does the scene have the required metadata
fields (performer + date by default)? If yes, compute the ideal path
from the template. If no, skip (leave the file where it is).

#### Report

```sh
stash-janitor organize report                    # proposed moves + renames (default)
stash-janitor organize report --filter all       # everything including skips
stash-janitor organize report --filter conflict  # two scenes → same target path
stash-janitor organize report --filter skip      # files without enough metadata
```

Example output:
```
[move] scene 20
  from: /data/SandraR/2018-11-09_Amber.Chase-Mom.Cures.Milf.Addiction_720p.mp4
  to:   /data/Amber Chase/2018-11-09_Amber.Chase-Mom.Cures.Milf.Addiction_720p.mp4

[rename] scene 49
  from: /data/Addie Andrews/2020-02-03_Addie.Andrews-Complicit.Consumption_544p.mp4
  to:   /data/Addie Andrews/2020-02-03_Addie.Andrews-Complicit.Consumption_720p.mp4
```

#### Apply

```sh
stash-janitor organize apply                     # dry-run preview
stash-janitor organize apply --commit            # interactive YES → moveFiles via Stash
stash-janitor organize apply --commit --yes      # scripted
```

#### Templates

The default template matches your naming convention:
```yaml
organize:
  path_template: "{performer}/{date}_{performer}-{title}_{resolution}.{ext}"
```

Result: `/data/Aimee Waves/2024-12-15_Aimee.Waves-Your.Work.Crush_1080p.mp4`

For **Whisparr compatibility**, uncomment the studio-first template in
`config.yaml`:
```yaml
organize:
  path_template: "{studio}/{date} - {title}/{date}_{performer}-{title}_{resolution}.{ext}"
```

Result: `/data/New Sensations/2024-12-15 - Your Work Crush/2024-12-15_Aimee.Waves-Your.Work.Crush_1080p.mp4`

Available template variables:

| Variable | Example | Source |
|---|---|---|
| `{performer}` | `Aimee Waves` | Primary performer (first alphabetically) |
| `{studio}` | `New Sensations` | Studio name from Stash |
| `{title}` | `Your Work Crush` | Scene title (spaces → `space_char`) |
| `{date}` | `2024-12-15` | Scene date |
| `{year}` | `2024` | First 4 chars of date |
| `{resolution}` | `1080p`, `4k` | Derived from video height |
| `{ext}` | `mp4` | File extension |
| `{id}` | `42` | Stash scene ID (guaranteed unique) |

Folder parts use `folder_space_char` (default: space), filename parts
use `space_char` (default: dot). So `Aimee Waves` becomes the folder
`Aimee Waves/` but the filename part `Aimee.Waves`.

## Safety features

- **Filename-info-loss safety net.** When the scorer picks a higher-quality
  keeper but a loser has a date-bearing structured filename that the keeper
  doesn't, the group is flagged `needs_review` instead of auto-decided.
  This prevents silently losing information encoded only in filenames.
  The check uses a permissive "contains a YYYY-MM-DD style date" test —
  not the strict convention regex — so it catches non-standard filename
  formats too.

- **Rename-on-merge.** Even when the keeper file is the right quality
  choice, it might have a junk filename. The merge pipeline renames it
  to use the loser's structured basename (with the resolution token
  swapped) before deleting the losers. Configurable via
  `merge.post_merge_file_cleanup.rename_winner_filename` (default: on).

- **Filename-to-metadata extraction.** During merge, if neither keeper nor
  losers have a title or date set in Stash but a loser file matches the
  `YYYY-MM-DD_Performer-Title_Resolution.ext` convention, the date and
  title are parsed from the filename and set on the keeper before merging.

## Interactive review (TUI)

For a faster review experience than the CLI report + mark workflow:

```sh
stash-janitor review                          # shows decided + needs_review groups
stash-janitor review --filter all             # shows everything
stash-janitor review --filter needs-review    # focus on the hard cases
```

**List mode** — browse all groups at a glance:
- `j`/`k` or `↑`/`↓` to navigate
- `PgUp`/`PgDn` to jump 10 groups
- `Enter` to drill into detail
- `q` to quit

**Detail mode** — one group at a time with full context:
- Color-coded `KEEP` / `drop` roles for each scene
- Resolution, codec, file size, flags (stashID, organized, tags)
- Per-loser `↳ kept by: <reason>` explanation from the scorer
- Full file paths

**Actions in detail mode:**
- `a` = accept the auto-pick (marks the group as dismissed)
- `o` = override the keeper (arrow-select a different scene, Enter to confirm)
- `n` = mark as not_duplicate (future scans will skip this group)
- `d` = dismiss
- `↓` = advance to next group
- `Esc` = back to list

All decisions save to `stash-janitor.sqlite` immediately. They take effect on the
next `stash-janitor scenes scan`.

**Status bar** shows your position, counts (decided/review/applied/dismissed),
and total reclaimable bytes across all decided groups.

## Tips

- **Run `stash-janitor stats` first** to see your library size, metadata coverage,
  and the state of each workflow's local cache.
- **Start with `--action tag`** until you trust the scoring. Tags are
  reversible; merge/delete are not.
- **Use `--distance 4`** (default). Distance 8 catches more but produces
  false positives. Distance 0 is ultra-safe but misses re-encodes.
- **Pipe reports through `less -S`** to avoid line wrapping on narrow
  terminals.
- **The `--json` flag** on every report/status command makes it easy to
  pipe into `jq` or a script.
- **Re-scan after changes.** If you delete scenes in Stash's UI, the
  local cache becomes stale. `stash-janitor stats` will warn; just re-scan.
- **`stash-janitor.sqlite` is portable.** Copy it to another machine to browse
  reports offline.
- **`stash-janitor scenes mark --group N`** is faster than copying signatures. Use
  the `#N` from the report output directly.
