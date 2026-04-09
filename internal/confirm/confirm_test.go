package confirm

import (
	"bytes"
	"strings"
	"testing"
)

func TestPromptYESExactMatch(t *testing.T) {
	in := strings.NewReader("YES\n")
	out := &bytes.Buffer{}
	ok, err := PromptYES(in, out, Summary{Action: "merge", GroupCount: 5, SceneCount: 12, ReclaimableBytes: 1024 * 1024 * 1024 * 3}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected ok=true for input \"YES\"")
	}
	if !strings.Contains(out.String(), "Confirmed") {
		t.Errorf("expected 'Confirmed' in output, got: %s", out.String())
	}
}

func TestPromptYESCaseSensitive(t *testing.T) {
	cases := []string{"yes", "Yes", "y", "YES!", " YES", "YE"}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			in := strings.NewReader(input + "\n")
			out := &bytes.Buffer{}
			ok, err := PromptYES(in, out, Summary{Action: "merge"}, false)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Errorf("input %q should NOT confirm", input)
			}
		})
	}
}

func TestPromptYESAutoYesShortcuts(t *testing.T) {
	out := &bytes.Buffer{}
	ok, err := PromptYES(nil, out, Summary{Action: "merge", GroupCount: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected autoYes to short-circuit to true")
	}
	if !strings.Contains(out.String(), "--yes flag set") {
		t.Errorf("expected --yes notice in output, got: %s", out.String())
	}
}

func TestPromptYESNilInWithoutAutoYes(t *testing.T) {
	out := &bytes.Buffer{}
	_, err := PromptYES(nil, out, Summary{}, false)
	if err == nil {
		t.Error("expected error when in is nil and autoYes is false")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.00 KiB"},
		{1024 * 1024, "1.00 MiB"},
		{int64(1.5 * 1024 * 1024 * 1024), "1.50 GiB"},
		{int64(2.5 * 1024 * 1024 * 1024 * 1024), "2.50 TiB"},
	}
	for _, c := range cases {
		got := HumanBytes(c.n)
		if got != c.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
