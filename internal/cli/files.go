package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/Wasylq/StashJanitor/internal/apply"
	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/decide"
	"github.com/Wasylq/StashJanitor/internal/report"
	"github.com/Wasylq/StashJanitor/internal/scan"
	"github.com/Wasylq/StashJanitor/internal/store"
	"github.com/spf13/cobra"
)

var (
	flagFilesScanPerPage   int
	flagFilesScanMaxScenes int

	flagFilesStatusJSON bool

	flagFilesReportFilter string
	flagFilesReportJSON   bool

	flagFilesApplyCommit       bool
	flagFilesApplyYes          bool
	flagFilesApplySubmitFprint bool

	flagFilesMarkSceneID string
	flagFilesMarkAs      string
	flagFilesMarkNotes   string
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
	applyCmd.Flags().BoolVar(&flagFilesApplyCommit, "commit", false, "actually mutate Stash (deletes loser files from disk)")
	applyCmd.Flags().BoolVar(&flagFilesApplyYes, "yes", false, "bypass interactive YES prompt for --commit")
	applyCmd.Flags().BoolVar(&flagFilesApplySubmitFprint, "submit-fingerprints", false, "after a successful --commit, submit keeper-scene fingerprints to stash-box endpoints")

	markCmd := &cobra.Command{
		Use:   "mark",
		Short: "Persist a manual override for a multi-file scene",
		Long: `Persist a decision about a multi-file scene that survives across scan
re-runs. Stored locally in stash-janitor.sqlite, keyed by the scene ID.

Decisions:
  keep_all   Don't ever propose pruning this scene's files. Future scans
             mark it dismissed and skip it during apply.
  dismiss    Same effect as keep_all but with a different label.

Use 'stash-janitor files report' to find the scene ID you want to mark.`,
		RunE: runFilesMark,
	}
	markCmd.Flags().StringVar(&flagFilesMarkSceneID, "scene-id", "", "Stash scene ID")
	markCmd.Flags().StringVar(&flagFilesMarkAs, "as", "", "decision to record: keep_all|dismiss")
	markCmd.Flags().StringVar(&flagFilesMarkNotes, "notes", "", "optional free-form notes saved with the decision")

	cmd.AddCommand(scanCmd, statusCmd, reportCmd, applyCmd, markCmd)
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
	cfg, st, _, cleanup, err := loadConfigAndStore()
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
	var explain report.ExplainFileFn
	if scorer, err := decide.NewFileScorer(cfg); err == nil {
		explain = scorer.ExplainFilePick
	}
	return report.PrintFilesReport(out, groups, explain)
}

func runFilesApply(cmd *cobra.Command, args []string) error {
	_, st, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	plan, err := apply.PlanFiles(ctx, st)
	if err != nil {
		return err
	}
	if err := apply.PrintFilesPlan(out, plan, flagFilesApplyCommit); err != nil {
		return err
	}
	if !flagFilesApplyCommit {
		return nil
	}
	if len(plan.Actions) == 0 {
		return nil
	}

	// Destructive (deletes files from disk via Stash). Always gate behind
	// interactive YES (or --yes).
	loserCount := 0
	for _, a := range plan.Actions {
		loserCount += len(a.LoserFileIDs)
	}
	summary := confirm.Summary{
		Action:           "delete files",
		GroupCount:       len(plan.Actions),
		SceneCount:       loserCount, // number of files that will be deleted
		ReclaimableBytes: plan.TotalReclaimable,
	}
	ok, err := confirm.PromptYES(os.Stdin, out, summary, flagFilesApplyYes)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	reports, err := apply.ExecuteFiles(ctx, client, st, plan)
	if err != nil {
		return err
	}
	if err := apply.PrintFilesReports(out, reports); err != nil {
		return err
	}
	if flagFilesApplySubmitFprint {
		keeperIDs := make([]string, 0, len(reports))
		for _, r := range reports {
			if r.Status == "success" && r.SceneID != "" {
				keeperIDs = append(keeperIDs, r.SceneID)
			}
		}
		if err := submitFingerprintsForScenePlan(ctx, client, st, out, keeperIDs); err != nil {
			return err
		}
	}
	return nil
}

func runFilesMark(cmd *cobra.Command, args []string) error {
	if flagFilesMarkSceneID == "" {
		return fmt.Errorf("--scene-id is required (run `stash-janitor files report` to find one)")
	}
	switch flagFilesMarkAs {
	case "keep_all", "dismiss":
	case "":
		return fmt.Errorf("--as is required (one of: keep_all|dismiss)")
	default:
		return fmt.Errorf("unknown --as %q (try: keep_all|dismiss)", flagFilesMarkAs)
	}

	_, st, _, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	d := store.UserDecision{
		Key:      "scene:" + flagFilesMarkSceneID,
		Workflow: store.WorkflowFiles,
		Decision: flagFilesMarkAs,
		Notes:    flagFilesMarkNotes,
	}
	if err := st.PutUserDecision(context.Background(), d); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"recorded: scene=%s decision=%s\nThis override applies to future `stash-janitor files scan` runs.\n",
		flagFilesMarkSceneID, flagFilesMarkAs,
	)
	return nil
}
