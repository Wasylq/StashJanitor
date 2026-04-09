package cli

import (
	"context"
	"fmt"

	"github.com/Wasylq/StashJanitor/internal/apply"
	"github.com/Wasylq/StashJanitor/internal/decide"
	"github.com/Wasylq/StashJanitor/internal/report"
	"github.com/Wasylq/StashJanitor/internal/scan"
	"github.com/spf13/cobra"
)

var (
	flagFilesScanPerPage   int
	flagFilesScanMaxScenes int

	flagFilesStatusJSON bool

	flagFilesReportFilter string
	flagFilesReportJSON   bool

	flagFilesApplyCommit bool
	flagFilesApplyYes    bool
)

// newFilesCmd builds the `stash-janitor files` subcommand tree (workflow B).
//
// Workflow B handles single Stash scenes that have multiple files attached.
// Stash only attaches files to a scene by oshash/md5 match (verified in
// pkg/scene/scan.go in v0.31.0), so all files in a scene are byte-equivalent
// — scoring uses only filename / path / mod_time, never tech specs.
//
// Stub children get filled in by subsequent tasks:
//
//	scan   → task #14
//	status → task #16
//	report → task #16
//	apply  → task #17 (Phase 1: report-only); --commit lands in Phase 1.5
func newFilesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files",
		Short: "Workflow B: within-scene multi-file cleanup",
		Long: `Workflow B finds Stash scenes with more than one attached file and helps you
keep the best one.

Because Stash attaches multi-files only by oshash/md5, all files within a
single scene are byte-equivalent. The scoring rules consider only filename
quality (configurable regex), path priority, and mod_time. Tech specs are
guaranteed equal and are reported but not scored.

Phase 1 default for 'apply' is report-only — no mutations. --commit support
lands in Phase 1.5.`,
	}

	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "Find scenes with file_count > 1 and populate the local cache",
		RunE:  runFilesScan,
	}
	scanCmd.Flags().IntVar(&flagFilesScanPerPage, "per-page", 100, "page size for findScenes pagination")
	scanCmd.Flags().IntVar(&flagFilesScanMaxScenes, "max-scenes", 0, "stop after processing N scenes (0 = no limit)")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show multi-file scene counts and reclaimable bytes from the local cache",
		RunE:  runFilesStatus,
	}
	statusCmd.Flags().BoolVar(&flagFilesStatusJSON, "json", false, "emit JSON instead of human-readable text")

	reportCmd := &cobra.Command{
		Use:   "report",
		Short: "Print a per-scene report of multi-file scenes from the local cache",
		RunE:  runFilesReport,
	}
	reportCmd.Flags().StringVar(&flagFilesReportFilter, "filter", "all", "which groups to show: all|decided|needs-review|applied|dismissed")
	reportCmd.Flags().BoolVar(&flagFilesReportJSON, "json", false, "emit JSON instead of human-readable text")

	applyCmd := &cobra.Command{
		Use:   "apply",
		Short: "Phase 1: report-only. Phase 1.5 will add --commit (sceneUpdate + deleteFiles).",
		RunE:  runFilesApply,
	}
	applyCmd.Flags().BoolVar(&flagFilesApplyCommit, "commit", false, "actually mutate Stash (Phase 1.5 only)")
	applyCmd.Flags().BoolVar(&flagFilesApplyYes, "yes", false, "bypass interactive YES prompt for --commit")

	mark := &cobra.Command{
		Use:   "mark",
		Short: "Persist a manual override for a multi-file scene (Phase 1.5)",
		RunE:  stub("files mark"),
	}

	cmd.AddCommand(scanCmd, statusCmd, reportCmd, applyCmd, mark)
	return cmd
}

func runFilesScan(cmd *cobra.Command, args []string) error {
	cfg, st, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	scorer, err := decide.NewFileScorer(cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	res, err := scan.Files(ctx, client, st, scorer, scan.FilesOptions{
		PerPage:   flagFilesScanPerPage,
		MaxScenes: flagFilesScanMaxScenes,
	})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "scan #%d complete\n", res.ScanRunID)
	fmt.Fprintf(out, "  scenes processed: %d (new: %d)\n", res.SceneCount, res.NewGroups)
	fmt.Fprintf(out, "  decided:          %d\n", res.Decided)
	fmt.Fprintf(out, "  needs_review:     %d\n", res.NeedsReview)
	fmt.Fprintf(out, "  dismissed:        %d\n", res.Dismissed)
	fmt.Fprintln(out, "\nNext: `stash-janitor files status` for reclaimable bytes, `stash-janitor files report` for per-scene detail.")
	return nil
}

func runFilesStatus(cmd *cobra.Command, args []string) error {
	_, st, _, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	s, err := report.ComputeFilesStatus(context.Background(), st)
	if err != nil {
		return err
	}
	if flagFilesStatusJSON {
		return report.PrintFilesStatusJSON(cmd.OutOrStdout(), s)
	}
	return report.PrintFilesStatus(cmd.OutOrStdout(), s)
}

func runFilesReport(cmd *cobra.Command, args []string) error {
	_, st, _, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	groups, err := report.ListFilesReport(context.Background(), st, report.ScenesReportFilter(flagFilesReportFilter))
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if flagFilesReportJSON {
		return report.PrintFilesReportJSON(out, groups)
	}
	return report.PrintFilesReport(out, groups)
}

func runFilesApply(cmd *cobra.Command, args []string) error {
	_, st, _, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	if flagFilesApplyCommit {
		return fmt.Errorf("files apply --commit is not implemented yet (Phase 1.5)")
	}

	plan, err := apply.PlanFiles(context.Background(), st)
	if err != nil {
		return err
	}
	return apply.PrintFilesPlan(cmd.OutOrStdout(), plan, false)
}
