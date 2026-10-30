package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

func TestLinkPullRequestsFindsTheAgentForEachPullRequest(t *testing.T) {
	dashboard := model.Dashboard{
		Repositories: []model.Repository{{ID: "repo", Title: "widgets", DefaultBranch: "main", DefaultRemote: "origin", Remotes: []model.RepositoryRemote{
			{Name: "origin", FetchURL: "git@github.com:acme/widgets.git"},
			{Name: "fork", FetchURL: "https://github.com/me/widgets"},
		}}},
		Agents: []model.Agent{
			{ID: "original", Title: "Original", CreatedAt: 100},
			{ID: "fork", Title: "Fork of original", CreatedAt: 200},
			{ID: "owner", Title: "Branch owner", CreatedAt: 300, Placement: model.AgentPlacement{PrimaryWorktreeID: "feature-worktree"}},
		},
		Worktrees: []model.Worktree{
			{ID: "feature-worktree", RepositoryID: "repo", CurrentBranch: "feature"},
			{ID: "main-worktree", RepositoryID: "repo", CurrentBranch: "main"},
		},
	}
	snapshot := model.PullRequests{PullRequests: []model.PullRequest{
		{Repository: "acme/widgets", Number: 1, State: "OPEN", HeadRepository: "me/widgets", HeadRef: "copied"},
		{Repository: "acme/widgets", Number: 2, State: "OPEN", HeadRef: "feature"},
		{Repository: "other/tool", Number: 3, State: "OPEN", HeadRef: "feature"},
		{Repository: "acme/widgets", Number: 4, State: "MERGED", HeadRef: "feature"},
		{Repository: "acme/widgets", Number: 5, State: "MERGED", HeadRef: "removed"},
		{Repository: "acme/widgets", Number: 6, State: "OPEN", HeadRef: "main", ReviewRequested: true},
	}}
	creators := map[string][]store.PullRequestCreator{
		// A fork copies the conversation, so both agents have the same result time.
		"acme/widgets#1": {{AgentID: "fork", CreatedAt: 1000}, {AgentID: "original", CreatedAt: 1000}},
		// The creator of pull request 2 no longer exists.
		"acme/widgets#2": {{AgentID: "removed", CreatedAt: 1000}},
	}
	linked := linkPullRequests(snapshot, creators, dashboard)
	got := map[int]model.PullRequest{}
	for _, pr := range linked.PullRequests {
		got[pr.Number] = pr
	}
	for number, want := range map[int]struct{ agent, link, worktree string }{
		1: {"original", "created", ""},
		2: {"owner", "branch", "feature-worktree"},
		4: {"owner", "branch", "feature-worktree"},
		6: {"", "", ""},
	} {
		pr, ok := got[number]
		if !ok {
			t.Fatalf("pull request %d is missing", number)
		}
		if pr.AgentID != want.agent || pr.AgentLink != want.link || pr.WorktreeID != want.worktree || pr.RepositoryID != "repo" {
			t.Errorf("pull request %d = agent %q link %q worktree %q repository %q; want %+v", number, pr.AgentID, pr.AgentLink, pr.WorktreeID, pr.RepositoryID, want)
		}
	}
	if pr := got[1]; pr.AgentTitle != "Original" {
		t.Errorf("agent title = %q, want Original", pr.AgentTitle)
	}
	// Another repository, and a merged branch without a worktree, are not shown.
	for _, number := range []int{3, 5} {
		if _, ok := got[number]; ok {
			t.Errorf("pull request %d is shown", number)
		}
	}
}

func TestSyncPullRequestsRecordsCreatorsFromConversations(t *testing.T) {
	env := newWorktreeTestEnvironment(t)
	ctx := context.Background()
	agent := env.createAgent(t, "Author", env.repositoryPlacement())
	// The test repository has no GitHub remote, so a sync must not call gh.
	env.app.GitHub.Run = func(context.Context, ...string) ([]byte, error) {
		t.Fatal("the sync called gh without a GitHub repository")
		return nil, nil
	}
	sessions := filepath.Join(env.config.StateDir, "agents", agent.ID, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	write := func(lines string, flag int) {
		t.Helper()
		file, err := os.OpenFile(path, flag|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		if _, err := file.WriteString(lines); err != nil {
			t.Fatal(err)
		}
	}
	created := func(id string, number string) string {
		return `{"type":"message","timestamp":"2026-10-01T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"` + id + `","name":"bash","arguments":{"command":"gh pr create --fill"}}]}}
{"type":"message","timestamp":"2026-10-01T10:00:02.000Z","message":{"role":"toolResult","toolCallId":"` + id + `","content":[{"type":"text","text":"https://github.com/acme/widgets/pull/` + number + `\n"}]}}
`
	}
	creatorsOf := func() map[string][]store.PullRequestCreator {
		t.Helper()
		creators, err := env.app.Store.PullRequestCreators(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return creators
	}
	write(created("call-1", "11"), os.O_CREATE|os.O_TRUNC)
	if err := env.app.SyncPullRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if creators := creatorsOf()["acme/widgets#11"]; len(creators) != 1 || creators[0].AgentID != agent.ID {
		t.Fatalf("creators of pull request 11 = %+v, want the agent", creators)
	}
	// A later sync finds a result that the agent saved after the earlier sync,
	// and it keeps one creator for the earlier pull request.
	write(created("call-2", "12"), os.O_APPEND)
	if err := env.app.SyncPullRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if creators := creatorsOf(); len(creators["acme/widgets#11"]) != 1 || len(creators["acme/widgets#12"]) != 1 {
		t.Fatalf("creators after the second sync = %+v", creators)
	}
}
