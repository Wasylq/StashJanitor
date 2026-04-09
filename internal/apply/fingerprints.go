package apply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// FingerprintReport summarizes a SubmitFingerprints run for the cli to print.
type FingerprintReport struct {
	// Submitted is the (scene_id, endpoint) pairs that were sent to
	// stash-box during this run.
	Submitted []SubmittedPair
	// Skipped is the count of (scene_id, endpoint) pairs we already had
	// recorded in fingerprint_submissions.
	SkippedAlreadySubmitted int
	// SkippedNoStashID is the count of keeper scenes that have no stash_id
	// at all (and so cannot be submitted anywhere).
	SkippedNoStashID int
	// Failures collects per-endpoint errors. The submission loop continues
	// past failures so one bad endpoint doesn't kill the whole run.
	Failures []FingerprintFailure
}

// SubmittedPair records one (scene, endpoint) submission.
type SubmittedPair struct {
	SceneID  string
	Endpoint string
}

// FingerprintFailure records one failed batch submission.
type FingerprintFailure struct {
	Endpoint string
	Error    string
}

// SubmitFingerprintsForScenes fetches each keeper scene from Stash, groups
// the (scene_id, endpoint) pairs by endpoint, skips any pair already in
// fingerprint_submissions, and submits the rest. Records every successful
// (scene, endpoint) into the local store so re-runs are no-ops.
//
// Per-endpoint failures are captured in the report and the loop continues.
// The outer error is non-nil only for nil deps.
func SubmitFingerprintsForScenes(
	ctx context.Context,
	c *stash.Client,
	st *store.Store,
	keeperSceneIDs []string,
) (*FingerprintReport, error) {
	if c == nil || st == nil {
		return nil, errors.New("SubmitFingerprintsForScenes: nil dependency")
	}
	report := &FingerprintReport{}
	if len(keeperSceneIDs) == 0 {
		return report, nil
	}

	// Group new (scene, endpoint) pairs by endpoint, skipping anything
	// already submitted.
	byEndpoint := map[string][]string{}
	for _, sceneID := range keeperSceneIDs {
		scene, err := c.FindScene(ctx, sceneID)
		if err != nil {
			slog.Warn("fetching scene for fingerprint submission failed; skipping",
				"scene_id", sceneID, "error", err)
			continue
		}
		if scene == nil || len(scene.StashIDs) == 0 {
			report.SkippedNoStashID++
			continue
		}
		for _, sid := range scene.StashIDs {
			already, err := st.HasSubmittedFingerprints(ctx, sceneID, sid.Endpoint)
			if err != nil {
				slog.Warn("checking submission status failed; skipping",
					"scene_id", sceneID, "endpoint", sid.Endpoint, "error", err)
				continue
			}
			if already {
				report.SkippedAlreadySubmitted++
				continue
			}
			byEndpoint[sid.Endpoint] = append(byEndpoint[sid.Endpoint], sceneID)
		}
	}

	// One batch call per endpoint.
	for endpoint, sceneIDs := range byEndpoint {
		if err := c.SubmitStashBoxFingerprints(ctx, sceneIDs, endpoint); err != nil {
			slog.Error("submitStashBoxFingerprints failed; continuing",
				"endpoint", endpoint, "scene_count", len(sceneIDs), "error", err)
			report.Failures = append(report.Failures, FingerprintFailure{
				Endpoint: endpoint,
				Error:    err.Error(),
			})
			continue
		}
		// Record on success — one row per (scene, endpoint).
		for _, sceneID := range sceneIDs {
			if err := st.RecordFingerprintSubmission(ctx, sceneID, endpoint); err != nil {
				slog.Warn("recording fingerprint submission failed",
					"scene_id", sceneID, "endpoint", endpoint, "error", err)
				continue
			}
			report.Submitted = append(report.Submitted, SubmittedPair{
				SceneID:  sceneID,
				Endpoint: endpoint,
			})
		}
	}

	return report, nil
}

// PrintFingerprintReport writes a human-readable summary of the submission
// run to w. Always called after the action's own report so the user sees
// it as a tail block.
func PrintFingerprintReport(w io.Writer, r *FingerprintReport) error {
	fmt.Fprintln(w, "\n=== fingerprint submission ===")
	fmt.Fprintf(w, "  submitted:                %d (scene, endpoint) pairs\n", len(r.Submitted))
	fmt.Fprintf(w, "  skipped (already sent):   %d\n", r.SkippedAlreadySubmitted)
	fmt.Fprintf(w, "  skipped (no stash_id):    %d\n", r.SkippedNoStashID)
	if len(r.Failures) > 0 {
		fmt.Fprintf(w, "  endpoint failures:        %d\n", len(r.Failures))
		for _, f := range r.Failures {
			fmt.Fprintf(w, "    %s: %s\n", f.Endpoint, f.Error)
		}
	}
	if len(r.Submitted) > 0 {
		// Group by endpoint for readability.
		perEndpoint := map[string]int{}
		for _, p := range r.Submitted {
			perEndpoint[p.Endpoint]++
		}
		fmt.Fprintln(w, "  per endpoint:")
		for ep, n := range perEndpoint {
			fmt.Fprintf(w, "    %s: %d scenes\n", ep, n)
		}
	}
	return nil
}
