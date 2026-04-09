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
	"github.com/Wasylq/StashJanitor/internal/confirm"
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
	// rules and ruleNames are parallel slices so ExplainPick can name the
	// rule that decided a particular comparison without changing how the
	// existing compare/DecideScenes paths walk the chain.
	rules                       []sceneRule
	ruleNames                   []string
	codecRank                   map[string]int // lower = better
	pathPriority                []string
	flagMetadataQualityConflict bool
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
	for _, name := range cfg.Scoring.Scenes.Rules {
		rule, err := makeSceneRule(name)
		if err != nil {
			return nil, fmt.Errorf("scoring.scenes.rules: %w", err)
		}
		s.rules = append(s.rules, rule)
		s.ruleNames = append(s.ruleNames, name)
	}
	if len(s.rules) == 0 {
		return nil, fmt.Errorf("scoring.scenes.rules: no rules configured")
	}
	return s, nil
}

// ExplainPick walks the rule chain and returns a human-readable string
// describing the FIRST rule that decided winner > loser. Used by the
// report to show "why was this kept?" annotations under each loser line.
//
// Returns an empty string when winner and loser tie on every rule
// (shouldn't happen in a decided group, but the caller is expected to
// no-op on empty).
func (s *SceneScorer) ExplainPick(winner, loser *store.SceneGroupScene) string {
	for i, rule := range s.rules {
		switch rule(s, winner, loser) {
		case 1:
			return formatRuleExplanation(s.ruleNames[i], winner, loser)
		case -1:
			// Defensive — winner should be strictly better than loser in
			// any decided group. If we hit this it's a bug, not user data.
			return fmt.Sprintf("(unexpected: keeper lost on %s)", s.ruleNames[i])
		}
	}
	return ""
}

// formatRuleExplanation turns a rule name + the two snapshots into a brief
// "why" line for the report. Hardcoded per-rule because each rule has its
// own natural value formatting.
func formatRuleExplanation(rule string, winner, loser *store.SceneGroupScene) string {
	switch rule {
	case "has_stash_id":
		return "has stash-box metadata (loser has none)"
	case "organized":
		return "is organized in Stash (loser is not)"
	case "resolution":
		return fmt.Sprintf("higher resolution: %dx%d vs %dx%d",
			winner.Width, winner.Height, loser.Width, loser.Height)
	case "bitrate":
		return fmt.Sprintf("higher bitrate: %s vs %s",
			formatBitrate(winner.Bitrate), formatBitrate(loser.Bitrate))
	case "codec_preference":
		return fmt.Sprintf("preferred codec: %s vs %s",
			codecOrUnknown(winner.Codec), codecOrUnknown(loser.Codec))
	case "file_size":
		return fmt.Sprintf("larger file: %s vs %s",
			confirm.HumanBytes(winner.FileSize), confirm.HumanBytes(loser.FileSize))
	case "tag_count":
		return fmt.Sprintf("more tags: %d vs %d", winner.TagCount, loser.TagCount)
	case "path_priority":
		return fmt.Sprintf("preferred path: %s vs %s", winner.PrimaryPath, loser.PrimaryPath)
	}
	return rule
}

func formatBitrate(bps int) string {
	switch {
	case bps >= 1_000_000:
		return fmt.Sprintf("%.1f Mbps", float64(bps)/1_000_000)
	case bps >= 1_000:
		return fmt.Sprintf("%d kbps", bps/1_000)
	case bps > 0:
		return fmt.Sprintf("%d bps", bps)
	default:
		return "unknown"
	}
}

func codecOrUnknown(c string) string {
	if c == "" {
		return "unknown"
	}
	return c
}

// looseDateRegex matches a YYYY-MM-DD style date anywhere in a string.
// Tolerates `-`, `_`, and `.` as separators between the components.
var looseDateRegex = regexp.MustCompile(`\d{4}[-._]\d{2}[-._]\d{2}`)

// ClassifyFilename returns 1 if basename appears to encode meaningful
// information that would be lost if the file were deleted, 0 otherwise.
//
// This is INTENTIONALLY different from FileScorer.ClassifyFilename:
//
//   - FileScorer asks "does this match my strict convention?" — used to
//     PICK the best file in workflow B's byte-equivalent file group.
//   - SceneScorer asks "does this contain ANY structured info?" — used by
//     the workflow A safety net to detect info-loss risk before auto-
//     merging two cross-scene duplicates.
//
// The safety net needs the looser check because the keeper and the loser
// might use different naming conventions (e.g. keeper has emoji-decorated
// title, loser has `<performer>_-_<date>_<title>` instead of
// `<date>_<performer>-<title>`). A strict convention regex would miss the
// info-loss risk in those cases.
//
// Currently the loose check is "contains a YYYY-MM-DD style date". This is
// a robust signal because dates are unambiguous and rarely appear in
// random/junk filenames.
func (s *SceneScorer) ClassifyFilename(basename string) int {
	if looseDateRegex.MatchString(basename) {
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
