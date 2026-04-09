package apply

import (
	"testing"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/decide"
	"github.com/Wasylq/StashJanitor/internal/stash"
)

func TestHeightToResolutionToken(t *testing.T) {
	cases := []struct {
		height int
		want   string
	}{
		{0, ""},
		{-1, ""},
		{360, "480p"},
		{480, "480p"},
		{540, "540p"},
		{720, "720p"},
		{1080, "1080p"},
		{1440, "1440p"},
		{1620, "4k"}, // between 1440 and 2160
		{2160, "4k"},
		{4320, "8k"},
		{8000, "8k"},
	}
	for _, c := range cases {
		if got := heightToResolutionToken(c.height); got != c.want {
			t.Errorf("heightToResolutionToken(%d) = %q, want %q", c.height, got, c.want)
		}
	}
}

func TestRebuildBasenameSwapsResolutionAndExtension(t *testing.T) {
	cases := []struct {
		name       string
		structured string
		winnerExt  string
		winnerH    int
		want       string
	}{
		{
			name:       "1080p structured → 4k winner",
			structured: "2024-12-15_Performer-Title_1080p.mp4",
			winnerExt:  "garbage.mp4",
			winnerH:    2160,
			want:       "2024-12-15_Performer-Title_4k.mp4",
		},
		{
			name:       "1080p structured → 8k winner with mkv",
			structured: "2024-12-15_Performer-Title_1080p.mp4",
			winnerExt:  "garbage.mkv",
			winnerH:    4320,
			want:       "2024-12-15_Performer-Title_8k.mkv",
		},
		{
			name:       "structured already has _N suffix",
			structured: "2024-12-15_Performer-Title_1080p_1.mp4",
			winnerExt:  "garbage.mp4",
			winnerH:    2160,
			want:       "2024-12-15_Performer-Title_4k.mp4",
		},
		{
			name:       "structured at 4k → 1080p winner",
			structured: "2024-12-15_Performer-Title_4k.mp4",
			winnerExt:  "garbage.mp4",
			winnerH:    1080,
			want:       "2024-12-15_Performer-Title_1080p.mp4",
		},
		{
			name:       "no resolution token → append",
			structured: "2024-12-15_Performer-Title.mp4",
			winnerExt:  "garbage.mp4",
			winnerH:    1080,
			want:       "2024-12-15_Performer-Title_1080p.mp4",
		},
		{
			name:       "8K → 4K with same ext",
			structured: "2024-12-15_Foo-Bar_8K.mp4",
			winnerExt:  "garbage.mp4",
			winnerH:    2160,
			want:       "2024-12-15_Foo-Bar_4k.mp4",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			winner := &stash.VideoFile{Basename: c.winnerExt, Height: c.winnerH}
			got := rebuildBasenameForFile(c.structured, winner)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestRebuildBasenameNilWinner(t *testing.T) {
	if got := rebuildBasenameForFile("foo.mp4", nil); got != "foo.mp4" {
		t.Errorf("nil winner should return input unchanged, got %q", got)
	}
}

func TestRebuildBasenameUnknownHeight(t *testing.T) {
	// Height 0 → token is "", we strip the structured token instead.
	winner := &stash.VideoFile{Basename: "x.mp4", Height: 0}
	got := rebuildBasenameForFile("2024-12-15_Foo-Bar_1080p.mp4", winner)
	want := "2024-12-15_Foo-Bar.mp4"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPickRenameTargetSkipsWhenWinnerHasGoodName(t *testing.T) {
	scorer := newFileScorerForTest(t)
	files := []stash.VideoFile{
		{ID: "winner", Basename: "2024-12-15_Foo-Bar_4k.mp4", Width: 3840, Height: 2160},
		{ID: "loser", Basename: "2024-12-15_Foo-Bar_1080p.mp4", Width: 1920, Height: 1080},
	}
	if got := pickRenameTarget(scorer, files, 0); got != "" {
		t.Errorf("expected no rename when winner already has structured name, got %q", got)
	}
}

func TestPickRenameTargetUsesLoserFilename(t *testing.T) {
	scorer := newFileScorerForTest(t)
	files := []stash.VideoFile{
		// Winner is junk-named, 4K.
		{ID: "winner", Basename: "garbage_blob.mp4", Width: 3840, Height: 2160},
		// Loser has structured name at 1080p.
		{ID: "loser", Basename: "2024-12-15_Foo-Bar_1080p.mp4", Width: 1920, Height: 1080},
	}
	got := pickRenameTarget(scorer, files, 0)
	want := "2024-12-15_Foo-Bar_4k.mp4"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPickRenameTargetNoStructuredLoser(t *testing.T) {
	scorer := newFileScorerForTest(t)
	files := []stash.VideoFile{
		{ID: "winner", Basename: "garbage_blob.mp4", Width: 3840, Height: 2160},
		{ID: "loser", Basename: "another_blob.mp4", Width: 1920, Height: 1080},
	}
	if got := pickRenameTarget(scorer, files, 0); got != "" {
		t.Errorf("expected no rename when no loser has structured name, got %q", got)
	}
}

// newFileScorerForTest builds a FileScorer using the embedded default
// config. Pulled out as a helper so the rename tests don't repeat config
// boilerplate.
func newFileScorerForTest(t *testing.T) *decide.FileScorer {
	t.Helper()
	cfg, err := config.Load("/missing")
	if err != nil {
		t.Fatal(err)
	}
	s, err := decide.NewFileScorer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStripResolutionToken(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"foo_1080p.mp4", "foo.mp4"},
		{"foo_1080p_1.mp4", "foo.mp4"},
		{"foo_4k.mkv", "foo.mkv"},
		{"foo.mp4", "foo.mp4"}, // no token, unchanged
	}
	for _, c := range cases {
		if got := stripResolutionToken(c.in); got != c.want {
			t.Errorf("stripResolutionToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
