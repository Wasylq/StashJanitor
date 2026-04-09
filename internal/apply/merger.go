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
	GroupID            int64
	KeeperSceneID      string
	LoserSceneIDs      []string
	Status             string // "success" | "skipped" | "failed"
	Error              string
	UnionedFields      []merge.FieldDiff
	NewPrimaryFileID   string
	PrimaryWasSwapped  bool
	FilesDeletedCount  int
	BytesReclaimed     int64
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
		bytes   int64
	)
	for _, r := range reports {
		switch r.Status {
		case "success":
			ok++
			bytes += r.BytesReclaimed
		case "failed":
			failed++
		case "skipped":
			skipped++
		}
	}
	fmt.Fprintf(w, "\n=== merge complete ===\n")
	fmt.Fprintf(w, "  successes: %d\n", ok)
	fmt.Fprintf(w, "  failures:  %d\n", failed)
	fmt.Fprintf(w, "  skipped:   %d\n", skipped)
	fmt.Fprintf(w, "  bytes reclaimed: %s\n", confirm.HumanBytes(bytes))
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
