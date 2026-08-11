package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/matipan/galpon/internal/model"
)

type CleanupPlan struct {
	Repositories    []model.Repository
	Workspaces      []model.Workspace
	Worktrees       []model.Worktree
	Agents          []model.Agent
	AllRepositories []model.Repository
}

type deletionGraph struct {
	repositories   map[string]bool
	workspaces     map[string]bool
	worktrees      map[string]graphWorktree
	agents         map[string]graphAgent
	agentWorktrees map[string][]string
	worktreeAgents map[string][]string
	deleted        map[string]map[string]bool
}

type graphWorktree struct {
	workspaceID  string
	repositoryID string
}

type graphAgent struct {
	workspaceID string
}

func (s *Store) SoftDelete(ctx context.Context, kind, id string) (model.DeletionResult, error) {
	if !validDeletionKind(kind) {
		return model.DeletionResult{}, fmt.Errorf("invalid resource kind %q", kind)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.DeletionResult{}, err
	}
	defer tx.Rollback()
	graph, err := loadDeletionGraph(ctx, tx)
	if err != nil {
		return model.DeletionResult{}, err
	}
	if !graph.exists(kind, id) || graph.deleted[kind][id] {
		return model.DeletionResult{}, sql.ErrNoRows
	}
	before := graph.counts()
	graph.deleted[kind][id] = true
	graph.expand()
	now := time.Now().UnixMilli()
	for _, resourceKind := range []string{"repository", "workspace", "worktree", "agent"} {
		ids := sortedDeletionIDs(graph.deleted[resourceKind])
		for _, resourceID := range ids {
			if _, err := tx.ExecContext(ctx, `insert into deleted_items(kind,resource_id,deleted_at) values(?,?,?) on conflict(kind,resource_id) do nothing`, resourceKind, resourceID, now); err != nil {
				return model.DeletionResult{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return model.DeletionResult{}, err
	}
	after := graph.counts()
	return model.DeletionResult{Kind: kind, ID: id, Hidden: subtractCounts(after, before)}, nil
}

func loadDeletionGraph(ctx context.Context, tx *sql.Tx) (deletionGraph, error) {
	graph := deletionGraph{
		repositories:   map[string]bool{},
		workspaces:     map[string]bool{},
		worktrees:      map[string]graphWorktree{},
		agents:         map[string]graphAgent{},
		agentWorktrees: map[string][]string{},
		worktreeAgents: map[string][]string{},
		deleted: map[string]map[string]bool{
			"repository": {}, "workspace": {}, "worktree": {}, "agent": {},
		},
	}
	rows, err := tx.QueryContext(ctx, `select id from repositories`)
	if err != nil {
		return graph, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return graph, err
		}
		graph.repositories[id] = true
	}
	if err := rows.Close(); err != nil {
		return graph, err
	}
	rows, err = tx.QueryContext(ctx, `select id from workstreams`)
	if err != nil {
		return graph, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return graph, err
		}
		graph.workspaces[id] = true
	}
	if err := rows.Close(); err != nil {
		return graph, err
	}
	rows, err = tx.QueryContext(ctx, `select id,workstream_id,repository_id from worktrees`)
	if err != nil {
		return graph, err
	}
	for rows.Next() {
		var id string
		var value graphWorktree
		if err := rows.Scan(&id, &value.workspaceID, &value.repositoryID); err != nil {
			rows.Close()
			return graph, err
		}
		graph.worktrees[id] = value
	}
	if err := rows.Close(); err != nil {
		return graph, err
	}
	rows, err = tx.QueryContext(ctx, `select id,workstream_id from agents`)
	if err != nil {
		return graph, err
	}
	for rows.Next() {
		var id string
		var value graphAgent
		if err := rows.Scan(&id, &value.workspaceID); err != nil {
			rows.Close()
			return graph, err
		}
		graph.agents[id] = value
	}
	if err := rows.Close(); err != nil {
		return graph, err
	}
	rows, err = tx.QueryContext(ctx, `select agent_id,worktree_id from agent_worktrees`)
	if err != nil {
		return graph, err
	}
	for rows.Next() {
		var agentID, worktreeID string
		if err := rows.Scan(&agentID, &worktreeID); err != nil {
			rows.Close()
			return graph, err
		}
		graph.agentWorktrees[agentID] = append(graph.agentWorktrees[agentID], worktreeID)
		graph.worktreeAgents[worktreeID] = append(graph.worktreeAgents[worktreeID], agentID)
	}
	if err := rows.Close(); err != nil {
		return graph, err
	}
	rows, err = tx.QueryContext(ctx, `select kind,resource_id from deleted_items`)
	if err != nil {
		return graph, err
	}
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			rows.Close()
			return graph, err
		}
		if values, ok := graph.deleted[kind]; ok {
			values[id] = true
		}
	}
	return graph, rows.Close()
}

func (g deletionGraph) exists(kind, id string) bool {
	switch kind {
	case "repository":
		return g.repositories[id]
	case "workspace":
		return g.workspaces[id]
	case "worktree":
		_, ok := g.worktrees[id]
		return ok
	case "agent":
		_, ok := g.agents[id]
		return ok
	default:
		return false
	}
}

func (g deletionGraph) expand() {
	for changed := true; changed; {
		changed = false
		for id, value := range g.worktrees {
			if g.deleted["workspace"][value.workspaceID] || g.deleted["repository"][value.repositoryID] {
				changed = markDeleted(g.deleted["worktree"], id) || changed
			}
		}
		for id, value := range g.agents {
			if g.deleted["workspace"][value.workspaceID] {
				changed = markDeleted(g.deleted["agent"], id) || changed
			}
		}
		for worktreeID := range g.deleted["worktree"] {
			for _, agentID := range g.worktreeAgents[worktreeID] {
				changed = markDeleted(g.deleted["agent"], agentID) || changed
			}
		}
		for agentID := range g.deleted["agent"] {
			for _, worktreeID := range g.agentWorktrees[agentID] {
				allAgentsDeleted := true
				for _, assignedAgentID := range g.worktreeAgents[worktreeID] {
					if !g.deleted["agent"][assignedAgentID] {
						allAgentsDeleted = false
						break
					}
				}
				if allAgentsDeleted {
					changed = markDeleted(g.deleted["worktree"], worktreeID) || changed
				}
			}
		}
	}
}

func markDeleted(values map[string]bool, id string) bool {
	if values[id] {
		return false
	}
	values[id] = true
	return true
}

func (g deletionGraph) counts() model.ResourceCounts {
	return model.ResourceCounts{
		Repositories: len(g.deleted["repository"]),
		Workspaces:   len(g.deleted["workspace"]),
		Worktrees:    len(g.deleted["worktree"]),
		Agents:       len(g.deleted["agent"]),
	}
}

func subtractCounts(after, before model.ResourceCounts) model.ResourceCounts {
	return model.ResourceCounts{
		Repositories: after.Repositories - before.Repositories,
		Workspaces:   after.Workspaces - before.Workspaces,
		Worktrees:    after.Worktrees - before.Worktrees,
		Agents:       after.Agents - before.Agents,
	}
}

func sortedDeletionIDs(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for id := range values {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func validDeletionKind(kind string) bool {
	switch kind {
	case "repository", "workspace", "worktree", "agent":
		return true
	default:
		return false
	}
}

func (s *Store) IsDeleted(ctx context.Context, kind, id string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from deleted_items where kind=? and resource_id=?`, kind, id).Scan(&count)
	return count != 0, err
}

func (s *Store) DeletedCleanupPlan(ctx context.Context) (CleanupPlan, error) {
	plan := CleanupPlan{}
	repositoryRows, err := s.db.QueryContext(ctx, `select id,title,source_path,fetch_url,mirror_path,default_remote,push_remote,default_branch,created_at from repositories order by id`)
	if err != nil {
		return plan, err
	}
	for repositoryRows.Next() {
		var value model.Repository
		if err := repositoryRows.Scan(&value.ID, &value.Title, &value.SourcePath, &value.FetchURL, &value.MirrorPath, &value.DefaultRemote, &value.PushRemote, &value.DefaultBranch, &value.CreatedAt); err != nil {
			repositoryRows.Close()
			return plan, err
		}
		plan.AllRepositories = append(plan.AllRepositories, value)
	}
	if err := repositoryRows.Close(); err != nil {
		return plan, err
	}
	remoteRows, err := s.db.QueryContext(ctx, `select repository_id,name,fetch_url,push_url from repository_remotes order by repository_id,name`)
	if err != nil {
		return plan, err
	}
	repositoryIndex := map[string]int{}
	for index := range plan.AllRepositories {
		repositoryIndex[plan.AllRepositories[index].ID] = index
	}
	for remoteRows.Next() {
		var repositoryID string
		var remote model.RepositoryRemote
		if err := remoteRows.Scan(&repositoryID, &remote.Name, &remote.FetchURL, &remote.PushURL); err != nil {
			remoteRows.Close()
			return plan, err
		}
		if index, ok := repositoryIndex[repositoryID]; ok {
			plan.AllRepositories[index].Remotes = append(plan.AllRepositories[index].Remotes, remote)
		}
	}
	if err := remoteRows.Close(); err != nil {
		return plan, err
	}
	deletedRepositories := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `select resource_id from deleted_items where kind='repository'`)
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return plan, err
		}
		deletedRepositories[id] = true
	}
	if err := rows.Close(); err != nil {
		return plan, err
	}
	for _, repository := range plan.AllRepositories {
		if deletedRepositories[repository.ID] {
			plan.Repositories = append(plan.Repositories, repository)
		}
	}
	rows, err = s.db.QueryContext(ctx, `select id,title,status,renderer,renderer_context,renderer_id,created_at,updated_at from workstreams where exists (select 1 from deleted_items where kind='workspace' and resource_id=workstreams.id) order by id`)
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		var value model.Workspace
		if err := rows.Scan(&value.ID, &value.Title, &value.Status, &value.Renderer, &value.RendererContext, &value.RendererID, &value.CreatedAt, &value.UpdatedAt); err != nil {
			rows.Close()
			return plan, err
		}
		plan.Workspaces = append(plan.Workspaces, value)
	}
	if err := rows.Close(); err != nil {
		return plan, err
	}
	rows, err = s.db.QueryContext(ctx, `select id,workstream_id,repository_id,path,branch,base_ref,source_remote,created_at from worktrees where exists (select 1 from deleted_items where kind='worktree' and resource_id=worktrees.id) order by id`)
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		var value model.Worktree
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.RepositoryID, &value.Path, &value.Branch, &value.BaseRef, &value.SourceRemote, &value.CreatedAt); err != nil {
			rows.Close()
			return plan, err
		}
		plan.Worktrees = append(plan.Worktrees, value)
	}
	if err := rows.Close(); err != nil {
		return plan, err
	}
	rows, err = s.db.QueryContext(ctx, `select id,workstream_id,title,role,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at from agents where exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id) order by id`)
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		value, err := scanAgent(rows)
		if err != nil {
			rows.Close()
			return plan, err
		}
		plan.Agents = append(plan.Agents, value)
	}
	return plan, rows.Close()
}

func (s *Store) PurgeDeleted(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`delete from agent_messages where target_agent_id in (select resource_id from deleted_items where kind='agent') or sender_agent_id in (select resource_id from deleted_items where kind='agent')`,
		`delete from agent_worktrees where agent_id in (select resource_id from deleted_items where kind='agent') or worktree_id in (select resource_id from deleted_items where kind='worktree')`,
		`delete from agents where id in (select resource_id from deleted_items where kind='agent')`,
		`delete from worktrees where id in (select resource_id from deleted_items where kind='worktree')`,
		`delete from workstreams where id in (select resource_id from deleted_items where kind='workspace')`,
		`delete from repository_remotes where repository_id in (select resource_id from deleted_items where kind='repository')`,
		`delete from repositories where id in (select resource_id from deleted_items where kind='repository')`,
		`delete from deleted_items`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}
