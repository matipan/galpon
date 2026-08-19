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
	"unicode/utf8"

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

	// backgroundStart is a test hook. Production uses the managed Pi RPC
	// supervisor when this function is nil.
	backgroundStart func(context.Context, model.Agent) error

	backgroundContext   context.Context
	backgroundCancel    context.CancelFunc
	backgroundMu        sync.Mutex
	backgroundProcesses map[string]*backgroundProcess

	agentMutationMu     sync.Mutex
	legacyRuntimeMu     sync.Mutex
	legacyRuntimeTools  map[string]string
	startRetryMu        sync.Mutex
	startRetries        map[string]bool
	agentLifecycleMu    sync.Mutex
	agentLifecycleLocks map[string]*agentLifecycleLock
	waitMu              sync.Mutex
	waits               map[string]map[string]string
	messageWaiterMu     sync.Mutex
	messageWaiters      map[string]map[chan struct{}]struct{}
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
	Presentation     string                `json:"-"`
	ContextAgentID   string                `json:"contextAgentId,omitempty"`
	Placement        AgentPlacementRequest `json:"placement"`
}

type CreateAgentToolResult struct {
	model.Agent
	InitialMessage *model.AgentMessage `json:"initialMessage,omitempty"`
}

const (
	companionTitleLimit       = 120
	companionRoleLimit        = 120
	companionPromptLimit      = 20_000
	crossAgentPromptByteLimit = 512 << 10
	crossAgentResultByteLimit = 512 << 10
	crossAgentErrorByteLimit  = 64 << 10
	crossAgentMaxDepth        = 16
)

type CreateAgentFromSourceRequest struct {
	SourceAgentID string   `json:"sourceAgentId,omitempty"`
	WorkspaceID   string   `json:"workspaceId,omitempty"`
	RepositoryIDs []string `json:"repositoryIds,omitempty"`
	Title         string   `json:"title"`
	Role          string   `json:"role,omitempty"`
	Prompt        string   `json:"prompt"`
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
	backgroundContext, backgroundCancel := context.WithCancel(context.Background())
	out := &App{
		Config: cfg, Store: st, Renderer: renderer, PiAssets: assets, Executable: executable, Logger: logger,
		backgroundContext: backgroundContext, backgroundCancel: backgroundCancel,
		backgroundProcesses: make(map[string]*backgroundProcess),
	}
	if err := st.ReconcileBackgroundRuntimes(ctx); err != nil {
		backgroundCancel()
		_ = st.Close()
		return nil, err
	}
	dashboard, err := st.Dashboard(ctx)
	if err != nil {
		backgroundCancel()
		_ = st.Close()
		return nil, err
	}
	out.legacyRuntimeTools = make(map[string]string)
	for _, agent := range dashboard.Agents {
		if agent.RuntimeID != "" {
			out.legacyRuntimeTools[agent.ID] = agent.RuntimeID
		}
	}
	go out.dispatchQueuedAgents()
	return out, nil
}

func (a *App) Close() error {
	a.stopAllBackgroundProcesses()
	a.backgroundCancel()
	return a.Store.Close()
}

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
		if agent.IsBackground() {
			stopCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
			err := a.stopBackgroundProcess(stopCtx, agent.ID)
			cancel()
			if err != nil {
				return model.DeletionResult{}, fmt.Errorf("stop background agent %s: %w", agent.Title, err)
			}
		}
		if strings.TrimSpace(agent.RendererID) == "" && agent.RuntimeID != "" && !agent.IsBackground() {
			// Preserve the durable soft deletion. Cleanup stays blocked until the
			// unmanaged runtime stops, as it did before background runners existed.
			continue
		}
		if strings.TrimSpace(agent.RendererID) != "" {
			if a.Renderer == nil || agent.Renderer != a.Renderer.Name() || agent.RendererContext != a.Renderer.Context() {
				return model.DeletionResult{}, fmt.Errorf("cannot close the terminal view for agent %s in renderer %s context %s", agent.Title, agent.Renderer, agent.RendererContext)
			}
			if err := a.Renderer.CloseAgent(ctx, agent); err != nil {
				return model.DeletionResult{}, fmt.Errorf("close terminal view for agent %s: %w", agent.Title, err)
			}
		}
		if agent.RuntimeID != "" {
			if err := a.Store.StopAgentRuntime(ctx, agent.ID, agent.RuntimeID, "hidden by user"); err != nil && !IsNotFound(err) {
				return model.DeletionResult{}, fmt.Errorf("stop agent %s: %w", agent.Title, err)
			}
		}
	}
	return a.Store.SoftDelete(ctx, kind, id)
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
		agent, ok := byID[id]
		if !ok {
			return model.AgentCleanupResult{}, fmt.Errorf("agent %s was not created by the calling agent", id)
		}
		if !agent.IsBackground() {
			return model.AgentCleanupResult{}, fmt.Errorf("agent %s is in the foreground and is protected from creator cleanup", id)
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
		if agent.RendererID == "" && agent.RuntimeID != "" && !agent.IsBackground() {
			return model.AgentCleanupResult{}, fmt.Errorf("cannot stop active agent %s without its managed terminal view", agent.Title)
		}
	}
	for _, agent := range agents {
		if agent.IsBackground() {
			stopCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
			err := a.stopBackgroundProcess(stopCtx, agent.ID)
			cancel()
			if err != nil {
				return model.AgentCleanupResult{}, fmt.Errorf("stop background agent %s: %w", agent.Title, err)
			}
		}
		if agent.RendererID != "" {
			if err := a.Renderer.CloseAgent(ctx, agent); err != nil {
				return model.AgentCleanupResult{}, fmt.Errorf("close terminal view for agent %s: %w", agent.Title, err)
			}
			result.ClosedViews++
		}
		if agent.RuntimeID != "" {
			if err := a.Store.StopAgentRuntime(ctx, agent.ID, agent.RuntimeID, "cleaned by creator"); err != nil && !IsNotFound(err) {
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
	presentation := request.Presentation
	if presentation == "" && creatorID != "" {
		presentation = "background"
	}
	if presentation == "" {
		presentation = "foreground"
	}
	value := model.Agent{ID: id, WorkspaceID: workspace.ID, Title: title, Role: strings.TrimSpace(request.Role), CreatedByAgentID: creatorID, Presentation: presentation, ContextAgentID: contextAgentID, Placement: placement, Kind: "pi", Status: "stopped", SessionID: id, CreatedAt: now, UpdatedAt: now}
	if err := a.Store.PutAgent(ctx, value, created); err != nil {
		return model.Agent{}, err
	}
	committed = true
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
	if strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Prompt) == "" {
		return CreateAgentFromSourceResult{}, fmt.Errorf("agent title and prompt are required")
	}
	if utf8.RuneCountInString(request.Title) > companionTitleLimit || utf8.RuneCountInString(request.Role) > companionRoleLimit || utf8.RuneCountInString(request.Prompt) > companionPromptLimit {
		return CreateAgentFromSourceResult{}, fmt.Errorf("agent title, role, or prompt exceeds companion limits")
	}
	request.SourceAgentID = strings.TrimSpace(request.SourceAgentID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	repositoryIDs := make([]string, 0, len(request.RepositoryIDs))
	seenRepositories := make(map[string]bool)
	for _, repositoryID := range request.RepositoryIDs {
		repositoryID = strings.TrimSpace(repositoryID)
		if repositoryID != "" && !seenRepositories[repositoryID] {
			seenRepositories[repositoryID] = true
			repositoryIDs = append(repositoryIDs, repositoryID)
		}
	}
	request.RepositoryIDs = repositoryIDs
	if request.SourceAgentID == "" && (request.WorkspaceID == "" || len(request.RepositoryIDs) == 0) {
		return CreateAgentFromSourceResult{}, fmt.Errorf("workspace and repository are required")
	}
	if request.SourceAgentID != "" && len(request.RepositoryIDs) > 0 {
		return CreateAgentFromSourceResult{}, fmt.Errorf("choose repositories or a source agent, not both")
	}
	if len(request.RepositoryIDs) > 8 {
		return CreateAgentFromSourceResult{}, fmt.Errorf("at most eight repositories can be selected")
	}
	var cached CreateAgentFromSourceResult
	fresh, err := a.admitCompanionMutation(ctx, idempotencyKey, "create_agent", request, &cached)
	if err != nil || !fresh {
		return cached, err
	}
	workspaceID := request.WorkspaceID
	placement := AgentPlacementRequest{Type: "worktrees"}
	if request.SourceAgentID != "" {
		source, err := a.Store.Agent(ctx, request.SourceAgentID)
		if err != nil {
			return CreateAgentFromSourceResult{}, err
		}
		if source.Placement.Type == "none" {
			return CreateAgentFromSourceResult{}, fmt.Errorf("source agent does not have a managed worktree placement")
		}
		if workspaceID == "" {
			workspaceID = source.WorkspaceID
		}
		placement = AgentPlacementRequest{Type: "agent", SourceAgentID: source.ID, Share: false}
	} else {
		for _, repositoryID := range request.RepositoryIDs {
			placement.Worktrees = append(placement.Worktrees, AgentPlacementWorktreeRequest{RepositoryID: repositoryID, FetchFirst: true})
		}
	}
	agent, err := a.CreateAgent(ctx, CreateAgentRequest{
		Title:       request.Title,
		Role:        request.Role,
		WorkspaceID: workspaceID,
		Placement:   placement,
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
	if strings.TrimSpace(prompt) == "" {
		return model.AgentMessage{}, fmt.Errorf("prompt is required")
	}
	if utf8.RuneCountInString(prompt) > companionPromptLimit {
		return model.AgentMessage{}, fmt.Errorf("prompt exceeds companion limits")
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
	if _, err := a.Store.Agent(ctx, strings.TrimSpace(agentID)); err != nil {
		return model.AgentMessage{}, err
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
	promoting := agent.IsBackground()
	if promoting {
		if agent.Status == "running" || agent.Status == "starting" {
			return model.Agent{}, fmt.Errorf("agent %s is still working in the background; wait until it is idle before opening it", agent.Title)
		}
		if agent.Status == "idle" && agent.RuntimeID != "" {
			if err := a.Store.RevokeIdleBackgroundRuntime(ctx, agent.ID, agent.RuntimeID); err != nil {
				if IsNotFound(err) {
					return model.Agent{}, fmt.Errorf("agent %s started background work while it was being opened; wait until it is idle", agent.Title)
				}
				return model.Agent{}, err
			}
		}
		stopCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
		err = a.stopBackgroundProcess(stopCtx, agent.ID)
		cancel()
		if err != nil {
			return model.Agent{}, fmt.Errorf("stop background Pi before opening agent: %w", err)
		}
		if agent.RuntimeID != "" {
			_ = a.Store.StopAgentRuntime(ctx, agent.ID, agent.RuntimeID, "promoted to foreground")
		}
		agent, err = a.Store.Agent(ctx, id)
		if err != nil {
			return model.Agent{}, err
		}
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
	openedAgent := agent
	openedAgent.Renderer = a.Renderer.Name()
	openedAgent.RendererContext = a.Renderer.Context()
	openedAgent.RendererID = paneID
	if err := a.Store.SetRenderer(ctx, ws.ID, a.Renderer.Name(), a.Renderer.Context(), workspaceID); err != nil {
		_ = a.Renderer.CloseAgent(context.Background(), openedAgent)
		_ = a.Store.SetAgentStatus(context.Background(), agent.ID, "failed", err.Error())
		return model.Agent{}, err
	}
	if promoting {
		err = a.Store.SetAgentForegroundRenderer(ctx, agent.ID, a.Renderer.Name(), a.Renderer.Context(), paneID)
	} else {
		err = a.Store.SetAgentRenderer(ctx, agent.ID, a.Renderer.Name(), a.Renderer.Context(), paneID)
	}
	if err != nil {
		_ = a.Renderer.CloseAgent(context.Background(), openedAgent)
		_ = a.Store.SetAgentStatus(context.Background(), agent.ID, "failed", err.Error())
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
	return a.QueueAgentMessageIdempotent(ctx, senderID, targetID, prompt, "")
}

func (a *App) QueueAgentMessageIdempotent(ctx context.Context, senderID, targetID, prompt, idempotencyKey string) (model.AgentMessage, error) {
	return a.queueCausalAgentMessageIdempotent(ctx, senderID, targetID, prompt, idempotencyKey, "")
}

func (a *App) queueCausalAgentMessageIdempotent(ctx context.Context, senderID, targetID, prompt, idempotencyKey, parentMessageID string) (model.AgentMessage, error) {
	value, fresh, err := a.enqueueCausalAgentMessageIdempotent(ctx, senderID, targetID, prompt, strings.TrimSpace(idempotencyKey), parentMessageID)
	if err != nil {
		return model.AgentMessage{}, err
	}
	if fresh {
		a.startAgentForQueuedMessage(ctx, targetID, value.ID)
	}
	return value, nil
}

func (a *App) startAgentForQueuedMessage(ctx context.Context, targetID, messageID string) {
	if _, err := a.StartAgent(ctx, targetID); err != nil {
		if a.Logger != nil {
			a.Logger.Printf("start Pi agent %s for message %s: %v", targetID, messageID, err)
		}
		a.scheduleAgentStartRetry(targetID, messageID)
	}
}

func (a *App) enqueueAgentMessage(ctx context.Context, senderID, targetID, prompt string) (model.AgentMessage, error) {
	value, _, err := a.enqueueAgentMessageIdempotent(ctx, senderID, targetID, prompt, "")
	return value, err
}

func (a *App) enqueueAgentMessageIdempotent(ctx context.Context, senderID, targetID, prompt, idempotencyKey string) (model.AgentMessage, bool, error) {
	return a.enqueueCausalAgentMessageIdempotent(ctx, senderID, targetID, prompt, idempotencyKey, "")
}

func (a *App) enqueueCausalAgentMessageIdempotent(ctx context.Context, senderID, targetID, prompt, idempotencyKey, parentMessageID string) (model.AgentMessage, bool, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return model.AgentMessage{}, false, fmt.Errorf("message text is required")
	}
	if len(prompt) > crossAgentPromptByteLimit {
		return model.AgentMessage{}, false, fmt.Errorf("message text exceeds the %d-byte limit", crossAgentPromptByteLimit)
	}
	if len(idempotencyKey) > 200 {
		return model.AgentMessage{}, false, fmt.Errorf("message idempotency key is too long")
	}
	if senderID != "" && senderID == targetID {
		return model.AgentMessage{}, false, fmt.Errorf("an agent cannot send work to itself")
	}
	if _, err := a.Store.Agent(ctx, targetID); err != nil {
		return model.AgentMessage{}, false, err
	}
	now := time.Now().UnixMilli()
	value := model.AgentMessage{
		ID: uuid.NewString(), SenderAgentID: senderID, TargetAgentID: targetID, Kind: "request", Prompt: prompt,
		Status: "queued", IdempotencyKey: idempotencyKey, QueueDeadlineAt: now + (7 * 24 * time.Hour).Milliseconds(), CreatedAt: now, UpdatedAt: now,
	}
	if senderID != "" {
		sender, err := a.Store.Agent(ctx, senderID)
		if err != nil {
			return model.AgentMessage{}, false, err
		}
		value.SenderTitle = sender.Title
	}
	parentMessageID = strings.TrimSpace(parentMessageID)
	if parentMessageID == "" {
		value.RootMessageID = value.ID
		value.RunID = uuid.NewString()
	} else {
		parent, err := a.Store.AgentMessageForParticipant(ctx, parentMessageID, senderID)
		if err != nil {
			return model.AgentMessage{}, false, fmt.Errorf("current delivery was not found: %w", err)
		}
		if parent.TargetAgentID != senderID || parent.Status != "delivered" ||
			(parent.LeaseExpiresAt > 0 && parent.LeaseExpiresAt <= now) ||
			(parent.ProcessingDeadlineAt > 0 && parent.ProcessingDeadlineAt <= now) {
			return model.AgentMessage{}, false, fmt.Errorf("current delivery is not active for the sending agent")
		}
		value.ParentMessageID = parent.ID
		value.RootMessageID = parent.RootMessageID
		value.RunID = parent.RunID
		value.Depth = parent.Depth + 1
		if value.Depth > crossAgentMaxDepth {
			return model.AgentMessage{}, false, fmt.Errorf("cross-agent orchestration depth exceeds the safe limit of %d", crossAgentMaxDepth)
		}
	}
	return a.Store.PutAgentMessageIdempotent(ctx, value)
}

func (a *App) AwaitAgentMessage(ctx context.Context, id string) (model.AgentMessage, error) {
	value, err := a.Store.AgentMessage(ctx, id)
	if err != nil || agentMessageSettled(value) {
		return value, err
	}
	notified, unregister := a.registerMessageWaiters([]string{id})
	defer unregister()

	// Read after registration. This closes the race between the first durable
	// read and waiter registration.
	value, err = a.Store.AgentMessage(ctx, id)
	if err != nil || agentMessageSettled(value) {
		return value, err
	}
	backstop := time.NewTicker(5 * time.Second)
	defer backstop.Stop()
	for {
		select {
		case <-ctx.Done():
			return model.AgentMessage{}, ctx.Err()
		case <-notified:
		case <-backstop.C:
		}
		value, err = a.Store.AgentMessage(ctx, id)
		if err != nil || agentMessageSettled(value) {
			return value, err
		}
	}
}

func agentMessageSettled(value model.AgentMessage) bool {
	return value.Status == "completed" || value.Status == "failed"
}

func (a *App) registerMessageWaiters(ids []string) (<-chan struct{}, func()) {
	notified := make(chan struct{}, 1)
	a.messageWaiterMu.Lock()
	if a.messageWaiters == nil {
		a.messageWaiters = make(map[string]map[chan struct{}]struct{})
	}
	for _, id := range ids {
		if a.messageWaiters[id] == nil {
			a.messageWaiters[id] = make(map[chan struct{}]struct{})
		}
		a.messageWaiters[id][notified] = struct{}{}
	}
	a.messageWaiterMu.Unlock()
	return notified, func() {
		a.messageWaiterMu.Lock()
		defer a.messageWaiterMu.Unlock()
		for _, id := range ids {
			delete(a.messageWaiters[id], notified)
			if len(a.messageWaiters[id]) == 0 {
				delete(a.messageWaiters, id)
			}
		}
	}
}

func (a *App) notifyMessageWaiters(id string) {
	a.messageWaiterMu.Lock()
	defer a.messageWaiterMu.Unlock()
	for notified := range a.messageWaiters[id] {
		select {
		case notified <- struct{}{}:
		default:
		}
	}
}

func (a *App) notifyAllMessageWaiters() {
	a.messageWaiterMu.Lock()
	defer a.messageWaiterMu.Unlock()
	for _, waiters := range a.messageWaiters {
		for notified := range waiters {
			select {
			case notified <- struct{}{}:
			default:
			}
		}
	}
}

var conversationEventKinds = map[string]bool{
	"user_message": true, "assistant_message_start": true, "assistant_text_delta": true,
	"assistant_reasoning_start": true, "assistant_reasoning_delta": true, "assistant_reasoning_end": true,
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
		if len(event.PiEntryID) > 200 || len(event.ToolName) > 200 || len(event.ToolCallID) > 200 || len(event.Content) > 64<<10 {
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

func (a *App) PrepareRuntime(ctx context.Context, agentID, runtimeID string) error {
	a.legacyRuntimeMu.Lock()
	delete(a.legacyRuntimeTools, agentID)
	a.legacyRuntimeMu.Unlock()
	return a.Store.PrepareAgentRuntime(ctx, agentID, strings.TrimSpace(runtimeID))
}

func (a *App) LegacyRuntimeID(agentID string) string {
	a.legacyRuntimeMu.Lock()
	defer a.legacyRuntimeMu.Unlock()
	return a.legacyRuntimeTools[agentID]
}

func (a *App) RegisterRuntime(ctx context.Context, agentID, runtimeID, sessionID, sessionPath string) error {
	unlock := a.lockAgentLifecycle(agentID)
	defer unlock()
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
	if err := a.Store.RegisterPreparedAgentRuntime(ctx, agentID, runtimeID, sessionID, sessionPath); err != nil {
		return err
	}
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
	return a.reportAgent(ctx, agentID, status, lastError)
}

func (a *App) StopRuntime(ctx context.Context, agentID, runtimeID, lastError string) error {
	unlock := a.lockAgentLifecycle(agentID)
	defer unlock()
	if err := a.Store.StopAgentRuntime(ctx, agentID, runtimeID, lastError); err != nil {
		return err
	}
	return a.reportAgent(ctx, agentID, "stopped", lastError)
}

func (a *App) ClaimMessage(ctx context.Context, agentID, runtimeID, claimKey string) (*model.AgentMessage, error) {
	claimKey = strings.TrimSpace(claimKey)
	if claimKey == "" || len(claimKey) > 200 {
		return nil, fmt.Errorf("a valid claim ID is required")
	}
	agent, err := a.Store.Agent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if agent.RuntimeID == "" || agent.RuntimeID != runtimeID {
		return nil, fmt.Errorf("pi runtime is not registered for this agent")
	}
	return a.Store.ClaimAgentMessage(ctx, agentID, runtimeID, claimKey)
}

func (a *App) RenewMessageLease(ctx context.Context, agentID, messageID, runtimeID string, attempt int) error {
	if attempt < 1 {
		return fmt.Errorf("a valid delivery attempt is required")
	}
	return a.Store.RenewAgentMessageLease(ctx, messageID, agentID, runtimeID, attempt)
}

func (a *App) CompleteMessage(ctx context.Context, agentID, messageID, runtimeID string, attempt int, response, failure string) error {
	if len(response) > crossAgentResultByteLimit {
		return fmt.Errorf("agent response exceeds the %d-byte limit", crossAgentResultByteLimit)
	}
	if len(failure) > crossAgentErrorByteLimit {
		return fmt.Errorf("agent error exceeds the %d-byte limit", crossAgentErrorByteLimit)
	}
	if err := a.Store.CompleteAgentMessage(ctx, messageID, agentID, runtimeID, attempt, response, failure); err != nil {
		return err
	}
	a.notifyMessageWaiters(messageID)
	request, err := a.Store.AgentMessage(ctx, messageID)
	if err == nil && request.Kind == "request" && request.SenderAgentID != "" {
		result, resultErr := a.Store.AgentMessage(ctx, "result:"+request.ID)
		if resultErr == nil && result.Status == "queued" {
			if sender, readErr := a.Store.Agent(ctx, request.SenderAgentID); readErr == nil && sender.RuntimeID == "" {
				a.startAgentForQueuedMessage(ctx, sender.ID, result.ID)
			}
		}
	}
	return nil
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
		return a.queueCausalAgentMessageIdempotent(ctx, callerID, target.ID, stringArg(args, "prompt"), stringArg(args, "__request_id"), stringArg(args, "__parent_message_id"))
	case "read_message":
		return a.Store.AgentMessageForParticipant(ctx, stringArg(args, "message_id"), callerID)
	case "cleanup_agents":
		agentIDs, err := stringListArg(args, "agent_ids")
		if err != nil {
			return nil, err
		}
		return a.CleanupAgents(ctx, callerID, agentIDs)
	case "await_agent":
		messageID := stringArg(args, "message_id")
		many, err := a.awaitAgentToolMessages(ctx, callerID, []string{messageID}, "all", agentWaitTimeout(args))
		if err != nil {
			return nil, err
		}
		return many.Outcomes[0], nil
	case "await_agents":
		messageIDs, err := stringListArg(args, "message_ids")
		if err != nil {
			return nil, err
		}
		returnWhen := stringArg(args, "return_when")
		if returnWhen == "" {
			returnWhen = "all"
		}
		return a.awaitAgentToolMessages(ctx, callerID, messageIDs, returnWhen, agentWaitTimeout(args))
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
		agent, err := a.CreateAgent(ctx, CreateAgentRequest{Title: stringArg(args, "title"), Role: stringArg(args, "role"), WorkspaceID: ws.ID, CreatedByAgentID: callerID, Presentation: "background", ContextAgentID: contextAgentID, Placement: placement})
		if err != nil {
			return nil, err
		}
		result := CreateAgentToolResult{Agent: agent}
		if prompt := stringArg(args, "prompt"); prompt != "" {
			message, _, err := a.enqueueCausalAgentMessageIdempotent(ctx, callerID, agent.ID, prompt, "", stringArg(args, "__parent_message_id"))
			if err != nil {
				return nil, err
			}
			result.InitialMessage = &message
		}
		result.Agent, err = a.StartBackgroundAgent(ctx, agent.ID)
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
	return a.beginAgentWaits(callerID, []model.AgentMessage{message})
}

func (a *App) beginAgentWaits(callerID string, messages []model.AgentMessage) (func(), error) {
	callerID = strings.TrimSpace(callerID)
	if callerID == "" {
		return func() {}, fmt.Errorf("the waiting Galpon agent is required")
	}
	a.waitMu.Lock()
	defer a.waitMu.Unlock()
	for _, message := range messages {
		if path := a.agentWaitPathLocked(message.TargetAgentID, callerID, map[string]bool{}); len(path) != 0 {
			cycle := append([]string{callerID}, path...)
			return func() {}, fmt.Errorf("cross-agent wait cycle detected: %s; finish the current message instead of waiting", strings.Join(cycle, " -> "))
		}
	}
	if a.waits == nil {
		a.waits = map[string]map[string]string{}
	}
	if a.waits[callerID] == nil {
		a.waits[callerID] = map[string]string{}
	}
	for _, message := range messages {
		a.waits[callerID][message.ID] = message.TargetAgentID
	}
	return func() {
		a.waitMu.Lock()
		defer a.waitMu.Unlock()
		for _, message := range messages {
			delete(a.waits[callerID], message.ID)
		}
		if len(a.waits[callerID]) == 0 {
			delete(a.waits, callerID)
		}
	}, nil
}

func (a *App) awaitAgentToolMessages(ctx context.Context, callerID string, ids []string, returnWhen string, timeout time.Duration) (model.AgentWaitManyResult, error) {
	if len(ids) < 1 || len(ids) > 16 {
		return model.AgentWaitManyResult{}, fmt.Errorf("message_ids must contain between 1 and 16 items")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return model.AgentWaitManyResult{}, fmt.Errorf("message IDs must not be empty")
		}
		if seen[id] {
			return model.AgentWaitManyResult{}, fmt.Errorf("duplicate message ID %q", id)
		}
		seen[id] = true
	}
	if returnWhen != "any" && returnWhen != "all" {
		return model.AgentWaitManyResult{}, fmt.Errorf("return_when must be any or all")
	}

	messages, err := a.readParticipantMessages(ctx, callerID, ids)
	if err != nil {
		return model.AgentWaitManyResult{}, err
	}
	if waitConditionMet(messages, returnWhen) {
		return a.finishAgentWaitResult(ctx, callerID, messages, returnWhen, "completed")
	}
	pending := pendingAgentMessages(messages)
	notified, unregister := a.registerMessageWaiters(messageIDs(pending))
	defer unregister()
	// Read after registration to close the durable-read/registration race.
	messages, err = a.readParticipantMessages(ctx, callerID, ids)
	if err != nil {
		return model.AgentWaitManyResult{}, err
	}
	if waitConditionMet(messages, returnWhen) {
		return a.finishAgentWaitResult(ctx, callerID, messages, returnWhen, "completed")
	}

	waitEdgesActive := false
	defer func() {
		if waitEdgesActive {
			_ = a.setAgentWaits(callerID, nil)
		}
	}()
	replaceWaitEdges := func(messages []model.AgentMessage) error {
		if err := a.setAgentWaits(callerID, pendingAgentMessages(messages)); err != nil {
			return err
		}
		waitEdgesActive = true
		return nil
	}
	if waitErr := replaceWaitEdges(messages); waitErr != nil {
		refreshed, err := a.readParticipantMessages(ctx, callerID, ids)
		if err != nil {
			return model.AgentWaitManyResult{}, err
		}
		if waitConditionMet(refreshed, returnWhen) {
			return a.finishAgentWaitResult(ctx, callerID, refreshed, returnWhen, "completed")
		}
		if len(pendingAgentMessages(refreshed)) == len(pendingAgentMessages(messages)) {
			return model.AgentWaitManyResult{}, waitErr
		}
		messages = refreshed
		if err := replaceWaitEdges(messages); err != nil {
			return model.AgentWaitManyResult{}, err
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	backstop := time.NewTicker(5 * time.Second)
	defer backstop.Stop()
	for {
		select {
		case <-waitCtx.Done():
			messages, err = a.readParticipantMessages(ctx, callerID, ids)
			if err != nil {
				return model.AgentWaitManyResult{}, err
			}
			status := "canceled"
			if waitConditionMet(messages, returnWhen) {
				status = "completed"
			} else if errors.Is(waitCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				status = "timeout"
			}
			return a.finishAgentWaitResult(ctx, callerID, messages, returnWhen, status)
		case <-notified:
		case <-backstop.C:
		}
		messages, err = a.readParticipantMessages(ctx, callerID, ids)
		if err != nil {
			return model.AgentWaitManyResult{}, err
		}
		if waitConditionMet(messages, returnWhen) {
			return a.finishAgentWaitResult(ctx, callerID, messages, returnWhen, "completed")
		}
		// For an all-wait, remove edges for work that has settled. This keeps
		// cycle detection accurate while the rest of the batch is still active.
		if err := replaceWaitEdges(messages); err != nil {
			return model.AgentWaitManyResult{}, err
		}
	}
}

func (a *App) readParticipantMessages(ctx context.Context, callerID string, ids []string) ([]model.AgentMessage, error) {
	messages := make([]model.AgentMessage, len(ids))
	for index, id := range ids {
		message, err := a.Store.AgentMessageForParticipant(ctx, id, callerID)
		if err != nil {
			return nil, err
		}
		messages[index] = message
	}
	return messages, nil
}

func waitConditionMet(messages []model.AgentMessage, returnWhen string) bool {
	settled := 0
	for _, message := range messages {
		if agentMessageSettled(message) {
			settled++
		}
	}
	return settled == len(messages) || (returnWhen == "any" && settled > 0)
}

func pendingAgentMessages(messages []model.AgentMessage) []model.AgentMessage {
	out := make([]model.AgentMessage, 0, len(messages))
	for _, message := range messages {
		if !agentMessageSettled(message) {
			out = append(out, message)
		}
	}
	return out
}

func messageIDs(messages []model.AgentMessage) []string {
	out := make([]string, len(messages))
	for index := range messages {
		out[index] = messages[index].ID
	}
	return out
}

func (a *App) finishAgentWaitResult(ctx context.Context, callerID string, messages []model.AgentMessage, returnWhen, status string) (model.AgentWaitManyResult, error) {
	result := model.AgentWaitManyResult{Status: status, ReturnWhen: returnWhen, Total: len(messages), Outcomes: make([]model.AgentWaitResult, len(messages))}
	for index, message := range messages {
		waitStatus := "pending"
		var waitError *model.AgentWaitError
		if message.Status == "completed" {
			waitStatus = "completed"
			result.Completed++
		} else if message.Status == "failed" {
			waitStatus = "failed"
			result.Completed++
			waitError = &model.AgentWaitError{Kind: "message_failed", Message: message.Error}
		} else if status == "timeout" {
			waitStatus = "timeout"
			waitError = &model.AgentWaitError{Kind: "timeout", Message: "the global wait timeout expired before this message settled"}
		} else if status == "canceled" {
			waitStatus = "canceled"
			waitError = &model.AgentWaitError{Kind: "request_canceled", Message: "the wait request was canceled"}
		}
		runtimeStatus := "unknown"
		if target, err := a.Store.Agent(ctx, message.TargetAgentID); err == nil {
			runtimeStatus = target.Status
		}
		result.Outcomes[index] = model.AgentWaitResult{AgentMessage: message, MessageID: message.ID, WaitStatus: waitStatus, MessageStatus: message.Status, TargetRuntimeStatus: runtimeStatus, WaitError: waitError}
		if agentMessageSettled(message) && message.SenderAgentID == callerID {
			if err := a.Store.ConsumeAgentMessageResult(ctx, message.ID, callerID); err != nil {
				return model.AgentWaitManyResult{}, err
			}
		}
	}
	return result, nil
}

// setAgentWaits atomically replaces one caller's batch edges. This prevents a
// gap between partial outcomes where another concurrent wait could miss a cycle.
func (a *App) setAgentWaits(callerID string, messages []model.AgentMessage) error {
	callerID = strings.TrimSpace(callerID)
	if callerID == "" {
		return fmt.Errorf("the waiting Galpon agent is required")
	}
	a.waitMu.Lock()
	defer a.waitMu.Unlock()
	if a.waits == nil {
		a.waits = map[string]map[string]string{}
	}
	previous := a.waits[callerID]
	delete(a.waits, callerID)
	for _, message := range messages {
		if path := a.agentWaitPathLocked(message.TargetAgentID, callerID, map[string]bool{}); len(path) != 0 {
			if previous != nil {
				a.waits[callerID] = previous
			}
			cycle := append([]string{callerID}, path...)
			return fmt.Errorf("cross-agent wait cycle detected: %s; finish the current message instead of waiting", strings.Join(cycle, " -> "))
		}
	}
	if len(messages) != 0 {
		a.waits[callerID] = make(map[string]string, len(messages))
		for _, message := range messages {
			a.waits[callerID][message.ID] = message.TargetAgentID
		}
	}
	return nil
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
