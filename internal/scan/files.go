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

// FilesOptions configures one invocation of `stash-janitor files scan`.
type FilesOptions struct {
	// PerPage is the page size used when paginating findScenes.
	// Defaults to 100 if zero.
	PerPage int
	// MaxScenes, when > 0, stops the scan after processing N multi-file
	// scenes — useful at large library scale to break the work into chunks.
	MaxScenes int
}

// FilesResult is what the cli prints after a successful scan.
type FilesResult struct {
	ScanRunID    int64
	SceneCount   int
	NewGroups    int
	Decided      int
	NeedsReview  int
	Dismissed    int
}

// Files runs workflow B's scan-and-decide pipeline.
//
//  1. start a scan_runs row,
//  2. paginate findScenes(file_count > 1),
//  3. for each multi-file scene: build snapshot, classify each file's
//     filename, apply user_decisions override, run scorer, upsert,
//  4. finalize the scan_runs row.
//
// Workflow B never fetches every scene from Stash — the file_count filter
// keeps the result set small.
func Files(ctx context.Context, c *stash.Client, st *store.Store, scorer *decide.FileScorer, opts FilesOptions) (*FilesResult, error) {
	if c == nil || st == nil || scorer == nil {
		return nil, errors.New("scan.Files: client, store, and scorer are required")
	}
	perPage := opts.PerPage
	if perPage <= 0 {
		perPage = 100
	}

	runID, err := st.StartScanRun(ctx, store.WorkflowFiles, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("starting scan run: %w", err)
	}

	res := &FilesResult{ScanRunID: runID}

	page := 1
	totalKnown := -1
	for {
		slog.Info("fetching multi-file scenes page",
			"page", page,
			"per_page", perPage,
		)
		result, err := c.FindMultiFileScenesPage(ctx, page, perPage)
		if err != nil {
			return nil, fmt.Errorf("findMultiFileScenes(page=%d): %w", page, err)
		}
		if totalKnown == -1 {
			totalKnown = result.Count
			slog.Info("multi-file scenes total", "count", totalKnown)
		}
		if len(result.Scenes) == 0 {
			break
		}

		for i := range result.Scenes {
			if opts.MaxScenes > 0 && res.SceneCount >= opts.MaxScenes {
				slog.Info("max-scenes limit reached, stopping",
					"limit", opts.MaxScenes,
					"remaining", totalKnown-res.SceneCount,
				)
				if err := st.FinishScanRun(ctx, runID, res.SceneCount); err != nil {
					return nil, err
				}
				return res, nil
			}

			processed, err := processFileGroup(ctx, st, scorer, runID, &result.Scenes[i])
			if err != nil {
				slog.Error("processing multi-file scene failed; continuing",
					"scene_id", result.Scenes[i].ID,
					"error", err,
				)
				continue
			}
			res.SceneCount++
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

		// Stop when we've seen everything Stash reported.
		if res.SceneCount >= totalKnown {
			break
		}
		page++
	}

	if err := st.FinishScanRun(ctx, runID, res.SceneCount); err != nil {
		return nil, fmt.Errorf("finishing scan run: %w", err)
	}
	return res, nil
}

// processFileGroup converts one multi-file scene into a store.FileGroup,
// applies any user override, runs the scorer, and upserts.
func processFileGroup(
	ctx context.Context,
	st *store.Store,
	scorer *decide.FileScorer,
	runID int64,
	scene *stash.Scene,
) (*processedGroup, error) {
	if len(scene.Files) < 2 {
		return nil, fmt.Errorf("scene %s has %d files; expected >= 2", scene.ID, len(scene.Files))
	}

	files := make([]store.FileGroupFile, len(scene.Files))
	for i := range scene.Files {
		files[i] = fileSnapshot(&scene.Files[i], scorer, i == 0 /* primary is files[0] */)
	}

	// User override lookup; key is "scene:<id>" for workflow B.
	key := "scene:" + scene.ID
	var userDecision *store.UserDecision
	if d, err := st.GetUserDecision(ctx, key); err == nil {
		userDecision = d
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("looking up user_decisions: %w", err)
	}

	var (
		status string
		reason string
	)
	switch {
	case userDecision != nil && userDecision.Decision == "keep_all":
		status = store.StatusDismissed
		reason = "user marked keep_all"
		for i := range files {
			files[i].Role = store.RoleUndecided
		}
	case userDecision != nil && userDecision.Decision == "dismiss":
		status = store.StatusDismissed
		reason = "user dismissed"
		for i := range files {
			files[i].Role = store.RoleUndecided
		}
	default:
		decision := scorer.DecideFiles(files)
		if decision.KeeperIndex == -1 {
			status = store.StatusNeedsReview
			reason = decision.Reason
			for i := range files {
				files[i].Role = store.RoleUndecided
			}
		} else {
			status = store.StatusDecided
			for i := range files {
				if i == decision.KeeperIndex {
					files[i].Role = store.RoleKeeper
				} else {
					files[i].Role = store.RoleLoser
				}
			}
		}
	}

	// Detect newly-created groups for the cli summary.
	newlyCreated := true
	{
		existing, err := st.ListFileGroups(ctx, nil)
		if err != nil {
			return nil, err
		}
		for _, fg := range existing {
			if fg.SceneID == scene.ID {
				newlyCreated = false
				break
			}
		}
	}

	fg := &store.FileGroup{
		ScanRunID:      runID,
		SceneID:        scene.ID,
		Status:         status,
		DecisionReason: reason,
		Files:          files,
	}
	if err := st.UpsertFileGroup(ctx, fg); err != nil {
		return nil, fmt.Errorf("upserting file group: %w", err)
	}
	return &processedGroup{Status: status, NewlyCreated: newlyCreated}, nil
}

// fileSnapshot converts a stash.VideoFile to a store.FileGroupFile,
// classifying the basename through the scorer's regex along the way.
func fileSnapshot(f *stash.VideoFile, scorer *decide.FileScorer, isPrimary bool) store.FileGroupFile {
	return store.FileGroupFile{
		FileID:          f.ID,
		Role:            store.RoleUndecided,
		IsPrimary:       isPrimary,
		Basename:        f.Basename,
		Path:            f.Path,
		ModTime:         f.ModTime,
		FilenameQuality: scorer.ClassifyFilename(f.Basename),
		Width:           f.Width,
		Height:          f.Height,
		Bitrate:         f.BitRate,
		Framerate:       f.FrameRate,
		Codec:           strings.ToLower(f.VideoCodec),
		FileSize:        f.Size,
	}
}
