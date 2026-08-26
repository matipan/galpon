package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/matipan/galpon/internal/model"
)

const workCoordinationFactLimit = 24

type projectionQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type projectionOperation struct {
	ID             string
	State          string
	Attempt        int
	LeaseExpiresAt int64
	UpdatedAt      int64
	SettledAt      int64
}

type projectionStateRow struct {
	State     string
	UpdatedAt int64
}

type projectionReceipt struct {
	Kind, State string
	Eligible    bool
	UpdatedAt   int64
}

type projectionResult struct {
	Status, LegacyState string
	CreatedAt           int64
}

type workCommunicationSource struct {
	targetOperation *projectionOperation
	sourceOperation *projectionOperation
	joins           []projectionStateRow
	result          *projectionResult
	receipts        []projectionReceipt
	todoLinks       []projectionStateRow
	todoSettlements []projectionStateRow
}

func communicationV2ProjectionEnabled(ctx context.Context, query projectionQueryer) (bool, error) {
	var generation int
	var complete bool
	if err := query.QueryRowContext(ctx, `select generation,cutover_complete from communication_protocol_state where singleton=1`).Scan(&generation, &complete); err != nil {
		return false, err
	}
	return complete && generation >= 2, nil
}

func projectionMarks(ids []string) (string, []any) {
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for index, id := range ids {
		marks[index], args[index] = "?", id
	}
	return strings.Join(marks, ","), args
}

// loadWorkCommunication reads only protocol state, timestamps, and internal
// relations. Private payload columns are not selected.
func loadWorkCommunication(ctx context.Context, query projectionQueryer, messageIDs []string) (map[string]*workCommunicationSource, error) {
	out := make(map[string]*workCommunicationSource, len(messageIDs))
	if len(messageIDs) == 0 {
		return out, nil
	}
	for _, id := range messageIDs {
		out[id] = &workCommunicationSource{}
	}
	marks, args := projectionMarks(messageIDs)
	rows, err := query.QueryContext(ctx, `select parent_message_id,id,state,attempt,lease_expires_at,updated_at,settled_at from agent_operations where parent_message_id in (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var messageID string
		var value projectionOperation
		if err := rows.Scan(&messageID, &value.ID, &value.State, &value.Attempt, &value.LeaseExpiresAt, &value.UpdatedAt, &value.SettledAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[messageID].targetOperation = &value
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = query.QueryContext(ctx, `select meta.message_id,operation.id,operation.state,operation.attempt,operation.lease_expires_at,operation.updated_at,operation.settled_at
		from coordination_message_meta meta join agent_operations operation on operation.id=meta.source_operation_id
		where meta.message_id in (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var messageID string
		var value projectionOperation
		if err := rows.Scan(&messageID, &value.ID, &value.State, &value.Attempt, &value.LeaseExpiresAt, &value.UpdatedAt, &value.SettledAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[messageID].sourceOperation = &value
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = query.QueryContext(ctx, `select message_id,state,updated_at from agent_operation_joins where message_id in (`+marks+`) order by created_at,id`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var messageID string
		var value projectionStateRow
		if err := rows.Scan(&messageID, &value.State, &value.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[messageID].joins = append(out[messageID].joins, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = query.QueryContext(ctx, `select message_id,status,legacy_state,created_at from agent_message_results where message_id in (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var messageID string
		var value projectionResult
		if err := rows.Scan(&messageID, &value.Status, &value.LegacyState, &value.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[messageID].result = &value
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = query.QueryContext(ctx, `select message_id,kind,state,eligible,updated_at from agent_inbox_receipts where message_id in (`+marks+`) order by created_at,id`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var messageID string
		var value projectionReceipt
		if err := rows.Scan(&messageID, &value.Kind, &value.State, &value.Eligible, &value.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[messageID].receipts = append(out[messageID].receipts, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = query.QueryContext(ctx, `select message_id,state,max(created_at,applied_at) from todo_link_intents where message_id in (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var messageID string
		var value projectionStateRow
		if err := rows.Scan(&messageID, &value.State, &value.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[messageID].todoLinks = append(out[messageID].todoLinks, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = query.QueryContext(ctx, `select intent.message_id,event.state,max(event.created_at,event.applied_at,event.acknowledged_at)
		from todo_settlement_events event join todo_link_intents intent on intent.id=event.intent_id
		where intent.message_id in (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var messageID string
		var value projectionStateRow
		if err := rows.Scan(&messageID, &value.State, &value.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[messageID].todoSettlements = append(out[messageID].todoSettlements, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

func appendCoordinationFact(facts *[]model.WorkCoordinationFact, kind, state string, count int, observedAt int64) {
	if state == "" || count == 0 {
		return
	}
	*facts = append(*facts, model.WorkCoordinationFact{Kind: kind, State: state, Count: count, ObservedAt: observedAt})
}

func buildWorkCoordination(messageState string, messageUpdatedAt int64, source *workCommunicationSource) *model.WorkCoordination {
	if source == nil {
		return &model.WorkCoordination{Version: 2, Facts: []model.WorkCoordinationFact{}}
	}
	facts := make([]model.WorkCoordinationFact, 0, 12)
	appendCoordinationFact(&facts, "message", messageState, 1, messageUpdatedAt)
	if source.targetOperation != nil {
		appendCoordinationFact(&facts, "target_operation", source.targetOperation.State, 1, source.targetOperation.UpdatedAt)
	}
	if source.sourceOperation != nil {
		appendCoordinationFact(&facts, "source_operation", source.sourceOperation.State, 1, source.sourceOperation.UpdatedAt)
	}
	counts := make(map[string]model.WorkCoordinationFact)
	add := func(kind, state string, observedAt int64) {
		key := kind + "\x00" + state
		value := counts[key]
		value.Kind, value.State, value.Count = kind, state, value.Count+1
		value.ObservedAt = max(value.ObservedAt, observedAt)
		counts[key] = value
	}
	for _, join := range source.joins {
		add("join", join.State, join.UpdatedAt)
	}
	if source.result != nil {
		state := source.result.Status
		if source.result.LegacyState != "" {
			state = source.result.LegacyState
		}
		add("result", state, source.result.CreatedAt)
	}
	resumeQueued := source.targetOperation != nil && source.targetOperation.State == "ready" && source.targetOperation.Attempt > 0
	for _, join := range source.joins {
		resumeQueued = resumeQueued || source.sourceOperation != nil && source.sourceOperation.State == "ready" && join.State == "ready"
	}
	for _, receipt := range source.receipts {
		add(receipt.Kind+"_receipt", receipt.State, receipt.UpdatedAt)
		if receipt.Eligible && (receipt.Kind == "result" || receipt.Kind == "blocker") && receipt.State == "pending" {
			add("result_delivery", "ready", receipt.UpdatedAt)
		}
	}
	if resumeQueued {
		observedAt := messageUpdatedAt
		if source.targetOperation != nil {
			observedAt = max(observedAt, source.targetOperation.UpdatedAt)
		}
		if source.sourceOperation != nil {
			observedAt = max(observedAt, source.sourceOperation.UpdatedAt)
		}
		add("resume", "queued", observedAt)
	}
	for _, value := range source.todoLinks {
		add("todo_link", value.State, value.UpdatedAt)
	}
	for _, value := range source.todoSettlements {
		add("todo_settlement", value.State, value.UpdatedAt)
	}
	orderedKinds := []string{"join", "result", "result_delivery", "request_receipt", "result_receipt", "blocker_receipt", "control_receipt", "resume", "todo_link", "todo_settlement"}
	for _, kind := range orderedKinds {
		states := make([]model.WorkCoordinationFact, 0)
		for _, value := range counts {
			if value.Kind == kind {
				states = append(states, value)
			}
		}
		slices.SortFunc(states, func(left, right model.WorkCoordinationFact) int { return strings.Compare(left.State, right.State) })
		facts = append(facts, states...)
	}
	out := &model.WorkCoordination{Version: 2, Facts: facts}
	if len(out.Facts) > workCoordinationFactLimit {
		out.Facts = out.Facts[:workCoordinationFactLimit]
		out.Truncated = true
	}
	return out
}

func v2WorkObservation(messageState, terminalReason string, messageObservedAt int64, operation *projectionOperation, now int64) model.WorkObservation {
	if operation == nil {
		message := operationsMessage{Status: messageState, TerminalReason: terminalReason, CreatedAt: messageObservedAt, UpdatedAt: messageObservedAt}
		return model.WorkObservation{State: operationsObservedState(message), Source: "observed", ObservedAt: messageObservedAt, Lease: "none"}
	}
	state, lease := operation.State, "none"
	switch operation.State {
	case "ready":
		state = "queued"
	case "claimed", "running", "settling":
		state = "started"
		if operation.LeaseExpiresAt > now {
			lease = "fresh"
		} else {
			lease = "stale"
		}
	case "settled":
		state = "completed"
	case "waiting":
		state = "waiting"
	}
	return model.WorkObservation{State: state, Source: "observed", ObservedAt: operation.UpdatedAt, Lease: lease, LeaseObservedAt: operation.UpdatedAt, Attempt: operation.Attempt, FreshnessAt: operation.LeaseExpiresAt}
}

func coordinationFact(value *model.WorkCoordination, kind, state string) bool {
	return value != nil && slices.ContainsFunc(value.Facts, func(fact model.WorkCoordinationFact) bool { return fact.Kind == kind && fact.State == state })
}

func coordinationFactTime(value *model.WorkCoordination, kind, state string) int64 {
	if value == nil {
		return 0
	}
	for _, fact := range value.Facts {
		if fact.Kind == kind && fact.State == state {
			return fact.ObservedAt
		}
	}
	return 0
}

func v2OperationsResultFact(value *model.WorkCoordination) *model.OperationsResult {
	facts := []struct {
		kind, state, stage, label string
	}{
		{"result", "legacy_suppressed_unknown", "legacy_suppressed_unknown", "Legacy result suppression state is unknown"},
		{"result_delivery", "ready", "result_ready", "Durable result is ready"},
		{"result_receipt", "presented", "receipt_presented", "Durable result receipt was presented"},
		{"result_receipt", "claimed", "receipt_claimed", "Durable result receipt is claimed"},
		{"result_receipt", "acknowledged", "receipt_acknowledged", "Durable result receipt is acknowledged"},
		{"blocker_receipt", "presented", "receipt_presented", "Durable blocker receipt was presented"},
		{"blocker_receipt", "claimed", "receipt_claimed", "Durable blocker receipt is claimed"},
		{"blocker_receipt", "acknowledged", "receipt_acknowledged", "Durable blocker receipt is acknowledged"},
		{"result", "failed", "delivery_failed", "Durable result records a failure"},
		{"result", "canceled", "delivery_failed", "Durable result records a cancellation"},
		{"result", "expired", "delivery_failed", "Durable result records an expiry"},
		{"result", "completed", "result_recorded", "Durable result is recorded"},
	}
	for _, fact := range facts {
		if observedAt := coordinationFactTime(value, fact.kind, fact.state); observedAt > 0 {
			return &model.OperationsResult{Stage: fact.stage, Label: fact.label, Source: "observed", ObservedAt: observedAt}
		}
	}
	return nil
}
