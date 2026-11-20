package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

func TestPullRequestAssignmentTakesThePlaceOfTheCreatorUntilRemoved(t *testing.T) {
	env := newWorktreeTestEnvironment(t)
	ctx := t.Context()
	if err := env.app.Store.PutRepositoryRemote(ctx, env.repository.ID, model.RepositoryRemote{Name: "github", FetchURL: "git@github.com:Acme/Widgets.git"}, false); err != nil {
		t.Fatal(err)
	}
	creator := env.createAgent(t, "Creator", env.repositoryPlacement())
	assignee := env.createAgent(t, "Assignee", env.repositoryPlacement())
	other := env.createAgent(t, "Other", env.repositoryPlacement())
	if err := env.app.Store.RecordConversationScans(ctx, nil, nil, []store.PullRequestCreator{
		{Repository: "acme/widgets", Number: 7, AgentID: creator.ID, CreatedAt: 1000},
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.app.Store.SaveGitHubPullRequests(ctx, "me", 1, []model.PullRequest{
		{Repository: "acme/widgets", Number: 7, Title: "Fix the widgets", State: "OPEN", HeadRef: "fix"},
	}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(env.app)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, httptest.NewRequest(method, path, bytes.NewReader(data)))
		return response
	}
	assign := func(agentID, ref string) *httptest.ResponseRecorder {
		t.Helper()
		return call(http.MethodPost, "/v1/pull-request-assignments", map[string]string{"agentId": agentID, "pullRequest": ref})
	}
	unassign := func(ref string) *httptest.ResponseRecorder {
		t.Helper()
		return call(http.MethodDelete, "/v1/pull-request-assignments?"+url.Values{"pullRequest": {ref}}.Encode(), nil)
	}
	linked := func() (agentID, link string) {
		t.Helper()
		pulls, err := env.app.StoredPullRequests(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(pulls.PullRequests) != 1 {
			t.Fatalf("pull requests = %+v", pulls.PullRequests)
		}
		return pulls.PullRequests[0].AgentID, pulls.PullRequests[0].AgentLink
	}
	lookup := func() []model.PullRequestCreator {
		t.Helper()
		response := call(http.MethodGet, "/v1/pull-request-creators?query=%237", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("lookup status %d: %s", response.Code, response.Body.String())
		}
		var out []model.PullRequestCreator
		if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if agent, link := linked(); agent != creator.ID || link != "created" {
		t.Fatalf("before an assignment the pull request shows %q (%s), want the creator", agent, link)
	}
	for _, invalid := range []struct{ agent, ref string }{
		{assignee.ID, "7"},
		{assignee.ID, "#7"},
		{assignee.ID, "widgets"},
		{"missing-agent", "acme/widgets#7"},
	} {
		if response := assign(invalid.agent, invalid.ref); response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("assign %q to %q = %d %s, want 422", invalid.ref, invalid.agent, response.Code, response.Body.String())
		}
	}

	// The repository name of a reference ignores case, and a URL works too.
	if response := assign(other.ID, "Acme/Widgets#7"); response.Code != http.StatusOK {
		t.Fatalf("assign = %d %s", response.Code, response.Body.String())
	}
	if response := assign(assignee.ID, "https://github.com/acme/widgets/pull/7/files"); response.Code != http.StatusOK {
		t.Fatalf("reassign = %d %s", response.Code, response.Body.String())
	}
	if agent, link := linked(); agent != assignee.ID || link != "assigned" {
		t.Fatalf("after the assignments the pull request shows %q (%s), want the last assignee", agent, link)
	}
	matches := lookup()
	if len(matches) != 2 || matches[0].Agent.ID != assignee.ID || matches[0].Link != "assigned" || matches[1].Agent.ID != creator.ID || matches[1].Link != "created" {
		t.Fatalf("lookup = %+v, want the assignee and then the creator", matches)
	}

	if response := unassign("acme/widgets#7"); response.Code != http.StatusOK {
		t.Fatalf("unassign = %d %s", response.Code, response.Body.String())
	}
	if agent, link := linked(); agent != creator.ID || link != "created" {
		t.Fatalf("after unassign the pull request shows %q (%s), want the creator again", agent, link)
	}
	if response := unassign("acme/widgets#7"); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second unassign = %d %s, want 422", response.Code, response.Body.String())
	}

}
