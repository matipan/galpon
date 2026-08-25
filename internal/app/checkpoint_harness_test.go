package app

import (
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func TestCheckpointExternalSessionIdentityValidationPreservesPi(t *testing.T) {
	workspace := model.Workspace{ID: "11111111-1111-4111-8111-111111111111", Title: "Work"}
	base := model.Agent{ID: "22222222-2222-4222-8222-222222222222", WorkspaceID: workspace.ID, Title: "Agent", Placement: model.AgentPlacement{Type: "none", CWD: "/work"}}
	validate := func(agent model.Agent) error {
		return validateCheckpointGraph(model.DurableState{Workspaces: []model.Workspace{workspace}, Agents: []model.Agent{agent}}, nil)
	}
	pi := base
	pi.Kind = "pi"
	pi.SessionID = "legacy-pi-session"
	if err := validate(pi); err != nil {
		t.Fatalf("legacy Pi session was rejected: %v", err)
	}
	codex := base
	codex.Kind = "codex"
	codex.SessionID = "not-a-uuid"
	if err := validate(codex); err == nil {
		t.Fatal("invalid Codex session was accepted")
	}
	claude := base
	claude.Kind = "claude"
	if err := validate(claude); err == nil {
		t.Fatal("missing Claude session was accepted")
	}
	claude.SessionID = "11111111-1111-4111-8111-111111111111"
	if err := validate(claude); err != nil {
		t.Fatalf("valid Claude session was rejected: %v", err)
	}
}

func TestCheckpointResourceIDsMustBeUUIDs(t *testing.T) {
	state := model.DurableState{Repositories: []model.Repository{{ID: "../../escape", Remotes: []model.RepositoryRemote{{Name: "origin", FetchURL: "git@example.invalid:repo", PushURL: "git@example.invalid:repo"}}}}}
	if err := validateCheckpointGraph(state, nil); err == nil {
		t.Fatal("checkpoint accepted a repository ID that can escape the managed directory")
	}
}
