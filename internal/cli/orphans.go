package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Wasylq/StashJanitor/internal/apply"
	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/report"
	"github.com/Wasylq/StashJanitor/internal/scan"
	"github.com/spf13/cobra"
)

// Flags scoped to the orphans subcommand tree.
var (
	flagOrphansScanEndpoint   string
	flagOrphansScanPerPage    int
	flagOrphansScanBatchSize  int
	flagOrphansScanMaxScenes  int
	flagOrphansScanBatchDelay time.Duration
	flagOrphansScanRescan     bool

	flagOrphansStatusJSON bool

	flagOrphansReportFilter string
	flagOrphansReportJSON   bool

	flagOrphansApplyCommit bool
	flagOrphansApplyYes    bool
)

// newOrphansCmd builds the `stash-janitor orphans` subcommand tree (workflow C).
//
// Workflow C finds scenes with no stash_ids ("orphans"), queries stash-box
// via Stash's scrapeMultiScenes for matches by phash, and lets you write
// the matches back to Stash with sceneUpdate.
func newOrphansCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "orphans",
		Short: "Workflow C: stash-box phash lookup for scenes with no metadata",
		Long: `Workflow C tackles the 'lots of scenes with no metadata' problem.

A scene is an "orphan" when it has no stash_ids attached — Stash hasn't
matched it to any stash-box entry. stash-janitor enumerates orphans, asks Stash to
query a stash-box endpoint by phash for each, and stores the results
locally so you can review and apply them in your own time.

The apply step ATTACHES the stash-box link via sceneUpdate but does NOT
pull metadata. Run Stash's built-in Scene Tagger after applying to fetch
the actual metadata (tags, performers, studio, date) from stash-box.

At large library scales (10k+ orphans) the scan can take a while. Use
--max-scenes to chunk it; re-runs skip already-looked-up orphans
unless --rescan is set.`,
	}

	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "Find orphans and query stash-box for matches",
		RunE:  runOrphansScan,
	}
	scanCmd.Flags().StringVar(&flagOrphansScanEndpoint, "endpoint", "", "stash-box endpoint URL (default: first one configured in Stash)")
	scanCmd.Flags().IntVar(&flagOrphansScanPerPage, "per-page", 100, "page size for findScenes pagination")
	scanCmd.Flags().IntVar(&flagOrphansScanBatchSize, "batch-size", 20, "scenes per scrapeMultiScenes call")
	scanCmd.Flags().IntVar(&flagOrphansScanMaxScenes, "max-scenes", 0, "stop after processing N orphans (0 = no limit)")
	scanCmd.Flags().DurationVar(&flagOrphansScanBatchDelay, "batch-delay", 250*time.Millisecond, "sleep between batches as a soft rate limit")
	scanCmd.Flags().BoolVar(&flagOrphansScanRescan, "rescan", false, "re-query orphans we've already looked up before")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show orphan-lookup counts from the local cache",
		RunE:  runOrphansStatus,
	}
	statusCmd.Flags().BoolVar(&flagOrphansStatusJSON, "json", false, "emit JSON instead of human-readable text")

	reportCmd := &cobra.Command{
		Use:   "report",
		Short: "Print a per-orphan report from the local cache",
		RunE:  runOrphansReport,
	}
	reportCmd.Flags().StringVar(&flagOrphansReportFilter, "filter", "matched", "which lookups to show: all|matched|no-match|applied|dismissed")
	reportCmd.Flags().BoolVar(&flagOrphansReportJSON, "json", false, "emit JSON instead of human-readable text")

	applyCmd := &cobra.Command{
		Use:   "apply",
		Short: "Write matched stash_ids back to Stash via sceneUpdate",
		RunE:  runOrphansApply,
	}
	applyCmd.Flags().BoolVar(&flagOrphansApplyCommit, "commit", false, "actually mutate Stash (default is dry-run)")
	applyCmd.Flags().BoolVar(&flagOrphansApplyYes, "yes", false, "bypass interactive YES prompt for --commit")

	cmd.AddCommand(scanCmd, statusCmd, reportCmd, applyCmd)
	return cmd
}

func runOrphansScan(cmd *cobra.Command, args []string) error {
	_, st, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	res, err := scan.Orphans(ctx, client, st, scan.OrphansOptions{
		Endpoint:       flagOrphansScanEndpoint,
		PerPage:        flagOrphansScanPerPage,
		BatchSize:      flagOrphansScanBatchSize,
		MaxScenes:      flagOrphansScanMaxScenes,
		BatchDelay:     flagOrphansScanBatchDelay,
		Rescan:         flagOrphansScanRescan,
		ProgressWriter: cmd.ErrOrStderr(),
	})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "scan #%d complete  (endpoint: %s)\n", res.ScanRunID, res.Endpoint)
	fmt.Fprintf(out, "  orphans seen:    %d\n", res.OrphansSeen)
	fmt.Fprintf(out, "  matched:         %d\n", res.Matched)
	fmt.Fprintf(out, "  no_match:        %d\n", res.NoMatch)
	fmt.Fprintf(out, "  skipped (cache): %d\n", res.Skipped)
	fmt.Fprintln(out, "\nNext: `stash-janitor orphans report --filter matched` to see what stash-box found.")
	return nil
}

func runOrphansStatus(cmd *cobra.Command, args []string) error {
	_, st, _, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	s, err := report.ComputeOrphansStatus(context.Background(), st)
	if err != nil {
		return err
	}
	if flagOrphansStatusJSON {
		return report.PrintOrphansStatusJSON(cmd.OutOrStdout(), s)
	}
	return report.PrintOrphansStatus(cmd.OutOrStdout(), s)
}

func runOrphansReport(cmd *cobra.Command, args []string) error {
	_, st, _, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	rows, err := report.ListOrphansReport(context.Background(), st, report.OrphansReportFilter(flagOrphansReportFilter))
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if flagOrphansReportJSON {
		return report.PrintOrphansReportJSON(out, rows)
	}
	return report.PrintOrphansReport(out, rows)
}

func runOrphansApply(cmd *cobra.Command, args []string) error {
	cfg, st, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	plan, err := apply.PlanOrphans(ctx, st)
	if err != nil {
		return err
	}
	if err := apply.PrintOrphansPlan(out, plan, flagOrphansApplyCommit); err != nil {
		return err
	}
	if !flagOrphansApplyCommit {
		return nil
	}
	if len(plan.Lookups) == 0 {
		return nil
	}

	// Mutating action — gate behind interactive YES.
	summary := confirm.Summary{
		Action:           "link orphans to stash-box",
		GroupCount:       len(plan.Lookups),
		SceneCount:       len(plan.Lookups),
		ReclaimableBytes: 0, // not space-related
	}
	ok, err := confirm.PromptYES(os.Stdin, out, summary, flagOrphansApplyYes)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	reports, err := apply.ExecuteOrphans(ctx, client, st, plan, apply.ExecuteOrphansOpts{
		WriteMetadata: cfg.Orphans.WriteMetadataOnApply,
	})
	if err != nil {
		return err
	}
	return apply.PrintOrphansReports(out, reports)
}
