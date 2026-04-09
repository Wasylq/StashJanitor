package scan

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/decide"
	"github.com/Wasylq/StashJanitor/internal/stash"
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

func newTestScorer(t *testing.T) *decide.SceneScorer {
	t.Helper()
	cfg, err := config.Load("/missing")
	if err != nil {
		t.Fatal(err)
	}
	s, err := decide.NewSceneScorer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// makeScene constructs a stash.Scene with the minimum fields the snapshot
// converter looks at. Helpers like this make the test cases readable.
func makeScene(id string, hasStashID bool, width, height, bitrate int, codec string, size int64, path string, organized bool, tags int) stash.Scene {
	sc := stash.Scene{
		ID:        id,
		Organized: organized,
		Files: []stash.VideoFile{{
			ID:         id + "-f",
			Path:       path,
			Basename:   filepath.Base(path),
			Size:       size,
			Width:      width,
			Height:     height,
			BitRate:    bitrate,
			VideoCodec: codec,
		}},
	}
	for i := 0; i < tags; i++ {
		sc.Tags = append(sc.Tags, stash.Tag{ID: "t", Name: "t"})
	}
	if hasStashID {
		sc.StashIDs = []stash.StashID{{Endpoint: "https://stashdb.org", StashID: "abc"}}
	}
	return sc
}

func TestProcessSceneGroupClearWinner(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	sc := newTestScorer(t)

	d := 4
	dd := 1.0
	runID, _ := st.StartScanRun(ctx, store.WorkflowScenes, &d, &dd)

	raw := []stash.Scene{
		makeScene("17", false, 3840, 2160, 8_000_000, "h264", 5_000_000_000, "/inbox/a.mp4", false, 0),
		makeScene("42", true, 1920, 1080, 4_000_000, "h264", 2_000_000_000, "/sorted/b.mp4", true, 5),
	}
	sig := store.SceneGroupSignature([]string{"17", "42"})

	got, err := processSceneGroup(ctx, st, sc, runID, sig, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusDecided {
		t.Errorf("status = %s, want decided", got.Status)
	}
	if !got.NewlyCreated {
		t.Error("expected NewlyCreated=true on first upsert")
	}

	stored, err := st.GetSceneGroupBySignature(ctx, sig)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Scenes) != 2 {
		t.Fatalf("got %d scenes in store, want 2", len(stored.Scenes))
	}
	// The metadata-rich scene 42 should be the keeper.
	for _, s := range stored.Scenes {
		switch s.SceneID {
		case "42":
			if s.Role != store.RoleKeeper {
				t.Errorf("scene 42 role = %s, want keeper", s.Role)
			}
		case "17":
			if s.Role != store.RoleLoser {
				t.Errorf("scene 17 role = %s, want loser", s.Role)
			}
		}
	}
}

func TestProcessSceneGroupRespectsUserDismiss(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	sc := newTestScorer(t)
	runID, _ := st.StartScanRun(ctx, store.WorkflowScenes, nil, nil)

	raw := []stash.Scene{
		makeScene("1", true, 1920, 1080, 4_000_000, "h264", 2_000_000_000, "/sorted/a.mp4", true, 5),
		makeScene("2", true, 1920, 1080, 4_000_000, "h264", 2_000_000_000, "/sorted/b.mp4", true, 5),
	}
	sig := store.SceneGroupSignature([]string{"1", "2"})

	// User said "not_duplicate" before; the scan must honor it and skip
	// scoring.
	if err := st.PutUserDecision(ctx, store.UserDecision{
		Key:      sig,
		Workflow: store.WorkflowScenes,
		Decision: "not_duplicate",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := processSceneGroup(ctx, st, sc, runID, sig, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusDismissed {
		t.Errorf("status = %s, want dismissed", got.Status)
	}
}

func TestProcessSceneGroupForceKeeper(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	sc := newTestScorer(t)
	runID, _ := st.StartScanRun(ctx, store.WorkflowScenes, nil, nil)

	// Without override, scene 2 (1080p) would beat scene 1 (720p).
	// User pins scene 1 as keeper anyway.
	raw := []stash.Scene{
		makeScene("1", true, 1280, 720, 4_000_000, "h264", 1_000_000_000, "/sorted/a.mp4", true, 5),
		makeScene("2", true, 1920, 1080, 4_000_000, "h264", 2_000_000_000, "/sorted/b.mp4", true, 5),
	}
	sig := store.SceneGroupSignature([]string{"1", "2"})

	if err := st.PutUserDecision(ctx, store.UserDecision{
		Key:      sig,
		Workflow: store.WorkflowScenes,
		Decision: "force_keeper",
		KeeperID: "1",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := processSceneGroup(ctx, st, sc, runID, sig, raw); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.GetSceneGroupBySignature(ctx, sig)
	for _, s := range stored.Scenes {
		switch s.SceneID {
		case "1":
			if s.Role != store.RoleKeeper {
				t.Errorf("scene 1 role = %s, want keeper (force_keeper)", s.Role)
			}
		case "2":
			if s.Role != store.RoleLoser {
				t.Errorf("scene 2 role = %s, want loser", s.Role)
			}
		}
	}
}
