package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/matipan/galpon/internal/model"
)

var (
	errActiveContextMissing   = errors.New("Galpon cannot identify the active Herdr pane; close the popup and try again")   //nolint:staticcheck // Galpon is a proper name
	errActiveContextMalformed = errors.New("Galpon could not read the active Herdr context; close the popup and try again") //nolint:staticcheck // Galpon is a proper name
	errActiveContextStale     = errors.New("the active Herdr context is no longer current; close the popup and try again")
	errActiveContextAmbiguous = errors.New("Galpon found more than one match for the active Herdr pane; close the popup and try again") //nolint:staticcheck // Galpon is a proper name
	errActiveContextUnmanaged = errors.New("the active Herdr pane is not in a current Galpon workspace")
	errActiveContextNonAgent  = errors.New("operations is available only from a current Galpon agent pane")
)

// ActiveContext is the underlying Herdr pane context supplied to a popup
// command. Popup processes do not have HERDR_PANE_ID.
type ActiveContext struct {
	WorkspaceID     string
	TabID           string
	PaneID          string
	PaneCWD         string
	SocketPath      string
	BinPath         string
	RendererContext string
}

type sessionFact struct {
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
}

type paneFact struct {
	PaneID       string       `json:"pane_id"`
	WorkspaceID  string       `json:"workspace_id"`
	TabID        string       `json:"tab_id"`
	AgentSession *sessionFact `json:"agent_session"`
}

type workspaceFact struct {
	WorkspaceID string `json:"workspace_id"`
}

type tabFact struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
}

type sessionSnapshot struct {
	Workspaces []workspaceFact `json:"workspaces"`
	Tabs       []tabFact       `json:"tabs"`
	Panes      []paneFact      `json:"panes"`
	Agents     []paneFact      `json:"agents"`
}

type snapshotResponse struct {
	Result struct {
		Type     string          `json:"type"`
		Snapshot sessionSnapshot `json:"snapshot"`
	} `json:"result"`
}

// ActiveContextFromEnv reads the Herdr popup contract without using the pane
// variables that belong to ordinary terminal processes.
func ActiveContextFromEnv() (ActiveContext, error) {
	return activeContextFromEnv(os.Getenv)
}

func activeContextFromEnv(getenv func(string) string) (ActiveContext, error) {
	value := ActiveContext{
		WorkspaceID:     strings.TrimSpace(getenv("HERDR_ACTIVE_WORKSPACE_ID")),
		TabID:           strings.TrimSpace(getenv("HERDR_ACTIVE_TAB_ID")),
		PaneID:          strings.TrimSpace(getenv("HERDR_ACTIVE_PANE_ID")),
		PaneCWD:         strings.TrimSpace(getenv("HERDR_ACTIVE_PANE_CWD")),
		SocketPath:      strings.TrimSpace(getenv("HERDR_SOCKET_PATH")),
		BinPath:         strings.TrimSpace(getenv("HERDR_BIN_PATH")),
		RendererContext: strings.TrimSpace(getenv("HERDR_SESSION")),
	}
	if value.RendererContext == "" {
		value.RendererContext = "default"
	}
	for _, field := range []string{value.WorkspaceID, value.TabID, value.PaneID, value.PaneCWD, value.SocketPath, value.BinPath} {
		if field == "" {
			return ActiveContext{}, errActiveContextMissing
		}
		if strings.ContainsFunc(field, unicode.IsControl) {
			return ActiveContext{}, errActiveContextMalformed
		}
	}
	if !filepath.IsAbs(value.PaneCWD) || !filepath.IsAbs(value.SocketPath) {
		return ActiveContext{}, errActiveContextMalformed
	}
	return value, nil
}

func parseSessionSnapshot(data []byte) (sessionSnapshot, error) {
	var response snapshotResponse
	if err := json.Unmarshal(data, &response); err != nil || response.Result.Type != "session_snapshot" {
		return sessionSnapshot{}, errActiveContextMalformed
	}
	return response.Result.Snapshot, nil
}

func loadSessionSnapshot(ctx context.Context, active ActiveContext) (sessionSnapshot, error) {
	command := exec.CommandContext(ctx, active.BinPath, "api", "snapshot")
	command.Env = append(os.Environ(), "HERDR_SOCKET_PATH="+active.SocketPath)
	output, err := command.Output()
	if err != nil {
		return sessionSnapshot{}, errActiveContextStale
	}
	return parseSessionSnapshot(output)
}

func activePaneFacts(snapshot sessionSnapshot, active ActiveContext) (paneFact, *paneFact, error) {
	panes := matchingPanes(snapshot.Panes, active.PaneID)
	if len(panes) == 0 {
		return paneFact{}, nil, errActiveContextStale
	}
	if len(panes) != 1 {
		return paneFact{}, nil, errActiveContextAmbiguous
	}
	pane := panes[0]
	if pane.WorkspaceID != active.WorkspaceID || pane.TabID != active.TabID {
		return paneFact{}, nil, errActiveContextStale
	}
	if countWorkspaceFacts(snapshot.Workspaces, active.WorkspaceID) != 1 || countTabFacts(snapshot.Tabs, active.TabID, active.WorkspaceID) != 1 {
		return paneFact{}, nil, errActiveContextStale
	}
	agents := matchingPanes(snapshot.Agents, active.PaneID)
	if len(agents) > 1 {
		return paneFact{}, nil, errActiveContextAmbiguous
	}
	if len(agents) == 1 {
		agent := agents[0]
		if agent.WorkspaceID != active.WorkspaceID || agent.TabID != active.TabID {
			return paneFact{}, nil, errActiveContextStale
		}
		return pane, &agent, nil
	}
	return pane, nil, nil
}

func matchingPanes(values []paneFact, paneID string) []paneFact {
	matches := make([]paneFact, 0, 1)
	for _, value := range values {
		if value.PaneID == paneID {
			matches = append(matches, value)
		}
	}
	return matches
}

func countWorkspaceFacts(values []workspaceFact, id string) int {
	count := 0
	for _, value := range values {
		if value.WorkspaceID == id {
			count++
		}
	}
	return count
}

func countTabFacts(values []tabFact, tabID, workspaceID string) int {
	count := 0
	for _, value := range values {
		if value.TabID == tabID && value.WorkspaceID == workspaceID {
			count++
		}
	}
	return count
}

// ResolveNewAgentWorkspace validates the popup context and returns only the
// Galpon workspace to preselect in the New Agent form.
func ResolveNewAgentWorkspace(ctx context.Context, dashboard model.Dashboard) (string, error) {
	workspaceID, _, err := ResolveNewAgentContext(ctx, dashboard)
	return workspaceID, err
}

// ResolveNewAgentContext also returns the current agent when the active pane is
// an agent pane. The caller uses its source repository as the new default.
func ResolveNewAgentContext(ctx context.Context, dashboard model.Dashboard) (string, model.Agent, error) {
	active, err := ActiveContextFromEnv()
	if err != nil {
		return "", model.Agent{}, err
	}
	snapshot, err := loadSessionSnapshot(ctx, active)
	if err != nil {
		return "", model.Agent{}, err
	}
	_, activeAgent, err := activePaneFacts(snapshot, active)
	if err != nil {
		return "", model.Agent{}, err
	}
	workspaceID, err := resolveWorkspace(dashboard, active, activeAgent)
	if err != nil {
		return "", model.Agent{}, err
	}
	if activeAgent == nil {
		return workspaceID, model.Agent{}, nil
	}
	agent, err := resolveAgent(dashboard, active, *activeAgent)
	if errors.Is(err, errActiveContextNonAgent) {
		return workspaceID, model.Agent{}, nil
	}
	if err != nil {
		return "", model.Agent{}, err
	}
	return workspaceID, agent, nil
}

// ResolveOperationsAgent validates that the underlying pane contains a current
// Galpon Pi agent. It returns the agent and workspace used by the TUI route.
func ResolveOperationsAgent(ctx context.Context, dashboard model.Dashboard) (model.Agent, string, error) {
	active, err := ActiveContextFromEnv()
	if err != nil {
		return model.Agent{}, "", err
	}
	snapshot, err := loadSessionSnapshot(ctx, active)
	if err != nil {
		return model.Agent{}, "", err
	}
	_, activeAgent, err := activePaneFacts(snapshot, active)
	if err != nil {
		return model.Agent{}, "", err
	}
	if activeAgent == nil {
		return model.Agent{}, "", errActiveContextNonAgent
	}
	agent, err := resolveAgent(dashboard, active, *activeAgent)
	if err != nil {
		return model.Agent{}, "", err
	}
	workspace, err := resolveWorkspace(dashboard, active, activeAgent)
	if err != nil {
		return model.Agent{}, "", err
	}
	if agent.WorkspaceID != workspace {
		return model.Agent{}, "", errActiveContextStale
	}
	return agent, workspace, nil
}

func resolveWorkspace(dashboard model.Dashboard, active ActiveContext, activeAgent *paneFact) (string, error) {
	if activeAgent != nil && activeAgent.AgentSession != nil {
		session := activeAgent.AgentSession
		if session.Kind == "path" {
			if strings.TrimSpace(session.Value) == "" {
				return "", errActiveContextMalformed
			}
			agents := agentsBySessionPath(dashboard.Agents, session.Value)
			if len(agents) == 0 {
				return "", errActiveContextStale
			}
			if len(agents) > 1 {
				return "", errActiveContextAmbiguous
			}
			if agents[0].Kind != "pi" {
				return "", errActiveContextNonAgent
			}
			if _, ok := dashboard.Workspace(agents[0].WorkspaceID); !ok {
				return "", errActiveContextStale
			}
			if mapped := workspacesByRenderer(dashboard.Workspaces, active); len(mapped) > 1 || (len(mapped) == 1 && mapped[0].ID != agents[0].WorkspaceID) {
				return "", errActiveContextAmbiguous
			}
			return agents[0].WorkspaceID, nil
		}
		if session.Kind != "id" {
			return "", errActiveContextMalformed
		}
	}
	workspaceMatches := workspacesByRenderer(dashboard.Workspaces, active)
	if len(workspaceMatches) > 1 {
		return "", errActiveContextAmbiguous
	}
	agentMatches := []model.Agent(nil)
	if activeAgent != nil {
		agentMatches = agentsByRenderer(dashboard.Agents, active)
		if len(agentMatches) > 1 {
			return "", errActiveContextAmbiguous
		}
	}
	if len(workspaceMatches) == 1 {
		if len(agentMatches) == 1 && agentMatches[0].WorkspaceID != workspaceMatches[0].ID {
			return "", errActiveContextAmbiguous
		}
		return workspaceMatches[0].ID, nil
	}
	if len(agentMatches) == 1 {
		if _, ok := dashboard.Workspace(agentMatches[0].WorkspaceID); !ok {
			return "", errActiveContextStale
		}
		return agentMatches[0].WorkspaceID, nil
	}
	return "", errActiveContextUnmanaged
}

func resolveAgent(dashboard model.Dashboard, active ActiveContext, fact paneFact) (model.Agent, error) {
	if fact.AgentSession != nil {
		if fact.AgentSession.Kind == "path" {
			if strings.TrimSpace(fact.AgentSession.Value) == "" {
				return model.Agent{}, errActiveContextMalformed
			}
			matches := agentsBySessionPath(dashboard.Agents, fact.AgentSession.Value)
			if len(matches) == 0 {
				return model.Agent{}, errActiveContextStale
			}
			if len(matches) > 1 {
				return model.Agent{}, errActiveContextAmbiguous
			}
			if matches[0].Kind != "pi" {
				return model.Agent{}, errActiveContextNonAgent
			}
			return matches[0], nil
		}
		if fact.AgentSession.Kind != "id" {
			return model.Agent{}, errActiveContextMalformed
		}
	}
	matches := agentsByRenderer(dashboard.Agents, active)
	if len(matches) == 0 {
		return model.Agent{}, errActiveContextNonAgent
	}
	if len(matches) > 1 {
		return model.Agent{}, errActiveContextAmbiguous
	}
	if matches[0].Kind != "pi" {
		return model.Agent{}, errActiveContextNonAgent
	}
	return matches[0], nil
}

func agentsBySessionPath(values []model.Agent, path string) []model.Agent {
	matches := make([]model.Agent, 0, 1)
	for _, value := range values {
		if value.SessionPath == path {
			matches = append(matches, value)
		}
	}
	return matches
}

func workspacesByRenderer(values []model.Workspace, active ActiveContext) []model.Workspace {
	matches := make([]model.Workspace, 0, 1)
	for _, value := range values {
		if value.Renderer == "herdr" && value.RendererContext == active.RendererContext && value.RendererID == active.WorkspaceID {
			matches = append(matches, value)
		}
	}
	return matches
}

func agentsByRenderer(values []model.Agent, active ActiveContext) []model.Agent {
	matches := make([]model.Agent, 0, 1)
	for _, value := range values {
		if value.Renderer == "herdr" && value.RendererContext == active.RendererContext && value.RendererID == active.PaneID {
			matches = append(matches, value)
		}
	}
	return matches
}
