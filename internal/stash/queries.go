package stash

import (
	"context"
	"errors"
	"fmt"
)

// sceneFieldsFragment is the GraphQL fragment we attach to every scene we
// fetch. Keeping it in one place means adding a field is a single-line edit
// rather than tracking it across multiple query strings.
const sceneFieldsFragment = `
fragment SceneFields on Scene {
  id
  title
  code
  details
  director
  urls
  date
  rating100
  organized
  o_counter
  play_count
  studio { id name }
  tags { id name }
  performers { id name }
  stash_ids { endpoint stash_id }
  galleries { id }
  groups { group { id } scene_index }
  files {
    id
    path
    basename
    size
    mod_time
    format
    width
    height
    duration
    video_codec
    audio_codec
    frame_rate
    bit_rate
    fingerprints { type value }
  }
}
`

// ============================================================================
// Workflow A: cross-scene queries
// ============================================================================

const findDuplicateScenesQuery = sceneFieldsFragment + `
query FindDuplicateScenes($distance: Int, $duration_diff: Float) {
  findDuplicateScenes(distance: $distance, duration_diff: $duration_diff) {
    ...SceneFields
  }
}
`

// FindDuplicateScenes returns groups of perceptually-duplicate scenes from
// Stash, computed server-side via findDuplicateScenes. distance is the phash
// hamming distance threshold (0 = identical, 4 = re-encodes, >8 risky).
// durationDiff is the maximum duration delta in seconds for two scenes to
// be considered the same content.
//
// The result is a slice of groups, each group a slice of two or more scenes.
// At your library scale (~60k scenes) this can be a multi-MB response — see
// PLAN.md section 11 for the streaming-decode mitigation that scan/scenes.go
// will eventually implement on top of this.
func (c *Client) FindDuplicateScenes(ctx context.Context, distance int, durationDiff float64) ([][]Scene, error) {
	var resp struct {
		FindDuplicateScenes [][]Scene `json:"findDuplicateScenes"`
	}
	vars := map[string]any{
		"distance":      distance,
		"duration_diff": durationDiff,
	}
	if err := c.Execute(ctx, findDuplicateScenesQuery, vars, &resp); err != nil {
		return nil, fmt.Errorf("findDuplicateScenes: %w", err)
	}
	return resp.FindDuplicateScenes, nil
}

// ============================================================================
// Single scene by ID — used by both workflows when full detail is needed
// ============================================================================

const findSceneQuery = sceneFieldsFragment + `
query FindScene($id: ID!) {
  findScene(id: $id) {
    ...SceneFields
  }
}
`

// FindScene fetches one scene by ID with full metadata. Returns nil if the
// scene does not exist (Stash returns null data, not an error).
func (c *Client) FindScene(ctx context.Context, id string) (*Scene, error) {
	var resp struct {
		FindScene *Scene `json:"findScene"`
	}
	vars := map[string]any{"id": id}
	if err := c.Execute(ctx, findSceneQuery, vars, &resp); err != nil {
		return nil, fmt.Errorf("findScene(%s): %w", id, err)
	}
	return resp.FindScene, nil
}

// ============================================================================
// Workflow B: multi-file scene enumeration
// ============================================================================

const findMultiFileScenesQuery = sceneFieldsFragment + `
query FindMultiFileScenes($page: Int!, $per_page: Int!) {
  findScenes(
    filter: { page: $page, per_page: $per_page, sort: "id", direction: ASC }
    scene_filter: { file_count: { modifier: GREATER_THAN, value: 1 } }
  ) {
    count
    scenes { ...SceneFields }
  }
}
`

// FindMultiFileScenesPage returns one page of scenes that have more than
// one file attached. The total result count is exposed in
// FindScenesResult.Count so callers can drive their own pagination loop.
//
// page is 1-indexed (Stash convention). perPage is capped server-side; 100
// is a sensible default.
func (c *Client) FindMultiFileScenesPage(ctx context.Context, page, perPage int) (*FindScenesResult, error) {
	if page < 1 {
		return nil, errors.New("findMultiFileScenes: page must be >= 1")
	}
	if perPage < 1 {
		return nil, errors.New("findMultiFileScenes: perPage must be >= 1")
	}
	var resp struct {
		FindScenes FindScenesResult `json:"findScenes"`
	}
	vars := map[string]any{
		"page":     page,
		"per_page": perPage,
	}
	if err := c.Execute(ctx, findMultiFileScenesQuery, vars, &resp); err != nil {
		return nil, fmt.Errorf("findMultiFileScenes(page=%d): %w", page, err)
	}
	return &resp.FindScenes, nil
}

// ============================================================================
// Tag find-or-create — used by `stash-janitor scenes apply --action tag`
// ============================================================================

const findTagByNameQuery = `
query FindTagByName($name: String!) {
  findTags(
    tag_filter: { name: { value: $name, modifier: EQUALS } }
    filter: { per_page: 1 }
  ) {
    count
    tags { id name }
  }
}
`

const tagCreateMutation = `
mutation TagCreate($name: String!) {
  tagCreate(input: { name: $name }) {
    id
    name
  }
}
`

// FindOrCreateTag returns a tag with the given name, creating it if it
// doesn't exist yet. Idempotent — safe to call repeatedly. Used to materialize
// the _dedupe_loser / _dedupe_keeper tags on first apply.
func (c *Client) FindOrCreateTag(ctx context.Context, name string) (*Tag, error) {
	var find struct {
		FindTags FindTagsResult `json:"findTags"`
	}
	if err := c.Execute(ctx, findTagByNameQuery, map[string]any{"name": name}, &find); err != nil {
		return nil, fmt.Errorf("findTagByName(%q): %w", name, err)
	}
	for i := range find.FindTags.Tags {
		// Stash's name filter is contains-by-default, so we have to do a
		// strict equality check ourselves to avoid matching a substring tag.
		if find.FindTags.Tags[i].Name == name {
			return &find.FindTags.Tags[i], nil
		}
	}

	var create struct {
		TagCreate Tag `json:"tagCreate"`
	}
	if err := c.Execute(ctx, tagCreateMutation, map[string]any{"name": name}, &create); err != nil {
		return nil, fmt.Errorf("tagCreate(%q): %w", name, err)
	}
	if create.TagCreate.ID == "" {
		return nil, fmt.Errorf("tagCreate(%q): empty response", name)
	}
	return &create.TagCreate, nil
}

// ============================================================================
// bulkSceneUpdate — used to add tags to many scenes at once
// ============================================================================

const bulkSceneUpdateMutation = `
mutation BulkSceneUpdate($ids: [ID!]!, $tag_ids: BulkUpdateIds!) {
  bulkSceneUpdate(input: { ids: $ids, tag_ids: $tag_ids }) {
    id
  }
}
`

// BulkAddTag adds a single tag to many scenes in one call. Mode is always
// ADD so existing tags on the scenes are preserved.
func (c *Client) BulkAddTag(ctx context.Context, sceneIDs []string, tagID string) error {
	if len(sceneIDs) == 0 {
		return nil
	}
	vars := map[string]any{
		"ids": sceneIDs,
		"tag_ids": BulkUpdateIDs{
			IDs:  []string{tagID},
			Mode: BulkUpdateAdd,
		},
	}
	return c.Execute(ctx, bulkSceneUpdateMutation, vars, nil)
}

// ============================================================================
// sceneUpdate — used by workflow B to swap the primary file
// ============================================================================

const sceneUpdatePrimaryFileMutation = `
mutation SceneUpdatePrimaryFile($id: ID!, $primary_file_id: ID!) {
  sceneUpdate(input: { id: $id, primary_file_id: $primary_file_id }) {
    id
  }
}
`

// SetPrimaryFile changes which file is the primary for a scene. Used by
// workflow B (and the post-merge cleanup of workflow A) to promote the
// best file before deleting the others.
func (c *Client) SetPrimaryFile(ctx context.Context, sceneID, fileID string) error {
	vars := map[string]any{
		"id":              sceneID,
		"primary_file_id": fileID,
	}
	return c.Execute(ctx, sceneUpdatePrimaryFileMutation, vars, nil)
}

// ============================================================================
// sceneMerge — workflow A's merge action
// ============================================================================

// SceneMergeInput mirrors Stash's SceneMergeInput. Values is the *computed*
// metadata union, NOT the raw loser values — Stash does not auto-union
// metadata, so the caller must compute it. See merge package.
type SceneMergeInput struct {
	Source      []string         `json:"source"`
	Destination string           `json:"destination"`
	Values      *SceneUpdateVals `json:"values,omitempty"`
	PlayHistory bool             `json:"play_history,omitempty"`
	OHistory    bool             `json:"o_history,omitempty"`
}

// SceneUpdateVals is the subset of SceneUpdateInput that the merge union
// engine populates. Pointer fields let us distinguish "leave unchanged"
// (nil) from "set to empty value" (non-nil pointer to zero value).
type SceneUpdateVals struct {
	Title         *string  `json:"title,omitempty"`
	Code          *string  `json:"code,omitempty"`
	Details       *string  `json:"details,omitempty"`
	Director      *string  `json:"director,omitempty"`
	URLs          []string `json:"urls,omitempty"`
	Date          *string  `json:"date,omitempty"`
	Rating100     *int     `json:"rating100,omitempty"`
	Organized     *bool    `json:"organized,omitempty"`
	StudioID      *string  `json:"studio_id,omitempty"`
	GalleryIDs    []string `json:"gallery_ids,omitempty"`
	PerformerIDs  []string `json:"performer_ids,omitempty"`
	TagIDs        []string `json:"tag_ids,omitempty"`
	StashIDs      []StashIDInput `json:"stash_ids,omitempty"`
}

// StashIDInput mirrors Stash's StashIDInput shape (used by sceneUpdate and
// sceneMerge). Identical to StashID but with a separate type to avoid
// implying we ever read it from Stash.
type StashIDInput struct {
	Endpoint string `json:"endpoint"`
	StashID  string `json:"stash_id"`
}

const sceneMergeMutation = `
mutation SceneMerge($input: SceneMergeInput!) {
  sceneMerge(input: $input) {
    id
  }
}
`

// SceneMerge runs the sceneMerge mutation against Stash. The values field
// must already be the computed metadata union — Stash does NOT auto-union
// scene-level metadata.
func (c *Client) SceneMerge(ctx context.Context, input SceneMergeInput) error {
	if len(input.Source) == 0 {
		return errors.New("sceneMerge: source must contain at least one scene id")
	}
	if input.Destination == "" {
		return errors.New("sceneMerge: destination scene id is required")
	}
	vars := map[string]any{"input": input}
	return c.Execute(ctx, sceneMergeMutation, vars, nil)
}

// ============================================================================
// deleteFiles — used by workflow B and post-merge cleanup
// ============================================================================

const deleteFilesMutation = `
mutation DeleteFiles($ids: [ID!]!) {
  deleteFiles(ids: $ids)
}
`

// DeleteFiles removes the given files from both Stash's database AND from
// the underlying filesystem. This is the "really delete it" mutation, NOT
// destroyFiles (which only removes the DB entry and would let Stash re-add
// the file on the next scan).
func (c *Client) DeleteFiles(ctx context.Context, fileIDs []string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	vars := map[string]any{"ids": fileIDs}
	return c.Execute(ctx, deleteFilesMutation, vars, nil)
}

// ============================================================================
// Diagnostics
// ============================================================================

const versionQuery = `{ version { version build_time } }`

// Version returns Stash's version info. Used for connectivity smoke tests
// and to record the schema version stash-janitor was talking to in scan_runs.
func (c *Client) Version(ctx context.Context) (string, string, error) {
	var resp struct {
		Version struct {
			Version   string `json:"version"`
			BuildTime string `json:"build_time"`
		} `json:"version"`
	}
	if err := c.Execute(ctx, versionQuery, nil, &resp); err != nil {
		return "", "", err
	}
	return resp.Version.Version, resp.Version.BuildTime, nil
}
