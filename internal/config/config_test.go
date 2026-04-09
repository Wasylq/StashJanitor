package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedDefaultsParse is the most important test in this package: it
// catches the case where someone edits default.yaml in a way the loader can't
// parse. The embedded defaults must always be loadable.
func TestEmbeddedDefaultsParse(t *testing.T) {
	cfg, err := Load("/this/file/does/not/exist")
	if err != nil {
		t.Fatalf("Load with no user file should succeed using embedded defaults, got: %v", err)
	}

	// Spot-check a handful of fields to make sure the YAML actually populated
	// the struct rather than leaving everything zero.
	if cfg.Stash.URL == "" {
		t.Error("expected Stash.URL to be populated from defaults")
	}
	if cfg.Scan.PhashDistance != 4 {
		t.Errorf("expected default phash_distance=4, got %d", cfg.Scan.PhashDistance)
	}
	if cfg.Scan.DurationDiffSeconds != 1.0 {
		t.Errorf("expected default duration_diff_seconds=1.0, got %v", cfg.Scan.DurationDiffSeconds)
	}
	if got, want := cfg.Apply.Scenes.DefaultAction, "tag"; got != want {
		t.Errorf("expected default apply.scenes.default_action=%q, got %q", want, got)
	}
	if got, want := cfg.Apply.Files.DefaultAction, "report"; got != want {
		t.Errorf("expected default apply.files.default_action=%q, got %q", want, got)
	}
	if !cfg.ReviewPolicy.FlagMetadataQualityConflict {
		t.Error("expected flag_metadata_quality_conflict to default to true")
	}
	if !cfg.Merge.PostMergeFileCleanup.Enabled {
		t.Error("expected merge.post_merge_file_cleanup.enabled to default to true")
	}
	if cfg.Scoring.Files.FilenameQuality.Pattern == "" {
		t.Error("expected scoring.files.filename_quality.pattern to default to a non-empty regex")
	}
	if len(cfg.Scoring.Scenes.Rules) == 0 {
		t.Error("expected scoring.scenes.rules to be populated by defaults")
	}
	if len(cfg.Scoring.Files.Rules) == 0 {
		t.Error("expected scoring.files.rules to be populated by defaults")
	}
	if got, want := cfg.Scoring.Files.Rules[0], "filename_quality"; got != want {
		t.Errorf("expected first file rule to be %q, got %q", want, got)
	}
}

// TestUserOverridesMergeOverDefaults verifies that user values replace
// defaults at the field level. yaml.v3 merges struct fields key-by-key, so a
// user file that only specifies stash.url should still inherit every other
// field from defaults.
func TestUserOverridesMergeOverDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	userYAML := `
stash:
  url: http://example.test:9999
scan:
  phash_distance: 8
`
	if err := os.WriteFile(path, []byte(userYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Stash.URL, "http://example.test:9999"; got != want {
		t.Errorf("Stash.URL = %q, want %q", got, want)
	}
	if got, want := cfg.Scan.PhashDistance, 8; got != want {
		t.Errorf("Scan.PhashDistance = %d, want %d", got, want)
	}
	// Unspecified field — must come from defaults.
	if got, want := cfg.Scan.DurationDiffSeconds, 1.0; got != want {
		t.Errorf("Scan.DurationDiffSeconds = %v, want %v (default)", got, want)
	}
	// Unspecified slice — must come from defaults.
	if len(cfg.Scoring.Scenes.Rules) == 0 {
		t.Error("Scoring.Scenes.Rules should still be populated from defaults")
	}
}

// TestUserSliceReplacesWholesale documents (and locks in) the yaml.v3
// behavior that user-provided slices REPLACE defaults rather than appending.
// This is the desired "I want exactly this list" semantics.
func TestUserSliceReplacesWholesale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	userYAML := `
stash:
  url: http://example.test:9999
scoring:
  codec_priority:
    - h264
`
	if err := os.WriteFile(path, []byte(userYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Scoring.CodecPriority, []string{"h264"}; len(got) != len(want) || got[0] != want[0] {
		t.Errorf("CodecPriority = %v, want %v (user list should replace, not append)", got, want)
	}
}

// TestValidateRejectsBadAction makes sure typos in apply.scenes.default_action
// are caught at load time, not silently ignored.
func TestValidateRejectsBadAction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	userYAML := `
stash:
  url: http://example.test:9999
apply:
  scenes:
    default_action: tagg
`
	if err := os.WriteFile(path, []byte(userYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for typo'd default_action, got nil")
	}
	if !strings.Contains(err.Error(), "default_action") {
		t.Errorf("expected error to mention default_action, got: %v", err)
	}
}

// TestStashAPIKey verifies the env-var indirection. We never want the API
// key serialized into the config struct.
func TestStashAPIKey(t *testing.T) {
	t.Setenv("SFX_TEST_STASH_KEY", "abc123")
	cfg := &Config{Stash: Stash{APIKeyEnv: "SFX_TEST_STASH_KEY"}}
	if got, want := cfg.StashAPIKey(), "abc123"; got != want {
		t.Errorf("StashAPIKey() = %q, want %q", got, want)
	}

	cfg.Stash.APIKeyEnv = ""
	if got := cfg.StashAPIKey(); got != "" {
		t.Errorf("StashAPIKey() with empty APIKeyEnv = %q, want empty", got)
	}

	cfg.Stash.APIKeyEnv = "SFX_TEST_NONEXISTENT_KEY"
	if got := cfg.StashAPIKey(); got != "" {
		t.Errorf("StashAPIKey() with unset env var = %q, want empty", got)
	}
}

// TestWriteDefaultRefusesOverwrite makes sure `stash-janitor config init` cannot
// silently clobber an existing config.
func TestWriteDefaultRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("# existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteDefault(path); err == nil {
		t.Fatal("expected WriteDefault to refuse overwriting an existing file")
	}
}

func TestWriteDefaultCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := WriteDefault(path); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The file should be byte-identical to the embedded YAML, comments and all.
	if string(data) != string(DefaultYAML()) {
		t.Error("written file does not match embedded default YAML")
	}
}
