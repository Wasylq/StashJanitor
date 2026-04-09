package decide

import (
	"testing"

	"github.com/Wasylq/StashJanitor/internal/stash"
)

func TestPickPostMergeKeeperPrefersResolution(t *testing.T) {
	s := defaultFileScorer(t)
	files := []stash.VideoFile{
		{ID: "1", Width: 1280, Height: 720, BitRate: 4_000_000, Basename: "a.mp4"},
		{ID: "2", Width: 1920, Height: 1080, BitRate: 4_000_000, Basename: "b.mp4"},
	}
	idx, reason := s.PickPostMergeKeeper(files)
	if idx != 1 {
		t.Errorf("idx = %d, want 1 (1080p beats 720p), reason=%q", idx, reason)
	}
}

func TestPickPostMergeKeeperPrefersBitrateWhenResEqual(t *testing.T) {
	s := defaultFileScorer(t)
	files := []stash.VideoFile{
		{ID: "1", Width: 1920, Height: 1080, BitRate: 4_000_000, Basename: "a.mp4"},
		{ID: "2", Width: 1920, Height: 1080, BitRate: 8_000_000, Basename: "b.mp4"},
	}
	idx, _ := s.PickPostMergeKeeper(files)
	if idx != 1 {
		t.Errorf("idx = %d, want 1 (higher bitrate)", idx)
	}
}

func TestPickPostMergeKeeperFilenameTiebreaker(t *testing.T) {
	// Resolution + bitrate equal — filename quality decides.
	s := defaultFileScorer(t)
	files := []stash.VideoFile{
		{ID: "1", Width: 1920, Height: 1080, BitRate: 4_000_000, Basename: "random_video.mp4"},
		{ID: "2", Width: 1920, Height: 1080, BitRate: 4_000_000, Basename: "2023-10-24_Codi.Vore-Title_1080p.mp4"},
	}
	idx, _ := s.PickPostMergeKeeper(files)
	if idx != 1 {
		t.Errorf("idx = %d, want 1 (filename quality match wins)", idx)
	}
}

func TestPickPostMergeKeeperEmpty(t *testing.T) {
	s := defaultFileScorer(t)
	idx, _ := s.PickPostMergeKeeper(nil)
	if idx != -1 {
		t.Errorf("idx = %d, want -1 for empty input", idx)
	}
}

func TestPickPostMergeKeeperSingleton(t *testing.T) {
	s := defaultFileScorer(t)
	idx, _ := s.PickPostMergeKeeper([]stash.VideoFile{{ID: "1"}})
	if idx != 0 {
		t.Errorf("idx = %d, want 0 for single file", idx)
	}
}

func TestPickPostMergeKeeperAllTied(t *testing.T) {
	s := defaultFileScorer(t)
	files := []stash.VideoFile{
		{ID: "1", Width: 1920, Height: 1080, BitRate: 4_000_000, Basename: "a.mp4", Size: 100, ModTime: "2023-10-24T00:00:00Z"},
		{ID: "2", Width: 1920, Height: 1080, BitRate: 4_000_000, Basename: "b.mp4", Size: 100, ModTime: "2023-10-24T00:00:00Z"},
	}
	idx, reason := s.PickPostMergeKeeper(files)
	if idx != -1 {
		t.Errorf("idx = %d, want -1 for all-tied, reason=%q", idx, reason)
	}
	if reason == "" {
		t.Error("expected non-empty reason for all-tied")
	}
}
