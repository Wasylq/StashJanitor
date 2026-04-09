package merge

import (
	"testing"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/stash"
)

func defaults(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("/missing")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestBuildUnionTagsCombined(t *testing.T) {
	cfg := defaults(t)
	keeper := &stash.Scene{
		ID: "K", Tags: []stash.Tag{{ID: "1"}, {ID: "2"}},
	}
	loser := &stash.Scene{
		ID: "L", Tags: []stash.Tag{{ID: "2"}, {ID: "3"}, {ID: "4"}},
	}
	res, err := BuildUnion(keeper, []*stash.Scene{loser}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Vals == nil {
		t.Fatal("expected non-nil Vals when tags would change")
	}
	if got, want := len(res.Vals.TagIDs), 4; got != want {
		t.Errorf("len(TagIDs) = %d, want %d (1,2,3,4 deduped)", got, want)
	}
	// Tags 1 and 2 from keeper come first, then 3 and 4 from loser.
	want := []string{"1", "2", "3", "4"}
	for i, w := range want {
		if res.Vals.TagIDs[i] != w {
			t.Errorf("TagIDs[%d] = %q, want %q", i, res.Vals.TagIDs[i], w)
		}
	}
	// Diff should mention tags.
	foundTagsDiff := false
	for _, d := range res.Diffs {
		if d.Field == "tags" {
			foundTagsDiff = true
			break
		}
	}
	if !foundTagsDiff {
		t.Error("expected a tags FieldDiff")
	}
}

func TestBuildUnionNothingToChange(t *testing.T) {
	cfg := defaults(t)
	// Keeper already has everything the loser does.
	keeper := &stash.Scene{
		ID: "K", Tags: []stash.Tag{{ID: "1"}, {ID: "2"}},
		Performers: []stash.Performer{{ID: "p1"}},
		Title:      "the title",
	}
	loser := &stash.Scene{
		ID: "L", Tags: []stash.Tag{{ID: "1"}},
		Performers: []stash.Performer{{ID: "p1"}},
		Title:      "different",
	}
	res, err := BuildUnion(keeper, []*stash.Scene{loser}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Vals != nil {
		t.Errorf("expected nil Vals when nothing changes, got %+v", res.Vals)
	}
	if len(res.Diffs) != 0 {
		t.Errorf("expected empty Diffs, got %+v", res.Diffs)
	}
}

func TestBuildUnionPrefersKeeperThenLoserForScalars(t *testing.T) {
	cfg := defaults(t)
	keeper := &stash.Scene{ID: "K"} // empty title, details, etc.
	loser := &stash.Scene{
		ID:       "L",
		Title:    "Real Title",
		Details:  "Real details from loser",
		Director: "Steven Spielberg",
	}
	res, err := BuildUnion(keeper, []*stash.Scene{loser}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Vals == nil {
		t.Fatal("expected non-nil Vals")
	}
	if res.Vals.Title == nil || *res.Vals.Title != "Real Title" {
		t.Errorf("Title = %v, want \"Real Title\"", res.Vals.Title)
	}
	if res.Vals.Details == nil || *res.Vals.Details != "Real details from loser" {
		t.Errorf("Details mismatch: %v", res.Vals.Details)
	}
	if res.Vals.Director == nil || *res.Vals.Director != "Steven Spielberg" {
		t.Errorf("Director mismatch: %v", res.Vals.Director)
	}
}

func TestBuildUnionKeeperScalarsWin(t *testing.T) {
	cfg := defaults(t)
	keeper := &stash.Scene{
		ID:    "K",
		Title: "Keeper title",
	}
	loser := &stash.Scene{
		ID:    "L",
		Title: "Loser title",
	}
	res, err := BuildUnion(keeper, []*stash.Scene{loser}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Vals != nil && res.Vals.Title != nil {
		t.Errorf("expected Title to remain unchanged (keeper wins), got %v", *res.Vals.Title)
	}
}

func TestBuildUnionRating100TakesMax(t *testing.T) {
	cfg := defaults(t)
	r70 := 70
	r90 := 90
	keeper := &stash.Scene{ID: "K", Rating100: &r70}
	loser := &stash.Scene{ID: "L", Rating100: &r90}
	res, err := BuildUnion(keeper, []*stash.Scene{loser}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Vals == nil || res.Vals.Rating100 == nil {
		t.Fatal("expected Rating100 to be set")
	}
	if *res.Vals.Rating100 != 90 {
		t.Errorf("Rating100 = %d, want 90", *res.Vals.Rating100)
	}
}

func TestBuildUnionOrganizedAnyTrue(t *testing.T) {
	cfg := defaults(t)
	keeper := &stash.Scene{ID: "K", Organized: false}
	loser := &stash.Scene{ID: "L", Organized: true}
	res, err := BuildUnion(keeper, []*stash.Scene{loser}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Vals == nil || res.Vals.Organized == nil || !*res.Vals.Organized {
		t.Errorf("expected Organized to be set true; got %+v", res.Vals)
	}
}

func TestBuildUnionStashIDsCombinedNoDup(t *testing.T) {
	cfg := defaults(t)
	keeper := &stash.Scene{
		ID:       "K",
		StashIDs: []stash.StashID{{Endpoint: "https://stashdb.org", StashID: "abc"}},
	}
	loser := &stash.Scene{
		ID: "L",
		StashIDs: []stash.StashID{
			{Endpoint: "https://stashdb.org", StashID: "abc"}, // dup
			{Endpoint: "https://fansdb.cc", StashID: "xyz"},
		},
	}
	res, err := BuildUnion(keeper, []*stash.Scene{loser}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Vals == nil || len(res.Vals.StashIDs) != 2 {
		t.Fatalf("expected 2 stash_ids, got %+v", res.Vals)
	}
}

func TestBuildUnionURLsCombined(t *testing.T) {
	cfg := defaults(t)
	keeper := &stash.Scene{ID: "K", URLs: []string{"http://a"}}
	loser := &stash.Scene{ID: "L", URLs: []string{"http://a", "http://b"}}
	res, err := BuildUnion(keeper, []*stash.Scene{loser}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Vals == nil || len(res.Vals.URLs) != 2 {
		t.Fatalf("expected 2 urls, got %+v", res.Vals)
	}
}

func TestBuildUnionRejectsNilKeeper(t *testing.T) {
	cfg := defaults(t)
	if _, err := BuildUnion(nil, nil, cfg); err == nil {
		t.Error("expected error for nil keeper")
	}
}
