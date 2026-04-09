package cli

import "github.com/spf13/cobra"

var (
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

	scan := &cobra.Command{
		Use:   "scan",
		Short: "Find scenes with file_count > 1 and populate the local cache",
		RunE:  stub("files scan"),
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show multi-file scene counts and reclaimable bytes from the local cache",
		RunE:  stub("files status"),
	}

	report := &cobra.Command{
		Use:   "report",
		Short: "Print a per-scene report of multi-file scenes from the local cache",
		RunE:  stub("files report"),
	}
	report.Flags().StringVar(&flagFilesReportFilter, "filter", "all", "which groups to show: all|decided|needs-review|applied|dismissed")
	report.Flags().BoolVar(&flagFilesReportJSON, "json", false, "emit JSON instead of human-readable text")

	apply := &cobra.Command{
		Use:   "apply",
		Short: "Phase 1: report-only. Phase 1.5 will add --commit (sceneUpdate + deleteFiles).",
		RunE:  stub("files apply"),
	}
	apply.Flags().BoolVar(&flagFilesApplyCommit, "commit", false, "actually mutate Stash (Phase 1.5 only)")
	apply.Flags().BoolVar(&flagFilesApplyYes, "yes", false, "bypass interactive YES prompt for --commit")

	mark := &cobra.Command{
		Use:   "mark",
		Short: "Persist a manual override for a multi-file scene (Phase 1.5)",
		RunE:  stub("files mark"),
	}

	cmd.AddCommand(scan, status, report, apply, mark)
	return cmd
}
