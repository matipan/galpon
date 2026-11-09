package tui

import (
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/sessioncost"
)

// NativeSidebar holds the Galpon facts of the sidebar in the Galpon layout of
// the Herdr app. The app gives the open terminals and their status. Galpon
// gives the names, the delegated work and the pull requests.
type NativeSidebar struct {
	// Build is the build of the galpon that made the result. The mods of a
	// screen compare it with their own build.
	Build string `json:"build,omitempty"`
	// Workspaces maps a workspace ID to its title.
	Workspaces map[string]string `json:"workspaces"`
	// Agents maps each requested agent ID to its title and its delegated
	// work. An agent that is hidden or deleted is not in it.
	Agents map[string]NativeSidebarAgent `json:"agents"`
	// PullRequestGroups are the group titles that NativeItem.Group indexes.
	PullRequestGroups []string `json:"pullRequestGroups"`
	// PullRequests are the pull requests in the groups NEEDS YOU, AGENT'S
	// TURN and WAITING, in the order of the command center. A pull request
	// whose agent is hidden has no agent ID, so that it opens on GitHub.
	PullRequests     []NativeItem `json:"pullRequests"`
	PullRequestError string       `json:"pullRequestError,omitempty"`
	// PullRequestsAt is when Galpon last got the pull requests from GitHub.
	PullRequestsAt int64 `json:"pullRequestsAt,omitempty"`
	Refreshing     bool  `json:"refreshing,omitempty"`
	// GitHub is false when no Galpon repository has a GitHub remote.
	GitHub bool `json:"github"`
}

// NativeSidebarAgent is one open agent in the sidebar.
type NativeSidebarAgent struct {
	Title string `json:"title"`
	// Delegating counts the delegated work that is not done yet.
	Delegating int `json:"delegating,omitempty"`
	// Background counts the background tasks that run for a Pi agent.
	Background *model.BackgroundTasks `json:"background,omitempty"`
	// Cost is what the session of a Pi agent cost at API prices.
	Cost *sessioncost.Cost `json:"cost,omitempty"`
	// Harness and View say where the agent opens: a Pi agent with view
	// "app" opens in a tab of the Herdr app.
	Harness string `json:"harness,omitempty"`
	View    string `json:"view,omitempty"`
}

// BuildNativeSidebar builds the sidebar facts for the agents agentIDs.
// delegated holds the delegated work of each agent, and background its
// background tasks.
func BuildNativeSidebar(d model.Dashboard, pullRequests model.PullRequests, pullRequestsErr error, agentIDs []string, delegated map[string]int, background map[string]model.BackgroundTasks) NativeSidebar {
	m := nativeModel(d, pullRequests, pullRequestsErr)
	out := NativeSidebar{
		Workspaces:        make(map[string]string, len(d.Workspaces)),
		Agents:            make(map[string]NativeSidebarAgent, len(agentIDs)),
		PullRequestGroups: pullRequestGroups,
		PullRequests:      []NativeItem{},
		PullRequestError:  m.pullRequestError(),
		PullRequestsAt:    pullRequests.FetchedAt,
		Refreshing:        pullRequests.Refreshing,
		GitHub:            m.hasGitHubRemote(),
	}
	for _, workspace := range d.Workspaces {
		out.Workspaces[workspace.ID] = workspace.Title
	}
	for _, id := range agentIDs {
		if agent, ok := d.Agent(id); ok {
			item := NativeSidebarAgent{Title: agent.Title, Delegating: delegated[id], Harness: agent.Harness(), View: agent.View}
			if tasks, ok := background[id]; ok {
				item.Background = &tasks
			}
			out.Agents[id] = item
		}
	}
	for _, row := range m.pullRequestRows {
		if row.Group > pullRequestWaiting {
			continue
		}
		item := m.nativePullRequest(row)
		item.State = pullRequestTurn(row)
		if _, ok := d.Agent(item.AgentID); !ok {
			item.AgentID = ""
		}
		out.PullRequests = append(out.PullRequests, item)
	}
	return out
}
