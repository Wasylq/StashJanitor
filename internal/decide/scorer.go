// Package decide is the scoring engine that picks a keeper from a duplicate
// group of scenes (workflow A) or files within a scene (workflow B).
//
// The scoring chain is configurable: each rule yields a comparison result
// (-1, 0, 1) and the rules are evaluated in order until one breaks the tie.
// If every rule reports a tie, or if a configured "review policy" detects a
// case the user wanted to inspect manually, the group is marked
// needs_review and skipped during apply.
//
// The scorer is intentionally pure — it takes snapshot data (store types)
// and returns a Decision. No I/O, no Stash calls.
package decide

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// Decision is the outcome of scoring a duplicate group.
type Decision struct {
	// KeeperIndex is the index into the input slice of the chosen keeper,
	// or -1 if the group needs human review.
	KeeperIndex int

	// Reason is populated when KeeperIndex == -1, explaining why we
	// declined to auto-decide.
	Reason string
}

// SceneScorer ranks scenes within a workflow A duplicate group per the
// rule chain in cfg.Scoring.Scenes.Rules.
type SceneScorer struct {
	rules                       []sceneRule
	codecRank                   map[string]int // lower = better
	pathPriority                []string
	flagMetadataQualityConflict bool
	// filenameRegex is shared with FileScorer (compiled from
	// cfg.Scoring.Files.FilenameQuality.Pattern). The scene scorer does
	// NOT use it as a ranking rule — workflow A scoring stays driven by
	// the cfg.Scoring.Scenes.Rules chain — but it exposes ClassifyFilename
	// so the scan code can populate the FilenameQuality snapshot field
	// and run the "filename info loss" safety net.
	filenameRegex *regexp.Regexp
}

// NewSceneScorer compiles the rule chain from config. Returns an error if
// any rule name is unknown — typos in config should fail loudly at startup,
// not silently produce wrong decisions.
func NewSceneScorer(cfg *config.Config) (*SceneScorer, error) {
	s := &SceneScorer{
		codecRank:                   buildCodecRank(cfg.Scoring.CodecPriority),
		pathPriority:                cfg.Scoring.PathPriority,
		flagMetadataQualityConflict: cfg.ReviewPolicy.FlagMetadataQualityConflict,
	}
	if pat := cfg.Scoring.Files.FilenameQuality.Pattern; pat != "" {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("scoring.files.filename_quality.pattern: %w", err)
		}
		s.filenameRegex = re
	}
	for _, name := range cfg.Scoring.Scenes.Rules {
		rule, err := makeSceneRule(name)
		if err != nil {
			return nil, fmt.Errorf("scoring.scenes.rules: %w", err)
		}
		s.rules = append(s.rules, rule)
	}
	if len(s.rules) == 0 {
		return nil, fmt.Errorf("scoring.scenes.rules: no rules configured")
	}
	return s, nil
}

// ClassifyFilename returns 1 if basename matches the configured filename
// quality regex, 0 otherwise. Same semantics as FileScorer.ClassifyFilename
// — both scorers compile the same regex from the same config field.
func (s *SceneScorer) ClassifyFilename(basename string) int {
	if s.filenameRegex == nil {
		return 0
	}
	if s.filenameRegex.MatchString(basename) {
		return 1
	}
	return 0
}

// DecideScenes scores a duplicate group and picks the keeper.
//
// Inputs are read but not mutated; the caller is responsible for assigning
// roles to the returned indices afterward.
//
// Behavior:
//   - len(scenes) < 2: degenerate, returns KeeperIndex=0 (or -1 if empty).
//   - one scene strictly beats every other on the rule chain → KeeperIndex
//     = its position, Reason="".
//   - any two scenes tie on every rule → KeeperIndex=-1, Reason explains.
//   - metadata vs quality conflict (and review policy enabled) →
//     KeeperIndex=-1, Reason explains.
func (s *SceneScorer) DecideScenes(scenes []store.SceneGroupScene) Decision {
	switch len(scenes) {
	case 0:
		return Decision{KeeperIndex: -1, Reason: "empty group"}
	case 1:
		return Decision{KeeperIndex: 0}
	}

	best := 0
	for i := 1; i < len(scenes); i++ {
		if s.compare(&scenes[i], &scenes[best]) > 0 {
			best = i
		}
	}

	// Verify the best is strictly better than every other scene. If any
	// other scene ties on the entire rule chain, we cannot make an
	// unambiguous pick.
	for i := range scenes {
		if i == best {
			continue
		}
		if s.compare(&scenes[best], &scenes[i]) == 0 {
			return Decision{
				KeeperIndex: -1,
				Reason: fmt.Sprintf("tied on every rule: scenes %s and %s", scenes[best].SceneID, scenes[i].SceneID),
			}
		}
	}

	// Safety net: even when the rule chain produced a clear winner, the
	// user may have asked us to flag groups where the chosen keeper lacks
	// metadata that some loser has.
	if s.flagMetadataQualityConflict {
		if reason := detectMetadataQualityConflict(scenes, best); reason != "" {
			return Decision{KeeperIndex: -1, Reason: reason}
		}
	}

	return Decision{KeeperIndex: best}
}

// compare runs the configured rule chain. Returns 1 if a > b, -1 if a < b,
// 0 if every rule ties.
func (s *SceneScorer) compare(a, b *store.SceneGroupScene) int {
	for _, rule := range s.rules {
		if c := rule(s, a, b); c != 0 {
			return c
		}
	}
	return 0
}

// detectMetadataQualityConflict reports a non-empty reason when the chosen
// keeper has no stash_id but at least one loser does. This is the user's
// explicit "don't auto-decide the hard ones" guard. With the default rule
// chain (has_stash_id first) this case cannot arise — the metadata-rich
// scene always wins. The check exists for users who reorder the rules.
func detectMetadataQualityConflict(scenes []store.SceneGroupScene, keeperIdx int) string {
	keeper := &scenes[keeperIdx]
	if keeper.HasStashID {
		return ""
	}
	for i := range scenes {
		if i == keeperIdx {
			continue
		}
		if scenes[i].HasStashID {
			return fmt.Sprintf(
				"metadata vs quality conflict: keeper %s has no stash_id but loser %s does",
				keeper.SceneID, scenes[i].SceneID,
			)
		}
	}
	return ""
}

// ----- rule implementations -----

// sceneRule signature: returns 1 if a is preferred, -1 if b is preferred, 0
// if the rule sees them as tied.
type sceneRule func(s *SceneScorer, a, b *store.SceneGroupScene) int

func makeSceneRule(name string) (sceneRule, error) {
	switch name {
	case "has_stash_id":
		return ruleHasStashID, nil
	case "organized":
		return ruleOrganized, nil
	case "resolution":
		return ruleResolution, nil
	case "bitrate":
		return ruleBitrate, nil
	case "codec_preference":
		return ruleCodecPreference, nil
	case "file_size":
		return ruleFileSize, nil
	case "tag_count":
		return ruleTagCount, nil
	case "path_priority":
		return rulePathPriority, nil
	default:
		return nil, fmt.Errorf("unknown rule %q", name)
	}
}

func ruleHasStashID(_ *SceneScorer, a, b *store.SceneGroupScene) int {
	return cmpBool(a.HasStashID, b.HasStashID)
}

func ruleOrganized(_ *SceneScorer, a, b *store.SceneGroupScene) int {
	return cmpBool(a.Organized, b.Organized)
}

func ruleResolution(_ *SceneScorer, a, b *store.SceneGroupScene) int {
	return cmpInt(a.Width*a.Height, b.Width*b.Height)
}

func ruleBitrate(_ *SceneScorer, a, b *store.SceneGroupScene) int {
	return cmpInt(a.Bitrate, b.Bitrate)
}

func ruleCodecPreference(s *SceneScorer, a, b *store.SceneGroupScene) int {
	aRank, aOK := s.codecRank[normalizeCodec(a.Codec)]
	bRank, bOK := s.codecRank[normalizeCodec(b.Codec)]
	switch {
	case !aOK && !bOK:
		return 0
	case !aOK:
		return -1
	case !bOK:
		return 1
	}
	// Lower rank = more preferred → invert for "higher score wins".
	return cmpInt(bRank, aRank)
}

func ruleFileSize(_ *SceneScorer, a, b *store.SceneGroupScene) int {
	return cmpInt64(a.FileSize, b.FileSize)
}

func ruleTagCount(_ *SceneScorer, a, b *store.SceneGroupScene) int {
	return cmpInt(a.TagCount, b.TagCount)
}

func rulePathPriority(s *SceneScorer, a, b *store.SceneGroupScene) int {
	aRank := pathRank(a.PrimaryPath, s.pathPriority)
	bRank := pathRank(b.PrimaryPath, s.pathPriority)
	switch {
	case aRank == -1 && bRank == -1:
		return 0
	case aRank == -1:
		return -1
	case bRank == -1:
		return 1
	}
	return cmpInt(bRank, aRank)
}

// ----- helpers -----

func cmpInt(a, b int) int {
	switch {
	case a > b:
		return 1
	case a < b:
		return -1
	default:
		return 0
	}
}

func cmpInt64(a, b int64) int {
	switch {
	case a > b:
		return 1
	case a < b:
		return -1
	default:
		return 0
	}
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

// normalizeCodec maps Stash's various codec strings to the canonical names
// used in the codec_priority list. Stash reports things like "h264", "hevc",
// "mpeg4", "av1"; we lower-case and strip dots so "h.264" matches "h264" and
// "x264" maps to itself (caller can list it explicitly if they care).
func normalizeCodec(c string) string {
	c = strings.ToLower(c)
	c = strings.ReplaceAll(c, ".", "")
	c = strings.ReplaceAll(c, "-", "")
	// Common Stash aliases:
	switch c {
	case "h265":
		return "hevc"
	}
	return c
}

// buildCodecRank builds a map from codec name to its rank (0 = best). The
// caller's slice is normalized through normalizeCodec on insertion.
func buildCodecRank(priority []string) map[string]int {
	out := make(map[string]int, len(priority))
	for i, c := range priority {
		out[normalizeCodec(c)] = i
	}
	return out
}

// pathRank returns the index of the first matching prefix in priority, or
// -1 if path doesn't start with any of them.
func pathRank(path string, priority []string) int {
	for i, prefix := range priority {
		if strings.HasPrefix(path, prefix) {
			return i
		}
	}
	return -1
}
