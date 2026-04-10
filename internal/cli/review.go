package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/Wasylq/StashJanitor/internal/decide"
	"github.com/Wasylq/StashJanitor/internal/store"
	"github.com/Wasylq/StashJanitor/internal/tui"
	"github.com/spf13/cobra"
)

var flagReviewFilter string

func newReviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Interactive TUI for walking through duplicate groups",
		Long: `Launch an interactive terminal UI for reviewing scene duplicate
groups one at a time. Navigate with arrow keys, see per-loser
explanations, and take actions:

  a = accept the auto-pick (marks dismissed so future scans skip it)
  o = override the keeper (select a different scene)
  n = mark as not_duplicate
  d = dismiss
  Enter = drill into detail view
  Esc = back to list

Decisions are saved to stash-janitor.sqlite and take effect on the next
'stash-janitor scenes scan'.`,
		RunE: runReview,
	}
	cmd.Flags().StringVar(&flagReviewFilter, "filter", "",
		"which groups to show: all|decided|needs-review|applied|dismissed (default: decided + needs-review)")
	return cmd
}

func runReview(cmd *cobra.Command, args []string) error {
	cfg, st, _, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	scorer, err := decide.NewSceneScorer(cfg)
	if err != nil {
		return fmt.Errorf("building scorer: %w", err)
	}

	// Default filter: show actionable groups (decided + needs_review).
	var statuses []string
	switch flagReviewFilter {
	case "all":
		statuses = nil
	case "decided":
		statuses = []string{store.StatusDecided}
	case "needs-review":
		statuses = []string{store.StatusNeedsReview}
	case "applied":
		statuses = []string{store.StatusApplied}
	case "dismissed":
		statuses = []string{store.StatusDismissed}
	case "":
		statuses = []string{store.StatusDecided, store.StatusNeedsReview}
	default:
		return fmt.Errorf("unknown --filter %q", flagReviewFilter)
	}

	model, err := tui.NewModel(st, scorer, statuses)
	if err != nil {
		return err
	}

	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return err
	}

	// Print the quit message AFTER bubbletea exits the alt-screen,
	// so the user actually sees it on their normal terminal.
	if m, ok := finalModel.(tui.Model); ok {
		if msg := m.QuitMessage(); msg != "" {
			fmt.Fprint(cmd.OutOrStdout(), msg)
		}
	}
	return nil
}
