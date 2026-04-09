package decide

import (
	"strings"
	"testing"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// defaultScorer builds a SceneScorer from the embedded default config —
// the same scoring chain a fresh user gets out of the box.
func defaultScorer(t *testing.T) *SceneScorer {
	t.Helper()
	cfg, err := config.Load("/this/file/does/not/exist") // forces defaults
	if err != nil {
		t.Fatalf("loading defaults: %v", err)
	}
	s, err := NewSceneScorer(cfg)
	if err != nil {
		t.Fatalf("NewSceneScorer: %v", err)
	}
	return s
}

func TestNewSceneScorerRejectsUnknownRule(t *testing.T) {
	cfg, _ := config.Load("/missing")
	cfg.Scoring.Scenes.Rules = []string{"has_stash_id", "frobnication"}
	_, err := NewSceneScorer(cfg)
	if err == nil {
		t.Fatal("expected error for unknown rule, got nil")
	}
	if !strings.Contains(err.Error(), "frobnication") {
		t.Errorf("expected error to mention bad rule, got: %v", err)
	}
}

func TestDecideScenesPrefersStashID(t *testing.T) {
	s := defaultScorer(t)
	scenes := []store.SceneGroupScene{
		{SceneID: "1", HasStashID: false, Width: 3840, Height: 2160}, // 4K but no metadata
		{SceneID: "2", HasStashID: true, Width: 1280, Height: 720},   // 720p with metadata
	}
	d := s.DecideScenes(scenes)
	if d.KeeperIndex != 1 {
		t.Errorf("KeeperIndex = %d, want 1 (the one with stash_id)", d.KeeperIndex)
	}
	if d.Reason != "" {
		t.Errorf("expected empty reason for clean win, got %q", d.Reason)
	}
}

func TestDecideScenesPrefersResolutionWhenMetadataEqual(t *testing.T) {
	s := defaultScorer(t)
	scenes := []store.SceneGroupScene{
		{SceneID: "1", HasStashID: true, Organized: true, Width: 1280, Height: 720, Bitrate: 4_000_000, Codec: "h264"},
		{SceneID: "2", HasStashID: true, Organized: true, Width: 1920, Height: 1080, Bitrate: 4_000_000, Codec: "h264"},
	}
	d := s.DecideScenes(scenes)
	if d.KeeperIndex != 1 {
		t.Errorf("KeeperIndex = %d, want 1 (1080p)", d.KeeperIndex)
	}
}

func TestDecideScenesPrefersBitrateWhenResolutionEqual(t *testing.T) {
	s := defaultScorer(t)
	scenes := []store.SceneGroupScene{
		{SceneID: "1", HasStashID: true, Organized: true, Width: 1920, Height: 1080, Bitrate: 4_000_000, Codec: "h264"},
		{SceneID: "2", HasStashID: true, Organized: true, Width: 1920, Height: 1080, Bitrate: 8_000_000, Codec: "h264"},
	}
	d := s.DecideScenes(scenes)
	if d.KeeperIndex != 1 {
		t.Errorf("KeeperIndex = %d, want 1 (higher bitrate)", d.KeeperIndex)
	}
}

func TestDecideScenesCodecPriority(t *testing.T) {
	s := defaultScorer(t)
	// Same metadata, resolution, bitrate — codec preference (av1 > hevc > h264) decides.
	scenes := []store.SceneGroupScene{
		{SceneID: "1", HasStashID: true, Organized: true, Width: 1920, Height: 1080, Bitrate: 4_000_000, Codec: "h264"},
		{SceneID: "2", HasStashID: true, Organized: true, Width: 1920, Height: 1080, Bitrate: 4_000_000, Codec: "hevc"},
	}
	d := s.DecideScenes(scenes)
	if d.KeeperIndex != 1 {
		t.Errorf("KeeperIndex = %d, want 1 (hevc beats h264)", d.KeeperIndex)
	}
}

func TestDecideScenesCodecAliasH265(t *testing.T) {
	s := defaultScorer(t)
	scenes := []store.SceneGroupScene{
		{SceneID: "1", HasStashID: true, Organized: true, Width: 1920, Height: 1080, Bitrate: 4_000_000, Codec: "h264"},
		{SceneID: "2", HasStashID: true, Organized: true, Width: 1920, Height: 1080, Bitrate: 4_000_000, Codec: "h265"},
	}
	d := s.DecideScenes(scenes)
	if d.KeeperIndex != 1 {
		t.Errorf("KeeperIndex = %d, want 1 (h265 normalized to hevc beats h264)", d.KeeperIndex)
	}
}

func TestDecideScenesAllTiedNeedsReview(t *testing.T) {
	s := defaultScorer(t)
	// Two scenes, identical on every rule.
	scenes := []store.SceneGroupScene{
		{SceneID: "1", HasStashID: true, Organized: true, Width: 1920, Height: 1080, Bitrate: 4_000_000, Codec: "h264", FileSize: 1_000_000_000, TagCount: 5, PrimaryPath: "/sorted/a.mp4"},
		{SceneID: "2", HasStashID: true, Organized: true, Width: 1920, Height: 1080, Bitrate: 4_000_000, Codec: "h264", FileSize: 1_000_000_000, TagCount: 5, PrimaryPath: "/sorted/b.mp4"},
	}
	d := s.DecideScenes(scenes)
	if d.KeeperIndex != -1 {
		t.Errorf("KeeperIndex = %d, want -1 (all tied)", d.KeeperIndex)
	}
	if d.Reason == "" {
		t.Error("expected non-empty reason for all-tied")
	}
}

func TestDecideScenesPathPriority(t *testing.T) {
	s := defaultScorer(t)
	// Identical except for path; /sorted should beat /inbox per default config.
	scenes := []store.SceneGroupScene{
		{SceneID: "1", HasStashID: true, Organized: true, Width: 1920, Height: 1080, Bitrate: 4_000_000, Codec: "h264", FileSize: 1_000_000_000, TagCount: 5, PrimaryPath: "/inbox/a.mp4"},
		{SceneID: "2", HasStashID: true, Organized: true, Width: 1920, Height: 1080, Bitrate: 4_000_000, Codec: "h264", FileSize: 1_000_000_000, TagCount: 5, PrimaryPath: "/sorted/b.mp4"},
	}
	d := s.DecideScenes(scenes)
	if d.KeeperIndex != 1 {
		t.Errorf("KeeperIndex = %d, want 1 (/sorted beats /inbox)", d.KeeperIndex)
	}
}

func TestDecideScenesEmptyOrSingle(t *testing.T) {
	s := defaultScorer(t)
	if d := s.DecideScenes(nil); d.KeeperIndex != -1 {
		t.Errorf("nil group: KeeperIndex = %d, want -1", d.KeeperIndex)
	}
	if d := s.DecideScenes([]store.SceneGroupScene{{SceneID: "1"}}); d.KeeperIndex != 0 {
		t.Errorf("singleton: KeeperIndex = %d, want 0", d.KeeperIndex)
	}
}

func TestMetadataQualityConflictDetection(t *testing.T) {
	cfg, _ := config.Load("/missing")
	// Force the rule chain to put resolution FIRST so the conflict check
	// has a chance to fire (default rules with has_stash_id first would
	// auto-pick the metadata-rich scene).
	cfg.Scoring.Scenes.Rules = []string{"resolution", "has_stash_id"}
	s, err := NewSceneScorer(cfg)
	if err != nil {
		t.Fatal(err)
	}

	scenes := []store.SceneGroupScene{
		{SceneID: "loQual", HasStashID: true, Width: 1280, Height: 720},  // metadata, lower res
		{SceneID: "hiQual", HasStashID: false, Width: 3840, Height: 2160}, // no metadata, higher res
	}
	d := s.DecideScenes(scenes)
	if d.KeeperIndex != -1 {
		t.Errorf("expected needs_review for metadata vs quality conflict, got KeeperIndex=%d", d.KeeperIndex)
	}
	if !strings.Contains(d.Reason, "metadata vs quality") {
		t.Errorf("reason = %q, want it to mention 'metadata vs quality'", d.Reason)
	}
}

func TestMetadataQualityConflictNotFiringWithDefaults(t *testing.T) {
	// With default rules (has_stash_id first), the metadata-rich scene
	// always wins so the conflict policy never fires — even though it's
	// enabled in the default config.
	s := defaultScorer(t)
	scenes := []store.SceneGroupScene{
		{SceneID: "loQual", HasStashID: true, Width: 1280, Height: 720},
		{SceneID: "hiQual", HasStashID: false, Width: 3840, Height: 2160},
	}
	d := s.DecideScenes(scenes)
	if d.KeeperIndex != 0 {
		t.Errorf("expected KeeperIndex=0 (metadata wins) with default rules, got %d (reason: %q)", d.KeeperIndex, d.Reason)
	}
}
