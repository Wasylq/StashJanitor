// Package config defines the on-disk YAML schema for stash-janitor and the loader
// that resolves it.
//
// The defaults live in default.yaml (embedded into the binary) and are also
// what `stash-janitor config init` writes to disk. Loading user config is implemented
// as: parse defaults, then unmarshal the user file on top of the same struct.
// yaml.v3 merges struct fields key-by-key, so any field the user omits keeps
// its default. Slices and maps are replaced wholesale by user values, which
// is the desired "I want exactly this list" behavior.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

//go:embed default.yaml
var defaultYAML []byte

// DefaultYAML returns the embedded default config as raw bytes. Used by
// `stash-janitor config init` so the comments are preserved verbatim.
func DefaultYAML() []byte {
	out := make([]byte, len(defaultYAML))
	copy(out, defaultYAML)
	return out
}

// Config is the root configuration object.
type Config struct {
	Stash                Stash                `yaml:"stash"`
	Scan                 Scan                 `yaml:"scan"`
	Scoring              Scoring              `yaml:"scoring"`
	ReviewPolicy         ReviewPolicy         `yaml:"review_policy"`
	Apply                Apply                `yaml:"apply"`
	StashBoxFingerprints StashBoxFingerprints `yaml:"stash_box_fingerprints"`
	Merge                Merge                `yaml:"merge"`
}

type Stash struct {
	URL        string `yaml:"url"`
	APIKeyEnv  string `yaml:"api_key_env"`
}

type Scan struct {
	PhashDistance       int     `yaml:"phash_distance"`
	DurationDiffSeconds float64 `yaml:"duration_diff_seconds"`
}

type Scoring struct {
	Scenes        ScoringScenes `yaml:"scenes"`
	Files         ScoringFiles  `yaml:"files"`
	CodecPriority []string      `yaml:"codec_priority"`
	PathPriority  []string      `yaml:"path_priority"`
}

type ScoringScenes struct {
	Rules []string `yaml:"rules"`
}

type ScoringFiles struct {
	Rules           []string        `yaml:"rules"`
	FilenameQuality FilenameQuality `yaml:"filename_quality"`
}

// FilenameQuality holds the regex pattern used to score files by filename.
// The pattern is stored as a string here; the scoring engine compiles it
// once at startup.
type FilenameQuality struct {
	Pattern string `yaml:"pattern"`
}

type ReviewPolicy struct {
	FlagMetadataQualityConflict bool `yaml:"flag_metadata_quality_conflict"`
}

type Apply struct {
	Scenes ApplyScenes `yaml:"scenes"`
	Files  ApplyFiles  `yaml:"files"`
}

type ApplyScenes struct {
	DefaultAction string `yaml:"default_action"`
	LoserTag      string `yaml:"loser_tag"`
	KeeperTag     string `yaml:"keeper_tag"`
}

type ApplyFiles struct {
	DefaultAction string `yaml:"default_action"`
}

type StashBoxFingerprints struct {
	Enabled bool `yaml:"enabled"`
}

type Merge struct {
	SceneLevel            map[string]string     `yaml:"scene_level"`
	History               MergeHistory          `yaml:"history"`
	PostMergeFileCleanup  PostMergeFileCleanup  `yaml:"post_merge_file_cleanup"`
}

type MergeHistory struct {
	PlayHistory bool `yaml:"play_history"`
	OHistory    bool `yaml:"o_history"`
}

type PostMergeFileCleanup struct {
	Enabled bool `yaml:"enabled"`
}

// Load reads the user's config file (if it exists) and merges it on top of
// the embedded defaults. A missing file is NOT an error — the embedded
// defaults are returned as-is, so first-time users can run sub-commands
// before they bother with `stash-janitor config init`.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if err := yaml.Unmarshal(defaultYAML, cfg); err != nil {
		return nil, fmt.Errorf("parsing embedded default config (this is a bug): %w", err)
	}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// fall through with embedded defaults
	default:
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks that the loaded config is internally consistent enough to
// run. It is intentionally permissive — most fields have defaults — but it
// catches the cases where a typo would silently cause weird behavior.
func (c *Config) Validate() error {
	if c.Stash.URL == "" {
		return errors.New("config: stash.url must not be empty")
	}
	if c.Scan.PhashDistance < 0 {
		return fmt.Errorf("config: scan.phash_distance must be >= 0, got %d", c.Scan.PhashDistance)
	}
	if c.Scan.DurationDiffSeconds < 0 {
		return fmt.Errorf("config: scan.duration_diff_seconds must be >= 0, got %v", c.Scan.DurationDiffSeconds)
	}
	switch c.Apply.Scenes.DefaultAction {
	case "tag", "merge", "delete":
	default:
		return fmt.Errorf("config: apply.scenes.default_action must be tag|merge|delete, got %q", c.Apply.Scenes.DefaultAction)
	}
	if c.Apply.Files.DefaultAction != "report" && c.Apply.Files.DefaultAction != "commit" {
		return fmt.Errorf("config: apply.files.default_action must be report|commit, got %q", c.Apply.Files.DefaultAction)
	}
	return nil
}

// StashAPIKey reads the API key from the env var named in stash.api_key_env.
// Returns an empty string if the env var is unset — that's a valid state for
// Stash instances without API keys configured.
//
// The key is intentionally NOT cached on the Config struct so it never gets
// serialized or printed if a caller logs the config.
func (c *Config) StashAPIKey() string {
	if c.Stash.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.Stash.APIKeyEnv)
}

// WriteDefault writes the embedded default config to path. Returns an error
// if the file already exists, to avoid clobbering a customized config.
func WriteDefault(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(defaultYAML); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// MarshalYAML serializes the loaded config back to YAML for display via
// `stash-janitor config show`. This deliberately does NOT preserve comments — it's
// the *effective* config after merging defaults and user overrides.
func MarshalYAML(c *Config) ([]byte, error) {
	return yaml.Marshal(c)
}
