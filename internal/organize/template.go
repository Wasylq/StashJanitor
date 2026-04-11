// Package organize computes ideal file paths from Stash scene metadata
// and a configurable template, then proposes moves via Stash's moveFiles.
package organize

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/stash"
)

// maxPathLen is the maximum total path length we'll produce. Paths longer
// than this get truncated with a _{id} suffix for uniqueness.
const maxPathLen = 240 // leave room for the _{id}.ext suffix

// ComputeTargetPath applies the path template to a scene's metadata and
// returns the full target path (base_dir + rendered template). Returns ""
// and a reason if the scene lacks required fields.
func ComputeTargetPath(scene *stash.Scene, file *stash.VideoFile, cfg *config.OrganizeConfig) (string, string) {
	vars := extractVars(scene, file, cfg)

	// Check required fields.
	for _, field := range cfg.RequiredFields {
		if vars[field] == "" {
			return "", "missing required field: " + field
		}
	}

	rendered := renderTemplate(cfg.PathTemplate, vars, cfg)
	if rendered == "" {
		return "", "template rendered to empty string"
	}

	fullPath := filepath.Join(cfg.BaseDir, rendered)

	// Enforce path length limit to avoid filesystem errors.
	fullPath = truncatePath(fullPath, scene.ID)

	return fullPath, ""
}

// extractVars builds the {variable} → value map from scene metadata.
func extractVars(scene *stash.Scene, file *stash.VideoFile, cfg *config.OrganizeConfig) map[string]string {
	vars := map[string]string{
		"id":         scene.ID,
		"title":      scene.Title,
		"date":       scene.Date,
		"year":       "",
		"performer":  "",
		"studio":     "",
		"resolution": "",
		"ext":        "",
	}

	if len(scene.Date) >= 4 {
		vars["year"] = scene.Date[:4]
	}

	if scene.Studio != nil && scene.Studio.Name != "" {
		vars["studio"] = scene.Studio.Name
	}

	// Performer selection.
	vars["performer"] = pickPerformer(scene.Performers, cfg.PerformerStrategy)

	if file != nil {
		vars["resolution"] = heightToToken(file.Height)
		ext := strings.TrimPrefix(filepath.Ext(file.Basename), ".")
		if ext == "" {
			ext = "mp4"
		}
		vars["ext"] = ext
	}

	return vars
}

// pickPerformer selects the "primary" performer from the scene's list.
func pickPerformer(performers []stash.Performer, strategy string) string {
	if len(performers) == 0 {
		return ""
	}
	switch strategy {
	case "first_listed":
		return performers[0].Name
	case "first_alphabetical":
		names := make([]string, len(performers))
		for i, p := range performers {
			names[i] = p.Name
		}
		sort.Strings(names)
		return names[0]
	default:
		return performers[0].Name
	}
}

// renderTemplate substitutes {variables} in the template string. The
// folder part and filename part get different space-char treatment.
func renderTemplate(tmpl string, vars map[string]string, cfg *config.OrganizeConfig) string {
	// Split into directory and filename at the last separator.
	dir, base := filepath.Split(tmpl)

	// Render the filename part (spaces → space_char, typically ".").
	base = substituteVars(base, vars, cfg.SpaceChar)

	// Render the directory part (spaces → folder_space_char, typically " ").
	dir = substituteVars(dir, vars, cfg.FolderSpaceChar)

	// Clean up any double separators or trailing slashes.
	result := filepath.Join(dir, base)
	return sanitizePath(result)
}

// substituteVars replaces {key} placeholders with values, using spaceChar
// to replace spaces within each value. Each value is sanitized first
// (slashes removed, control chars stripped) to prevent path injection.
func substituteVars(s string, vars map[string]string, spaceChar string) string {
	for key, val := range vars {
		placeholder := "{" + key + "}"
		if !strings.Contains(s, placeholder) {
			continue
		}
		rendered := sanitizeValue(val)
		if spaceChar != " " && spaceChar != "" {
			rendered = strings.ReplaceAll(rendered, " ", spaceChar)
		}
		s = strings.ReplaceAll(s, placeholder, rendered)
	}
	return s
}

// emojiAndNonASCIIRegex matches emoji and other non-ASCII characters that
// can cause trouble on NFS/SMB mounts and Windows filesystems.
var emojiAndNonASCIIRegex = regexp.MustCompile(`[^\x20-\x7E]`)

// sanitizePath cleans up characters that are problematic in filenames.
func sanitizePath(p string) string {
	// Replace characters that cause trouble on common filesystems.
	replacer := strings.NewReplacer(
		":", "-",
		"?", "",
		"*", "",
		"\"", "",
		"<", "",
		">", "",
		"|", "",
	)
	p = replacer.Replace(p)

	// Strip emoji and non-ASCII (problematic on NFS/SMB/Windows).
	p = emojiAndNonASCIIRegex.ReplaceAllString(p, "")

	// Collapse multiple spaces/dots/underscores left by stripping.
	for strings.Contains(p, "  ") {
		p = strings.ReplaceAll(p, "  ", " ")
	}
	for strings.Contains(p, "..") {
		p = strings.ReplaceAll(p, "..", ".")
	}
	for strings.Contains(p, "__") {
		p = strings.ReplaceAll(p, "__", "_")
	}

	// Strip leading/trailing dots and spaces from each path component
	// (Windows rejects these).
	parts := strings.Split(p, string(filepath.Separator))
	for i, part := range parts {
		parts[i] = strings.TrimFunc(part, func(r rune) bool {
			return r == '.' || r == ' '
		})
	}
	p = strings.Join(parts, string(filepath.Separator))

	return filepath.Clean(p)
}

// sanitizeValue cleans a single template variable value before substitution.
// This runs BEFORE the value is placed into the path, so slashes in a
// performer name like "Step-Mom/Step-Son" don't create subdirectories.
func sanitizeValue(v string) string {
	// Replace forward slash with dash (would create unwanted subdirs).
	v = strings.ReplaceAll(v, "/", "-")
	// Replace backslash too (Windows path separator).
	v = strings.ReplaceAll(v, "\\", "-")
	// Strip control characters.
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
	return v
}

// truncatePath ensures the total path length stays under maxPathLen.
// If it's too long, the filename (last component) gets truncated and
// a _{sceneID} suffix is appended for uniqueness.
func truncatePath(fullPath string, sceneID string) string {
	if len(fullPath) <= maxPathLen {
		return fullPath
	}

	dir := filepath.Dir(fullPath)
	ext := filepath.Ext(fullPath)
	suffix := "_" + sceneID + ext

	// How much room do we have for the base name?
	available := maxPathLen - len(dir) - 1 - len(suffix) // -1 for separator
	if available < 10 {
		// Dir itself is too long; just use scene ID as filename.
		return filepath.Join(dir, sceneID+ext)
	}

	base := strings.TrimSuffix(filepath.Base(fullPath), ext)
	if len(base) > available {
		base = base[:available]
	}
	// Trim trailing dots/spaces from the truncated base.
	base = strings.TrimRight(base, ". ")

	return filepath.Join(dir, base+suffix)
}

// heightToToken maps video height to a filename resolution token.
func heightToToken(h int) string {
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
	case h <= 4320:
		return "8k"
	default:
		return "8k"
	}
}
