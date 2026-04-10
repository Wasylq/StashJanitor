package merge

import (
	"testing"
)

func TestParseFilenameMetadata(t *testing.T) {
	cases := []struct {
		name     string
		basename string
		wantDate string
		wantTitle string
		wantPerf []string
	}{
		{
			name:      "standard single performer",
			basename:  "2024-12-15_Aimee.Waves-Your.Work.Crush.has.a.Girl.Cock_1080p.mp4",
			wantDate:  "2024-12-15",
			wantTitle: "Your Work Crush has a Girl Cock",
			wantPerf:  []string{"Aimee Waves"},
		},
		{
			name:      "title with dash",
			basename:  "2023-10-24_Codi.Vore-How.Women.Orgasm.-.Codi.Vore_1080p.mp4",
			wantDate:  "2023-10-24",
			wantTitle: "How Women Orgasm - Codi Vore",
			wantPerf:  []string{"Codi Vore"},
		},
		{
			name:      "4k resolution",
			basename:  "2022-08-24_Angel.The.Dreamgirl-716.She.Makes.You.Hungry_4k.mp4",
			wantDate:  "2022-08-24",
			wantTitle: "716 She Makes You Hungry",
			wantPerf:  []string{"Angel The Dreamgirl"},
		},
		{
			name:      "8k resolution",
			basename:  "2025-07-03_Gigi.Dior-Breeding.Gigi.Dior_8k.mp4",
			wantDate:  "2025-07-03",
			wantTitle: "Breeding Gigi Dior",
			wantPerf:  []string{"Gigi Dior"},
		},
		{
			name:      "import suffix",
			basename:  "2024-12-15_Aimee.Waves-Your.Work.Crush_1080p_1.mp4",
			wantDate:  "2024-12-15",
			wantTitle: "Your Work Crush",
			wantPerf:  []string{"Aimee Waves"},
		},
		{
			name:      "performers omitted (dash at start)",
			basename:  "2023-10-24_-Group.Title_1080p.mp4",
			wantDate:  "2023-10-24",
			wantTitle: "Group Title",
			wantPerf:  nil,
		},
		{
			name:      "dot separators in date",
			basename:  "2024.12.15_Performer-Title_1080p.mp4",
			wantDate:  "2024-12-15",
			wantTitle: "Title",
			wantPerf:  []string{"Performer"},
		},
		{
			name:      "mkv extension",
			basename:  "2024-12-15_Foo-Bar_720p.mkv",
			wantDate:  "2024-12-15",
			wantTitle: "Bar",
			wantPerf:  []string{"Foo"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			md := ParseFilenameMetadata(c.basename)
			if md == nil {
				t.Fatalf("ParseFilenameMetadata(%q) returned nil", c.basename)
			}
			if md.Date != c.wantDate {
				t.Errorf("Date = %q, want %q", md.Date, c.wantDate)
			}
			if md.Title != c.wantTitle {
				t.Errorf("Title = %q, want %q", md.Title, c.wantTitle)
			}
			if len(md.Performers) != len(c.wantPerf) {
				t.Fatalf("Performers = %v, want %v", md.Performers, c.wantPerf)
			}
			for i, want := range c.wantPerf {
				if md.Performers[i] != want {
					t.Errorf("Performers[%d] = %q, want %q", i, md.Performers[i], want)
				}
			}
		})
	}
}

func TestParseFilenameMetadataReturnsNilForJunk(t *testing.T) {
	junk := []string{
		"random_garbage_4k_encoded.mp4",
		"🎄No PPV 🎄 Justine Jakobs 🌽 OnlyFans.mp4",
		"tbkjnjkvfucrkfobvnuewewtgwiihcdg.mp4",
		"Gigi Dior - Oil Anal and MILF - Big Wet Tits 24 - ELEGANT ANGEL.mp4",
	}
	for _, name := range junk {
		if md := ParseFilenameMetadata(name); md != nil {
			t.Errorf("expected nil for junk %q, got %+v", name, md)
		}
	}
}

func TestParseFilenameMultiplePerformers(t *testing.T) {
	md := ParseFilenameMetadata("2024-01-01_Alice_Bob.Smith-Some.Title_1080p.mp4")
	if md == nil {
		t.Fatal("nil")
	}
	if len(md.Performers) != 2 {
		t.Fatalf("expected 2 performers, got %v", md.Performers)
	}
	if md.Performers[0] != "Alice" {
		t.Errorf("Performers[0] = %q, want Alice", md.Performers[0])
	}
	if md.Performers[1] != "Bob Smith" {
		t.Errorf("Performers[1] = %q, want Bob Smith", md.Performers[1])
	}
}
