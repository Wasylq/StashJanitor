// Package report renders scan results from the local SQLite cache as
// human-readable text or machine-readable JSON. Renders only — no Stash
// calls — so reports work even when Stash is offline.
package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// ScenesStatus is the summary of workflow A's local cache.
type ScenesStatus struct {
	LastScanRunID    int64      `json:"last_scan_run_id"`
	LastScanStarted  *time.Time `json:"last_scan_started,omitempty"`
	LastScanFinished *time.Time `json:"last_scan_finished,omitempty"`
	LastScanDistance *int       `json:"last_scan_distance,omitempty"`

	TotalGroups int `json:"total_groups"`
	Pending     int `json:"pending"`
	Decided     int `json:"decided"`
	NeedsReview int `json:"needs_review"`
	Applied     int `json:"applied"`
	Dismissed   int `json:"dismissed"`

	// ReclaimableBytes is the sum of loser file sizes across groups whose
	// status is "decided" — i.e. what would be freed if you ran the
	// equivalent --action delete or --action merge --commit right now.
	ReclaimableBytes int64 `json:"reclaimable_bytes"`
}

// ComputeScenesStatus walks the store and produces the ScenesStatus.
func ComputeScenesStatus(ctx context.Context, st *store.Store) (*ScenesStatus, error) {
	out := &ScenesStatus{}

	// Last scan run for context.
	if run, err := st.LatestScanRun(ctx, store.WorkflowScenes); err == nil {
		out.LastScanRunID = run.ID
		t := run.StartedAt
		out.LastScanStarted = &t
		out.LastScanFinished = run.FinishedAt
		out.LastScanDistance = run.Distance
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	groups, err := st.ListSceneGroups(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		out.TotalGroups++
		switch g.Status {
		case store.StatusPending:
			out.Pending++
		case store.StatusDecided:
			out.Decided++
			out.ReclaimableBytes += sumLoserBytes(g)
		case store.StatusNeedsReview:
			out.NeedsReview++
		case store.StatusApplied:
			out.Applied++
		case store.StatusDismissed:
			out.Dismissed++
		}
	}
	return out, nil
}

// sumLoserBytes sums the file sizes of every scene marked as a loser in g.
func sumLoserBytes(g *store.SceneGroup) int64 {
	var total int64
	for _, s := range g.Scenes {
		if s.Role == store.RoleLoser {
			total += s.FileSize
		}
	}
	return total
}

// PrintScenesStatus writes the human-readable summary to w.
func PrintScenesStatus(w io.Writer, s *ScenesStatus) error {
	var b strings.Builder

	if s.LastScanRunID == 0 {
		b.WriteString("No scenes scan has been run yet. Try `stash-janitor scenes scan`.\n")
		_, err := io.WriteString(w, b.String())
		return err
	}

	b.WriteString("=== stash-janitor scenes status ===\n")
	fmt.Fprintf(&b, "Last scan run:    #%d\n", s.LastScanRunID)
	if s.LastScanStarted != nil {
		fmt.Fprintf(&b, "  Started:        %s\n", s.LastScanStarted.Local().Format(time.RFC3339))
	}
	if s.LastScanFinished != nil {
		fmt.Fprintf(&b, "  Finished:       %s\n", s.LastScanFinished.Local().Format(time.RFC3339))
	}
	if s.LastScanDistance != nil {
		fmt.Fprintf(&b, "  phash distance: %d\n", *s.LastScanDistance)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Total groups:     %d\n", s.TotalGroups)
	fmt.Fprintf(&b, "  decided:        %d  (ready to apply)\n", s.Decided)
	fmt.Fprintf(&b, "  needs_review:   %d  (manual review required)\n", s.NeedsReview)
	fmt.Fprintf(&b, "  pending:        %d\n", s.Pending)
	fmt.Fprintf(&b, "  applied:        %d\n", s.Applied)
	fmt.Fprintf(&b, "  dismissed:      %d\n", s.Dismissed)
	b.WriteString("\n")
	fmt.Fprintf(&b, "Reclaimable:      %s  (%d bytes across decided groups' losers)\n",
		confirm.HumanBytes(s.ReclaimableBytes), s.ReclaimableBytes)

	_, err := io.WriteString(w, b.String())
	return err
}

// PrintScenesStatusJSON marshals the status as indented JSON.
func PrintScenesStatusJSON(w io.Writer, s *ScenesStatus) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// ----- per-group report -----

// ScenesReportFilter is the set of statuses report should include.
type ScenesReportFilter string

const (
	FilterAll         ScenesReportFilter = "all"
	FilterDecided     ScenesReportFilter = "decided"
	FilterNeedsReview ScenesReportFilter = "needs-review"
	FilterApplied     ScenesReportFilter = "applied"
	FilterDismissed   ScenesReportFilter = "dismissed"
)

// FilterStatuses translates the user-facing filter into the store status
// values to query for. Returns nil for "all".
func (f ScenesReportFilter) FilterStatuses() ([]string, error) {
	switch f {
	case FilterAll, "":
		return nil, nil
	case FilterDecided:
		return []string{store.StatusDecided}, nil
	case FilterNeedsReview:
		return []string{store.StatusNeedsReview}, nil
	case FilterApplied:
		return []string{store.StatusApplied}, nil
	case FilterDismissed:
		return []string{store.StatusDismissed}, nil
	default:
		return nil, fmt.Errorf("unknown filter %q (try: all|decided|needs-review|applied|dismissed)", string(f))
	}
}

// ListScenesReport returns the groups matching the filter, with their
// member scenes already loaded.
func ListScenesReport(ctx context.Context, st *store.Store, filter ScenesReportFilter) ([]*store.SceneGroup, error) {
	statuses, err := filter.FilterStatuses()
	if err != nil {
		return nil, err
	}
	return st.ListSceneGroups(ctx, statuses)
}

// ExplainSceneFn is the callback PrintScenesReport uses to annotate each
// loser line with the first rule that the keeper beat it on. Pass nil to
// disable per-loser explanations entirely (e.g. in tests).
type ExplainSceneFn func(winner, loser *store.SceneGroupScene) string

// PrintScenesReport renders the groups as plain text, one block per group.
// If explain is non-nil, each loser line is followed by an indented
// "↳ kept by: <reason>" line showing why the keeper outranked it.
func PrintScenesReport(w io.Writer, groups []*store.SceneGroup, explain ExplainSceneFn) error {
	if len(groups) == 0 {
		_, err := io.WriteString(w, "(no groups match the filter)\n")
		return err
	}
	var b strings.Builder
	for i, g := range groups {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "#%d  status=%s", g.ID, g.Status)
		if g.DecisionReason != "" {
			fmt.Fprintf(&b, "  (%s)", g.DecisionReason)
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "    signature: %s\n", g.Signature)
		if g.AppliedAt != nil {
			fmt.Fprintf(&b, "    applied:   %s\n", g.AppliedAt.Local().Format(time.RFC3339))
		}

		// Find the keeper once per group so we can annotate each loser
		// against it without an inner search.
		keeperIdx := -1
		for j := range g.Scenes {
			if g.Scenes[j].Role == store.RoleKeeper {
				keeperIdx = j
				break
			}
		}

		var loserBytes int64
		for j := range g.Scenes {
			s := &g.Scenes[j]
			marker := "       "
			switch s.Role {
			case store.RoleKeeper:
				marker = "  KEEP "
			case store.RoleLoser:
				marker = "  drop "
				loserBytes += s.FileSize
			}
			fmt.Fprintf(&b, "    %s scene %-8s  %dx%-4d  %-6s  %s  %s%s\n",
				marker, s.SceneID,
				s.Width, s.Height,
				codecOrDash(s.Codec),
				confirm.HumanBytes(s.FileSize),
				flagSummary(*s),
				s.PrimaryPath,
			)

			// Annotate losers with the rule that decided they lost.
			if s.Role == store.RoleLoser && explain != nil && keeperIdx >= 0 {
				if reason := explain(&g.Scenes[keeperIdx], s); reason != "" {
					fmt.Fprintf(&b, "                ↳ kept by: %s\n", reason)
				}
			}
		}
		if loserBytes > 0 {
			fmt.Fprintf(&b, "    reclaimable: %s\n", confirm.HumanBytes(loserBytes))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// PrintScenesReportJSON marshals the groups as indented JSON.
func PrintScenesReportJSON(w io.Writer, groups []*store.SceneGroup) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(groups)
}

// ----- formatting helpers -----

func flagSummary(s store.SceneGroupScene) string {
	var parts []string
	if s.HasStashID {
		parts = append(parts, "stashID")
	}
	if s.Organized {
		parts = append(parts, "organized")
	}
	if s.TagCount > 0 {
		parts = append(parts, fmt.Sprintf("tags=%d", s.TagCount))
	}
	if s.PerformerCount > 0 {
		parts = append(parts, fmt.Sprintf("perf=%d", s.PerformerCount))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, ",") + "] "
}

func codecOrDash(c string) string {
	if c == "" {
		return "-"
	}
	return c
}
