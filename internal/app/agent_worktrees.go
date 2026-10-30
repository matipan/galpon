package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/gitx"
	"github.com/matipan/galpon/internal/model"
)

// checkoutCacheAge limits Git reads when views poll the dashboard.
const checkoutCacheAge = 2 * time.Second

type checkoutCacheEntry struct {
	at        time.Time
	checkouts map[string]gitx.Checkout
}

type AddAgentWorktreeRequest struct {
	// Repository is an ID or an exact title. It defaults to the repository of
	// the agent's primary worktree.
	Repository string `json:"repository,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Base       string `json:"base,omitempty"`
	Remote     string `json:"remote,omitempty"`
	Detach     bool   `json:"detach,omitempty"`
	NoFetch    bool   `json:"noFetch,omitempty"`
}

type AddAgentWorktreeResult struct {
	Folder   string         `json:"folder"`
	Worktree model.Worktree `json:"worktree"`
}

type RemoveAgentWorktreeRequest struct {
	// Worktree is a worktree path or ID.
	Worktree string `json:"worktree"`
	Force    bool   `json:"force,omitempty"`
}

type RemoveAgentWorktreeResult struct {
	Worktree        model.Worktree `json:"worktree"`
	DeletedBranches []string       `json:"deletedBranches,omitempty"`
	KeptBranches    []string       `json:"keptBranches,omitempty"`
	Note            string         `json:"note,omitempty"`
}

// AgentFolder returns the managed folder for an agent's worktrees and scratch
// files. The folder of an existing private worktree wins over the name from the
// current titles, so a renamed agent keeps one folder.
func AgentFolder(stateDir string, dashboard model.Dashboard, agent model.Agent) string {
	root := filepath.Join(stateDir, "worktrees")
	suffix := "-" + shortID(agent.ID)
	for _, assignment := range agent.Placement.Worktrees {
		worktree, ok := dashboard.Worktree(assignment.WorktreeID)
		if !ok {
			continue
		}
		folder := filepath.Dir(worktree.Path)
		if pathInside(root, folder) && strings.HasSuffix(filepath.Base(folder), suffix) {
			return folder
		}
	}
	workspace, _ := dashboard.Workspace(agent.WorkspaceID)
	return filepath.Join(root, gitx.Slug(workspace.Title)+"-"+shortID(agent.WorkspaceID), gitx.Slug(agent.Title)+suffix)
}

// PrepareAgentFolder creates the agent folder and its scratch folder.
func PrepareAgentFolder(stateDir string, dashboard model.Dashboard, agent model.Agent) (string, error) {
	folder := AgentFolder(stateDir, dashboard, agent)
	if err := os.MkdirAll(filepath.Join(folder, "scratch"), 0o700); err != nil {
		return "", fmt.Errorf("create agent folder: %w", err)
	}
	return folder, nil
}

// FolderInstructions tells an agent where its checkouts and work files go.
func FolderInstructions(folder, harness string) string {
	outside := "/tmp, ~/.cache"
	if harness == model.HarnessClaude {
		outside = "/tmp, the Claude Code scratchpad, ~/.cache"
	}
	return "Your Galpón folder is " + folder + ". Continue a feature in its worktree. " +
		"When the work on the current branch is done, for example when its pull request is merged or closed, you can start the next feature on a new branch in that worktree. " +
		"When the current work is not done, or when you need another checkout for a review or a comparison, use galpon_add_worktree to create a worktree in your Galpón folder. " +
		"When you no longer need an extra worktree, remove it with galpon_remove_worktree. " +
		"Put other work files, such as scripts, logs, and test output, in " + filepath.Join(folder, "scratch") + ". " +
		"Do not create Git worktrees, clones, or work folders in " + outside + ", or other places outside your Galpón folder, unless the user asks for another place."
}

// PlacementDescription describes an agent's directory and worktrees with what
// each worktree has checked out.
func PlacementDescription(dashboard model.Dashboard, agent model.Agent) string {
	parts := make([]string, 0, len(agent.Placement.Worktrees)+1)
	if agent.Placement.Type == "none" {
		parts = append(parts, "a directory at "+agent.Placement.CWD)
	}
	for _, assignment := range agent.Placement.Worktrees {
		worktree, ok := dashboard.Worktree(assignment.WorktreeID)
		if !ok {
			continue
		}
		repository, _ := dashboard.Repository(worktree.RepositoryID)
		kind := "additional"
		if agent.Placement.Type != "none" {
			kind = "secondary"
			if worktree.ID == agent.Placement.PrimaryWorktreeID {
				kind = "primary"
			}
		}
		parts = append(parts, fmt.Sprintf("%s %s worktree at %s %s (%s)", kind, repository.Title, worktree.Path, checkoutPhrase(worktree), assignment.Mode))
	}
	return strings.Join(parts, "; ")
}

func checkoutPhrase(worktree model.Worktree) string {
	label := worktree.CheckoutLabel()
	if strings.HasPrefix(label, "detached") {
		return label
	}
	return "on branch " + label
}

// withCheckouts fills the current checkout of each worktree from Git. Git
// reads are cached briefly because views poll the dashboard.
func (a *App) withCheckouts(ctx context.Context, dashboard model.Dashboard) model.Dashboard {
	if len(dashboard.Worktrees) == 0 {
		return dashboard
	}
	dashboard.Worktrees = a.worktreeCheckouts(ctx, dashboard.Repositories, dashboard.Worktrees)
	return dashboard
}

func (a *App) worktreeCheckouts(ctx context.Context, repositories []model.Repository, worktrees []model.Worktree) []model.Worktree {
	mirrors := make(map[string]string, len(repositories))
	for _, repository := range repositories {
		mirrors[repository.ID] = repository.MirrorPath
	}
	out := make([]model.Worktree, len(worktrees))
	copy(out, worktrees)
	for index := range out {
		mirror := mirrors[out[index].RepositoryID]
		if mirror == "" {
			continue
		}
		checkouts := a.mirrorCheckouts(ctx, mirror)
		checkout, ok := checkouts[filepath.Clean(out[index].Path)]
		if !ok {
			if real, err := filepath.EvalSymlinks(out[index].Path); err == nil {
				checkout, ok = checkouts[filepath.Clean(real)]
			}
		}
		if ok {
			out[index].CurrentBranch, out[index].Head, out[index].Detached = checkout.Branch, checkout.Head, checkout.Detached
		}
	}
	return out
}

func (a *App) mirrorCheckouts(ctx context.Context, mirror string) map[string]gitx.Checkout {
	a.checkoutMu.Lock()
	entry, ok := a.checkoutCache[mirror]
	a.checkoutMu.Unlock()
	if ok && time.Since(entry.at) < checkoutCacheAge {
		return entry.checkouts
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	checkouts, err := gitx.Checkouts(readCtx, mirror)
	if err != nil {
		checkouts = nil
	}
	a.checkoutMu.Lock()
	if a.checkoutCache == nil {
		a.checkoutCache = make(map[string]checkoutCacheEntry)
	}
	a.checkoutCache[mirror] = checkoutCacheEntry{at: time.Now(), checkouts: checkouts}
	a.checkoutMu.Unlock()
	return checkouts
}

func (a *App) forgetCheckouts(mirror string) {
	a.checkoutMu.Lock()
	delete(a.checkoutCache, mirror)
	a.checkoutMu.Unlock()
}

// AddAgentWorktree creates a worktree in the agent folder and adds it to the
// agent's placement after the existing worktrees.
func (a *App) AddAgentWorktree(ctx context.Context, agentID string, request AddAgentWorktreeRequest) (AddAgentWorktreeResult, error) {
	a.agentMutationMu.Lock()
	defer a.agentMutationMu.Unlock()

	dashboard, err := a.Store.Dashboard(ctx)
	if err != nil {
		return AddAgentWorktreeResult{}, err
	}
	agent, ok := dashboard.Agent(strings.TrimSpace(agentID))
	if !ok {
		return AddAgentWorktreeResult{}, fmt.Errorf("agent not found: %s", agentID)
	}
	workspace, ok := dashboard.Workspace(agent.WorkspaceID)
	if !ok {
		return AddAgentWorktreeResult{}, fmt.Errorf("the agent's workspace is not available")
	}
	repository, err := agentWorktreeRepository(dashboard, agent, request.Repository)
	if err != nil {
		return AddAgentWorktreeResult{}, err
	}
	branch := strings.TrimSpace(request.Branch)
	base := strings.TrimSpace(request.Base)
	if request.Detach && branch != "" {
		return AddAgentWorktreeResult{}, invalidRequestf("a detached worktree cannot have a branch")
	}
	remote := strings.TrimSpace(request.Remote)
	if remote == "" {
		remote = repository.DefaultRemote
	}
	folder := AgentFolder(a.Config.StateDir, dashboard, agent)
	worktreeID := uuid.NewString()
	name := gitx.Slug(repository.Title)
	switch {
	case branch != "":
		name += "-" + gitx.Slug(branch)
	case request.Detach && base != "":
		name += "-" + gitx.Slug(base)
	case request.Detach:
		name += "-detached"
	default:
		branch = "galpon/" + gitx.Slug(workspace.Title) + "/" + gitx.Slug(agent.Title) + "-" + shortID(agent.ID) + "/" + gitx.Slug(repository.Title) + "-" + shortID(worktreeID)
	}
	path := filepath.Join(folder, name+"-"+shortID(worktreeID))
	added, err := gitx.AddWorktree(ctx, repository, gitx.WorktreeRequest{Path: path, Branch: branch, Base: base, Remote: remote, Detach: request.Detach, Fetch: !request.NoFetch})
	if err != nil {
		return AddAgentWorktreeResult{}, invalidRequestf("%s", err)
	}
	a.forgetCheckouts(repository.MirrorPath)
	worktree := model.Worktree{ID: worktreeID, WorkspaceID: agent.WorkspaceID, RepositoryID: repository.ID, Path: path, Branch: branch, BaseRef: added.Base, SourceRemote: remote, Lifecycle: "agent", CreatedAt: time.Now().UnixMilli()}
	if err := a.Store.AddAgentWorktree(ctx, agent.ID, worktree); err != nil {
		createdBranch := ""
		if added.CreatedBranch {
			createdBranch = branch
		}
		_ = gitx.CleanupWorktree(context.Background(), repository, path, createdBranch)
		return AddAgentWorktreeResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(folder, "scratch"), 0o700); err != nil && a.Logger != nil {
		a.Logger.Printf("create scratch folder for agent %s: %v", agent.ID, err)
	}
	worktree.CurrentBranch, worktree.Head, worktree.Detached = branch, added.Head, request.Detach
	return AddAgentWorktreeResult{Folder: folder, Worktree: worktree}, nil
}

func agentWorktreeRepository(dashboard model.Dashboard, agent model.Agent, query string) (model.Repository, error) {
	if query = strings.TrimSpace(query); query != "" {
		repository := findRepository(dashboard.Repositories, query)
		if repository.ID == "" {
			return model.Repository{}, invalidRequestf("repository not found: %s", query)
		}
		return repository, nil
	}
	if primary, ok := dashboard.PrimaryWorktree(agent); ok {
		if repository, ok := dashboard.Repository(primary.RepositoryID); ok {
			return repository, nil
		}
	}
	for _, worktree := range dashboard.AgentWorktrees(agent) {
		if repository, ok := dashboard.Repository(worktree.RepositoryID); ok {
			return repository, nil
		}
	}
	return model.Repository{}, invalidRequestf("repository is required because the agent has no worktree")
}

// RemoveAgentWorktree removes one extra worktree of the agent. The primary
// worktree and a worktree that another agent uses stay. A local branch is
// deleted only when the remote-tracking references contain all its commits.
func (a *App) RemoveAgentWorktree(ctx context.Context, agentID string, request RemoveAgentWorktreeRequest) (RemoveAgentWorktreeResult, error) {
	a.agentMutationMu.Lock()
	defer a.agentMutationMu.Unlock()

	query := strings.TrimSpace(request.Worktree)
	if query == "" {
		return RemoveAgentWorktreeResult{}, invalidRequestf("worktree path or ID is required")
	}
	dashboard, err := a.Store.DashboardWithHidden(ctx)
	if err != nil {
		return RemoveAgentWorktreeResult{}, err
	}
	agent, ok := dashboard.Agent(strings.TrimSpace(agentID))
	if !ok || agent.Hidden {
		return RemoveAgentWorktreeResult{}, fmt.Errorf("agent not found: %s", agentID)
	}
	var target model.Worktree
	for _, worktree := range dashboard.AgentWorktrees(agent) {
		if worktree.ID == query || filepath.Clean(worktree.Path) == filepath.Clean(query) {
			target = worktree
			break
		}
	}
	if target.ID == "" {
		return RemoveAgentWorktreeResult{}, invalidRequestf("worktree %s is not in this agent's placement", query)
	}
	if target.ID == agent.Placement.PrimaryWorktreeID {
		return RemoveAgentWorktreeResult{}, invalidRequestf("the primary worktree is the agent's start directory and cannot be removed")
	}
	for _, other := range dashboard.Agents {
		if other.ID == agent.ID {
			continue
		}
		for _, assignment := range other.Placement.Worktrees {
			if assignment.WorktreeID == target.ID {
				return RemoveAgentWorktreeResult{}, invalidRequestf("agent %s also uses worktree %s", other.Title, target.Path)
			}
		}
	}
	if !pathInside(filepath.Join(a.Config.StateDir, "worktrees"), target.Path) {
		return RemoveAgentWorktreeResult{}, fmt.Errorf("refuse to remove unmanaged worktree path %s", target.Path)
	}
	repository, ok := dashboard.Repository(target.RepositoryID)
	if !ok {
		return RemoveAgentWorktreeResult{}, fmt.Errorf("repository for worktree %s was not found", target.Path)
	}
	branches := []string{target.Branch}
	if checkout, err := gitx.WorktreeCheckout(ctx, target.Path); err == nil && checkout.Branch != "" {
		branches = append(branches, checkout.Branch)
	}
	removed, err := gitx.RemoveAgentWorktree(ctx, repository, target.Path, branches, request.Force)
	if errors.Is(err, gitx.ErrWorktreeChanges) {
		return RemoveAgentWorktreeResult{}, invalidRequestf("worktree %s has uncommitted changes; commit them first, or use force to discard them", target.Path)
	}
	if err != nil {
		return RemoveAgentWorktreeResult{}, err
	}
	a.forgetCheckouts(repository.MirrorPath)
	if err := a.Store.RemoveAgentWorktree(ctx, agent.ID, target.ID); err != nil {
		return RemoveAgentWorktreeResult{}, err
	}
	result := RemoveAgentWorktreeResult{Worktree: target, DeletedBranches: removed.DeletedBranches, KeptBranches: removed.KeptBranches}
	if len(removed.KeptBranches) != 0 {
		result.Note = "The kept branches have commits that are not on a remote, or another worktree uses them."
	}
	return result, nil
}

// agentFolders returns the folders of agents before cleanup removes their
// worktrees.
func (a *App) agentFolders(ctx context.Context, agentIDs []string) []string {
	dashboard, err := a.Store.DashboardWithHidden(ctx)
	if err != nil {
		return nil
	}
	folders := make([]string, 0, len(agentIDs))
	for _, id := range agentIDs {
		if agent, ok := dashboard.Agent(id); ok {
			folders = append(folders, AgentFolder(a.Config.StateDir, dashboard, agent))
		}
	}
	return folders
}

// removeAgentFolders removes the scratch folders and then each folder and its
// workspace folder when they are empty. A folder that still holds a worktree
// of another agent stays.
func (a *App) removeAgentFolders(folders []string) {
	root := filepath.Join(a.Config.StateDir, "worktrees")
	for _, folder := range folders {
		if !pathInside(root, folder) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(folder, "scratch")); err != nil && a.Logger != nil {
			a.Logger.Printf("remove scratch folder %s: %v", folder, err)
		}
		if os.Remove(folder) == nil && pathInside(root, filepath.Dir(folder)) {
			_ = os.Remove(filepath.Dir(folder))
		}
	}
}
