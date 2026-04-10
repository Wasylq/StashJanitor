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

To verify connectivity:

```sh
stash-janitor scenes status
# Should print "No scenes scan has been run yet."
# If it errors, check the URL and that Stash is running.
```

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

If the tool picked wrong, tell it:

```sh
# "These aren't actually duplicates."
stash-janitor scenes mark --signature "22871|83186" --as not_duplicate

# "Keep this specific scene, even though the scorer picked the other one."
stash-janitor scenes mark --signature "22871|83186" --as force_keeper --keeper 83186

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
Per group: computes a metadata union (tags, performers, stash_ids, play
history) → calls `sceneMerge` → picks the best file → renames it if a
loser had a better filename → deletes the rest from disk.

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
stash-janitor files report | less
stash-janitor files apply                        # preview
stash-janitor files apply --commit               # interactive YES → delete losers
stash-janitor files apply --commit --yes         # scripted
```

The filename scoring uses a configurable regex. The default matches your
`YYYY-MM-DD_Performer-Title_Resolution.ext` convention. Files matching
the convention win over files that don't.

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
- `--batch-size 50` — bigger batches (faster, heavier on stash-box)
- `--batch-delay 100ms` — less sleep between batches
- `--rescan` — re-query previously-looked-up orphans

The endpoint is auto-discovered from Stash's configuration if not passed.

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

Run `stash-janitor config show` to see the *effective* config after merging defaults
with your overrides.

## Tips

- **Start with `--action tag`** until you trust the scoring. Tags are
  reversible; merge/delete are not.
- **Use `--distance 4`** (default). Distance 8 catches more but produces
  false positives. Distance 0 is ultra-safe but misses re-encodes.
- **Pipe reports through `less -S`** to avoid line wrapping on narrow
  terminals.
- **The `--json` flag** on every report/status command makes it easy to
  pipe into `jq` or a script.
- **Re-scan after changes.** If you delete scenes in Stash's UI, the
  local cache becomes stale. Just `stash-janitor scenes scan` again — it's fast.
- **`stash-janitor.sqlite` is portable.** Copy it to another machine to browse
  reports offline.
