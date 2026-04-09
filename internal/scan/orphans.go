package scan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// OrphansOptions configures one invocation of `stash-janitor orphans scan`.
type OrphansOptions struct {
	// Endpoint is the stash-box endpoint to query. If empty, the scanner
	// auto-discovers via Stash's configuration query and uses the first
	// available.
	Endpoint string

	// PerPage is the orphan-discovery page size (findScenes pagination).
	// Defaults to 100.
	PerPage int

	// BatchSize is how many scene IDs to bundle into one scrapeMultiScenes
	// call. Defaults to 20.
	BatchSize int

	// MaxScenes, when > 0, stops the scan after processing N orphans.
	// Useful for sanity-checking before running against the full library.
	MaxScenes int

	// BatchDelay is the sleep between batches, used as a rough rate limit
	// against stash-box. Defaults to 250ms.
	BatchDelay time.Duration

	// Rescan, when true, re-queries orphans we've already looked up
	// before. Default false: we skip them.
	Rescan bool
}

// OrphansResult is the per-run summary returned by Orphans.
type OrphansResult struct {
	ScanRunID    int64
	Endpoint     string
	OrphansSeen  int
	Matched      int
	NoMatch      int
	Skipped      int // already-looked-up scenes (Rescan=false)
}

// Orphans runs workflow C's scan-and-lookup pipeline.
//
//  1. discover the stash-box endpoint (or use the one supplied),
//  2. paginate findScenes(stash_id_count = 0),
//  3. batch the orphans into scrapeMultiScenes calls,
//  4. for each scene + match list, upsert one orphan_lookups row.
//
// On error inside a batch, the error is logged and the loop continues.
// At 33k+ orphans, the user can't afford one bad batch to abort the run.
func Orphans(ctx context.Context, c *stash.Client, st *store.Store, opts OrphansOptions) (*OrphansResult, error) {
	if c == nil || st == nil {
		return nil, errors.New("scan.Orphans: client and store are required")
	}
	if opts.PerPage <= 0 {
		opts.PerPage = 100
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 20
	}
	if opts.BatchDelay <= 0 {
		opts.BatchDelay = 250 * time.Millisecond
	}

	// Pick an endpoint.
	endpoint := opts.Endpoint
	if endpoint == "" {
		boxes, err := c.StashBoxes(ctx)
		if err != nil {
			return nil, fmt.Errorf("discovering stash-box endpoints: %w", err)
		}
		if len(boxes) == 0 {
			return nil, errors.New("no stash-box endpoints configured in Stash; set one in Stash settings or pass --endpoint")
		}
		endpoint = boxes[0].Endpoint
		slog.Info("auto-selected stash-box endpoint", "endpoint", endpoint, "name", boxes[0].Name)
	}

	runID, err := st.StartScanRun(ctx, store.WorkflowOrphans, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("starting scan run: %w", err)
	}

	res := &OrphansResult{ScanRunID: runID, Endpoint: endpoint}
	page := 1
	totalOrphans := -1

	// Buffer to accumulate scenes until we hit BatchSize, then submit.
	pending := make([]stash.Scene, 0, opts.BatchSize)
	flushBatch := func() error {
		if len(pending) == 0 {
			return nil
		}
		sceneIDs := make([]string, len(pending))
		for i := range pending {
			sceneIDs[i] = pending[i].ID
		}
		matches, err := c.ScrapeMultiScenes(ctx, endpoint, sceneIDs)
		if err != nil {
			slog.Error("scrapeMultiScenes batch failed; continuing",
				"endpoint", endpoint, "batch_size", len(sceneIDs), "error", err)
			pending = pending[:0]
			return nil // log and continue
		}
		for i, scene := range pending {
			row := orphanSnapshot(&scene, runID, endpoint)
			if i < len(matches) && len(matches[i]) > 0 {
				row.Status = store.StatusMatched
				row.MatchCount = len(matches[i])
				best := matches[i][0]
				row.MatchRemoteID = best.RemoteSiteID
				row.MatchTitle = best.Title
				row.MatchDate = best.Date
				if best.Studio != nil {
					row.MatchStudio = best.Studio.Name
				}
				if len(best.Performers) > 0 {
					names := make([]string, len(best.Performers))
					for j, p := range best.Performers {
						names[j] = p.Name
					}
					row.MatchPerformers = strings.Join(names, ", ")
				}
				res.Matched++
			} else {
				row.Status = store.StatusNoMatch
				res.NoMatch++
			}
			if err := st.UpsertOrphanLookup(ctx, row); err != nil {
				slog.Error("upsertOrphanLookup failed; continuing",
					"scene_id", scene.ID, "error", err)
			}
		}
		pending = pending[:0]

		// Rate limit between batches.
		if opts.BatchDelay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(opts.BatchDelay):
			}
		}
		return nil
	}

	for {
		slog.Info("fetching orphan scenes page", "page", page, "per_page", opts.PerPage)
		result, err := c.FindOrphanScenesPage(ctx, page, opts.PerPage)
		if err != nil {
			return nil, fmt.Errorf("findOrphanScenes(page=%d): %w", page, err)
		}
		if totalOrphans == -1 {
			totalOrphans = result.Count
			slog.Info("orphan scenes total", "count", totalOrphans)
		}
		if len(result.Scenes) == 0 {
			break
		}

		for i := range result.Scenes {
			if opts.MaxScenes > 0 && res.OrphansSeen >= opts.MaxScenes {
				if err := flushBatch(); err != nil {
					return nil, err
				}
				slog.Info("max-scenes limit reached, stopping",
					"limit", opts.MaxScenes, "remaining", totalOrphans-res.OrphansSeen)
				if err := st.FinishScanRun(ctx, runID, res.OrphansSeen); err != nil {
					return nil, err
				}
				return res, nil
			}

			scene := result.Scenes[i]
			res.OrphansSeen++

			// Skip already-looked-up scenes unless rescanning.
			if !opts.Rescan {
				if seen, err := st.HasOrphanLookup(ctx, scene.ID, endpoint); err == nil && seen {
					res.Skipped++
					continue
				}
			}

			pending = append(pending, scene)
			if len(pending) >= opts.BatchSize {
				if err := flushBatch(); err != nil {
					return nil, err
				}
			}
		}

		// Stop when we've enumerated everything Stash reported.
		if res.OrphansSeen >= totalOrphans {
			break
		}
		page++
	}

	if err := flushBatch(); err != nil {
		return nil, err
	}
	if err := st.FinishScanRun(ctx, runID, res.OrphansSeen); err != nil {
		return nil, fmt.Errorf("finishing scan run: %w", err)
	}
	return res, nil
}

// orphanSnapshot builds the snapshot fields of an OrphanLookup from a
// stash.Scene. The match fields are filled in by the caller.
func orphanSnapshot(s *stash.Scene, runID int64, endpoint string) *store.OrphanLookup {
	o := &store.OrphanLookup{
		ScanRunID: runID,
		SceneID:   s.ID,
		Endpoint:  endpoint,
	}
	if pf := s.PrimaryFile(); pf != nil {
		o.PrimaryPath = pf.Path
		o.Basename = filepath.Base(pf.Path)
		o.Duration = pf.Duration
		o.Width = pf.Width
		o.Height = pf.Height
	}
	return o
}
