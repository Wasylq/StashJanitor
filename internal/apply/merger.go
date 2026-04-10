package apply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/decide"
	"github.com/Wasylq/StashJanitor/internal/merge"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// MergePlan is the precomputed work for `stash-janitor scenes apply --action merge`.
//
// Unlike TagPlan we cannot show a per-group metadata-union diff in the
// dry-run preview without fetching every keeper+losers from Stash, which
// would be slow at large library scale. The lite preview shows counts and
// reclaimable bytes; the rich per-group diff happens during ExecuteMerge
// when we already have the data fetched.
type MergePlan struct {
	Groups           []*store.SceneGroup
	TotalLosers      int
	ReclaimableBytes int64
}

// PlanMerge walks the store and builds a MergePlan from groups currently
// marked decided. It does not call Stash.
func PlanMerge(ctx context.Context, st *store.Store) (*MergePlan, error) {
	groups, err := st.ListSceneGroups(ctx, []string{store.StatusDecided})
	if err != nil {
		return nil, fmt.Errorf("listing decided groups: %w", err)
	}
	plan := &MergePlan{Groups: groups}
	for _, g := range groups {
		if g.AppliedAt != nil {
			continue
		}
		for _, s := range g.Scenes {
			if s.Role == store.RoleLoser {
				plan.TotalLosers++
				plan.ReclaimableBytes += s.FileSize
			}
		}
	}
	return plan, nil
}

// PrintMergePlan writes the human-readable summary used by both dry-run
// and the commit-mode confirmation prompt's "about to do this" header.
func PrintMergePlan(w io.Writer, p *MergePlan, commit bool) error {
	mode := "DRY RUN"
	if commit {
		mode = "COMMIT"
	}
	fmt.Fprintf(w, "=== stash-janitor scenes apply --action merge (%s) ===\n", mode)
	fmt.Fprintf(w, "Decided groups in scope:  %d\n", len(p.Groups))
	fmt.Fprintf(w, "Loser scenes to merge:    %d  (will be folded into their keepers and destroyed)\n", p.TotalLosers)
	fmt.Fprintf(w, "Reclaimable bytes:        %s  (after post-merge file pruning)\n",
		confirm.HumanBytes(p.ReclaimableBytes))
	if len(p.Groups) == 0 {
		fmt.Fprintln(w, "\nNothing to do. Try `stash-janitor scenes scan` first.")
		return nil
	}
	fmt.Fprintln(w, "\nMerge pipeline per group:")
	fmt.Fprintln(w, "  1. fetch full metadata for keeper + losers")
	fmt.Fprintln(w, "  2. compute scene-level metadata union (tags, performers, stash_ids, ...)")
	fmt.Fprintln(w, "  3. call sceneMerge with the union as `values`")
	fmt.Fprintln(w, "  4. on the merged scene, pick the best file as primary and deleteFiles the rest")
	fmt.Fprintln(w, "  5. mark the group applied")
	if !commit {
		fmt.Fprintln(w, "\nThis was a dry run. Re-run with --commit to mutate Stash.")
		fmt.Fprintln(w, "--commit triggers an interactive YES confirmation; --yes bypasses it.")
	}
	return nil
}

// MergeReport is the per-group outcome of ExecuteMerge.
type MergeReport struct {
	GroupID           int64
	KeeperSceneID     string
	LoserSceneIDs     []string
	Status            string // "success" | "skipped" | "failed"
	Error             string
	UnionedFields     []merge.FieldDiff
	NewPrimaryFileID  string
	PrimaryWasSwapped bool
	// RenamedTo is set when post-merge rename swapped the winner file's
	// basename to a structured name derived from a loser. Empty otherwise.
	RenamedTo         string
	FilesDeletedCount int
	BytesReclaimed    int64
}

// ExecuteMerge runs the full merge pipeline for every group in the plan.
// Returns one MergeReport per group attempted (success or failure). The
// outer error is non-nil only for catastrophic failures (e.g. nil deps);
// per-group failures are recorded in the reports and the loop continues.
//
// Caller is responsible for the YES prompt: this function does not gate
// itself on confirmation. The cli layer prints the plan, prompts, then
// calls ExecuteMerge.
func ExecuteMerge(
	ctx context.Context,
	c *stash.Client,
	st *store.Store,
	cfg *config.Config,
	scorer *decide.FileScorer,
	plan *MergePlan,
) ([]*MergeReport, error) {
	if c == nil || st == nil || cfg == nil || scorer == nil || plan == nil {
		return nil, errors.New("ExecuteMerge: nil dependency")
	}

	reports := make([]*MergeReport, 0, len(plan.Groups))
	for _, g := range plan.Groups {
		report := &MergeReport{GroupID: g.ID}
		reports = append(reports, report)

		if g.AppliedAt != nil {
			report.Status = "skipped"
			report.Error = "already applied"
			continue
		}

		// Identify keeper and losers from the snapshot.
		var (
			keeperID string
			loserIDs []string
		)
		for _, s := range g.Scenes {
			switch s.Role {
			case store.RoleKeeper:
				keeperID = s.SceneID
			case store.RoleLoser:
				loserIDs = append(loserIDs, s.SceneID)
				report.BytesReclaimed += s.FileSize
			}
		}
		report.KeeperSceneID = keeperID
		report.LoserSceneIDs = loserIDs
		if keeperID == "" || len(loserIDs) == 0 {
			report.Status = "failed"
			report.Error = "group is missing keeper or losers"
			continue
		}

		if err := executeOneMerge(ctx, c, st, cfg, scorer, g, keeperID, loserIDs, report); err != nil {
			slog.Error("merge failed for group; continuing",
				"group_id", g.ID, "keeper", keeperID, "error", err)
			report.Status = "failed"
			report.Error = err.Error()
			continue
		}
		report.Status = "success"
		if err := st.MarkSceneGroupApplied(ctx, g.ID); err != nil {
			report.Status = "failed"
			report.Error = "mutation succeeded but mark-applied failed: " + err.Error()
		}
	}
	return reports, nil
}

// executeOneMerge handles a single group's pipeline. Pulled out so the
// happy/error paths in ExecuteMerge stay readable.
func executeOneMerge(
	ctx context.Context,
	c *stash.Client,
	st *store.Store,
	cfg *config.Config,
	scorer *decide.FileScorer,
	g *store.SceneGroup,
	keeperID string,
	loserIDs []string,
	report *MergeReport,
) error {
	// Step 1: fetch full metadata for keeper + losers.
	keeper, err := c.FindScene(ctx, keeperID)
	if err != nil {
		return fmt.Errorf("fetching keeper %s: %w", keeperID, err)
	}
	if keeper == nil {
		return fmt.Errorf("keeper scene %s not found in stash (was it deleted since last scan?)", keeperID)
	}

	losers := make([]*stash.Scene, 0, len(loserIDs))
	for _, lid := range loserIDs {
		l, err := c.FindScene(ctx, lid)
		if err != nil {
			return fmt.Errorf("fetching loser %s: %w", lid, err)
		}
		if l == nil {
			// Loser already gone — log and continue. Treat as a partial merge.
			slog.Warn("loser scene already gone; skipping it",
				"loser_id", lid, "group_id", g.ID)
			continue
		}
		losers = append(losers, l)
	}
	if len(losers) == 0 {
		return errors.New("no losers remain to merge (all already deleted?)")
	}

	// Step 2: compute the metadata union.
	union, err := merge.BuildUnion(keeper, losers, cfg)
	if err != nil {
		return fmt.Errorf("computing metadata union: %w", err)
	}
	report.UnionedFields = union.Diffs

	// Step 2b: filename metadata extraction. If the union still has empty
	// title/date (neither keeper nor losers had them in Stash), try to
	// parse them from the files' basenames. This is a last-resort source
	// that catches the common case where the filename IS the metadata.
	if union.Vals == nil {
		union.Vals = &stash.SceneUpdateVals{}
	}
	allFiles := collectAllFiles(keeper, losers)
	if md := merge.ExtractMetadataFromFiles(allFiles); md != nil {
		if union.Vals.Title == nil && keeper.Title == "" && md.Title != "" {
			s := md.Title
			union.Vals.Title = &s
			union.Diffs = append(union.Diffs, merge.FieldDiff{
				Field: "title", Action: "set (from filename)", Details: truncateStr(md.Title, 60),
			})
		}
		if union.Vals.Date == nil && keeper.Date == "" && md.Date != "" {
			s := md.Date
			union.Vals.Date = &s
			union.Diffs = append(union.Diffs, merge.FieldDiff{
				Field: "date", Action: "set (from filename)", Details: md.Date,
			})
		}
	}
	// If we didn't actually set anything, reset Vals to nil so the
	// sceneMerge call doesn't send an empty values block.
	if union.Vals.Title == nil && union.Vals.Date == nil &&
		union.Vals.TagIDs == nil && union.Vals.PerformerIDs == nil &&
		union.Vals.URLs == nil && union.Vals.StashIDs == nil &&
		union.Vals.GalleryIDs == nil && union.Vals.Details == nil &&
		union.Vals.Director == nil && union.Vals.Code == nil &&
		union.Vals.StudioID == nil && union.Vals.Rating100 == nil &&
		union.Vals.Organized == nil {
		union.Vals = nil
	}
	report.UnionedFields = union.Diffs

	// Step 3: call sceneMerge with the computed values.
	srcIDs := make([]string, 0, len(losers))
	for _, l := range losers {
		srcIDs = append(srcIDs, l.ID)
	}
	mergeInput := stash.SceneMergeInput{
		Source:      srcIDs,
		Destination: keeper.ID,
		Values:      union.Vals,
		PlayHistory: cfg.Merge.History.PlayHistory,
		OHistory:    cfg.Merge.History.OHistory,
	}
	if err := c.SceneMerge(ctx, mergeInput); err != nil {
		return fmt.Errorf("sceneMerge: %w", err)
	}

	// Step 4: post-merge file cleanup. The keeper now has every loser's
	// files attached. Pick the best one and delete the rest from disk.
	if !cfg.Merge.PostMergeFileCleanup.Enabled {
		return nil
	}
	merged, err := c.FindScene(ctx, keeper.ID)
	if err != nil {
		return fmt.Errorf("re-fetching merged scene: %w", err)
	}
	if merged == nil || len(merged.Files) == 0 {
		return errors.New("merged scene has no files (this should never happen)")
	}
	if len(merged.Files) == 1 {
		// Only one file — nothing to prune.
		return nil
	}

	winnerIdx, reason := scorer.PickPostMergeKeeper(merged.Files)
	if winnerIdx == -1 {
		// Tied — leave the multi-file scene alone, the user can use
		// `stash-janitor files scan` later to deal with it manually.
		slog.Warn("post-merge file pick was a tie; leaving multi-file scene for files workflow",
			"scene_id", merged.ID, "reason", reason)
		return nil
	}
	winner := merged.Files[winnerIdx]
	report.NewPrimaryFileID = winner.ID

	currentPrimary := merged.Files[0]
	if winner.ID != currentPrimary.ID {
		report.PrimaryWasSwapped = true
		if err := c.SetPrimaryFile(ctx, merged.ID, winner.ID); err != nil {
			return fmt.Errorf("setting primary file: %w", err)
		}
	}

	// Rename-on-merge: if the winner has a junk basename and any loser has
	// a structured one, rename the winner to use a basename derived from
	// the loser (with the resolution token swapped to match the winner's
	// actual height). This preserves the human-readable info encoded in
	// the loser's filename. Errors here are non-fatal — log and continue
	// to the deletion step so we don't leave the scene in a half-applied
	// state.
	if cfg.Merge.PostMergeFileCleanup.RenameWinnerFilename {
		if newBasename := pickRenameTarget(scorer, merged.Files, winnerIdx); newBasename != "" && newBasename != winner.Basename {
			if err := c.RenameFile(ctx, winner.ID, newBasename); err != nil {
				slog.Warn("rename winner file failed; continuing without rename",
					"scene_id", merged.ID, "file_id", winner.ID, "new_basename", newBasename, "error", err)
			} else {
				report.RenamedTo = newBasename
				slog.Info("renamed winner file to preserve loser filename info",
					"scene_id", merged.ID, "file_id", winner.ID, "new_basename", newBasename)
			}
		}
	}

	var loserFileIDs []string
	for i, f := range merged.Files {
		if i == winnerIdx {
			continue
		}
		loserFileIDs = append(loserFileIDs, f.ID)
	}
	if err := c.DeleteFiles(ctx, loserFileIDs); err != nil {
		return fmt.Errorf("deleting loser files: %w", err)
	}
	report.FilesDeletedCount = len(loserFileIDs)

	return nil
}

// collectAllFiles aggregates every VideoFile from the keeper and losers
// into a single slice, used by filename metadata extraction which scans all
// files for the first parseable basename.
func collectAllFiles(keeper *stash.Scene, losers []*stash.Scene) []stash.VideoFile {
	out := make([]stash.VideoFile, 0, len(keeper.Files))
	out = append(out, keeper.Files...)
	for _, l := range losers {
		out = append(out, l.Files...)
	}
	return out
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// pickRenameTarget chooses a target basename for the winner file based on
// the losers' filenames. Returns "" when no rename should happen:
//
//   - winner already has a structured basename (matches the file scorer's
//     filename_quality regex) — nothing to gain
//   - no loser has a structured basename — nothing to derive from
//
// Otherwise the first loser with a structured basename is used as the
// source, and rebuildBasenameForFile swaps in the winner's resolution
// and extension.
func pickRenameTarget(scorer *decide.FileScorer, files []stash.VideoFile, winnerIdx int) string {
	if winnerIdx < 0 || winnerIdx >= len(files) {
		return ""
	}
	winner := &files[winnerIdx]
	if scorer.ClassifyFilename(winner.Basename) == 1 {
		// Winner already has a good name; do nothing.
		return ""
	}
	for i := range files {
		if i == winnerIdx {
			continue
		}
		if scorer.ClassifyFilename(files[i].Basename) == 1 {
			return rebuildBasenameForFile(files[i].Basename, winner)
		}
	}
	return ""
}

// PrintMergeReports writes a summary of the per-group outcomes after
// ExecuteMerge has finished.
func PrintMergeReports(w io.Writer, reports []*MergeReport) error {
	if len(reports) == 0 {
		return nil
	}
	var (
		ok      int
		failed  int
		skipped int
		renamed int
		bytes   int64
	)
	for _, r := range reports {
		switch r.Status {
		case "success":
			ok++
			bytes += r.BytesReclaimed
			if r.RenamedTo != "" {
				renamed++
			}
		case "failed":
			failed++
		case "skipped":
			skipped++
		}
	}
	fmt.Fprintf(w, "\n=== merge complete ===\n")
	fmt.Fprintf(w, "  successes:        %d\n", ok)
	fmt.Fprintf(w, "  failures:         %d\n", failed)
	fmt.Fprintf(w, "  skipped:          %d\n", skipped)
	fmt.Fprintf(w, "  files renamed:    %d  (winner basename swapped to preserve loser filename info)\n", renamed)
	fmt.Fprintf(w, "  bytes reclaimed:  %s\n", confirm.HumanBytes(bytes))
	if renamed > 0 {
		fmt.Fprintln(w, "\nRenamed files:")
		for _, r := range reports {
			if r.RenamedTo != "" {
				fmt.Fprintf(w, "  group #%d (keeper %s): → %s\n", r.GroupID, r.KeeperSceneID, r.RenamedTo)
			}
		}
	}
	if failed > 0 {
		fmt.Fprintln(w, "\nFailed groups:")
		for _, r := range reports {
			if r.Status == "failed" {
				fmt.Fprintf(w, "  group #%d (keeper %s): %s\n", r.GroupID, r.KeeperSceneID, r.Error)
			}
		}
	}
	return nil
}
