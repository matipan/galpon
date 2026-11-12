package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/matipan/galpon/internal/model"
)

// The list of the command center: one search field and one list, without
// view tabs and without an inspector, as in the command center of the Herdr
// app.
//
// With an empty search, NEEDS YOU holds the failed agents and the pull
// requests that need the person, RECENT holds the other agents with the
// most recent first, and BROWSE opens all pull requests or all workspaces.
// Rows keep their places and their groups until ctrl+r. A search finds
// agents, pull requests, workspaces, commands, worktrees and repositories,
// and its last row starts a new agent with the search text as its name. Tab
// on an agent opens a peek with its last reply and the last message to it;
// Tab on a workspace shows its agents. Ctrl+K lists the actions of the
// selected row and the Galpon commands.

const (
	resultScope   resultKind = "scope"
	resultLaunch  resultKind = "launch"
	resultCommand resultKind = "command"

	groupNeedsYou = "NEEDS YOU"
	groupRecent   = "RECENT"
	groupBrowse   = "BROWSE"

	scopeWorkspace    = "workspace"
	scopePullRequests = "pull-requests"
	scopeWorkspaces   = "workspaces"

	// commandScore is the lowest search score of a command: a command shows
	// only for a search that its name contains.
	commandScore = 700_000
	// briefAge is how long a peek shows a brief before it reads it again.
	briefAge = 4 * time.Second

	searchPlaceholder = "Search agents, pull requests and workspaces"
)

// listScope narrows the list to the agents of one workspace, all pull
// requests or all workspaces.
type listScope struct {
	Kind  string
	ID    string
	Title string
}

// agentBriefState is the last reply and the last message of an agent, as
// its peek shows them.
type agentBriefState struct {
	brief   model.AgentBrief
	err     error
	loading bool
	at      time.Time
}

type briefMsg struct {
	agentID string
	brief   model.AgentBrief
	err     error
}

// listCommand is a Galpon command that a search finds by its name.
type listCommand struct {
	ID, Title, Detail, Key string
}

var listCommands = []listCommand{
	{"new-agent", "New agent", "Name it, then write its task in it", "ctrl+n"},
	{"pull-requests", "Pull requests", "All open pull requests", "alt+p"},
	{"workspaces", "Workspaces", "All workspaces and their agents", ""},
	{"new-workspace", "New workspace", "Group related agent and human work", "w"},
	{"new-repository", "Add repository", "A local path or a Git URL", "r"},
	{"new-remote", "Add remote", "A named source for a repository", "R"},
	{"hidden", "Show hidden items", "Hidden agents, workspaces, worktrees and repositories", "ctrl+h"},
	{"refresh", "Refresh pull requests", "Read the pull requests from GitHub again", ""},
}

// listAction is an action of the actions list: Ctrl+K.
type listAction struct {
	ID, Label, Key string
	Galpon         bool
}

func pullRequestResult(row pullRequestRow) searchResult {
	return searchResult{
		Kind: resultPullRequest, ID: row.PR.Key(), Title: row.PR.Title, Detail: row.Reason,
		ActivityAt: row.activity(), SearchText: row.ref() + " " + row.PR.Title,
	}
}

// keepOrder sorts items by less the first time that it sees them, and then
// keeps that order: a new item goes after the known ones.
func keepOrder(order map[string]int, items []searchResult, less func(a, b searchResult) bool) {
	sorted := append([]searchResult(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })
	for _, item := range sorted {
		if _, ok := order[controlKey(item)]; !ok {
			order[controlKey(item)] = len(order)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return order[controlKey(items[i])] < order[controlKey(items[j])] })
}

// keepGroup keeps the first group of an item while the list is open, so
// that a row does not move when its state changes.
func (m *Model) keepGroup(item searchResult, group string) string {
	if m.homeGroups == nil {
		m.homeGroups = map[string]string{}
	}
	key := controlKey(item)
	if known, ok := m.homeGroups[key]; ok {
		return known
	}
	m.homeGroups[key] = group
	return group
}

// homeResults are the rows without a search and without a scope.
func (m *Model) homeResults(all []searchResult, selected searchResult) []searchResult {
	if m.homeOrder == nil {
		m.homeOrder = map[string]int{}
	}
	cutoff := time.Now().Add(-switcherOlderAfter).UnixMilli()
	var needs, recent, older []searchResult
	for _, item := range all {
		if item.Kind != resultAgent {
			continue
		}
		current := groupRecent
		if item.AgentState == agentStateFailed {
			current = groupNeedsYou
		}
		if m.keepGroup(item, current) == groupNeedsYou {
			needs = append(needs, item)
			continue
		}
		if item.Delegated {
			continue
		}
		if item.ActivityAt > 0 && item.ActivityAt < cutoff && item.AgentState != agentStateWorking && item.AgentState != agentStateFailed {
			older = append(older, item)
			if selected.Kind == resultAgent && selected.ID == item.ID {
				m.expandedOlderAgents = true
			}
		} else {
			recent = append(recent, item)
		}
	}
	for _, row := range m.pullRequestRows {
		if row.Group != pullRequestNeedsYou {
			continue
		}
		if item := pullRequestResult(row); m.keepGroup(item, groupNeedsYou) == groupNeedsYou {
			needs = append(needs, item)
		}
	}
	// Agents come before pull requests, and the one that waited longest
	// comes first.
	keepOrder(m.homeOrder, needs, func(a, b searchResult) bool {
		if (a.Kind == resultAgent) != (b.Kind == resultAgent) {
			return a.Kind == resultAgent
		}
		return a.ActivityAt < b.ActivityAt
	})
	var out []searchResult
	for _, item := range needs {
		item.Group = groupNeedsYou
		out = append(out, item)
	}
	keepOrder(m.homeOrder, recent, agentResultLess)
	for _, item := range m.nestDelegated(all, recent) {
		item.Group = groupRecent
		out = append(out, item)
	}
	if len(older) > 0 {
		keepOrder(m.homeOrder, older, agentResultLess)
		detail := "tab to show"
		if m.expandedOlderAgents {
			detail = "tab to fold"
		}
		out = append(out, searchResult{Kind: resultDisclosure, Title: fmt.Sprintf("Older agents (%d)", len(older)), Detail: detail, Disclosure: "older-agents", Group: groupRecent})
		if m.expandedOlderAgents {
			for _, item := range m.nestDelegated(all, older) {
				item.Group = groupRecent
				out = append(out, item)
			}
		}
	}
	pulls := fmt.Sprintf("%d open", len(m.pullRequestRows))
	if attention := m.pullRequestAttention(); attention > 0 {
		pulls += fmt.Sprintf(" · %d need a step", attention)
	}
	out = append(out,
		searchResult{Kind: resultScope, ID: scopePullRequests, Title: "Pull requests", Detail: pulls, Group: groupBrowse},
		searchResult{Kind: resultScope, ID: scopeWorkspaces, Title: "Workspaces", Detail: fmt.Sprint(len(m.dashboard.Workspaces)), Group: groupBrowse},
	)
	return out
}

// nestDelegated puts the agents that an expanded agent created below it.
func (m *Model) nestDelegated(all, rows []searchResult) []searchResult {
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
		collect(item.ID, map[string]bool{})
	}
	var out []searchResult
	for _, item := range rows {
		if !nested[item.ID] {
			out = appendAgentResult(out, item, 0, children, m.expandedAgents)
		}
	}
	return out
}

// searchListResults are the rows of a search: each kind in its group, the
// best matches first, and a last row that starts a new agent.
func (m *Model) searchListResults(all []searchResult, query string, selected searchResult) []searchResult {
	items := append([]searchResult(nil), all...)
	var pulls, commands []searchResult
	for _, row := range m.pullRequestRows {
		item := pullRequestResult(row)
		if score, ok := fuzzyScore(item.SearchText, query); ok {
			item.Score = score
			pulls = append(pulls, item)
		}
	}
	for _, command := range listCommands {
		title := command.Title
		if command.ID == "hidden" && m.showHidden {
			title = "Hide hidden items"
		}
		if score, ok := fuzzyScore(title, query); ok && score >= commandScore {
			commands = append(commands, searchResult{Kind: resultCommand, ID: command.ID, Title: title, Detail: command.Detail, SearchText: command.Key, Score: score})
		}
	}
	for _, group := range [][]searchResult{pulls, commands} {
		sort.SliceStable(group, func(i, j int) bool { return group[i].Score > group[j].Score })
		items = append(items, group...)
	}
	sort.SliceStable(items, func(i, j int) bool { return resultOrder(items[i]) < resultOrder(items[j]) })
	m.holdSearchOrder(items)
	rows := m.searchSwitcherResults(items, selected)
	for i := range rows {
		kind := rows[i].Kind
		if kind == resultDisclosure {
			kind = rows[i].DisclosureGroup
		}
		rows[i].Group = groupTitle(kind)
	}
	return append(rows, searchResult{Kind: resultLaunch, Title: strings.TrimSpace(query)})
}

// scopeResults are the rows of a scope. The search narrows them.
func (m *Model) scopeResults(all []searchResult, query string) []searchResult {
	var rows []searchResult
	switch m.scope.Kind {
	case scopeWorkspace:
		for _, item := range all {
			if item.Kind == resultAgent && item.WorkspaceID == m.scope.ID {
				item.WorkspaceParentID = m.scope.ID
				rows = append(rows, item)
			}
		}
		if normalizedSearchText(query) == "" {
			if m.homeOrder == nil {
				m.homeOrder = map[string]int{}
			}
			keepOrder(m.homeOrder, rows, agentResultLess)
		}
		rows = append(rows, searchResult{Kind: resultLaunch, Title: strings.TrimSpace(query), WorkspaceID: m.scope.ID, Detail: m.scope.Title})
	case scopePullRequests:
		counts := map[int]int{}
		for _, row := range m.pullRequestRows {
			counts[row.Group]++
		}
		for _, row := range m.pullRequestRows {
			item := pullRequestResult(row)
			if _, ok := fuzzyScore(item.SearchText, query); !ok {
				continue
			}
			item.Group = fmt.Sprintf("%s · %d", pullRequestGroups[row.Group], counts[row.Group])
			rows = append(rows, item)
		}
	case scopeWorkspaces:
		for _, item := range all {
			if item.Kind == resultWorkspace {
				rows = append(rows, item)
			}
		}
	}
	return rows
}

// enterScope shows one workspace, all pull requests or all workspaces.
func (m *Model) enterScope(scope listScope) {
	m.scope = &scope
	switch scope.Kind {
	case scopeWorkspace:
		m.query.Placeholder = "Search its agents"
	case scopePullRequests:
		m.query.Placeholder = "Search pull requests"
	default:
		m.query.Placeholder = "Search workspaces"
	}
	m.actionsOpen = false
	m.peek = ""
	m.query.SetValue("")
	m.resultsQuery = "\x00"
	m.status = ""
	m.refreshResults()
	m.cursor = 0
}

// leaveScope shows the list again, with the row of the scope selected.
func (m *Model) leaveScope() {
	left := m.scope
	m.scope = nil
	m.query.Placeholder = searchPlaceholder
	m.peek = ""
	m.query.SetValue("")
	m.resultsQuery = "\x00"
	m.refreshResults()
	m.cursor = 0
	for index, item := range m.results {
		if left != nil && (left.Kind == scopeWorkspace && item.Kind == resultWorkspace && item.ID == left.ID || item.Kind == resultScope && item.ID == left.Kind) {
			m.cursor = index
			break
		}
	}
}

func (m Model) selectedResult() (searchResult, bool) {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		return searchResult{}, false
	}
	return m.results[m.cursor], true
}

// activateSelected is Enter on a row.
func (m *Model) activateSelected() tea.Cmd {
	selected, ok := m.selectedResult()
	if !ok {
		return nil
	}
	switch selected.Kind {
	case resultDisclosure:
		m.toggleSwitcherExpansion()
		return nil
	case resultScope:
		m.enterScope(listScope{Kind: selected.ID, Title: selected.Title})
		return nil
	case resultLaunch:
		m.beginNewAgent(selected.Title, selected.WorkspaceID)
		return nil
	case resultCommand:
		return m.runListAction(selected.ID)
	}
	if selected.Hidden {
		m.selectedHiddenBlocked()
		return nil
	}
	switch selected.Kind {
	case resultAgent:
		return m.openAgent(selected.ID)
	case resultPullRequest:
		return m.openPullRequest(selected)
	case resultWorkspace:
		m.enterScope(listScope{Kind: scopeWorkspace, ID: selected.ID, Title: selected.Title})
		return nil
	case resultWorktree:
		return m.openSelected(selected, nil)
	case resultRepository:
		return m.beginTerminal(selected, nil)
	}
	return nil
}

// expandSelected is Tab on a row: the peek of an agent, the agents of a
// workspace, a scope, or more rows.
func (m *Model) expandSelected() tea.Cmd {
	selected, ok := m.selectedResult()
	if !ok {
		return nil
	}
	switch selected.Kind {
	case resultAgent:
		if m.peek == selected.ID {
			m.peek = ""
			return nil
		}
		m.peek = selected.ID
		return m.loadBrief(selected.ID, false)
	case resultWorkspace:
		m.enterScope(listScope{Kind: scopeWorkspace, ID: selected.ID, Title: selected.Title})
	case resultScope:
		m.enterScope(listScope{Kind: selected.ID, Title: selected.Title})
	case resultDisclosure:
		m.toggleSwitcherExpansion()
	}
	return nil
}

// beginNewAgent opens New agent with a name. The agent opens when the form
// is sent, and the person writes its task there.
func (m *Model) beginNewAgent(name, workspaceID string) {
	if workspaceID == "" {
		workspaceID = m.selectedWorkspace()
	}
	if workspaceID == "" {
		workspaceID = m.recentWorkspace()
	}
	if workspaceID == "" {
		m.status = "Create a workspace first: ctrl+k, then w"
		return
	}
	sourceAgentID, sourceWorktreeID := "", ""
	if selected, ok := m.selectedResult(); ok && selected.WorkspaceID == workspaceID {
		switch selected.Kind {
		case resultAgent:
			sourceAgentID = selected.ID
		case resultWorktree:
			sourceWorktreeID = selected.WorktreeID
		}
	}
	m.beginAgentFormWithSource(workspaceID, "", sourceAgentID, sourceWorktreeID)
	m.agentDraft.Name = strings.TrimSpace(name)
	m.agentFocus = 0
	m.loadAgentInput()
}

// recentWorkspace is the workspace of the agent with the most recent
// activity, else the first workspace.
func (m Model) recentWorkspace() string {
	best, at := "", int64(-1)
	for _, agent := range m.dashboard.Agents {
		if _, ok := m.dashboard.Workspace(agent.WorkspaceID); ok && agent.UpdatedAt > at {
			best, at = agent.WorkspaceID, agent.UpdatedAt
		}
	}
	if best == "" && len(m.dashboard.Workspaces) > 0 {
		best = m.dashboard.Workspaces[0].ID
	}
	return best
}

// loadBrief reads the last reply and the last message of an agent.
func (m *Model) loadBrief(agentID string, force bool) tea.Cmd {
	if m.client == nil || agentID == "" {
		return nil
	}
	if m.briefs == nil {
		m.briefs = map[string]agentBriefState{}
	}
	known := m.briefs[agentID]
	if known.loading || !force && !known.at.IsZero() && time.Since(known.at) < briefAge {
		return nil
	}
	known.loading = true
	m.briefs[agentID] = known
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		brief, err := client.AgentBrief(ctx, agentID)
		return briefMsg{agentID: agentID, brief: brief, err: err}
	}
}

func (m *Model) applyBrief(value briefMsg) {
	if m.briefs == nil {
		m.briefs = map[string]agentBriefState{}
	}
	known := m.briefs[value.agentID]
	known.loading, known.at, known.err = false, time.Now(), value.err
	if value.err == nil {
		known.brief = value.brief
	}
	m.briefs[value.agentID] = known
}

// --- actions: ctrl+k

// listActions are the actions of the selected row, then the Galpon
// commands. A key runs its action while the list shows.
func (m Model) listActions() []listAction {
	var out []listAction
	add := func(id, label, key string) { out = append(out, listAction{ID: id, Label: label, Key: key}) }
	if item, ok := m.selectedResult(); ok {
		resource := item.Kind == resultAgent || item.Kind == resultWorkspace || item.Kind == resultWorktree || item.Kind == resultRepository
		switch {
		case item.Hidden && resource:
			add("hide", "Unhide", "x")
		case item.Kind == resultAgent:
			add("open", "Open", "enter")
			add("message", "Message", "m")
			add("terminal", "Open terminal", "t")
			add("editor", "Open editor", "e")
			add("new-agent-here", "New agent in this workspace", "a")
			add("fork", "Fork agent", "f")
			add("rename", "Rename", "n")
			add("operations", "Operations", "o")
			if item.DelegatedCount > 0 {
				label := "Show delegated agents"
				if m.expandedAgents[item.ID] {
					label = "Hide delegated agents"
				}
				add("delegated", label, "d")
			}
			add("hide", "Hide", "x")
		case item.Kind == resultPullRequest:
			if row, ok := m.pullRequestRow(item.ID); ok {
				if _, found := m.dashboard.Agent(row.PR.AgentID); found {
					add("open", "Open its agent", "enter")
				}
			}
			add("github", "Open on GitHub", "g")
		case item.Kind == resultWorkspace:
			add("open", "Show its agents", "enter")
			add("new-agent-here", "New agent here", "a")
			add("terminal", "Open terminal", "t")
			add("editor", "Open editor", "e")
			add("hide", "Hide", "x")
		case item.Kind == resultWorktree:
			add("terminal", "Open terminal", "t")
			add("editor", "Open editor", "e")
			add("new-agent-here", "New agent here", "a")
			add("hide", "Hide", "x")
		case item.Kind == resultRepository:
			add("terminal", "New worktree", "t")
			add("editor", "New worktree in the editor", "e")
			add("hide", "Hide", "x")
		}
	}
	own := len(out)
	for _, command := range listCommands {
		label := command.Title
		if command.ID == "hidden" && m.showHidden {
			label = "Hide hidden items"
		}
		add(command.ID, label, command.Key)
	}
	for i := own; i < len(out); i++ {
		out[i].Galpon = true
	}
	return out
}

func (m *Model) openActions() tea.Cmd {
	m.actionsOpen = true
	m.actionsCursor = 0
	m.peek = ""
	m.query.Blur()
	return nil
}

func (m *Model) closeActions() tea.Cmd {
	m.actionsOpen = false
	return m.query.Focus()
}

func (m *Model) updateActions(key tea.KeyMsg) tea.Cmd {
	actions := m.listActions()
	switch key.String() {
	case "up", "ctrl+p":
		m.actionsCursor = max(0, m.actionsCursor-1)
		return nil
	case "down":
		m.actionsCursor = min(len(actions)-1, m.actionsCursor+1)
		return nil
	case "esc", "ctrl+k", "ctrl+@":
		return m.closeActions()
	case "enter":
		if m.actionsCursor < 0 || m.actionsCursor >= len(actions) {
			return nil
		}
		m.closeActions()
		return m.runListAction(actions[m.actionsCursor].ID)
	}
	for _, action := range actions {
		if len([]rune(action.Key)) == 1 && action.Key == key.String() {
			m.closeActions()
			return m.runListAction(action.ID)
		}
	}
	return nil
}

// runListAction runs an action of the actions list or a command.
func (m *Model) runListAction(id string) tea.Cmd {
	m.actionsOpen = false
	selected, hasSelection := m.selectedResult()
	needsItem := func() bool {
		if !hasSelection {
			m.status = "Select an item first"
			return false
		}
		return m.selectedSwitcherActionable() && !m.selectedHiddenBlocked()
	}
	switch id {
	case "open":
		return m.activateSelected()
	case "message":
		if !needsItem() {
			return nil
		}
		return m.beginMessage()
	case "terminal", "editor":
		if !needsItem() {
			return nil
		}
		var command []string
		if id == "editor" {
			command = EditorCommand()
		}
		return m.beginTerminal(selected, command)
	case "operations":
		if !needsItem() || selected.Kind != resultAgent {
			return nil
		}
		return m.beginOperations(selected.ID)
	case "rename":
		if !needsItem() || selected.Kind != resultAgent {
			return nil
		}
		m.beginRenameForm(selected.ID)
	case "fork":
		if !needsItem() || selected.Kind != resultAgent {
			return nil
		}
		m.beginAgentForkForm(selected.ID)
	case "new-agent-here":
		workspaceID := m.selectedWorkspace()
		if workspaceID == "" {
			m.status = "Select a workspace first"
			return nil
		}
		suggested := ""
		if hasSelection && selected.Kind == resultWorktree {
			suggested = selected.WorktreeID
		}
		m.beginAgentForm(workspaceID, suggested)
	case "delegated":
		if hasSelection && selected.Kind == resultAgent {
			m.expandedAgents[selected.ID] = !m.expandedAgents[selected.ID]
			m.refreshResults()
		}
	case "hide":
		if hasSelection && m.selectedSwitcherActionable() {
			m.controlConfirm = &selected
		}
		return nil
	case "github":
		if hasSelection && selected.Kind == resultPullRequest {
			return m.openPullRequestOnGitHub(selected)
		}
	case "new-agent":
		workspaceID := ""
		if m.scope != nil && m.scope.Kind == scopeWorkspace {
			workspaceID = m.scope.ID
		}
		m.beginNewAgent("", workspaceID)
	case "pull-requests":
		m.enterScope(listScope{Kind: scopePullRequests, Title: "Pull requests"})
	case "workspaces":
		m.enterScope(listScope{Kind: scopeWorkspaces, Title: "Workspaces"})
	case "new-workspace":
		m.beginForm(formWorkspace, "Workspace title", "")
	case "new-repository":
		m.beginForm(formRepository, "Local path or Git URL", "")
	case "new-remote":
		if len(m.dashboard.Repositories) == 0 {
			m.status = "Add a repository first"
			return nil
		}
		m.beginRemoteForm(selected)
	case "hidden":
		m.showHidden = !m.showHidden
		if m.showHidden {
			m.status = "Hidden items show · unhide one from its actions"
		} else {
			m.status = "Hidden items no longer show"
		}
		return m.loadDashboard()
	case "refresh":
		m.status = "Reading the pull requests from GitHub…"
		return m.refreshPullRequests()
	}
	return m.focusSwitcher()
}

// hideSelected hides the row that the confirmation names, or unhides it.
func (m *Model) hideSelected(selected searchResult) tea.Cmd {
	m.controlConfirm = nil
	m.busy = true
	m.busyTicks = 0
	m.err = nil
	client := m.client
	if selected.Hidden {
		m.status = "Unhiding " + selected.Title + "…"
		return func() tea.Msg {
			value, err := client.RestoreResource(context.Background(), string(selected.Kind), selected.ID)
			return restoreMsg{value: value, title: selected.Title, err: err}
		}
	}
	m.status = "Hiding " + selected.Title + "…"
	return func() tea.Msg {
		value, err := client.DeleteResource(context.Background(), string(selected.Kind), selected.ID)
		return deleteMsg{value: value, title: selected.Title, err: err}
	}
}

// --- drawing

// screenHeader is the top line of a screen: a chip that names the screen or
// the scope, then its field or a quiet text.
func screenHeader(title, body string, width int) string {
	chip := ""
	if title != "" {
		chip = lipgloss.NewStyle().Foreground(Tokyo.StatusInk).Background(Tokyo.Status).Bold(true).Render(" "+consoleGlyph("‹", "<")+" "+consoleText(title)+" ") + " "
	}
	// The field pads itself to its width, so the line cuts without a mark.
	return ansi.Truncate(chip+body, width, "") + "\n" + consoleRule(width)
}

func (m Model) viewConsoleSwitcher(width, height int) string {
	inner := max(1, width-4)
	title, body := "", m.query.View()
	if m.scope != nil {
		title = m.scope.Title
	}
	if m.actionsOpen {
		title, body = "Actions", mutedStyle.Render("↑↓ select · enter or a key runs · esc closes")
	}
	bodyHeight := max(1, height-5)
	var list string
	switch {
	case m.controlConfirm != nil:
		list = m.confirmList(inner)
	case m.actionsOpen:
		list = m.actionsList(inner, bodyHeight)
	default:
		list = m.controlList(inner, bodyHeight)
	}
	notice, color := "", Tokyo.Muted
	if m.scope != nil && m.scope.Kind == scopePullRequests {
		notice = m.pullRequestNotice()
	}
	if m.status != "" {
		notice = m.status
		if m.busy {
			notice, color = consoleActivityMark()+" "+notice, Tokyo.Yellow
		}
	}
	if m.err != nil {
		notice, color = "! "+m.err.Error(), Tokyo.Red
	}
	parts := []string{
		screenHeader(title, body, inner),
		consoleRows(list, inner, bodyHeight),
		lipgloss.NewStyle().Foreground(color).Render(ansi.Truncate(notice, inner, "…")),
		footerBar(inner, m.listHints()...),
	}
	return lipgloss.NewStyle().Padding(0, 2).Render(strings.Join(parts, "\n"))
}

// listHints are the keys of the footer: the verb of Enter and of Tab for
// the selected row, then the actions, a new agent and Esc.
func (m Model) listHints() []string {
	if m.controlConfirm != nil {
		verb := "hide"
		if m.controlConfirm.Hidden {
			verb = "unhide"
		}
		return []string{keyHint("enter", verb), keyHint("esc", "cancel")}
	}
	if m.actionsOpen {
		return []string{keyHint("enter", "run"), keyHint("↑↓", "select"), keyHint("esc", "close")}
	}
	enter, tab := "open", ""
	if item, ok := m.selectedResult(); ok {
		switch item.Kind {
		case resultAgent:
			tab = "peek"
			if m.peek == item.ID {
				tab = "close peek"
			}
		case resultWorkspace:
			enter, tab = "show", "show"
		case resultScope:
			enter, tab = "show", "show"
		case resultDisclosure:
			enter, tab = "show", "show"
		case resultLaunch:
			enter = "new agent"
		case resultCommand:
			enter = "run"
		case resultRepository:
			enter = "new worktree"
		case resultWorktree:
			enter = "terminal"
		}
	}
	hints := []string{keyHint("enter", enter)}
	if tab != "" {
		hints = append(hints, keyHint("tab", tab))
	}
	escape := "close"
	if m.peek != "" {
		escape = "close peek"
	} else if m.scope != nil {
		escape = "back"
	}
	return append(hints, keyHint("ctrl+k", "actions"), keyHint("ctrl+n", "new agent"), keyHint("esc", escape))
}

func (m Model) controlList(width, height int) string {
	if len(m.results) == 0 {
		message := "Nothing here yet."
		switch {
		case m.pullRequestLookupNotice() != "":
			message = m.pullRequestLookupNotice()
		case !m.loaded:
			message = "Loading…"
		case len(m.dashboard.Workspaces) == 0:
			message = "No workspaces yet. Press ctrl+k, then w, to create one."
		case m.scope != nil && m.scope.Kind == scopePullRequests && normalizedSearchText(m.query.Value()) == "":
			message = m.pullRequestEmpty()
		case m.scope != nil:
			message = "Nothing matches."
		}
		return consoleParagraph(message, width)
	}
	agents := make(map[string]model.Agent, len(m.dashboard.Agents))
	for _, agent := range m.dashboard.Agents {
		agents[agent.ID] = agent
	}
	if m.prLookup != nil {
		for _, creator := range m.prLookup.creators {
			agents[creator.Agent.ID] = creator.Agent
		}
	}
	guides := controlTreeGuides(m.results)
	var lines []switcherLine
	last := ""
	for i, item := range m.results {
		if item.Group != last {
			if item.Group != "" {
				lines = append(lines, switcherLine{value: consoleSection(item.Group), resultIndex: -1, header: true, group: item.Group})
			} else if len(lines) > 0 {
				lines = append(lines, switcherLine{resultIndex: -1, header: true})
			}
			last = item.Group
		}
		selected := i == m.cursor
		lines = append(lines, switcherLine{value: m.listRow(item, guides[i], width, selected, agents), resultIndex: i, group: item.Group})
		if item.CreatorRef != "" {
			text := "      Created " + consoleText(item.CreatorRef)
			if item.Hidden {
				text += " · Hidden"
			}
			lines = append(lines, switcherLine{value: mutedStyle.Render(ansi.Truncate(text, width, "…")), resultIndex: i, group: item.Group})
		}
		if item.Kind == resultAgent && item.Group == groupNeedsYou {
			if text := m.needsLine(item, width, agents); text != "" {
				lines = append(lines, switcherLine{value: text, resultIndex: i, group: item.Group})
			}
		}
		if item.Kind == resultAgent && m.peek == item.ID {
			for _, text := range m.peekLines(item, width) {
				lines = append(lines, switcherLine{value: text, resultIndex: i, group: item.Group})
			}
		}
	}
	return strings.Join(visibleSwitcherLines(lines, m.cursor, max(1, height)), "\n")
}

// listRow is one row. Agents, pull requests, workspaces, worktrees and
// repositories have the columns of controlRow.
func (m Model) listRow(item searchResult, guide string, width int, selected bool, agents map[string]model.Agent) string {
	switch item.Kind {
	case resultScope, resultLaunch, resultCommand:
	default:
		return m.controlRow(item, guide, width, selected, agents)
	}
	style := rowStyle
	prefix := "  "
	if selected {
		style = selectedStyle.Foreground(Tokyo.Status).Bold(true)
		prefix = consoleMark(iconFocus) + " "
	}
	title, detail, mark := consoleText(item.Title), consoleText(item.Detail), consoleMark(iconCollapsed)
	switch item.Kind {
	case resultLaunch:
		mark = consoleMark(iconAdd)
		title = "New agent"
		if item.Title != "" {
			title = "New agent “" + consoleText(item.Title) + "”"
		} else if item.Detail != "" {
			title = "New agent in " + consoleText(item.Detail)
		}
		detail = "ctrl+n"
	case resultCommand:
		mark = consoleGlyph("⚡", "*")
		if item.SearchText != "" {
			detail += "  ·  " + item.SearchText
		}
	}
	text := style.Render(prefix) + style.Foreground(Tokyo.Status).Render(mark+" ") + style.Render(consoleCell(title, max(14, min(52, width-30)))+"  ") +
		style.Foreground(Tokyo.Muted).Render(ansi.Truncate(detail, max(0, width-lipgloss.Width(prefix)-min(52, width-30)-6), "…"))
	return consoleFillRow(text, width, style)
}

// needsLine is the second line of an agent that needs the person: why.
func (m Model) needsLine(item searchResult, width int, agents map[string]model.Agent) string {
	agent := agents[item.ID]
	if item.AgentState != agentStateFailed {
		return ""
	}
	reason := strings.TrimSpace(strings.SplitN(agent.LastError, "\n", 2)[0])
	if reason == "" {
		reason = "It stopped with an error."
	}
	text := "      " + lipgloss.NewStyle().Foreground(Tokyo.Red).Render("Failed") + mutedStyle.Render("  "+consoleText(reason))
	return ansi.Truncate(text, width, "…")
}

// peekLines show the last reply of an agent and the last message to it,
// below its row.
func (m Model) peekLines(item searchResult, width int) []string {
	indent := "      "
	inner := max(16, width-len(indent)-2)
	label := func(text string) string { return indent + groupStyle.Render(text) }
	body := func(lines []string, color lipgloss.Color) []string {
		var out []string
		for _, line := range lines {
			for _, part := range strings.Split(consoleParagraph(consoleText(line), inner), "\n") {
				out = append(out, indent+lipgloss.NewStyle().Foreground(color).Render(part))
			}
		}
		return out
	}
	known, ok := m.briefs[item.ID]
	var out []string
	if !ok || known.loading && known.at.IsZero() {
		out = append(out, indent+mutedStyle.Render("Reading the conversation…"))
	}
	if known.err != nil {
		out = append(out, indent+lipgloss.NewStyle().Foreground(Tokyo.Red).Render(ansi.Truncate("! "+known.err.Error(), inner, "…")))
	}
	brief := known.brief
	if brief.Reply != "" {
		heading := "Last reply"
		if brief.ReplyAt > 0 {
			heading += " · " + operationsObservedAge(brief.ReplyAt)
		}
		out = append(out, label(heading))
		reply := body(excerpt(brief.Reply, 8), Tokyo.Foreground)
		out = append(out, reply[:min(len(reply), 10)]...)
	}
	if brief.Prompt != "" {
		text, from := DeliveryText(brief.Prompt)
		heading := "Your last message"
		if from != "" {
			heading = "Message from " + from
		}
		if brief.PromptAt > 0 {
			heading += " · " + operationsObservedAge(brief.PromptAt)
		}
		out = append(out, label(heading))
		prompt := body(excerpt(text, 2), Tokyo.Muted)
		out = append(out, prompt[:min(len(prompt), 3)]...)
	}
	if item.DelegatedCount > 0 {
		noun := "agents"
		if item.DelegatedCount == 1 {
			noun = "agent"
		}
		out = append(out, indent+mutedStyle.Render(fmt.Sprintf("%d delegated %s · ctrl+k shows them", item.DelegatedCount, noun)))
	}
	if ok && !known.at.IsZero() && brief.Reply == "" && brief.Prompt == "" && known.err == nil {
		out = append(out, indent+mutedStyle.Render("No messages yet."))
	}
	return out
}

// actionsList draws the actions of the selected row, then the Galpon
// commands, each with its key.
func (m Model) actionsList(width, height int) string {
	actions := m.listActions()
	title := ""
	if item, ok := m.selectedResult(); ok && item.Kind != resultDisclosure && item.Kind != resultLaunch {
		title = consoleText(item.Title)
	}
	var lines []switcherLine
	for i, action := range actions {
		if i == 0 && !action.Galpon && title != "" {
			lines = append(lines, switcherLine{value: consoleSection(title), resultIndex: -1, header: true})
		}
		if action.Galpon && (i == 0 || !actions[i-1].Galpon) {
			lines = append(lines, switcherLine{value: consoleSection("GALPON"), resultIndex: -1, header: true, group: "galpon"})
		}
		style := rowStyle
		prefix := "  "
		if i == m.actionsCursor {
			style = selectedStyle.Foreground(Tokyo.Status).Bold(true)
			prefix = consoleMark(iconFocus) + " "
		}
		group := ""
		if action.Galpon {
			group = "galpon"
		}
		keyWidth := 8
		text := style.Render(prefix+consoleCell(action.Label, max(8, width-keyWidth-4))) + style.Foreground(Tokyo.Muted).Render(consoleCell(action.Key, keyWidth))
		lines = append(lines, switcherLine{value: consoleFillRow(text, width, style), resultIndex: i, group: group})
	}
	return strings.Join(visibleSwitcherLines(lines, m.actionsCursor, max(1, height)), "\n")
}

// confirmList asks before Galpon hides or unhides a row.
func (m Model) confirmList(width int) string {
	item := m.controlConfirm
	verb, text := "Hide", "Galpon stops showing it and the items that depend on it. Their files stay, and you can unhide them later."
	if item.Hidden {
		verb, text = "Unhide", "It and its related items show again in Galpon."
	}
	row := selectedStyle.Foreground(Tokyo.Status).Bold(true).Render(consoleCell(consoleMark(iconFocus)+" "+verb+" "+consoleText(item.Title), max(1, width-8))) + mutedStyle.Render("  enter")
	return strings.Join([]string{consoleSection(strings.ToUpper(verb)), "", consoleParagraph(text, width), "", row}, "\n")
}
