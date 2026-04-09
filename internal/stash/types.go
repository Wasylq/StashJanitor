package stash

// Types in this file mirror the subset of Stash's GraphQL schema that stash-janitor
// actually consumes. They are intentionally NOT a complete schema clone —
// adding a new field is cheap and explicit, but carrying the entire surface
// area would couple us to schema churn we don't care about.
//
// JSON tags match Stash's field names (snake_case). Field names are Go
// idiomatic (CamelCase).

// Scene is a Stash scene with the fields stash-janitor needs across both workflows.
type Scene struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Code      string  `json:"code"`
	Details   string  `json:"details"`
	Director  string  `json:"director"`
	URLs      []string `json:"urls"`
	Date      string  `json:"date"`
	Rating100 *int    `json:"rating100"`
	Organized bool    `json:"organized"`
	OCounter  int     `json:"o_counter"`
	PlayCount *int    `json:"play_count"`

	Studio     *Studio      `json:"studio"`
	Tags       []Tag        `json:"tags"`
	Performers []Performer  `json:"performers"`
	StashIDs   []StashID    `json:"stash_ids"`
	Galleries  []Gallery    `json:"galleries"`
	Groups     []SceneGroup `json:"groups"`

	Files []VideoFile `json:"files"`
}

// PrimaryFile returns the first file in the scene's files list, which Stash
// treats as the canonical/primary file. Returns nil if the scene has no
// files (shouldn't happen in practice but defensively handled).
func (s *Scene) PrimaryFile() *VideoFile {
	if len(s.Files) == 0 {
		return nil
	}
	return &s.Files[0]
}

// HasStashID reports whether the scene has at least one stash-box ID
// attached. Used by the scoring engine.
func (s *Scene) HasStashID() bool {
	return len(s.StashIDs) > 0
}

// VideoFile is a Stash video file (subset of the VideoFile GraphQL type).
type VideoFile struct {
	ID         string        `json:"id"`
	Path       string        `json:"path"`
	Basename   string        `json:"basename"`
	Size       int64         `json:"size"`
	ModTime    string        `json:"mod_time"`
	Format     string        `json:"format"`
	Width      int           `json:"width"`
	Height     int           `json:"height"`
	Duration   float64       `json:"duration"`
	VideoCodec string        `json:"video_codec"`
	AudioCodec string        `json:"audio_codec"`
	FrameRate  float64       `json:"frame_rate"`
	BitRate    int           `json:"bit_rate"`

	Fingerprints []Fingerprint `json:"fingerprints"`
}

// Fingerprint is a single content hash for a file.
type Fingerprint struct {
	Type  string `json:"type"`  // oshash, md5, phash
	Value string `json:"value"`
}

// Tag is a Stash tag (id+name only — stash-janitor doesn't need anything else).
type Tag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Performer is a Stash performer (id+name only).
type Performer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Studio is a Stash studio (id+name only).
type Studio struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// StashID associates a scene with an entry in a stash-box endpoint.
type StashID struct {
	Endpoint string `json:"endpoint"`
	StashID  string `json:"stash_id"`
}

// Gallery is a referenced gallery (id only — used for the merge union).
type Gallery struct {
	ID string `json:"id"`
}

// SceneGroup is the join between a scene and a Group (formerly Movie),
// holding the scene's index within the group.
type SceneGroup struct {
	Group      Group `json:"group"`
	SceneIndex *int  `json:"scene_index"`
}

// Group is a Stash group / movie (id only).
type Group struct {
	ID string `json:"id"`
}

// FindScenesResult is the return type of findScenes, used for paginated
// enumeration in workflow B.
type FindScenesResult struct {
	Count  int     `json:"count"`
	Scenes []Scene `json:"scenes"`
}

// FindTagsResult is the return type of findTags.
type FindTagsResult struct {
	Count int   `json:"count"`
	Tags  []Tag `json:"tags"`
}

// StashBoxConfig is one entry from configuration.general.stashBoxes —
// the list of stash-box endpoints the user has configured in Stash.
type StashBoxConfig struct {
	Endpoint string `json:"endpoint"`
	Name     string `json:"name"`
}

// ScrapedScene mirrors Stash's ScrapedScene type, the result of a
// scrapeMultiScenes / scrapeSingleScene call. We only carry the fields the
// orphans report needs.
type ScrapedScene struct {
	RemoteSiteID string             `json:"remote_site_id"`
	Title        string             `json:"title"`
	Date         string             `json:"date"`
	URLs         []string           `json:"urls"`
	Studio       *ScrapedStudio     `json:"studio"`
	Performers   []ScrapedPerformer `json:"performers"`
}

// ScrapedStudio is the studio sub-object on a ScrapedScene.
type ScrapedStudio struct {
	Name         string `json:"name"`
	RemoteSiteID string `json:"remote_site_id"`
}

// ScrapedPerformer is one entry in ScrapedScene.Performers.
type ScrapedPerformer struct {
	Name         string `json:"name"`
	RemoteSiteID string `json:"remote_site_id"`
}

// CriterionModifier mirrors the GraphQL enum used in scene filters.
type CriterionModifier string

const (
	ModifierEquals      CriterionModifier = "EQUALS"
	ModifierNotEquals   CriterionModifier = "NOT_EQUALS"
	ModifierGreaterThan CriterionModifier = "GREATER_THAN"
	ModifierLessThan    CriterionModifier = "LESS_THAN"
)

// BulkUpdateIDMode mirrors the GraphQL enum for bulk-update ID semantics.
type BulkUpdateIDMode string

const (
	BulkUpdateAdd    BulkUpdateIDMode = "ADD"
	BulkUpdateRemove BulkUpdateIDMode = "REMOVE"
	BulkUpdateSet    BulkUpdateIDMode = "SET"
)

// BulkUpdateIDs is the input shape for bulkSceneUpdate's tag_ids/etc fields.
// We always use Mode=ADD when tagging dedup losers/keepers so we don't
// accidentally drop existing tags.
type BulkUpdateIDs struct {
	IDs  []string         `json:"ids"`
	Mode BulkUpdateIDMode `json:"mode"`
}
