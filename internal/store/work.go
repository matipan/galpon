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
	"unicode"

	"github.com/matipan/galpon/internal/model"
)

const (
	WorkProgressPerMessageLimit = 64
	WorkProgressTotalLimit      = 100_000
	WorkTimelineLimit           = 12
	WorkProjectionMaxRoots      = 128
	WorkProjectionMaxItems      = 256
	WorkProjectionQueryLimit    = 512
	WorkTitleLimit              = 96
	WorkSettledVisibility       = 5 * time.Minute
)

const workProgressColumns = `sequence,message_id,event_id,runtime_id,attempt,version,phase,summary,milestones,blocker,counts,created_at`

var ErrWorkProgressLimit = errors.New("work progress event limit reached")

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
	var active int
	err = tx.QueryRowContext(ctx, `select count(*) from agent_messages where id=? and target_agent_id=? and kind='request' and runtime_id=? and attempt=? and status='delivered' and lease_expires_at>? and processing_deadline_at>?`, value.MessageID, agentID, runtimeID, attempt, now, now).Scan(&active)
	if err != nil {
		return value, false, err
	}
	if active != 1 {
		return value, false, sql.ErrNoRows
	}
	var messageProgressCount, totalProgressCount int
	if err := tx.QueryRowContext(ctx, `select count(*) from work_progress_events where message_id=?`, value.MessageID).Scan(&messageProgressCount); err != nil {
		return value, false, err
	}
	if messageProgressCount >= WorkProgressPerMessageLimit {
		return value, false, fmt.Errorf("%w for this delivery", ErrWorkProgressLimit)
	}
	if err := tx.QueryRowContext(ctx, `select count(*) from work_progress_events`).Scan(&totalProgressCount); err != nil {
		return value, false, err
	}
	if totalProgressCount >= WorkProgressTotalLimit {
		return value, false, fmt.Errorf("%w in total", ErrWorkProgressLimit)
	}
	previousPhase := ""
	if err := tx.QueryRowContext(ctx, `select phase from work_progress_events where message_id=? and attempt=? order by sequence desc limit 1`, value.MessageID, attempt).Scan(&previousPhase); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return value, false, err
	}
	blockerNotificationKey := fmt.Sprintf("work-blocker:%s:%d", value.MessageID, attempt)
	var blockerNotifications int
	if err := tx.QueryRowContext(ctx, `select count(*) from lifecycle_events where event_type='work.blocked' and message_id=? and coalesce_key=?`, value.MessageID, blockerNotificationKey).Scan(&blockerNotifications); err != nil {
		return value, false, err
	}
	milestones, _ := json.Marshal(value.Milestones)
	counts, _ := json.Marshal(value.Counts)
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
	if value.Phase == "blocked" && previousPhase != "blocked" && blockerNotifications == 0 {
		var recipientID, subjectTitle string
		err := tx.QueryRowContext(ctx, `select message.sender_agent_id,agent.title from agent_messages message join agents agent on agent.id=message.target_agent_id where message.id=?`, value.MessageID).Scan(&recipientID, &subjectTitle)
		if err != nil {
			return value, false, err
		}
		if recipientID != "" {
			coalesceKey := blockerNotificationKey
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
		switch message.TerminalReason {
		case "canceled", "expired":
			return message.TerminalReason
		default:
			return "failed"
		}
	default:
		return "failed"
	}
}

func workObservedAt(message model.AgentMessage) int64 {
	switch observedWorkState(message) {
	case "queued":
		if message.UpdatedAt > message.CreatedAt {
			return message.UpdatedAt
		}
		return message.CreatedAt
	case "started":
		if message.ClaimedAt > 0 {
			return message.ClaimedAt
		}
	case "completed", "failed", "canceled", "expired":
		if message.CompletedAt > 0 {
			return message.CompletedAt
		}
	}
	return message.UpdatedAt
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
func boundedWorkTitle(value string) string {
	var out []rune
	for _, r := range strings.TrimSpace(value) {
		if r == '\n' || r == '\r' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		out = append(out, r)
		if len(out) == WorkTitleLimit {
			break
		}
	}
	if title := strings.TrimSpace(string(out)); title != "" {
		return title
	}
	return "Delegated work"
}

// AgentWork returns one bounded projection. The initial queries use the sender,
// status, and run indexes. The causal query keeps result rows only as structural
// parents and renders request rows only.
func (s *Store) AgentWork(ctx context.Context, agentID string, includeSettled bool) (model.WorkProjection, error) {
	projection := model.WorkProjection{Items: []model.WorkItem{}}
	candidateIDs := make([]string, 0, WorkProjectionMaxRoots)
	seenCandidates := make(map[string]bool)
	appendCandidates := func(query string, args ...any) error {
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			if seenCandidates[id] {
				continue
			}
			seenCandidates[id] = true
			if len(candidateIDs) == WorkProjectionMaxRoots {
				projection.Truncated = true
				continue
			}
			candidateIDs = append(candidateIDs, id)
		}
		return rows.Err()
	}
	if err := appendCandidates(`select id from agent_messages where sender_agent_id=? and kind='request' and status in ('queued','delivered') order by updated_at desc,id limit ?`, agentID, WorkProjectionMaxRoots+1); err != nil {
		return projection, err
	}
	remaining := WorkProjectionMaxRoots - len(candidateIDs)
	if remaining > 0 {
		if includeSettled {
			if err := appendCandidates(`select id from agent_messages where sender_agent_id=? and kind='request' and status in ('completed','failed') order by updated_at desc,id limit ?`, agentID, remaining+1); err != nil {
				return projection, err
			}
		} else {
			cutoff := time.Now().Add(-WorkSettledVisibility).UnixMilli()
			if err := appendCandidates(`select root.id from agent_messages root where root.sender_agent_id=? and root.kind='request' and root.status in ('completed','failed') and (root.updated_at>=? or exists (select 1 from agent_messages recent where recent.run_id=root.run_id and recent.updated_at>=?)) order by root.updated_at desc,root.id limit ?`, agentID, cutoff, cutoff, remaining+1); err != nil {
				return projection, err
			}
		}
	} else {
		var omittedSettled int
		if includeSettled {
			err := s.db.QueryRowContext(ctx, `select exists(select 1 from agent_messages where sender_agent_id=? and kind='request' and status in ('completed','failed'))`, agentID).Scan(&omittedSettled)
			if err != nil {
				return projection, err
			}
		} else {
			cutoff := time.Now().Add(-WorkSettledVisibility).UnixMilli()
			err := s.db.QueryRowContext(ctx, `select exists(select 1 from agent_messages root where root.sender_agent_id=? and root.kind='request' and root.status in ('completed','failed') and (root.updated_at>=? or exists (select 1 from agent_messages recent where recent.run_id=root.run_id and recent.updated_at>=?)))`, agentID, cutoff, cutoff).Scan(&omittedSettled)
			if err != nil {
				return projection, err
			}
		}
		projection.Truncated = projection.Truncated || omittedSettled == 1
	}
	if len(candidateIDs) == 0 {
		return projection, nil
	}
	messages := make(map[string]model.AgentMessage)
	order := make([]string, 0, WorkProjectionMaxItems)
	queryIDs := func(column string, ids []string, limit int) ([]model.AgentMessage, error) {
		if len(ids) == 0 || limit <= 0 {
			return nil, nil
		}
		marks := make([]string, len(ids))
		args := make([]any, 0, len(ids)+1)
		for index, id := range ids {
			marks[index] = "?"
			args = append(args, id)
		}
		args = append(args, limit)
		rows, err := s.db.QueryContext(ctx, `select `+agentMessageColumns+` from agent_messages where `+column+` in (`+strings.Join(marks, ",")+`) order by created_at,id limit ?`, args...)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		out := make([]model.AgentMessage, 0, min(limit, len(ids)))
		for rows.Next() {
			message, scanErr := scanAgentMessage(rows)
			if scanErr != nil {
				return nil, scanErr
			}
			out = append(out, message)
		}
		return out, rows.Err()
	}
	frontier := append([]string(nil), candidateIDs...)
	for level := 0; level <= 16 && len(frontier) > 0; level++ {
		loaded, err := queryIDs("id", frontier, WorkProjectionQueryLimit-len(messages)+1)
		if err != nil {
			return projection, err
		}
		next := make([]string, 0, len(loaded))
		for _, message := range loaded {
			if messages[message.ID].ID != "" {
				continue
			}
			if len(messages) == WorkProjectionQueryLimit {
				projection.Truncated = true
				break
			}
			messages[message.ID] = message
			order = append(order, message.ID)
			if message.ParentMessageID != "" && messages[message.ParentMessageID].ID == "" {
				next = append(next, message.ParentMessageID)
			}
		}
		frontier = next
	}
	rootSeeds := make([]string, 0, len(candidateIDs))
	seenRootSeeds := make(map[string]bool)
	for _, candidateID := range candidateIDs {
		message, ok := messages[candidateID]
		if !ok {
			projection.Truncated = true
			continue
		}
		rootID := candidateID
		for parentID := message.ParentMessageID; parentID != ""; {
			parent, found := messages[parentID]
			if !found {
				projection.Truncated = true
				break
			}
			if parent.Kind == "request" && parent.SenderAgentID == agentID {
				rootID = parent.ID
			}
			parentID = parent.ParentMessageID
		}
		if !seenRootSeeds[rootID] {
			seenRootSeeds[rootID] = true
			rootSeeds = append(rootSeeds, rootID)
		}
	}
	frontier = rootSeeds
	for level := 0; level <= 16 && len(frontier) > 0 && len(messages) < WorkProjectionQueryLimit; level++ {
		loaded, err := queryIDs("parent_message_id", frontier, WorkProjectionQueryLimit-len(messages)+1)
		if err != nil {
			return projection, err
		}
		next := make([]string, 0, len(loaded))
		for _, message := range loaded {
			if messages[message.ID].ID != "" {
				continue
			}
			if len(messages) == WorkProjectionQueryLimit {
				projection.Truncated = true
				break
			}
			messages[message.ID] = message
			order = append(order, message.ID)
			next = append(next, message.ID)
		}
		if len(loaded) > len(next) && len(messages) == WorkProjectionQueryLimit {
			projection.Truncated = true
		}
		frontier = next
	}
	slices.SortStableFunc(order, func(left, right string) int {
		leftMessage, rightMessage := messages[left], messages[right]
		if leftMessage.CreatedAt < rightMessage.CreatedAt {
			return -1
		}
		if leftMessage.CreatedAt > rightMessage.CreatedAt {
			return 1
		}
		return strings.Compare(left, right)
	})
	children := make(map[string][]string)
	requestOrder := make([]string, 0, len(order))
	targetIDs := make(map[string]bool)
	for _, id := range order {
		message := messages[id]
		if message.Kind != "request" {
			continue
		}
		requestOrder = append(requestOrder, id)
		targetIDs[message.TargetAgentID] = true
		for parentID := message.ParentMessageID; parentID != ""; {
			parent, ok := messages[parentID]
			if !ok {
				projection.Truncated = true
				break
			}
			if parent.Kind == "request" {
				children[parent.ID] = append(children[parent.ID], id)
				break
			}
			parentID = parent.ParentMessageID
		}
	}
	titles := make(map[string]string, len(targetIDs))
	if len(targetIDs) > 0 {
		ids := make([]string, 0, len(targetIDs))
		titleArgs := make([]any, 0, len(targetIDs))
		for id := range targetIDs {
			ids = append(ids, "?")
			titleArgs = append(titleArgs, id)
		}
		titleRows, titleErr := s.db.QueryContext(ctx, `select id,title from agents where id in (`+strings.Join(ids, ",")+`)`, titleArgs...)
		if titleErr != nil {
			return projection, titleErr
		}
		for titleRows.Next() {
			var id, title string
			if err := titleRows.Scan(&id, &title); err != nil {
				_ = titleRows.Close()
				return projection, err
			}
			titles[id] = boundedWorkTitle(title)
		}
		if err := titleRows.Close(); err != nil {
			return projection, err
		}
	}
	progress := make(map[string][]model.WorkProgressEvent)
	if len(requestOrder) > 0 {
		marks := make([]string, len(requestOrder))
		progressArgs := make([]any, len(requestOrder))
		for index, id := range requestOrder {
			marks[index], progressArgs[index] = "?", id
		}
		progressRows, progressErr := s.db.QueryContext(ctx, `select `+workProgressColumns+` from work_progress_events where message_id in (`+strings.Join(marks, ",")+`) order by sequence`, progressArgs...)
		if progressErr != nil {
			return projection, progressErr
		}
		for progressRows.Next() {
			event, scanErr := scanWorkProgress(progressRows)
			if scanErr != nil {
				_ = progressRows.Close()
				return projection, scanErr
			}
			if event.Attempt == messages[event.MessageID].Attempt {
				progress[event.MessageID] = append(progress[event.MessageID], event)
			}
		}
		if err := progressRows.Close(); err != nil {
			return projection, err
		}
	}
	roots := make([]string, 0, len(candidateIDs))
	for _, id := range requestOrder {
		message := messages[id]
		if message.SenderAgentID != agentID {
			continue
		}
		hasDelegatorAncestor := false
		for parentID := message.ParentMessageID; parentID != ""; {
			parent, ok := messages[parentID]
			if !ok {
				projection.Truncated = true
				break
			}
			if parent.Kind == "request" && parent.SenderAgentID == agentID {
				hasDelegatorAncestor = true
				break
			}
			parentID = parent.ParentMessageID
		}
		if !hasDelegatorAncestor {
			roots = append(roots, id)
		}
	}
	slices.SortStableFunc(roots, func(left, right string) int {
		leftMessage, rightMessage := messages[left], messages[right]
		leftActive := leftMessage.Status == "queued" || leftMessage.Status == "delivered"
		rightActive := rightMessage.Status == "queued" || rightMessage.Status == "delivered"
		if leftActive != rightActive {
			if leftActive {
				return -1
			}
			return 1
		}
		if leftMessage.UpdatedAt > rightMessage.UpdatedAt {
			return -1
		}
		if leftMessage.UpdatedAt < rightMessage.UpdatedAt {
			return 1
		}
		return strings.Compare(left, right)
	})
	now := time.Now().UnixMilli()
	builtItems := 0
	var build func(string, int) (model.WorkItem, bool)
	build = func(id string, projectionDepth int) (model.WorkItem, bool) {
		if builtItems >= WorkProjectionMaxItems {
			projection.Truncated = true
			return model.WorkItem{}, false
		}
		builtItems++
		message := messages[id]
		state := observedWorkState(message)
		observedAt := workObservedAt(message)
		title := titles[message.TargetAgentID]
		if title == "" {
			title = "Delegated work"
		}
		item := model.WorkItem{ID: id, Title: title, TargetAgentID: message.TargetAgentID, TargetTitle: title, Depth: message.Depth, CreatedAt: message.CreatedAt, UpdatedAt: observedAt, CompletedAt: message.CompletedAt,
			Observation: model.WorkObservation{State: state, Source: "observed", ObservedAt: observedAt, Lease: workLease(message, now), Attempt: message.Attempt, ResultMode: message.ResultMode, Act: message.Act, FreshnessAt: message.LeaseExpiresAt},
			Timeline:    []model.WorkTimelineEvent{{Kind: "lifecycle", Label: state, Source: "observed", CreatedAt: observedAt}}}
		events := progress[id]
		if len(events) > 0 {
			latest := events[len(events)-1]
			item.Checkpoint = &model.WorkCheckpoint{Phase: latest.Phase, Summary: latest.Summary, Milestones: latest.Milestones, Blocker: latest.Blocker, Counts: latest.Counts, Source: "reported", ReportedAt: latest.CreatedAt}
			item.Timeline = item.Timeline[:0]
			for _, event := range events {
				item.Timeline = append(item.Timeline, model.WorkTimelineEvent{Kind: "checkpoint", Label: event.Summary, Source: "reported", CreatedAt: event.CreatedAt})
			}
			item.UpdatedAt = max(item.UpdatedAt, latest.CreatedAt)
			item.Timeline = append(item.Timeline, model.WorkTimelineEvent{Kind: "lifecycle", Label: state, Source: "observed", CreatedAt: observedAt})
		}
		if projectionDepth >= 15 && len(children[id]) > 0 {
			projection.Truncated = true
		}
		for childIndex, childID := range children[id] {
			if projectionDepth >= 15 {
				break
			}
			if childIndex == WorkProjectionMaxRoots {
				projection.Truncated = true
				break
			}
			child, ok := build(childID, projectionDepth+1)
			if !ok {
				continue
			}
			item.Children = append(item.Children, child)
			item.Timeline = append(item.Timeline, model.WorkTimelineEvent{Kind: "child", Label: "child delegated", Source: "observed", CreatedAt: child.CreatedAt})
			if !child.Active() {
				item.Timeline = append(item.Timeline, model.WorkTimelineEvent{Kind: "child", Label: "child settled", Source: "observed", CreatedAt: max(child.CompletedAt, child.Observation.ObservedAt)})
			}
		}
		slices.SortStableFunc(item.Timeline, func(left, right model.WorkTimelineEvent) int {
			if left.CreatedAt < right.CreatedAt {
				return -1
			}
			if left.CreatedAt > right.CreatedAt {
				return 1
			}
			return strings.Compare(left.Kind+left.Label, right.Kind+right.Label)
		})
		if len(item.Timeline) > WorkTimelineLimit {
			item.Timeline = item.Timeline[len(item.Timeline)-WorkTimelineLimit:]
		}
		return item, true
	}
	cutoff := now - WorkSettledVisibility.Milliseconds()
	for _, rootID := range roots {
		if len(projection.Items) == WorkProjectionMaxRoots {
			projection.Truncated = true
			break
		}
		item, ok := build(rootID, 0)
		if !ok {
			break
		}
		if includeSettled || workTreeActive(item) || workTreeRecentlyUpdated(item, cutoff) {
			projection.Items = append(projection.Items, item)
		}
	}
	projection.ReturnedRoots = len(projection.Items)
	var countItems func([]model.WorkItem) int
	countItems = func(items []model.WorkItem) int {
		total := 0
		for _, item := range items {
			total += 1 + countItems(item.Children)
		}
		return total
	}
	projection.ReturnedItems = countItems(projection.Items)
	return projection, nil
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
