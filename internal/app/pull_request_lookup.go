package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/matipan/galpon/internal/model"
)

// LookupPullRequestCreators reads the saved assignments and creator records
// without calling GitHub or scanning conversations. The assigned agent of a
// PR comes before its creator. A copied fork keeps the original creator.
func (a *App) LookupPullRequestCreators(ctx context.Context, query string) ([]model.PullRequestCreator, error) {
	ref, ok := model.ParsePullRequestReference(query)
	if !ok {
		return nil, fmt.Errorf("use a PR number, owner/repo#number, or a GitHub PR URL")
	}
	creators, err := a.Store.PullRequestCreators(ctx)
	if err != nil {
		return nil, err
	}
	assignments, err := a.Store.PullRequestAssignments(ctx)
	if err != nil {
		return nil, err
	}
	dashboard, err := a.Store.DashboardWithHidden(ctx)
	if err != nil {
		return nil, err
	}
	agents := make(map[string]model.Agent, len(dashboard.Agents))
	for _, agent := range dashboard.Agents {
		agents[agent.ID] = agent
	}
	workspaces := make(map[string]string, len(dashboard.Workspaces))
	for _, workspace := range dashboard.Workspaces {
		workspaces[workspace.ID] = workspace.Title
	}
	matches := func(repository string, number int) bool {
		return number == ref.Number && (ref.Repository == "" || strings.EqualFold(repository, ref.Repository))
	}
	out := []model.PullRequestCreator{}
	seen := map[string]bool{}
	add := func(repository string, number int, link, id string) {
		agent, ok := agents[id]
		key := model.PullRequestKey(repository, number) + "\x00" + id
		if !ok || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, model.PullRequestCreator{
			Repository: strings.ToLower(repository), Number: number, Link: link,
			Agent: agent, WorkspaceTitle: workspaces[agent.WorkspaceID],
			RepositoryTitles: dashboard.AgentRepositoryTitles(agent),
		})
	}
	for _, assignment := range assignments {
		if matches(assignment.Repository, assignment.Number) {
			add(assignment.Repository, assignment.Number, "assigned", assignment.AgentID)
		}
	}
	for _, records := range creators {
		if len(records) == 0 || !matches(records[0].Repository, records[0].Number) {
			continue
		}
		if id := firstCreator(records, agents); id != "" {
			add(records[0].Repository, records[0].Number, "created", id)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Ref() != out[j].Ref() {
			return out[i].Ref() < out[j].Ref()
		}
		return out[i].Link == "assigned" && out[j].Link != "assigned"
	})
	return out, nil
}
