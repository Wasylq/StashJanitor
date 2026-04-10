package cli

import (
	"context"
	"fmt"

	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/report"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
	"github.com/spf13/cobra"
)

func newStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show a top-level dashboard of your library and stash-janitor state",
		Long: `Combines live Stash library numbers with the local stash-janitor cache
state into a single 'where am I?' summary. Useful as the first thing
you run in a new session before deciding which workflow to run next.`,
		RunE: runStats,
	}
}

func runStats(cmd *cobra.Command, args []string) error {
	_, st, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	// Stash version.
	ver, _, err := client.Version(ctx)
	if err != nil {
		return fmt.Errorf("connecting to stash: %w", err)
	}

	// Library numbers from Stash.
	stats, err := client.LibraryStats(ctx)
	if err != nil {
		return err
	}

	orphanCount := stats.SceneCount - stats.WithMetadata
	metaPct := float64(0)
	if stats.SceneCount > 0 {
		metaPct = float64(stats.WithMetadata) / float64(stats.SceneCount) * 100
	}

	fmt.Fprintf(out, "=== stash-janitor stats ===\n")
	fmt.Fprintf(out, "Stash version:     %s\n", ver)
	fmt.Fprintf(out, "\n")
	fmt.Fprintf(out, "Library:\n")
	fmt.Fprintf(out, "  scenes:          %d\n", stats.SceneCount)
	fmt.Fprintf(out, "  total size:      %.1f GiB\n", stats.ScenesSizeGB)
	fmt.Fprintf(out, "  total duration:  %.0f hours\n", stats.ScenesDuration/3600)
	fmt.Fprintf(out, "  with metadata:   %d (%.0f%%)\n", stats.WithMetadata, metaPct)
	fmt.Fprintf(out, "  orphans:         %d (%.0f%%)\n", orphanCount, 100-metaPct)

	// Workflow A summary from cache.
	fmt.Fprintf(out, "\nWorkflow A (cross-scene duplicates):\n")
	if ss, err := report.ComputeScenesStatus(ctx, st); err == nil && ss.LastScanRunID > 0 {
		fmt.Fprintf(out, "  last scan:       #%d\n", ss.LastScanRunID)
		fmt.Fprintf(out, "  groups:          %d  (decided: %d, needs_review: %d, applied: %d)\n",
			ss.TotalGroups, ss.Decided, ss.NeedsReview, ss.Applied)
		fmt.Fprintf(out, "  reclaimable:     %s\n", confirm.HumanBytes(ss.ReclaimableBytes))
	} else {
		fmt.Fprintf(out, "  (not scanned yet — run `stash-janitor scenes scan`)\n")
	}

	// Workflow B summary.
	fmt.Fprintf(out, "\nWorkflow B (within-scene multi-files):\n")
	if fs, err := report.ComputeFilesStatus(ctx, st); err == nil && fs.LastScanRunID > 0 {
		fmt.Fprintf(out, "  last scan:       #%d\n", fs.LastScanRunID)
		fmt.Fprintf(out, "  scenes:          %d  (decided: %d, needs_review: %d, applied: %d)\n",
			fs.TotalGroups, fs.Decided, fs.NeedsReview, fs.Applied)
		fmt.Fprintf(out, "  reclaimable:     %s\n", confirm.HumanBytes(fs.ReclaimableBytes))
	} else {
		fmt.Fprintf(out, "  (not scanned yet — run `stash-janitor files scan`)\n")
	}

	// Workflow C summary.
	fmt.Fprintf(out, "\nWorkflow C (orphans stash-box lookup):\n")
	if os, err := report.ComputeOrphansStatus(ctx, st); err == nil && os.LastScanRunID > 0 {
		fmt.Fprintf(out, "  last scan:       #%d\n", os.LastScanRunID)
		fmt.Fprintf(out, "  looked up:       %d  (matched: %d, no_match: %d, applied: %d)\n",
			os.TotalLookups, os.Matched, os.NoMatch, os.Applied)
	} else {
		fmt.Fprintf(out, "  (not scanned yet — run `stash-janitor orphans scan`)\n")
	}

	// Stale-cache check: sample a few cached scene IDs and verify they
	// still exist in Stash. This catches the case where the user deleted
	// scenes in Stash's UI but hasn't re-scanned in stash-janitor.
	stale := checkStaleness(ctx, client, st)
	if stale > 0 {
		fmt.Fprintf(out, "\n⚠ Cache staleness: %d sampled scene(s) no longer exist in Stash.\n", stale)
		fmt.Fprintf(out, "  Consider re-scanning: `stash-janitor scenes scan`, `stash-janitor files scan`.\n")
	}

	return nil
}

// checkStaleness samples up to 5 scene IDs from the local cache and
// verifies each via FindScene. Returns the count that returned nil
// (scene no longer exists in Stash). Returns 0 if the cache is empty.
func checkStaleness(ctx context.Context, client *stash.Client, st *store.Store) int {
	// Collect up to 5 unique scene IDs from scene_groups.
	groups, err := st.ListSceneGroups(ctx, nil)
	if err != nil {
		return 0
	}
	seen := map[string]bool{}
	var sample []string
	for _, g := range groups {
		for _, s := range g.Scenes {
			if !seen[s.SceneID] {
				seen[s.SceneID] = true
				sample = append(sample, s.SceneID)
				if len(sample) >= 5 {
					break
				}
			}
		}
		if len(sample) >= 5 {
			break
		}
	}
	if len(sample) == 0 {
		return 0
	}

	stale := 0
	for _, id := range sample {
		scene, err := client.FindScene(ctx, id)
		if err != nil {
			continue // network error — don't count as stale
		}
		if scene == nil {
			stale++
		}
	}
	return stale
}
