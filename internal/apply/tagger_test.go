package apply

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wasylq/StashJanitor/internal/config"
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

func loadDefaults(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("/missing")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPlanTagCollectsScenesAcrossGroups(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	runID, _ := st.StartScanRun(ctx, store.WorkflowScenes, nil, nil)

	groups := []*store.SceneGroup{
		{
			ScanRunID: runID,
			Signature: "1|2",
			Status:    store.StatusDecided,
			Scenes: []store.SceneGroupScene{
				{SceneID: "1", Role: store.RoleKeeper, FileSize: 5_000_000_000},
				{SceneID: "2", Role: store.RoleLoser, FileSize: 1_000_000_000},
			},
		},
		{
			ScanRunID: runID,
			Signature: "3|4|5",
			Status:    store.StatusDecided,
			Scenes: []store.SceneGroupScene{
				{SceneID: "3", Role: store.RoleKeeper, FileSize: 4_000_000_000},
				{SceneID: "4", Role: store.RoleLoser, FileSize: 2_000_000_000},
				{SceneID: "5", Role: store.RoleLoser, FileSize: 1_500_000_000},
			},
		},
		// needs_review group should NOT be in the plan.
		{
			ScanRunID: runID,
			Signature: "100|101",
			Status:    store.StatusNeedsReview,
			Scenes: []store.SceneGroupScene{
				{SceneID: "100", Role: store.RoleUndecided, FileSize: 999},
				{SceneID: "101", Role: store.RoleUndecided, FileSize: 999},
			},
		},
	}
	for _, g := range groups {
		if err := st.UpsertSceneGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := PlanTag(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 2 {
		t.Errorf("len(plan.Groups) = %d, want 2 (needs_review excluded)", len(plan.Groups))
	}
	if len(plan.KeeperSceneIDs) != 2 {
		t.Errorf("len(KeeperSceneIDs) = %d, want 2", len(plan.KeeperSceneIDs))
	}
	if len(plan.LoserSceneIDs) != 3 {
		t.Errorf("len(LoserSceneIDs) = %d, want 3", len(plan.LoserSceneIDs))
	}
	want := int64(1_000_000_000 + 2_000_000_000 + 1_500_000_000)
	if plan.ReclaimableBytes != want {
		t.Errorf("ReclaimableBytes = %d, want %d", plan.ReclaimableBytes, want)
	}
}

func TestPlanTagDeduplicatesSceneIDs(t *testing.T) {
	// If a scene appears as a loser in two different groups (rare but
	// possible at high distance settings), we should only tag it once.
	ctx := context.Background()
	st := newTestStore(t)
	runID, _ := st.StartScanRun(ctx, store.WorkflowScenes, nil, nil)

	for _, g := range []*store.SceneGroup{
		{
			ScanRunID: runID,
			Signature: "1|17",
			Status:    store.StatusDecided,
			Scenes: []store.SceneGroupScene{
				{SceneID: "1", Role: store.RoleKeeper},
				{SceneID: "17", Role: store.RoleLoser, FileSize: 100},
			},
		},
		{
			ScanRunID: runID,
			Signature: "17|42",
			Status:    store.StatusDecided,
			Scenes: []store.SceneGroupScene{
				{SceneID: "42", Role: store.RoleKeeper},
				{SceneID: "17", Role: store.RoleLoser, FileSize: 100},
			},
		},
	} {
		if err := st.UpsertSceneGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := PlanTag(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, id := range plan.LoserSceneIDs {
		if id == "17" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("scene 17 should appear exactly once in LoserSceneIDs, got %d", count)
	}
}

func TestPrintTagPlanDryRunMessage(t *testing.T) {
	cfg := loadDefaults(t)
	plan := &TagPlan{
		Groups:           []*store.SceneGroup{{ID: 1}},
		KeeperSceneIDs:   []string{"42"},
		LoserSceneIDs:    []string{"17"},
		ReclaimableBytes: 1_500_000_000,
	}
	buf := &bytes.Buffer{}
	if err := PrintTagPlan(buf, plan, cfg, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"DRY RUN", "_dedupe_loser", "_dedupe_keeper", "1.40 GiB", "--commit"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in dry-run output, got:\n%s", want, out)
		}
	}
}

// TestPlanFilesAndPrint covers the report-only path of files apply.
func TestPlanFilesPicksLosersAndKeeperSwap(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	runID, _ := st.StartScanRun(ctx, store.WorkflowFiles, nil, nil)

	// Scene with the keeper NOT being the current primary → swap.
	fg := &store.FileGroup{
		ScanRunID: runID,
		SceneID:   "999",
		Status:    store.StatusDecided,
		Files: []store.FileGroupFile{
			{FileID: "100", Role: store.RoleLoser, IsPrimary: true, FileSize: 800_000_000, Path: "/old/junk.mp4"},
			{FileID: "101", Role: store.RoleKeeper, IsPrimary: false, FileSize: 800_000_000, Path: "/new/2024-01-15_Good_1080p.mp4"},
		},
	}
	if err := st.UpsertFileGroup(ctx, fg); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanFiles(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("len(actions) = %d, want 1", len(plan.Actions))
	}
	a := plan.Actions[0]
	if a.NewPrimaryFile != "101" {
		t.Errorf("NewPrimaryFile = %s, want 101", a.NewPrimaryFile)
	}
	if a.WasPrimary {
		t.Error("WasPrimary should be false (the keeper wasn't primary at scan time)")
	}
	if len(a.LoserFileIDs) != 1 || a.LoserFileIDs[0] != "100" {
		t.Errorf("LoserFileIDs = %v, want [100]", a.LoserFileIDs)
	}
	if a.LoserBytes != 800_000_000 {
		t.Errorf("LoserBytes = %d, want 800000000", a.LoserBytes)
	}
}

func TestPlanFilesKeeperAlreadyPrimary(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	runID, _ := st.StartScanRun(ctx, store.WorkflowFiles, nil, nil)

	fg := &store.FileGroup{
		ScanRunID: runID,
		SceneID:   "999",
		Status:    store.StatusDecided,
		Files: []store.FileGroupFile{
			{FileID: "100", Role: store.RoleKeeper, IsPrimary: true, FileSize: 800_000_000, Path: "/good/2024.mp4"},
			{FileID: "101", Role: store.RoleLoser, IsPrimary: false, FileSize: 800_000_000, Path: "/inbox/dup.mp4"},
		},
	}
	if err := st.UpsertFileGroup(ctx, fg); err != nil {
		t.Fatal(err)
	}
	plan, _ := PlanFiles(ctx, st)
	if !plan.Actions[0].WasPrimary {
		t.Error("WasPrimary should be true (no swap needed)")
	}
}

func TestPrintFilesPlanCommitVsDryRun(t *testing.T) {
	plan := &FilesPlan{
		Groups: []*store.FileGroup{{ID: 1}},
		Actions: []FilesPlanAction{
			{
				SceneID:        "999",
				NewPrimaryFile: "101",
				NewPrimaryPath: "/sorted/keeper.mp4",
				LoserFileIDs:   []string{"100"},
				LoserPaths:     []string{"/inbox/loser.mp4"},
				LoserBytes:     1_500_000_000,
			},
		},
		TotalReclaimable: 1_500_000_000,
	}
	buf := &bytes.Buffer{}
	if err := PrintFilesPlan(buf, plan, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "DRY RUN") {
		t.Errorf("expected DRY RUN marker, got:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "interactive YES") {
		t.Errorf("expected interactive YES hint in dry-run output")
	}

	buf.Reset()
	if err := PrintFilesPlan(buf, plan, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "COMMIT") {
		t.Errorf("expected COMMIT marker, got:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "DRY RUN") {
		t.Errorf("commit-mode output shouldn't say DRY RUN")
	}
}

func TestPrintFilesReports(t *testing.T) {
	reports := []*FilesReport{
		{SceneID: "1", Status: "success", BytesReclaimed: 1_500_000_000, FilesDeletedCount: 2, PrimaryWasSwapped: true},
		{SceneID: "2", Status: "failed", Error: "boom"},
	}
	buf := &bytes.Buffer{}
	if err := PrintFilesReports(buf, reports); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"successes:        1", "failures:         1", "primary swaps:    1", "files deleted:    2", "1.40 GiB", "boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestPrintTagPlanCommitOmitsDryRunNotice(t *testing.T) {
	cfg := loadDefaults(t)
	plan := &TagPlan{Groups: []*store.SceneGroup{{ID: 1}}}
	buf := &bytes.Buffer{}
	if err := PrintTagPlan(buf, plan, cfg, true); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "dry run") || strings.Contains(out, "DRY RUN") {
		t.Errorf("commit-mode output shouldn't mention dry run, got:\n%s", out)
	}
	if !strings.Contains(out, "COMMIT") {
		t.Errorf("expected COMMIT marker, got:\n%s", out)
	}
}
