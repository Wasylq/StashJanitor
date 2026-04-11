package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/organize"
	"github.com/spf13/cobra"
)

var (
	flagOrganizeScanPerPage   int
	flagOrganizeScanMaxScenes int
	flagOrganizeReportFilter  string
	flagOrganizeApplyCommit   bool
	flagOrganizeApplyYes      bool
)

func newOrganizeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "organize",
		Short: "Workflow D: move and rename files based on Stash metadata",
		Long: `Workflow D computes an ideal file path for every scene based on a
configurable template and Stash metadata (performer, studio, title,
date, resolution). Files that are already in the right place are
skipped. Files that need to move are proposed as a plan you can
review before committing.

All moves go through Stash's moveFiles API — stash-janitor never touches the
filesystem directly. Same safety model: dry-run default, --commit
required, interactive YES prompt on commit.

Recommended: run stash-janitor scenes apply (dedup) and stash-janitor orphans scan
(metadata recovery) BEFORE organize, so more files have the metadata
needed to compute their ideal path.`,
	}

	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "Fetch all scenes and compute ideal paths (stored locally)",
		RunE:  runOrganizeScan,
	}
	scanCmd.Flags().IntVar(&flagOrganizeScanPerPage, "per-page", 100, "page size for findScenes pagination")
	scanCmd.Flags().IntVar(&flagOrganizeScanMaxScenes, "max-scenes", 0, "stop after processing N scenes (0 = no limit)")

	reportCmd := &cobra.Command{
		Use:   "report",
		Short: "Show proposed moves from the local cache",
		RunE:  runOrganizeReport,
	}
	reportCmd.Flags().StringVar(&flagOrganizeReportFilter, "filter", "move",
		"which plans to show: all|move|rename|conflict|skip|already_correct")

	applyCmd := &cobra.Command{
		Use:   "apply",
		Short: "Execute the proposed moves via Stash's moveFiles API",
		RunE:  runOrganizeApply,
	}
	applyCmd.Flags().BoolVar(&flagOrganizeApplyCommit, "commit", false, "actually move files (default is dry-run)")
	applyCmd.Flags().BoolVar(&flagOrganizeApplyYes, "yes", false, "bypass interactive YES prompt for --commit")

	cmd.AddCommand(scanCmd, reportCmd, applyCmd)
	return cmd
}

func runOrganizeScan(cmd *cobra.Command, args []string) error {
	cfg, st, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	res, err := organize.Scan(context.Background(), client, st, cfg, organize.ScanOptions{
		PerPage:   flagOrganizeScanPerPage,
		MaxScenes: flagOrganizeScanMaxScenes,
	})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "scan #%d complete\n", res.ScanRunID)
	fmt.Fprintf(out, "  scenes processed:  %d\n", res.ScenesProcessed)
	fmt.Fprintf(out, "  to move:           %d\n", res.Moves)
	fmt.Fprintf(out, "  to rename:         %d\n", res.Renames)
	fmt.Fprintf(out, "  already correct:   %d\n", res.AlreadyCorrect)
	fmt.Fprintf(out, "  skip (no metadata):%d\n", res.SkipNoMetadata)
	fmt.Fprintf(out, "  conflicts:         %d\n", res.Conflicts)
	if res.MultiFileWarning > 0 {
		fmt.Fprintf(out, "\n  ⚠ %d scenes have multiple files — only the primary file is moved.\n", res.MultiFileWarning)
		fmt.Fprintf(out, "    Run `stash-janitor files apply --commit` first to clean up multi-file scenes.\n")
	}
	fmt.Fprintln(out, "\nNext: `stash-janitor organize report` to see proposed moves.")
	return nil
}

func runOrganizeReport(cmd *cobra.Command, args []string) error {
	_, st, _, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	var statuses []string
	switch flagOrganizeReportFilter {
	case "all":
		statuses = nil
	case "move":
		statuses = []string{"move", "rename"}
	case "rename":
		statuses = []string{"rename"}
	case "conflict":
		statuses = []string{"conflict"}
	case "skip":
		statuses = []string{"skip_no_metadata"}
	case "already_correct":
		statuses = []string{"already_correct"}
	default:
		return fmt.Errorf("unknown --filter %q", flagOrganizeReportFilter)
	}

	plans, err := st.ListOrganizePlans(context.Background(), statuses)
	if err != nil {
		return err
	}
	return organize.PrintReport(cmd.OutOrStdout(), plans)
}

func runOrganizeApply(cmd *cobra.Command, args []string) error {
	_, st, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	plan, err := organize.PlanApply(ctx, st)
	if err != nil {
		return err
	}
	if err := organize.PrintApplyPlan(out, plan, flagOrganizeApplyCommit); err != nil {
		return err
	}
	if !flagOrganizeApplyCommit {
		return nil
	}
	total := len(plan.Moves) + len(plan.Renames)
	if total == 0 {
		return nil
	}

	summary := confirm.Summary{
		Action:     "move/rename files",
		GroupCount: total,
		SceneCount: total,
	}
	ok, err := confirm.PromptYES(os.Stdin, out, summary, flagOrganizeApplyYes)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	report, err := organize.Execute(ctx, client, st, plan)
	if err != nil {
		return err
	}
	return organize.PrintApplyReport(out, report)
}

func init() {
	// Register in root — done via newOrganizeCmd() called from root.go.
}
