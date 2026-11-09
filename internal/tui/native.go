package tui

import (
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

// NativeView is the command center for a native client, such as the Herdr
// app mod. It holds the titles, states and groups of the terminal command
// center, so that both show the same facts. The client keeps the selection,
// the search and the layout.
type NativeView struct {
	// Build is the build of the galpon that made the view. The mods of a
	// screen compare it with their own build.
	Build string `json:"build,omitempty"`
	Now   int64  `json:"now"`
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
	Open string `json:"open,omitempty"`
	// Harness and View of an agent: a Pi agent with view "app" opens in a
	// tab of the Herdr app.
	Harness string `json:"harness,omitempty"`
	View    string `json:"view,omitempty"`
	Hidden  bool   `json:"hidden,omitempty"`
	// Count is the number of agents in a workspace.
	Count      int    `json:"count,omitempty"`
	Group      int    `json:"group,omitempty"`
	Ref        string `json:"ref,omitempty"`
	URL        string `json:"url,omitempty"`
	AgentID    string `json:"agentId,omitempty"`
	AgentTitle string `json:"agentTitle,omitempty"`
}

// NativePullRequests holds the Pull requests view texts.
type NativePullRequests struct {
	Groups     []string `json:"groups"`
	Attention  int      `json:"attention"`
	Notice     string   `json:"notice"`
	Empty      string   `json:"empty"`
	Refreshing bool     `json:"refreshing,omitempty"`
}

func nativeModel(d model.Dashboard, pullRequests model.PullRequests, pullRequestsErr error) Model {
	applyPalette(configuredPalette())
	m := Model{dashboard: d, pullRequests: pullRequests, pullRequestsErr: pullRequestsErr, pullRequestsLoaded: true}
	m.rebuildPullRequestRows()
	return m
}

// BuildNativeView builds the command center rows from the daemon data. A pull
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
		view.Items = append(view.Items, m.nativeItem(item))
	}
	for _, row := range m.pullRequestRows {
		view.Items = append(view.Items, m.nativePullRequest(row))
	}
	return view
}

func (m Model) nativeItem(item searchResult) NativeItem {
	out := NativeItem{
		Kind: string(item.Kind), ID: item.ID, Title: item.Title, Detail: item.Detail, Search: item.Title,
		WorkspaceID: item.WorkspaceID, WorkspaceTitle: item.WorkspaceTitle, ParentAgentID: item.ParentAgentID,
		Delegated: item.Delegated, DelegatedCount: item.DelegatedCount, ActivityAt: item.ActivityAt,
		State: string(item.AgentState), Hidden: item.Hidden,
	}
	if item.SearchText != "" {
		out.Search = item.SearchText
	}
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
		out.Harness, out.View = agent.Harness(), agent.View
	case resultWorkspace:
		out.Count = countWorkspaceAgents(m.dashboard, item.ID)
	}
	return out
}

func (m Model) nativePullRequest(row pullRequestRow) NativeItem {
	pr := row.PR
	mark, color := pullRequestMark(row)
	out := NativeItem{
		Kind: string(resultPullRequest), ID: pr.Key(), Title: pr.Title, Detail: row.Reason,
		Search: row.ref() + " " + pr.Title, Group: row.Group, Ref: row.ref(), URL: pr.URL,
		Mark: mark, Color: string(color), Label: row.Reason, ActivityAt: row.activity(),
		Age:     strings.TrimSuffix(operationsObservedAge(row.activity()), " ago"),
		AgentID: pr.AgentID, AgentTitle: m.pullRequestAgentTitle(pr),
	}
	return out
}
