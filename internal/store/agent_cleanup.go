package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

// AgentDescendants returns all agents created directly or indirectly by creatorID.
// Deleted agents stay in the result until permanent cleanup removes them.
func (s *Store) AgentDescendants(ctx context.Context, creatorID string) ([]model.Agent, error) {
	rows, err := s.db.QueryContext(ctx, `with recursive descendants(id,depth,path) as (
  select id,1,',' || id || ',' from agents where created_by_agent_id=?
  union all
  select agents.id,descendants.depth+1,descendants.path || agents.id || ','
  from agents join descendants on agents.created_by_agent_id=descendants.id
  where instr(descendants.path,',' || agents.id || ',')=0
)
select agents.id,agents.workstream_id,agents.title,agents.role,agents.created_by_agent_id,agents.context_agent_id,agents.placement_kind,agents.placement_cwd,agents.primary_worktree_id,agents.kind,agents.status,agents.session_id,agents.session_path,agents.renderer,agents.renderer_context,agents.renderer_id,agents.runtime_id,agents.last_error,agents.created_at,agents.updated_at
from agents join descendants on descendants.id=agents.id
order by descendants.depth desc,agents.created_at desc,agents.id`, strings.TrimSpace(creatorID))
	if err != nil {
		return nil, err
	}
	var agents []model.Agent
	for rows.Next() {
		value, scanErr := scanAgent(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		agents = append(agents, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range agents {
		agents[index].Placement.Worktrees, err = s.agentWorktrees(ctx, agents[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return agents, nil
}

// SoftDeleteAgents hides the selected agents and returns worktrees that are no
// longer assigned to a visible agent.
func (s *Store) SoftDeleteAgents(ctx context.Context, agentIDs []string) ([]string, error) {
	if len(agentIDs) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	graph, err := loadDeletionGraph(ctx, tx)
	if err != nil {
		return nil, err
	}
	targets := make(map[string]bool, len(agentIDs))
	for _, id := range agentIDs {
		id = strings.TrimSpace(id)
		if _, ok := graph.agents[id]; !ok {
			return nil, fmt.Errorf("agent not found: %s", id)
		}
		targets[id] = true
		graph.deleted["agent"][id] = true
	}
	graph.expand()
	worktreeSet := map[string]bool{}
	for agentID := range targets {
		for _, worktreeID := range graph.agentWorktrees[agentID] {
			if graph.deleted["worktree"][worktreeID] {
				worktreeSet[worktreeID] = true
			}
		}
	}
	now := time.Now().UnixMilli()
	for agentID := range targets {
		if _, err := tx.ExecContext(ctx, `insert into deleted_items(kind,resource_id,deleted_at) values('agent',?,?) on conflict(kind,resource_id) do nothing`, agentID, now); err != nil {
			return nil, err
		}
	}
	for worktreeID := range worktreeSet {
		if _, err := tx.ExecContext(ctx, `insert into deleted_items(kind,resource_id,deleted_at) values('worktree',?,?) on conflict(kind,resource_id) do nothing`, worktreeID, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return sortedDeletionIDs(worktreeSet), nil
}

func (s *Store) WorktreesIncludingDeleted(ctx context.Context, ids []string) ([]model.Worktree, error) {
	values := make([]model.Worktree, 0, len(ids))
	for _, id := range ids {
		row := s.db.QueryRowContext(ctx, `select id,workstream_id,repository_id,path,branch,base_ref,source_remote,lifecycle,created_at from worktrees where id=?`, id)
		var value model.Worktree
		if err := row.Scan(&value.ID, &value.WorkspaceID, &value.RepositoryID, &value.Path, &value.Branch, &value.BaseRef, &value.SourceRemote, &value.Lifecycle, &value.CreatedAt); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (s *Store) PurgeAgentCleanup(ctx context.Context, agentIDs, worktreeIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range agentIDs {
		var deleted int
		if err := tx.QueryRowContext(ctx, `select count(*) from deleted_items where kind='agent' and resource_id=?`, id).Scan(&deleted); err != nil {
			return err
		}
		if deleted == 0 {
			return fmt.Errorf("refuse to purge agent %s before it is deleted", id)
		}
	}
	for _, id := range worktreeIDs {
		var deleted int
		if err := tx.QueryRowContext(ctx, `select count(*) from deleted_items where kind='worktree' and resource_id=?`, id).Scan(&deleted); err != nil {
			return err
		}
		if deleted == 0 {
			return fmt.Errorf("refuse to purge worktree %s before it is deleted", id)
		}
	}
	for _, id := range agentIDs {
		if _, err := tx.ExecContext(ctx, `delete from agent_messages where target_agent_id=? or sender_agent_id=?`, id, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `delete from agent_worktrees where agent_id=?`, id); err != nil {
			return err
		}
	}
	for _, id := range worktreeIDs {
		if _, err := tx.ExecContext(ctx, `delete from agent_worktrees where worktree_id=?`, id); err != nil {
			return err
		}
	}
	for _, id := range agentIDs {
		if _, err := tx.ExecContext(ctx, `delete from agents where id=?`, id); err != nil {
			return err
		}
	}
	for _, id := range worktreeIDs {
		if _, err := tx.ExecContext(ctx, `delete from worktrees where id=?`, id); err != nil {
			return err
		}
	}
	for _, id := range agentIDs {
		if _, err := tx.ExecContext(ctx, `delete from deleted_items where kind='agent' and resource_id=?`, id); err != nil {
			return err
		}
	}
	for _, id := range worktreeIDs {
		if _, err := tx.ExecContext(ctx, `delete from deleted_items where kind='worktree' and resource_id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
