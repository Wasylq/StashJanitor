package apply

import (
	"context"
	"fmt"
	"io"

	"github.com/Wasylq/StashJanitor/internal/confirm"
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

// PrintFilesPlan writes the human-readable preview. Always report-only in
// Phase 1; the function takes a `commit` flag for forward compatibility
// with Phase 1.5 but currently rejects commit=true with a clear message.
func PrintFilesPlan(w io.Writer, p *FilesPlan, commit bool) error {
	mode := "REPORT"
	if commit {
		mode = "COMMIT (Phase 1.5 only)"
	}
	fmt.Fprintf(w, "=== stash-janitor files apply (%s) ===\n", mode)
	fmt.Fprintf(w, "Multi-file scenes in scope:  %d\n", len(p.Actions))
	fmt.Fprintf(w, "Reclaimable on commit:       %s\n", confirm.HumanBytes(p.TotalReclaimable))
	if len(p.Actions) == 0 {
		fmt.Fprintln(w, "\nNothing to do. Try `stash-janitor files scan` first.")
		return nil
	}

	fmt.Fprintln(w, "\nProposed actions (Phase 1.5 will execute these on --commit):")
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
		fmt.Fprintln(w, "\nThis is a Phase 1 report. The destructive --commit path lands in Phase 1.5.")
	}
	return nil
}
