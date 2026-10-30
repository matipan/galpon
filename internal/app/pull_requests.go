package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/matipan/galpon/internal/github"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

const (
	// pullRequestSyncInterval keeps agent badges current when nobody looks.
	pullRequestSyncInterval = 5 * time.Minute
	// pullRequestFreshFor is the age at which a read starts a sync.
	pullRequestFreshFor = time.Minute
	// mergedPullRequestWindow limits the merged pull requests that can still
	// have a worktree on their branch.
	mergedPullRequestWindow = 14 * 24 * time.Hour
)

// runPullRequestSync runs in the daemon until it closes.
func (a *App) runPullRequestSync() {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-a.backgroundContext.Done():
			return
		case <-timer.C:
		case <-a.pullRequestWake:
		}
		if err := a.SyncPullRequests(a.backgroundContext); err != nil && a.Logger != nil && !errors.Is(err, context.Canceled) {
			a.Logger.Printf("sync pull requests: %v", err)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(pullRequestSyncInterval)
	}
}

// RefreshPullRequests asks the daemon to sync now. A sync that is in progress
// already includes the request.
func (a *App) RefreshPullRequests() {
	select {
	case a.pullRequestWake <- struct{}{}:
	default:
	}
}

// SyncPullRequests reads new conversation lines, then gets the pull requests
// of the gh user from GitHub. It does not call GitHub when no repository has a
// GitHub remote.
func (a *App) SyncPullRequests(ctx context.Context) error {
	a.pullRequestSyncing.Store(true)
	defer a.pullRequestSyncing.Store(false)
	a.pullRequestAttempt.Store(time.Now().UnixMilli())
	if err := a.scanConversations(ctx); err != nil {
		return fmt.Errorf("read agent conversations: %w", err)
	}
	dashboard, err := a.Store.DashboardWithHidden(ctx)
	if err != nil {
		return err
	}
	if !hasGitHubRepository(dashboard.Repositories) {
		return nil
	}
	pullRequests, login, err := a.fetchPullRequests(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.Join(err, a.Store.SaveGitHubSyncError(ctx, err.Error()))
	}
	return a.Store.SaveGitHubPullRequests(ctx, login, time.Now().UnixMilli(), pullRequests)
}

func (a *App) fetchPullRequests(ctx context.Context) ([]model.PullRequest, string, error) {
	current, err := a.Store.GitHubPullRequests(ctx)
	if err != nil {
		return nil, "", err
	}
	login := current.Login
	if login == "" {
		if login, err = a.GitHub.Login(ctx); err != nil {
			return nil, "", err
		}
	}
	since := time.Now().Add(-mergedPullRequestWindow).UTC().Format("2006-01-02")
	searches := []struct {
		query  string
		merged bool
		review bool
	}{
		{query: "is:pr is:open archived:false author:" + login},
		{query: "is:pr is:open archived:false review-requested:" + login, review: true},
		{query: "is:pr is:merged author:" + login + " merged:>=" + since, merged: true},
	}
	results := make([][]model.PullRequest, len(searches))
	errs := make([]error, len(searches))
	var wait sync.WaitGroup
	for i, search := range searches {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[i], errs[i] = a.GitHub.Search(ctx, search.query, search.merged)
			for j := range results[i] {
				results[i][j].ReviewRequested = search.review
			}
		}()
	}
	wait.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, "", err
	}
	seen := map[string]bool{}
	var out []model.PullRequest
	for _, result := range results {
		for _, pr := range result {
			if !seen[pr.Key()] {
				seen[pr.Key()] = true
				out = append(out, pr)
			}
		}
	}
	return out, login, nil
}

// scanConversations reads the new lines of every saved agent conversation and
// records the pull requests that gh pr create returned.
func (a *App) scanConversations(ctx context.Context) error {
	files, err := filepath.Glob(filepath.Join(a.Config.StateDir, "agents", "*", "sessions", "*.jsonl"))
	if err != nil {
		return err
	}
	previous, err := a.Store.ConversationScans(ctx)
	if err != nil {
		return err
	}
	changed := map[string]store.ConversationScan{}
	var creators []store.PullRequestCreator
	present := make(map[string]bool, len(files))
	for _, path := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		present[path] = true
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		state := previous[path]
		if info.Size() == state.ReadBytes {
			continue
		}
		next, creations, err := github.ScanConversation(path, github.ScanState{ReadBytes: state.ReadBytes, Pending: state.Pending})
		if err != nil {
			continue
		}
		if next.ReadBytes == state.ReadBytes && len(next.Pending) == len(state.Pending) && len(creations) == 0 {
			continue
		}
		agentID := filepath.Base(filepath.Dir(filepath.Dir(path)))
		for _, creation := range creations {
			var at int64
			if !creation.At.IsZero() {
				at = creation.At.UnixMilli()
			}
			creators = append(creators, store.PullRequestCreator{Repository: strings.ToLower(creation.Repository), Number: creation.Number, AgentID: agentID, CreatedAt: at})
		}
		changed[path] = store.ConversationScan{ReadBytes: next.ReadBytes, Pending: next.Pending}
	}
	var removed []string
	for path := range previous {
		if !present[path] {
			removed = append(removed, path)
		}
	}
	if len(changed) == 0 && len(removed) == 0 {
		return nil
	}
	return a.Store.RecordConversationScans(ctx, changed, removed, creators)
}

// PullRequests returns the stored pull requests with their Galpon links. A
// read of data older than one minute starts a sync, at most once a minute.
func (a *App) PullRequests(ctx context.Context) (model.PullRequests, error) {
	snapshot, err := a.Store.GitHubPullRequests(ctx)
	if err != nil {
		return model.PullRequests{}, err
	}
	creators, err := a.Store.PullRequestCreators(ctx)
	if err != nil {
		return model.PullRequests{}, err
	}
	dashboard, err := a.Store.DashboardWithHidden(ctx)
	if err != nil {
		return model.PullRequests{}, err
	}
	dashboard = a.withCheckouts(ctx, dashboard)
	out := linkPullRequests(snapshot, creators, dashboard)
	now := time.Now()
	stale := now.Sub(time.UnixMilli(snapshot.FetchedAt)) > pullRequestFreshFor
	attempted := now.Sub(time.UnixMilli(a.pullRequestAttempt.Load())) <= pullRequestFreshFor
	if stale && !attempted && hasGitHubRepository(dashboard.Repositories) {
		a.RefreshPullRequests()
		out.Refreshing = true
	}
	if a.pullRequestSyncing.Load() {
		out.Refreshing = true
	}
	return out, nil
}

func hasGitHubRepository(repositories []model.Repository) bool {
	for _, repository := range repositories {
		for _, remote := range repository.Remotes {
			if github.RepositoryName(remote.FetchURL) != "" {
				return true
			}
		}
	}
	return false
}

// linkPullRequests keeps the pull requests of Galpon repositories and adds
// their agent. The agent whose conversation ran gh pr create comes first. An
// agent with the pull request branch checked out comes next. A merged pull
// request stays only while a worktree is on its branch, and its agent is the
// owner of that worktree.
func linkPullRequests(snapshot model.PullRequests, creators map[string][]store.PullRequestCreator, dashboard model.Dashboard) model.PullRequests {
	repositories := map[string]model.Repository{}
	for _, repository := range dashboard.Repositories {
		for _, remote := range repository.Remotes {
			if name := github.RepositoryName(remote.FetchURL); name != "" {
				repositories[name] = repository
			}
		}
	}
	agents := make(map[string]model.Agent, len(dashboard.Agents))
	owners := map[string][]model.Agent{}
	for _, agent := range dashboard.Agents {
		agents[agent.ID] = agent
		for _, id := range placementWorktreeIDs(agent) {
			owners[id] = append(owners[id], agent)
		}
	}
	for id := range owners {
		sort.SliceStable(owners[id], func(i, j int) bool { return owners[id][i].UpdatedAt > owners[id][j].UpdatedAt })
	}
	branches := map[string][]model.Worktree{}
	for _, worktree := range dashboard.Worktrees {
		if worktree.CurrentBranch != "" {
			key := worktree.RepositoryID + "\x00" + worktree.CurrentBranch
			branches[key] = append(branches[key], worktree)
		}
	}
	onBranch := func(pr model.PullRequest, repository model.Repository) (worktreeID, ownerID string) {
		if pr.HeadRef == "" || pr.HeadRef == repository.DefaultBranch {
			return "", ""
		}
		for _, worktree := range branches[repository.ID+"\x00"+pr.HeadRef] {
			if candidates := owners[worktree.ID]; len(candidates) > 0 {
				return worktree.ID, candidates[0].ID
			}
			worktreeID = worktree.ID
		}
		return worktreeID, ""
	}
	out := model.PullRequests{Login: snapshot.Login, FetchedAt: snapshot.FetchedAt, Error: snapshot.Error, PullRequests: []model.PullRequest{}}
	for _, pr := range snapshot.PullRequests {
		repository, ok := repositories[strings.ToLower(pr.Repository)]
		if !ok {
			repository, ok = repositories[strings.ToLower(pr.HeadRepository)]
		}
		if !ok {
			continue
		}
		pr.RepositoryID = repository.ID
		worktreeID, owner := onBranch(pr, repository)
		pr.WorktreeID = worktreeID
		creator := firstCreator(creators[pr.Key()], agents)
		switch {
		case pr.State == "MERGED" && worktreeID == "":
			continue
		case pr.State == "MERGED" && owner != "":
			pr.AgentID, pr.AgentLink = owner, "branch"
		case creator != "":
			pr.AgentID, pr.AgentLink = creator, "created"
		case owner != "":
			pr.AgentID, pr.AgentLink = owner, "branch"
		}
		pr.AgentTitle = agents[pr.AgentID].Title
		out.PullRequests = append(out.PullRequests, pr)
	}
	return out
}

// firstCreator returns the agent that got the pull request URL first. A fork
// copies the conversation with its times, so the older agent wins a tie.
func firstCreator(creators []store.PullRequestCreator, agents map[string]model.Agent) string {
	creators = append([]store.PullRequestCreator(nil), creators...)
	sort.SliceStable(creators, func(i, j int) bool {
		a, b := creators[i], creators[j]
		if (a.CreatedAt == 0) != (b.CreatedAt == 0) {
			return a.CreatedAt != 0
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		return agents[a.AgentID].CreatedAt < agents[b.AgentID].CreatedAt
	})
	for _, creator := range creators {
		if _, ok := agents[creator.AgentID]; ok {
			return creator.AgentID
		}
	}
	return ""
}

func placementWorktreeIDs(agent model.Agent) []string {
	var out []string
	seen := map[string]bool{}
	if agent.Placement.PrimaryWorktreeID != "" {
		seen[agent.Placement.PrimaryWorktreeID] = true
		out = append(out, agent.Placement.PrimaryWorktreeID)
	}
	for _, assignment := range agent.Placement.Worktrees {
		if assignment.WorktreeID != "" && !seen[assignment.WorktreeID] {
			seen[assignment.WorktreeID] = true
			out = append(out, assignment.WorktreeID)
		}
	}
	return out
}
