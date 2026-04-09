package store

import "time"

// ScanRun records one invocation of `stash-janitor scenes scan` or `stash-janitor files scan`.
type ScanRun struct {
	ID           int64      `json:"id"`
	Workflow     string     `json:"workflow"` // WorkflowScenes | WorkflowFiles
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Distance     *int       `json:"distance,omitempty"`      // only meaningful for WorkflowScenes
	DurationDiff *float64   `json:"duration_diff,omitempty"` // only meaningful for WorkflowScenes
	GroupCount   int        `json:"group_count"`
	Notes        string     `json:"notes,omitempty"`
}

// SceneGroup is a workflow A duplicate group as cached locally.
type SceneGroup struct {
	ID             int64             `json:"id"`
	ScanRunID      int64             `json:"scan_run_id"`
	Signature      string            `json:"signature"`
	Status         string            `json:"status"`
	DecisionReason string            `json:"decision_reason,omitempty"`
	DecidedAt      *time.Time        `json:"decided_at,omitempty"`
	AppliedAt      *time.Time        `json:"applied_at,omitempty"`
	Scenes         []SceneGroupScene `json:"scenes"`
}

// SceneGroupScene is one scene's snapshot within a SceneGroup.
type SceneGroupScene struct {
	SceneID         string  `json:"scene_id"`
	Role            string  `json:"role"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	Bitrate         int     `json:"bitrate,omitempty"`
	Framerate       float64 `json:"framerate,omitempty"`
	Codec           string  `json:"codec,omitempty"`
	FileSize        int64   `json:"file_size,omitempty"`
	Duration        float64 `json:"duration,omitempty"`
	Organized       bool    `json:"organized"`
	HasStashID      bool    `json:"has_stash_id"`
	TagCount        int     `json:"tag_count"`
	PerformerCount  int     `json:"performer_count"`
	PrimaryPath     string  `json:"primary_path,omitempty"`
	// FilenameQuality is 0/1 from running the filename_quality regex
	// against the primary file's basename. Used by the workflow A safety
	// net to flag groups where the keeper has a junk filename but a loser
	// has a structured one (so deleting the loser would lose the only
	// human-readable metadata).
	FilenameQuality int `json:"filename_quality"`
}

// FileGroup is a workflow B multi-file scene as cached locally.
type FileGroup struct {
	ID             int64           `json:"id"`
	ScanRunID      int64           `json:"scan_run_id"`
	SceneID        string          `json:"scene_id"`
	Status         string          `json:"status"`
	DecisionReason string          `json:"decision_reason,omitempty"`
	DecidedAt      *time.Time      `json:"decided_at,omitempty"`
	AppliedAt      *time.Time      `json:"applied_at,omitempty"`
	Files          []FileGroupFile `json:"files"`
}

// FileGroupFile is one file's snapshot within a FileGroup.
type FileGroupFile struct {
	FileID          string  `json:"file_id"`
	Role            string  `json:"role"`
	IsPrimary       bool    `json:"is_primary"`
	Basename        string  `json:"basename"`
	Path            string  `json:"path"`
	ModTime         string  `json:"mod_time,omitempty"`
	FilenameQuality int     `json:"filename_quality"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	Bitrate         int     `json:"bitrate,omitempty"`
	Framerate       float64 `json:"framerate,omitempty"`
	Codec           string  `json:"codec,omitempty"`
	FileSize        int64   `json:"file_size,omitempty"`
}

// UserDecision is a persistent override that survives scan re-runs.
type UserDecision struct {
	Key       string    `json:"key"` // signature (scenes) or "scene:<id>" (files)
	Workflow  string    `json:"workflow"`
	Decision  string    `json:"decision"` // not_duplicate | force_keeper | dismiss | keep_all
	KeeperID  string    `json:"keeper_id,omitempty"`
	DecidedAt time.Time `json:"decided_at"`
	Notes     string    `json:"notes,omitempty"`
}

// SceneGroupSignature builds the canonical signature for a workflow A group:
// the sorted scene IDs joined by '|'. Stable across scan runs so user
// decisions can be re-applied automatically.
func SceneGroupSignature(sceneIDs []string) string {
	if len(sceneIDs) == 0 {
		return ""
	}
	// Make a copy so we don't reorder the caller's slice.
	sorted := make([]string, len(sceneIDs))
	copy(sorted, sceneIDs)
	// Tiny insertion sort — typically 2-5 IDs per group, fewer cycles than
	// dragging in sort.Strings for the rare big group.
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	out := sorted[0]
	for _, id := range sorted[1:] {
		out += "|" + id
	}
	return out
}
