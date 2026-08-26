package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/matipan/galpon/internal/model"
)

const lifecycleEventColumns = `id,event_type,subject_agent_id,recipient_agent_id,message_id,payload,coalesce_key,status,created_at,delivered_at`

func scanLifecycleEvent(row rowScanner) (model.LifecycleEvent, error) {
	var value model.LifecycleEvent
	err := row.Scan(&value.ID, &value.EventType, &value.SubjectAgentID, &value.RecipientAgentID, &value.MessageID, &value.Payload, &value.CoalesceKey, &value.Status, &value.CreatedAt, &value.DeliveredAt)
	return value, err
}

// PutLifecycleEvent adds a general durable event for a normal agent. A stable
// ID gives producers an idempotent transactional outbox boundary. CoalesceKey
// lets a producer replace one pending summary instead of creating an event
// storm.
func (s *Store) PutLifecycleEvent(ctx context.Context, value model.LifecycleEvent) error {
	if value.ID == "" || value.EventType == "" || value.RecipientAgentID == "" {
		return fmt.Errorf("lifecycle event ID, type, and recipient are required")
	}
	if value.Status == "" {
		value.Status = "pending"
	}
	if value.CreatedAt == 0 {
		value.CreatedAt = time.Now().UnixMilli()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if value.CoalesceKey != "" {
		if _, err := tx.ExecContext(ctx, `delete from lifecycle_events where recipient_agent_id=? and coalesce_key=? and status='pending'`, value.RecipientAgentID, value.CoalesceKey); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into lifecycle_events(`+lifecycleEventColumns+`) values(?,?,?,?,?,?,?,?,?,?) on conflict(id) do nothing`, value.ID, value.EventType, value.SubjectAgentID, value.RecipientAgentID, value.MessageID, value.Payload, value.CoalesceKey, value.Status, value.CreatedAt, value.DeliveredAt); err != nil {
		return err
	}
	return tx.Commit()
}

// DispatchLifecycleEvents projects outbox events into the normal agent message
// queue. It is safe to retry after any process failure: result IDs and event
// state change in one transaction.
func (s *Store) DispatchLifecycleEvents(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `select `+lifecycleEventColumns+` from lifecycle_events where status='pending' order by created_at,id limit ?`, limit)
	if err != nil {
		return err
	}
	var events []model.LifecycleEvent
	for rows.Next() {
		value, scanErr := scanLifecycleEvent(rows)
		if scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		events = append(events, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, event := range events {
		message := model.AgentMessage{
			ID: "event:" + event.ID, SenderAgentID: event.SubjectAgentID,
			TargetAgentID: event.RecipientAgentID, Kind: "result", Act: "done", ResultMode: "none", ReplyTo: event.MessageID,
			Prompt: event.Payload, Status: "queued", NotificationState: "pending",
			QueueDeadlineAt: now + agentMessageQueueLifetime.Milliseconds(), CreatedAt: event.CreatedAt, UpdatedAt: now,
		}
		if event.SubjectAgentID != "" {
			_ = tx.QueryRowContext(ctx, `select title from agents where id=?`, event.SubjectAgentID).Scan(&message.SenderTitle)
		}
		if event.MessageID != "" {
			request, readErr := scanAgentMessage(tx.QueryRowContext(ctx, `select `+agentMessageColumns+` from agent_messages where id=?`, event.MessageID))
			if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
				return readErr
			}
			if readErr == nil {
				message.ParentMessageID = request.ID
				message.RootMessageID = request.RootMessageID
				message.RunID = request.RunID
				message.Depth = request.Depth
				if event.EventType == "message.result" {
					message.ID = "result:" + request.ID
					if request.Status == "failed" {
						message.Error = request.Error
						if message.Error == "" {
							message.Error = "delegated request failed"
						}
					}
					if request.NotificationState == "suppressed" {
						message.Status = "completed"
						message.NotificationState = "suppressed"
						message.CompletedAt = now
					}
					_ = tx.QueryRowContext(ctx, `select title from agents where id=?`, request.TargetAgentID).Scan(&message.SenderTitle)
				}
			}
		}
		message = normalizeAgentMessage(message)
		if _, err := tx.ExecContext(ctx, `insert into agent_messages(`+agentMessageColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do nothing`, agentMessageValues(message)...); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update lifecycle_events set status='delivered',delivered_at=? where id=? and status='pending'`, now, event.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneAgentMessageHistory removes complete orchestration runs, never active
// work or work updated in the minimum retention window. The age rule and hard
// row target keep durable message storage bounded under normal operation.
func (s *Store) PruneAgentMessageHistory(ctx context.Context) (int64, error) {
	now := time.Now().UnixMilli()
	oldCutoff := now - agentMessageRetention.Milliseconds()
	recentCutoff := now - agentMessageMinimumRetention.Milliseconds()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `delete from agent_messages where run_id<>'' and run_id in (
  select run_id from agent_messages where run_id<>'' group by run_id
  having max(updated_at)<? and sum(case when status in ('queued','delivered') then 1 else 0 end)=0
  and sum(case when exists(select 1 from agent_operations where parent_message_id=agent_messages.id)
    or exists(select 1 from agent_message_results where message_id=agent_messages.id)
    or exists(select 1 from agent_inbox_receipts where message_id=agent_messages.id)
    or exists(select 1 from agent_operation_joins where message_id=agent_messages.id)
    or exists(select 1 from coordination_message_meta where message_id=agent_messages.id)
    or exists(select 1 from coordination_send_receipts where message_id=agent_messages.id)
    or exists(select 1 from todo_link_intents where message_id=agent_messages.id) then 1 else 0 end)=0
)`, oldCutoff)
	if err != nil {
		return 0, err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	var count int64
	if err := tx.QueryRowContext(ctx, `select count(*) from agent_messages`).Scan(&count); err != nil {
		return 0, err
	}
	if count > agentMessageRetentionLimit {
		excess := count - agentMessageRetentionLimit
		result, err = tx.ExecContext(ctx, `delete from agent_messages where run_id<>'' and run_id in (
  select run_id from agent_messages where run_id<>'' group by run_id
  having max(updated_at)<? and sum(case when status in ('queued','delivered') then 1 else 0 end)=0
  and sum(case when exists(select 1 from agent_operations where parent_message_id=agent_messages.id)
    or exists(select 1 from agent_message_results where message_id=agent_messages.id)
    or exists(select 1 from agent_inbox_receipts where message_id=agent_messages.id)
    or exists(select 1 from agent_operation_joins where message_id=agent_messages.id)
    or exists(select 1 from coordination_message_meta where message_id=agent_messages.id)
    or exists(select 1 from coordination_send_receipts where message_id=agent_messages.id)
    or exists(select 1 from todo_link_intents where message_id=agent_messages.id) then 1 else 0 end)=0
  order by max(updated_at),run_id limit ?
)`, recentCutoff, excess)
		if err != nil {
			return 0, err
		}
		more, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		removed += more
	}
	if _, err := tx.ExecContext(ctx, `delete from lifecycle_events where status='delivered' and delivered_at>0 and delivered_at<?`, oldCutoff); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}
