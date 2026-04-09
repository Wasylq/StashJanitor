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

// FilesPlan is the precomputed work for `stash-janitor files apply`.
//
// In Phase 1 this is purely for display: workflow B is report-only by
// default. Phase 1.5 will add an Execute method that calls
// sceneUpdate(primary_file_id) and deleteFiles for each plan entry.
type FilesPlan struct {
	Groups []*store.FileGroup

	// Per-scene actions: which file would become primary, which file IDs
	// would be deleted, how many bytes would be freed.
	Actions []FilesPlanAction
	TotalReclaimable int64
}

// FilesPlanAction is one scene's worth of work in a FilesPlan.
type FilesPlanAction struct {
	SceneID         string
	NewPrimaryFile  string   // file_id chosen as the new primary
	NewPrimaryPath  string
	WasPrimary      bool     // was this file already the primary?
	LoserFileIDs    []string // file_ids to delete
	LoserPaths      []string
	LoserBytes      int64
}

// PlanFiles walks the store and builds a FilesPlan from groups currently
// marked decided. It does not call Stash.
func PlanFiles(ctx context.Context, st *store.Store) (*FilesPlan, error) {
	groups, err := st.ListFileGroups(ctx, []string{store.StatusDecided})
	if err != nil {
		return nil, fmt.Errorf("listing decided file groups: %w", err)
	}
	plan := &FilesPlan{Groups: groups}

	for _, g := range groups {
		if g.AppliedAt != nil {
			continue
		}
		var action FilesPlanAction
		action.SceneID = g.SceneID
		for _, f := range g.Files {
			switch f.Role {
			case store.RoleKeeper:
				action.NewPrimaryFile = f.FileID
				action.NewPrimaryPath = f.Path
				action.WasPrimary = f.IsPrimary
			case store.RoleLoser:
				action.LoserFileIDs = append(action.LoserFileIDs, f.FileID)
				action.LoserPaths = append(action.LoserPaths, f.Path)
				action.LoserBytes += f.FileSize
			}
		}
		plan.Actions = append(plan.Actions, action)
		plan.TotalReclaimable += action.LoserBytes
	}
	return plan, nil
}

// PrintFilesPlan writes the human-readable preview used by both dry-run
// and the commit-mode confirmation prompt's "about to do this" header.
func PrintFilesPlan(w io.Writer, p *FilesPlan, commit bool) error {
	mode := "DRY RUN"
	if commit {
		mode = "COMMIT"
	}
	fmt.Fprintf(w, "=== stash-janitor files apply (%s) ===\n", mode)
	fmt.Fprintf(w, "Multi-file scenes in scope:  %d\n", len(p.Actions))
	fmt.Fprintf(w, "Reclaimable on commit:       %s\n", confirm.HumanBytes(p.TotalReclaimable))
	if len(p.Actions) == 0 {
		fmt.Fprintln(w, "\nNothing to do. Try `stash-janitor files scan` first.")
		return nil
	}

	fmt.Fprintln(w, "\nProposed actions:")
	for _, a := range p.Actions {
		fmt.Fprintf(w, "\n  scene %s\n", a.SceneID)
		marker := "(swap)"
		if a.WasPrimary {
			marker = "(keep as primary)"
		}
		fmt.Fprintf(w, "    promote primary → %s  %s\n", a.NewPrimaryFile, marker)
		fmt.Fprintf(w, "      %s\n", a.NewPrimaryPath)
		for i, id := range a.LoserFileIDs {
			fmt.Fprintf(w, "    delete file %s\n      %s\n", id, a.LoserPaths[i])
		}
		fmt.Fprintf(w, "    reclaim: %s\n", confirm.HumanBytes(a.LoserBytes))
	}
	if !commit {
		fmt.Fprintln(w, "\nThis was a dry run. Re-run with --commit to mutate Stash.")
		fmt.Fprintln(w, "--commit triggers an interactive YES confirmation; --yes bypasses it.")
	}
	return nil
}

// FilesReport is the per-scene outcome of ExecuteFiles.
type FilesReport struct {
	GroupID            int64
	SceneID            string
	NewPrimaryFileID   string
	PrimaryWasSwapped  bool
	FilesDeletedCount  int
	BytesReclaimed     int64
	Status             string // "success" | "skipped" | "failed"
	Error              string
}

// ExecuteFiles runs the files-apply pipeline for every action in the plan.
//
// Per scene:
//
//   1. If the chosen primary differs from the current primary, call
//      sceneUpdate(primary_file_id) to swap it.
//   2. Call deleteFiles(loserFileIDs) to delete the loser files from disk
//      via Stash. This is the actually-destructive step.
//   3. Mark the file group applied in the local store.
//
// Per-scene errors are captured in the returned FilesReport slice and the
// loop continues — at large library scale, one bad scene should not waste
// a full apply run. The outer error is non-nil only for catastrophic
// failures (e.g. nil deps).
//
// Caller (cli) is responsible for the YES prompt before invoking this.
func ExecuteFiles(
	ctx context.Context,
	c *stash.Client,
	st *store.Store,
	plan *FilesPlan,
) ([]*FilesReport, error) {
	if c == nil || st == nil || plan == nil {
		return nil, errors.New("ExecuteFiles: nil dependency")
	}
	reports := make([]*FilesReport, 0, len(plan.Actions))

	// Build an index from scene_id → group_id so we can mark applied
	// without re-querying the store.
	groupIDByScene := make(map[string]int64, len(plan.Groups))
	for _, g := range plan.Groups {
		groupIDByScene[g.SceneID] = g.ID
	}

	for _, a := range plan.Actions {
		report := &FilesReport{
			SceneID:           a.SceneID,
			GroupID:           groupIDByScene[a.SceneID],
			NewPrimaryFileID:  a.NewPrimaryFile,
			BytesReclaimed:    a.LoserBytes,
			PrimaryWasSwapped: !a.WasPrimary,
		}
		reports = append(reports, report)

		if !a.WasPrimary {
			if err := c.SetPrimaryFile(ctx, a.SceneID, a.NewPrimaryFile); err != nil {
				slog.Error("setPrimaryFile failed; continuing",
					"scene_id", a.SceneID, "new_primary", a.NewPrimaryFile, "error", err)
				report.Status = "failed"
				report.Error = "setPrimaryFile: " + err.Error()
				continue
			}
		}

		if len(a.LoserFileIDs) > 0 {
			if err := c.DeleteFiles(ctx, a.LoserFileIDs); err != nil {
				slog.Error("deleteFiles failed; continuing",
					"scene_id", a.SceneID, "ids", a.LoserFileIDs, "error", err)
				report.Status = "failed"
				report.Error = "deleteFiles: " + err.Error()
				continue
			}
			report.FilesDeletedCount = len(a.LoserFileIDs)
		}

		if report.GroupID > 0 {
			if err := st.MarkFileGroupApplied(ctx, report.GroupID); err != nil {
				report.Status = "failed"
				report.Error = "mutation succeeded but mark-applied failed: " + err.Error()
				continue
			}
		}
		report.Status = "success"
	}
	return reports, nil
}

// PrintFilesReports writes a summary of the per-scene outcomes after
// ExecuteFiles has finished.
func PrintFilesReports(w io.Writer, reports []*FilesReport) error {
	if len(reports) == 0 {
		return nil
	}
	var (
		ok      int
		failed  int
		bytes   int64
		swapped int
		deleted int
	)
	for _, r := range reports {
		switch r.Status {
		case "success":
			ok++
			bytes += r.BytesReclaimed
			if r.PrimaryWasSwapped {
				swapped++
			}
			deleted += r.FilesDeletedCount
		case "failed":
			failed++
		}
	}
	fmt.Fprintf(w, "\n=== files apply complete ===\n")
	fmt.Fprintf(w, "  successes:        %d\n", ok)
	fmt.Fprintf(w, "  failures:         %d\n", failed)
	fmt.Fprintf(w, "  primary swaps:    %d\n", swapped)
	fmt.Fprintf(w, "  files deleted:    %d\n", deleted)
	fmt.Fprintf(w, "  bytes reclaimed:  %s\n", confirm.HumanBytes(bytes))
	if failed > 0 {
		fmt.Fprintln(w, "\nFailed scenes:")
		for _, r := range reports {
			if r.Status == "failed" {
				fmt.Fprintf(w, "  scene %s: %s\n", r.SceneID, r.Error)
			}
		}
	}
	return nil
}
