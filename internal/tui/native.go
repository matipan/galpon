package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

// NativeView is the command center for a native client, such as the Herdr
// app mod. It holds the titles, states, groups and inspector fields of the
// terminal command center, so that both show the same facts. The client keeps
// the selection, the search and the layout.
type NativeView struct {
	Now int64 `json:"now"`
	// OlderAfter folds idle agents and worktrees without activity for longer.
	OlderAfter int64 `json:"olderAfter"`
	// SearchLimit is the number of search results of one kind before a
	// "More" row.
	SearchLimit  int                `json:"searchLimit"`
	Palette      map[string]string  `json:"palette"`
	Items        []NativeItem       `json:"items"`
	PullRequests NativePullRequests `json:"pullRequests"`
}

// NativeItem is one agent, workspace, worktree, repository or pull request.
// Agents, workspaces, worktrees and repositories come in the order of the
// terminal command center.
type NativeItem struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	// Search is the text that a search query matches.
	Search         string `json:"search"`
	WorkspaceID    string `json:"workspaceId,omitempty"`
	WorkspaceTitle string `json:"workspaceTitle,omitempty"`
	ParentAgentID  string `json:"parentAgentId,omitempty"`
	// Delegated agents show only below the agent that created them.
	Delegated      bool  `json:"delegated,omitempty"`
	DelegatedCount int   `json:"delegatedCount,omitempty"`
	ActivityAt     int64 `json:"activityAt,omitempty"`
	// State is the attention state of an agent: active, changed, working,
	// idle or failed. In the sidebar, a pull request has the state of
	// pullRequestTurn.
	State string `json:"state,omitempty"`
	// Label, Mark and Color show the status of an agent or a pull request.
	Label string `json:"label,omitempty"`
	Mark  string `json:"mark,omitempty"`
	Color string `json:"color,omitempty"`
	Age   string `json:"age,omitempty"`
	// Badge is the most urgent pull request of an agent.
	Badge      string `json:"badge,omitempty"`
	BadgeColor string `json:"badgeColor,omitempty"`
	// Open is the terminal app that shows the agent: herdr or herdr-app.
	Open   string `json:"open,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
	// Count is the number of agents in a workspace.
	Count      int           `json:"count,omitempty"`
	Group      int           `json:"group,omitempty"`
	Ref        string        `json:"ref,omitempty"`
	URL        string        `json:"url,omitempty"`
	AgentID    string        `json:"agentId,omitempty"`
	AgentTitle string        `json:"agentTitle,omitempty"`
	Fields     []NativeField `json:"fields,omitempty"`
}

// NativeField is one inspector line: a label and its value, or a section
// title. Break puts a blank line before it.
type NativeField struct {
	Label   string `json:"label,omitempty"`
	Value   string `json:"value"`
	Color   string `json:"color,omitempty"`
	Section bool   `json:"section,omitempty"`
	Break   bool   `json:"break,omitempty"`
}

// NativePullRequests holds the Pull requests view texts.
type NativePullRequests struct {
	Groups     []string `json:"groups"`
	Attention  int      `json:"attention"`
	Notice     string   `json:"notice"`
	Empty      string   `json:"empty"`
	Refreshing bool     `json:"refreshing,omitempty"`
}

// nativeDelegationLimit keeps the inspector of an agent with many delegated
// agents short.
const nativeDelegationLimit = 20

func nativeModel(d model.Dashboard, pullRequests model.PullRequests, pullRequestsErr error) Model {
	applyPalette(configuredPalette())
	m := Model{dashboard: d, pullRequests: pullRequests, pullRequestsErr: pullRequestsErr, pullRequestsLoaded: true}
	m.rebuildPullRequestRows()
	return m
}

// BuildNativeView builds the command center rows from the daemon data,
// without inspector fields. NativeFields gives the fields of one row. A pull
// request error shows in the Pull requests view, as in the terminal.
func BuildNativeView(d model.Dashboard, pullRequests model.PullRequests, pullRequestsErr error) NativeView {
	m := nativeModel(d, pullRequests, pullRequestsErr)
	view := NativeView{
		Now:         time.Now().UnixMilli(),
		OlderAfter:  switcherOlderAfter.Milliseconds(),
		SearchLimit: searchCategoryLimit,
		Palette:     CompanionPalette(),
		PullRequests: NativePullRequests{
			Groups: pullRequestGroups, Attention: m.pullRequestAttention(),
			Notice: m.pullRequestNotice(), Empty: m.pullRequestEmpty(), Refreshing: pullRequests.Refreshing,
		},
	}
	for _, item := range buildResults(d, "") {
		view.Items = append(view.Items, m.nativeItem(item, false))
	}
	for _, row := range m.pullRequestRows {
		view.Items = append(view.Items, m.nativePullRequest(row, false))
	}
	return view
}

// NativeFields gives the inspector fields of one row, or false when the row
// is no longer available. The fields of an agent include the operations facts
// of operations, or a note when operationsErr is set.
func NativeFields(d model.Dashboard, pullRequests model.PullRequests, pullRequestsErr error, kind, id string, operations *model.AgentOperations, operationsErr error) ([]NativeField, bool) {
	m := nativeModel(d, pullRequests, pullRequestsErr)
	if operations != nil || operationsErr != nil {
		m.operationsAgent, m.operationsLoaded, m.operationsErr = id, true, operationsErr
		if operations != nil {
			m.operations = *operations
		}
	}
	if resultKind(kind) == resultPullRequest {
		row, ok := m.pullRequestRow(id)
		return m.nativePullRequest(row, true).Fields, ok
	}
	for _, item := range buildResults(d, "") {
		if string(item.Kind) == kind && item.ID == id {
			return m.nativeItem(item, true).Fields, true
		}
	}
	return nil, false
}

type nativeFields struct {
	list []NativeField
	gap  bool
}

func (f *nativeFields) add(label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	f.list = append(f.list, NativeField{Label: label, Value: value, Break: f.gap && len(f.list) > 0})
	f.gap = false
}

func (f *nativeFields) section(title string) {
	f.list = append(f.list, NativeField{Value: title, Section: true, Break: len(f.list) > 0})
	f.gap = false
}

func (f *nativeFields) line(value, color string) {
	f.list = append(f.list, NativeField{Value: value, Color: color, Break: f.gap && len(f.list) > 0})
	f.gap = false
}

func (m Model) nativeItem(item searchResult, withFields bool) NativeItem {
	out := NativeItem{
		Kind: string(item.Kind), ID: item.ID, Title: item.Title, Detail: item.Detail, Search: item.Title,
		WorkspaceID: item.WorkspaceID, WorkspaceTitle: item.WorkspaceTitle, ParentAgentID: item.ParentAgentID,
		Delegated: item.Delegated, DelegatedCount: item.DelegatedCount, ActivityAt: item.ActivityAt,
		State: string(item.AgentState), Hidden: item.Hidden,
	}
	if item.SearchText != "" {
		out.Search = item.SearchText
	}
	var fields nativeFields
	switch item.Kind {
	case resultAgent:
		agent, _ := m.dashboard.Agent(item.ID)
		mark, label, color := consoleState(agent)
		out.Mark, out.Label, out.Color = mark, label, string(color)
		out.Age = "?"
		if agent.UpdatedAt > 0 {
			out.Age = strings.TrimSuffix(operationsObservedAge(agent.UpdatedAt), " ago")
		}
		badge, badgeColor := m.agentPullRequestBadge(item.ID)
		out.Badge, out.BadgeColor = badge, string(badgeColor)
		if agent.RendererID != "" {
			out.Open = agent.Renderer
		}
		m.nativeAgentFields(&fields, item, agent)
	case resultWorkspace:
		out.Count = countWorkspaceAgents(m.dashboard, item.ID)
		worktrees := 0
		for _, wt := range m.dashboard.Worktrees {
			if wt.WorkspaceID == item.ID {
				worktrees++
			}
		}
		fields.add("AGENTS", fmt.Sprint(out.Count))
		fields.add("WORKTREES", fmt.Sprint(worktrees))
	case resultWorktree:
		wt, _ := m.dashboard.Worktree(item.ID)
		repo, _ := m.dashboard.Repository(wt.RepositoryID)
		fields.add("REPOSITORY", repo.Title)
		fields.add("BRANCH", wt.CheckoutLabel())
		fields.add("SOURCE", strings.TrimSpace(wt.SourceRemote+" "+wt.BaseRef))
		fields.add("PATH", wt.Path)
	case resultRepository:
		repo, _ := m.dashboard.Repository(item.ID)
		fields.add("DEFAULT REF", repo.DefaultBranch)
		for _, remote := range repo.Remotes {
			fields.gap = true
			fields.add("REMOTE", remote.Name)
			fields.add("FETCH URL", remote.FetchURL)
		}
	}
	if withFields {
		out.Fields = fields.list
	}
	return out
}

// nativeAgentFields are the detail fields of the terminal inspector.
func (m Model) nativeAgentFields(fields *nativeFields, item searchResult, agent model.Agent) {
	fields.add("WORKSPACE", item.WorkspaceTitle)
	fields.add("UPDATED", operationsObservedAge(agent.UpdatedAt))
	fields.add("ERROR", agent.LastError)
	connection := "not connected"
	if agent.RuntimeID != "" {
		connection = "connected"
	}
	fields.add("SESSION", connection)
	fields.add("HARNESS", model.HarnessLabel(agent.Kind))
	if agent.RendererID != "" {
		switch agent.Renderer {
		case "herdr":
			fields.add("OPEN IN", "Herdr")
		case "herdr-app":
			fields.add("OPEN IN", "Herdr app")
		default:
			fields.add("OPEN IN", agent.Renderer)
		}
	}
	fields.add("ROLE", agent.Role)
	for _, row := range m.agentPullRequests(agent.ID) {
		fields.add("PR", row.ref()+" · "+row.Reason)
	}
	if m.operationsAgent == agent.ID {
		m.nativeOperationsFields(fields)
	}
	if agent.CreatedByAgentID != "" {
		if parent, ok := m.dashboard.Agent(agent.CreatedByAgentID); ok {
			fields.add("CREATED BY", parent.Title)
		}
	}
	if agent.ContextAgentID != "" {
		if source, ok := m.dashboard.Agent(agent.ContextAgentID); ok {
			fields.add("CONTEXT", source.Title)
		}
	}
	fields.add("PLACEMENT", agent.Placement.Type)
	fields.add("DIRECTORY", agent.Placement.CWD)
	for _, id := range agentWorktreeIDs(agent) {
		if wt, ok := m.dashboard.Worktree(id); ok {
			repo, _ := m.dashboard.Repository(wt.RepositoryID)
			fields.gap = true
			fields.add("REPOSITORY", repo.Title)
			fields.add("SOURCE", strings.TrimSpace(wt.SourceRemote+" "+wt.BaseRef))
			fields.add("BRANCH", wt.CheckoutLabel())
			fields.add("PATH", wt.Path)
		}
	}
	if item.DelegatedCount == 0 {
		return
	}
	fields.section("DELEGATIONS")
	shown := 0
	for _, child := range m.dashboard.Agents {
		if child.CreatedByAgentID != agent.ID {
			continue
		}
		if shown == nativeDelegationLimit {
			fields.line(fmt.Sprintf("%d more", item.DelegatedCount-shown), "")
			break
		}
		mark, state, color := consoleState(child)
		fields.line(mark+" "+child.Title+" · "+state, string(color))
		shown++
	}
}

// nativeOperationsFields are the operations facts of the terminal inspector:
// current work and its reports, direct work and observed activity.
func (m Model) nativeOperationsFields(fields *nativeFields) {
	if m.operationsErr != nil {
		fields.add("OPERATIONS", "Unavailable · press o in actions to retry")
		return
	}
	for i, work := range m.operations.Current {
		if i == 2 {
			break
		}
		fields.add("WORK", work.Title)
		fields.add("OBSERVED", work.Observation.State)
		if work.Checkpoint != nil {
			if work.Checkpoint.Summary != work.Title {
				fields.add("REPORTED", work.Checkpoint.Summary)
			}
			fields.add("BLOCKER", work.Checkpoint.Blocker)
		}
	}
	for _, fact := range m.operations.DirectOperations {
		fields.add("DIRECT WORK", fact.Title+" · "+fact.State)
	}
	if m.operations.Activity != nil {
		for i, fact := range m.operations.Activity.Facts {
			if i == 2 {
				break
			}
			fields.add("ACTIVITY", fact.Category+" · "+fact.Status+" · "+operationsObservedAge(fact.ObservedAt))
		}
	}
}

func (m Model) nativePullRequest(row pullRequestRow, withFields bool) NativeItem {
	pr := row.PR
	mark, color := pullRequestMark(row)
	out := NativeItem{
		Kind: string(resultPullRequest), ID: pr.Key(), Title: pr.Title, Detail: row.Reason,
		Search: row.ref() + " " + pr.Title, Group: row.Group, Ref: row.ref(), URL: pr.URL,
		Mark: mark, Color: string(color), Label: row.Reason, ActivityAt: row.activity(),
		Age:     strings.TrimSuffix(operationsObservedAge(row.activity()), " ago"),
		AgentID: pr.AgentID, AgentTitle: m.pullRequestAgentTitle(pr),
	}
	if withFields {
		var fields nativeFields
		m.pullRequestFields(searchResult{Kind: resultPullRequest, ID: pr.Key()}, fields.add)
		out.Fields = fields.list
	}
	return out
}
