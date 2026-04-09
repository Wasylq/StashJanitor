// Package confirm provides the interactive YES prompt used by stash-janitor's
// destructive --commit actions (scenes apply --action merge, future
// scenes apply --action delete, files apply --commit).
//
// The protection is intentionally annoying: the user must type the literal
// string "YES" verbatim, with no shortcut, to proceed. The --yes flag
// short-circuits the prompt for scripted/cron use, but is independent of
// --commit, so a single typo cannot trigger destruction.
package confirm

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Summary describes what's about to happen, used in the prompt header.
type Summary struct {
	Action          string // e.g. "merge"
	GroupCount      int
	SceneCount      int
	ReclaimableBytes int64
}

// PromptYES displays the summary and reads from in. Returns true only when
// the user types exactly "YES" followed by a newline.
//
// If autoYes is true the prompt and stdin are skipped entirely; the summary
// is still printed to out so the user (or log) sees what was committed.
//
// in may be nil if autoYes is true; otherwise it must be non-nil. out is
// always required.
func PromptYES(in io.Reader, out io.Writer, s Summary, autoYes bool) (bool, error) {
	if out == nil {
		return false, fmt.Errorf("confirm: out is nil")
	}

	header := formatSummary(s)
	if _, err := io.WriteString(out, header); err != nil {
		return false, err
	}

	if autoYes {
		_, err := io.WriteString(out, "\n--yes flag set, proceeding without prompt.\n")
		return true, err
	}

	if in == nil {
		return false, fmt.Errorf("confirm: in is nil and autoYes is false")
	}

	if _, err := io.WriteString(out, "\nType YES (uppercase) to proceed, anything else to abort: "); err != nil {
		return false, err
	}

	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "YES" {
		_, _ = io.WriteString(out, "Confirmed.\n")
		return true, nil
	}
	_, _ = io.WriteString(out, "Aborted (input was not exactly \"YES\").\n")
	return false, nil
}

// formatSummary builds the human-readable header. Pulled out for testing.
func formatSummary(s Summary) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("================================================================\n")
	fmt.Fprintf(&b, "  ABOUT TO %s — this will mutate your Stash library.\n", strings.ToUpper(s.Action))
	b.WriteString("================================================================\n")
	fmt.Fprintf(&b, "  Groups affected:    %d\n", s.GroupCount)
	fmt.Fprintf(&b, "  Scenes affected:    %d\n", s.SceneCount)
	fmt.Fprintf(&b, "  Reclaimable bytes:  %s (%d bytes)\n", humanBytes(s.ReclaimableBytes), s.ReclaimableBytes)
	b.WriteString("================================================================\n")
	return b.String()
}

// humanBytes formats a byte count using SI binary units. Pulled out as a
// package helper because the report package will want it too.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// HumanBytes is the exported form of humanBytes for use by other stash-janitor packages.
func HumanBytes(n int64) string { return humanBytes(n) }
