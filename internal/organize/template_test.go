package organize

import (
	"strings"
	"testing"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/stash"
)

func defaultCfg() *config.OrganizeConfig {
	return &config.OrganizeConfig{
		BaseDir:                  "/data",
		PathTemplate:             "{performer}/{date}_{performers}-{title}_{resolution}.{ext}",
		SpaceChar:                ".",
		FolderSpaceChar:          " ",
		PerformerStrategy:        "first_alphabetical",
		MaxPerformersInFilename:  3,
		RequiredFields:           []string{"performer", "date"},
		RenameInPlace:            true,
	}
}

func TestComputeTargetPathSinglePerformer(t *testing.T) {
	scene := &stash.Scene{
		ID:    "42",
		Title: "Your Work Crush has a Girl Cock",
		Date:  "2024-12-15",
		Performers: []stash.Performer{{Name: "Aimee Waves"}},
	}
	file := &stash.VideoFile{Basename: "junk.mp4", Height: 1080}
	target, reason := ComputeTargetPath(scene, file, defaultCfg())
	if reason != "" {
		t.Fatalf("unexpected skip: %s", reason)
	}
	want := "/data/Aimee Waves/2024-12-15_Aimee.Waves-Your.Work.Crush.has.a.Girl.Cock_1080p.mp4"
	if target != want {
		t.Errorf("got:  %s\nwant: %s", target, want)
	}
}

func TestComputeTargetPathTwoPerformers(t *testing.T) {
	scene := &stash.Scene{
		ID: "42", Title: "PPPD-488", Date: "2016-07-19",
		Performers: []stash.Performer{{Name: "AIKA"}, {Name: "Alice Otsu"}},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 720}
	target, _ := ComputeTargetPath(scene, file, defaultCfg())
	// Both performers in filename, alphabetical, underscore-joined, dots for spaces.
	want := "/data/AIKA/2016-07-19_AIKA_Alice.Otsu-PPPD-488_720p.mp4"
	if target != want {
		t.Errorf("got:  %s\nwant: %s", target, want)
	}
}

func TestComputeTargetPathThreePerformers(t *testing.T) {
	scene := &stash.Scene{
		ID: "42", Title: "Title", Date: "2024-01-01",
		Performers: []stash.Performer{{Name: "Charlie"}, {Name: "Alice"}, {Name: "Bob Smith"}},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	target, _ := ComputeTargetPath(scene, file, defaultCfg())
	// 3 performers = still included (max is 3).
	want := "/data/Alice/2024-01-01_Alice_Bob.Smith_Charlie-Title_1080p.mp4"
	if target != want {
		t.Errorf("got:  %s\nwant: %s", target, want)
	}
}

func TestComputeTargetPathFourPerformersOmitted(t *testing.T) {
	scene := &stash.Scene{
		ID: "42", Title: "Group Scene", Date: "2024-01-01",
		Performers: []stash.Performer{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	target, _ := ComputeTargetPath(scene, file, defaultCfg())
	// >3 performers: {performers} is empty, so filename starts with date_-title
	want := "/data/A/2024-01-01_-Group.Scene_1080p.mp4"
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
		ID: "42", Title: "Title", Date: "2024-01-01",
		Performers: []stash.Performer{
			{Name: "Zoe Bloom"},
			{Name: "Aimee Waves"},
		},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	target, _ := ComputeTargetPath(scene, file, defaultCfg())
	// Folder = first alphabetical performer.
	if !strings.Contains(target, "Aimee Waves/") {
		t.Errorf("expected folder to be Aimee Waves, got: %s", target)
	}
	// Filename = both performers, alphabetical.
	if !strings.Contains(target, "Aimee.Waves_Zoe.Bloom") {
		t.Errorf("expected both performers in filename, got: %s", target)
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
	cfg.PathTemplate = "{studio}/{date} - {title}/{date}_{performers}-{title}_{resolution}.{ext}"
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

func TestComputeTargetPathSlashInTitle(t *testing.T) {
	scene := &stash.Scene{
		ID: "42", Title: "Step-Mom/Step-Son Fantasy", Date: "2024-01-01",
		Performers: []stash.Performer{{Name: "Test Performer"}},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	target, _ := ComputeTargetPath(scene, file, defaultCfg())
	// Slash should be replaced with dash, not create a subdirectory.
	// Expected: /data/Test Performer/...Step-Mom-Step-Son... (3 slashes total)
	if strings.Contains(target, "Step-Mom/Step-Son") {
		t.Errorf("raw slash survived in path: %s", target)
	}
	if !strings.Contains(target, "Step-Mom-Step-Son") {
		t.Errorf("expected slash replaced with dash, got: %s", target)
	}
}

func TestComputeTargetPathEmojiStripped(t *testing.T) {
	scene := &stash.Scene{
		ID: "42", Title: "🎄 No PPV 🎄 Holiday Special", Date: "2024-12-25",
		Performers: []stash.Performer{{Name: "Test"}},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	target, _ := ComputeTargetPath(scene, file, defaultCfg())
	if strings.ContainsRune(target, '🎄') {
		t.Errorf("emoji survived in path: %s", target)
	}
	// Should still have the text content.
	if !strings.Contains(target, "Holiday") {
		t.Errorf("expected 'Holiday' in path, got: %s", target)
	}
}

func TestComputeTargetPathLongTitleTruncated(t *testing.T) {
	longTitle := strings.Repeat("A Very Long Title ", 20) // ~360 chars
	scene := &stash.Scene{
		ID: "42", Title: longTitle, Date: "2024-01-01",
		Performers: []stash.Performer{{Name: "Test"}},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	target, _ := ComputeTargetPath(scene, file, defaultCfg())
	if len(target) > 255 {
		t.Errorf("path too long (%d chars): %s", len(target), target)
	}
	// Should contain the scene ID for uniqueness.
	if !strings.Contains(target, "42") {
		t.Errorf("truncated path should contain scene ID for uniqueness: %s", target)
	}
}

func TestComputeTargetPathBackslashInPerformer(t *testing.T) {
	scene := &stash.Scene{
		ID: "42", Title: "Title", Date: "2024-01-01",
		Performers: []stash.Performer{{Name: "Performer\\Name"}},
	}
	file := &stash.VideoFile{Basename: "x.mp4", Height: 1080}
	target, _ := ComputeTargetPath(scene, file, defaultCfg())
	if strings.Contains(target, "\\") {
		t.Errorf("backslash survived in path: %s", target)
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
