package organize

import (
	"testing"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/stash"
)

func defaultCfg() *config.OrganizeConfig {
	return &config.OrganizeConfig{
		BaseDir:           "/data",
		PathTemplate:      "{performer}/{date}_{performer}-{title}_{resolution}.{ext}",
		SpaceChar:         ".",
		FolderSpaceChar:   " ",
		PerformerStrategy: "first_alphabetical",
		RequiredFields:    []string{"performer", "date"},
		RenameInPlace:     true,
	}
}

func TestComputeTargetPathFullMetadata(t *testing.T) {
	scene := &stash.Scene{
		ID:    "42",
		Title: "Your Work Crush has a Girl Cock",
		Date:  "2024-12-15",
		Performers: []stash.Performer{{Name: "Aimee Waves"}},
		Studio:     &stash.Studio{Name: "Some Studio"},
	}
	file := &stash.VideoFile{
		Basename: "junk.mp4",
		Height:   1080,
	}
	target, reason := ComputeTargetPath(scene, file, defaultCfg())
	if reason != "" {
		t.Fatalf("unexpected skip: %s", reason)
	}
	want := "/data/Aimee Waves/2024-12-15_Aimee.Waves-Your.Work.Crush.has.a.Girl.Cock_1080p.mp4"
	if target != want {
		t.Errorf("got:  %s\nwant: %s", target, want)
	}
}

func TestComputeTargetPath4K(t *testing.T) {
	scene := &stash.Scene{
		Title: "Some Title", Date: "2022-08-24",
		Performers: []stash.Performer{{Name: "Angel The Dreamgirl"}},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 2160}
	target, _ := ComputeTargetPath(scene, file, defaultCfg())
	want := "/data/Angel The Dreamgirl/2022-08-24_Angel.The.Dreamgirl-Some.Title_4k.mp4"
	if target != want {
		t.Errorf("got:  %s\nwant: %s", target, want)
	}
}

func TestComputeTargetPathMultiPerformerAlphabetical(t *testing.T) {
	scene := &stash.Scene{
		Title: "Title", Date: "2024-01-01",
		Performers: []stash.Performer{
			{Name: "Zoe Bloom"},
			{Name: "Aimee Waves"},
		},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	target, _ := ComputeTargetPath(scene, file, defaultCfg())
	if target == "" {
		t.Fatal("expected a target")
	}
	// Alphabetically, Aimee Waves comes before Zoe Bloom.
	if !contains(target, "Aimee Waves") {
		t.Errorf("expected folder to be Aimee Waves, got: %s", target)
	}
}

func TestComputeTargetPathMissingPerformer(t *testing.T) {
	scene := &stash.Scene{Title: "Title", Date: "2024-01-01"}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	_, reason := ComputeTargetPath(scene, file, defaultCfg())
	if reason == "" {
		t.Error("expected skip reason for missing performer")
	}
}

func TestComputeTargetPathMissingDate(t *testing.T) {
	scene := &stash.Scene{
		Title:      "Title",
		Performers: []stash.Performer{{Name: "Foo"}},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	_, reason := ComputeTargetPath(scene, file, defaultCfg())
	if reason == "" {
		t.Error("expected skip reason for missing date")
	}
}

func TestComputeTargetPathSanitizesFilename(t *testing.T) {
	scene := &stash.Scene{
		Title: "What? Why: Because!",
		Date:  "2024-01-01",
		Performers: []stash.Performer{{Name: "Test"}},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	target, _ := ComputeTargetPath(scene, file, defaultCfg())
	// ? should be stripped, : should become -
	if contains(target, "?") || contains(target, ":") {
		t.Errorf("target contains unsafe chars: %s", target)
	}
}

func TestComputeTargetPathStudioTemplate(t *testing.T) {
	cfg := defaultCfg()
	cfg.PathTemplate = "{studio}/{date} - {title}/{date}_{performer}-{title}_{resolution}.{ext}"
	scene := &stash.Scene{
		Title:      "Blonde MILF Dogging",
		Date:       "2016-10-14",
		Studio:     &stash.Studio{Name: "On A Dogging Mission"},
		Performers: []stash.Performer{{Name: "Gemma Gold"}},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	target, _ := ComputeTargetPath(scene, file, cfg)
	want := "/data/On A Dogging Mission/2016-10-14 - Blonde MILF Dogging/2016-10-14_Gemma.Gold-Blonde.MILF.Dogging_1080p.mp4"
	if target != want {
		t.Errorf("got:  %s\nwant: %s", target, want)
	}
}

func contains(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && s != sub && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
