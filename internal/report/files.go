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

// FilesStatus is the workflow B equivalent of ScenesStatus.
type FilesStatus struct {
	LastScanRunID    int64      `json:"last_scan_run_id"`
	LastScanStarted  *time.Time `json:"last_scan_started,omitempty"`
	LastScanFinished *time.Time `json:"last_scan_finished,omitempty"`

	TotalGroups int `json:"total_groups"`
	Pending     int `json:"pending"`
	Decided     int `json:"decided"`
	NeedsReview int `json:"needs_review"`
	Applied     int `json:"applied"`
	Dismissed   int `json:"dismissed"`

	// ReclaimableBytes is the sum of loser file sizes across decided
	// groups — what would be freed if you ran `stash-janitor files apply --commit`
	// (Phase 1.5). Files in a multi-file scene are byte-equivalent, so
	// this number is reliable.
	ReclaimableBytes int64 `json:"reclaimable_bytes"`
}

// ComputeFilesStatus walks the file_groups table and produces a summary.
func ComputeFilesStatus(ctx context.Context, st *store.Store) (*FilesStatus, error) {
	out := &FilesStatus{}

	if run, err := st.LatestScanRun(ctx, store.WorkflowFiles); err == nil {
		out.LastScanRunID = run.ID
		t := run.StartedAt
		out.LastScanStarted = &t
		out.LastScanFinished = run.FinishedAt
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	groups, err := st.ListFileGroups(ctx, nil)
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
			out.ReclaimableBytes += sumLoserFileBytes(g)
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

func sumLoserFileBytes(g *store.FileGroup) int64 {
	var total int64
	for _, f := range g.Files {
		if f.Role == store.RoleLoser {
			total += f.FileSize
		}
	}
	return total
}

// PrintFilesStatus writes a human-readable summary.
func PrintFilesStatus(w io.Writer, s *FilesStatus) error {
	var b strings.Builder

	if s.LastScanRunID == 0 {
		_, err := io.WriteString(w, "No files scan has been run yet. Try `stash-janitor files scan`.\n")
		return err
	}

	b.WriteString("=== stash-janitor files status ===\n")
	fmt.Fprintf(&b, "Last scan run:    #%d\n", s.LastScanRunID)
	if s.LastScanStarted != nil {
		fmt.Fprintf(&b, "  Started:        %s\n", s.LastScanStarted.Local().Format(time.RFC3339))
	}
	if s.LastScanFinished != nil {
		fmt.Fprintf(&b, "  Finished:       %s\n", s.LastScanFinished.Local().Format(time.RFC3339))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Multi-file scenes: %d\n", s.TotalGroups)
	fmt.Fprintf(&b, "  decided:         %d  (ready to apply, Phase 1.5)\n", s.Decided)
	fmt.Fprintf(&b, "  needs_review:    %d  (manual review required)\n", s.NeedsReview)
	fmt.Fprintf(&b, "  pending:         %d\n", s.Pending)
	fmt.Fprintf(&b, "  applied:         %d\n", s.Applied)
	fmt.Fprintf(&b, "  dismissed:       %d\n", s.Dismissed)
	b.WriteString("\n")
	fmt.Fprintf(&b, "Reclaimable:       %s  (%d bytes across decided multi-file scenes)\n",
		confirm.HumanBytes(s.ReclaimableBytes), s.ReclaimableBytes)
	b.WriteString("\n")
	b.WriteString("Note: workflow B is report-only in Phase 1. --commit support lands in Phase 1.5.\n")

	_, err := io.WriteString(w, b.String())
	return err
}

// PrintFilesStatusJSON marshals the files status as indented JSON.
func PrintFilesStatusJSON(w io.Writer, s *FilesStatus) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// ListFilesReport returns the file groups matching the filter.
//
// We reuse ScenesReportFilter because the status values are the same — no
// point in defining a parallel type.
func ListFilesReport(ctx context.Context, st *store.Store, filter ScenesReportFilter) ([]*store.FileGroup, error) {
	statuses, err := filter.FilterStatuses()
	if err != nil {
		return nil, err
	}
	return st.ListFileGroups(ctx, statuses)
}

// PrintFilesReport renders the per-scene report for workflow B.
func PrintFilesReport(w io.Writer, groups []*store.FileGroup) error {
	if len(groups) == 0 {
		_, err := io.WriteString(w, "(no multi-file scenes match the filter)\n")
		return err
	}
	var b strings.Builder
	for i, g := range groups {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "scene %s  status=%s", g.SceneID, g.Status)
		if g.DecisionReason != "" {
			fmt.Fprintf(&b, "  (%s)", g.DecisionReason)
		}
		b.WriteString("\n")
		if g.AppliedAt != nil {
			fmt.Fprintf(&b, "    applied:   %s\n", g.AppliedAt.Local().Format(time.RFC3339))
		}

		var loserBytes int64
		for _, f := range g.Files {
			marker := "       "
			switch f.Role {
			case store.RoleKeeper:
				marker = "  KEEP "
			case store.RoleLoser:
				marker = "  drop "
				loserBytes += f.FileSize
			}
			primaryFlag := "     "
			if f.IsPrimary {
				primaryFlag = "[pri]"
			}
			fnFlag := ""
			if f.FilenameQuality == 1 {
				fnFlag = " good-filename"
			}
			fmt.Fprintf(&b, "    %s %s file %-8s  %s%s\n",
				marker, primaryFlag, f.FileID,
				abbreviatePath(f.Path, 70),
				fnFlag,
			)
		}
		if loserBytes > 0 {
			fmt.Fprintf(&b, "    reclaimable: %s\n", confirm.HumanBytes(loserBytes))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// PrintFilesReportJSON marshals the file groups as indented JSON.
func PrintFilesReportJSON(w io.Writer, groups []*store.FileGroup) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(groups)
}
