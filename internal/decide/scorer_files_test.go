package decide

import (
	"strings"
	"testing"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/store"
)

func defaultFileScorer(t *testing.T) *FileScorer {
	t.Helper()
	cfg, err := config.Load("/missing")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewFileScorer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewFileScorerRejectsBadRegex(t *testing.T) {
	cfg, _ := config.Load("/missing")
	cfg.Scoring.Files.FilenameQuality.Pattern = "(unclosed"
	_, err := NewFileScorer(cfg)
	if err == nil {
		t.Fatal("expected error for bad regex")
	}
	if !strings.Contains(err.Error(), "filename_quality") {
		t.Errorf("expected error to mention filename_quality, got: %v", err)
	}
}

func TestNewFileScorerRejectsUnknownRule(t *testing.T) {
	cfg, _ := config.Load("/missing")
	cfg.Scoring.Files.Rules = []string{"filename_quality", "warpdrive"}
	_, err := NewFileScorer(cfg)
	if err == nil {
		t.Fatal("expected error for unknown rule")
	}
}

func TestClassifyFilenameMatchesUserConvention(t *testing.T) {
	s := defaultFileScorer(t)
	matches := []string{
		// User's convention from PLAN.md
		"2023-10-24_Codi.Vore-How.Women.Orgasm.-.Codi.Vore_1080p.mp4",
		"2024-12-15_Aimee.Waves-Your.Work.Crush.has.a.Girl.Cock_1080p.mp4",
		// Performers omitted (>3 performer case)
		"2023-10-24_-Group.Title_1080p.mp4",
		// 4K resolution token
		"2024-11-07_Anya.Olsen-Stepson.Maybe.You.Can.Get.Me.Pregnant_4k.mp4",
		// Different extension
		"2023-10-24_Some.Person-Title_1080p.mkv",
	}
	for _, name := range matches {
		if got := s.ClassifyFilename(name); got != 1 {
			t.Errorf("ClassifyFilename(%q) = %d, want 1", name, got)
		}
	}

	misses := []string{
		"BettieBondage_BettieBondage_Boyfriend_and_Dad_Cumfuck_Bettie_4K.mp4", // missing date prefix
		"video_001.mp4",
		"IMG_2023.mov",
		"some random title.mp4",
		"2023-10-24_no_resolution.mp4",
	}
	for _, name := range misses {
		if got := s.ClassifyFilename(name); got != 0 {
			t.Errorf("ClassifyFilename(%q) = %d, want 0", name, got)
		}
	}
}

func TestDecideFilesPrefersFilenameQuality(t *testing.T) {
	s := defaultFileScorer(t)
	files := []store.FileGroupFile{
		{FileID: "1", Basename: "video_001.mp4", FilenameQuality: 0, Path: "/data/video_001.mp4", ModTime: "2023-10-24T00:00:00Z"},
		{FileID: "2", Basename: "2023-10-24_Codi.Vore-Title_1080p.mp4", FilenameQuality: 1, Path: "/data/2023-10-24_Codi.Vore-Title_1080p.mp4", ModTime: "2023-10-24T00:00:00Z"},
	}
	d := s.DecideFiles(files)
	if d.KeeperIndex != 1 {
		t.Errorf("KeeperIndex = %d, want 1 (filename_quality wins)", d.KeeperIndex)
	}
}

func TestDecideFilesFallsBackToModTime(t *testing.T) {
	s := defaultFileScorer(t)
	// Both filenames are quality matches; both paths are the same; mod_time
	// is the only differentiator.
	files := []store.FileGroupFile{
		{FileID: "1", FilenameQuality: 1, Path: "/data/a.mp4", ModTime: "2023-10-24T10:00:00Z"},
		{FileID: "2", FilenameQuality: 1, Path: "/data/b.mp4", ModTime: "2023-10-25T10:00:00Z"},
	}
	d := s.DecideFiles(files)
	if d.KeeperIndex != 1 {
		t.Errorf("KeeperIndex = %d, want 1 (newer mod_time wins)", d.KeeperIndex)
	}
}

func TestDecideFilesAllTied(t *testing.T) {
	s := defaultFileScorer(t)
	files := []store.FileGroupFile{
		{FileID: "1", FilenameQuality: 0, Path: "/x/a.mp4", ModTime: "2023-10-24T10:00:00Z"},
		{FileID: "2", FilenameQuality: 0, Path: "/x/b.mp4", ModTime: "2023-10-24T10:00:00Z"},
	}
	d := s.DecideFiles(files)
	if d.KeeperIndex != -1 {
		t.Errorf("KeeperIndex = %d, want -1 (all tied)", d.KeeperIndex)
	}
}

func TestDecideFilesPathPriority(t *testing.T) {
	s := defaultFileScorer(t)
	// filename_quality tied → path_priority decides → /sorted beats /inbox.
	files := []store.FileGroupFile{
		{FileID: "1", FilenameQuality: 1, Path: "/inbox/a.mp4", ModTime: "2023-10-24T00:00:00Z"},
		{FileID: "2", FilenameQuality: 1, Path: "/sorted/b.mp4", ModTime: "2023-10-24T00:00:00Z"},
	}
	d := s.DecideFiles(files)
	if d.KeeperIndex != 1 {
		t.Errorf("KeeperIndex = %d, want 1 (/sorted beats /inbox)", d.KeeperIndex)
	}
}
