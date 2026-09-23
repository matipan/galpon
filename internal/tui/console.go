package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
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

func controlGroup(item searchResult) string {
	if item.WorkspaceParentID != "" {
		return "WORKSPACES"
	}
	if item.Kind == resultDisclosure {
		return "MORE"
	}
	if item.Kind != resultAgent {
		return groupTitle(item.Kind)
	}
	switch item.AgentState {
	case agentStateFailed:
		return "NEEDS ATTENTION"
	case agentStateWorking:
		return "IN PROGRESS"
	default:
		return "IDLE / RECENT"
	}
}

func controlKey(item searchResult) string {
	return string(item.Kind) + ":" + item.ID + ":" + item.WorkspaceParentID + ":" + item.Disclosure
}

func (m *Model) controlResults(all []searchResult, selected searchResult) []searchResult {
	kind := m.controlKind
	if kind == "" {
		kind = resultAgent
	}
	if normalizedSearchText(m.query.Value()) != "" {
		return m.searchSwitcherResults(all, searchResult{})
	}
	var rows, older []searchResult
	cutoff := time.Now().Add(-switcherOlderAfter).UnixMilli()
	for _, item := range all {
		if item.Kind != kind || (kind == resultAgent && item.Delegated) {
			continue
		}
		if (kind == resultAgent || kind == resultWorktree) && item.ActivityAt > 0 && item.ActivityAt < cutoff && item.AgentState != agentStateFailed && item.AgentState != agentStateWorking {
			older = append(older, item)
		} else {
			rows = append(rows, item)
		}
	}
	if m.controlOrder == nil {
		m.controlOrder = map[string]int{}
		m.controlGroups = map[string]string{}
	}
	if len(m.controlOrder) == 0 {
		sort.SliceStable(rows, func(i, j int) bool {
			rank := func(item searchResult) int {
				switch controlGroup(item) {
				case "NEEDS ATTENTION":
					return 0
				case "IN PROGRESS":
					return 1
				default:
					return 2
				}
			}
			return rank(rows[i]) < rank(rows[j])
		})
	}
	for _, row := range rows {
		key := controlKey(row)
		if _, ok := m.controlOrder[key]; !ok {
			m.controlOrder[key] = len(m.controlOrder)
			m.controlGroups[key] = controlGroup(row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return m.controlOrder[controlKey(rows[i])] < m.controlOrder[controlKey(rows[j])] })
	if kind == resultAgent {
		var visible, more []searchResult
		idle := 0
		idleLimit := max(8, m.height-18)
		for _, item := range rows {
			if controlGroup(item) == "IDLE / RECENT" {
				idle++
			}
			if controlGroup(item) == "IDLE / RECENT" && idle > idleLimit {
				more = append(more, item)
			} else {
				visible = append(visible, item)
			}
		}
		for _, item := range more {
			if selected.Kind == resultAgent && item.ID == selected.ID {
				m.controlMore = true
			}
		}
		if len(more) <= 3 {
			visible = append(visible, more...)
			more = nil
		}
		if len(more) > 0 {
			visible = append(visible, searchResult{Kind: resultDisclosure, Title: fmt.Sprintf("More idle / stopped (%d)", len(more)), Detail: "tab to expand / collapse", Disclosure: "idle-agents"})
			if m.controlMore {
				visible = append(visible, more...)
			}
		}
		rows = visible
	}
	if len(older) != 0 {
		for _, item := range older {
			if item.Kind == selected.Kind && item.ID == selected.ID {
				if kind == resultAgent {
					m.expandedOlderAgents = true
				} else {
					m.expandedOlderWorktrees = true
				}
			}
		}
		disclosure, expanded := "older-agents", m.expandedOlderAgents
		if kind == resultWorktree {
			disclosure, expanded = "older-worktrees", m.expandedOlderWorktrees
		}
		rows = append(rows, searchResult{Kind: resultDisclosure, Title: fmt.Sprintf("Older %s (%d)", strings.ToLower(groupTitle(kind)), len(older)), Detail: "tab to expand / collapse", Disclosure: disclosure})
		if expanded {
			rows = append(rows, older...)
		}
	}
	if kind == resultAgent {
		children := map[string][]searchResult{}
		for _, item := range all {
			if item.Kind == resultAgent && item.ParentAgentID != "" {
				children[item.ParentAgentID] = append(children[item.ParentAgentID], item)
			}
		}
		nested := map[string]bool{}
		var collect func(string, map[string]bool)
		collect = func(id string, path map[string]bool) {
			if !m.expandedAgents[id] || path[id] {
				return
			}
			path[id] = true
			for _, child := range children[id] {
				if !path[child.ID] {
					nested[child.ID] = true
					collect(child.ID, path)
				}
			}
			delete(path, id)
		}
		for _, item := range rows {
			if item.Kind == resultAgent {
				collect(item.ID, map[string]bool{})
			}
		}
		var expanded []searchResult
		for _, item := range rows {
			if item.Kind != resultAgent {
				expanded = append(expanded, item)
				continue
			}
			if !nested[item.ID] {
				expanded = appendAgentResult(expanded, item, 0, children, m.expandedAgents)
			}
		}
		rows = expanded
	}
	return rows
}

func (m *Model) nextControlView() {
	views := []resultKind{resultAgent, resultWorkspace, resultWorktree, resultRepository}
	i := 0
	for index, view := range views {
		if m.controlKind == view {
			i = index
		}
	}
	m.controlKind = views[(i+1)%len(views)]
	m.controlOrder, m.controlGroups = nil, nil
	m.results = nil
	m.cursor = 0
	m.controlScroll = 0
	m.controlDetail = false
	m.refreshResults()
}

func (m Model) controlList(width, height int) string {
	var lines []switcherLine
	last := ""
	querying := normalizedSearchText(m.query.Value()) != ""
	agents := make(map[string]model.Agent, len(m.dashboard.Agents))
	for _, agent := range m.dashboard.Agents {
		agents[agent.ID] = agent
	}
	guides := controlTreeGuides(m.results)
	for i, item := range m.results {
		group := controlGroup(item)
		if querying {
			group = groupTitle(item.Kind)
			if item.DisclosureGroup != "" {
				group = groupTitle(item.DisclosureGroup)
			}
			if item.WorkspaceParentID != "" {
				group = "WORKSPACES"
			}
		}
		// State already has a live column. Group headings are useful only
		// for cross-entity search, not as a second, stale state display.
		if querying && group != last {
			lines = append(lines, switcherLine{value: consoleSection(group), resultIndex: -1, header: true, group: group})
			last = group
		}
		lines = append(lines, switcherLine{value: m.controlRow(item, guides[i], width, i == m.cursor, agents), resultIndex: i, group: group})
	}
	if len(lines) == 0 {
		message := "No " + strings.ToLower(groupTitle(m.controlKind)) + " in this view."
		if m.controlKind == "" {
			message = "No agents yet. Select a workspace or press ctrl+n."
		}
		if len(m.dashboard.Workspaces) == 0 && !querying {
			message = "No workspaces yet. Press ctrl+space, then w to create one."
		}
		if querying {
			message = "No matching titles. Search includes all resource types."
		}
		return consoleParagraph(message, width)
	}
	if !querying && (m.controlKind == "" || m.controlKind == resultAgent) && width >= 56 {
		nameWidth, workspaceWidth := consoleAgentColumns(width)
		header := "  " + consoleCell("NAME", nameWidth) + "  " + consoleCell("WORKSPACE", workspaceWidth) + "  " + consoleCell("STATE", 10) + "  AGE"
		return mutedStyle.Render(header) + "\n" + strings.Join(visibleSwitcherLines(lines, m.cursor, max(1, height-1)), "\n")
	}
	return strings.Join(visibleSwitcherLines(lines, m.cursor, max(1, height)), "\n")
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
		guide = ansi.Truncate(guide, max(0, nameWidth-lipgloss.Width(delegations)-8), "")
		nameWidth -= lipgloss.Width(guide) + lipgloss.Width(delegations)
		text += paint(guide, Tokyo.Muted) + style.Render(consoleCell(name, nameWidth)) + paint(delegations, Tokyo.Muted) + style.Render("  ")
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
			marker := iconCollapsed
			if item.Expanded {
				marker = iconExpanded
			}
			name = consoleMark(marker) + " " + name
		}
		if item.Kind == resultDisclosure {
			expanded := m.expandedSearchGroups[item.DisclosureGroup]
			switch item.Disclosure {
			case "idle-agents":
				expanded = m.controlMore
			case "older-agents":
				expanded = m.expandedOlderAgents
			case "older-worktrees":
				expanded = m.expandedOlderWorktrees
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

func (m Model) controlInspector(width int) string {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		return consoleSection("SELECTED ITEM") + "\n\n" + mutedStyle.Render("Select a row to inspect it.")
	}
	item := m.results[m.cursor]
	lines := []string{consoleSection("DETAIL"), brandStyle.Render(consoleParagraph(item.Title, width)), ""}
	field := func(label, value string) {
		if value == "" {
			return
		}
		labelWidth := min(12, max(8, width/3))
		wrapped := strings.Split(consoleParagraph(value, width-labelWidth-1), "\n")
		if !m.controlDetail && len(wrapped) > 3 {
			wrapped = append(wrapped[:2], mutedStyle.Render("…"))
		}
		for i, text := range wrapped {
			name := ""
			if i == 0 {
				name = label
			}
			lines = append(lines, mutedStyle.Render(consoleCell(name, labelWidth))+" "+text)
		}
	}
	switch item.Kind {
	case resultAgent:
		agent, _ := m.dashboard.Agent(item.ID)
		if m.controlDetail {
			mark, state, color := consoleState(agent)
			lines = append(lines, lipgloss.NewStyle().Foreground(color).Render(mark+" "+state), "")
			field("WORKSPACE", item.WorkspaceTitle)
			field("UPDATED", operationsObservedAge(agent.UpdatedAt))
		}
		field("ERROR", agent.LastError)
		connection := "not connected"
		if agent.RuntimeID != "" {
			connection = "connected"
		}
		field("SESSION", connection)
		field("ROLE", agent.Role)
		if m.operationsAgent == agent.ID {
			if !m.operationsLoaded {
				field("OPERATIONS", "Loading observed facts")
			} else if m.operationsErr != nil {
				field("OPERATIONS", "Unavailable · press o in actions to retry")
			} else {
				if m.operationsRefreshErr != nil {
					field("OPERATIONS", "Refresh failed. Showing prior facts.")
				}
				for i, work := range m.operations.Current {
					if i == 2 {
						break
					}
					field("WORK", work.Title)
					if m.controlDetail {
						field("OBSERVED", work.Observation.State)
					}
					if work.Checkpoint != nil {
						if work.Checkpoint.Summary != work.Title {
							field("REPORTED", work.Checkpoint.Summary)
						}
						field("BLOCKER", work.Checkpoint.Blocker)
					}
				}
				if m.controlDetail || len(m.operations.Current) == 0 {
					for _, fact := range m.operations.DirectOperations {
						field("DIRECT WORK", fact.Title+" · "+fact.State)
					}
				}
				if m.operations.Activity != nil && (m.controlDetail || len(m.operations.Current)+len(m.operations.DirectOperations) == 0) {
					for i, fact := range m.operations.Activity.Facts {
						if i == 2 {
							break
						}
						field("ACTIVITY", fact.Category+" · "+fact.Status+" · "+operationsObservedAge(fact.ObservedAt))
					}
				}
			}
		}
		if agent.CreatedByAgentID != "" {
			if parent, ok := m.dashboard.Agent(agent.CreatedByAgentID); ok {
				field("CREATED BY", parent.Title)
			}
		}
		if agent.ContextAgentID != "" {
			if source, ok := m.dashboard.Agent(agent.ContextAgentID); ok {
				field("CONTEXT", source.Title)
			}
		}
		if m.controlDetail || len(agentWorktreeIDs(agent)) == 0 {
			field("PLACEMENT", agent.Placement.Type)
			field("DIRECTORY", agent.Placement.CWD)
		}
		for _, id := range agentWorktreeIDs(agent) {
			if wt, ok := m.dashboard.Worktree(id); ok {
				repo, _ := m.dashboard.Repository(wt.RepositoryID)
				lines = append(lines, "")
				field("REPOSITORY", repo.Title)
				field("SOURCE", strings.TrimSpace(wt.SourceRemote+" "+wt.BaseRef))
				if m.controlDetail {
					field("BRANCH", wt.Branch)
					field("PATH", wt.Path)
				}
			}
		}
		if item.DelegatedCount > 0 && m.controlDetail {
			lines = append(lines, "", consoleSection("DELEGATIONS"))
			for _, child := range m.dashboard.Agents {
				if child.CreatedByAgentID == agent.ID {
					_, state, _ := consoleState(child)
					lines = append(lines, consoleParagraph(child.Title+" · "+state, width))
				}
			}
		}

	case resultWorkspace:
		if m.controlDetail {
			field("AGENTS", fmt.Sprint(countWorkspaceAgents(m.dashboard, item.ID)))
		}
		worktrees := 0
		for _, wt := range m.dashboard.Worktrees {
			if wt.WorkspaceID == item.ID {
				worktrees++
			}
		}
		field("WORKTREES", fmt.Sprint(worktrees))

	case resultWorktree:
		wt, _ := m.dashboard.Worktree(item.ID)
		repo, _ := m.dashboard.Repository(wt.RepositoryID)
		field("REPOSITORY", repo.Title)
		field("BRANCH", wt.Branch)
		field("SOURCE", wt.SourceRemote+" "+wt.BaseRef)
		field("PATH", wt.Path)

	case resultRepository:
		repo, _ := m.dashboard.Repository(item.ID)
		field("DEFAULT REF", repo.DefaultBranch)
		for _, remote := range repo.Remotes {
			field("REMOTE", remote.Name)
			field("FETCH URL", remote.FetchURL)
		}

	default:
		lines = append(lines, consoleParagraph(item.Detail, width), "", "tab    Expand / collapse")
	}
	for i := range lines {
		lines[i] = rowStyle.Render(lines[i])
	}
	return strings.Join(lines, "\n")
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

func (m Model) viewConsoleSwitcher(width, height int) string {
	inner := max(1, width-4)
	position := ""
	if len(m.results) > 0 {
		position = fmt.Sprintf("%d / %d", m.cursor+1, len(m.results))
	}
	header := titleLine("Control", position, inner)
	var tabs []string
	for _, tab := range []struct {
		kind  resultKind
		label string
	}{{resultAgent, consoleMark(iconAgent) + " Agents"}, {resultWorkspace, consoleMark(iconWorkspace) + " Workspaces"}, {resultWorktree, consoleMark(iconWorktree) + " Worktrees"}, {resultRepository, consoleMark(iconRepository) + " Repositories"}} {
		label := tab.label
		if tab.kind == m.controlKind || m.controlKind == "" && tab.kind == resultAgent {
			label = lipgloss.NewStyle().Foreground(Tokyo.Status).Bold(true).Underline(true).Render(label)
		} else {
			label = mutedStyle.Render(label)
		}
		tabs = append(tabs, label)
	}
	nav := ansi.Truncate(strings.Join(tabs, "   "), inner, "…")
	search := m.query.View()
	if m.normalMode {
		search = mutedStyle.Render("Choose an action. ctrl+space returns to search.")
	}
	bodyHeight := max(1, height-8)
	listWidth := inner
	if inner >= 104 {
		listWidth, _ = consoleSplit(inner)
	}
	body := ""
	if m.controlConfirm != nil {
		action := "Hide"
		if m.controlConfirm.Hidden {
			action = "Restore"
		}
		body = consoleRows(consoleSection("CONFIRM ACTION")+"\n\n"+brandStyle.Render(action+" "+consoleText(m.controlConfirm.Title))+"\n\n"+consoleParagraph("This action can affect dependent resources and their active views. Review the selected item before you continue.", min(76, inner))+"\n\n"+keyHint("enter", strings.ToLower(action))+"    "+keyHint("esc", "cancel"), inner, bodyHeight)
	} else if m.normalMode && !m.controlDetail {
		body = m.consoleActions(inner, bodyHeight)
	} else if m.controlDetail {
		detail := strings.Split(m.controlInspector(min(94, inner)), "\n")
		start := min(m.controlDetailScroll, max(0, len(detail)-bodyHeight))
		body = consoleRows(strings.Join(detail[start:], "\n"), inner, bodyHeight)
	} else if inner >= 104 {
		body = consoleColumns(m.controlList(listWidth, bodyHeight), m.controlInspector(inner-listWidth-3), listWidth, inner-listWidth-3, bodyHeight)
	} else {
		body = consoleRows(m.controlList(listWidth, bodyHeight), inner, bodyHeight)
	}
	notice := "Attention first · ctrl+r reorder · tab expand"
	if m.controlKind != "" && m.controlKind != resultAgent {
		notice = "tab expand · ctrl+r reorder"
	}
	if normalizedSearchText(m.query.Value()) != "" {
		notice = "Search includes agents, workspaces, worktrees and repositories."
	}
	if m.controlDetail {
		notice = "ctrl+g or esc returns to the list"
	}
	if m.status != "" {
		notice = m.status
		if m.busy {
			notice = consoleActivityMark() + " " + notice
		}
	}
	if m.err != nil {
		notice = "! " + m.err.Error()
	}
	footer := footerBar(inner, keyHint("enter", "open"), keyHint("esc", "back / close"), keyHint("ctrl+g", "detail"), keyHint("ctrl+n", "new agent"), keyHint("shift+tab", "views"), keyHint("ctrl+space", "actions"))
	if inner < 104 && m.status == "" && m.err == nil && !m.controlDetail {
		footer = footerBar(inner, keyHint("enter", "open"), keyHint("ctrl+g", "detail"), keyHint("esc", "close"))
		notice = "shift+tab views · ctrl+space actions · tab expand · ctrl+r reorder"
	}
	noticeLine := mutedStyle.Render(ansi.Truncate(notice, inner, "…"))
	if m.busy {
		noticeLine = lipgloss.NewStyle().Foreground(Tokyo.Yellow).Render(ansi.Truncate(notice, inner, "…"))
	}
	if m.err != nil {
		noticeLine = consoleError(notice, inner)
	}
	parts := []string{header, nav, ansi.Truncate(search, inner, "…"), body, ansi.Truncate(noticeLine, inner, "…"), consoleRule(inner), footer}
	return lipgloss.NewStyle().Padding(0, 2).Render(strings.Join(parts, "\n"))
}

func (m Model) consoleActions(width, height int) string {
	selected := "No item selected"
	if m.cursor >= 0 && m.cursor < len(m.results) {
		selected = m.results[m.cursor].Title
	}
	lines := []string{consoleSection("ACTIONS"), "", brandStyle.Render(consoleParagraph(selected, min(width, 90))), "",
		"enter   Open selected item", "ctrl+n  Create agent", "ctrl+f  Fork selected agent", "m       Write a message to selected agent", "o       Inspect observed operations", "t       Open a real terminal", "e       Open an editor", "", consoleSection("ORGANIZE"), "r       Add repository", "R       Add remote", "w       Create workspace", "ctrl+h  Show / hide hidden resources", "", "x       Hide / restore selected item", "", mutedStyle.Render("Closing this view does not stop an agent.")}
	return consoleRows(strings.Join(lines, "\n"), width, height)
}
