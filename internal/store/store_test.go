package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// newTestStore creates a fresh store in a temp dir, applies the schema, and
// closes it on test teardown.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "stash-janitor.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenAppliesSchemaIdempotently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stash-janitor.sqlite")

	for i := 0; i < 3; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		// Should always end up at currentSchemaVersion.
		var v int
		if err := s.DB().QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
			t.Fatalf("reading schema_version: %v", err)
		}
		if v != currentSchemaVersion {
			t.Errorf("schema_version = %d, want %d", v, currentSchemaVersion)
		}
		_ = s.Close()
	}
}

func TestSceneGroupRoundtrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	dist := 4
	dur := 1.0
	runID, err := s.StartScanRun(ctx, WorkflowScenes, &dist, &dur)
	if err != nil {
		t.Fatalf("StartScanRun: %v", err)
	}
	if runID == 0 {
		t.Fatal("StartScanRun returned id 0")
	}

	g := &SceneGroup{
		ScanRunID: runID,
		Signature: SceneGroupSignature([]string{"42", "17", "8"}),
		Status:    StatusDecided,
		Scenes: []SceneGroupScene{
			{
				SceneID:    "42",
				Role:       RoleKeeper,
				Width:      1920,
				Height:     1080,
				Bitrate:    5_000_000,
				Codec:      "hevc",
				FileSize:   2_500_000_000,
				Duration:   1234.5,
				Organized:  true,
				HasStashID: true,
				TagCount:   8,
				PrimaryPath: "/sorted/scene42.mp4",
			},
			{
				SceneID:  "17",
				Role:     RoleLoser,
				Width:    1280,
				Height:   720,
				FileSize: 800_000_000,
			},
			{
				SceneID:  "8",
				Role:     RoleLoser,
				Width:    1920,
				Height:   1080,
				FileSize: 2_400_000_000,
			},
		},
	}
	if err := s.UpsertSceneGroup(ctx, g); err != nil {
		t.Fatalf("UpsertSceneGroup: %v", err)
	}
	if g.ID == 0 {
		t.Fatal("UpsertSceneGroup did not populate g.ID")
	}

	got, err := s.GetSceneGroupBySignature(ctx, g.Signature)
	if err != nil {
		t.Fatalf("GetSceneGroupBySignature: %v", err)
	}
	if got.ID != g.ID {
		t.Errorf("got.ID = %d, want %d", got.ID, g.ID)
	}
	if got.Status != StatusDecided {
		t.Errorf("got.Status = %s, want %s", got.Status, StatusDecided)
	}
	if len(got.Scenes) != 3 {
		t.Fatalf("len(scenes) = %d, want 3", len(got.Scenes))
	}
	// Scenes are returned ordered by scene_id, so order is 17, 42, 8 → "17","42","8"
	// String sort puts "17" < "42" < "8" since '8' > '4' lexicographically.
	wantOrder := []string{"17", "42", "8"}
	for i, want := range wantOrder {
		if got.Scenes[i].SceneID != want {
			t.Errorf("scenes[%d].SceneID = %s, want %s", i, got.Scenes[i].SceneID, want)
		}
	}
	// Spot-check a couple of fields on the keeper.
	var keeper *SceneGroupScene
	for i := range got.Scenes {
		if got.Scenes[i].Role == RoleKeeper {
			keeper = &got.Scenes[i]
			break
		}
	}
	if keeper == nil {
		t.Fatal("no keeper in returned group")
	}
	if !keeper.Organized {
		t.Error("keeper Organized round-trip failed")
	}
	if !keeper.HasStashID {
		t.Error("keeper HasStashID round-trip failed")
	}
	if keeper.FileSize != 2_500_000_000 {
		t.Errorf("keeper FileSize = %d, want 2500000000", keeper.FileSize)
	}
}

func TestSceneGroupSignatureStable(t *testing.T) {
	a := SceneGroupSignature([]string{"42", "17", "8"})
	b := SceneGroupSignature([]string{"8", "42", "17"})
	if a != b {
		t.Errorf("signatures differ: %q vs %q", a, b)
	}
	if a == "" {
		t.Error("expected non-empty signature")
	}
	// Lexicographic sort: "17" < "42" < "8"
	want := "17|42|8"
	if a != want {
		t.Errorf("signature = %q, want %q", a, want)
	}
}

func TestUpsertSceneGroupReplacesScenes(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	runID, _ := s.StartScanRun(ctx, WorkflowScenes, nil, nil)

	g := &SceneGroup{
		ScanRunID: runID,
		Signature: "42|17",
		Status:    StatusPending,
		Scenes: []SceneGroupScene{
			{SceneID: "42", Role: RoleUndecided},
			{SceneID: "17", Role: RoleUndecided},
		},
	}
	if err := s.UpsertSceneGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	firstID := g.ID

	// Re-upsert with one scene swapped out — should reflect the new state.
	g.Scenes = []SceneGroupScene{
		{SceneID: "42", Role: RoleKeeper},
		{SceneID: "99", Role: RoleLoser},
	}
	g.Status = StatusDecided
	if err := s.UpsertSceneGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	if g.ID != firstID {
		t.Errorf("expected upsert to keep id=%d, got %d", firstID, g.ID)
	}

	got, err := s.GetSceneGroupBySignature(ctx, "42|17")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusDecided {
		t.Errorf("status = %s, want %s", got.Status, StatusDecided)
	}
	if len(got.Scenes) != 2 {
		t.Fatalf("len(scenes) = %d, want 2", len(got.Scenes))
	}
	// IDs should be the new set: 42 and 99 (NOT 17).
	seen := map[string]bool{}
	for _, sc := range got.Scenes {
		seen[sc.SceneID] = true
	}
	if !seen["42"] || !seen["99"] {
		t.Errorf("expected scenes [42, 99], got %v", seen)
	}
	if seen["17"] {
		t.Errorf("expected old scene 17 to be removed by upsert")
	}
}

func TestFileGroupRoundtrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	runID, _ := s.StartScanRun(ctx, WorkflowFiles, nil, nil)

	fg := &FileGroup{
		ScanRunID: runID,
		SceneID:   "56297",
		Status:    StatusDecided,
		Files: []FileGroupFile{
			{
				FileID:          "100",
				Role:            RoleKeeper,
				IsPrimary:       false,
				Basename:        "2023-10-24_Codi.Vore-How.Women.Orgasm.-.Codi.Vore_1080p.mp4",
				Path:            "/sorted/2023-10-24_Codi.Vore-How.Women.Orgasm.-.Codi.Vore_1080p.mp4",
				ModTime:         "2023-10-25T12:00:00Z",
				FilenameQuality: 1,
				Width:           1920,
				Height:          1080,
				FileSize:        1_500_000_000,
				Codec:           "h264",
			},
			{
				FileID:          "101",
				Role:            RoleLoser,
				IsPrimary:       true,
				Basename:        "video1.mp4",
				Path:            "/inbox/video1.mp4",
				FilenameQuality: 0,
				FileSize:        1_500_000_000,
			},
		},
	}
	if err := s.UpsertFileGroup(ctx, fg); err != nil {
		t.Fatalf("UpsertFileGroup: %v", err)
	}
	groups, err := s.ListFileGroups(ctx, []string{StatusDecided})
	if err != nil {
		t.Fatalf("ListFileGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1", len(groups))
	}
	got := groups[0]
	if got.SceneID != "56297" {
		t.Errorf("SceneID = %s, want 56297", got.SceneID)
	}
	if len(got.Files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(got.Files))
	}
	// Find the keeper and verify FilenameQuality round-trip.
	var keeper *FileGroupFile
	for i := range got.Files {
		if got.Files[i].Role == RoleKeeper {
			keeper = &got.Files[i]
		}
	}
	if keeper == nil {
		t.Fatal("no keeper file in round-trip")
	}
	if keeper.FilenameQuality != 1 {
		t.Errorf("keeper FilenameQuality = %d, want 1", keeper.FilenameQuality)
	}
	if keeper.Basename != "2023-10-24_Codi.Vore-How.Women.Orgasm.-.Codi.Vore_1080p.mp4" {
		t.Errorf("keeper Basename round-trip mismatch: %q", keeper.Basename)
	}
}

func TestUserDecisionUpsert(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	d := UserDecision{
		Key:      "42|17|8",
		Workflow: WorkflowScenes,
		Decision: "not_duplicate",
		Notes:    "manually verified",
	}
	if err := s.PutUserDecision(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUserDecision(ctx, d.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Decision != "not_duplicate" {
		t.Errorf("Decision = %q, want not_duplicate", got.Decision)
	}
	if got.Notes != "manually verified" {
		t.Errorf("Notes = %q, want manually verified", got.Notes)
	}

	// Upsert with new decision.
	d.Decision = "dismiss"
	d.Notes = "changed my mind"
	if err := s.PutUserDecision(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUserDecision(ctx, d.Key)
	if got.Decision != "dismiss" {
		t.Errorf("Decision after upsert = %q, want dismiss", got.Decision)
	}

	// Missing key.
	if _, err := s.GetUserDecision(ctx, "does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestMarkSceneGroupApplied(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	runID, _ := s.StartScanRun(ctx, WorkflowScenes, nil, nil)

	g := &SceneGroup{
		ScanRunID: runID,
		Signature: "42|17",
		Status:    StatusDecided,
		Scenes:    []SceneGroupScene{{SceneID: "42", Role: RoleKeeper}, {SceneID: "17", Role: RoleLoser}},
	}
	if err := s.UpsertSceneGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSceneGroupApplied(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSceneGroupBySignature(ctx, "42|17")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusApplied {
		t.Errorf("status = %s, want applied", got.Status)
	}
	if got.AppliedAt == nil {
		t.Error("AppliedAt should be non-nil after MarkSceneGroupApplied")
	}
}

func TestLatestScanRun(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// No runs yet.
	if _, err := s.LatestScanRun(ctx, WorkflowScenes); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	d := 4
	dd := 1.0
	id1, _ := s.StartScanRun(ctx, WorkflowScenes, &d, &dd)
	id2, _ := s.StartScanRun(ctx, WorkflowScenes, &d, &dd)
	if err := s.FinishScanRun(ctx, id2, 5); err != nil {
		t.Fatal(err)
	}

	got, err := s.LatestScanRun(ctx, WorkflowScenes)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id2 {
		t.Errorf("got latest id = %d, want %d (id1=%d)", got.ID, id2, id1)
	}
	if got.GroupCount != 5 {
		t.Errorf("group_count = %d, want 5", got.GroupCount)
	}
	if got.FinishedAt == nil {
		t.Error("FinishedAt should be set after FinishScanRun")
	}
}
