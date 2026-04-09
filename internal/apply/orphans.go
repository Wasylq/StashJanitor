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
func PrintOrphansPlan(w io.Writer, p *OrphansPlan, commit bool) error {
	mode := "DRY RUN"
	if commit {
		mode = "COMMIT"
	}
	fmt.Fprintf(w, "=== stash-janitor orphans apply (%s) ===\n", mode)
	fmt.Fprintf(w, "Matched orphans to link:  %d\n", len(p.Lookups))
	if len(p.Lookups) == 0 {
		fmt.Fprintln(w, "\nNothing to do. Try `stash-janitor orphans scan` first.")
		return nil
	}
	fmt.Fprintln(w, "\nFor each matched orphan, stash-janitor will call:")
	fmt.Fprintln(w, "  sceneUpdate(input: { id: <scene>, stash_ids: [{ endpoint, stash_id: <remote_id> }] })")
	fmt.Fprintln(w, "\nThis only ATTACHES the stash-box link. To pull metadata (tags, performers,")
	fmt.Fprintln(w, "studio, date) from stash-box, run Stash's built-in Scene Tagger after this")
	fmt.Fprintln(w, "completes.")
	if !commit {
		fmt.Fprintln(w, "\nThis was a dry run. Re-run with --commit to mutate Stash.")
		fmt.Fprintln(w, "--commit triggers an interactive YES confirmation; --yes bypasses it.")
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

// ExecuteOrphans writes each matched orphan's stash_id back to its scene.
//
// Caller (cli) is responsible for the YES prompt. Per-row errors are
// captured and the loop continues.
func ExecuteOrphans(
	ctx context.Context,
	c *stash.Client,
	st *store.Store,
	plan *OrphansPlan,
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

		// We need the scene's existing stash_ids so we can union the new
		// one in instead of replacing the list (a scene might already be
		// matched on a DIFFERENT endpoint).
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
					StashID:  r.MatchRemoteID, // overwrite with new value
				})
				continue
			}
			existing = append(existing, stash.StashIDInput{
				Endpoint: sid.Endpoint,
				StashID:  sid.StashID,
			})
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
