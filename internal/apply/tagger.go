// Package apply executes the resolution actions on scoring decisions.
//
// Each action (tag, merge, delete, files-prune) is its own file. The shared
// rules across all actions:
//
//   - Dry-run is the default at the cli layer; this package gets a `commit`
//     bool and prints the plan when commit is false.
//   - Apply functions split into Plan + Execute so unit tests can verify
//     planning without hitting Stash.
//   - Idempotency: groups with applied_at != NULL are skipped at the source
//     (we list status="decided" only).
//   - Errors per group are captured but don't abort the whole batch — at
//     60k library scale, one bad group should not waste a half-hour run.
package apply

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// TagPlan is the precomputed work for the `scenes apply --action tag` flow.
type TagPlan struct {
	// Groups in scope (status=decided, not yet applied).
	Groups []*store.SceneGroup
	// All scene IDs that would receive the keeper tag, deduplicated.
	KeeperSceneIDs []string
	// All scene IDs that would receive the loser tag, deduplicated.
	LoserSceneIDs []string
	// Sum of loser file sizes — what would be reclaimable AFTER manual
	// review and deletion in the Stash UI. The tag action itself frees no
	// space; this is the upper-bound estimate.
	ReclaimableBytes int64
}

// PlanTag walks the store and builds a TagPlan from groups currently
// marked decided. It does not call Stash.
func PlanTag(ctx context.Context, st *store.Store) (*TagPlan, error) {
	groups, err := st.ListSceneGroups(ctx, []string{store.StatusDecided})
	if err != nil {
		return nil, fmt.Errorf("listing decided groups: %w", err)
	}
	plan := &TagPlan{Groups: groups}

	keeperSeen := map[string]bool{}
	loserSeen := map[string]bool{}
	for _, g := range groups {
		// Defensive: if a group somehow has applied_at set despite being
		// status=decided, skip it. This shouldn't happen but the cost of
		// the check is nil.
		if g.AppliedAt != nil {
			continue
		}
		for _, s := range g.Scenes {
			switch s.Role {
			case store.RoleKeeper:
				if !keeperSeen[s.SceneID] {
					keeperSeen[s.SceneID] = true
					plan.KeeperSceneIDs = append(plan.KeeperSceneIDs, s.SceneID)
				}
			case store.RoleLoser:
				if !loserSeen[s.SceneID] {
					loserSeen[s.SceneID] = true
					plan.LoserSceneIDs = append(plan.LoserSceneIDs, s.SceneID)
				}
				plan.ReclaimableBytes += s.FileSize
			}
		}
	}
	return plan, nil
}

// PrintTagPlan writes a human-readable preview of the plan to w. Used by
// both the dry-run path and the commit path's "about to do this" header.
func PrintTagPlan(w io.Writer, p *TagPlan, cfg *config.Config, commit bool) error {
	mode := "DRY RUN"
	if commit {
		mode = "COMMIT"
	}
	fmt.Fprintf(w, "=== stash-janitor scenes apply --action tag (%s) ===\n", mode)
	fmt.Fprintf(w, "Decided groups in scope:  %d\n", len(p.Groups))
	fmt.Fprintf(w, "Keeper scenes to tag:     %d  (tag: %q)\n", len(p.KeeperSceneIDs), cfg.Apply.Scenes.KeeperTag)
	fmt.Fprintf(w, "Loser scenes to tag:      %d  (tag: %q)\n", len(p.LoserSceneIDs), cfg.Apply.Scenes.LoserTag)
	fmt.Fprintf(w, "Reclaimable (if you delete losers in Stash UI afterward): %s\n",
		confirm.HumanBytes(p.ReclaimableBytes))
	if len(p.Groups) == 0 {
		fmt.Fprintln(w, "\nNothing to do. Try `stash-janitor scenes scan` first.")
	}
	if !commit {
		fmt.Fprintln(w, "\nThis was a dry run. Re-run with --commit to apply tags in Stash.")
	}
	return nil
}

// ExecuteTag mutates Stash according to the plan and marks the affected
// groups as applied in the local store. Should only be called when the
// caller is in --commit mode.
//
// On any GraphQL error, ExecuteTag returns the error without marking any
// groups applied — that way the user can re-run after fixing the issue and
// the apply is idempotent (tags are sets; the second attempt is a no-op
// for the scenes that already got tagged the first time, but the local DB
// will end up consistent).
func ExecuteTag(ctx context.Context, c *stash.Client, st *store.Store, cfg *config.Config, plan *TagPlan) error {
	if c == nil || st == nil || cfg == nil || plan == nil {
		return errors.New("ExecuteTag: nil dependency")
	}
	if len(plan.Groups) == 0 {
		return nil
	}

	// Find or create the two tags. Idempotent — first call creates, every
	// subsequent call returns the existing tag.
	loserTag, err := c.FindOrCreateTag(ctx, cfg.Apply.Scenes.LoserTag)
	if err != nil {
		return fmt.Errorf("ensuring loser tag %q: %w", cfg.Apply.Scenes.LoserTag, err)
	}
	keeperTag, err := c.FindOrCreateTag(ctx, cfg.Apply.Scenes.KeeperTag)
	if err != nil {
		return fmt.Errorf("ensuring keeper tag %q: %w", cfg.Apply.Scenes.KeeperTag, err)
	}

	// Apply the tags as bulk-update operations. ADD mode preserves any
	// existing tags on the affected scenes.
	if len(plan.LoserSceneIDs) > 0 {
		if err := c.BulkAddTag(ctx, plan.LoserSceneIDs, loserTag.ID); err != nil {
			return fmt.Errorf("tagging losers: %w", err)
		}
	}
	if len(plan.KeeperSceneIDs) > 0 {
		if err := c.BulkAddTag(ctx, plan.KeeperSceneIDs, keeperTag.ID); err != nil {
			return fmt.Errorf("tagging keepers: %w", err)
		}
	}

	// Mark every group applied. Errors here are surfaced but the Stash
	// side is already consistent so re-running tag is safe.
	for _, g := range plan.Groups {
		if err := st.MarkSceneGroupApplied(ctx, g.ID); err != nil {
			return fmt.Errorf("marking group %d applied: %w", g.ID, err)
		}
	}
	return nil
}
