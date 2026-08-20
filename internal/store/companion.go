package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

type CompanionMutation struct {
	Key          string
	Operation    string
	RequestHash  string
	StatusCode   int
	ResponseJSON []byte
	CreatedAt    int64
}

// PutConversationEvents authenticates the active runtime and inserts each Pi
// event at most once. One durable invalidation is emitted for each non-empty
// inserted batch.
func (s *Store) PutConversationEvents(ctx context.Context, agentID, runtimeID string, events []model.ConversationEvent) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var workspaceID, activeRuntime string
	if err := tx.QueryRowContext(ctx, `select workstream_id,runtime_id from agents where id=? and not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id)`, agentID).Scan(&workspaceID, &activeRuntime); err != nil {
		return 0, err
	}
	if activeRuntime == "" || activeRuntime != runtimeID {
		return 0, errors.New("pi runtime is not registered for this agent")
	}
	inserted := 0
	for _, event := range events {
		result, err := tx.ExecContext(ctx, `insert or ignore into conversation_events(agent_id,event_id,runtime_id,runtime_seq,kind,pi_entry_id,role,content,tool_name,tool_call_id,is_delta,is_error,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			agentID, event.EventID, runtimeID, event.RuntimeSeq, event.Kind, event.PiEntryID, event.Role, event.Content, event.ToolName, event.ToolCallID, event.IsDelta, event.IsError, event.CreatedAt)
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if count == 1 {
			if err := putConversationImages(ctx, tx, agentID, event.EventID, event.Images, event.CreatedAt); err != nil {
				return 0, err
			}
		}
		inserted += int(count)
	}
	if inserted > 0 {
		if _, err := appendCompanionEvent(ctx, tx, "invalidate", agentID, workspaceID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (s *Store) ConversationEvents(ctx context.Context, agentID string) ([]model.ConversationEvent, error) {
	rows, err := s.db.QueryContext(ctx, `select sequence,agent_id,event_id,runtime_seq,kind,pi_entry_id,role,content,tool_name,tool_call_id,is_delta,is_error,created_at from conversation_events where agent_id=? order by sequence`, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events, err := scanConversationEvents(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.hydrateConversationImages(ctx, events); err != nil {
		return nil, err
	}
	return events, nil
}

// ConversationEventsPage returns at most limit newest visible discussion events
// before the opaque sequence boundary, in normal ascending stream order. Private
// model reasoning is not part of the Companion discussion.
func (s *Store) ConversationEventsPage(ctx context.Context, agentID string, before int64, limit int) ([]model.ConversationEvent, bool, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query := `select sequence,agent_id,event_id,runtime_seq,kind,pi_entry_id,role,content,tool_name,tool_call_id,is_delta,is_error,created_at from conversation_events where agent_id=? and kind not in ('assistant_reasoning_start','assistant_reasoning_delta','assistant_reasoning_end')`
	args := []any{agentID}
	if before > 0 {
		query += ` and sequence<?`
		args = append(args, before)
	}
	query += ` order by sequence desc limit ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	events, err := scanConversationEvents(rows)
	if err != nil {
		return nil, false, err
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	slices.Reverse(events)
	if err := s.hydrateConversationImages(ctx, events); err != nil {
		return nil, false, err
	}
	return events, hasMore, nil
}

func (s *Store) ConversationDeliveryPromptSequences(ctx context.Context, agentID string, messageIDs []string) (map[string]int64, error) {
	sequences := make(map[string]int64, len(messageIDs))
	wanted := make(map[string]bool, len(messageIDs))
	for _, messageID := range messageIDs {
		if messageID != "" {
			wanted[messageID] = true
		}
	}
	if len(wanted) == 0 {
		return sequences, nil
	}
	rows, err := s.db.QueryContext(ctx, `select sequence,content from conversation_events where agent_id=? and kind='user_message' order by sequence`, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sequence int64
		var content string
		if err := rows.Scan(&sequence, &content); err != nil {
			return nil, err
		}
		const prefix = "[delivery "
		for offset := 0; offset < len(content); {
			start := strings.Index(content[offset:], prefix)
			if start < 0 {
				break
			}
			start += offset
			rest := content[start+len(prefix):]
			end := strings.IndexByte(rest, ']')
			if end <= 0 {
				break
			}
			messageID := rest[:end]
			if wanted[messageID] {
				sequences[messageID] = sequence
			}
			offset = start + len(prefix) + end + 1
		}
	}
	return sequences, rows.Err()
}

func (s *Store) ConversationAssistantEndSequences(ctx context.Context, agentID, content string, afterSequence, notBefore, notAfter int64) ([]int64, error) {
	content = strings.TrimSpace(content)
	rows, err := s.db.QueryContext(ctx, `select sequence,content from conversation_events where agent_id=? and kind='assistant_message_end' and sequence>? and created_at between ? and ? order by sequence`, agentID, afterSequence, notBefore, notAfter)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	sequences := make([]int64, 0)
	for rows.Next() {
		var sequence int64
		var candidate string
		if err := rows.Scan(&sequence, &candidate); err != nil {
			return nil, err
		}
		candidate = strings.TrimSpace(candidate)
		if candidate == content || len(content) >= 32768 && len(candidate) >= 32768 && candidate[:32768] == content[:32768] {
			sequences = append(sequences, sequence)
		}
	}
	return sequences, rows.Err()
}

type conversationRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanConversationEvents(rows conversationRows) ([]model.ConversationEvent, error) {
	out := []model.ConversationEvent{}
	for rows.Next() {
		var event model.ConversationEvent
		if err := rows.Scan(&event.Sequence, &event.AgentID, &event.EventID, &event.RuntimeSeq, &event.Kind, &event.PiEntryID, &event.Role, &event.Content, &event.ToolName, &event.ToolCallID, &event.IsDelta, &event.IsError, &event.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) AppendCompanionEvent(ctx context.Context, eventType, agentID, workspaceID string) (model.CompanionEvent, error) {
	return appendCompanionEvent(ctx, s.db, eventType, agentID, workspaceID)
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func appendCompanionEvent(ctx context.Context, exec sqlExecer, eventType, agentID, workspaceID string) (model.CompanionEvent, error) {
	createdAt := time.Now().UnixMilli()
	result, err := exec.ExecContext(ctx, `insert into companion_events(event_type,agent_id,workspace_id,created_at) values(?,?,?,?)`, eventType, agentID, workspaceID, createdAt)
	if err != nil {
		return model.CompanionEvent{}, err
	}
	sequence, err := result.LastInsertId()
	return model.CompanionEvent{Sequence: sequence, Type: eventType, AgentID: agentID, WorkspaceID: workspaceID, CreatedAt: createdAt}, err
}

func (s *Store) CompanionEventRange(ctx context.Context) (int64, int64, error) {
	var minimum, maximum int64
	err := s.db.QueryRowContext(ctx, `select coalesce(min(sequence),0),coalesce(max(sequence),0) from companion_events`).Scan(&minimum, &maximum)
	return minimum, maximum, err
}

func (s *Store) CompanionEventsAfter(ctx context.Context, after int64, limit int) ([]model.CompanionEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `select sequence,event_type,agent_id,workspace_id,created_at from companion_events where sequence>? order by sequence limit ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []model.CompanionEvent{}
	for rows.Next() {
		var event model.CompanionEvent
		if err := rows.Scan(&event.Sequence, &event.Type, &event.AgentID, &event.WorkspaceID, &event.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) CompanionDashboard(ctx context.Context) (model.Dashboard, error) {
	out := model.Dashboard{Repositories: []model.Repository{}, Workspaces: []model.Workspace{}, Agents: []model.Agent{}}
	rows, err := s.db.QueryContext(ctx, `select id,title from repositories where not exists (select 1 from deleted_items where kind='repository' and resource_id=repositories.id) order by title,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var value model.Repository
		if err := rows.Scan(&value.ID, &value.Title); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Repositories = append(out.Repositories, value)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `select id,title,status,renderer,renderer_context,renderer_id,created_at,updated_at from workstreams where status='active' and not exists (select 1 from deleted_items where kind='workspace' and resource_id=workstreams.id) order by updated_at desc,id`)
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
	rows, err = s.db.QueryContext(ctx, `select id,workstream_id,title,role,created_by_agent_id,presentation,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at from agents where not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id) order by updated_at desc,id`)
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
	assignmentRows, err := s.db.QueryContext(ctx, `select agent_id,worktree_id,position,assignment_mode from agent_worktrees order by agent_id,position`)
	if err != nil {
		return out, err
	}
	agentIndex := make(map[string]int, len(out.Agents))
	for index := range out.Agents {
		agentIndex[out.Agents[index].ID] = index
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
	return out, assignmentRows.Close()
}

func (s *Store) WorkspaceTitle(ctx context.Context, workspaceID string) (string, error) {
	var title string
	err := s.db.QueryRowContext(ctx, `select title from workstreams where id=? and not exists (select 1 from deleted_items where kind='workspace' and resource_id=workstreams.id)`, workspaceID).Scan(&title)
	return title, err
}

func (s *Store) CompanionSequence(ctx context.Context) (int64, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `select coalesce(max(sequence),0) from companion_events`).Scan(&sequence)
	return sequence, err
}

func (s *Store) CompanionMutation(ctx context.Context, key string) (*CompanionMutation, error) {
	var value CompanionMutation
	err := s.db.QueryRowContext(ctx, `select idempotency_key,operation,request_hash,status_code,response_json,created_at from companion_mutations where idempotency_key=?`, key).Scan(
		&value.Key, &value.Operation, &value.RequestHash, &value.StatusCode, &value.ResponseJSON, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &value, err
}

// ReserveCompanionMutation durably admits a command before it has side
// effects. A status code of zero means that a prior process did not finish the
// command. Callers must fail that retry safely instead of running it again.
func (s *Store) ReserveCompanionMutation(ctx context.Context, key, operation, requestHash string) (*CompanionMutation, bool, error) {
	result, err := s.db.ExecContext(ctx, `insert or ignore into companion_mutations(idempotency_key,operation,request_hash,status_code,response_json,created_at) values(?,?,?,0,?,?)`,
		key, operation, requestHash, []byte(`{}`), time.Now().UnixMilli())
	if err != nil {
		return nil, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	value, err := s.CompanionMutation(ctx, key)
	return value, count == 1, err
}

func (s *Store) CompleteCompanionMutation(ctx context.Context, key string, statusCode int, responseJSON []byte) error {
	result, err := s.db.ExecContext(ctx, `update companion_mutations set status_code=?,response_json=? where idempotency_key=? and status_code=0`, statusCode, responseJSON, key)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("companion mutation is not pending")
	}
	return nil
}
