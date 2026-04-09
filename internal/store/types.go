package store

import "time"

// ScanRun records one invocation of `stash-janitor scenes scan` or `stash-janitor files scan`.
type ScanRun struct {
	ID           int64
	Workflow     string // WorkflowScenes | WorkflowFiles
	StartedAt    time.Time
	FinishedAt   *time.Time
	Distance     *int     // only meaningful for WorkflowScenes
	DurationDiff *float64 // only meaningful for WorkflowScenes
	GroupCount   int
	Notes        string
}

// SceneGroup is a workflow A duplicate group as cached locally.
type SceneGroup struct {
	ID             int64
	ScanRunID      int64
	Signature      string
	Status         string
	DecisionReason string
	DecidedAt      *time.Time
	AppliedAt      *time.Time
	Scenes         []SceneGroupScene
}

// SceneGroupScene is one scene's snapshot within a SceneGroup.
type SceneGroupScene struct {
	SceneID        string
	Role           string
	Width          int
	Height         int
	Bitrate        int
	Framerate      float64
	Codec          string
	FileSize       int64
	Duration       float64
	Organized      bool
	HasStashID     bool
	TagCount       int
	PerformerCount int
	PrimaryPath    string
}

// FileGroup is a workflow B multi-file scene as cached locally.
type FileGroup struct {
	ID             int64
	ScanRunID      int64
	SceneID        string
	Status         string
	DecisionReason string
	DecidedAt      *time.Time
	AppliedAt      *time.Time
	Files          []FileGroupFile
}

// FileGroupFile is one file's snapshot within a FileGroup.
type FileGroupFile struct {
	FileID          string
	Role            string
	IsPrimary       bool
	Basename        string
	Path            string
	ModTime         string // free-form RFC3339 string from Stash; we don't parse it
	FilenameQuality int    // 0 or 1
	Width           int
	Height          int
	Bitrate         int
	Framerate       float64
	Codec           string
	FileSize        int64
}

// UserDecision is a persistent override that survives scan re-runs.
type UserDecision struct {
	Key       string // signature (scenes) or "scene:<id>" (files)
	Workflow  string
	Decision  string // not_duplicate | force_keeper | dismiss | keep_all
	KeeperID  string
	DecidedAt time.Time
	Notes     string
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
