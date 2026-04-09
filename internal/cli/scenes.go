package cli

import "github.com/spf13/cobra"

// Flags scoped to the scenes subcommand tree. Defined at package scope so
// command Run functions can read them without threading state through.
var (
	flagScenesScanDistance     int
	flagScenesScanDurationDiff float64
	flagScenesScanMaxGroups    int

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

	scan := &cobra.Command{
		Use:   "scan",
		Short: "Query Stash for duplicate groups and populate the local cache",
		RunE:  stub("scenes scan"),
	}
	scan.Flags().IntVar(&flagScenesScanDistance, "distance", 4, "phash hamming distance (0=identical, 4=re-encodes, >8 risks false positives)")
	scan.Flags().Float64Var(&flagScenesScanDurationDiff, "duration-diff", 1.0, "max duration difference in seconds for two scenes to be considered duplicates")
	scan.Flags().IntVar(&flagScenesScanMaxGroups, "max-groups", 0, "stop after processing N groups (0 = no limit)")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show duplicate-group counts and reclaimable bytes from the local cache",
		RunE:  stub("scenes status"),
	}

	report := &cobra.Command{
		Use:   "report",
		Short: "Print a per-group report from the local cache (no Stash calls)",
		RunE:  stub("scenes report"),
	}
	report.Flags().StringVar(&flagScenesReportFilter, "filter", "all", "which groups to show: all|decided|needs-review|applied|dismissed")
	report.Flags().BoolVar(&flagScenesReportJSON, "json", false, "emit JSON instead of human-readable text")

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

	cmd.AddCommand(scan, status, report, apply, mark)
	return cmd
}
