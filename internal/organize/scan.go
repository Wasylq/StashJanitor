package organize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// ScanOptions configures one invocation of `stash-janitor organize scan`.
type ScanOptions struct {
	PerPage   int
	MaxScenes int
}

// ScanResult summarizes the scan for the CLI.
type ScanResult struct {
	ScanRunID       int64
	ScenesProcessed int
	Moves           int
	Renames         int
	AlreadyCorrect  int
	SkipNoMetadata  int
	Conflicts       int
}

// Scan walks every scene in Stash, computes the ideal path per the
// template, and stores the plan in organize_plans. Conflict detection
// runs at the end: if two files map to the same target, both are marked
// as conflicts.
func Scan(ctx context.Context, c *stash.Client, st *store.Store, cfg *config.Config, opts ScanOptions) (*ScanResult, error) {
	if c == nil || st == nil {
		return nil, errors.New("organize.Scan: client and store are required")
	}
	orgCfg := &cfg.Organize
	if orgCfg.BaseDir == "" {
		return nil, errors.New("organize.base_dir must be set in config")
	}
	if orgCfg.PathTemplate == "" {
		return nil, errors.New("organize.path_template must be set in config")
	}
	perPage := opts.PerPage
	if perPage <= 0 {
		perPage = 100
	}

	runID, err := st.StartScanRun(ctx, store.WorkflowOrganize, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("starting scan run: %w", err)
	}

	res := &ScanResult{ScanRunID: runID}
	targetSeen := map[string]*store.OrganizePlan{} // target_path → first plan that claimed it
	page := 1
	totalScenes := -1

	for {
		slog.Info("fetching scenes page", "page", page, "per_page", perPage)
		result, err := c.FindAllScenesPage(ctx, page, perPage)
		if err != nil {
			return nil, fmt.Errorf("findScenes(page=%d): %w", page, err)
		}
		if totalScenes == -1 {
			totalScenes = result.Count
			slog.Info("total scenes in library", "count", totalScenes)
		}
		if len(result.Scenes) == 0 {
			break
		}

		for i := range result.Scenes {
			if opts.MaxScenes > 0 && res.ScenesProcessed >= opts.MaxScenes {
				goto done
			}
			res.ScenesProcessed++
			scene := &result.Scenes[i]
			pf := scene.PrimaryFile()
			if pf == nil {
				continue
			}

			target, skipReason := ComputeTargetPath(scene, pf, orgCfg)
			plan := &store.OrganizePlan{
				ScanRunID:   runID,
				SceneID:     scene.ID,
				FileID:      pf.ID,
				CurrentPath: pf.Path,
			}

			if skipReason != "" {
				plan.Status = "skip_no_metadata"
				plan.Reason = skipReason
				plan.TargetPath = pf.Path
				res.SkipNoMetadata++
			} else if target == pf.Path {
				plan.Status = "already_correct"
				plan.TargetPath = target
				res.AlreadyCorrect++
			} else if filepath.Dir(target) == filepath.Dir(pf.Path) && orgCfg.RenameInPlace {
				plan.Status = "rename"
				plan.TargetPath = target
				res.Renames++
			} else if filepath.Dir(target) == filepath.Dir(pf.Path) && !orgCfg.RenameInPlace {
				plan.Status = "already_correct"
				plan.TargetPath = pf.Path
				plan.Reason = "in correct folder; rename_in_place is off"
				res.AlreadyCorrect++
			} else {
				plan.Status = "move"
				plan.TargetPath = target
				res.Moves++
			}

			// Track for conflict detection (below).
			if plan.Status == "move" || plan.Status == "rename" {
				if existing, ok := targetSeen[target]; ok {
					// Conflict: two different files → same target.
					if existing.Status != "conflict" {
						existing.Status = "conflict"
						existing.Reason = fmt.Sprintf("conflicts with file %s (scene %s)", plan.FileID, plan.SceneID)
						res.Conflicts++
						res.Moves--
					}
					plan.Status = "conflict"
					plan.Reason = fmt.Sprintf("conflicts with file %s (scene %s)", existing.FileID, existing.SceneID)
					res.Conflicts++
					if plan.Status == "move" {
						res.Moves--
					} else {
						res.Renames--
					}
				} else {
					targetSeen[target] = plan
				}
			}

			if err := st.UpsertOrganizePlan(ctx, plan); err != nil {
				slog.Error("upsert organize plan failed; continuing",
					"scene_id", scene.ID, "error", err)
			}
		}

		if res.ScenesProcessed >= totalScenes {
			break
		}
		page++
	}

done:
	if err := st.FinishScanRun(ctx, runID, res.ScenesProcessed); err != nil {
		return nil, fmt.Errorf("finishing scan run: %w", err)
	}
	return res, nil
}
