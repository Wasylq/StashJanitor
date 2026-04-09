package scan

import (
	"context"
	"testing"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/decide"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
)

func newFileScorer(t *testing.T) *decide.FileScorer {
	t.Helper()
	cfg, err := config.Load("/missing")
	if err != nil {
		t.Fatal(err)
	}
	s, err := decide.NewFileScorer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// makeMultiFileScene constructs a stash.Scene with the given files.
// fingerprints/etc are omitted because workflow B doesn't read them.
func makeMultiFileScene(id string, files []stash.VideoFile) stash.Scene {
	return stash.Scene{ID: id, Files: files}
}

func TestProcessFileGroupPicksGoodFilename(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	sc := newFileScorer(t)
	runID, _ := st.StartScanRun(ctx, store.WorkflowFiles, nil, nil)

	scene := makeMultiFileScene("56297", []stash.VideoFile{
		{
			ID:       "100",
			Path:     "/data/Codi.Vore/2023-10-24_Codi.Vore-How.Women.Orgasm_1080p.mp4",
			Basename: "2023-10-24_Codi.Vore-How.Women.Orgasm_1080p.mp4",
			Size:     1_500_000_000,
			ModTime:  "2023-10-25T12:00:00Z",
		},
		{
			ID:       "101",
			Path:     "/data/inbox/random_video.mp4",
			Basename: "random_video.mp4",
			Size:     1_500_000_000,
			ModTime:  "2023-10-26T12:00:00Z", // newer, but loses on filename quality
		},
	})

	got, err := processFileGroup(ctx, st, sc, runID, &scene)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusDecided {
		t.Errorf("status = %s, want decided", got.Status)
	}

	groups, err := st.ListFileGroups(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1", len(groups))
	}
	fg := groups[0]
	if fg.SceneID != "56297" {
		t.Errorf("SceneID = %s, want 56297", fg.SceneID)
	}
	if len(fg.Files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(fg.Files))
	}
	for _, f := range fg.Files {
		switch f.FileID {
		case "100":
			if f.Role != store.RoleKeeper {
				t.Errorf("file 100 role = %s, want keeper (filename_quality wins)", f.Role)
			}
			if f.FilenameQuality != 1 {
				t.Errorf("file 100 FilenameQuality = %d, want 1", f.FilenameQuality)
			}
		case "101":
			if f.Role != store.RoleLoser {
				t.Errorf("file 101 role = %s, want loser", f.Role)
			}
		}
	}
}

func TestProcessFileGroupHonorsKeepAll(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	sc := newFileScorer(t)
	runID, _ := st.StartScanRun(ctx, store.WorkflowFiles, nil, nil)

	scene := makeMultiFileScene("999", []stash.VideoFile{
		{ID: "1", Basename: "2023-10-24_Good_1080p.mp4", Path: "/d/a.mp4", Size: 1000},
		{ID: "2", Basename: "junk.mp4", Path: "/d/b.mp4", Size: 1000},
	})
	if err := st.PutUserDecision(ctx, store.UserDecision{
		Key:      "scene:999",
		Workflow: store.WorkflowFiles,
		Decision: "keep_all",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := processFileGroup(ctx, st, sc, runID, &scene)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusDismissed {
		t.Errorf("status = %s, want dismissed", got.Status)
	}
	groups, _ := st.ListFileGroups(ctx, nil)
	for _, f := range groups[0].Files {
		if f.Role != store.RoleUndecided {
			t.Errorf("file %s role = %s, want undecided", f.FileID, f.Role)
		}
	}
}

func TestProcessFileGroupAllTiedNeedsReview(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	sc := newFileScorer(t)
	runID, _ := st.StartScanRun(ctx, store.WorkflowFiles, nil, nil)

	// Two files with no filename match, no path priority match, identical mod_time.
	scene := makeMultiFileScene("777", []stash.VideoFile{
		{ID: "1", Basename: "junkA.mp4", Path: "/random/a.mp4", ModTime: "2023-10-24T00:00:00Z"},
		{ID: "2", Basename: "junkB.mp4", Path: "/random/b.mp4", ModTime: "2023-10-24T00:00:00Z"},
	})
	got, err := processFileGroup(ctx, st, sc, runID, &scene)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusNeedsReview {
		t.Errorf("status = %s, want needs_review", got.Status)
	}
}
