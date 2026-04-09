// Package merge computes the metadata union stash-janitor passes to Stash's
// sceneMerge mutation.
//
// Stash's sceneMerge does NOT auto-union scene-level metadata in v0.31.0
// (verified by reading pkg/scene/merge.go). It moves files from sources
// to destination, merges scene markers, and merges play/o history if
// requested — but tags, performers, studio, stash_ids, title, urls, etc.
// are *only* set on the destination via the `values` field, which the
// caller must compute.
//
// This package is that caller. Given a keeper scene and a set of loser
// scenes plus a configurable per-field policy, it builds a SceneUpdateVals
// that, when passed to sceneMerge, brings the loser metadata into the
// keeper without losing anything.
package merge

import (
	"fmt"
	"strings"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/stash"
)

// FieldDiff is a single field that the union changed on the keeper. Used
// in the dry-run preview to show the user what metadata is being added.
type FieldDiff struct {
	Field   string // "tags", "performers", "title", etc.
	Action  string // "added", "set", "no-change"
	Details string // human-readable summary
}

// Result is the output of BuildUnion.
type Result struct {
	// Vals is the SceneUpdateVals that should be passed as `values` to
	// sceneMerge. Will be nil if no fields need updating.
	Vals *stash.SceneUpdateVals

	// Diffs lists every field the union *changed* on the keeper. Empty
	// when the keeper already had everything the losers offered.
	Diffs []FieldDiff
}

// BuildUnion computes the metadata union for one merge group per the
// policy in cfg.Merge.SceneLevel.
//
// The function never mutates its inputs. It is a pure transformation
// (keeper, losers, policy) → Result, so it's trivially testable.
func BuildUnion(keeper *stash.Scene, losers []*stash.Scene, cfg *config.Config) (*Result, error) {
	if keeper == nil {
		return nil, fmt.Errorf("BuildUnion: keeper is nil")
	}
	policy := cfg.Merge.SceneLevel
	if policy == nil {
		policy = map[string]string{}
	}

	vals := &stash.SceneUpdateVals{}
	var diffs []FieldDiff

	// ----- multi-value fields -----

	// Tags: union of IDs.
	if mode := policy["tags"]; mode == "union" || mode == "" {
		newTagIDs := unionIDs(
			collectIDs(keeper.Tags, func(t stash.Tag) string { return t.ID }),
			multiCollectIDs(losers, func(s *stash.Scene) []string {
				return collectIDs(s.Tags, func(t stash.Tag) string { return t.ID })
			}),
		)
		if added := diffStrings(collectIDs(keeper.Tags, func(t stash.Tag) string { return t.ID }), newTagIDs); len(added) > 0 {
			vals.TagIDs = newTagIDs
			diffs = append(diffs, FieldDiff{
				Field: "tags", Action: "added",
				Details: fmt.Sprintf("+%d (now %d total)", len(added), len(newTagIDs)),
			})
		}
	}

	// Performers: union of IDs.
	if mode := policy["performers"]; mode == "union" || mode == "" {
		newIDs := unionIDs(
			collectIDs(keeper.Performers, func(p stash.Performer) string { return p.ID }),
			multiCollectIDs(losers, func(s *stash.Scene) []string {
				return collectIDs(s.Performers, func(p stash.Performer) string { return p.ID })
			}),
		)
		if added := diffStrings(collectIDs(keeper.Performers, func(p stash.Performer) string { return p.ID }), newIDs); len(added) > 0 {
			vals.PerformerIDs = newIDs
			diffs = append(diffs, FieldDiff{
				Field: "performers", Action: "added",
				Details: fmt.Sprintf("+%d (now %d total)", len(added), len(newIDs)),
			})
		}
	}

	// URLs: union of strings (no IDs).
	if mode := policy["urls"]; mode == "union" || mode == "" {
		newURLs := unionStrings(keeper.URLs, multiCollectStrings(losers, func(s *stash.Scene) []string { return s.URLs }))
		if added := diffStrings(keeper.URLs, newURLs); len(added) > 0 {
			vals.URLs = newURLs
			diffs = append(diffs, FieldDiff{
				Field: "urls", Action: "added",
				Details: fmt.Sprintf("+%d (now %d total)", len(added), len(newURLs)),
			})
		}
	}

	// stash_ids: union of (endpoint, stash_id) pairs.
	if mode := policy["stash_ids"]; mode == "union" || mode == "" {
		// Build a key set first to deduplicate.
		seen := map[string]bool{}
		for _, sid := range keeper.StashIDs {
			seen[sid.Endpoint+"|"+sid.StashID] = true
		}
		var newStashIDs []stash.StashIDInput
		for _, sid := range keeper.StashIDs {
			newStashIDs = append(newStashIDs, stash.StashIDInput{Endpoint: sid.Endpoint, StashID: sid.StashID})
		}
		var added []string
		for _, l := range losers {
			for _, sid := range l.StashIDs {
				key := sid.Endpoint + "|" + sid.StashID
				if seen[key] {
					continue
				}
				seen[key] = true
				newStashIDs = append(newStashIDs, stash.StashIDInput{Endpoint: sid.Endpoint, StashID: sid.StashID})
				added = append(added, key)
			}
		}
		if len(added) > 0 {
			vals.StashIDs = newStashIDs
			diffs = append(diffs, FieldDiff{
				Field: "stash_ids", Action: "added",
				Details: fmt.Sprintf("+%d (now %d total)", len(added), len(newStashIDs)),
			})
		}
	}

	// Galleries: union of IDs.
	if mode := policy["galleries"]; mode == "union" || mode == "" {
		newIDs := unionIDs(
			collectIDs(keeper.Galleries, func(g stash.Gallery) string { return g.ID }),
			multiCollectIDs(losers, func(s *stash.Scene) []string {
				return collectIDs(s.Galleries, func(g stash.Gallery) string { return g.ID })
			}),
		)
		if added := diffStrings(collectIDs(keeper.Galleries, func(g stash.Gallery) string { return g.ID }), newIDs); len(added) > 0 {
			vals.GalleryIDs = newIDs
			diffs = append(diffs, FieldDiff{
				Field: "gallery_ids", Action: "added",
				Details: fmt.Sprintf("+%d (now %d total)", len(added), len(newIDs)),
			})
		}
	}

	// ----- scalar fields -----

	if mode := policy["title"]; mode == "" || mode == "prefer_keeper_then_loser" {
		if v := preferKeeperThenLoser(keeper.Title, losers, func(s *stash.Scene) string { return s.Title }); v != keeper.Title {
			s := v
			vals.Title = &s
			diffs = append(diffs, FieldDiff{Field: "title", Action: "set", Details: truncate(v, 60)})
		}
	}
	if mode := policy["details"]; mode == "" || mode == "prefer_keeper_then_loser" {
		if v := preferKeeperThenLoser(keeper.Details, losers, func(s *stash.Scene) string { return s.Details }); v != keeper.Details {
			s := v
			vals.Details = &s
			diffs = append(diffs, FieldDiff{Field: "details", Action: "set", Details: truncate(v, 60)})
		}
	}
	if mode := policy["director"]; mode == "" || mode == "prefer_keeper_then_loser" {
		if v := preferKeeperThenLoser(keeper.Director, losers, func(s *stash.Scene) string { return s.Director }); v != keeper.Director {
			s := v
			vals.Director = &s
			diffs = append(diffs, FieldDiff{Field: "director", Action: "set", Details: v})
		}
	}
	if mode := policy["code"]; mode == "" || mode == "prefer_keeper_then_loser" {
		if v := preferKeeperThenLoser(keeper.Code, losers, func(s *stash.Scene) string { return s.Code }); v != keeper.Code {
			s := v
			vals.Code = &s
			diffs = append(diffs, FieldDiff{Field: "code", Action: "set", Details: v})
		}
	}
	if mode := policy["studio_id"]; mode == "" || mode == "prefer_keeper_then_loser" {
		var keeperStudioID string
		if keeper.Studio != nil {
			keeperStudioID = keeper.Studio.ID
		}
		if v := preferKeeperThenLoser(keeperStudioID, losers, func(s *stash.Scene) string {
			if s.Studio != nil {
				return s.Studio.ID
			}
			return ""
		}); v != keeperStudioID && v != "" {
			s := v
			vals.StudioID = &s
			diffs = append(diffs, FieldDiff{Field: "studio_id", Action: "set", Details: v})
		}
	}
	if mode := policy["date"]; mode == "" || mode == "prefer_keeper_then_loser" {
		if v := preferKeeperThenLoser(keeper.Date, losers, func(s *stash.Scene) string { return s.Date }); v != keeper.Date {
			s := v
			vals.Date = &s
			diffs = append(diffs, FieldDiff{Field: "date", Action: "set", Details: v})
		}
	}

	if mode := policy["rating100"]; mode == "" || mode == "max" {
		var keeperR int
		if keeper.Rating100 != nil {
			keeperR = *keeper.Rating100
		}
		max := keeperR
		for _, l := range losers {
			if l.Rating100 != nil && *l.Rating100 > max {
				max = *l.Rating100
			}
		}
		if max > keeperR {
			r := max
			vals.Rating100 = &r
			diffs = append(diffs, FieldDiff{Field: "rating100", Action: "set", Details: fmt.Sprintf("%d", max)})
		}
	}

	if mode := policy["organized"]; mode == "" || mode == "any_true" {
		if !keeper.Organized {
			for _, l := range losers {
				if l.Organized {
					t := true
					vals.Organized = &t
					diffs = append(diffs, FieldDiff{Field: "organized", Action: "set", Details: "true"})
					break
				}
			}
		}
	}

	// If no field needs changing, return nil Vals so the caller can pass
	// a bare sceneMerge call without `values`.
	if len(diffs) == 0 {
		return &Result{Vals: nil, Diffs: nil}, nil
	}
	return &Result{Vals: vals, Diffs: diffs}, nil
}

// ----- helpers -----

// collectIDs walks a slice and extracts a string ID per element.
func collectIDs[T any](items []T, idOf func(T) string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, idOf(it))
	}
	return out
}

// multiCollectIDs walks losers and concatenates collectIDs results.
func multiCollectIDs(losers []*stash.Scene, idsOf func(*stash.Scene) []string) [][]string {
	out := make([][]string, 0, len(losers))
	for _, l := range losers {
		out = append(out, idsOf(l))
	}
	return out
}

// multiCollectStrings is the equivalent for plain string slices like URLs.
func multiCollectStrings(losers []*stash.Scene, of func(*stash.Scene) []string) [][]string {
	out := make([][]string, 0, len(losers))
	for _, l := range losers {
		out = append(out, of(l))
	}
	return out
}

// unionIDs returns the union of one base slice and N additional slices,
// preserving the base order and deduplicating with a set.
func unionIDs(base []string, more [][]string) []string {
	seen := make(map[string]bool, len(base))
	out := make([]string, 0, len(base))
	for _, id := range base {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, slice := range more {
		for _, id := range slice {
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// unionStrings is the same as unionIDs but for arbitrary strings (URLs etc).
func unionStrings(base []string, more [][]string) []string {
	return unionIDs(base, more)
}

// diffStrings returns elements in `after` that are not in `before`.
func diffStrings(before, after []string) []string {
	beforeSet := make(map[string]bool, len(before))
	for _, b := range before {
		beforeSet[b] = true
	}
	var added []string
	for _, a := range after {
		if !beforeSet[a] {
			added = append(added, a)
		}
	}
	return added
}

// preferKeeperThenLoser returns the keeper value if non-empty, otherwise
// the first non-empty loser value (in input order). Returns the keeper
// value (possibly empty) if every value is empty.
func preferKeeperThenLoser(keeper string, losers []*stash.Scene, of func(*stash.Scene) string) string {
	if keeper != "" {
		return keeper
	}
	for _, l := range losers {
		if v := of(l); v != "" {
			return v
		}
	}
	return keeper
}

// truncate shortens s for display, with an ellipsis if cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n-3]) + "..."
}
