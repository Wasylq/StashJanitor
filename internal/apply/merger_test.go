package apply

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Wasylq/StashJanitor/internal/store"
)

func TestPlanMergeCountsLosersAndBytes(t *testing.T) {
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
				{SceneID: "2", Role: store.RoleLoser, FileSize: 1_500_000_000},
			},
		},
		{
			ScanRunID: runID,
			Signature: "10|11|12",
			Status:    store.StatusDecided,
			Scenes: []store.SceneGroupScene{
				{SceneID: "10", Role: store.RoleKeeper},
				{SceneID: "11", Role: store.RoleLoser, FileSize: 800_000_000},
				{SceneID: "12", Role: store.RoleLoser, FileSize: 1_200_000_000},
			},
		},
		// needs_review group should not be in the plan.
		{
			ScanRunID: runID,
			Signature: "100|101",
			Status:    store.StatusNeedsReview,
			Scenes: []store.SceneGroupScene{
				{SceneID: "100", Role: store.RoleUndecided, FileSize: 999},
			},
		},
	}
	for _, g := range groups {
		if err := st.UpsertSceneGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := PlanMerge(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 2 {
		t.Errorf("len(plan.Groups) = %d, want 2 (needs_review excluded)", len(plan.Groups))
	}
	if plan.TotalLosers != 3 {
		t.Errorf("TotalLosers = %d, want 3", plan.TotalLosers)
	}
	want := int64(1_500_000_000 + 800_000_000 + 1_200_000_000)
	if plan.ReclaimableBytes != want {
		t.Errorf("ReclaimableBytes = %d, want %d", plan.ReclaimableBytes, want)
	}
}

func TestPrintMergePlanDryRunMessage(t *testing.T) {
	plan := &MergePlan{
		Groups:           []*store.SceneGroup{{ID: 1}},
		TotalLosers:      1,
		ReclaimableBytes: 2_000_000_000,
	}
	buf := &bytes.Buffer{}
	if err := PrintMergePlan(buf, plan, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"DRY RUN", "1.86 GiB", "interactive YES", "sceneMerge"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in dry-run output, got:\n%s", want, out)
		}
	}
}

func TestPrintMergePlanCommitMessage(t *testing.T) {
	plan := &MergePlan{
		Groups: []*store.SceneGroup{{ID: 1}},
	}
	buf := &bytes.Buffer{}
	if err := PrintMergePlan(buf, plan, true); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "COMMIT") {
		t.Errorf("expected COMMIT marker in commit-mode output, got:\n%s", out)
	}
	if strings.Contains(out, "DRY RUN") {
		t.Errorf("commit-mode output shouldn't say DRY RUN, got:\n%s", out)
	}
}

func TestPrintMergeReports(t *testing.T) {
	reports := []*MergeReport{
		{GroupID: 1, KeeperSceneID: "42", Status: "success", BytesReclaimed: 1_500_000_000},
		{GroupID: 2, KeeperSceneID: "43", Status: "failed", Error: "test failure"},
		{GroupID: 3, KeeperSceneID: "44", Status: "skipped", Error: "already applied"},
	}
	buf := &bytes.Buffer{}
	if err := PrintMergeReports(buf, reports); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"successes: 1", "failures:  1", "skipped:   1", "test failure", "1.40 GiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in reports output, got:\n%s", want, out)
		}
	}
}
