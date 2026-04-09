package apply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// DeletePlan is the precomputed work for `stash-janitor scenes apply --action delete`.
//
// Structurally identical to MergePlan — both walk the decided groups and
// sum loser file sizes — but the execute path is different: delete just
// destroys the loser scenes, while merge preserves their metadata first.
type DeletePlan struct {
	Groups           []*store.SceneGroup
	TotalLosers      int
	ReclaimableBytes int64
}

// PlanDelete walks the store and builds a DeletePlan from groups currently
// marked decided. It does not call Stash.
func PlanDelete(ctx context.Context, st *store.Store) (*DeletePlan, error) {
	groups, err := st.ListSceneGroups(ctx, []string{store.StatusDecided})
	if err != nil {
		return nil, fmt.Errorf("listing decided groups: %w", err)
	}
	plan := &DeletePlan{Groups: groups}
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

// PrintDeletePlan writes the human-readable summary used by both dry-run
// and the commit-mode confirmation prompt's "about to do this" header.
func PrintDeletePlan(w io.Writer, p *DeletePlan, commit bool) error {
	mode := "DRY RUN"
	if commit {
		mode = "COMMIT"
	}
	fmt.Fprintf(w, "=== stash-janitor scenes apply --action delete (%s) ===\n", mode)
	fmt.Fprintf(w, "Decided groups in scope:  %d\n", len(p.Groups))
	fmt.Fprintf(w, "Loser scenes to destroy:  %d\n", p.TotalLosers)
	fmt.Fprintf(w, "Reclaimable bytes:        %s\n", confirm.HumanBytes(p.ReclaimableBytes))
	if len(p.Groups) == 0 {
		fmt.Fprintln(w, "\nNothing to do. Try `stash-janitor scenes scan` first.")
		return nil
	}
	fmt.Fprintln(w, "\nDelete pipeline per group:")
	fmt.Fprintln(w, "  1. scenesDestroy(ids: [losers], delete_file: true, delete_generated: true)")
	fmt.Fprintln(w, "  2. mark the group applied")
	fmt.Fprintln(w, "\n⚠ Unlike --action merge, delete does NOT preserve loser metadata")
	fmt.Fprintln(w, "  (tags, performers, stash_ids, play history). Use merge unless you")
	fmt.Fprintln(w, "  are certain the losers contain nothing worth preserving.")
	if !commit {
		fmt.Fprintln(w, "\nThis was a dry run. Re-run with --commit to mutate Stash.")
		fmt.Fprintln(w, "--commit triggers an interactive YES confirmation; --yes bypasses it.")
	}
	return nil
}

// DeleteReport is the per-group outcome of ExecuteDelete.
type DeleteReport struct {
	GroupID         int64
	KeeperSceneID   string
	LoserSceneIDs   []string
	Status          string // "success" | "skipped" | "failed"
	Error           string
	BytesReclaimed  int64
}

// ExecuteDelete runs the hard-delete pipeline for every group in the plan.
//
// Per group:
//   1. Call scenesDestroy with delete_file=true and delete_generated=true.
//      Stash deletes the underlying files from disk and removes the scene
//      records.
//   2. Mark the group applied in the local store.
//
// Per-group failures are captured in the returned slice and the loop
// continues. Caller (cli) is responsible for the YES prompt before invoking
// this function.
func ExecuteDelete(
	ctx context.Context,
	c *stash.Client,
	st *store.Store,
	plan *DeletePlan,
) ([]*DeleteReport, error) {
	if c == nil || st == nil || plan == nil {
		return nil, errors.New("ExecuteDelete: nil dependency")
	}
	reports := make([]*DeleteReport, 0, len(plan.Groups))

	for _, g := range plan.Groups {
		report := &DeleteReport{GroupID: g.ID}
		reports = append(reports, report)

		if g.AppliedAt != nil {
			report.Status = "skipped"
			report.Error = "already applied"
			continue
		}

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
		if len(loserIDs) == 0 {
			report.Status = "failed"
			report.Error = "group has no losers to delete"
			continue
		}

		if err := c.ScenesDestroy(ctx, loserIDs); err != nil {
			slog.Error("scenesDestroy failed; continuing",
				"group_id", g.ID, "loser_ids", loserIDs, "error", err)
			report.Status = "failed"
			report.Error = err.Error()
			continue
		}

		if err := st.MarkSceneGroupApplied(ctx, g.ID); err != nil {
			report.Status = "failed"
			report.Error = "scenesDestroy succeeded but mark-applied failed: " + err.Error()
			continue
		}
		report.Status = "success"
	}
	return reports, nil
}

// PrintDeleteReports writes a summary of the per-group outcomes after
// ExecuteDelete has finished.
func PrintDeleteReports(w io.Writer, reports []*DeleteReport) error {
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
	fmt.Fprintf(w, "\n=== delete complete ===\n")
	fmt.Fprintf(w, "  successes:        %d\n", ok)
	fmt.Fprintf(w, "  failures:         %d\n", failed)
	fmt.Fprintf(w, "  skipped:          %d\n", skipped)
	fmt.Fprintf(w, "  bytes reclaimed:  %s\n", confirm.HumanBytes(bytes))
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
