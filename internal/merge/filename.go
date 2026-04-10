package merge

import (
	"regexp"
	"strings"

	"github.com/Wasylq/StashJanitor/internal/stash"
)

// defaultFilenameMetadataPattern matches the user's convention:
//
//	YYYY-MM-DD_Performer.Name1[_Performer2]-Title.With.Dots_Resolution.ext
//
// Named groups:
//
//	date        — the ISO-ish date prefix
//	performers  — dot-separated names, underscore-separated people (may be empty)
//	title       — everything between the first dash and the resolution token
//
// The resolution and extension parts are matched but not captured as named
// groups — we only care about date/performers/title for metadata extraction.
var defaultFilenameMetadataPattern = regexp.MustCompile(
	`^(?P<date>\d{4}[-._]\d{2}[-._]\d{2})_(?P<performers>.*?)-(?P<title>.+)_` +
		`(?:480|540|720|1080|1440|2160|2880|4320|[2468][kK])p?(?:_\d+)?` +
		`\.[A-Za-z0-9]+$`,
)

// FilenameMetadata is the result of parsing a structured filename.
type FilenameMetadata struct {
	Date       string   // "2024-12-15" — normalized to YYYY-MM-DD
	Title      string   // "Your Work Crush has a Girl Cock"
	Performers []string // ["Aimee Waves"]
}

// ParseFilenameMetadata attempts to extract date, title, and performer
// names from a structured basename. Returns nil when the filename doesn't
// match the convention at all.
//
// The regex is compiled once at package init (defaultFilenameMetadataPattern).
// A future version could accept a user-supplied regex via config.
func ParseFilenameMetadata(basename string) *FilenameMetadata {
	return parseWithPattern(defaultFilenameMetadataPattern, basename)
}

func parseWithPattern(re *regexp.Regexp, basename string) *FilenameMetadata {
	match := re.FindStringSubmatch(basename)
	if match == nil {
		return nil
	}

	// Build a name→value map from the named groups.
	groups := map[string]string{}
	for i, name := range re.SubexpNames() {
		if name != "" && i < len(match) {
			groups[name] = match[i]
		}
	}

	md := &FilenameMetadata{}

	// Date: normalize separators to dashes.
	if d := groups["date"]; d != "" {
		d = strings.ReplaceAll(d, "_", "-")
		d = strings.ReplaceAll(d, ".", "-")
		md.Date = d
	}

	// Title: dots → spaces, trim.
	if t := groups["title"]; t != "" {
		t = strings.ReplaceAll(t, ".", " ")
		md.Title = strings.TrimSpace(t)
	}

	// Performers: split by underscore, dots → spaces within each name.
	if p := groups["performers"]; p != "" {
		for _, raw := range strings.Split(p, "_") {
			name := strings.TrimSpace(strings.ReplaceAll(raw, ".", " "))
			if name != "" {
				md.Performers = append(md.Performers, name)
			}
		}
	}

	return md
}

// ExtractMetadataFromFiles tries to parse metadata from any of the given
// files' basenames. Returns the first successful parse, or nil if none
// match. Used by the merge pipeline as a last-resort metadata source when
// neither the keeper nor the losers have the field set in Stash.
func ExtractMetadataFromFiles(files []stash.VideoFile) *FilenameMetadata {
	for _, f := range files {
		if md := ParseFilenameMetadata(f.Basename); md != nil {
			return md
		}
	}
	return nil
}
