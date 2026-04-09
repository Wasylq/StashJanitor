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
