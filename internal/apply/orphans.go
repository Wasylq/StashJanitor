package apply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// OrphansPlan is the precomputed work for `stash-janitor orphans apply`.
//
// We only act on rows whose status is "matched" — no_match rows are
// nothing to do, and applied rows are already handled. Each row in
// Lookups becomes one sceneUpdate call during execute.
type OrphansPlan struct {
	Lookups []*store.OrphanLookup
}

// PlanOrphans walks the store and builds an OrphansPlan.
func PlanOrphans(ctx context.Context, st *store.Store) (*OrphansPlan, error) {
	rows, err := st.ListOrphanLookups(ctx, []string{store.StatusMatched})
	if err != nil {
		return nil, fmt.Errorf("listing matched orphan lookups: %w", err)
	}
	// Filter out anything that already has applied_at — defensive, the
	// status filter should already exclude these.
	var pending []*store.OrphanLookup
	for _, r := range rows {
		if r.AppliedAt == nil {
			pending = append(pending, r)
		}
	}
	return &OrphansPlan{Lookups: pending}, nil
}

// PrintOrphansPlan writes the human-readable summary.
func PrintOrphansPlan(w io.Writer, p *OrphansPlan, commit bool, writeStashID bool) error {
	mode := "DRY RUN"
	if commit {
		mode = "COMMIT"
	}
	fmt.Fprintf(w, "=== stash-janitor orphans apply (%s) ===\n", mode)
	fmt.Fprintf(w, "Matched orphans:  %d\n", len(p.Lookups))
	if len(p.Lookups) == 0 {
		fmt.Fprintln(w, "\nNothing to do. Try `stash-janitor orphans scan` first.")
		return nil
	}
	if !writeStashID {
		fmt.Fprintln(w, "\norphans.write_stash_id_on_apply is false (default).")
		fmt.Fprintln(w, "stash-janitor will NOT write anything to Stash. To handle matches:")
		fmt.Fprintln(w, "  1. Run `stash-janitor orphans report --filter matched` to see what was found")
		fmt.Fprintln(w, "  2. In Stash UI, use the Scene Tagger to link and pull metadata")
		fmt.Fprintln(w, "\n--commit will mark these as reviewed in the local cache so future")
		fmt.Fprintln(w, "scans skip them. No Stash mutations.")
		fmt.Fprintln(w, "\nTo enable auto-linking, set orphans.write_stash_id_on_apply: true in config.")
	} else {
		fmt.Fprintln(w, "\nFor each matched orphan, stash-janitor will write the stash_id link via sceneUpdate.")
		fmt.Fprintln(w, "Then run Stash's Scene Tagger to pull full metadata (performers, studio, tags).")
	}
	if !commit {
		fmt.Fprintln(w, "\nThis was a dry run. Re-run with --commit to proceed.")
	}
	return nil
}

// OrphansReport is the per-row outcome of ExecuteOrphans.
type OrphansReport struct {
	SceneID  string
	Endpoint string
	RemoteID string
	Status   string // "success" | "failed"
	Error    string
}

// ExecuteOrphansOpts controls optional behavior during orphan apply.
type ExecuteOrphansOpts struct {
	// WriteStashID, when true, writes the stash_id link onto the scene.
	// Default false — the orphans workflow is purely discovery by default.
	// The user reviews matches in the report and handles linking in
	// Stash's UI (Scene Tagger).
	WriteStashID bool

	// WriteMetadata, when true AND WriteStashID is also true, also sets
	// title and date from the scrape result (only when the scene's
	// current value is empty). Default false.
	WriteMetadata bool
}

// ExecuteOrphans writes each matched orphan's stash_id back to its scene.
// When opts.WriteMetadata is true, also writes title and date from the
// stored scrape result.
//
// Caller (cli) is responsible for the YES prompt. Per-row errors are
// captured and the loop continues.
func ExecuteOrphans(
	ctx context.Context,
	c *stash.Client,
	st *store.Store,
	plan *OrphansPlan,
	opts ExecuteOrphansOpts,
) ([]*OrphansReport, error) {
	if c == nil || st == nil || plan == nil {
		return nil, errors.New("ExecuteOrphans: nil dependency")
	}
	reports := make([]*OrphansReport, 0, len(plan.Lookups))

	for _, r := range plan.Lookups {
		report := &OrphansReport{
			SceneID:  r.SceneID,
			Endpoint: r.Endpoint,
			RemoteID: r.MatchRemoteID,
		}
		reports = append(reports, report)

		if r.MatchRemoteID == "" {
			report.Status = "failed"
			report.Error = "row has no match_remote_id"
			continue
		}

		if !opts.WriteStashID {
			// Discovery-only mode: just mark applied in the local cache
			// so re-runs skip it. No mutations to Stash.
			if err := st.MarkOrphanLookupApplied(ctx, r.ID); err != nil {
				report.Status = "failed"
				report.Error = "mark-applied failed: " + err.Error()
				continue
			}
			report.Status = "success"
			continue
		}

		// --- Write mode (both config flags must be on) ---

		// Fetch the scene's existing stash_ids so we can union the new
		// one in instead of replacing the list.
		scene, err := c.FindScene(ctx, r.SceneID)
		if err != nil {
			report.Status = "failed"
			report.Error = "fetching scene: " + err.Error()
			continue
		}
		if scene == nil {
			report.Status = "failed"
			report.Error = "scene no longer exists in stash"
			continue
		}

		// Build the union: keep existing stash_ids for other endpoints,
		// add or replace the one for this endpoint.
		existing := make([]stash.StashIDInput, 0, len(scene.StashIDs)+1)
		hasThisEndpoint := false
		for _, sid := range scene.StashIDs {
			if sid.Endpoint == r.Endpoint {
				hasThisEndpoint = true
				existing = append(existing, stash.StashIDInput{
					Endpoint: sid.Endpoint,
					StashID:  r.MatchRemoteID,
				})
				continue
			}
			existing = append(existing, stash.StashIDInput(sid))
		}
		if !hasThisEndpoint {
			existing = append(existing, stash.StashIDInput{
				Endpoint: r.Endpoint,
				StashID:  r.MatchRemoteID,
			})
		}

		if err := c.SetSceneStashIDs(ctx, r.SceneID, existing); err != nil {
			slog.Error("sceneUpdate(stash_ids) failed; continuing",
				"scene_id", r.SceneID, "error", err)
			report.Status = "failed"
			report.Error = err.Error()
			continue
		}

		// Optionally write title + date from the scrape.
		if opts.WriteMetadata && (r.MatchTitle != "" || r.MatchDate != "") {
			titleToSet := ""
			dateToSet := ""
			if scene.Title == "" && r.MatchTitle != "" {
				titleToSet = r.MatchTitle
			}
			if scene.Date == "" && r.MatchDate != "" {
				dateToSet = r.MatchDate
			}
			if titleToSet != "" || dateToSet != "" {
				if err := c.SetSceneTitleDate(ctx, r.SceneID, titleToSet, dateToSet); err != nil {
					slog.Warn("writing title/date failed; stash_id was already set so continuing",
						"scene_id", r.SceneID, "error", err)
				}
			}
		}

		if err := st.MarkOrphanLookupApplied(ctx, r.ID); err != nil {
			report.Status = "failed"
			report.Error = "mutation succeeded but mark-applied failed: " + err.Error()
			continue
		}
		report.Status = "success"
	}
	return reports, nil
}

// PrintOrphansReports writes a summary after ExecuteOrphans.
func PrintOrphansReports(w io.Writer, reports []*OrphansReport) error {
	if len(reports) == 0 {
		return nil
	}
	var ok, failed int
	for _, r := range reports {
		switch r.Status {
		case "success":
			ok++
		case "failed":
			failed++
		}
	}
	fmt.Fprintf(w, "\n=== orphans apply complete ===\n")
	fmt.Fprintf(w, "  successes: %d\n", ok)
	fmt.Fprintf(w, "  failures:  %d\n", failed)
	if failed > 0 {
		fmt.Fprintln(w, "\nFailed:")
		for _, r := range reports {
			if r.Status == "failed" {
				fmt.Fprintf(w, "  scene %s (endpoint %s): %s\n", r.SceneID, r.Endpoint, r.Error)
			}
		}
	}
	return nil
}

// _ keeps confirm imported even if the cli stops calling our prompt
// helper from this file. Tagged for clarity in linters.
var _ = confirm.HumanBytes
