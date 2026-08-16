package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/gitx"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/piagent"
	"github.com/matipan/galpon/internal/store"
	"github.com/matipan/galpon/internal/terminal"
)

type App struct {
	Config     config.Config
	Store      *store.Store
	Renderer   terminal.Renderer
	PiAssets   piagent.Assets
	Executable string
	Logger     *log.Logger

	agentMutationMu     sync.Mutex
	agentLifecycleMu    sync.Mutex
	agentLifecycleLocks map[string]*agentLifecycleLock
	waitMu              sync.Mutex
	waits               map[string]map[string]string
}

type agentLifecycleLock struct {
	mutex sync.Mutex
	users int
}

type CreateWorkspaceRequest struct {
	Title string `json:"title"`
}

type CreateWorktreeRequest struct {
	WorkspaceID    string `json:"workspaceId,omitempty"`
	WorkspaceTitle string `json:"workspaceTitle,omitempty"`
	RepositoryID   string `json:"repositoryId"`
	Remote         string `json:"remote,omitempty"`
	Ref            string `json:"ref,omitempty"`
	FetchFirst     bool   `json:"fetchFirst,omitempty"`
}

type CreateWorktreeResult struct {
	Workspace model.Workspace `json:"workspace"`
	Worktree  model.Worktree  `json:"worktree"`
}

type CreateAgentRequest struct {
	Title            string                `json:"title"`
	Role             string                `json:"role,omitempty"`
	WorkspaceID      string                `json:"workspaceId"`
	CreatedByAgentID string                `json:"-"`
	ContextAgentID   string                `json:"contextAgentId,omitempty"`
	Placement        AgentPlacementRequest `json:"placement"`
}

type CreateAgentToolResult struct {
	model.Agent
	InitialMessage *model.AgentMessage `json:"initialMessage,omitempty"`
}

type CreateAgentFromSourceRequest struct {
	SourceAgentID string `json:"sourceAgentId"`
	Title         string `json:"title"`
	Role          string `json:"role,omitempty"`
	Prompt        string `json:"prompt"`
}

type CreateAgentFromSourceResult struct {
	Agent          model.Agent        `json:"agent"`
	InitialMessage model.AgentMessage `json:"initialMessage"`
	StartPending   bool               `json:"startPending"`
}

type ConversationEventsRequest struct {
	RuntimeID string                    `json:"runtimeId"`
	Events    []model.ConversationEvent `json:"events"`
}

type AgentPlacementRequest struct {
	Type          string                          `json:"type"`
	CWD           string                          `json:"cwd,omitempty"`
	SourceAgentID string                          `json:"sourceAgentId,omitempty"`
	Share         bool                            `json:"share,omitempty"`
	Worktrees     []AgentPlacementWorktreeRequest `json:"worktrees,omitempty"`
}

type AgentPlacementWorktreeRequest struct {
	RepositoryID     string `json:"repositoryId,omitempty"`
	Remote           string `json:"remote,omitempty"`
	Ref              string `json:"ref,omitempty"`
	FetchFirst       bool   `json:"fetchFirst,omitempty"`
	SourceWorktreeID string `json:"sourceWorktreeId,omitempty"`
	Mode             string `json:"mode,omitempty"`
}

type AddRepositoryRequest struct {
	Path       string                   `json:"path"`
	Title      string                   `json:"title"`
	Remotes    []model.RepositoryRemote `json:"remotes,omitempty"`
	PushRemote string                   `json:"pushRemote,omitempty"`
}

func Open(ctx context.Context, cfg config.Config, logger *log.Logger, renderer terminal.Renderer) (*App, error) {
	st, err := store.Open(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	assets, err := piagent.Materialize(cfg.StateDir)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	out := &App{Config: cfg, Store: st, Renderer: renderer, PiAssets: assets, Executable: executable, Logger: logger}
	return out, nil
}

func (a *App) Close() error { return a.Store.Close() }

func (a *App) AddRepository(ctx context.Context, request AddRepositoryRequest) (model.Repository, bool, error) {
	inspection, err := gitx.InspectRepository(ctx, request.Path)
	if err != nil {
		return model.Repository{}, false, err
	}
	if existing, err := a.Store.RepositoryBySource(ctx, inspection.SourcePath); err != nil {
		return model.Repository{}, false, err
	} else if existing != nil {
		deleted, deletedErr := a.Store.IsDeleted(ctx, "repository", existing.ID)
		if deletedErr != nil {
			return model.Repository{}, false, deletedErr
		}
		if deleted {
			return model.Repository{}, false, fmt.Errorf("repository %s is deleted; run galpon cleanup before you add it again", existing.Title)
		}
		for _, remote := range request.Remotes {
			pushDefault := strings.TrimSpace(request.PushRemote) == strings.TrimSpace(remote.Name)
			if _, err := a.AddRepositoryRemote(ctx, existing.ID, remote, pushDefault); err != nil {
				return model.Repository{}, false, err
			}
		}
		if len(request.Remotes) > 0 {
			updated, err := a.Store.Repository(ctx, existing.ID)
			return updated, true, err
		}
		return *existing, true, nil
	}
	if strings.TrimSpace(request.Title) == "" {
		request.Title = inspection.Title
	}
	remotes := append([]model.RepositoryRemote(nil), inspection.Remotes...)
	names := make(map[string]bool, len(remotes)+len(request.Remotes))
	for _, remote := range remotes {
		names[remote.Name] = true
	}
	for _, remote := range request.Remotes {
		remote, err = gitx.ValidateRemote(remote)
		if err != nil {
			return model.Repository{}, false, err
		}
		if names[remote.Name] {
			return model.Repository{}, false, fmt.Errorf("repository remote %s already exists", remote.Name)
		}
		names[remote.Name] = true
		remotes = append(remotes, remote)
	}
	pushRemote := strings.TrimSpace(request.PushRemote)
	if pushRemote == "" {
		pushRemote = inspection.PushRemote
	}
	if !names[pushRemote] {
		return model.Repository{}, false, fmt.Errorf("default push remote %s is not configured", pushRemote)
	}
	fetchURL := ""
	for _, remote := range remotes {
		if remote.Name == inspection.DefaultRemote {
			fetchURL = remote.FetchURL
			break
		}
	}
	id := uuid.NewString()
	value := model.Repository{ID: id, Title: strings.TrimSpace(request.Title), SourcePath: inspection.SourcePath, FetchURL: fetchURL, MirrorPath: filepath.Join(a.Config.StateDir, "repositories", id+".git"), DefaultRemote: inspection.DefaultRemote, PushRemote: pushRemote, Remotes: remotes, DefaultBranch: inspection.DefaultBranch, CreatedAt: time.Now().UnixMilli()}
	if err := gitx.CreateMirror(ctx, value.Remotes, value.DefaultRemote, value.PushRemote, value.MirrorPath); err != nil {
		return model.Repository{}, false, err
	}
	if value.DefaultBranch == "" {
		value.DefaultBranch = gitx.DefaultBranch(ctx, value.MirrorPath, value.DefaultRemote)
	}
	if err := a.Store.PutRepository(ctx, value); err != nil {
		_ = os.RemoveAll(value.MirrorPath)
		return model.Repository{}, false, err
	}
	return value, false, nil
}

func (a *App) AddRepositoryRemote(ctx context.Context, repository string, remote model.RepositoryRemote, pushDefault bool) (model.Repository, error) {
	dashboard, err := a.Store.Dashboard(ctx)
	if err != nil {
		return model.Repository{}, err
	}
	repo := findRepository(dashboard.Repositories, repository)
	if repo.ID == "" {
		return model.Repository{}, fmt.Errorf("repository not found: %s", repository)
	}
	remote, err = gitx.ValidateRemote(remote)
	if err != nil {
		return model.Repository{}, err
	}
	for _, existing := range repo.Remotes {
		if existing.Name == remote.Name {
			return model.Repository{}, fmt.Errorf("repository remote %s already exists", remote.Name)
		}
	}
	if err := gitx.AddRemote(ctx, repo.MirrorPath, remote, pushDefault); err != nil {
		return model.Repository{}, err
	}
	if err := a.Store.PutRepositoryRemote(ctx, repo.ID, remote, pushDefault); err != nil {
		_ = gitx.RemoveRemote(context.Background(), repo.MirrorPath, remote.Name)
		if pushDefault {
			_ = gitx.SetPushRemote(context.Background(), repo.MirrorPath, repo.PushRemote)
		}
		return model.Repository{}, err
	}
	return a.Store.Repository(ctx, repo.ID)
}

func (a *App) CreateWorkspace(ctx context.Context, request CreateWorkspaceRequest) (model.Workspace, error) {
	title := strings.TrimSpace(request.Title)
	if title == "" {
		return model.Workspace{}, fmt.Errorf("workspace title is required")
	}
	now := time.Now().UnixMilli()
	wsID := uuid.NewString()
	ws := model.Workspace{ID: wsID, Title: title, Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.Store.PutWorkspace(ctx, ws); err != nil {
		return model.Workspace{}, err
	}
	a.notifyCompanion(ctx, "invalidate", "", ws.ID)
	return ws, nil
}

func (a *App) CreateWorktree(ctx context.Context, request CreateWorktreeRequest) (CreateWorktreeResult, error) {
	a.agentMutationMu.Lock()
	defer a.agentMutationMu.Unlock()

	dashboard, err := a.Store.Dashboard(ctx)
	if err != nil {
		return CreateWorktreeResult{}, err
	}
	repository, ok := dashboard.Repository(strings.TrimSpace(request.RepositoryID))
	if !ok {
		return CreateWorktreeResult{}, fmt.Errorf("repository not found")
	}
	workspaceID := strings.TrimSpace(request.WorkspaceID)
	workspaceTitle := strings.TrimSpace(request.WorkspaceTitle)
	if workspaceID != "" && workspaceTitle != "" {
		return CreateWorktreeResult{}, fmt.Errorf("select an existing workspace or provide a new workspace title, not both")
	}
	newWorkspace := workspaceID == ""
	var workspace model.Workspace
	now := time.Now().UnixMilli()
	if newWorkspace {
		if workspaceTitle == "" {
			return CreateWorktreeResult{}, fmt.Errorf("workspace title is required")
		}
		workspace = model.Workspace{ID: uuid.NewString(), Title: workspaceTitle, Status: "active", CreatedAt: now, UpdatedAt: now}
	} else {
		workspace, ok = dashboard.Workspace(workspaceID)
		if !ok {
			return CreateWorktreeResult{}, fmt.Errorf("workspace not found")
		}
	}
	remote := strings.TrimSpace(request.Remote)
	if remote == "" {
		remote = repository.DefaultRemote
	}
	baseRef := strings.TrimSpace(request.Ref)
	if baseRef == "" {
		baseRef = "refs/remotes/" + remote + "/" + repository.DefaultBranch
	} else if !strings.HasPrefix(baseRef, "refs/") {
		baseRef = "refs/remotes/" + remote + "/" + baseRef
	}
	worktreeID := uuid.NewString()
	branch := "galpon/" + gitx.Slug(workspace.Title) + "/worktree-" + shortID(worktreeID) + "/" + gitx.Slug(repository.Title) + "-" + shortID(worktreeID)
	path := filepath.Join(a.Config.StateDir, "worktrees", gitx.Slug(workspace.Title)+"-"+shortID(workspace.ID), "worktree-"+shortID(worktreeID), gitx.Slug(repository.Title)+"-"+shortID(worktreeID))
	if err := gitx.CreateWorktreeFrom(ctx, repository, path, branch, baseRef, remote, request.FetchFirst); err != nil {
		return CreateWorktreeResult{}, err
	}
	worktree := model.Worktree{ID: worktreeID, WorkspaceID: workspace.ID, RepositoryID: repository.ID, Path: path, Branch: branch, BaseRef: baseRef, SourceRemote: remote, Lifecycle: "workspace", CreatedAt: now}
	committed := false
	defer func() {
		if !committed {
			a.removeCreatedWorktrees([]model.Worktree{worktree}, dashboard.Repositories)
		}
	}()
	if newWorkspace {
		err = a.Store.PutWorkspaceWorktree(ctx, workspace, worktree)
	} else {
		err = a.Store.PutWorktree(ctx, worktree)
	}
	if err != nil {
		return CreateWorktreeResult{}, err
	}
	committed = true
	a.notifyCompanion(ctx, "invalidate", "", workspace.ID)
	return CreateWorktreeResult{Workspace: workspace, Worktree: worktree}, nil
}

func (a *App) RequestAgentFinish(ctx context.Context, id, runtimeID string) error {
	id = strings.TrimSpace(id)
	runtimeID = strings.TrimSpace(runtimeID)
	if id == "" || runtimeID == "" {
		return fmt.Errorf("agent ID and runtime ID are required")
	}
	agent, err := a.Store.Agent(ctx, id)
	if err != nil {
		return err
	}
	if agent.RuntimeID != runtimeID {
		return fmt.Errorf("pi runtime is not registered for agent %s", agent.Title)
	}
	return nil
}

func (a *App) DeleteResource(ctx context.Context, kind, id string) (model.DeletionResult, error) {
	a.agentMutationMu.Lock()
	defer a.agentMutationMu.Unlock()

	kind = strings.TrimSpace(kind)
	id = strings.TrimSpace(id)
	agents, err := a.Store.AgentsHiddenByDeletion(ctx, kind, id)
	if err != nil {
		return model.DeletionResult{}, err
	}
	for _, agent := range agents {
		if strings.TrimSpace(agent.RendererID) == "" {
			continue
		}
		if a.Renderer == nil || agent.Renderer != a.Renderer.Name() || agent.RendererContext != a.Renderer.Context() {
			return model.DeletionResult{}, fmt.Errorf("cannot close the terminal view for agent %s in renderer %s context %s", agent.Title, agent.Renderer, agent.RendererContext)
		}
		if err := a.Renderer.CloseAgent(ctx, agent); err != nil {
			return model.DeletionResult{}, fmt.Errorf("close terminal view for agent %s: %w", agent.Title, err)
		}
		if agent.RuntimeID != "" {
			if err := a.Store.StopAgentRuntime(ctx, agent.ID, agent.RuntimeID, "hidden by user"); err != nil {
				return model.DeletionResult{}, fmt.Errorf("stop agent %s: %w", agent.Title, err)
			}
		}
	}
	result, err := a.Store.SoftDelete(ctx, kind, id)
	if err == nil {
		agentID, workspaceID := "", ""
		if kind == "agent" {
			agentID = id
		}
		if kind == "workspace" {
			workspaceID = id
		}
		a.notifyCompanion(ctx, "invalidate", agentID, workspaceID)
	}
	return result, err
}

func (a *App) Cleanup(ctx context.Context) (model.CleanupResult, error) {
	plan, err := a.Store.DeletedCleanupPlan(ctx)
	if err != nil {
		return model.CleanupResult{}, err
	}
	for _, agent := range plan.Agents {
		if agent.RuntimeID != "" {
			return model.CleanupResult{}, fmt.Errorf("cannot clean deleted agent %s while Pi is active; stop it first", agent.Title)
		}
	}
	managedWorktrees := filepath.Join(a.Config.StateDir, "worktrees")
	for _, worktree := range plan.Worktrees {
		if !pathInside(managedWorktrees, worktree.Path) {
			return model.CleanupResult{}, fmt.Errorf("refuse to remove unmanaged worktree path %s", worktree.Path)
		}
		repository := findRepository(plan.AllRepositories, worktree.RepositoryID)
		if repository.ID == "" {
			return model.CleanupResult{}, fmt.Errorf("repository for deleted worktree %s was not found", worktree.ID)
		}
		if err := gitx.CleanupWorktree(ctx, repository, worktree.Path, worktree.Branch); err != nil {
			return model.CleanupResult{}, fmt.Errorf("clean worktree %s: %w", worktree.ID, err)
		}
	}
	for _, agent := range plan.Agents {
		path := filepath.Join(a.Config.StateDir, "agents", agent.ID)
		if !pathInside(filepath.Join(a.Config.StateDir, "agents"), path) {
			return model.CleanupResult{}, fmt.Errorf("refuse to remove unmanaged agent path %s", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return model.CleanupResult{}, fmt.Errorf("clean agent %s: %w", agent.ID, err)
		}
	}
	managedRepositories := filepath.Join(a.Config.StateDir, "repositories")
	for _, repository := range plan.Repositories {
		if !pathInside(managedRepositories, repository.MirrorPath) {
			return model.CleanupResult{}, fmt.Errorf("refuse to remove unmanaged repository path %s", repository.MirrorPath)
		}
		if err := os.RemoveAll(repository.MirrorPath); err != nil {
			return model.CleanupResult{}, fmt.Errorf("clean repository %s: %w", repository.ID, err)
		}
	}
	if err := a.Store.PurgeDeleted(ctx); err != nil {
		return model.CleanupResult{}, err
	}
	return model.CleanupResult{Removed: model.ResourceCounts{
		Repositories: len(plan.Repositories),
		Workspaces:   len(plan.Workspaces),
		Worktrees:    len(plan.Worktrees),
		Agents:       len(plan.Agents),
	}}, nil
}

func (a *App) CleanupAgents(ctx context.Context, callerID string, requestedIDs []string) (model.AgentCleanupResult, error) {
	callerID = strings.TrimSpace(callerID)
	if callerID == "" {
		return model.AgentCleanupResult{}, fmt.Errorf("calling agent is required")
	}
	if len(requestedIDs) == 0 {
		return model.AgentCleanupResult{}, fmt.Errorf("at least one agent ID is required")
	}
	a.agentMutationMu.Lock()
	defer a.agentMutationMu.Unlock()

	if _, err := a.Store.Agent(ctx, callerID); err != nil {
		return model.AgentCleanupResult{}, fmt.Errorf("calling agent not found: %w", err)
	}
	descendants, err := a.Store.AgentDescendants(ctx, callerID)
	if err != nil {
		return model.AgentCleanupResult{}, err
	}
	byID := make(map[string]model.Agent, len(descendants))
	for _, agent := range descendants {
		byID[agent.ID] = agent
	}
	selected := make(map[string]bool, len(requestedIDs))
	for _, requestedID := range requestedIDs {
		id := strings.TrimSpace(requestedID)
		if id == "" {
			return model.AgentCleanupResult{}, fmt.Errorf("agent IDs must not be empty")
		}
		if id == callerID {
			return model.AgentCleanupResult{}, fmt.Errorf("refuse to clean the calling agent")
		}
		if selected[id] {
			return model.AgentCleanupResult{}, fmt.Errorf("agent ID is selected more than once: %s", id)
		}
		if _, ok := byID[id]; !ok {
			return model.AgentCleanupResult{}, fmt.Errorf("agent %s was not created by the calling agent", id)
		}
		selected[id] = true
	}

	var omitted []string
	for _, agent := range descendants {
		if selected[agent.ID] {
			continue
		}
		for creatorID := agent.CreatedByAgentID; creatorID != ""; {
			if selected[creatorID] {
				omitted = append(omitted, agent.ID+" ("+agent.Title+")")
				break
			}
			creator, ok := byID[creatorID]
			if !ok {
				break
			}
			creatorID = creator.CreatedByAgentID
		}
	}
	if len(omitted) != 0 {
		return model.AgentCleanupResult{}, fmt.Errorf("selected agents have descendants that are not selected: %s", strings.Join(omitted, ", "))
	}

	agents := make([]model.Agent, 0, len(selected))
	agentIDs := make([]string, 0, len(selected))
	result := model.AgentCleanupResult{Agents: []model.CleanedAgentRef{}}
	for _, agent := range descendants {
		if !selected[agent.ID] {
			continue
		}
		agents = append(agents, agent)
		agentIDs = append(agentIDs, agent.ID)
		result.Agents = append(result.Agents, model.CleanedAgentRef{ID: agent.ID, Title: agent.Title})
	}
	for _, agent := range agents {
		if agent.RendererID != "" && (a.Renderer == nil || agent.Renderer != a.Renderer.Name() || agent.RendererContext != a.Renderer.Context()) {
			return model.AgentCleanupResult{}, fmt.Errorf("cannot close the terminal view for agent %s in renderer %s context %s", agent.Title, agent.Renderer, agent.RendererContext)
		}
		if agent.RendererID == "" && agent.RuntimeID != "" {
			return model.AgentCleanupResult{}, fmt.Errorf("cannot stop active agent %s without its managed terminal view", agent.Title)
		}
	}
	for _, agent := range agents {
		if agent.RendererID != "" {
			if err := a.Renderer.CloseAgent(ctx, agent); err != nil {
				return model.AgentCleanupResult{}, fmt.Errorf("close terminal view for agent %s: %w", agent.Title, err)
			}
			result.ClosedViews++
		}
		if agent.RuntimeID != "" {
			if err := a.Store.StopAgentRuntime(ctx, agent.ID, agent.RuntimeID, "cleaned by creator"); err != nil {
				return model.AgentCleanupResult{}, fmt.Errorf("stop agent %s: %w", agent.Title, err)
			}
		}
	}

	worktreeIDs, err := a.Store.SoftDeleteAgents(ctx, agentIDs)
	if err != nil {
		return model.AgentCleanupResult{}, err
	}
	worktrees, err := a.Store.WorktreesIncludingDeleted(ctx, worktreeIDs)
	if err != nil {
		return model.AgentCleanupResult{}, err
	}
	plan, err := a.Store.DeletedCleanupPlan(ctx)
	if err != nil {
		return model.AgentCleanupResult{}, err
	}
	managedWorktrees := filepath.Join(a.Config.StateDir, "worktrees")
	for _, worktree := range worktrees {
		if !pathInside(managedWorktrees, worktree.Path) {
			return model.AgentCleanupResult{}, fmt.Errorf("refuse to remove unmanaged worktree path %s", worktree.Path)
		}
		repository := findRepository(plan.AllRepositories, worktree.RepositoryID)
		if repository.ID == "" {
			return model.AgentCleanupResult{}, fmt.Errorf("repository for deleted worktree %s was not found", worktree.ID)
		}
		if err := gitx.CleanupWorktree(ctx, repository, worktree.Path, worktree.Branch); err != nil {
			return model.AgentCleanupResult{}, fmt.Errorf("clean worktree %s: %w", worktree.ID, err)
		}
	}
	managedAgents := filepath.Join(a.Config.StateDir, "agents")
	for _, agent := range agents {
		path := filepath.Join(managedAgents, agent.ID)
		if !pathInside(managedAgents, path) {
			return model.AgentCleanupResult{}, fmt.Errorf("refuse to remove unmanaged agent path %s", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return model.AgentCleanupResult{}, fmt.Errorf("clean agent %s: %w", agent.ID, err)
		}
	}
	if err := a.Store.PurgeAgentCleanup(ctx, agentIDs, worktreeIDs); err != nil {
		return model.AgentCleanupResult{}, err
	}
	a.dropAgentWaits(agentIDs)
	result.Removed = model.ResourceCounts{Agents: len(agents), Worktrees: len(worktrees)}
	return result, nil
}

func (a *App) dropAgentWaits(agentIDs []string) {
	removed := make(map[string]bool, len(agentIDs))
	for _, id := range agentIDs {
		removed[id] = true
	}
	a.waitMu.Lock()
	defer a.waitMu.Unlock()
	for callerID, messages := range a.waits {
		if removed[callerID] {
			delete(a.waits, callerID)
			continue
		}
		for messageID, targetID := range messages {
			if removed[targetID] {
				delete(messages, messageID)
			}
		}
		if len(messages) == 0 {
			delete(a.waits, callerID)
		}
	}
}

func (a *App) notifyCompanion(ctx context.Context, eventType, agentID, workspaceID string) {
	if _, err := a.Store.AppendCompanionEvent(ctx, eventType, agentID, workspaceID); err != nil && a.Logger != nil {
		a.Logger.Printf("persist companion event: %v", err)
	}
}

func (a *App) notifyCompanionForAgent(ctx context.Context, agentID string) {
	agent, err := a.Store.Agent(ctx, agentID)
	if err != nil {
		return
	}
	a.notifyCompanion(ctx, "invalidate", agent.ID, agent.WorkspaceID)
}

func pathInside(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func (a *App) CreateAgent(ctx context.Context, request CreateAgentRequest) (model.Agent, error) {
	a.agentMutationMu.Lock()
	defer a.agentMutationMu.Unlock()
	return a.createAgent(ctx, request)
}

func (a *App) createAgent(ctx context.Context, request CreateAgentRequest) (model.Agent, error) {
	title := strings.TrimSpace(request.Title)
	if title == "" {
		return model.Agent{}, fmt.Errorf("agent title is required")
	}
	dashboard, err := a.Store.Dashboard(ctx)
	if err != nil {
		return model.Agent{}, err
	}
	workspace, ok := dashboard.Workspace(request.WorkspaceID)
	if !ok {
		return model.Agent{}, fmt.Errorf("workspace not found")
	}
	contextAgentID := strings.TrimSpace(request.ContextAgentID)
	if contextAgentID != "" {
		source, ok := dashboard.Agent(contextAgentID)
		if !ok {
			return model.Agent{}, fmt.Errorf("context agent not found")
		}
		if source.SessionPath == "" {
			return model.Agent{}, fmt.Errorf("context agent has no Pi session to fork")
		}
		if source.Status == "running" || source.Status == "starting" {
			return model.Agent{}, fmt.Errorf("context agent must be idle or stopped before it is forked")
		}
	}
	now := time.Now().UnixMilli()
	id := uuid.NewString()
	placement, created, err := a.createAgentPlacement(ctx, dashboard, workspace, id, title, request.Placement, now)
	if err != nil {
		return model.Agent{}, err
	}
	committed := false
	defer func() {
		if !committed {
			a.removeCreatedWorktrees(created, dashboard.Repositories)
		}
	}()
	creatorID := strings.TrimSpace(request.CreatedByAgentID)
	if creatorID != "" {
		if creator, ok := dashboard.Agent(creatorID); !ok {
			return model.Agent{}, fmt.Errorf("creator agent not found")
		} else if creator.ID == id {
			return model.Agent{}, fmt.Errorf("an agent cannot create itself")
		}
	}
	value := model.Agent{ID: id, WorkspaceID: workspace.ID, Title: title, Role: strings.TrimSpace(request.Role), CreatedByAgentID: creatorID, ContextAgentID: contextAgentID, Placement: placement, Kind: "pi", Status: "stopped", SessionID: id, CreatedAt: now, UpdatedAt: now}
	if err := a.Store.PutAgent(ctx, value, created); err != nil {
		return model.Agent{}, err
	}
	committed = true
	a.notifyCompanion(ctx, "invalidate", value.ID, value.WorkspaceID)
	return value, nil
}

func (a *App) createAgentPlacement(ctx context.Context, dashboard model.Dashboard, workspace model.Workspace, agentID, agentTitle string, request AgentPlacementRequest, now int64) (model.AgentPlacement, []model.Worktree, error) {
	kind := strings.TrimSpace(request.Type)
	if kind == "" {
		kind = "worktrees"
	}
	if kind == "none" {
		cwd := filepath.Clean(strings.TrimSpace(request.CWD))
		if !filepath.IsAbs(cwd) {
			return model.AgentPlacement{}, nil, fmt.Errorf("worktreeless placement needs an absolute directory")
		}
		info, err := os.Stat(cwd)
		if err != nil || !info.IsDir() {
			return model.AgentPlacement{}, nil, fmt.Errorf("worktreeless placement directory is not available: %s", cwd)
		}
		return model.AgentPlacement{Type: "none", CWD: cwd}, nil, nil
	}
	worktrees := append([]AgentPlacementWorktreeRequest(nil), request.Worktrees...)
	if kind == "agent" {
		source, ok := dashboard.Agent(strings.TrimSpace(request.SourceAgentID))
		if !ok {
			return model.AgentPlacement{}, nil, fmt.Errorf("placement source agent not found")
		}
		if source.Placement.Type == "none" {
			return model.AgentPlacement{Type: "none", CWD: source.Placement.CWD}, nil, nil
		}
		worktrees = make([]AgentPlacementWorktreeRequest, 0, len(source.Placement.Worktrees))
		for _, assignment := range source.Placement.Worktrees {
			mode := "fork"
			if request.Share {
				mode = "share"
			}
			worktrees = append(worktrees, AgentPlacementWorktreeRequest{SourceWorktreeID: assignment.WorktreeID, Mode: mode})
		}
	} else if kind != "worktrees" {
		return model.AgentPlacement{}, nil, fmt.Errorf("invalid placement type %q", kind)
	}
	if len(worktrees) == 0 {
		return model.AgentPlacement{}, nil, fmt.Errorf("worktree placement needs a primary worktree")
	}

	created := make([]model.Worktree, 0, len(worktrees))
	complete := false
	defer func() {
		if !complete {
			a.removeCreatedWorktrees(created, dashboard.Repositories)
		}
	}()
	placement := model.AgentPlacement{Type: "worktrees", Worktrees: make([]model.AgentWorktree, 0, len(worktrees))}
	seen := map[string]bool{}
	for position, input := range worktrees {
		mode := strings.TrimSpace(input.Mode)
		if mode == "" {
			mode = "fork"
		}
		if mode != "fork" && mode != "share" {
			return model.AgentPlacement{}, nil, fmt.Errorf("invalid assignment mode %q", mode)
		}
		var source model.Worktree
		if input.SourceWorktreeID != "" {
			var ok bool
			source, ok = dashboard.Worktree(input.SourceWorktreeID)
			if !ok {
				return model.AgentPlacement{}, nil, fmt.Errorf("placement source worktree not found")
			}
		}
		if mode == "share" {
			if source.ID == "" {
				return model.AgentPlacement{}, nil, fmt.Errorf("exact sharing needs an existing worktree")
			}
			if seen[source.ID] {
				return model.AgentPlacement{}, nil, fmt.Errorf("worktree %s is selected more than once", source.ID)
			}
			seen[source.ID] = true
			placement.Worktrees = append(placement.Worktrees, model.AgentWorktree{WorktreeID: source.ID, Position: position, Mode: "shared"})
			continue
		}

		repositoryID := strings.TrimSpace(input.RepositoryID)
		baseRef := strings.TrimSpace(input.Ref)
		remote := strings.TrimSpace(input.Remote)
		fetchFirst := input.FetchFirst
		if source.ID != "" {
			repositoryID = source.RepositoryID
			baseRef = source.Branch
			remote = source.SourceRemote
			fetchFirst = false
		}
		repository, ok := dashboard.Repository(repositoryID)
		if !ok {
			return model.AgentPlacement{}, nil, fmt.Errorf("repository not found: %s", repositoryID)
		}
		if remote == "" {
			remote = repository.DefaultRemote
		}
		if baseRef == "" {
			baseRef = "refs/remotes/" + remote + "/" + repository.DefaultBranch
		} else if source.ID == "" && !strings.HasPrefix(baseRef, "refs/") {
			baseRef = "refs/remotes/" + remote + "/" + baseRef
		}
		worktreeID := uuid.NewString()
		branch := "galpon/" + gitx.Slug(workspace.Title) + "/" + gitx.Slug(agentTitle) + "-" + shortID(agentID) + "/" + gitx.Slug(repository.Title) + "-" + shortID(worktreeID)
		path := filepath.Join(a.Config.StateDir, "worktrees", gitx.Slug(workspace.Title)+"-"+shortID(workspace.ID), gitx.Slug(agentTitle)+"-"+shortID(agentID), gitx.Slug(repository.Title)+"-"+shortID(worktreeID))
		if err := gitx.CreateWorktreeFrom(ctx, repository, path, branch, baseRef, remote, fetchFirst); err != nil {
			return model.AgentPlacement{}, nil, err
		}
		worktree := model.Worktree{ID: worktreeID, WorkspaceID: workspace.ID, RepositoryID: repository.ID, Path: path, Branch: branch, BaseRef: baseRef, SourceRemote: remote, Lifecycle: "agent", CreatedAt: now}
		created = append(created, worktree)
		placement.Worktrees = append(placement.Worktrees, model.AgentWorktree{WorktreeID: worktree.ID, Position: position, Mode: "private"})
	}
	placement.PrimaryWorktreeID = placement.Worktrees[0].WorktreeID
	complete = true
	return placement, created, nil
}

func (a *App) removeCreatedWorktrees(worktrees []model.Worktree, repositories []model.Repository) {
	for index := len(worktrees) - 1; index >= 0; index-- {
		worktree := worktrees[index]
		repository := findRepository(repositories, worktree.RepositoryID)
		if repository.ID == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := gitx.RemoveWorktree(ctx, repository, worktree.Path, worktree.Branch)
		cancel()
		if err != nil && a.Logger != nil {
			a.Logger.Printf("roll back worktree %s: %v", worktree.ID, err)
		}
	}
}

func shortID(id string) string {
	value := strings.ReplaceAll(id, "-", "")
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func (a *App) CreateAgentFromSource(ctx context.Context, idempotencyKey string, request CreateAgentFromSourceRequest) (CreateAgentFromSourceResult, error) {
	source, err := a.Store.Agent(ctx, strings.TrimSpace(request.SourceAgentID))
	if err != nil {
		return CreateAgentFromSourceResult{}, err
	}
	if source.Placement.Type == "none" {
		return CreateAgentFromSourceResult{}, fmt.Errorf("source agent does not have a managed worktree placement")
	}
	if strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Prompt) == "" {
		return CreateAgentFromSourceResult{}, fmt.Errorf("agent title and prompt are required")
	}
	var cached CreateAgentFromSourceResult
	fresh, err := a.admitCompanionMutation(ctx, idempotencyKey, "create_agent", request, &cached)
	if err != nil || !fresh {
		return cached, err
	}
	agent, err := a.CreateAgent(ctx, CreateAgentRequest{
		Title:       request.Title,
		Role:        request.Role,
		WorkspaceID: source.WorkspaceID,
		Placement:   AgentPlacementRequest{Type: "agent", SourceAgentID: source.ID, Share: false},
	})
	if err != nil {
		return CreateAgentFromSourceResult{}, err
	}
	message, err := a.enqueueAgentMessage(ctx, "", agent.ID, request.Prompt)
	if err != nil {
		return CreateAgentFromSourceResult{}, err
	}
	startedAgent, startErr := a.OpenAgent(ctx, agent.ID, false)
	if startErr == nil {
		agent = startedAgent
	}
	result := CreateAgentFromSourceResult{Agent: agent, InitialMessage: message, StartPending: startErr != nil}
	return result, a.completeCompanionMutation(ctx, idempotencyKey, result)
}

func (a *App) QueueCompanionMessage(ctx context.Context, idempotencyKey, agentID, prompt string) (model.AgentMessage, error) {
	if _, err := a.Store.Agent(ctx, strings.TrimSpace(agentID)); err != nil {
		return model.AgentMessage{}, err
	}
	if strings.TrimSpace(prompt) == "" {
		return model.AgentMessage{}, fmt.Errorf("prompt is required")
	}
	request := struct {
		AgentID string `json:"agentId"`
		Prompt  string `json:"prompt"`
	}{AgentID: agentID, Prompt: prompt}
	var cached model.AgentMessage
	fresh, err := a.admitCompanionMutation(ctx, idempotencyKey, "send_message", request, &cached)
	if err != nil || !fresh {
		return cached, err
	}
	message, err := a.QueueAgentMessage(ctx, "", agentID, prompt)
	if err != nil {
		return model.AgentMessage{}, err
	}
	return message, a.completeCompanionMutation(ctx, idempotencyKey, message)
}

func (a *App) admitCompanionMutation(ctx context.Context, key, operation string, request, cached any) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 200 {
		return false, fmt.Errorf("a valid Idempotency-Key is required")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return false, err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	mutation, fresh, err := a.Store.ReserveCompanionMutation(ctx, key, operation, hash)
	if err != nil {
		return false, err
	}
	if fresh {
		return true, nil
	}
	if mutation.Operation != operation || mutation.RequestHash != hash {
		return false, fmt.Errorf("Idempotency-Key was already used for a different request")
	}
	if mutation.StatusCode == 0 {
		return false, fmt.Errorf("the prior request with this Idempotency-Key did not finish; its outcome needs manual review")
	}
	if err := json.Unmarshal(mutation.ResponseJSON, cached); err != nil {
		return false, err
	}
	return false, nil
}

func (a *App) completeCompanionMutation(ctx context.Context, key string, response any) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return a.Store.CompleteCompanionMutation(ctx, strings.TrimSpace(key), 200, body)
}

func (a *App) OpenAgent(ctx context.Context, id string, focus bool) (model.Agent, error) {
	unlock := a.lockAgentLifecycle(id)
	defer unlock()

	// Read the agent only after this agent's lifecycle lock is held. The prior
	// open can save its view and starting state before another call decides
	// whether the same durable Pi session needs a new process.
	agent, err := a.Store.Agent(ctx, id)
	if err != nil {
		return model.Agent{}, err
	}
	if a.Renderer == nil {
		return model.Agent{}, fmt.Errorf("terminal renderer is not configured")
	}
	dashboard, err := a.Store.Dashboard(ctx)
	if err != nil {
		return model.Agent{}, err
	}
	ws, ok := dashboard.Workspace(agent.WorkspaceID)
	if !ok {
		return model.Agent{}, fmt.Errorf("workspace not found")
	}
	worktree, ok := dashboard.PrimaryWorktree(agent)
	if !ok {
		if agent.Placement.Type != "none" || agent.Placement.CWD == "" {
			return model.Agent{}, fmt.Errorf("agent primary worktree not found")
		}
		worktree = model.Worktree{Path: agent.Placement.CWD}
	}
	if agent.Status == "stopped" || agent.Status == "failed" {
		_ = a.Store.SetAgentStatus(ctx, agent.ID, "starting", "")
	}
	command := []string{
		"env",
		"GALPON_STATE_DIR=" + a.Config.StateDir,
		"GALPON_PI_BIN=" + a.Config.PiBin,
		"GALPON_PI_PROVIDER=" + a.Config.PiProvider,
		"GALPON_PI_MODEL=" + a.Config.PiModel,
		"GALPON_HERDR_BIN=" + a.Config.HerdrBin,
	}
	for _, key := range []string{"PI_CODING_AGENT_DIR", "PI_OFFLINE", "NO_COLOR"} {
		if value, ok := os.LookupEnv(key); ok {
			command = append(command, key+"="+value)
		}
	}
	command = append(command, a.Executable, "pi", "run", agent.ID)
	workspaceID, paneID, started, err := a.Renderer.OpenAgent(ctx, ws, worktree, agent, command, focus)
	if err != nil {
		_ = a.Store.SetAgentStatus(ctx, agent.ID, "failed", err.Error())
		return model.Agent{}, err
	}
	if err := a.Store.SetRenderer(ctx, ws.ID, a.Renderer.Name(), a.Renderer.Context(), workspaceID); err != nil {
		return model.Agent{}, err
	}
	if err := a.Store.SetAgentRenderer(ctx, agent.ID, a.Renderer.Name(), a.Renderer.Context(), paneID); err != nil {
		return model.Agent{}, err
	}
	agent, err = a.Store.Agent(ctx, agent.ID)
	if err != nil {
		return model.Agent{}, err
	}
	if started {
		_ = a.Renderer.ReportAgent(ctx, agent, "starting", "Starting Pi")
	}
	return agent, nil
}

func (a *App) lockAgentLifecycle(id string) func() {
	a.agentLifecycleMu.Lock()
	if a.agentLifecycleLocks == nil {
		a.agentLifecycleLocks = make(map[string]*agentLifecycleLock)
	}
	lock := a.agentLifecycleLocks[id]
	if lock == nil {
		lock = &agentLifecycleLock{}
		a.agentLifecycleLocks[id] = lock
	}
	lock.users++
	a.agentLifecycleMu.Unlock()

	lock.mutex.Lock()
	return func() {
		lock.mutex.Unlock()
		a.agentLifecycleMu.Lock()
		defer a.agentLifecycleMu.Unlock()
		lock.users--
		if lock.users == 0 && a.agentLifecycleLocks[id] == lock {
			delete(a.agentLifecycleLocks, id)
		}
	}
}

func (a *App) QueueAgentMessage(ctx context.Context, senderID, targetID, prompt string) (model.AgentMessage, error) {
	value, err := a.enqueueAgentMessage(ctx, senderID, targetID, prompt)
	if err != nil {
		return model.AgentMessage{}, err
	}
	if _, err := a.OpenAgent(ctx, targetID, false); err != nil && a.Logger != nil {
		a.Logger.Printf("start Pi agent %s for message %s: %v", targetID, value.ID, err)
	}
	return value, nil
}

func (a *App) enqueueAgentMessage(ctx context.Context, senderID, targetID, prompt string) (model.AgentMessage, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return model.AgentMessage{}, fmt.Errorf("message text is required")
	}
	if senderID != "" && senderID == targetID {
		return model.AgentMessage{}, fmt.Errorf("an agent cannot send work to itself")
	}
	if _, err := a.Store.Agent(ctx, targetID); err != nil {
		return model.AgentMessage{}, err
	}
	now := time.Now().UnixMilli()
	value := model.AgentMessage{ID: uuid.NewString(), SenderAgentID: senderID, TargetAgentID: targetID, Prompt: prompt, Status: "queued", CreatedAt: now, UpdatedAt: now}
	if err := a.Store.PutAgentMessage(ctx, value); err != nil {
		return model.AgentMessage{}, err
	}
	agent, _ := a.Store.Agent(ctx, targetID)
	a.notifyCompanion(ctx, "invalidate", targetID, agent.WorkspaceID)
	return value, nil
}

func (a *App) AwaitAgentMessage(ctx context.Context, id string) (model.AgentMessage, error) {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		value, err := a.Store.AgentMessage(ctx, id)
		if err != nil {
			return model.AgentMessage{}, err
		}
		if value.Status == "completed" || value.Status == "failed" {
			return value, nil
		}
		select {
		case <-ctx.Done():
			return model.AgentMessage{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

var conversationEventKinds = map[string]bool{
	"user_message": true, "assistant_message_start": true, "assistant_text_delta": true,
	"assistant_message_end": true, "tool_execution_start": true, "tool_execution_update": true,
	"tool_execution_end": true, "agent_start": true, "agent_end": true, "agent_settled": true,
	"compaction_start": true, "compaction_end": true, "retry_start": true, "retry_end": true,
}

func (a *App) IngestConversationEvents(ctx context.Context, agentID string, request ConversationEventsRequest) (int, error) {
	if strings.TrimSpace(request.RuntimeID) == "" {
		return 0, fmt.Errorf("runtime ID is required")
	}
	if len(request.Events) == 0 || len(request.Events) > 200 {
		return 0, fmt.Errorf("events must contain between 1 and 200 items")
	}
	for index := range request.Events {
		event := &request.Events[index]
		event.EventID = strings.TrimSpace(event.EventID)
		event.Kind = strings.TrimSpace(event.Kind)
		event.Role = strings.TrimSpace(event.Role)
		if event.EventID == "" || len(event.EventID) > 200 {
			return 0, fmt.Errorf("event %d has an invalid eventId", index)
		}
		if len(event.PiEntryID) > 200 || len(event.ToolName) > 200 || len(event.ToolCallID) > 200 || len(event.Content) > 1<<20 {
			return 0, fmt.Errorf("event %d exceeds conversation field limits", index)
		}
		if !conversationEventKinds[event.Kind] {
			return 0, fmt.Errorf("event %d has invalid kind %q", index, event.Kind)
		}
		if event.RuntimeSeq < 0 {
			return 0, fmt.Errorf("event %d has an invalid runtimeSeq", index)
		}
		if event.Role != "" && event.Role != "user" && event.Role != "assistant" && event.Role != "tool" && event.Role != "system" {
			return 0, fmt.Errorf("event %d has invalid role %q", index, event.Role)
		}
		if event.CreatedAt <= 0 {
			return 0, fmt.Errorf("event %d has an invalid createdAt", index)
		}
		event.Sequence = 0
		event.AgentID = ""
	}
	return a.Store.PutConversationEvents(ctx, agentID, strings.TrimSpace(request.RuntimeID), request.Events)
}

func (a *App) RegisterRuntime(ctx context.Context, agentID, runtimeID, sessionID, sessionPath string) error {
	if strings.TrimSpace(runtimeID) == "" || strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("runtime ID and session ID are required")
	}
	agent, err := a.Store.Agent(ctx, agentID)
	if err != nil {
		return err
	}
	if agent.SessionID != "" && agent.SessionID != sessionID {
		return fmt.Errorf("pi session %s does not belong to agent %s", sessionID, agentID)
	}
	if err := a.Store.RegisterAgentRuntime(ctx, agentID, runtimeID, sessionID, sessionPath); err != nil {
		return err
	}
	a.notifyCompanionForAgent(ctx, agentID)
	return a.reportAgent(ctx, agentID, "idle", "")
}

func (a *App) SetRuntimeStatus(ctx context.Context, agentID, runtimeID, status, lastError string) error {
	switch status {
	case "idle", "running", "failed":
	default:
		return fmt.Errorf("invalid Pi agent status %q", status)
	}
	if err := a.Store.SetAgentRuntimeStatus(ctx, agentID, runtimeID, status, lastError); err != nil {
		return err
	}
	a.notifyCompanionForAgent(ctx, agentID)
	return a.reportAgent(ctx, agentID, status, lastError)
}

func (a *App) StopRuntime(ctx context.Context, agentID, runtimeID, lastError string) error {
	if err := a.Store.StopAgentRuntime(ctx, agentID, runtimeID, lastError); err != nil {
		return err
	}
	a.notifyCompanionForAgent(ctx, agentID)
	return a.reportAgent(ctx, agentID, "stopped", lastError)
}

func (a *App) ClaimMessage(ctx context.Context, agentID, runtimeID string) (*model.AgentMessage, error) {
	agent, err := a.Store.Agent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if agent.RuntimeID == "" || agent.RuntimeID != runtimeID {
		return nil, fmt.Errorf("pi runtime is not registered for this agent")
	}
	message, err := a.Store.ClaimAgentMessage(ctx, agentID, runtimeID)
	if err == nil && message != nil {
		a.notifyCompanionForAgent(ctx, agentID)
	}
	return message, err
}

func (a *App) CompleteMessage(ctx context.Context, agentID, messageID, runtimeID, response, failure string) error {
	err := a.Store.CompleteAgentMessage(ctx, messageID, agentID, runtimeID, response, failure)
	if err == nil {
		a.notifyCompanionForAgent(ctx, agentID)
	}
	return err
}

func (a *App) reportAgent(ctx context.Context, agentID, status, message string) error {
	if a.Renderer == nil {
		return nil
	}
	agent, err := a.Store.Agent(ctx, agentID)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := a.Renderer.ReportAgent(ctx, agent, status, message); err != nil && a.Logger != nil {
		a.Logger.Printf("report agent %s to %s: %v", agentID, a.Renderer.Name(), err)
	}
	return nil
}

func (a *App) handleAgentTool(ctx context.Context, callerID, tool string, args map[string]any) (any, error) {
	dashboard, err := a.Store.Dashboard(ctx)
	if err != nil {
		return nil, err
	}
	switch tool {
	case "list_repositories":
		return dashboard.Repositories, nil
	case "list_workspaces":
		return dashboard.Workspaces, nil
	case "create_workspace":
		return a.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: stringArg(args, "title")})
	case "list_agents":
		return agentToolViews(dashboard), nil
	case "send_agent":
		target := findAgent(dashboard.Agents, stringArg(args, "agent"))
		if target.ID == "" {
			return nil, fmt.Errorf("agent not found: %s", stringArg(args, "agent"))
		}
		return a.QueueAgentMessage(ctx, callerID, target.ID, stringArg(args, "prompt"))
	case "read_message":
		return a.Store.AgentMessage(ctx, stringArg(args, "message_id"))
	case "cleanup_agents":
		agentIDs, err := stringListArg(args, "agent_ids")
		if err != nil {
			return nil, err
		}
		return a.CleanupAgents(ctx, callerID, agentIDs)
	case "await_agent":
		messageID := stringArg(args, "message_id")
		message, err := a.Store.AgentMessage(ctx, messageID)
		if err != nil || message.Status == "completed" || message.Status == "failed" {
			return message, err
		}
		finishWait, err := a.beginAgentWait(callerID, message)
		if err != nil {
			return nil, err
		}
		defer finishWait()
		waitCtx, cancel := context.WithTimeout(ctx, agentWaitTimeout(args))
		defer cancel()
		message, err = a.AwaitAgentMessage(waitCtx, messageID)
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return a.Store.AgentMessage(ctx, messageID)
		}
		return message, err
	case "create_agent":
		ws := findWorkspace(dashboard.Workspaces, stringArg(args, "workspace"))
		if ws.ID == "" {
			return nil, fmt.Errorf("workspace not found: %s", stringArg(args, "workspace"))
		}
		contextAgentID := ""
		if query := stringArg(args, "context_agent"); query != "" {
			contextAgent := findAgent(dashboard.Agents, query)
			if contextAgent.ID == "" {
				return nil, fmt.Errorf("context agent not found: %s", query)
			}
			contextAgentID = contextAgent.ID
		}
		placement, err := placementFromToolArgs(dashboard, args)
		if err != nil {
			return nil, err
		}
		agent, err := a.CreateAgent(ctx, CreateAgentRequest{Title: stringArg(args, "title"), Role: stringArg(args, "role"), WorkspaceID: ws.ID, CreatedByAgentID: callerID, ContextAgentID: contextAgentID, Placement: placement})
		if err != nil {
			return nil, err
		}
		result := CreateAgentToolResult{Agent: agent}
		if prompt := stringArg(args, "prompt"); prompt != "" {
			message, err := a.enqueueAgentMessage(ctx, callerID, agent.ID, prompt)
			if err != nil {
				return nil, err
			}
			result.InitialMessage = &message
		}
		result.Agent, err = a.OpenAgent(ctx, agent.ID, false)
		return result, err
	default:
		return nil, fmt.Errorf("unknown Galpon tool %s", tool)
	}
}

func placementFromToolArgs(dashboard model.Dashboard, args map[string]any) (AgentPlacementRequest, error) {
	if cwd := stringArg(args, "cwd"); cwd != "" {
		return AgentPlacementRequest{Type: "none", CWD: cwd}, nil
	}
	if query := stringArg(args, "placement_agent"); query != "" {
		source := findAgent(dashboard.Agents, query)
		if source.ID == "" {
			return AgentPlacementRequest{}, fmt.Errorf("placement agent not found: %s", query)
		}
		share, _ := args["share"].(bool)
		return AgentPlacementRequest{Type: "agent", SourceAgentID: source.ID, Share: share}, nil
	}
	primary := findRepository(dashboard.Repositories, stringArg(args, "repository"))
	if primary.ID == "" {
		return AgentPlacementRequest{}, fmt.Errorf("repository is required for a new worktree placement")
	}
	worktrees := []AgentPlacementWorktreeRequest{{RepositoryID: primary.ID, Remote: stringArg(args, "remote"), Ref: stringArg(args, "ref"), FetchFirst: true}}
	if values, ok := args["secondary"].([]any); ok {
		for _, raw := range values {
			entry, _ := raw.(map[string]any)
			repo := findRepository(dashboard.Repositories, stringArg(entry, "repository"))
			if repo.ID == "" {
				return AgentPlacementRequest{}, fmt.Errorf("secondary repository not found: %s", stringArg(entry, "repository"))
			}
			worktrees = append(worktrees, AgentPlacementWorktreeRequest{RepositoryID: repo.ID, Remote: stringArg(entry, "remote"), Ref: stringArg(entry, "ref"), FetchFirst: true})
		}
	}
	return AgentPlacementRequest{Type: "worktrees", Worktrees: worktrees}, nil
}

func agentToolViews(dashboard model.Dashboard) []map[string]any {
	out := make([]map[string]any, 0, len(dashboard.Agents))
	for _, agent := range dashboard.Agents {
		out = append(out, map[string]any{"agent": agent, "worktrees": dashboard.AgentWorktrees(agent)})
	}
	return out
}

func findAgent(items []model.Agent, query string) model.Agent {
	for _, v := range items {
		if v.ID == query || strings.EqualFold(v.Title, query) {
			return v
		}
	}
	return model.Agent{}
}
func findWorkspace(items []model.Workspace, query string) model.Workspace {
	for _, v := range items {
		if v.ID == query || strings.EqualFold(v.Title, query) {
			return v
		}
	}
	return model.Workspace{}
}
func findRepository(items []model.Repository, query string) model.Repository {
	for _, v := range items {
		if v.ID == query || strings.EqualFold(v.Title, query) {
			return v
		}
	}
	return model.Repository{}
}
func (a *App) beginAgentWait(callerID string, message model.AgentMessage) (func(), error) {
	callerID = strings.TrimSpace(callerID)
	if callerID == "" {
		return func() {}, fmt.Errorf("the waiting Galpon agent is required")
	}
	a.waitMu.Lock()
	defer a.waitMu.Unlock()
	if path := a.agentWaitPathLocked(message.TargetAgentID, callerID, map[string]bool{}); len(path) != 0 {
		cycle := append([]string{callerID}, path...)
		return func() {}, fmt.Errorf("cross-agent wait cycle detected: %s; finish the current message instead of waiting", strings.Join(cycle, " -> "))
	}
	if a.waits == nil {
		a.waits = map[string]map[string]string{}
	}
	if a.waits[callerID] == nil {
		a.waits[callerID] = map[string]string{}
	}
	a.waits[callerID][message.ID] = message.TargetAgentID
	return func() {
		a.waitMu.Lock()
		defer a.waitMu.Unlock()
		delete(a.waits[callerID], message.ID)
		if len(a.waits[callerID]) == 0 {
			delete(a.waits, callerID)
		}
	}, nil
}

func (a *App) agentWaitPathLocked(from, target string, visited map[string]bool) []string {
	if from == target {
		return []string{from}
	}
	if visited[from] {
		return nil
	}
	visited[from] = true
	for _, next := range a.waits[from] {
		if path := a.agentWaitPathLocked(next, target, visited); len(path) != 0 {
			return append([]string{from}, path...)
		}
	}
	return nil
}

func agentWaitTimeout(args map[string]any) time.Duration {
	seconds := 60
	switch value := args["timeout_seconds"].(type) {
	case float64:
		seconds = int(value)
	case int:
		seconds = value
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			seconds = int(parsed)
		}
	}
	if seconds < 1 {
		seconds = 1
	}
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func stringListArg(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok {
		return nil, fmt.Errorf("%s is required", key)
	}
	var values []string
	switch list := raw.(type) {
	case []any:
		values = make([]string, 0, len(list))
		for _, item := range list {
			value, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s must contain only strings", key)
			}
			values = append(values, value)
		}
	case []string:
		values = append([]string(nil), list...)
	default:
		return nil, fmt.Errorf("%s must be a list of strings", key)
	}
	return values, nil
}

func IsNotFound(err error) bool    { return err == sql.ErrNoRows }
func JSON(value any) string        { data, _ := json.Marshal(value); return string(data) }
func EnsurePath(path string) error { return os.MkdirAll(path, 0o700) }
