// Package tui implements the `stash-janitor review` interactive terminal UI for
// walking through duplicate groups and making per-group decisions.
//
// Built on bubbletea + lipgloss. Two modes:
//
//   - **List mode**: browse all groups, see status/reclaimable/paths at a glance
//   - **Detail mode**: drill into one group, see every scene with per-loser
//     "kept by" explanations, take actions (accept, override, dismiss, etc.)
//
// Actions write UserDecisions to the store and update the in-memory state
// immediately. The decisions take effect on the next `stash-janitor scenes scan`
// (which re-reads user_decisions when assigning roles).
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/decide"
	"github.com/Wasylq/StashJanitor/internal/store"
)

// Model is the bubbletea model for `stash-janitor review`.
type Model struct {
	store  *store.Store
	scorer *decide.SceneScorer

	groups []*store.SceneGroup
	cursor int    // index in groups
	mode   string // "list" or "detail"

	// Detail mode: if override is active, the user picks a scene by number.
	overrideMode  bool
	overrideCursor int

	// Terminal dimensions.
	width  int
	height int

	// Status flash message (e.g. "marked not_duplicate").
	message string

	quitting bool
}

// NewModel constructs the review TUI model. statuses controls which groups
// are loaded — pass nil for all, or e.g. ["decided","needs_review"] to
// focus on actionable groups.
func NewModel(st *store.Store, scorer *decide.SceneScorer, statuses []string) (*Model, error) {
	groups, err := st.ListSceneGroups(context.Background(), statuses)
	if err != nil {
		return nil, err
	}
	return &Model{
		store:  st,
		scorer: scorer,
		groups: groups,
		mode:   "list",
	}, nil
}

// Init is the bubbletea Init function. We request the terminal size.
func (m Model) Init() tea.Cmd {
	return nil
}

// Update handles key presses and window resizes.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		m.message = ""

		if m.quitting {
			return m, tea.Quit
		}

		switch m.mode {
		case "list":
			return m.updateList(msg)
		case "detail":
			return m.updateDetail(msg)
		}
	}
	return m, nil
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.groups)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(0, len(m.groups)-1)
	case "pgdown":
		m.cursor = min(m.cursor+10, max(0, len(m.groups)-1))
	case "pgup":
		m.cursor = max(m.cursor-10, 0)
	case "enter":
		if len(m.groups) > 0 {
			m.mode = "detail"
			m.overrideMode = false
			m.overrideCursor = 0
		}
	}
	return m, nil
}

func (m Model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	g := m.currentGroup()
	if g == nil {
		m.mode = "list"
		return m, nil
	}

	if m.overrideMode {
		return m.updateOverride(msg, g)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc", "backspace":
		m.mode = "list"
	case "a":
		// Accept auto-pick → mark as decided (no-op if already decided,
		// but this clears needs_review overrides).
		m.message = m.applyDecision(g, "dismiss", "", "accepted auto-pick")
		m.advanceToNext()
	case "n":
		m.message = m.applyDecision(g, "not_duplicate", "", "")
		m.advanceToNext()
	case "d":
		m.message = m.applyDecision(g, "dismiss", "", "")
		m.advanceToNext()
	case "o":
		m.overrideMode = true
		m.overrideCursor = 0
	case "j", "down":
		m.advanceToNext()
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	}
	return m, nil
}

func (m Model) updateOverride(msg tea.KeyMsg, g *store.SceneGroup) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.overrideMode = false
	case "j", "down":
		if m.overrideCursor < len(g.Scenes)-1 {
			m.overrideCursor++
		}
	case "k", "up":
		if m.overrideCursor > 0 {
			m.overrideCursor--
		}
	case "enter":
		chosen := g.Scenes[m.overrideCursor]
		m.message = m.applyDecision(g, "force_keeper", chosen.SceneID, "")
		m.overrideMode = false
		m.advanceToNext()
	}
	return m, nil
}

func (m *Model) advanceToNext() {
	if m.cursor < len(m.groups)-1 {
		m.cursor++
	}
}

func (m *Model) currentGroup() *store.SceneGroup {
	if m.cursor < 0 || m.cursor >= len(m.groups) {
		return nil
	}
	return m.groups[m.cursor]
}

func (m *Model) applyDecision(g *store.SceneGroup, decision, keeperID, notes string) string {
	d := store.UserDecision{
		Key:      g.Signature,
		Workflow: store.WorkflowScenes,
		Decision: decision,
		KeeperID: keeperID,
		Notes:    notes,
	}
	if err := m.store.PutUserDecision(context.Background(), d); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return fmt.Sprintf("group #%d → %s", g.ID, decision)
}

// View renders the current state.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if len(m.groups) == 0 {
		return "No groups to review. Run `stash-janitor scenes scan` first.\n\nPress q to quit."
	}

	switch m.mode {
	case "detail":
		return m.viewDetail()
	default:
		return m.viewList()
	}
}

// ----- styles -----

var (
	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleKeep     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	styleDrop     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleUndecided = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleStatus   = lipgloss.NewStyle().Background(lipgloss.Color("236")).Padding(0, 1)
	styleMsg      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	styleSelected = lipgloss.NewStyle().Background(lipgloss.Color("237"))
	styleOverSel  = lipgloss.NewStyle().Background(lipgloss.Color("22")).Bold(true)
)

// ----- list view -----

func (m Model) viewList() string {
	var b strings.Builder

	b.WriteString(styleTitle.Render("stash-janitor review — scenes"))
	b.WriteString(fmt.Sprintf("  (%d groups)\n\n", len(m.groups)))

	// Determine visible window.
	maxVisible := max(1, m.height-6)
	start := max(0, m.cursor-maxVisible/2)
	end := min(start+maxVisible, len(m.groups))
	if end-start < maxVisible {
		start = max(0, end-maxVisible)
	}

	for i := start; i < end; i++ {
		g := m.groups[i]
		line := m.formatListLine(g)
		if i == m.cursor {
			line = styleSelected.Render("▸ " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}

	// Status bar.
	b.WriteString("\n")
	b.WriteString(m.statusBar())

	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(styleMsg.Render(m.message))
	}

	return b.String()
}

func (m Model) formatListLine(g *store.SceneGroup) string {
	status := g.Status
	var reclaimable int64
	for _, s := range g.Scenes {
		if s.Role == store.RoleLoser {
			reclaimable += s.FileSize
		}
	}
	return fmt.Sprintf("#%-4d  %-13s  %d scenes  %8s  %s",
		g.ID, status, len(g.Scenes),
		confirm.HumanBytes(reclaimable),
		truncSig(g.Signature, 30),
	)
}

// ----- detail view -----

func (m Model) viewDetail() string {
	g := m.currentGroup()
	if g == nil {
		return "no group selected"
	}

	var b strings.Builder

	b.WriteString(styleTitle.Render(fmt.Sprintf("Group #%d", g.ID)))
	b.WriteString(fmt.Sprintf("  status=%s\n", g.Status))
	b.WriteString(fmt.Sprintf("signature: %s\n", g.Signature))
	if g.DecisionReason != "" {
		b.WriteString(fmt.Sprintf("reason: %s\n", g.DecisionReason))
	}
	b.WriteString("\n")

	for i := range g.Scenes {
		s := &g.Scenes[i]
		line := m.formatSceneLine(s)

		if m.overrideMode && i == m.overrideCursor {
			line = styleOverSel.Render("→ " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")

		// Show explanation for losers.
		if s.Role == store.RoleLoser && m.scorer != nil {
			keeper := m.findKeeper(g)
			if keeper != nil {
				if reason := m.scorer.ExplainPick(keeper, s); reason != "" {
					b.WriteString(styleDim.Render(fmt.Sprintf("      ↳ kept by: %s", reason)) + "\n")
				}
			}
		}
	}

	b.WriteString("\n")
	if m.overrideMode {
		b.WriteString(styleMsg.Render("OVERRIDE: ↑↓ to select, Enter to confirm, Esc to cancel") + "\n")
	} else {
		b.WriteString(styleDim.Render("  a=accept  o=override keeper  n=not_duplicate  d=dismiss  ↓=next  Esc=back") + "\n")
	}

	b.WriteString("\n")
	b.WriteString(m.statusBar())

	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(styleMsg.Render(m.message))
	}

	return b.String()
}

func (m Model) formatSceneLine(s *store.SceneGroupScene) string {
	var marker string
	switch s.Role {
	case store.RoleKeeper:
		marker = styleKeep.Render("KEEP")
	case store.RoleLoser:
		marker = styleDrop.Render("drop")
	default:
		marker = styleUndecided.Render("  ? ")
	}

	flags := ""
	if s.HasStashID {
		flags += "stashID,"
	}
	if s.Organized {
		flags += "org,"
	}
	if s.TagCount > 0 {
		flags += fmt.Sprintf("tags=%d,", s.TagCount)
	}
	flags = strings.TrimSuffix(flags, ",")
	if flags != "" {
		flags = "[" + flags + "]"
	}

	return fmt.Sprintf("%s  scene %-6s  %dx%-4d  %-5s  %8s  %s %s",
		marker, s.SceneID,
		s.Width, s.Height,
		codecOrDash(s.Codec),
		confirm.HumanBytes(s.FileSize),
		flags,
		s.PrimaryPath,
	)
}

func (m Model) findKeeper(g *store.SceneGroup) *store.SceneGroupScene {
	for i := range g.Scenes {
		if g.Scenes[i].Role == store.RoleKeeper {
			return &g.Scenes[i]
		}
	}
	return nil
}

// ----- status bar -----

func (m Model) statusBar() string {
	decided, needsReview, applied, dismissed, total := 0, 0, 0, 0, len(m.groups)
	var reclaimable int64
	for _, g := range m.groups {
		switch g.Status {
		case store.StatusDecided:
			decided++
			for _, s := range g.Scenes {
				if s.Role == store.RoleLoser {
					reclaimable += s.FileSize
				}
			}
		case store.StatusNeedsReview:
			needsReview++
		case store.StatusApplied:
			applied++
		case store.StatusDismissed:
			dismissed++
		}
	}

	bar := fmt.Sprintf(
		" %d/%d | decided:%d review:%d applied:%d dismissed:%d | reclaimable: %s ",
		m.cursor+1, total,
		decided, needsReview, applied, dismissed,
		confirm.HumanBytes(reclaimable),
	)
	return styleStatus.Render(bar)
}

// ----- helpers -----

func truncSig(sig string, n int) string {
	if len(sig) <= n {
		return sig
	}
	return sig[:n-3] + "..."
}

func codecOrDash(c string) string {
	if c == "" {
		return "-"
	}
	return c
}

// min and max are builtins since Go 1.21.
