// Package tui implements the `stash-janitor review` interactive terminal UI
// for walking through duplicate groups and making per-group decisions.
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
	overrideMode   bool
	overrideCursor int

	// Terminal dimensions.
	width  int
	height int

	// Status flash message.
	message string

	// Counters — updated after every decision so the status bar is live.
	decided   int
	reviewed  int // needs_review
	applied   int
	dismissed int

	quitting bool
}

// NewModel constructs the review TUI model.
func NewModel(st *store.Store, scorer *decide.SceneScorer, statuses []string) (*Model, error) {
	groups, err := st.ListSceneGroups(context.Background(), statuses)
	if err != nil {
		return nil, err
	}
	m := &Model{
		store:  st,
		scorer: scorer,
		groups: groups,
		mode:   "list",
	}
	m.recount()
	return m, nil
}

func (m *Model) recount() {
	m.decided, m.reviewed, m.applied, m.dismissed = 0, 0, 0, 0
	for _, g := range m.groups {
		switch g.Status {
		case store.StatusDecided:
			m.decided++
		case store.StatusNeedsReview:
			m.reviewed++
		case store.StatusApplied:
			m.applied++
		case store.StatusDismissed:
			m.dismissed++
		}
	}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		m.message = ""
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
		// Accept only works on DECIDED groups (confirm the scorer's pick).
		// For needs_review, you must pick a keeper first with 'o'.
		if g.Status == store.StatusNeedsReview {
			m.message = "This group needs review — press 'o' to pick a keeper, or 'n' for not_duplicate"
		} else {
			// Already decided: confirm the scorer's pick. Find the current
			// keeper and save a force_keeper override.
			if keeper := m.findKeeper(g); keeper != nil {
				m.message = m.applyDecision(g, "force_keeper", keeper.SceneID, "accepted auto-pick via review")
				g.Status = store.StatusDecided
				m.recount()
			} else {
				m.message = "No keeper found — press 'o' to pick one"
			}
			m.advanceToNext()
		}

	case "o":
		// Override: pick a keeper manually. Works for both decided and
		// needs_review groups.
		m.overrideMode = true
		m.overrideCursor = 0

	case "n":
		m.message = m.applyDecision(g, "not_duplicate", "", "")
		g.Status = store.StatusDismissed
		m.recount()
		m.advanceToNext()

	case "d":
		m.message = m.applyDecision(g, "dismiss", "", "")
		g.Status = store.StatusDismissed
		m.recount()
		m.advanceToNext()

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
		g.Status = store.StatusDecided
		m.recount()
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
	action := decision
	if keeperID != "" {
		action += " (keeper=" + keeperID + ")"
	}
	return fmt.Sprintf("group #%d → %s ✓", g.ID, action)
}

// View renders the current state.
func (m Model) View() string {
	if m.quitting {
		return "" // quit message is printed by the CLI after alt-screen exits
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

// QuitMessage returns the post-exit message. Exported so the CLI can
// print it AFTER bubbletea restores the normal terminal (the alt-screen
// swallows anything rendered inside View on quit).
func (m Model) QuitMessage() string {
	changes := m.decided + m.dismissed
	if changes == 0 {
		return "\nNo decisions made. Your groups are unchanged.\n\n"
	}
	return fmt.Sprintf(
		"\nSaved %d decisions. Next steps:\n"+
			"  1. stash-janitor scenes scan                              (re-scan to apply your decisions)\n"+
			"  2. stash-janitor scenes status                            (verify decided/dismissed counts)\n"+
			"  3. stash-janitor scenes apply --action merge --commit     (execute merges and reclaim disk)\n\n",
		changes,
	)
}

// ----- styles -----

var (
	styleTitle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleKeep      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	styleDrop      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleUndecided = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleDim       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleStatus    = lipgloss.NewStyle().Background(lipgloss.Color("236")).Padding(0, 1)
	styleMsg       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	styleWarn      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	styleSelected  = lipgloss.NewStyle().Background(lipgloss.Color("237"))
	styleOverSel   = lipgloss.NewStyle().Background(lipgloss.Color("22")).Bold(true)
)

// ----- list view -----

func (m Model) viewList() string {
	var b strings.Builder

	b.WriteString(styleTitle.Render("stash-janitor review — scenes"))
	b.WriteString(fmt.Sprintf("  (%d groups)\n\n", len(m.groups)))

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

	b.WriteString("\n")
	b.WriteString(m.statusBar())
	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(styleMsg.Render(m.message))
	}
	return b.String()
}

func (m Model) formatListLine(g *store.SceneGroup) string {
	var reclaimable int64
	for _, s := range g.Scenes {
		if s.Role == store.RoleLoser {
			reclaimable += s.FileSize
		}
	}
	status := g.Status
	if status == store.StatusDismissed {
		status = styleDim.Render(status)
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
		b.WriteString(styleMsg.Render("PICK KEEPER: ↑↓ to select, Enter to confirm, Esc to cancel") + "\n")
	} else if g.Status == store.StatusNeedsReview {
		b.WriteString(styleWarn.Render("  NEEDS REVIEW — press 'o' to pick which scene to keep") + "\n")
		b.WriteString(styleDim.Render("  o=pick keeper  n=not_duplicate  d=dismiss  ↓=skip  Esc=back") + "\n")
	} else {
		b.WriteString(styleDim.Render("  a=accept  o=change keeper  n=not_duplicate  d=dismiss  ↓=next  Esc=back") + "\n")
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
	var reclaimable int64
	for _, g := range m.groups {
		if g.Status == store.StatusDecided {
			for _, s := range g.Scenes {
				if s.Role == store.RoleLoser {
					reclaimable += s.FileSize
				}
			}
		}
	}

	bar := fmt.Sprintf(
		" %d/%d | decided:%d review:%d applied:%d dismissed:%d | reclaimable: %s ",
		m.cursor+1, len(m.groups),
		m.decided, m.reviewed, m.applied, m.dismissed,
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
