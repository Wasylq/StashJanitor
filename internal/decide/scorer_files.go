package decide

import (
	"fmt"
	"regexp"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// FileScorer ranks files within a single Stash scene (workflow B). Files
// in the same scene are byte-equivalent (Stash matches by oshash/md5), so
// the rule chain considers only filename, path, and mod_time. Tech specs
// are deliberately not part of the chain — see PLAN.md section 7.
type FileScorer struct {
	rules         []fileRule
	pathPriority  []string
	filenameRegex *regexp.Regexp
}

// NewFileScorer compiles the rule chain and the filename-quality regex
// from config. A bad regex is a startup-time failure rather than a silent
// "no files match" at scan time.
func NewFileScorer(cfg *config.Config) (*FileScorer, error) {
	s := &FileScorer{
		pathPriority: cfg.Scoring.PathPriority,
	}
	if pat := cfg.Scoring.Files.FilenameQuality.Pattern; pat != "" {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("scoring.files.filename_quality.pattern: %w", err)
		}
		s.filenameRegex = re
	}
	for _, name := range cfg.Scoring.Files.Rules {
		rule, err := makeFileRule(name)
		if err != nil {
			return nil, fmt.Errorf("scoring.files.rules: %w", err)
		}
		s.rules = append(s.rules, rule)
	}
	if len(s.rules) == 0 {
		return nil, fmt.Errorf("scoring.files.rules: no rules configured")
	}
	return s, nil
}

// ClassifyFilename returns 1 if basename matches the configured regex, 0
// otherwise. Used by the workflow B scan code at snapshot-build time to
// populate FileGroupFile.FilenameQuality, so the scoring chain only has
// to compare an int.
func (s *FileScorer) ClassifyFilename(basename string) int {
	if s.filenameRegex == nil {
		return 0
	}
	if s.filenameRegex.MatchString(basename) {
		return 1
	}
	return 0
}

// DecideFiles scores a multi-file scene's files and picks the keeper.
//
// Behavior mirrors DecideScenes:
//   - len(files) < 2: degenerate (file_count > 1 should always give us
//     at least 2, but be defensive). Returns KeeperIndex=0 if non-empty.
//   - one file strictly beats every other → KeeperIndex = its position.
//   - any two tie on every rule → KeeperIndex=-1 needs_review.
func (s *FileScorer) DecideFiles(files []store.FileGroupFile) Decision {
	switch len(files) {
	case 0:
		return Decision{KeeperIndex: -1, Reason: "empty file group"}
	case 1:
		return Decision{KeeperIndex: 0}
	}

	best := 0
	for i := 1; i < len(files); i++ {
		if s.compareFiles(&files[i], &files[best]) > 0 {
			best = i
		}
	}

	for i := range files {
		if i == best {
			continue
		}
		if s.compareFiles(&files[best], &files[i]) == 0 {
			return Decision{
				KeeperIndex: -1,
				Reason: fmt.Sprintf("tied on every rule: files %s and %s", files[best].FileID, files[i].FileID),
			}
		}
	}

	return Decision{KeeperIndex: best}
}

func (s *FileScorer) compareFiles(a, b *store.FileGroupFile) int {
	for _, rule := range s.rules {
		if c := rule(s, a, b); c != 0 {
			return c
		}
	}
	return 0
}

// ----- file rule implementations -----

type fileRule func(s *FileScorer, a, b *store.FileGroupFile) int

func makeFileRule(name string) (fileRule, error) {
	switch name {
	case "filename_quality":
		return ruleFilenameQuality, nil
	case "path_priority":
		return ruleFilePathPriority, nil
	case "mod_time":
		return ruleModTime, nil
	default:
		return nil, fmt.Errorf("unknown file rule %q", name)
	}
}

func ruleFilenameQuality(_ *FileScorer, a, b *store.FileGroupFile) int {
	return cmpInt(a.FilenameQuality, b.FilenameQuality)
}

func ruleFilePathPriority(s *FileScorer, a, b *store.FileGroupFile) int {
	aRank := pathRank(a.Path, s.pathPriority)
	bRank := pathRank(b.Path, s.pathPriority)
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

// ruleModTime compares mod_time strings lexicographically. Stash returns
// RFC3339 timestamps which sort correctly as strings, so this is a tiny
// optimization over parsing them. Newer = bigger string = wins.
func ruleModTime(_ *FileScorer, a, b *store.FileGroupFile) int {
	switch {
	case a.ModTime > b.ModTime:
		return 1
	case a.ModTime < b.ModTime:
		return -1
	default:
		return 0
	}
}

// PickPostMergeKeeper picks the best file from a multi-file scene that
// resulted from a sceneMerge.
//
// Unlike the main DecideFiles path (which assumes byte-equivalent files
// because Stash matches workflow B's multi-files by oshash/md5), the files
// after a cross-scene sceneMerge come from previously-separate scenes and
// have *different* tech specs. Picking by filename alone would silently
// lose quality, so this method uses a fixed rule chain that prioritizes
// quality first, then filename quality:
//
//	1. resolution     (width × height — higher wins)
//	2. bitrate        (higher wins)
//	3. filename_quality (regex match — true wins)
//	4. file_size      (larger wins, tiebreaker)
//	5. mod_time       (newer wins, last-resort tiebreaker)
//
// This rule chain is intentionally NOT configurable in Phase 1 — there
// is exactly one sensible policy for picking the best of N different
// files. We can revisit if anyone has a strong opinion.
//
// Returns the index into files plus an empty string on success, or
// (-1, reason) when every rule ties. files must be non-empty.
func (s *FileScorer) PickPostMergeKeeper(files []stash.VideoFile) (int, string) {
	switch len(files) {
	case 0:
		return -1, "no files"
	case 1:
		return 0, ""
	}

	best := 0
	for i := 1; i < len(files); i++ {
		if s.comparePostMerge(&files[i], &files[best]) > 0 {
			best = i
		}
	}
	for i := range files {
		if i == best {
			continue
		}
		if s.comparePostMerge(&files[best], &files[i]) == 0 {
			return -1, fmt.Sprintf("post-merge: files %s and %s tied on every rule", files[best].ID, files[i].ID)
		}
	}
	return best, ""
}

func (s *FileScorer) comparePostMerge(a, b *stash.VideoFile) int {
	if c := cmpInt(a.Width*a.Height, b.Width*b.Height); c != 0 {
		return c
	}
	if c := cmpInt(a.BitRate, b.BitRate); c != 0 {
		return c
	}
	if c := cmpInt(s.ClassifyFilename(a.Basename), s.ClassifyFilename(b.Basename)); c != 0 {
		return c
	}
	if c := cmpInt64(a.Size, b.Size); c != 0 {
		return c
	}
	switch {
	case a.ModTime > b.ModTime:
		return 1
	case a.ModTime < b.ModTime:
		return -1
	default:
		return 0
	}
}
