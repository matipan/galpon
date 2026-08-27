package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/matipan/galpon/internal/model"
)

type CommunicationCutoverOptions struct {
	Generation           int
	MaintenanceConfirmed bool
	BackupVerified       bool
	SafeIdleConfirmed    bool
	KnownTodoLinks       []model.AgentTodoLinkIntent
}

type CommunicationCutoverResult struct {
	Generation int
	Messages   int
	Operations int
	Results    int
	Receipts   int
	Joins      int
	TodoLinks  int
}

func protocolStateDetails(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (generation, pendingGeneration int, complete, maintenance bool, err error) {
	var completeValue, maintenanceValue int
	err = query.QueryRowContext(ctx, `select generation,pending_generation,cutover_complete,maintenance from communication_protocol_state where singleton=1`).Scan(&generation, &pendingGeneration, &completeValue, &maintenanceValue)
	return generation, pendingGeneration, completeValue != 0, maintenanceValue != 0, err
}

func protocolState(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (generation int, complete, maintenance bool, err error) {
	generation, _, complete, maintenance, err = protocolStateDetails(ctx, query)
	return
}

func requireRuntimeGeneration(ctx context.Context, tx *sql.Tx, agentID, runtimeID string, generation int) error {
	current, complete, maintenance, err := protocolState(ctx, tx)
	if err != nil {
		return err
	}
	if maintenance {
		return fmt.Errorf("communication protocol is in maintenance mode")
	}
	if !complete {
		return nil
	}
	if generation == 0 {
		generation = current
	}
	if generation != current {
		return fmt.Errorf("communication protocol generation %d is stale; current generation is %d", generation, current)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `select count(*) from agent_runtime_protocol_generations where agent_id=? and runtime_id=? and generation=?`, agentID, runtimeID, current).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("runtime is not registered for communication protocol generation %d", current)
	}
	return nil
}

func requireObjectGeneration(ctx context.Context, tx *sql.Tx, generation int) (int, error) {
	current, complete, maintenance, err := protocolState(ctx, tx)
	if err != nil {
		return 0, err
	}
	if maintenance {
		return 0, fmt.Errorf("communication protocol is in maintenance mode")
	}
	if !complete {
		if generation == 0 {
			return current, nil
		}
		return generation, nil
	}
	if generation != current {
		return 0, fmt.Errorf("communication protocol generation %d is stale; current generation is %d", generation, current)
	}
	return current, nil
}

func (s *Store) CommunicationProtocolState(ctx context.Context) (generation int, complete, maintenance bool, err error) {
	return protocolState(ctx, s.db)
}

// BeginCommunicationCutover durably enters maintenance before the caller
// validates idle state, creates a backup, or performs semantic backfill.
func (s *Store) BeginCommunicationCutover(ctx context.Context, generation int) error {
	if generation <= 1 {
		return fmt.Errorf("new protocol generation is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, pending, complete, maintenance, err := protocolStateDetails(ctx, tx)
	if err != nil {
		return err
	}
	if complete {
		return fmt.Errorf("communication protocol generation %d is already active", current)
	}
	if maintenance {
		if pending != generation {
			return fmt.Errorf("communication cutover generation %d is already in maintenance", pending)
		}
		return tx.Commit()
	}
	if generation <= current {
		return fmt.Errorf("new protocol generation must be greater than %d", current)
	}
	if _, err := tx.ExecContext(ctx, `update communication_protocol_state set maintenance=1,pending_generation=?,updated_at=? where singleton=1 and cutover_complete=0 and maintenance=0`, generation, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RegisterAgentProtocolGeneration(ctx context.Context, agentID, runtimeID string, generation int) error {
	if agentID == "" || runtimeID == "" || generation <= 0 {
		return fmt.Errorf("agent, runtime, and protocol generation are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, complete, _, err := protocolState(ctx, tx)
	if err != nil {
		return err
	}
	if !complete || generation != current {
		return fmt.Errorf("communication protocol generation %d is not active", generation)
	}
	var recovered int
	if err := tx.QueryRowContext(ctx, `select count(*) from communication_runtime_recoveries where agent_id=? and runtime_id=?`, agentID, runtimeID).Scan(&recovered); err != nil {
		return err
	}
	if recovered != 0 {
		return fmt.Errorf("runtime identity was recovered and cannot be reused")
	}
	var count int
	if err := tx.QueryRowContext(ctx, `select count(*) from agents where id=? and runtime_id=?`, agentID, runtimeID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	_, err = tx.ExecContext(ctx, `insert into agent_runtime_protocol_generations(agent_id,runtime_id,generation,registered_at) values(?,?,?,?) on conflict(agent_id,runtime_id) do update set generation=excluded.generation,registered_at=excluded.registered_at`, agentID, runtimeID, generation, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteCommunicationCutover(ctx context.Context, generation int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, complete, maintenance, err := protocolState(ctx, tx)
	if err != nil {
		return err
	}
	if !complete || current != generation {
		return fmt.Errorf("communication protocol generation %d is not ready", generation)
	}
	if !maintenance {
		return tx.Commit()
	}
	var missing int
	if err := tx.QueryRowContext(ctx, `select count(*) from agents where runtime_id<>'' and not exists (select 1 from agent_runtime_protocol_generations registration where registration.agent_id=agents.id and registration.runtime_id=agents.runtime_id and registration.generation=?)`, generation).Scan(&missing); err != nil {
		return err
	}
	if missing != 0 {
		return fmt.Errorf("%d running agent runtimes have not registered protocol generation %d", missing, generation)
	}
	if _, err := tx.ExecContext(ctx, `update communication_protocol_state set maintenance=0,pending_generation=0,recovery_pending=1,updated_at=? where singleton=1 and generation=?`, time.Now().UnixMilli(), generation); err != nil {
		return err
	}
	return tx.Commit()
}

// BackfillCommunicationV2 performs the semantic v1 to v2 conversion only
// during an explicitly confirmed coordinated cutover. Normal Open never calls
// this method.
func (s *Store) BackfillCommunicationV2(ctx context.Context, options CommunicationCutoverOptions) (CommunicationCutoverResult, error) {
	out := CommunicationCutoverResult{Generation: options.Generation}
	if options.Generation <= 1 || !options.MaintenanceConfirmed || !options.BackupVerified || !options.SafeIdleConfirmed {
		return out, fmt.Errorf("coordinated cutover needs a new generation, maintenance mode, a verified backup, and a safe idle point")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	current, pending, complete, maintenance, err := protocolStateDetails(ctx, tx)
	if err != nil {
		return out, err
	}
	if complete {
		if current == options.Generation {
			return communicationCutoverCounts(ctx, tx, options.Generation)
		}
		return out, fmt.Errorf("communication protocol generation %d is already active", current)
	}
	if !maintenance || pending != options.Generation {
		return out, fmt.Errorf("communication cutover generation %d has not entered durable maintenance", options.Generation)
	}
	if options.Generation <= current {
		return out, fmt.Errorf("new protocol generation must be greater than %d", current)
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `update communication_protocol_state set maintenance_writer='backfill',updated_at=? where singleton=1 and maintenance=1 and pending_generation=? and maintenance_writer=''`, now, options.Generation); err != nil {
		return out, err
	}
	for _, intent := range options.KnownTodoLinks {
		if intent.ID == "" {
			intent.ID = "todo:" + intent.MessageID
		}
		if intent.Policy == "" {
			intent.Policy = "complete_on_success"
		}
		if intent.State == "" {
			intent.State = "pending"
		}
		if intent.CreatedAt == 0 {
			intent.CreatedAt = now
		}
		if intent.MessageID == "" || intent.TodoID <= 0 || (intent.Policy != "complete_on_success" && intent.Policy != "annotate") {
			return out, fmt.Errorf("known TODO link has invalid fields")
		}
		if _, err := tx.ExecContext(ctx, `insert into todo_link_intents(id,message_id,operation_id,todo_id,policy,state,created_at,protocol_generation) values(?,?,null,?,?,?, ?,?) on conflict(message_id) do nothing`, intent.ID, intent.MessageID, intent.TodoID, intent.Policy, intent.State, intent.CreatedAt, options.Generation); err != nil {
			return out, err
		}
	}
	rows, err := tx.QueryContext(ctx, `select `+agentMessageColumns+` from agent_messages where kind='request' order by created_at,id`)
	if err != nil {
		return out, err
	}
	var messages []model.AgentMessage
	for rows.Next() {
		value, err := scanAgentMessage(rows)
		if err != nil {
			_ = rows.Close()
			return out, err
		}
		messages = append(messages, value)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	operationForMessage := make(map[string]string)
	for index := range messages {
		message := &messages[index]
		if message.RootMessageID == "" {
			message.RootMessageID = message.ID
		}
		if message.RunID == "" {
			message.RunID = message.RootMessageID
		}
		if message.Status != "queued" && message.Status != "delivered" {
			continue
		}
		operationID := "legacy-operation:" + message.ID
		operationForMessage[message.ID] = operationID
		kind, parentMessageID := "inbound", message.ID
		if message.SenderAgentID == "" {
			kind, parentMessageID = "direct", ""
		}
		deadline := message.ProcessingDeadlineAt
		if deadline == 0 {
			deadline = message.QueueDeadlineAt
		}
		if _, err := tx.ExecContext(ctx, `insert or ignore into agent_operations(`+operationColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, operationID, message.TargetAgentID, kind, "ready", parentMessageID, message.RunID, "", "", "", 0, 0, 0, deadline, "", "", message.CreatedAt, now, 0, "", options.Generation); err != nil {
			return out, err
		}
		eligible := 1
		if err := tx.QueryRowContext(ctx, `select case when exists(select 1 from todo_link_intents where message_id=? and state<>'applied') then 0 else 1 end`, message.ID).Scan(&eligible); err != nil {
			return out, err
		}
		if _, err := tx.ExecContext(ctx, `insert or ignore into agent_inbox_receipts(id,agent_id,operation_id,message_id,kind,state,eligible,created_at,updated_at,protocol_generation) values(?,?,?,?,?,'pending',?,?,?,?)`, "request:"+message.ID, message.TargetAgentID, operationID, message.ID, "request", eligible, message.CreatedAt, now, options.Generation); err != nil {
			return out, err
		}
		if message.Status == "delivered" && message.Attempt > 0 {
			started := message.ClaimedAt
			if started == 0 {
				started = message.UpdatedAt
			}
			if _, err := tx.ExecContext(ctx, `insert or ignore into agent_operation_attempts(id,operation_id,attempt,runtime_id,claim_key,state,started_at,updated_at,finished_at,terminal_reason) values(?,?,?,?,?,'recovered',?,?,?,'cutover')`, fmt.Sprintf("%s:%d", operationID, message.Attempt), operationID, message.Attempt, message.RuntimeID, message.ClaimKey, started, now, now); err != nil {
				return out, err
			}
		}
	}
	for _, message := range messages {
		images, err := loadMessageImages(ctx, tx, message.ID, true)
		if err != nil {
			return out, err
		}
		message.Images = imagePointer(images)
		joinDeadline := int64(0)
		if message.ResultMode == "join" {
			joinDeadline = message.ProcessingDeadlineAt
			if joinDeadline == 0 {
				joinDeadline = message.QueueDeadlineAt
			}
		}
		var todoID int64
		var todoPolicy string
		err = tx.QueryRowContext(ctx, `select todo_id,policy from todo_link_intents where message_id=?`, message.ID).Scan(&todoID, &todoPolicy)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		hash, err := coordinationRequestHash(message, operationForMessage[message.ParentMessageID], todoID, todoPolicy, joinDeadline)
		if err != nil {
			return out, err
		}
		if _, err := tx.ExecContext(ctx, `insert or ignore into coordination_message_meta(message_id,source_operation_id,request_hash,created_at) values(?,?,?,?)`, message.ID, operationForMessage[message.ParentMessageID], hash, message.CreatedAt); err != nil {
			return out, err
		}
		if message.IdempotencyKey != "" {
			if _, err := tx.ExecContext(ctx, `insert or ignore into coordination_send_receipts(sender_agent_id,idempotency_key,request_hash,message_id,created_at) values(?,?,?,?,?)`, message.SenderAgentID, message.IdempotencyKey, hash, message.ID, message.CreatedAt); err != nil {
				return out, err
			}
		}
		if message.Status != "completed" && message.Status != "failed" {
			continue
		}
		status := "completed"
		if message.Status == "failed" {
			status = message.TerminalReason
			if status != "canceled" && status != "expired" {
				status = "failed"
			}
		}
		legacy := ""
		if message.NotificationState == "suppressed" {
			legacy = "legacy_suppressed_unknown"
		}
		created := message.CompletedAt
		if created == 0 {
			created = message.UpdatedAt
		}
		resultID := "result:" + message.ID
		if _, err := tx.ExecContext(ctx, `insert or ignore into agent_message_results(id,message_id,status,response,error,terminal_reason,legacy_state,created_at,protocol_generation) values(?,?,?,?,?,?,?,?,?)`, resultID, message.ID, status, message.Response, message.Error, message.TerminalReason, legacy, created, options.Generation); err != nil {
			return out, err
		}
		if err := createTodoSettlementEvents(ctx, tx, model.AgentMessageResult{ID: resultID, MessageID: message.ID, Status: status, Response: message.Response, Error: message.Error, TerminalReason: message.TerminalReason, LegacyState: legacy, CreatedAt: created, ProtocolGeneration: options.Generation}, created); err != nil {
			return out, err
		}
		if message.SenderAgentID == "" || message.Act == "inform" || message.ResultMode == "none" || legacy != "" {
			continue
		}
		if message.ResultMode == "join" {
			if parentOperation := operationForMessage[message.ParentMessageID]; parentOperation != "" {
				joinState, kind := "ready", "result"
				if status != "completed" {
					joinState, kind = "failed", "blocker"
				}
				joinID := "join:" + parentOperation + ":" + message.ID
				if _, err := tx.ExecContext(ctx, `insert or ignore into agent_operation_joins(id,operation_id,message_id,state,deadline_at,created_at,updated_at,resolved_at,protocol_generation) values(?,?,?,?,?,?,?,?,?)`, joinID, parentOperation, message.ID, joinState, message.ProcessingDeadlineAt, message.CreatedAt, now, now, options.Generation); err != nil {
					return out, err
				}
				if _, err := tx.ExecContext(ctx, `insert or ignore into agent_inbox_receipts(id,agent_id,operation_id,message_id,result_id,kind,state,eligible,created_at,updated_at,protocol_generation) select ?,agent_id,?,?,?,?, 'pending',1,?,?,? from agent_operations where id=?`, "join-receipt:"+joinID, parentOperation, message.ID, resultID, kind, created, now, options.Generation, parentOperation); err != nil {
					return out, err
				}
			} else {
				if _, err := tx.ExecContext(ctx, `insert or ignore into agent_inbox_receipts(id,agent_id,message_id,result_id,kind,state,eligible,created_at,updated_at,protocol_generation) values(?,?,?,?, 'result','pending',1,?,?,?)`, "legacy-late-result:"+message.ID, message.SenderAgentID, message.ID, resultID, created, now, options.Generation); err != nil {
					return out, err
				}
			}
		} else if message.NotificationState == "pending" || message.NotificationState == "delivered" {
			if _, err := tx.ExecContext(ctx, `insert or ignore into agent_inbox_receipts(id,agent_id,message_id,result_id,kind,state,eligible,created_at,updated_at,protocol_generation) values(?,?,?,?, 'result','pending',1,?,?,?)`, "result-receipt:"+message.ID, message.SenderAgentID, message.ID, resultID, created, now, options.Generation); err != nil {
				return out, err
			}
		}
	}
	// Backfill open joins after all message operations exist.
	for _, message := range messages {
		if message.ResultMode != "join" || (message.Status != "queued" && message.Status != "delivered") {
			continue
		}
		parentOperation := operationForMessage[message.ParentMessageID]
		if parentOperation == "" {
			continue
		}
		deadline := message.ProcessingDeadlineAt
		if deadline == 0 {
			deadline = message.QueueDeadlineAt
		}
		if deadline == 0 {
			return out, fmt.Errorf("legacy joined message %s has no deadline", message.ID)
		}
		if _, err := tx.ExecContext(ctx, `insert or ignore into agent_operation_joins(id,operation_id,message_id,state,deadline_at,created_at,updated_at,protocol_generation) values(?,?,?,'open',?,?,?,?)`, "join:"+parentOperation+":"+message.ID, parentOperation, message.ID, deadline, message.CreatedAt, now, options.Generation); err != nil {
			return out, err
		}
	}
	if err := rejectStoredOperationCycles(ctx, tx); err != nil {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, `update communication_protocol_state set generation=?,cutover_complete=1,maintenance=1,pending_generation=?,maintenance_writer='',updated_at=? where singleton=1 and cutover_complete=0 and maintenance=1 and pending_generation=? and maintenance_writer='backfill'`, options.Generation, options.Generation, now, options.Generation); err != nil {
		return out, err
	}
	out, err = communicationCutoverCounts(ctx, tx, options.Generation)
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

func communicationCutoverCounts(ctx context.Context, tx *sql.Tx, generation int) (CommunicationCutoverResult, error) {
	out := CommunicationCutoverResult{Generation: generation}
	queries := []struct {
		query string
		out   *int
	}{
		{`select count(*) from coordination_message_meta`, &out.Messages},
		{`select count(*) from agent_operations where protocol_generation=?`, &out.Operations},
		{`select count(*) from agent_message_results where protocol_generation=?`, &out.Results},
		{`select count(*) from agent_inbox_receipts where protocol_generation=?`, &out.Receipts},
		{`select count(*) from agent_operation_joins where protocol_generation=?`, &out.Joins},
		{`select count(*) from todo_link_intents where protocol_generation=?`, &out.TodoLinks},
	}
	for _, item := range queries {
		args := []any{generation}
		if item.query == `select count(*) from coordination_message_meta` {
			args = nil
		}
		if err := tx.QueryRowContext(ctx, item.query, args...).Scan(item.out); err != nil {
			return out, err
		}
	}
	return out, nil
}

func rejectStoredOperationCycles(ctx context.Context, tx *sql.Tx) error {
	var count int
	err := tx.QueryRowContext(ctx, `with recursive edges(source_id,target_id) as (
  select dependency.operation_id,target.id
  from agent_operation_joins dependency
  join agent_operations target on target.parent_message_id=dependency.message_id
  where dependency.state in ('open','ready')
), reach(source_id,target_id) as (
  select source_id,target_id from edges
  union
  select reach.source_id,edges.target_id from reach join edges on edges.source_id=reach.target_id
)
select count(*) from reach where source_id=target_id`).Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("coordination operation dependency cycle detected")
	}
	return nil
}
