// Package scan contains the orchestration code that pulls duplicate groups
// from Stash, scores them, and persists the results into the local store.
//
// Each entry-point (Scenes for workflow A, Files for workflow B) is a thin
// glue layer: fetch from stash, convert to store snapshots, run the
// scorer, upsert. The conversion functions live alongside the scan they're
// for so adding a field is a single-file change.
package scan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Wasylq/StashJanitor/internal/decide"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// ScenesOptions configures one invocation of `stash-janitor scenes scan`.
type ScenesOptions struct {
	Distance     int
	DurationDiff float64
	// MaxGroups, when > 0, stops processing after N groups so the user can
	// iterate scan/review/apply in chunks at large library sizes.
	MaxGroups int
}

// ScenesResult is what the caller (cli layer) reports to the user after a
// successful scan.
type ScenesResult struct {
	ScanRunID    int64
	GroupCount   int
	NewGroups    int
	Decided      int
	NeedsReview  int
	Dismissed    int
}

// Scenes runs workflow A's scan-and-decide pipeline:
//
//  1. start a scan_runs row,
//  2. call findDuplicateScenes,
//  3. for each returned group: convert to store snapshot, apply any
//     persistent user_decisions override (e.g. "not_duplicate"), otherwise
//     score it with the configured scorer and upsert,
//  4. finalize the scan_runs row.
//
// Returns counts so the cli can print a one-line summary. Errors from
// individual groups are logged and the scan continues — we don't want one
// bad scene to abort the whole run at 60k library scale.
func Scenes(ctx context.Context, c *stash.Client, st *store.Store, scorer *decide.SceneScorer, opts ScenesOptions) (*ScenesResult, error) {
	if c == nil || st == nil || scorer == nil {
		return nil, errors.New("scan.Scenes: client, store, and scorer are required")
	}

	dist := opts.Distance
	dur := opts.DurationDiff
	runID, err := st.StartScanRun(ctx, store.WorkflowScenes, &dist, &dur)
	if err != nil {
		return nil, fmt.Errorf("starting scan run: %w", err)
	}

	slog.Info("fetching duplicate groups from stash",
		"distance", opts.Distance,
		"duration_diff", opts.DurationDiff,
	)
	groups, err := c.FindDuplicateScenes(ctx, opts.Distance, opts.DurationDiff)
	if err != nil {
		return nil, fmt.Errorf("findDuplicateScenes: %w", err)
	}
	slog.Info("stash returned duplicate groups", "count", len(groups))

	res := &ScenesResult{ScanRunID: runID}

	for i, raw := range groups {
		if opts.MaxGroups > 0 && res.GroupCount >= opts.MaxGroups {
			slog.Info("max-groups limit reached, stopping",
				"limit", opts.MaxGroups,
				"remaining", len(groups)-i,
			)
			break
		}
		if len(raw) < 2 {
			// findDuplicateScenes shouldn't return singletons but be defensive.
			continue
		}

		sceneIDs := make([]string, len(raw))
		for j, sc := range raw {
			sceneIDs[j] = sc.ID
		}
		signature := store.SceneGroupSignature(sceneIDs)

		processed, err := processSceneGroup(ctx, st, scorer, runID, signature, raw)
		if err != nil {
			slog.Error("processing duplicate group failed; continuing",
				"group_index", i,
				"signature", signature,
				"error", err,
			)
			continue
		}
		res.GroupCount++
		switch processed.Status {
		case store.StatusDecided:
			res.Decided++
		case store.StatusNeedsReview:
			res.NeedsReview++
		case store.StatusDismissed:
			res.Dismissed++
		}
		if processed.NewlyCreated {
			res.NewGroups++
		}
	}

	if err := st.FinishScanRun(ctx, runID, res.GroupCount); err != nil {
		return nil, fmt.Errorf("finishing scan run: %w", err)
	}
	return res, nil
}

// processedGroup is the per-group return from processSceneGroup.
type processedGroup struct {
	Status       string
	NewlyCreated bool
}

// processSceneGroup converts one duplicate group from stash format to a
// store snapshot, applies any persistent user override, runs the scorer,
// and upserts. Pulled out as a separate function so it's easy to unit-test
// without hitting Stash.
func processSceneGroup(
	ctx context.Context,
	st *store.Store,
	scorer *decide.SceneScorer,
	runID int64,
	signature string,
	raw []stash.Scene,
) (*processedGroup, error) {
	// Build the snapshot first; we score against the snapshot so the same
	// data the report shows is what the decider used.
	scenes := make([]store.SceneGroupScene, len(raw))
	for i := range raw {
		scenes[i] = sceneSnapshot(&raw[i], scorer)
	}

	// Look up any persistent user override for this group. Stable signature
	// means the override survives re-scans.
	var (
		status        string
		reason        string
		userDecision  *store.UserDecision
		keeperIDForce string
	)
	if d, err := st.GetUserDecision(ctx, signature); err == nil {
		userDecision = d
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("looking up user_decisions: %w", err)
	}

	switch {
	case userDecision != nil && userDecision.Decision == "not_duplicate":
		status = store.StatusDismissed
		reason = "user marked not_duplicate"
	case userDecision != nil && userDecision.Decision == "dismiss":
		status = store.StatusDismissed
		reason = "user dismissed"
	case userDecision != nil && userDecision.Decision == "force_keeper":
		keeperIDForce = userDecision.KeeperID
		fallthrough
	default:
		// Score the group. If keeperIDForce is set we honor that instead
		// of the scorer's pick.
		decision := scorer.DecideScenes(scenes)
		var keeperIdx int
		if keeperIDForce != "" {
			keeperIdx = -1
			for i := range scenes {
				if scenes[i].SceneID == keeperIDForce {
					keeperIdx = i
					break
				}
			}
			if keeperIdx == -1 {
				// User pinned a keeper that's no longer in the group.
				status = store.StatusNeedsReview
				reason = fmt.Sprintf("force_keeper scene %s no longer in this group", keeperIDForce)
			} else {
				status = store.StatusDecided
				reason = fmt.Sprintf("force_keeper override (user pinned scene %s)", keeperIDForce)
			}
		} else if decision.KeeperIndex == -1 {
			status = store.StatusNeedsReview
			reason = decision.Reason
		} else {
			status = store.StatusDecided
			keeperIdx = decision.KeeperIndex
		}

		// Safety net: don't auto-decide cases where the keeper has a junk
		// filename but a loser has a structured filename. SKIP this check
		// when the user explicitly picked a keeper via force_keeper — they
		// already reviewed it and the rename-on-merge feature handles the
		// filename preservation.
		if status == store.StatusDecided && keeperIDForce == "" {
			if reviewReason := detectFilenameInfoLoss(scenes, keeperIdx); reviewReason != "" {
				status = store.StatusNeedsReview
				reason = reviewReason
			}
		}

		// Assign roles based on the chosen keeper. needs_review groups get
		// every scene marked undecided so the apply step can recognize
		// they shouldn't be touched.
		switch status {
		case store.StatusDecided:
			for i := range scenes {
				if i == keeperIdx {
					scenes[i].Role = store.RoleKeeper
				} else {
					scenes[i].Role = store.RoleLoser
				}
			}
		case store.StatusNeedsReview:
			for i := range scenes {
				scenes[i].Role = store.RoleUndecided
			}
		}
	}

	// For dismissed groups, every scene is undecided.
	if status == store.StatusDismissed {
		for i := range scenes {
			scenes[i].Role = store.RoleUndecided
		}
	}

	// Detect whether this is a brand-new group so the cli can report it.
	// We check by signature against the existing rows; if there's no row
	// yet, it's new.
	newlyCreated := true
	if existing, err := st.GetSceneGroupBySignature(ctx, signature); err == nil && existing != nil {
		newlyCreated = false
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("checking existing group: %w", err)
	}

	g := &store.SceneGroup{
		ScanRunID:      runID,
		Signature:      signature,
		Status:         status,
		DecisionReason: reason,
		Scenes:         scenes,
	}
	if err := st.UpsertSceneGroup(ctx, g); err != nil {
		return nil, fmt.Errorf("upserting scene group: %w", err)
	}

	return &processedGroup{Status: status, NewlyCreated: newlyCreated}, nil
}

// sceneSnapshot converts a stash.Scene into the store snapshot we score
// against. The "primary" file is used as the source of tech specs because
// that's what Stash itself displays — see PLAN.md section 7.
func sceneSnapshot(s *stash.Scene, scorer *decide.SceneScorer) store.SceneGroupScene {
	out := store.SceneGroupScene{
		SceneID:        s.ID,
		Role:           store.RoleUndecided,
		Organized:      s.Organized,
		HasStashID:     s.HasStashID(),
		TagCount:       len(s.Tags),
		PerformerCount: len(s.Performers),
	}
	if pf := s.PrimaryFile(); pf != nil {
		out.Width = pf.Width
		out.Height = pf.Height
		out.Bitrate = pf.BitRate
		out.Framerate = pf.FrameRate
		out.Codec = strings.ToLower(pf.VideoCodec)
		out.FileSize = pf.Size
		out.Duration = pf.Duration
		out.PrimaryPath = pf.Path
		out.FilenameQuality = scorer.ClassifyFilename(pf.Basename)
	}
	return out
}

// detectFilenameInfoLoss returns a non-empty reason when the chosen keeper
// has FilenameQuality=0 but at least one loser has FilenameQuality=1. The
// loser's filename encodes information (date, performer, title per the
// user's convention) that exists nowhere else in Stash, so deleting that
// loser would silently lose data.
//
// Returns "" when the keeper either matches the regex itself or no loser
// does — both safe cases.
func detectFilenameInfoLoss(scenes []store.SceneGroupScene, keeperIdx int) string {
	keeper := &scenes[keeperIdx]
	if keeper.FilenameQuality == 1 {
		return ""
	}
	for i := range scenes {
		if i == keeperIdx {
			continue
		}
		if scenes[i].FilenameQuality == 1 {
			return fmt.Sprintf(
				"filename info loss risk: keeper %s has unstructured filename but loser %s has a structured one — manual review to preserve filename-encoded metadata",
				keeper.SceneID, scenes[i].SceneID,
			)
		}
	}
	return ""
}
