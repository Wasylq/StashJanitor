package report

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wasylq/StashJanitor/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "stash-janitor.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestComputeScenesStatusEmpty(t *testing.T) {
	st := newTestStore(t)
	s, err := ComputeScenesStatus(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if s.TotalGroups != 0 {
		t.Errorf("TotalGroups = %d, want 0", s.TotalGroups)
	}
	if s.LastScanRunID != 0 {
		t.Errorf("expected zero last_scan_run_id when no run, got %d", s.LastScanRunID)
	}
}

func TestComputeScenesStatusReclaimable(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	dist, dur := 4, 1.0
	runID, _ := st.StartScanRun(ctx, store.WorkflowScenes, &dist, &dur)

	// Group 1: decided, 2 losers totalling 3 GiB.
	g1 := &store.SceneGroup{
		ScanRunID: runID,
		Signature: "1|2|3",
		Status:    store.StatusDecided,
		Scenes: []store.SceneGroupScene{
			{SceneID: "1", Role: store.RoleKeeper, FileSize: 5_000_000_000},
			{SceneID: "2", Role: store.RoleLoser, FileSize: 1_500_000_000},
			{SceneID: "3", Role: store.RoleLoser, FileSize: 1_500_000_000},
		},
	}
	if err := st.UpsertSceneGroup(ctx, g1); err != nil {
		t.Fatal(err)
	}

	// Group 2: needs_review, has loser file size but should NOT contribute.
	g2 := &store.SceneGroup{
		ScanRunID: runID,
		Signature: "10|11",
		Status:    store.StatusNeedsReview,
		Scenes: []store.SceneGroupScene{
			{SceneID: "10", Role: store.RoleUndecided, FileSize: 4_000_000_000},
			{SceneID: "11", Role: store.RoleUndecided, FileSize: 4_000_000_000},
		},
	}
	if err := st.UpsertSceneGroup(ctx, g2); err != nil {
		t.Fatal(err)
	}

	// Group 3: applied — also should not contribute (already reclaimed).
	g3 := &store.SceneGroup{
		ScanRunID: runID,
		Signature: "100|101",
		Status:    store.StatusApplied,
		Scenes: []store.SceneGroupScene{
			{SceneID: "100", Role: store.RoleKeeper, FileSize: 1_000_000_000},
			{SceneID: "101", Role: store.RoleLoser, FileSize: 1_000_000_000},
		},
	}
	if err := st.UpsertSceneGroup(ctx, g3); err != nil {
		t.Fatal(err)
	}

	s, err := ComputeScenesStatus(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if s.TotalGroups != 3 {
		t.Errorf("TotalGroups = %d, want 3", s.TotalGroups)
	}
	if s.Decided != 1 {
		t.Errorf("Decided = %d, want 1", s.Decided)
	}
	if s.NeedsReview != 1 {
		t.Errorf("NeedsReview = %d, want 1", s.NeedsReview)
	}
	if s.Applied != 1 {
		t.Errorf("Applied = %d, want 1", s.Applied)
	}
	want := int64(3_000_000_000) // group 1 losers only
	if s.ReclaimableBytes != want {
		t.Errorf("ReclaimableBytes = %d, want %d", s.ReclaimableBytes, want)
	}
}

func TestPrintScenesStatusEmpty(t *testing.T) {
	buf := &bytes.Buffer{}
	if err := PrintScenesStatus(buf, &ScenesStatus{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No scenes scan has been run") {
		t.Errorf("expected friendly empty message, got: %s", buf.String())
	}
}

func TestPrintScenesReport(t *testing.T) {
	groups := []*store.SceneGroup{
		{
			ID:        1,
			Signature: "17|42",
			Status:    store.StatusDecided,
			Scenes: []store.SceneGroupScene{
				{SceneID: "42", Role: store.RoleKeeper, Width: 1920, Height: 1080, Codec: "hevc", FileSize: 2_000_000_000, HasStashID: true, Organized: true, TagCount: 5, PrimaryPath: "/sorted/keeper.mp4"},
				{SceneID: "17", Role: store.RoleLoser, Width: 1280, Height: 720, Codec: "h264", FileSize: 800_000_000, PrimaryPath: "/inbox/loser.mp4"},
			},
		},
	}
	buf := &bytes.Buffer{}
	if err := PrintScenesReport(buf, groups, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"#1", "decided", "KEEP", "drop", "stashID", "organized", "tags=5", "reclaimable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
	// With nil explain, no "kept by" annotation should appear.
	if strings.Contains(out, "kept by") {
		t.Errorf("expected no 'kept by' lines when explain is nil, got:\n%s", out)
	}
}

func TestPrintScenesReportWithExplain(t *testing.T) {
	groups := []*store.SceneGroup{
		{
			ID:        1,
			Signature: "17|42",
			Status:    store.StatusDecided,
			Scenes: []store.SceneGroupScene{
				{SceneID: "42", Role: store.RoleKeeper, Width: 1920, Height: 1080, FileSize: 4_000_000_000, PrimaryPath: "/sorted/keep.mp4"},
				{SceneID: "17", Role: store.RoleLoser, Width: 1920, Height: 1080, FileSize: 1_000_000_000, PrimaryPath: "/inbox/loser.mp4"},
			},
		},
	}
	explain := func(winner, loser *store.SceneGroupScene) string {
		return "larger file (test stub)"
	}
	buf := &bytes.Buffer{}
	if err := PrintScenesReport(buf, groups, explain); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "kept by: larger file (test stub)") {
		t.Errorf("expected explain annotation under loser, got:\n%s", out)
	}
}

func TestFilterStatusesValidation(t *testing.T) {
	if _, err := ScenesReportFilter("nope").FilterStatuses(); err == nil {
		t.Error("expected error for unknown filter")
	}
	if got, _ := ScenesReportFilter("all").FilterStatuses(); got != nil {
		t.Errorf("'all' should map to nil, got %v", got)
	}
}

