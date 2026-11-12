package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/matipan/galpon/internal/model"
)

// LookupPullRequestCreators reads the saved creator records without calling
// GitHub or scanning conversations. A copied fork keeps the original creator.
func (a *App) LookupPullRequestCreators(ctx context.Context, query string) ([]model.PullRequestCreator, error) {
	ref, ok := model.ParsePullRequestReference(query)
	if !ok {
		return nil, fmt.Errorf("use a PR number, owner/repo#number, or a GitHub PR URL")
	}
	creators, err := a.Store.PullRequestCreators(ctx)
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
	out := []model.PullRequestCreator{}
	for _, records := range creators {
		if len(records) == 0 {
			continue
		}
		pr := records[0]
		if pr.Number != ref.Number || ref.Repository != "" && !strings.EqualFold(pr.Repository, ref.Repository) {
			continue
		}
		id := firstCreator(records, agents)
		if id == "" {
			continue
		}
		agent := agents[id]
		out = append(out, model.PullRequestCreator{
			Repository: strings.ToLower(pr.Repository), Number: pr.Number,
			Agent: agent, WorkspaceTitle: workspaces[agent.WorkspaceID],
			RepositoryTitles: dashboard.AgentRepositoryTitles(agent),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref() < out[j].Ref() })
	return out, nil
}
