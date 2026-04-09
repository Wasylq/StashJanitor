package apply

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Wasylq/StashJanitor/internal/stash"
)

// resolutionTokenRegex matches the trailing resolution segment of a
// "structured" filename, e.g. `_1080p.mp4`, `_4k_1.mp4`, `_2160p.mkv`.
//
// The match captures the entire `_<resolution>p?(_<digits>)?` chunk so
// rebuildBasenameForFile can replace it cleanly.
var resolutionTokenRegex = regexp.MustCompile(`_(?:480|540|720|1080|1440|2160|2880|4320|[2468][kK])p?(?:_\d+)?(?:\.[A-Za-z0-9]+)?$`)

// rebuildBasenameForFile takes a structured basename (one that contains a
// recognizable resolution token) plus the winner file's metadata, and
// returns a new basename derived from the structured one but with:
//
//   - the resolution token swapped to match winner.Height
//   - the extension swapped to match the winner's extension
//
// Example:
//
//	rebuildBasenameForFile(
//	  "2024-12-15_Performer-Title_1080p.mp4",
//	  &VideoFile{Height: 2160, Basename: "garbage.mkv"},
//	) → "2024-12-15_Performer-Title_4k.mkv"
//
// If the structured basename doesn't have a recognizable resolution token,
// the function appends one (so callers can safely pass any basename and
// trust the result).
func rebuildBasenameForFile(structuredBasename string, winner *stash.VideoFile) string {
	if winner == nil {
		return structuredBasename
	}

	winnerExt := strings.ToLower(filepath.Ext(winner.Basename))
	if winnerExt == "" {
		// Defensive — fall back to .mp4 if the winner has no extension.
		winnerExt = ".mp4"
	}

	winnerToken := heightToResolutionToken(winner.Height)
	if winnerToken == "" {
		// Unknown resolution — strip the structured basename's resolution
		// token and just keep the descriptive part with the winner's ext.
		stem := stripResolutionToken(structuredBasename)
		return strings.TrimSuffix(stem, filepath.Ext(stem)) + winnerExt
	}

	// Try to substitute the resolution token in place. If the structured
	// basename has no token, append one.
	if resolutionTokenRegex.MatchString(structuredBasename) {
		return resolutionTokenRegex.ReplaceAllString(structuredBasename, "_"+winnerToken+winnerExt)
	}
	stem := strings.TrimSuffix(structuredBasename, filepath.Ext(structuredBasename))
	return stem + "_" + winnerToken + winnerExt
}

// stripResolutionToken removes a trailing _<resolution>[_digit] chunk from
// a basename if present, keeping the original extension. Used as a fallback
// when the winner's height doesn't map to a known token.
func stripResolutionToken(basename string) string {
	ext := filepath.Ext(basename)
	stem := strings.TrimSuffix(basename, ext)
	// Find the resolution chunk in the stem (without the extension).
	re := regexp.MustCompile(`_(?:480|540|720|1080|1440|2160|2880|4320|[2468][kK])p?(?:_\d+)?$`)
	stem = re.ReplaceAllString(stem, "")
	return stem + ext
}

// heightToResolutionToken maps a video height in pixels to the human
// resolution token most commonly used in filenames. Boundaries are inclusive
// at the top — i.e. a 1080p file (height=1080) maps to "1080p", and a 2160p
// file (height=2160) maps to "4k".
//
// Returns "" for heights below 360 or unrecognizable inputs.
func heightToResolutionToken(h int) string {
	switch {
	case h <= 0:
		return ""
	case h <= 480:
		return "480p"
	case h <= 540:
		return "540p"
	case h <= 720:
		return "720p"
	case h <= 1080:
		return "1080p"
	case h <= 1440:
		return "1440p"
	case h <= 2160:
		return "4k"
	case h <= 2880:
		return "2880p"
	case h <= 4320:
		return "8k"
	default:
		return "8k"
	}
}
