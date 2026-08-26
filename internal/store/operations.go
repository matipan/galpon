package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

const (
	OperationsMaxAgents        = 128
	OperationsMaxRoots         = 64
	OperationsMaxItems         = 256
	OperationsMaxTimeline      = 128
	OperationsMaxActivityFacts = 64
	OperationsSourceScanLimit  = 512
	operationsMessageLimit     = 1024
)

const operationsMessageColumns = `message.id,message.sender_agent_id,message.target_agent_id,message.kind,message.act,message.result_mode,message.reply_to,message.parent_message_id,message.root_message_id,message.run_id,message.depth,message.status,message.notification_state,message.terminal_reason,message.runtime_id,message.attempt,message.claimed_at,message.lease_expires_at,message.processing_deadline_at,message.completed_at,message.created_at,message.updated_at`

type operationsMessage struct {
	ID, SenderAgentID, TargetAgentID, Kind, Act, ResultMode string
	ReplyTo, ParentMessageID, RootMessageID, RunID          string
	Depth                                                   int
	Status, NotificationState, TerminalReason, RuntimeID    string
	Attempt                                                 int
	ClaimedAt, LeaseExpiresAt, ProcessingDeadlineAt         int64
	CompletedAt, CreatedAt, UpdatedAt                       int64
}

type operationsResultRow struct {
	Status, NotificationState string
	ClaimedAt, LeaseExpiresAt int64
	CompletedAt, UpdatedAt    int64
}

type operationsRoot struct {
	item model.WorkItem
	rank int
}

func scanOperationsMessage(row rowScanner) (operationsMessage, error) {
	var value operationsMessage
	err := row.Scan(&value.ID, &value.SenderAgentID, &value.TargetAgentID, &value.Kind, &value.Act, &value.ResultMode,
		&value.ReplyTo, &value.ParentMessageID, &value.RootMessageID, &value.RunID, &value.Depth, &value.Status,
		&value.NotificationState, &value.TerminalReason, &value.RuntimeID, &value.Attempt, &value.ClaimedAt,
		&value.LeaseExpiresAt, &value.ProcessingDeadlineAt, &value.CompletedAt, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func operationsFresh(message operationsMessage, now int64) bool {
	return message.Status == "delivered" && message.LeaseExpiresAt > now && (message.ProcessingDeadlineAt == 0 || message.ProcessingDeadlineAt > now)
}

func operationsObservedState(message operationsMessage) string {
	switch message.Status {
	case "queued":
		return "queued"
	case "delivered":
		return "started"
	case "completed":
		return "completed"
	case "failed":
		if message.TerminalReason == "canceled" || message.TerminalReason == "expired" {
			return message.TerminalReason
		}
		return "failed"
	default:
		return "failed"
	}
}

func operationsObservedAt(message operationsMessage) int64 {
	switch operationsObservedState(message) {
	case "queued":
		return max(message.CreatedAt, message.UpdatedAt)
	case "started":
		return max(message.ClaimedAt, message.CreatedAt)
	default:
		return max(message.CompletedAt, message.UpdatedAt)
	}
}

func operationsLease(message operationsMessage, now int64) string {
	if message.Status != "delivered" || message.LeaseExpiresAt == 0 {
		return "none"
	}
	if !operationsFresh(message, now) {
		return "stale"
	}
	return "fresh"
}

// WorkspaceOperations builds one bounded projection in one read transaction.
// It reads only fields needed for the public projection and captures one time
// value for every lease and recency decision.
func (s *Store) WorkspaceOperations(ctx context.Context, workspaceID string) (model.WorkspaceOperations, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	now := time.Now().UnixMilli()
	cutoff := now - WorkSettledVisibility.Milliseconds()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return model.WorkspaceOperations{}, err
	}
	defer func() { _ = tx.Rollback() }()

	v2Projection, err := communicationV2ProjectionEnabled(ctx, tx)
	if err != nil {
		return model.WorkspaceOperations{}, err
	}
	out := model.WorkspaceOperations{
		Version: 1, Agents: []model.OperationsAgent{}, Work: []model.WorkItem{}, Timeline: []model.OperationsTimelineFact{},
		Truncation: model.OperationsTruncation{
			MaxAgents: OperationsMaxAgents, MaxRoots: OperationsMaxRoots, MaxItems: OperationsMaxItems, MaxTimeline: OperationsMaxTimeline,
			AgentsOmissionExact: true, RootsOmissionExact: true, ItemsOmissionExact: true, TimelineOmissionExact: true,
		},
	}
	var workspaceTitle string
	if err := tx.QueryRowContext(ctx, `select title from workstreams where id=? and not exists (select 1 from deleted_items where kind='workspace' and resource_id=workstreams.id)`, workspaceID).Scan(&workspaceTitle); err != nil {
		if err == sql.ErrNoRows {
			return model.WorkspaceOperations{}, fmt.Errorf("workspace not found")
		}
		return model.WorkspaceOperations{}, err
	}
	out.Workspace = model.OperationsWorkspace{ID: workspaceID, Title: boundedWorkspaceTitle(workspaceTitle)}

	if err := tx.QueryRowContext(ctx, `select count(*),coalesce(sum(case when status in ('running','starting') then 1 else 0 end),0) from agents where workstream_id=? and not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id)`, workspaceID).Scan(&out.Summary.Agents, &out.Summary.ActiveAgents); err != nil {
		return model.WorkspaceOperations{}, err
	}
	agentRows, err := tx.QueryContext(ctx, `select id,title,role,status,presentation,updated_at from agents where workstream_id=? and not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id) order by case status when 'running' then 0 when 'starting' then 1 when 'failed' then 2 when 'idle' then 3 else 4 end,updated_at desc,lower(title),id limit ?`, workspaceID, OperationsMaxAgents)
	if err != nil {
		return model.WorkspaceOperations{}, err
	}
	for agentRows.Next() {
		var value model.OperationsAgent
		if err := agentRows.Scan(&value.ID, &value.Title, &value.Role, &value.Status, &value.Presentation, &value.UpdatedAt); err != nil {
			_ = agentRows.Close()
			return model.WorkspaceOperations{}, err
		}
		value.Title = boundedWorkTitle(value.Title)
		if strings.TrimSpace(value.Role) != "" {
			value.Role = boundedWorkTitle(value.Role)
		} else {
			value.Role = ""
		}
		out.Agents = append(out.Agents, value)
	}
	if err := agentRows.Close(); err != nil {
		return model.WorkspaceOperations{}, err
	}
	out.Truncation.AgentsOmitted = max(0, out.Summary.Agents-len(out.Agents))

	var operationWaiting, operationResumes int
	if v2Projection {
		out.Version = 2
		err = scanOperationsQueueV2(ctx, tx, workspaceID, now, &out.Queue)
		if err == nil {
			out.DirectOperations, err = loadDirectOperationFacts(ctx, tx, "workspace", workspaceID, now, cutoff)
		}
		if err == nil {
			err = scanWorkspaceOperationCounts(ctx, tx, workspaceID, &operationWaiting, &operationResumes)
		}
	} else {
		err = scanOperationsQueue(ctx, tx, workspaceID, now, &out.Queue)
	}
	if err != nil {
		return model.WorkspaceOperations{}, err
	}

	candidateRows, err := tx.QueryContext(ctx, `select `+operationsMessageColumns+`
		from agent_messages message
		where message.kind='request'
		and exists (select 1 from agent_messages seed join agents initiator on initiator.id=seed.sender_agent_id where seed.run_id=message.run_id and seed.kind='request' and initiator.workstream_id=? and not exists (select 1 from deleted_items where kind='agent' and resource_id=initiator.id))
		and not exists (select 1 from deleted_items where kind='agent' and resource_id=message.target_agent_id)
		and (message.status in ('queued','delivered') or message.updated_at>=?
			or ?=1 and exists (select 1 from agent_inbox_receipts receipt where receipt.message_id=message.id and receipt.kind in ('result','blocker') and receipt.state in ('pending','claimed','presented'))
			or ?=1 and exists (select 1 from agent_message_results result where result.message_id=message.id and result.legacy_state='legacy_suppressed_unknown'))
		order by case
			when ?=1 and exists (select 1 from agent_inbox_receipts receipt where receipt.message_id=message.id and receipt.kind in ('result','blocker') and receipt.state in ('pending','claimed','presented')) then 0
			when ?=1 and exists (select 1 from agent_message_results result where result.message_id=message.id and result.legacy_state='legacy_suppressed_unknown') then 1
			when message.status='delivered' and message.lease_expires_at>? and (message.processing_deadline_at=0 or message.processing_deadline_at>?) and exists (
				select 1 from work_progress_events progress where progress.message_id=message.id and progress.attempt=message.attempt and progress.blocker<>''
				and progress.sequence=(select max(latest.sequence) from work_progress_events latest where latest.message_id=message.id and latest.attempt=message.attempt)
			) then 0
			when message.status='delivered' and message.lease_expires_at>? and (message.processing_deadline_at=0 or message.processing_deadline_at>?) then 1
			when message.status='queued' then 2
			when message.status='delivered' then 3
			when message.status='failed' then 4
			else 5 end,
			message.updated_at desc,message.id
		limit ?`, workspaceID, cutoff, v2Projection, v2Projection, v2Projection, v2Projection, now, now, now, now, OperationsSourceScanLimit+1)
	if err != nil {
		return model.WorkspaceOperations{}, err
	}
	messages := make(map[string]operationsMessage)
	candidateIDs := make([]string, 0, OperationsSourceScanLimit)
	for candidateRows.Next() {
		message, scanErr := scanOperationsMessage(candidateRows)
		if scanErr != nil {
			_ = candidateRows.Close()
			return model.WorkspaceOperations{}, scanErr
		}
		if len(candidateIDs) == OperationsSourceScanLimit {
			out.Truncation.SourceTruncated = true
			continue
		}
		messages[message.ID] = message
		candidateIDs = append(candidateIDs, message.ID)
	}
	if err := candidateRows.Close(); err != nil {
		return model.WorkspaceOperations{}, err
	}

	frontier := make([]string, 0)
	for _, id := range candidateIDs {
		if parent := messages[id].ParentMessageID; parent != "" {
			frontier = append(frontier, parent)
		}
	}
	for depth := 0; depth < 16 && len(frontier) > 0; depth++ {
		frontier = uniqueMissingOperationsIDs(frontier, messages)
		if len(frontier) == 0 {
			break
		}
		remaining := operationsMessageLimit - len(messages)
		if remaining <= 0 {
			out.Truncation.SourceTruncated = true
			break
		}
		if len(frontier) > remaining {
			frontier = frontier[:remaining]
			out.Truncation.SourceTruncated = true
		}
		loaded, loadErr := queryOperationsMessages(ctx, tx, frontier)
		if loadErr != nil {
			return model.WorkspaceOperations{}, loadErr
		}
		next := make([]string, 0, len(loaded))
		for _, message := range loaded {
			messages[message.ID] = message
			if message.ParentMessageID != "" {
				next = append(next, message.ParentMessageID)
			}
		}
		frontier = next
	}
	if len(frontier) > 0 {
		out.Truncation.SourceTruncated = true
	}

	titles, err := operationsAgentTitles(ctx, tx, messages)
	if err != nil {
		return model.WorkspaceOperations{}, err
	}
	latestProgress, err := operationsLatestProgress(ctx, tx, messages)
	if err != nil {
		return model.WorkspaceOperations{}, err
	}
	results, lifecycle, err := operationsResultRows(ctx, tx, messages)
	if err != nil {
		return model.WorkspaceOperations{}, err
	}
	communication := map[string]*workCommunicationSource{}
	if v2Projection {
		ids := make([]string, 0, len(messages))
		for id, message := range messages {
			if message.Kind == "request" {
				ids = append(ids, id)
			}
		}
		communication, err = loadWorkCommunication(ctx, tx, ids)
		if err != nil {
			return model.WorkspaceOperations{}, err
		}
	}

	candidateSet := make(map[string]bool, len(candidateIDs))
	for _, id := range candidateIDs {
		candidateSet[id] = true
	}
	requestIDs := make([]string, 0, len(messages))
	children := make(map[string][]string)
	for id, message := range messages {
		if message.Kind != "request" || (!candidateSet[id] && !operationsAncestorOfCandidate(id, candidateIDs, messages)) {
			continue
		}
		requestIDs = append(requestIDs, id)
		if parent := nearestOperationsRequestParent(message.ParentMessageID, messages); parent != "" {
			children[parent] = append(children[parent], id)
		}
	}
	for parent := range children {
		slices.Sort(children[parent])
	}
	slices.Sort(requestIDs)
	roots := make([]string, 0)
	for _, id := range requestIDs {
		if nearestOperationsRequestParent(messages[id].ParentMessageID, messages) == "" {
			roots = append(roots, id)
		}
	}

	var build func(string, int) model.WorkItem
	build = func(id string, depth int) model.WorkItem {
		message := messages[id]
		state := operationsObservedState(message)
		lease := operationsLease(message, now)
		observedAt := operationsObservedAt(message)
		item := model.WorkItem{
			ID: id, Title: titleOrDelegated(titles[message.TargetAgentID]), TargetAgentID: message.TargetAgentID,
			TargetTitle: titleOrDelegated(titles[message.TargetAgentID]), DelegatorTitle: titleOrDelegated(titles[message.SenderAgentID]),
			Depth: message.Depth, CreatedAt: message.CreatedAt, UpdatedAt: observedAt, CompletedAt: message.CompletedAt,
			Observation: model.WorkObservation{State: state, Source: "observed", ObservedAt: observedAt, Lease: lease, Attempt: message.Attempt, ResultMode: message.ResultMode, Act: message.Act, FreshnessAt: message.LeaseExpiresAt},
			Timeline:    []model.WorkTimelineEvent{{Kind: "lifecycle", Label: state, Source: "observed", CreatedAt: observedAt}}, Children: []model.WorkItem{},
		}
		if v2Projection {
			source := communication[id]
			item.Coordination = buildWorkCoordination(message.Status, message.UpdatedAt, source)
			if source != nil {
				item.Observation = v2WorkObservation(message.Status, message.TerminalReason, observedAt, source.targetOperation, now)
				state, lease, observedAt = item.Observation.State, item.Observation.Lease, item.Observation.ObservedAt
				item.UpdatedAt = max(item.UpdatedAt, observedAt)
				item.Timeline[0] = model.WorkTimelineEvent{Kind: "lifecycle", Label: state, Source: "observed", CreatedAt: observedAt}
			}
		}
		if state == "started" && !v2Projection {
			item.Observation.LeaseObservedAt = message.UpdatedAt
			item.UpdatedAt = max(item.UpdatedAt, message.UpdatedAt)
		}
		if progress, ok := latestProgress[id]; ok {
			item.Timeline = append(item.Timeline, model.WorkTimelineEvent{Kind: "checkpoint", Label: progress.Summary, Source: "reported", CreatedAt: progress.CreatedAt})
			item.UpdatedAt = max(item.UpdatedAt, progress.CreatedAt)
			currentAttempt := message.Attempt
			current := operationsFresh(message, now)
			if v2Projection && communication[id] != nil && communication[id].targetOperation != nil {
				operation := communication[id].targetOperation
				currentAttempt = operation.Attempt
				current = (operation.State == "claimed" || operation.State == "running") && operation.LeaseExpiresAt > now
			}
			if current && progress.Attempt == currentAttempt {
				item.Checkpoint = checkpointFromProgress(progress)
			}
		}
		result := operationsResultFact(message, results[id], lifecycle[id], now)
		if v2Projection {
			result = v2OperationsResultFact(item.Coordination)
		}
		if result != nil {
			item.Result = result
			item.Timeline = append(item.Timeline, model.WorkTimelineEvent{Kind: "result", Label: result.Label, Source: "observed", CreatedAt: result.ObservedAt})
			item.UpdatedAt = max(item.UpdatedAt, result.ObservedAt)
		}
		if depth < 16 {
			for _, childID := range children[id] {
				item.Children = append(item.Children, build(childID, depth+1))
			}
		} else if len(children[id]) > 0 {
			out.Truncation.SourceTruncated = true
		}
		slices.SortStableFunc(item.Timeline, func(left, right model.WorkTimelineEvent) int {
			if left.CreatedAt != right.CreatedAt {
				return int(left.CreatedAt - right.CreatedAt)
			}
			return strings.Compare(left.Kind+left.Label, right.Kind+right.Label)
		})
		return item
	}

	unique := make([]operationsRoot, 0, len(roots))
	for _, id := range roots {
		item := build(id, 0)
		unique = append(unique, operationsRoot{item: item, rank: classifyOperationsTree(&item)})
	}
	slices.SortStableFunc(unique, func(left, right operationsRoot) int {
		if left.rank != right.rank {
			return left.rank - right.rank
		}
		if left.item.UpdatedAt != right.item.UpdatedAt {
			if left.item.UpdatedAt > right.item.UpdatedAt {
				return -1
			}
			return 1
		}
		return strings.Compare(left.item.ID, right.item.ID)
	})

	totalItems := 0
	for _, root := range unique {
		totalItems += countOperationsItems(root.item)
		collectOperationsSummary(root.item, &out.Summary)
	}
	if v2Projection {
		out.Summary.WaitingWork = operationWaiting
		out.Summary.ResumeQueued = operationResumes
	}
	out.Summary.WorkCountsExact = !out.Truncation.SourceTruncated
	rootLimit := min(len(unique), OperationsMaxRoots)
	remaining := OperationsMaxItems
	for index := 0; index < rootLimit && remaining > 0; index++ {
		item, used := copyBoundedOperationsTree(unique[index].item, &remaining)
		if used {
			out.Work = append(out.Work, item)
		}
	}
	returnedItems := 0
	for _, root := range out.Work {
		returnedItems += countOperationsItems(root)
	}
	out.Truncation.RootsOmitted = max(0, len(unique)-len(out.Work))
	out.Truncation.ItemsOmitted = max(0, totalItems-returnedItems)
	if out.Truncation.SourceTruncated {
		out.Truncation.RootsOmissionExact = false
		out.Truncation.ItemsOmissionExact = false
	}

	current := make(map[string]model.OperationsDelivery)
	observed := make(map[string]model.OperationsDelivery)
	currentIDs := make(map[string]string)
	for _, root := range unique {
		collectCurrentDeliveries(root.item, current, currentIDs)
		collectObservedDeliveries(root.item, observed)
	}
	for index := range out.Agents {
		if delivery, found := current[out.Agents[index].ID]; found {
			value := delivery
			out.Agents[index].CurrentDelivery = &value
		}
		if delivery, found := observed[out.Agents[index].ID]; found {
			value := delivery
			out.Agents[index].ObservedDelivery = &value
		}
	}
	activityLane, err := operationsActivityLane(ctx, tx, currentIDs, v2Projection)
	if err != nil {
		return model.WorkspaceOperations{}, err
	}
	if out.Truncation.SourceTruncated {
		if activityLane == nil {
			activityLane = &model.OperationsActivityLane{Version: 1, Facts: []model.OperationsActivityFact{}, Truncation: model.OperationsLaneTruncation{MaxFacts: OperationsMaxActivityFacts}}
		}
		activityLane.Truncation.Truncated = true
		activityLane.Truncation.OmissionExact = false
	}
	out.Activity = activityLane

	facts := make([]model.OperationsTimelineFact, 0)
	for _, root := range unique {
		collectOperationsTimeline(root.item, &facts)
	}
	slices.SortStableFunc(facts, func(left, right model.OperationsTimelineFact) int {
		if left.CreatedAt != right.CreatedAt {
			if left.CreatedAt > right.CreatedAt {
				return -1
			}
			return 1
		}
		return strings.Compare(left.WorkID+left.Kind+left.Label, right.WorkID+right.Kind+right.Label)
	})
	if len(facts) > OperationsMaxTimeline {
		out.Truncation.TimelineOmitted = len(facts) - OperationsMaxTimeline
		facts = facts[:OperationsMaxTimeline]
	}
	if out.Truncation.SourceTruncated {
		out.Truncation.TimelineOmissionExact = false
	}
	out.Timeline = facts
	out.Truncation.Truncated = out.Truncation.AgentsOmitted > 0 || out.Truncation.RootsOmitted > 0 || out.Truncation.ItemsOmitted > 0 || out.Truncation.TimelineOmitted > 0 || out.Truncation.SourceTruncated || (out.Activity != nil && out.Activity.Truncation.Truncated)
	if err := tx.Commit(); err != nil {
		return model.WorkspaceOperations{}, err
	}
	return out, nil
}

func scanOperationsQueueV2(ctx context.Context, tx *sql.Tx, workspaceID string, now int64, out *model.OperationsQueue) error {
	return tx.QueryRowContext(ctx, `select
		coalesce(sum(case when receipt.kind='request' and receipt.state='pending' and receipt.eligible=1 then 1 else 0 end),0),
		coalesce(sum(case when receipt.kind='request' and receipt.state in ('claimed','presented') then 1 else 0 end),0),
		coalesce(sum(case when receipt.kind='request' and receipt.state in ('claimed','presented') and receipt.lease_expires_at>? then 1 else 0 end),0),
		coalesce(sum(case when receipt.kind in ('result','blocker') and receipt.state='pending' and receipt.eligible=1 then 1 else 0 end),0),
		coalesce(sum(case when receipt.kind in ('result','blocker') and receipt.state='pending' then 1 else 0 end),0),
		coalesce(sum(case when receipt.kind in ('result','blocker') and receipt.state in ('claimed','presented') then 1 else 0 end),0),
		coalesce(sum(case when receipt.state='claimed' then 1 else 0 end),0),
		coalesce(sum(case when receipt.state='presented' then 1 else 0 end),0),
		coalesce(sum(case when receipt.state='acknowledged' then 1 else 0 end),0)
		from agents agent left join agent_inbox_receipts receipt on receipt.agent_id=agent.id
		where agent.workstream_id=? and not exists (select 1 from deleted_items where kind='agent' and resource_id=agent.id)`, now, workspaceID).
		Scan(&out.InboundQueued, &out.InboundClaimed, &out.InboundClaimedFresh, &out.ResultsReady, &out.ResultDeliveries, &out.ResultClaims, &out.ReceiptsClaimed, &out.ReceiptsPresented, &out.ReceiptsAcknowledged)
}

func scanOperationsQueue(ctx context.Context, tx *sql.Tx, workspaceID string, now int64, out *model.OperationsQueue) error {
	if err := tx.QueryRowContext(ctx, `select
		coalesce(sum(case when message.status='queued' and (message.kind='request' or message.notification_state='pending') then 1 else 0 end),0),
		coalesce(sum(case when message.status='delivered' then 1 else 0 end),0),
		coalesce(sum(case when message.status='delivered' and message.lease_expires_at>? and (message.processing_deadline_at=0 or message.processing_deadline_at>?) then 1 else 0 end),0),
		coalesce(sum(case when message.kind='result' and message.status='queued' and message.notification_state='pending' then 1 else 0 end),0),
		coalesce(sum(case when message.kind='result' and message.status='delivered' then 1 else 0 end),0)
		from agents target left join agent_messages message on message.target_agent_id=target.id
		where target.workstream_id=? and not exists (select 1 from deleted_items where kind='agent' and resource_id=target.id)`, now, now, workspaceID).
		Scan(&out.InboundQueued, &out.InboundClaimed, &out.InboundClaimedFresh, &out.ResultDeliveries, &out.ResultClaims); err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, `select count(*) from lifecycle_events event join agents recipient on recipient.id=event.recipient_agent_id where recipient.workstream_id=? and event.event_type='message.result' and event.status='pending'`, workspaceID).Scan(&out.ResultsReady)
}

func uniqueMissingOperationsIDs(ids []string, messages map[string]operationsMessage) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if _, exists := messages[id]; !exists {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func queryOperationsMessages(ctx context.Context, tx *sql.Tx, ids []string) ([]operationsMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for index, id := range ids {
		marks[index], args[index] = "?", id
	}
	rows, err := tx.QueryContext(ctx, `select `+operationsMessageColumns+` from agent_messages message where message.id in (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]operationsMessage, 0, len(ids))
	for rows.Next() {
		value, err := scanOperationsMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func operationsAncestorOfCandidate(id string, candidates []string, messages map[string]operationsMessage) bool {
	for _, candidateID := range candidates {
		parent := messages[candidateID].ParentMessageID
		for depth := 0; depth < 16 && parent != ""; depth++ {
			if parent == id {
				return true
			}
			message, ok := messages[parent]
			if !ok {
				break
			}
			parent = message.ParentMessageID
		}
	}
	return false
}

func nearestOperationsRequestParent(parent string, messages map[string]operationsMessage) string {
	for depth := 0; depth < 16 && parent != ""; depth++ {
		message, ok := messages[parent]
		if !ok {
			return ""
		}
		if message.Kind == "request" {
			return parent
		}
		parent = message.ParentMessageID
	}
	return ""
}

func operationsAgentTitles(ctx context.Context, tx *sql.Tx, messages map[string]operationsMessage) (map[string]string, error) {
	ids := make(map[string]bool)
	for _, message := range messages {
		if message.SenderAgentID != "" {
			ids[message.SenderAgentID] = true
		}
		if message.TargetAgentID != "" {
			ids[message.TargetAgentID] = true
		}
	}
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	marks := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for id := range ids {
		marks, args = append(marks, "?"), append(args, id)
	}
	rows, err := tx.QueryContext(ctx, `select id,title from agents where id in (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string, len(ids))
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out[id] = boundedWorkTitle(title)
	}
	return out, rows.Err()
}

func titleOrDelegated(title string) string {
	if title == "" {
		return "Delegated work"
	}
	return title
}

func operationsLatestProgress(ctx context.Context, tx *sql.Tx, messages map[string]operationsMessage) (map[string]model.WorkProgressEvent, error) {
	ids := make([]string, 0)
	for id, message := range messages {
		if message.Kind == "request" {
			ids = append(ids, id)
		}
	}
	out := make(map[string]model.WorkProgressEvent, len(ids))
	for start := 0; start < len(ids); start += 400 {
		end := min(start+400, len(ids))
		marks := make([]string, end-start)
		args := make([]any, end-start)
		for index, id := range ids[start:end] {
			marks[index], args[index] = "?", id
		}
		rows, err := tx.QueryContext(ctx, `select `+workProgressColumns+` from work_progress_events progress where progress.message_id in (`+strings.Join(marks, ",")+`) and progress.sequence=(select max(latest.sequence) from work_progress_events latest where latest.message_id=progress.message_id)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			value, err := scanWorkProgress(rows)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			out[value.MessageID] = value
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func checkpointFromProgress(value model.WorkProgressEvent) *model.WorkCheckpoint {
	return &model.WorkCheckpoint{Phase: value.Phase, Summary: value.Summary, Milestones: value.Milestones, Blocker: value.Blocker, Counts: value.Counts, Source: "reported", ReportedAt: value.CreatedAt}
}

func operationsResultRows(ctx context.Context, tx *sql.Tx, messages map[string]operationsMessage) (map[string]operationsResultRow, map[string]string, error) {
	ids := make([]string, 0)
	for id, message := range messages {
		if message.Kind == "request" {
			ids = append(ids, id)
		}
	}
	results := make(map[string]operationsResultRow)
	lifecycle := make(map[string]string)
	for start := 0; start < len(ids); start += 400 {
		end := min(start+400, len(ids))
		marks := make([]string, end-start)
		args := make([]any, end-start)
		for index, id := range ids[start:end] {
			marks[index], args[index] = "?", id
		}
		rows, err := tx.QueryContext(ctx, `select reply_to,status,notification_state,claimed_at,lease_expires_at,completed_at,updated_at from agent_messages where kind='result' and reply_to in (`+strings.Join(marks, ",")+`) and id='result:'||reply_to`, args...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var reply string
			var value operationsResultRow
			if err := rows.Scan(&reply, &value.Status, &value.NotificationState, &value.ClaimedAt, &value.LeaseExpiresAt, &value.CompletedAt, &value.UpdatedAt); err != nil {
				_ = rows.Close()
				return nil, nil, err
			}
			results[reply] = value
		}
		if err := rows.Close(); err != nil {
			return nil, nil, err
		}
		lifecycleArgs := append([]any(nil), args...)
		lifeRows, err := tx.QueryContext(ctx, `select message_id,status from lifecycle_events where event_type='message.result' and message_id in (`+strings.Join(marks, ",")+`)`, lifecycleArgs...)
		if err != nil {
			return nil, nil, err
		}
		for lifeRows.Next() {
			var id, status string
			if err := lifeRows.Scan(&id, &status); err != nil {
				_ = lifeRows.Close()
				return nil, nil, err
			}
			lifecycle[id] = status
		}
		if err := lifeRows.Close(); err != nil {
			return nil, nil, err
		}
	}
	return results, lifecycle, nil
}

func operationsResultFact(message operationsMessage, result operationsResultRow, lifecycle string, now int64) *model.OperationsResult {
	if message.Status != "completed" && message.Status != "failed" {
		return nil
	}
	if message.ResultMode == "none" || message.Act == "inform" {
		return nil
	}
	fact := &model.OperationsResult{Source: "observed", ObservedAt: max(message.CompletedAt, message.UpdatedAt)}
	if result.Status != "" {
		fact.ObservedAt = max(fact.ObservedAt, result.UpdatedAt)
		switch result.Status {
		case "queued":
			fact.Stage, fact.Label = "delivery_queued", "Durable result delivery queued; Pi handling is not observed"
		case "delivered":
			fact.Stage, fact.Label = "delivery_claimed", "Durable result delivery claimed; Pi handling is not observed"
			if result.LeaseExpiresAt > now {
				fact.Lease = "fresh"
			} else {
				fact.Lease = "stale"
			}
		case "completed":
			fact.Stage, fact.Label = "delivery_completed", "Durable result delivery completed"
		default:
			fact.Stage, fact.Label = "delivery_failed", "Durable result delivery failed"
		}
		return fact
	}
	if lifecycle == "pending" || message.NotificationState == "pending" {
		fact.Stage, fact.Label = "result_ready", "Result ready for durable queue projection"
	} else if lifecycle == "delivered" {
		fact.Stage, fact.Label = "result_projected", "Result projected to the durable queue"
	} else if message.NotificationState == "suppressed" {
		fact.Stage, fact.Label = "result_suppressed", "Result notification suppressed by durable policy"
	} else {
		return nil
	}
	return fact
}

func operationsActivityLane(ctx context.Context, tx *sql.Tx, currentIDs map[string]string, v2Projection bool) (*model.OperationsActivityLane, error) {
	ids := make([]string, 0, len(currentIDs))
	for _, id := range currentIDs {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	facts := make([]model.OperationsActivityFact, 0)
	for start := 0; start < len(ids); start += 300 {
		end := min(start+300, len(ids))
		marks := make([]string, end-start)
		args := make([]any, end-start)
		for index, id := range ids[start:end] {
			marks[index], args[index] = "?", id
		}
		activityFence := `join agent_messages message on message.id=activity.message_id and message.attempt=activity.attempt and message.runtime_id=activity.runtime_id`
		if v2Projection {
			activityFence = `join agent_operations operation on operation.parent_message_id=activity.message_id and operation.attempt=activity.attempt and operation.runtime_id=activity.runtime_id`
		}
		rows, err := tx.QueryContext(ctx, `select activity.message_id,activity.category,activity.status,activity.observed_at
			from work_activity_events activity `+activityFence+`
			where activity.message_id in (`+strings.Join(marks, ",")+`) and activity.sequence=(select max(latest.sequence) from work_activity_events latest where latest.message_id=activity.message_id and latest.attempt=activity.attempt)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, category, status string
			var observedAt int64
			if err := rows.Scan(&id, &category, &status, &observedAt); err != nil {
				_ = rows.Close()
				return nil, err
			}
			facts = append(facts, model.OperationsActivityFact{Category: category, Status: status, Source: "observed", ObservedAt: observedAt})
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	slices.SortStableFunc(facts, func(left, right model.OperationsActivityFact) int {
		if left.ObservedAt != right.ObservedAt {
			if left.ObservedAt > right.ObservedAt {
				return -1
			}
			return 1
		}
		return strings.Compare(left.Category+left.Status, right.Category+right.Status)
	})
	lane := &model.OperationsActivityLane{Version: 1, Facts: facts, Truncation: model.OperationsLaneTruncation{MaxFacts: OperationsMaxActivityFacts, OmissionExact: true}}
	if len(lane.Facts) > OperationsMaxActivityFacts {
		lane.Truncation.Truncated = true
		lane.Truncation.FactsOmitted = len(lane.Facts) - OperationsMaxActivityFacts
		lane.Facts = lane.Facts[:OperationsMaxActivityFacts]
	}
	return lane, nil
}

func classifyOperationsTree(item *model.WorkItem) int {
	rank := classifyOperationsItem(*item)
	for index := range item.Children {
		childRank := classifyOperationsTree(&item.Children[index])
		if childRank < rank {
			rank = childRank
		}
	}
	slices.SortStableFunc(item.Children, func(left, right model.WorkItem) int {
		leftRank, rightRank := operationsTreeRank(left), operationsTreeRank(right)
		if leftRank != rightRank {
			return leftRank - rightRank
		}
		if left.UpdatedAt != right.UpdatedAt {
			if left.UpdatedAt > right.UpdatedAt {
				return -1
			}
			return 1
		}
		return strings.Compare(left.ID, right.ID)
	})
	item.Priority = operationsPriorityLabel(rank)
	return rank
}

func operationsTreeRank(item model.WorkItem) int {
	rank := classifyOperationsItem(item)
	for _, child := range item.Children {
		if childRank := operationsTreeRank(child); childRank < rank {
			rank = childRank
		}
	}
	return rank
}

func classifyOperationsItem(item model.WorkItem) int {
	if item.Checkpoint != nil && item.Checkpoint.Blocker != "" {
		return 0
	}
	if item.Observation.State == "started" && item.Observation.Lease == "fresh" {
		return 1
	}
	if item.Observation.State == "waiting" {
		return 2
	}
	if item.Observation.State == "queued" {
		return 3
	}
	if item.Observation.State == "started" && item.Observation.Lease == "stale" {
		return 4
	}
	if item.Observation.State == "failed" || item.Observation.State == "canceled" || item.Observation.State == "expired" {
		return 5
	}
	return 6
}

func operationsPriorityLabel(rank int) string {
	return []string{"reported_blocker", "active", "waiting", "queued", "stale_observation", "recent_failure", "recent_completion"}[min(max(rank, 0), 6)]
}

func countOperationsItems(item model.WorkItem) int {
	total := 1
	for _, child := range item.Children {
		total += countOperationsItems(child)
	}
	return total
}

func copyBoundedOperationsTree(item model.WorkItem, remaining *int) (model.WorkItem, bool) {
	if *remaining <= 0 {
		return model.WorkItem{}, false
	}
	*remaining--
	children := item.Children
	item.Children = []model.WorkItem{}
	for _, child := range children {
		value, ok := copyBoundedOperationsTree(child, remaining)
		if !ok {
			break
		}
		item.Children = append(item.Children, value)
	}
	return item, true
}

func collectOperationsSummary(item model.WorkItem, summary *model.OperationsSummary) {
	if item.Checkpoint != nil && item.Checkpoint.Blocker != "" {
		summary.ReportedBlockers++
	}
	switch {
	case item.Observation.State == "started" && item.Observation.Lease == "fresh":
		summary.ActiveWork++
	case item.Observation.State == "waiting":
		summary.WaitingWork++
	case item.Observation.State == "queued":
		summary.QueuedWork++
	case item.Observation.State == "started" && item.Observation.Lease == "stale":
		summary.StaleObservations++
	case item.Observation.State == "failed" || item.Observation.State == "canceled" || item.Observation.State == "expired":
		summary.RecentFailures++
	case item.Observation.State == "completed":
		summary.RecentCompletions++
	}
	if coordinationFact(item.Coordination, "resume", "queued") {
		summary.ResumeQueued++
	}
	if coordinationFact(item.Coordination, "todo_link", "pending") || coordinationFact(item.Coordination, "todo_settlement", "pending") {
		summary.TodoPending++
	}
	if coordinationFact(item.Coordination, "todo_link", "applied") || coordinationFact(item.Coordination, "todo_settlement", "applied") {
		summary.TodoApplied++
	}
	if coordinationFact(item.Coordination, "result", "legacy_suppressed_unknown") {
		summary.LegacySuppressedUnknown++
	}
	for _, child := range item.Children {
		collectOperationsSummary(child, summary)
	}
}

func collectCurrentDeliveries(item model.WorkItem, current map[string]model.OperationsDelivery, currentIDs map[string]string) {
	if item.Observation.State == "started" && item.Observation.Lease == "fresh" && item.TargetAgentID != "" {
		candidate := model.OperationsDelivery{WorkID: item.ID, Title: item.Title, Observation: item.Observation, Checkpoint: item.Checkpoint, UpdatedAt: item.UpdatedAt}
		existing, found := current[item.TargetAgentID]
		if !found || candidate.UpdatedAt > existing.UpdatedAt || (candidate.UpdatedAt == existing.UpdatedAt && candidate.WorkID < existing.WorkID) {
			current[item.TargetAgentID] = candidate
			currentIDs[item.TargetAgentID] = item.ID
		}
	}
	for _, child := range item.Children {
		collectCurrentDeliveries(child, current, currentIDs)
	}
}

func collectObservedDeliveries(item model.WorkItem, observed map[string]model.OperationsDelivery) {
	if item.TargetAgentID != "" {
		candidate := model.OperationsDelivery{WorkID: item.ID, Title: item.Title, Observation: item.Observation, Checkpoint: item.Checkpoint, UpdatedAt: item.UpdatedAt}
		existing, found := observed[item.TargetAgentID]
		if !found || candidate.UpdatedAt > existing.UpdatedAt || (candidate.UpdatedAt == existing.UpdatedAt && candidate.WorkID < existing.WorkID) {
			observed[item.TargetAgentID] = candidate
		}
	}
	for _, child := range item.Children {
		collectObservedDeliveries(child, observed)
	}
}

func collectOperationsTimeline(item model.WorkItem, facts *[]model.OperationsTimelineFact) {
	for _, event := range item.Timeline {
		*facts = append(*facts, model.OperationsTimelineFact{WorkID: item.ID, WorkTitle: item.Title, TargetTitle: item.TargetTitle, Kind: event.Kind, Label: event.Label, Source: event.Source, CreatedAt: event.CreatedAt})
	}
	for _, child := range item.Children {
		collectOperationsTimeline(child, facts)
	}
}
