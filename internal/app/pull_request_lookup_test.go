package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

func TestPullRequestCreatorLookupUsesSavedHistoryAcrossRepositories(t *testing.T) {
	env := newWorktreeTestEnvironment(t)
	ctx := t.Context()
	original := env.createAgent(t, "Engine startup", env.repositoryPlacement())
	fork := env.createAgent(t, "Fork with copied history", env.repositoryPlacement())
	other := env.createAgent(t, "Another repository", env.repositoryPlacement())
	if _, err := env.app.DeleteResource(ctx, "agent", original.ID); err != nil {
		t.Fatal(err)
	}
	if err := env.app.Store.RecordConversationScans(ctx, nil, nil, []store.PullRequestCreator{
		{Repository: "dagger/dagger", Number: 14964, AgentID: fork.ID, CreatedAt: 1000},
		{Repository: "dagger/dagger", Number: 14964, AgentID: original.ID, CreatedAt: 1000},
		{Repository: "acme/tools", Number: 14964, AgentID: other.ID, CreatedAt: 2000},
	}); err != nil {
		t.Fatal(err)
	}
	// A merged PR can leave the GitHub cache. Its saved creator remains useful.
	if err := env.app.Store.SaveGitHubPullRequests(ctx, "user", 1, []model.PullRequest{
		{Repository: "dagger/dagger", Number: 14964, State: "MERGED"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.app.Store.SaveGitHubPullRequests(ctx, "user", 2, nil); err != nil {
		t.Fatal(err)
	}
	env.app.GitHub.Run = func(context.Context, ...string) ([]byte, error) {
		t.Error("a local creator lookup called GitHub")
		return nil, nil
	}
	server := NewServer(env.app)
	lookup := func(query string) []model.PullRequestCreator {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/v1/pull-request-creators?query="+url.QueryEscape(query), nil)
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("lookup status %d: %s", response.Code, response.Body.String())
		}
		var matches []model.PullRequestCreator
		if err := json.Unmarshal(response.Body.Bytes(), &matches); err != nil {
			t.Fatal(err)
		}
		return matches
	}
	matches := lookup("#14964")
	if len(matches) != 2 || matches[0].Repository != "acme/tools" || matches[0].Agent.ID != other.ID || matches[1].Agent.ID != original.ID {
		t.Fatalf("ambiguous number matched %+v", matches)
	}
	for _, query := range []string{"Dagger/Dagger#14964", "https://github.com/dagger/dagger/pull/14964/files#diff-abc"} {
		matches := lookup(query)
		if len(matches) != 1 || matches[0].Agent.ID != original.ID || matches[0].Agent.Title != original.Title || !matches[0].Agent.Hidden || matches[0].WorkspaceTitle != env.workspace.Title {
			t.Fatalf("lookup %q = %+v, want the hidden original creator", query, matches)
		}
	}
	if matches := lookup("dagger/dagger#42"); len(matches) != 0 {
		t.Fatalf("unknown PR matched %+v", matches)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/pull-request-creators?query=engine", nil)
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid reference status = %d, want 400", response.Code)
	}
}
