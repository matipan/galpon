package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/gitx"
	"github.com/matipan/galpon/internal/model"
)

// agentWorkspace resolves a selected workspace or prepares an uncommitted one.
// Resource names are not IDs: an ambiguous name must be selected from the list.
func agentWorkspace(d model.Dashboard, request CreateAgentRequest) (model.Workspace, *model.Workspace, error) {
	if request.WorkspaceTitle == "" {
		workspace, ok := d.Workspace(request.WorkspaceID)
		if !ok {
			return model.Workspace{}, nil, invalidRequestf("workspace: choose a workspace or enter a name")
		}
		return workspace, nil, nil
	}
	if request.WorkspaceID != "" || request.CreatedByAgentID != "" {
		return model.Workspace{}, nil, invalidRequestf("workspace: a new name is available only in user forms without a selected workspace")
	}
	title, err := validateWorkspaceTitle(request.WorkspaceTitle)
	if err != nil {
		return model.Workspace{}, nil, invalidRequestf("workspace: %s", err)
	}
	var matches []model.Workspace
	for _, workspace := range d.Workspaces {
		if strings.EqualFold(strings.TrimSpace(workspace.Title), title) {
			matches = append(matches, workspace)
		}
	}
	if len(matches) > 1 {
		return model.Workspace{}, nil, invalidRequestf("workspace: more than one workspace has this name; select one from the list")
	}
	if len(matches) == 1 {
		return matches[0], nil, nil
	}
	now := time.Now().UnixMilli()
	workspace := model.Workspace{ID: uuid.NewString(), Title: title, Status: "active", CreatedAt: now, UpdatedAt: now}
	return workspace, &workspace, nil
}

// prepareAgentRepositories builds private mirrors without publishing them.
// The caller removes these mirrors on failure and stores them with the agent.
func (a *App) prepareAgentRepositories(ctx context.Context, d *model.Dashboard, request *CreateAgentRequest) (created []model.Repository, err error) {
	placement := &request.Placement
	if kind := strings.TrimSpace(placement.Type); kind != "worktrees" && kind != "" {
		return nil, nil
	}
	placement.Worktrees = append([]AgentPlacementWorktreeRequest(nil), placement.Worktrees...)
	for index := range placement.Worktrees {
		item := &placement.Worktrees[index]
		if item.RepositoryURL == "" {
			continue
		}
		fail := func(err error) ([]model.Repository, error) {
			return created, fmt.Errorf("repository %d: %w", index+1, err)
		}
		if request.CreatedByAgentID != "" || item.RepositoryID != "" || item.SourceWorktreeID != "" {
			return fail(invalidRequestf("a URL is available only in user forms without a selected repository or worktree"))
		}
		source := strings.TrimSpace(item.RepositoryURL)
		if err := model.ValidateRepositoryURL(source); err != nil {
			return fail(invalidRequestf("%s", err))
		}
		var matches []model.Repository
		for _, repository := range d.Repositories {
			if repository.HasSourceURL(source) {
				matches = append(matches, repository)
			}
		}
		if len(matches) > 1 {
			return fail(invalidRequestf("more than one repository uses this URL; select one from the list"))
		}
		var repository model.Repository
		if len(matches) == 1 {
			repository = matches[0]
		} else {
			if existing, err := a.Store.RepositoryBySource(ctx, source); err != nil {
				return fail(err)
			} else if existing != nil {
				return fail(invalidRequestf("repository %s is deleted; run galpon cleanup before you add it again", existing.Title))
			}
			inspection, err := gitx.InspectRepository(ctx, source)
			if err != nil {
				return fail(err)
			}
			id := uuid.NewString()
			repository = model.Repository{ID: id, Title: inspection.Title, SourcePath: source, FetchURL: source,
				MirrorPath: filepath.Join(a.Config.StateDir, "repositories", id+".git"), DefaultRemote: inspection.DefaultRemote,
				PushRemote: inspection.PushRemote, Remotes: inspection.Remotes, CreatedAt: time.Now().UnixMilli()}
			created = append(created, repository)
			if err := gitx.CreateMirror(ctx, repository.Remotes, repository.DefaultRemote, repository.PushRemote, repository.MirrorPath); err != nil {
				return fail(err)
			}
			repository.DefaultBranch = gitx.DefaultBranch(ctx, repository.MirrorPath, repository.DefaultRemote)
			created[len(created)-1] = repository
			d.Repositories = append(d.Repositories, repository)
		}
		item.RepositoryID, item.RepositoryURL = repository.ID, ""
		if item.Remote == "" {
			for _, remote := range repository.Remotes {
				if model.RepositorySourceKey(remote.FetchURL) == model.RepositorySourceKey(source) {
					item.Remote = remote.Name
					break
				}
			}
		}
	}
	return created, nil
}
