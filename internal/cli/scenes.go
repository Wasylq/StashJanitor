package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/Wasylq/StashJanitor/internal/apply"
	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/confirm"
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

	flagScenesApplyAction       string
	flagScenesApplyCommit       bool
	flagScenesApplyYes          bool
	flagScenesApplySubmitFprint bool

	flagScenesMarkSignature string
	flagScenesMarkAs        string
	flagScenesMarkKeeper    string
	flagScenesMarkNotes     string
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

	applyCmd := &cobra.Command{
		Use:   "apply",
		Short: "Resolve duplicate groups (tag, merge, or delete)",
		RunE:  runScenesApply,
	}
	applyCmd.Flags().StringVar(&flagScenesApplyAction, "action", "tag", "tag|merge|delete (delete is Phase 2)")
	applyCmd.Flags().BoolVar(&flagScenesApplyCommit, "commit", false, "actually mutate Stash (default is dry-run)")
	applyCmd.Flags().BoolVar(&flagScenesApplyYes, "yes", false, "bypass interactive YES prompt for destructive --commit actions")
	applyCmd.Flags().BoolVar(&flagScenesApplySubmitFprint, "submit-fingerprints", false, "after a successful --commit, submit keeper fingerprints to stash-box endpoints")

	markCmd := &cobra.Command{
		Use:   "mark",
		Short: "Persist a manual override for a duplicate group",
		Long: `Persist a decision about a duplicate group that survives across scan
re-runs. Stored locally in stash-janitor.sqlite, keyed by the group signature
(sorted scene IDs joined by '|').

Decisions:
  not_duplicate    Mark the group as 'these aren't actually duplicates'.
                   Future scans will mark it dismissed and skip it during apply.
  dismiss          Same effect as not_duplicate but with a different label.
  force_keeper     Override the scorer's pick. Requires --keeper SCENE_ID.
                   Future scans will use the pinned keeper instead of scoring.

Use 'stash-janitor scenes report' to find the signature you want to mark.`,
		RunE: runScenesMark,
	}
	markCmd.Flags().StringVar(&flagScenesMarkSignature, "signature", "", "group signature (sorted scene IDs joined by |)")
	markCmd.Flags().StringVar(&flagScenesMarkAs, "as", "", "decision to record: not_duplicate|dismiss|force_keeper")
	markCmd.Flags().StringVar(&flagScenesMarkKeeper, "keeper", "", "scene ID to pin as keeper (required for --as force_keeper)")
	markCmd.Flags().StringVar(&flagScenesMarkNotes, "notes", "", "optional free-form notes saved with the decision")

	cmd.AddCommand(scanCmd, statusCmd, reportCmd, applyCmd, markCmd)
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
	cfg, st, _, cleanup, err := loadConfigAndStore()
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

	// Build the per-loser explainer from a SceneScorer wired up with the
	// same config the scan used. Failing to construct the scorer is not
	// fatal — we just print without annotations.
	var explain report.ExplainSceneFn
	if scorer, err := decide.NewSceneScorer(cfg); err == nil {
		explain = scorer.ExplainPick
	}
	return report.PrintScenesReport(out, groups, explain)
}

func runScenesApply(cmd *cobra.Command, args []string) error {
	cfg, st, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	switch flagScenesApplyAction {
	case "tag":
		plan, err := apply.PlanTag(ctx, st)
		if err != nil {
			return err
		}
		if err := apply.PrintTagPlan(out, plan, cfg, flagScenesApplyCommit); err != nil {
			return err
		}
		if !flagScenesApplyCommit {
			return nil
		}
		if err := apply.ExecuteTag(ctx, client, st, cfg, plan); err != nil {
			return err
		}
		fmt.Fprintln(out, "\nDone. Tags have been applied. Review and bulk-delete losers in Stash's UI.")
		if flagScenesApplySubmitFprint {
			if err := submitFingerprintsForScenePlan(ctx, client, st, out, plan.KeeperSceneIDs); err != nil {
				return err
			}
		}
		return nil

	case "merge":
		plan, err := apply.PlanMerge(ctx, st)
		if err != nil {
			return err
		}
		if err := apply.PrintMergePlan(out, plan, flagScenesApplyCommit); err != nil {
			return err
		}
		if !flagScenesApplyCommit {
			return nil
		}
		if len(plan.Groups) == 0 {
			return nil
		}

		// Destructive action — gate behind interactive YES (or --yes).
		summary := confirm.Summary{
			Action:           "merge",
			GroupCount:       len(plan.Groups),
			SceneCount:       plan.TotalLosers + len(plan.Groups), // losers + keepers
			ReclaimableBytes: plan.ReclaimableBytes,
		}
		ok, err := confirm.PromptYES(os.Stdin, out, summary, flagScenesApplyYes)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		scorer, err := decide.NewFileScorer(cfg)
		if err != nil {
			return err
		}
		reports, err := apply.ExecuteMerge(ctx, client, st, cfg, scorer, plan)
		if err != nil {
			return err
		}
		if err := apply.PrintMergeReports(out, reports); err != nil {
			return err
		}
		if flagScenesApplySubmitFprint {
			keeperIDs := make([]string, 0, len(reports))
			for _, r := range reports {
				if r.Status == "success" && r.KeeperSceneID != "" {
					keeperIDs = append(keeperIDs, r.KeeperSceneID)
				}
			}
			if err := submitFingerprintsForScenePlan(ctx, client, st, out, keeperIDs); err != nil {
				return err
			}
		}
		return nil

	case "delete":
		return fmt.Errorf("scenes apply --action delete is Phase 2; use --action merge to reclaim space without losing metadata")
	default:
		return fmt.Errorf("unknown --action %q (try: tag|merge|delete)", flagScenesApplyAction)
	}
}

// submitFingerprintsForScenePlan is the post-commit fingerprint submission
// helper used by all apply paths that opt in via --submit-fingerprints.
// Idempotent: pairs already in fingerprint_submissions are skipped.
func submitFingerprintsForScenePlan(
	ctx context.Context,
	client *stash.Client,
	st *store.Store,
	out interface{ Write([]byte) (int, error) },
	keeperSceneIDs []string,
) error {
	if len(keeperSceneIDs) == 0 {
		return nil
	}
	report, err := apply.SubmitFingerprintsForScenes(ctx, client, st, keeperSceneIDs)
	if err != nil {
		return err
	}
	return apply.PrintFingerprintReport(out, report)
}

func runScenesMark(cmd *cobra.Command, args []string) error {
	if flagScenesMarkSignature == "" {
		return fmt.Errorf("--signature is required (run `stash-janitor scenes report` to find one)")
	}
	switch flagScenesMarkAs {
	case "not_duplicate", "dismiss", "force_keeper":
	case "":
		return fmt.Errorf("--as is required (one of: not_duplicate|dismiss|force_keeper)")
	default:
		return fmt.Errorf("unknown --as %q (try: not_duplicate|dismiss|force_keeper)", flagScenesMarkAs)
	}
	if flagScenesMarkAs == "force_keeper" && flagScenesMarkKeeper == "" {
		return fmt.Errorf("--keeper SCENE_ID is required when --as force_keeper")
	}

	_, st, _, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	d := store.UserDecision{
		Key:      flagScenesMarkSignature,
		Workflow: store.WorkflowScenes,
		Decision: flagScenesMarkAs,
		KeeperID: flagScenesMarkKeeper,
		Notes:    flagScenesMarkNotes,
	}
	if err := st.PutUserDecision(context.Background(), d); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"recorded: signature=%s decision=%s%s\nThis override applies to future `stash-janitor scenes scan` runs.\n",
		flagScenesMarkSignature, flagScenesMarkAs,
		map[bool]string{true: " keeper=" + flagScenesMarkKeeper, false: ""}[flagScenesMarkKeeper != ""],
	)
	return nil
}
