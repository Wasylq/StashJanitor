package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Wasylq/StashJanitor/internal/store"
)

// OrphansStatus is the workflow C summary of orphan-lookup state.
type OrphansStatus struct {
	LastScanRunID    int64      `json:"last_scan_run_id"`
	LastScanStarted  *time.Time `json:"last_scan_started,omitempty"`
	LastScanFinished *time.Time `json:"last_scan_finished,omitempty"`

	TotalLookups int `json:"total_lookups"`
	Matched      int `json:"matched"`
	NoMatch      int `json:"no_match"`
	Applied      int `json:"applied"`
	Dismissed    int `json:"dismissed"`
}

// ComputeOrphansStatus walks the store and produces an OrphansStatus.
func ComputeOrphansStatus(ctx context.Context, st *store.Store) (*OrphansStatus, error) {
	out := &OrphansStatus{}

	if run, err := st.LatestScanRun(ctx, store.WorkflowOrphans); err == nil {
		out.LastScanRunID = run.ID
		t := run.StartedAt
		out.LastScanStarted = &t
		out.LastScanFinished = run.FinishedAt
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	rows, err := st.ListOrphanLookups(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.TotalLookups++
		switch r.Status {
		case store.StatusMatched:
			out.Matched++
		case store.StatusNoMatch:
			out.NoMatch++
		case store.StatusApplied:
			out.Applied++
		case store.StatusDismissed:
			out.Dismissed++
		}
	}
	return out, nil
}

// PrintOrphansStatus writes the human-readable summary.
func PrintOrphansStatus(w io.Writer, s *OrphansStatus) error {
	var b strings.Builder
	if s.LastScanRunID == 0 {
		_, err := io.WriteString(w, "No orphans scan has been run yet. Try `stash-janitor orphans scan`.\n")
		return err
	}
	b.WriteString("=== stash-janitor orphans status ===\n")
	fmt.Fprintf(&b, "Last scan run:    #%d\n", s.LastScanRunID)
	if s.LastScanStarted != nil {
		fmt.Fprintf(&b, "  Started:        %s\n", s.LastScanStarted.Local().Format(time.RFC3339))
	}
	if s.LastScanFinished != nil {
		fmt.Fprintf(&b, "  Finished:       %s\n", s.LastScanFinished.Local().Format(time.RFC3339))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Total orphan lookups:  %d\n", s.TotalLookups)
	fmt.Fprintf(&b, "  matched:             %d  (ready to apply)\n", s.Matched)
	fmt.Fprintf(&b, "  no_match:            %d  (stash-box has nothing)\n", s.NoMatch)
	fmt.Fprintf(&b, "  applied:             %d\n", s.Applied)
	fmt.Fprintf(&b, "  dismissed:           %d\n", s.Dismissed)
	_, err := io.WriteString(w, b.String())
	return err
}

// PrintOrphansStatusJSON marshals the status as indented JSON.
func PrintOrphansStatusJSON(w io.Writer, s *OrphansStatus) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// OrphansReportFilter selects which orphan_lookups statuses to render.
type OrphansReportFilter string

const (
	OrphansFilterAll       OrphansReportFilter = "all"
	OrphansFilterMatched   OrphansReportFilter = "matched"
	OrphansFilterNoMatch   OrphansReportFilter = "no-match"
	OrphansFilterApplied   OrphansReportFilter = "applied"
	OrphansFilterDismissed OrphansReportFilter = "dismissed"
)

// FilterStatuses translates the filter string to store statuses.
func (f OrphansReportFilter) FilterStatuses() ([]string, error) {
	switch f {
	case OrphansFilterAll, "":
		return nil, nil
	case OrphansFilterMatched:
		return []string{store.StatusMatched}, nil
	case OrphansFilterNoMatch:
		return []string{store.StatusNoMatch}, nil
	case OrphansFilterApplied:
		return []string{store.StatusApplied}, nil
	case OrphansFilterDismissed:
		return []string{store.StatusDismissed}, nil
	default:
		return nil, fmt.Errorf("unknown filter %q (try: all|matched|no-match|applied|dismissed)", string(f))
	}
}

// ListOrphansReport returns the orphan lookups matching the filter.
func ListOrphansReport(ctx context.Context, st *store.Store, filter OrphansReportFilter) ([]*store.OrphanLookup, error) {
	statuses, err := filter.FilterStatuses()
	if err != nil {
		return nil, err
	}
	return st.ListOrphanLookups(ctx, statuses)
}

// PrintOrphansReport renders the per-orphan list as plain text.
func PrintOrphansReport(w io.Writer, rows []*store.OrphanLookup) error {
	if len(rows) == 0 {
		_, err := io.WriteString(w, "(no orphan lookups match the filter)\n")
		return err
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "scene %s  status=%s  endpoint=%s\n", r.SceneID, r.Status, r.Endpoint)
		fmt.Fprintf(&b, "    file: %s\n", r.PrimaryPath)
		if r.Status == store.StatusMatched || r.Status == store.StatusApplied {
			fmt.Fprintf(&b, "    ↳ match: %s\n", r.MatchTitle)
			if r.MatchStudio != "" {
				fmt.Fprintf(&b, "      studio: %s\n", r.MatchStudio)
			}
			if r.MatchPerformers != "" {
				fmt.Fprintf(&b, "      performers: %s\n", r.MatchPerformers)
			}
			if r.MatchDate != "" {
				fmt.Fprintf(&b, "      date: %s\n", r.MatchDate)
			}
			fmt.Fprintf(&b, "      remote_id: %s\n", r.MatchRemoteID)
			if r.MatchCount > 1 {
				fmt.Fprintf(&b, "      (stash-box returned %d candidates; first one shown)\n", r.MatchCount)
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// PrintOrphansReportJSON marshals the orphan lookups as indented JSON.
func PrintOrphansReportJSON(w io.Writer, rows []*store.OrphanLookup) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}
