package store

import (
	"context"
	"fmt"

	"github.com/matipan/galpon/internal/model"
)

// DurableState returns all resources that are not marked for cleanup. Archived
// workspaces stay in the export even though the command center hides them.
func (s *Store) DurableState(ctx context.Context) (model.DurableState, error) {
	out := model.DurableState{
		Repositories: []model.Repository{}, Workspaces: []model.Workspace{},
		Worktrees: []model.Worktree{}, Agents: []model.Agent{}, Messages: []model.AgentMessage{},
	}
	rows, err := s.db.QueryContext(ctx, `select id,title,source_path,fetch_url,mirror_path,default_remote,push_remote,default_branch,created_at
from repositories where not exists (select 1 from deleted_items where kind='repository' and resource_id=repositories.id) order by id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var value model.Repository
		if err := rows.Scan(&value.ID, &value.Title, &value.SourcePath, &value.FetchURL, &value.MirrorPath, &value.DefaultRemote, &value.PushRemote, &value.DefaultBranch, &value.CreatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Repositories = append(out.Repositories, value)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	repositoryIndex := make(map[string]int, len(out.Repositories))
	for index := range out.Repositories {
		repositoryIndex[out.Repositories[index].ID] = index
	}
	remoteRows, err := s.db.QueryContext(ctx, `select repository_id,name,fetch_url,push_url from repository_remotes order by repository_id,name`)
	if err != nil {
		return out, err
	}
	for remoteRows.Next() {
		var repositoryID string
		var remote model.RepositoryRemote
		if err := remoteRows.Scan(&repositoryID, &remote.Name, &remote.FetchURL, &remote.PushURL); err != nil {
			_ = remoteRows.Close()
			return out, err
		}
		if index, ok := repositoryIndex[repositoryID]; ok {
			out.Repositories[index].Remotes = append(out.Repositories[index].Remotes, remote)
		}
	}
	if err := remoteRows.Close(); err != nil {
		return out, err
	}

	rows, err = s.db.QueryContext(ctx, `select id,title,status,renderer,renderer_context,renderer_id,created_at,updated_at
from workstreams where not exists (select 1 from deleted_items where kind='workspace' and resource_id=workstreams.id) order by id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var value model.Workspace
		if err := rows.Scan(&value.ID, &value.Title, &value.Status, &value.Renderer, &value.RendererContext, &value.RendererID, &value.CreatedAt, &value.UpdatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Workspaces = append(out.Workspaces, value)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = s.db.QueryContext(ctx, `select id,workstream_id,repository_id,path,branch,base_ref,source_remote,lifecycle,created_at
from worktrees where not exists (select 1 from deleted_items where kind='worktree' and resource_id=worktrees.id) order by id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var value model.Worktree
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.RepositoryID, &value.Path, &value.Branch, &value.BaseRef, &value.SourceRemote, &value.Lifecycle, &value.CreatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Worktrees = append(out.Worktrees, value)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = s.db.QueryContext(ctx, `select id,workstream_id,title,role,created_by_agent_id,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at
from agents where not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id) order by id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		value, err := scanAgent(rows)
		if err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Agents = append(out.Agents, value)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	agentIndex := make(map[string]int, len(out.Agents))
	for index := range out.Agents {
		agentIndex[out.Agents[index].ID] = index
	}
	assignmentRows, err := s.db.QueryContext(ctx, `select agent_id,worktree_id,position,assignment_mode from agent_worktrees order by agent_id,position`)
	if err != nil {
		return out, err
	}
	for assignmentRows.Next() {
		var agentID string
		var assignment model.AgentWorktree
		if err := assignmentRows.Scan(&agentID, &assignment.WorktreeID, &assignment.Position, &assignment.Mode); err != nil {
			_ = assignmentRows.Close()
			return out, err
		}
		if index, ok := agentIndex[agentID]; ok {
			out.Agents[index].Placement.Worktrees = append(out.Agents[index].Placement.Worktrees, assignment)
		}
	}
	if err := assignmentRows.Close(); err != nil {
		return out, err
	}

	messageRows, err := s.db.QueryContext(ctx, `select id,sender_agent_id,target_agent_id,prompt,status,response,error,runtime_id,created_at,updated_at
from agent_messages where target_agent_id in (
  select id from agents where not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id)
) order by created_at,id`)
	if err != nil {
		return out, err
	}
	for messageRows.Next() {
		value, err := scanAgentMessage(messageRows)
		if err != nil {
			_ = messageRows.Close()
			return out, err
		}
		out.Messages = append(out.Messages, value)
	}
	return out, messageRows.Close()
}

// RestoreDurableState imports a logical checkpoint into a new, empty store.
func (s *Store) RestoreDurableState(ctx context.Context, state model.DurableState) error {
	empty, err := s.Empty(ctx)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("checkpoint restore needs an empty Galpon state directory")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, repository := range state.Repositories {
		if _, err := tx.ExecContext(ctx, `insert into repositories(id,title,source_path,fetch_url,mirror_path,default_remote,push_remote,default_branch,created_at) values(?,?,?,?,?,?,?,?,?)`,
			repository.ID, repository.Title, repository.SourcePath, repository.FetchURL, repository.MirrorPath, repository.DefaultRemote, repository.PushRemote, repository.DefaultBranch, repository.CreatedAt); err != nil {
			return fmt.Errorf("restore repository %s: %w", repository.ID, err)
		}
		for _, remote := range repository.Remotes {
			if _, err := tx.ExecContext(ctx, `insert into repository_remotes(repository_id,name,fetch_url,push_url,created_at) values(?,?,?,?,?)`, repository.ID, remote.Name, remote.FetchURL, remote.PushURL, repository.CreatedAt); err != nil {
				return fmt.Errorf("restore remote %s for repository %s: %w", remote.Name, repository.ID, err)
			}
		}
	}
	for _, workspace := range state.Workspaces {
		if _, err := tx.ExecContext(ctx, `insert into workstreams(id,title,status,renderer,renderer_context,renderer_id,created_at,updated_at) values(?,?,?,?,?,?,?,?)`,
			workspace.ID, workspace.Title, workspace.Status, workspace.Renderer, workspace.RendererContext, workspace.RendererID, workspace.CreatedAt, workspace.UpdatedAt); err != nil {
			return fmt.Errorf("restore workspace %s: %w", workspace.ID, err)
		}
	}
	for _, worktree := range state.Worktrees {
		if err := putWorktree(ctx, tx, worktree); err != nil {
			return fmt.Errorf("restore worktree %s: %w", worktree.ID, err)
		}
	}
	for _, agent := range state.Agents {
		if _, err := tx.ExecContext(ctx, `insert into agents(id,workstream_id,title,role,created_by_agent_id,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			agent.ID, agent.WorkspaceID, agent.Title, agent.Role, agent.CreatedByAgentID, agent.ContextAgentID, agent.Placement.Type, agent.Placement.CWD, agent.Placement.PrimaryWorktreeID, agent.Kind, agent.Status, agent.SessionID, agent.SessionPath, agent.Renderer, agent.RendererContext, agent.RendererID, agent.RuntimeID, agent.LastError, agent.CreatedAt, agent.UpdatedAt); err != nil {
			return fmt.Errorf("restore agent %s: %w", agent.ID, err)
		}
		for _, assignment := range agent.Placement.Worktrees {
			if _, err := tx.ExecContext(ctx, `insert into agent_worktrees(agent_id,worktree_id,position,assignment_mode) values(?,?,?,?)`, agent.ID, assignment.WorktreeID, assignment.Position, assignment.Mode); err != nil {
				return fmt.Errorf("restore placement for agent %s: %w", agent.ID, err)
			}
		}
	}
	for _, message := range state.Messages {
		if _, err := tx.ExecContext(ctx, `insert into agent_messages(id,sender_agent_id,target_agent_id,prompt,status,response,error,runtime_id,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?)`,
			message.ID, message.SenderAgentID, message.TargetAgentID, message.Prompt, message.Status, message.Response, message.Error, message.RuntimeID, message.CreatedAt, message.UpdatedAt); err != nil {
			return fmt.Errorf("restore agent message %s: %w", message.ID, err)
		}
	}
	return tx.Commit()
}

func (s *Store) Empty(ctx context.Context) (bool, error) {
	for _, table := range []string{"repositories", "workstreams", "worktrees", "agents", "agent_messages", "deleted_items"} {
		var count int
		if err := s.db.QueryRowContext(ctx, `select count(*) from `+table).Scan(&count); err != nil {
			return false, err
		}
		if count != 0 {
			return false, nil
		}
	}
	return true, nil
}
