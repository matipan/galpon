package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

const (
	WorkProgressPerMessageLimit = 64
	WorkProgressTotalLimit      = 100_000
	WorkTimelineLimit           = 12
	WorkSettledVisibility       = 5 * time.Minute
)

const workProgressColumns = `sequence,message_id,event_id,runtime_id,attempt,version,phase,summary,milestones,blocker,counts,created_at`

func scanWorkProgress(row rowScanner) (model.WorkProgressEvent, error) {
	var value model.WorkProgressEvent
	var milestones, counts string
	err := row.Scan(&value.Sequence, &value.MessageID, &value.EventID, &value.RuntimeID, &value.Attempt, &value.Version, &value.Phase, &value.Summary, &milestones, &value.Blocker, &counts, &value.CreatedAt)
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal([]byte(milestones), &value.Milestones); err != nil {
		return value, fmt.Errorf("decode work milestones: %w", err)
	}
	if err := json.Unmarshal([]byte(counts), &value.Counts); err != nil {
		return value, fmt.Errorf("decode work counts: %w", err)
	}
	return value, nil
}

// PutWorkProgress commits one idempotent, attempt-fenced report. The active
// delivery check and insert share one transaction, so a stale runtime cannot
// update a message after another attempt claims it.
func (s *Store) PutWorkProgress(ctx context.Context, agentID, runtimeID string, attempt int, value model.WorkProgressEvent) (model.WorkProgressEvent, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return value, false, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	var active int
	err = tx.QueryRowContext(ctx, `select count(*) from agent_messages where id=? and target_agent_id=? and runtime_id=? and attempt=? and status='delivered' and lease_expires_at>? and processing_deadline_at>?`, value.MessageID, agentID, runtimeID, attempt, now, now).Scan(&active)
	if err != nil {
		return value, false, err
	}
	if active != 1 {
		return value, false, sql.ErrNoRows
	}
	milestones, _ := json.Marshal(value.Milestones)
	counts, _ := json.Marshal(value.Counts)
	existing, lookupErr := scanWorkProgress(tx.QueryRowContext(ctx, `select `+workProgressColumns+` from work_progress_events where message_id=? and event_id=?`, value.MessageID, value.EventID))
	if lookupErr == nil {
		if existing.RuntimeID != runtimeID || existing.Attempt != attempt || existing.Version != value.Version || existing.Phase != value.Phase || existing.Summary != value.Summary || existing.Blocker != value.Blocker || !slices.Equal(existing.Milestones, value.Milestones) || !slices.Equal(existing.Counts, value.Counts) {
			return value, false, fmt.Errorf("progress event ID was already used for another report")
		}
		return existing, false, nil
	}
	if !errors.Is(lookupErr, sql.ErrNoRows) {
		return value, false, lookupErr
	}
	value.RuntimeID = runtimeID
	value.Attempt = attempt
	value.CreatedAt = now
	result, err := tx.ExecContext(ctx, `insert into work_progress_events(message_id,event_id,runtime_id,attempt,version,phase,summary,milestones,blocker,counts,created_at) values(?,?,?,?,?,?,?,?,?,?,?)`, value.MessageID, value.EventID, runtimeID, attempt, value.Version, value.Phase, value.Summary, string(milestones), value.Blocker, string(counts), now)
	if err != nil {
		return value, false, err
	}
	value.Sequence, err = result.LastInsertId()
	if err != nil {
		return value, false, err
	}
	if _, err := tx.ExecContext(ctx, `delete from work_progress_events where sequence in (select sequence from work_progress_events where message_id=? order by sequence desc limit -1 offset ?)`, value.MessageID, WorkProgressPerMessageLimit); err != nil {
		return value, false, err
	}
	if _, err := tx.ExecContext(ctx, `delete from work_progress_events where sequence in (select sequence from work_progress_events order by sequence desc limit -1 offset ?)`, WorkProgressTotalLimit); err != nil {
		return value, false, err
	}
	if value.Phase == "blocked" {
		var recipientID, subjectTitle string
		err := tx.QueryRowContext(ctx, `select message.sender_agent_id,agent.title from agent_messages message join agents agent on agent.id=message.target_agent_id where message.id=?`, value.MessageID).Scan(&recipientID, &subjectTitle)
		if err != nil {
			return value, false, err
		}
		if recipientID != "" {
			coalesceKey := "work-blocker:" + value.MessageID
			if _, err := tx.ExecContext(ctx, `delete from lifecycle_events where recipient_agent_id=? and coalesce_key=? and status='pending'`, recipientID, coalesceKey); err != nil {
				return value, false, err
			}
			payload := fmt.Sprintf("Blocked delegated work reported by %s:\n\n%s", subjectTitle, value.Blocker)
			if _, err := tx.ExecContext(ctx, `insert into lifecycle_events(id,event_type,subject_agent_id,recipient_agent_id,message_id,payload,coalesce_key,status,created_at,delivered_at) values(?,?,?,?,?,?,?,'pending',?,0) on conflict(id) do nothing`, "work-blocker:"+value.MessageID+":"+value.EventID, "work.blocked", agentID, recipientID, value.MessageID, payload, coalesceKey, now); err != nil {
				return value, false, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into companion_events(event_type,created_at) values('invalidate',?)`, now); err != nil {
		return value, false, err
	}
	if err := tx.Commit(); err != nil {
		return value, false, err
	}
	return value, true, nil
}

func (s *Store) WorkProgressEvents(ctx context.Context, messageID string) ([]model.WorkProgressEvent, error) {
	rows, err := s.db.QueryContext(ctx, `select `+workProgressColumns+` from work_progress_events where message_id=? order by sequence`, messageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.WorkProgressEvent
	for rows.Next() {
		value, err := scanWorkProgress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func observedWorkState(message model.AgentMessage) string {
	switch message.Status {
	case "queued":
		return "queued"
	case "delivered":
		return "started"
	case "completed":
		return "completed"
	case "failed":
		failure := strings.ToLower(message.Error + " " + message.LastError)
		if strings.Contains(failure, "cancel") {
			return "canceled"
		}
		if strings.Contains(failure, "expired") || strings.Contains(failure, "deadline") || strings.Contains(failure, "attempts") {
			return "expired"
		}
		return "failed"
	default:
		return "failed"
	}
}

func workLease(message model.AgentMessage, now int64) string {
	if message.Status != "delivered" || message.LeaseExpiresAt == 0 {
		return "none"
	}
	if message.LeaseExpiresAt <= now {
		return "stale"
	}
	return "fresh"
}

// AgentWork projects request deliveries delegated by one agent. It never uses
// message prompts or runtime/session identifiers.
func (s *Store) AgentWork(ctx context.Context, agentID string, includeSettled bool) ([]model.WorkItem, error) {
	dashboard, err := s.Dashboard(ctx)
	if err != nil {
		return nil, err
	}
	titles := make(map[string]string, len(dashboard.Agents))
	for _, agent := range dashboard.Agents {
		titles[agent.ID] = agent.Title
	}
	rows, err := s.db.QueryContext(ctx, `select `+agentMessageColumns+` from agent_messages where kind='request' order by created_at,id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	messages := make(map[string]model.AgentMessage)
	children := make(map[string][]string)
	var order []string
	for rows.Next() {
		message, err := scanAgentMessage(rows)
		if err != nil {
			return nil, err
		}
		messages[message.ID] = message
		order = append(order, message.ID)
		children[message.ParentMessageID] = append(children[message.ParentMessageID], message.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var roots []string
	for _, id := range order {
		message := messages[id]
		if message.SenderAgentID != agentID {
			continue
		}
		hasDelegatorAncestor := false
		for parentID := message.ParentMessageID; parentID != ""; {
			parent, ok := messages[parentID]
			if !ok {
				break
			}
			if parent.SenderAgentID == agentID {
				hasDelegatorAncestor = true
				break
			}
			parentID = parent.ParentMessageID
		}
		if !hasDelegatorAncestor {
			roots = append(roots, message.ID)
		}
	}
	progress := make(map[string][]model.WorkProgressEvent)
	progressRows, err := s.db.QueryContext(ctx, `select `+workProgressColumns+` from work_progress_events where message_id in (select id from agent_messages where run_id in (select run_id from agent_messages where sender_agent_id=? and kind='request')) order by sequence`, agentID)
	if err != nil {
		return nil, err
	}
	for progressRows.Next() {
		event, scanErr := scanWorkProgress(progressRows)
		if scanErr != nil {
			_ = progressRows.Close()
			return nil, scanErr
		}
		progress[event.MessageID] = append(progress[event.MessageID], event)
	}
	if err := progressRows.Close(); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	var build func(string) model.WorkItem
	build = func(id string) model.WorkItem {
		message := messages[id]
		state := observedWorkState(message)
		item := model.WorkItem{
			ID: id, Title: titles[message.TargetAgentID], TargetTitle: titles[message.TargetAgentID], Depth: message.Depth,
			CreatedAt: message.CreatedAt, UpdatedAt: message.UpdatedAt, CompletedAt: message.CompletedAt,
			Observation: model.WorkObservation{State: state, Source: "observed", ObservedAt: message.UpdatedAt, Lease: workLease(message, now), Attempt: message.Attempt, ResultMode: message.ResultMode, Act: message.Act, FreshnessAt: message.LeaseExpiresAt},
			Timeline:    []model.WorkTimelineEvent{{Kind: "lifecycle", Label: state, Source: "observed", CreatedAt: message.UpdatedAt}},
		}
		if item.Title == "" {
			item.Title = "Delegated work"
			item.TargetTitle = item.Title
		}
		events := progress[id]
		if len(events) > 0 {
			latest := events[len(events)-1]
			item.Checkpoint = &model.WorkCheckpoint{Phase: latest.Phase, Summary: latest.Summary, Milestones: latest.Milestones, Blocker: latest.Blocker, Counts: latest.Counts, Source: "reported", ReportedAt: latest.CreatedAt}
			start := max(0, len(events)-(WorkTimelineLimit-1))
			item.Timeline = item.Timeline[:0]
			for _, event := range events[start:] {
				item.Timeline = append(item.Timeline, model.WorkTimelineEvent{Kind: "checkpoint", Label: event.Summary, Source: "reported", CreatedAt: event.CreatedAt})
			}
			item.Timeline = append(item.Timeline, model.WorkTimelineEvent{Kind: "lifecycle", Label: state, Source: "observed", CreatedAt: message.UpdatedAt})
		}
		for _, childID := range children[id] {
			item.Children = append(item.Children, build(childID))
		}
		return item
	}
	out := make([]model.WorkItem, 0, len(roots))
	for _, root := range roots {
		item := build(root)
		if includeSettled || workTreeActive(item) || workTreeRecentlyUpdated(item, now-WorkSettledVisibility.Milliseconds()) {
			out = append(out, item)
		}
	}
	return out, nil
}

func workTreeRecentlyUpdated(item model.WorkItem, cutoff int64) bool {
	if item.UpdatedAt >= cutoff {
		return true
	}
	for _, child := range item.Children {
		if workTreeRecentlyUpdated(child, cutoff) {
			return true
		}
	}
	return false
}

func workTreeActive(item model.WorkItem) bool {
	if item.Active() {
		return true
	}
	for _, child := range item.Children {
		if workTreeActive(child) {
			return true
		}
	}
	return false
}
