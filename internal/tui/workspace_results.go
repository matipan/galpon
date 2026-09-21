package tui

import (
	"fmt"
	"sort"
)

// Workspace expansion is a flat list of all its available agents, independent
// of title matches, older-item groups, and delegated-agent expansion. Apply it
// after category limits so children do not consume workspace search slots.
func (m *Model) workspaceSwitcherResults(all []searchResult) []searchResult {
	hasWorkspace := false
	for _, result := range m.results {
		if result.Kind == resultWorkspace {
			hasWorkspace = true
			break
		}
	}
	if !hasWorkspace {
		return m.results
	}
	agents := all
	if normalizedSearchText(m.query.Value()) != "" {
		agents = buildAgentResults(m.dashboard, "")
	}
	children := make(map[string][]searchResult)
	for _, agent := range agents {
		if agent.Kind == resultAgent {
			children[agent.WorkspaceID] = append(children[agent.WorkspaceID], agent)
		}
	}
	out := make([]searchResult, 0, len(m.results))
	for _, result := range m.results {
		if result.Kind != resultWorkspace {
			out = append(out, result)
			continue
		}
		group := children[result.ID]
		result.Expanded = m.expandedWorkspaces[result.ID]
		label := "agents"
		if len(group) == 1 {
			label = "agent"
		}
		action := "expand"
		if result.Expanded {
			action = "collapse"
		}
		result.Detail = hiddenDetail(fmt.Sprintf("%d %s · tab to %s", len(group), label, action), result.Hidden)
		out = append(out, result)
		if !result.Expanded {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool { return agentResultLess(group[i], group[j]) })
		for _, agent := range group {
			agent.Depth = 1
			agent.WorkspaceParentID = result.ID
			out = append(out, agent)
		}
	}
	return out
}
