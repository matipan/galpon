package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/matipan/galpon/internal/model"
)

func consoleRule(width int) string {
	glyph := "─"
	if consoleASCII() {
		glyph = "-"
	}
	return lipgloss.NewStyle().Foreground(Tokyo.Border).Render(strings.Repeat(glyph, max(0, width)))
}

func consolePair(left, right string, width int) string {
	if lipgloss.Width(left)+lipgloss.Width(right)+3 > width {
		return ansi.Truncate(left, max(0, width), "…")
	}
	return left + strings.Repeat(" ", max(0, width-lipgloss.Width(left)-lipgloss.Width(right))) + right
}

func consoleCell(text string, width int) string {
	text = ansi.Truncate(text, max(0, width), "…")
	return text + strings.Repeat(" ", max(0, width-lipgloss.Width(text)))
}

// Each colored fragment owns its background. Padding is a separate fragment:
// an inner ANSI reset must never end the selection band for later columns.
func consoleFillRow(text string, width int, style lipgloss.Style) string {
	text = ansi.Truncate(text, max(0, width), style.Render("…"))
	return text + style.Render(strings.Repeat(" ", max(0, width-lipgloss.Width(text))))
}

func consoleSplit(width int) (int, int) {
	available := max(0, width-3)
	left := (available + 1) / 2
	return left, available - left
}

func consoleSection(text string) string {
	return lipgloss.NewStyle().Foreground(Tokyo.Status).Render(consoleMark(iconSection)+" ") + groupStyle.Render(text)
}

func consoleColumns(left, right string, leftWidth, rightWidth, height int) string {
	l, r := strings.Split(left, "\n"), strings.Split(right, "\n")
	lines := make([]string, max(0, height))
	divider := "┊"
	if consoleASCII() {
		divider = "|"
	}
	divider = lipgloss.NewStyle().Foreground(Tokyo.Border).Render(" " + divider + " ")
	for i := range lines {
		a, b := "", ""
		if i < len(l) {
			a = l[i]
		}
		if i < len(r) {
			b = r[i]
		}
		lines[i] = consoleCell(a, leftWidth) + divider + consoleCell(b, rightWidth)
	}
	return strings.Join(lines, "\n")
}

func consoleRows(body string, width, height int) string {
	lines := strings.Split(body, "\n")
	out := make([]string, max(0, height))
	for i := range out {
		if i < len(lines) {
			out[i] = consoleCell(lines[i], width)
		} else {
			out[i] = strings.Repeat(" ", max(0, width))
		}
	}
	return strings.Join(out, "\n")
}

func consoleText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, ansi.Strip(text))
}

func consoleParagraph(text string, width int) string {
	paragraphs := strings.Split(text, "\n")
	for i := range paragraphs {
		paragraphs[i] = consoleText(paragraphs[i])
	}
	return rowStyle.Render(ansi.Wrap(strings.Join(paragraphs, "\n"), max(1, width), ""))
}

func consoleError(text string, width int) string {
	return lipgloss.NewStyle().Foreground(Tokyo.Red).Render(ansi.Strip(consoleParagraph(text, width)))
}

// Blue is structural. State markers use amber, green, red, or neutral gray.
func consoleStateMark(state string, live bool) (string, lipgloss.Color) {
	switch state {
	case "running", "starting", "started":
		mark := consoleGlyph("◐", "*")
		if live {
			mark = consoleActivityMark()
		}
		return mark, Tokyo.Yellow
	case "completed", "succeeded":
		return consoleMark(iconSuccess), Tokyo.Green
	case "failed", "expired":
		return consoleMark(iconFailure), Tokyo.Red
	case "waiting", "blocked":
		return consoleMark(iconAttention), Tokyo.Yellow
	case "queued", "pending":
		return consoleMark(iconPending), Tokyo.Muted
	case "idle":
		return consoleMark(iconIdle), Tokyo.Muted
	case "stopped":
		return consoleMark(iconStopped), Tokyo.Comment
	case "canceled":
		return consoleMark(iconCanceled), Tokyo.Muted
	case "connected", "updated":
		return consoleMark(iconIdle), Tokyo.Muted
	default:
		return consoleMark(iconUnknown), Tokyo.Muted
	}
}

func consoleState(agent model.Agent) (string, string, lipgloss.Color) {
	mark, color := consoleStateMark(agent.Status, true)
	label := consoleText(agent.Status)
	if label == "" {
		label = "unknown"
	}
	return mark, label, color
}

func controlKey(item searchResult) string {
	return string(item.Kind) + ":" + item.ID + ":" + item.WorkspaceParentID + ":" + item.Disclosure
}

func consoleAgentColumns(width int) (int, int) {
	workspace := min(18, max(8, width/4-2))
	return max(12, width-workspace-24), workspace
}

// A continuous guide replaces a repeated child icon. Ancestor guides remain
// only while a later sibling exists. Compute once, before viewport clipping.
func controlTreeGuides(rows []searchResult) []string {
	guides := make([]string, len(rows))
	next := map[int]bool{}
	for i := len(rows) - 1; i >= 0; i-- {
		depth := rows[i].Depth
		for level := range next {
			if level > depth {
				delete(next, level)
			}
		}
		var guide strings.Builder
		for level := 1; level <= min(depth, 5); level++ {
			part := "    "
			if next[level] {
				part = "  " + consoleGlyph("│", "|") + " "
			} else if level == depth {
				part = "  " + consoleGlyph("└", "`") + " "
			}
			guide.WriteString(part)
		}
		guides[i] = guide.String()
		next[depth] = true
	}
	return guides
}

func (m Model) controlRow(item searchResult, guide string, width int, selected bool, agents map[string]model.Agent) string {
	style := rowStyle
	prefix := "  "
	if selected {
		style = selectedStyle.Foreground(Tokyo.Status).Bold(true)
		prefix = consoleMark(iconFocus) + " "
	}
	paint := func(text string, color lipgloss.Color) string { return style.Foreground(color).Render(text) }
	if item.Kind == resultPullRequest {
		return m.pullRequestRowView(item, width, style, prefix)
	}
	name := consoleText(item.Title)
	delegations := ""
	if item.Kind == resultAgent && item.DelegatedCount > 0 && item.WorkspaceParentID == "" {
		delegations = fmt.Sprintf(" %s %d", consoleMark(iconDelegation), item.DelegatedCount)
	}
	text := style.Render(prefix)
	if item.Kind == resultAgent {
		agent := agents[item.ID]
		mark, state, color := consoleState(agent)
		nameWidth, workspaceWidth := consoleAgentColumns(width)
		if item.WorkspaceParentID != "" {
			nameWidth += workspaceWidth + 2
		}
		badge, badgeColor := m.agentPullRequestBadge(item.ID)
		if badge != "" {
			badge = " " + badge
		}
		guide = ansi.Truncate(guide, max(0, nameWidth-lipgloss.Width(delegations)-lipgloss.Width(badge)-8), "")
		nameWidth -= lipgloss.Width(guide) + lipgloss.Width(delegations) + lipgloss.Width(badge)
		text += paint(guide, Tokyo.Muted) + style.Render(consoleCell(name, nameWidth)) + paint(delegations, Tokyo.Muted)
		if badge != "" {
			text += paint(badge, badgeColor)
		}
		text += style.Render("  ")
		if item.WorkspaceParentID == "" {
			text += style.Render(consoleCell(consoleText(item.WorkspaceTitle), workspaceWidth) + "  ")
		}
		text += paint(consoleCell(mark+" "+state, 10), color)
		if width >= 56 {
			age := "?"
			if agent.UpdatedAt > 0 {
				age = strings.TrimSuffix(operationsObservedAge(agent.UpdatedAt), " ago")
			}
			text += style.Render("  ") + paint(consoleCell(age, 6), Tokyo.Muted)
		}
	} else {
		detail := item.Detail
		if item.Kind == resultWorkspace {
			detail = fmt.Sprintf("%d agents", countWorkspaceAgents(m.dashboard, item.ID))
			// Tab and Enter show the agents of a workspace.
			name = consoleMark(iconCollapsed) + " " + name
		}
		if item.Kind == resultDisclosure {
			expanded := m.expandedSearchGroups[item.DisclosureGroup]
			if item.Disclosure == "older-agents" {
				expanded = m.expandedOlderAgents
			}
			marker := iconCollapsed
			if expanded {
				marker = iconExpanded
			}
			text += style.Render(consoleMark(marker) + " " + name)
		} else {
			text += style.Render(consoleCell(name, max(14, min(48, width-22)))+"  ") + paint(ansi.Truncate(consoleText(detail), 28, "…"), Tokyo.Muted)
		}
	}
	return consoleFillRow(text, width, style)
}

func countWorkspaceAgents(d model.Dashboard, id string) int {
	n := 0
	for _, a := range d.Agents {
		if a.WorkspaceID == id {
			n++
		}
	}
	return n
}
