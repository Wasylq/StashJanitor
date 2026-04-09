package cli

import (
	"context"
	"fmt"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/decide"
	"github.com/Wasylq/StashJanitor/internal/report"
	"github.com/Wasylq/StashJanitor/internal/scan"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
	"github.com/spf13/cobra"
)

// Flags scoped to the scenes subcommand tree. Defined at package scope so
// command Run functions can read them without threading state through.
var (
	flagScenesScanDistance     int
	flagScenesScanDurationDiff float64
	flagScenesScanMaxGroups    int

	flagScenesStatusJSON bool

	flagScenesReportFilter string
	flagScenesReportJSON   bool

	flagScenesApplyAction string
	flagScenesApplyCommit bool
	flagScenesApplyYes    bool
)

// newScenesCmd builds the `stash-janitor scenes` subcommand tree (workflow A).
//
// All children are stubs in Phase 1 task #1; subsequent tasks fill them in:
//
//	scan   → task #7
//	status → task #10
//	report → task #10
//	apply  → tasks #11 (tag) and #13 (merge)
//	mark   → Phase 1.5 task #3
func newScenesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scenes",
		Short: "Workflow A: cross-scene duplicate detection and resolution",
		Long: `Workflow A finds duplicate scenes via Stash's findDuplicateScenes query and
helps you resolve them safely.

The default action ('tag') adds _dedupe_loser / _dedupe_keeper tags so you can
review and bulk-delete in Stash's UI. The 'merge' action computes a metadata
union, calls sceneMerge, then prunes the resulting multi-file scene. Both
default to dry-run; --commit is required to mutate Stash. Merge --commit also
requires an interactive YES confirmation (or --yes for scripted use).`,
	}

	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "Query Stash for duplicate groups and populate the local cache",
		RunE:  runScenesScan,
	}
	scanCmd.Flags().IntVar(&flagScenesScanDistance, "distance", 4, "phash hamming distance (0=identical, 4=re-encodes, >8 risks false positives)")
	scanCmd.Flags().Float64Var(&flagScenesScanDurationDiff, "duration-diff", 1.0, "max duration difference in seconds for two scenes to be considered duplicates")
	scanCmd.Flags().IntVar(&flagScenesScanMaxGroups, "max-groups", 0, "stop after processing N groups (0 = no limit)")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show duplicate-group counts and reclaimable bytes from the local cache",
		RunE:  runScenesStatus,
	}
	statusCmd.Flags().BoolVar(&flagScenesStatusJSON, "json", false, "emit JSON instead of human-readable text")

	reportCmd := &cobra.Command{
		Use:   "report",
		Short: "Print a per-group report from the local cache (no Stash calls)",
		RunE:  runScenesReport,
	}
	reportCmd.Flags().StringVar(&flagScenesReportFilter, "filter", "all", "which groups to show: all|decided|needs-review|applied|dismissed")
	reportCmd.Flags().BoolVar(&flagScenesReportJSON, "json", false, "emit JSON instead of human-readable text")

	apply := &cobra.Command{
		Use:   "apply",
		Short: "Resolve duplicate groups (tag, merge, or delete)",
		RunE:  stub("scenes apply"),
	}
	apply.Flags().StringVar(&flagScenesApplyAction, "action", "tag", "tag|merge|delete (delete is Phase 2)")
	apply.Flags().BoolVar(&flagScenesApplyCommit, "commit", false, "actually mutate Stash (default is dry-run)")
	apply.Flags().BoolVar(&flagScenesApplyYes, "yes", false, "bypass interactive YES prompt for destructive --commit actions")

	mark := &cobra.Command{
		Use:   "mark",
		Short: "Persist a manual override for a duplicate group (Phase 1.5)",
		RunE:  stub("scenes mark"),
	}

	cmd.AddCommand(scanCmd, statusCmd, reportCmd, apply, mark)
	return cmd
}

// loadConfigAndStore is the boilerplate every scenes/files subcommand
// needs: parse config, open the store, build a Stash client. Returns a
// cleanup func the caller MUST defer.
func loadConfigAndStore() (*config.Config, *store.Store, *stash.Client, func(), error) {
	cfg, err := config.Load(flagConfigPath)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	st, err := store.Open(flagDBPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("opening sqlite at %s: %w", flagDBPath, err)
	}
	cleanup := func() { _ = st.Close() }
	client := stash.NewClient(cfg.Stash.URL, cfg.StashAPIKey())
	return cfg, st, client, cleanup, nil
}

func runScenesScan(cmd *cobra.Command, args []string) error {
	cfg, st, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	scorer, err := decide.NewSceneScorer(cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	res, err := scan.Scenes(ctx, client, st, scorer, scan.ScenesOptions{
		Distance:     flagScenesScanDistance,
		DurationDiff: flagScenesScanDurationDiff,
		MaxGroups:    flagScenesScanMaxGroups,
	})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "scan #%d complete\n", res.ScanRunID)
	fmt.Fprintf(out, "  groups processed: %d (new: %d)\n", res.GroupCount, res.NewGroups)
	fmt.Fprintf(out, "  decided:          %d\n", res.Decided)
	fmt.Fprintf(out, "  needs_review:     %d\n", res.NeedsReview)
	fmt.Fprintf(out, "  dismissed:        %d\n", res.Dismissed)
	fmt.Fprintln(out, "\nNext: `stash-janitor scenes status` for reclaimable bytes, `stash-janitor scenes report` for per-group detail.")
	return nil
}

func runScenesStatus(cmd *cobra.Command, args []string) error {
	_, st, _, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	s, err := report.ComputeScenesStatus(context.Background(), st)
	if err != nil {
		return err
	}
	if flagScenesStatusJSON {
		return report.PrintScenesStatusJSON(cmd.OutOrStdout(), s)
	}
	return report.PrintScenesStatus(cmd.OutOrStdout(), s)
}

func runScenesReport(cmd *cobra.Command, args []string) error {
	_, st, _, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	groups, err := report.ListScenesReport(context.Background(), st, report.ScenesReportFilter(flagScenesReportFilter))
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if flagScenesReportJSON {
		return report.PrintScenesReportJSON(out, groups)
	}
	return report.PrintScenesReport(out, groups)
}
