package stash

import (
	"context"
	"os"
	"testing"
	"time"
)

// Integration tests in this file talk to a real Stash instance and only run
// when SFX_INT_STASH_URL is set in the environment. Without that env var
// they're skipped, so `go test ./...` stays hermetic.
//
// To run locally:
//
//	SFX_INT_STASH_URL=http://localhost:9999 \
//	  go test ./internal/stash/... -run Integration -v
//
// All operations are read-only.

func integrationClient(t *testing.T) *Client {
	t.Helper()
	url := os.Getenv("SFX_INT_STASH_URL")
	if url == "" {
		t.Skip("SFX_INT_STASH_URL not set; skipping integration test")
	}
	return NewClient(url, os.Getenv("SFX_INT_STASH_API_KEY"))
}

func TestIntegrationVersion(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, _, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version == "" {
		t.Error("expected non-empty version")
	}
	t.Logf("stash version: %s", version)
}

func TestIntegrationFindMultiFileScenesPage(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	page, err := c.FindMultiFileScenesPage(ctx, 1, 5)
	if err != nil {
		t.Fatalf("FindMultiFileScenesPage: %v", err)
	}
	t.Logf("multi-file scenes total count: %d (got %d on page 1)", page.Count, len(page.Scenes))
	for _, s := range page.Scenes {
		if len(s.Files) < 2 {
			t.Errorf("scene %s has %d files; filter is supposed to return file_count > 1", s.ID, len(s.Files))
		}
	}
}

func TestIntegrationFindDuplicateScenesIdentical(t *testing.T) {
	c := integrationClient(t)
	// distance=0 keeps the response small (only byte-equivalent phashes).
	// duration_diff=1.0 matches our default.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	groups, err := c.FindDuplicateScenes(ctx, 0, 1.0)
	if err != nil {
		t.Fatalf("FindDuplicateScenes: %v", err)
	}
	t.Logf("found %d duplicate groups at distance=0", len(groups))
	for i, g := range groups {
		if len(g) < 2 {
			t.Errorf("group %d has %d scenes; expected >= 2", i, len(g))
		}
		// Spot-check that we actually got rich data, not just IDs.
		for _, s := range g {
			if s.ID == "" {
				t.Errorf("group %d: scene with empty ID", i)
			}
		}
	}
}

func TestIntegrationFindScene(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pick any scene id we know exists. We use scene 1 because every Stash
	// install has at least one scene.
	scene, err := c.FindScene(ctx, "1")
	if err != nil {
		t.Fatalf("FindScene(1): %v", err)
	}
	if scene == nil {
		t.Skip("scene 1 not found; can't test full-scene shape against this stash")
	}
	if scene.ID != "1" {
		t.Errorf("scene.ID = %q, want 1", scene.ID)
	}
	if len(scene.Files) == 0 {
		t.Error("expected scene to have at least one file")
	} else {
		f := scene.Files[0]
		if f.Path == "" {
			t.Error("file path is empty")
		}
		if f.Size == 0 {
			t.Error("file size is 0; the JSON unmarshal probably mis-decoded Int64")
		}
		t.Logf("scene 1 primary file: %s (%d bytes, %dx%d, %s)", f.Basename, f.Size, f.Width, f.Height, f.VideoCodec)
	}
}
