package app

import (
	"context"
	"time"

	"github.com/matipan/galpon/internal/model"
)

// pullRequestOf parses a pull request reference that names its repository:
// owner/repo#number or a GitHub pull request URL.
func pullRequestOf(ref string) (model.PullRequestReference, error) {
	parsed, ok := model.ParsePullRequestReference(ref)
	if !ok || parsed.Repository == "" {
		return model.PullRequestReference{}, invalidRequestf("use owner/repo#number or a GitHub PR URL")
	}
	return parsed, nil
}

// AssignPullRequest makes an agent the agent of a pull request. The pull
// request then shows the agent, and the agent shows the pull request badge.
// An assignment replaces the agent that the pull request had before, also
// the agent that created it.
func (a *App) AssignPullRequest(ctx context.Context, agentID, ref string) (model.PullRequestReference, error) {
	parsed, err := pullRequestOf(ref)
	if err != nil {
		return parsed, err
	}
	if _, err := a.Store.Agent(ctx, agentID); IsNotFound(err) {
		return parsed, invalidRequestf("agent %q does not exist", agentID)
	} else if err != nil {
		return parsed, err
	}
	return parsed, a.Store.AssignPullRequest(ctx, parsed.Repository, parsed.Number, agentID, time.Now().UnixMilli())
}

// UnassignPullRequest removes the assigned agent of a pull request. The pull
// request then shows the agent that created it, or the agent on its branch.
func (a *App) UnassignPullRequest(ctx context.Context, ref string) (model.PullRequestReference, error) {
	parsed, err := pullRequestOf(ref)
	if err != nil {
		return parsed, err
	}
	removed, err := a.Store.UnassignPullRequest(ctx, parsed.Repository, parsed.Number)
	if err != nil {
		return parsed, err
	}
	if !removed {
		return parsed, invalidRequestf("pull request %s has no assigned agent", parsed)
	}
	return parsed, nil
}
