package organize

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// ApplyPlan collects the actionable rows for the commit step.
type ApplyPlan struct {
	Moves   []*store.OrganizePlan
	Renames []*store.OrganizePlan
}

// PlanApply walks the store and builds an ApplyPlan from actionable rows.
func PlanApply(ctx context.Context, st *store.Store) (*ApplyPlan, error) {
	rows, err := st.ListOrganizePlans(ctx, []string{"move", "rename"})
	if err != nil {
		return nil, err
	}
	plan := &ApplyPlan{}
	for _, r := range rows {
		if r.AppliedAt != nil {
			continue
		}
		switch r.Status {
		case "move":
			plan.Moves = append(plan.Moves, r)
		case "rename":
			plan.Renames = append(plan.Renames, r)
		}
	}
	return plan, nil
}

// PrintApplyPlan writes the summary.
func PrintApplyPlan(w io.Writer, plan *ApplyPlan, commit bool) error {
	mode := "DRY RUN"
	if commit {
		mode = "COMMIT"
	}
	total := len(plan.Moves) + len(plan.Renames)
	fmt.Fprintf(w, "=== stash-janitor organize apply (%s) ===\n", mode)
	fmt.Fprintf(w, "Files to move:    %d\n", len(plan.Moves))
	fmt.Fprintf(w, "Files to rename:  %d\n", len(plan.Renames))
	fmt.Fprintf(w, "Total actions:    %d\n", total)
	if total == 0 {
		fmt.Fprintln(w, "\nNothing to do. Try `stash-janitor organize scan` first.")
		return nil
	}
	if !commit {
		fmt.Fprintln(w, "\nThis was a dry run. Re-run with --commit to move files via Stash.")
		fmt.Fprintln(w, "--commit triggers an interactive YES confirmation; --yes bypasses it.")
	}
	return nil
}

// ApplyReport summarizes execution.
type ApplyReport struct {
	Succeeded int
	Failed    int
	Failures  []string
}

// Execute runs the moveFiles mutations for each actionable plan row.
func Execute(ctx context.Context, c *stash.Client, st *store.Store, plan *ApplyPlan) (*ApplyReport, error) {
	if c == nil || st == nil || plan == nil {
		return nil, errors.New("organize.Execute: nil dependency")
	}
	report := &ApplyReport{}

	all := make([]*store.OrganizePlan, 0, len(plan.Moves)+len(plan.Renames))
	all = append(all, plan.Moves...)
	all = append(all, plan.Renames...)

	for _, p := range all {
		targetDir := filepath.Dir(p.TargetPath)
		targetBase := filepath.Base(p.TargetPath)

		err := c.MoveFiles(ctx, stash.MoveFilesInput{
			IDs:                 []string{p.FileID},
			DestinationFolder:   targetDir,
			DestinationBasename: targetBase,
		})
		if err != nil {
			slog.Error("moveFiles failed; continuing",
				"file_id", p.FileID, "target", p.TargetPath, "error", err)
			if markErr := st.MarkOrganizePlanFailed(ctx, p.ID, err.Error()); markErr != nil {
				slog.Error("mark-failed also failed", "error", markErr)
			}
			report.Failed++
			report.Failures = append(report.Failures,
				fmt.Sprintf("file %s → %s: %v", p.FileID, p.TargetPath, err))
			continue
		}

		if err := st.MarkOrganizePlanApplied(ctx, p.ID); err != nil {
			slog.Error("mark-applied failed after successful move",
				"file_id", p.FileID, "error", err)
		}
		report.Succeeded++
	}
	return report, nil
}

// PrintApplyReport writes the post-execution summary.
func PrintApplyReport(w io.Writer, r *ApplyReport) error {
	fmt.Fprintf(w, "\n=== organize apply complete ===\n")
	fmt.Fprintf(w, "  succeeded: %d\n", r.Succeeded)
	fmt.Fprintf(w, "  failed:    %d\n", r.Failed)
	if r.Failed > 0 {
		fmt.Fprintln(w, "\nFailures:")
		for _, f := range r.Failures {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
	return nil
}

// PrintReport renders the per-file organize report.
func PrintReport(w io.Writer, plans []*store.OrganizePlan) error {
	if len(plans) == 0 {
		_, err := io.WriteString(w, "(no organize plans match the filter)\n")
		return err
	}
	var moves, renames, skips, conflicts, correct int
	for _, p := range plans {
		switch p.Status {
		case "move":
			moves++
		case "rename":
			renames++
		case "skip_no_metadata":
			skips++
		case "conflict":
			conflicts++
		case "already_correct":
			correct++
		}
	}
	fmt.Fprintf(w, "=== organize report (%d plans) ===\n", len(plans))
	fmt.Fprintf(w, "  move: %d  rename: %d  already_correct: %d  skip: %d  conflict: %d\n\n",
		moves, renames, correct, skips, conflicts)

	for _, p := range plans {
		switch p.Status {
		case "move", "rename":
			fmt.Fprintf(w, "[%s] scene %s\n", p.Status, p.SceneID)
			fmt.Fprintf(w, "  from: %s\n", p.CurrentPath)
			fmt.Fprintf(w, "  to:   %s\n", p.TargetPath)
		case "conflict":
			fmt.Fprintf(w, "[CONFLICT] scene %s  file %s\n", p.SceneID, p.FileID)
			fmt.Fprintf(w, "  from: %s\n", p.CurrentPath)
			fmt.Fprintf(w, "  to:   %s\n", p.TargetPath)
			if p.Reason != "" {
				fmt.Fprintf(w, "  reason: %s\n", p.Reason)
			}
		case "skip_no_metadata":
			fmt.Fprintf(w, "[skip] scene %s  %s\n", p.SceneID, p.Reason)
			fmt.Fprintf(w, "  path: %s\n", p.CurrentPath)
		}
	}

	// Don't print already_correct individually — too noisy.
	if correct > 0 {
		fmt.Fprintf(w, "\n(%d files already in the correct location, not shown)\n", correct)
	}
	return nil
}

// HumanBytes re-exports for use by the CLI.
var HumanBytes = confirm.HumanBytes
