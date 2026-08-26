package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/matipan/galpon/internal/model"
)

func durableCommunicationState(ctx context.Context, tx *sql.Tx, out *model.DurableState) error {
	agents := make(map[string]bool, len(out.Agents))
	messages := make(map[string]bool, len(out.Messages))
	for _, value := range out.Agents {
		agents[value.ID] = true
	}
	for _, value := range out.Messages {
		messages[value.ID] = true
	}
	rows, err := tx.QueryContext(ctx, `select `+operationColumns+` from agent_operations order by created_at,id`)
	if err != nil {
		return err
	}
	operations := make(map[string]bool)
	for rows.Next() {
		value, err := scanOperation(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if agents[value.AgentID] && (value.ParentMessageID == "" || messages[value.ParentMessageID]) {
			out.AgentOperations = append(out.AgentOperations, value)
			operations[value.ID] = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `select id,operation_id,attempt,runtime_id,claim_key,state,started_at,updated_at,finished_at,terminal_reason from agent_operation_attempts order by started_at,id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var value model.AgentOperationAttempt
		if err := rows.Scan(&value.ID, &value.OperationID, &value.Attempt, &value.RuntimeID, &value.ClaimKey, &value.State, &value.StartedAt, &value.UpdatedAt, &value.FinishedAt, &value.TerminalReason); err != nil {
			_ = rows.Close()
			return err
		}
		if operations[value.OperationID] {
			out.AgentOperationAttempts = append(out.AgentOperationAttempts, value)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `select id,message_id,status,response,error,terminal_reason,legacy_state,created_at from agent_message_results order by created_at,id`)
	if err != nil {
		return err
	}
	results := make(map[string]bool)
	for rows.Next() {
		value, err := scanAgentMessageResult(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if messages[value.MessageID] {
			out.AgentMessageResults = append(out.AgentMessageResults, value)
			results[value.ID] = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `select `+receiptColumns+` from agent_inbox_receipts order by created_at,id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		value, err := scanAgentInboxReceipt(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if agents[value.AgentID] && (value.OperationID == "" || operations[value.OperationID]) && (value.MessageID == "" || messages[value.MessageID]) && (value.ResultID == "" || results[value.ResultID]) {
			out.AgentInboxReceipts = append(out.AgentInboxReceipts, value)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `select id,operation_id,message_id,state,deadline_at,failure,created_at,updated_at,resolved_at from agent_operation_joins order by created_at,id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var value model.AgentOperationJoin
		if err := rows.Scan(&value.ID, &value.OperationID, &value.MessageID, &value.State, &value.DeadlineAt, &value.Failure, &value.CreatedAt, &value.UpdatedAt, &value.ResolvedAt); err != nil {
			_ = rows.Close()
			return err
		}
		if operations[value.OperationID] && messages[value.MessageID] {
			out.AgentOperationJoins = append(out.AgentOperationJoins, value)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `select id,agent_id,coalesce(operation_id,''),operation_attempt,event_id,kind,state,payload,created_at,acknowledged_at from agent_pi_local_events order by created_at,id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var value model.AgentPiLocalEvent
		if err := rows.Scan(&value.ID, &value.AgentID, &value.OperationID, &value.OperationAttempt, &value.EventID, &value.Kind, &value.State, &value.Payload, &value.CreatedAt, &value.AcknowledgedAt); err != nil {
			_ = rows.Close()
			return err
		}
		if agents[value.AgentID] && (value.OperationID == "" || operations[value.OperationID]) {
			out.AgentPiLocalEvents = append(out.AgentPiLocalEvents, value)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `select message_id,source_operation_id,request_hash,created_at from coordination_message_meta order by created_at,message_id`)
	if err != nil {
		return err
	}
	metaMessages := make(map[string]bool)
	for rows.Next() {
		var value model.AgentCoordinationMessageMeta
		if err := rows.Scan(&value.MessageID, &value.SourceOperationID, &value.RequestHash, &value.CreatedAt); err != nil {
			_ = rows.Close()
			return err
		}
		if messages[value.MessageID] && (value.SourceOperationID == "" || operations[value.SourceOperationID]) {
			out.AgentCoordinationMessageMeta = append(out.AgentCoordinationMessageMeta, value)
			metaMessages[value.MessageID] = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `select sender_agent_id,idempotency_key,request_hash,message_id,created_at from coordination_send_receipts order by created_at,message_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var value model.AgentCoordinationSendReceipt
		if err := rows.Scan(&value.SenderAgentID, &value.IdempotencyKey, &value.RequestHash, &value.MessageID, &value.CreatedAt); err != nil {
			_ = rows.Close()
			return err
		}
		if agents[value.SenderAgentID] && messages[value.MessageID] && metaMessages[value.MessageID] {
			out.AgentCoordinationSendReceipts = append(out.AgentCoordinationSendReceipts, value)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `select id,message_id,coalesce(operation_id,''),todo_id,policy,state,created_at,applied_at from todo_link_intents order by created_at,id`)
	if err != nil {
		return err
	}
	intents := make(map[string]bool)
	for rows.Next() {
		var value model.AgentTodoLinkIntent
		if err := rows.Scan(&value.ID, &value.MessageID, &value.OperationID, &value.TodoID, &value.Policy, &value.State, &value.CreatedAt, &value.AppliedAt); err != nil {
			_ = rows.Close()
			return err
		}
		if messages[value.MessageID] && (value.OperationID == "" || operations[value.OperationID]) {
			out.AgentTodoLinkIntents = append(out.AgentTodoLinkIntents, value)
			intents[value.ID] = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `select id,intent_id,result_id,state,snapshot,created_at,applied_at from todo_settlement_events order by created_at,id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var value model.AgentTodoSettlementEvent
		if err := rows.Scan(&value.ID, &value.IntentID, &value.ResultID, &value.State, &value.Snapshot, &value.CreatedAt, &value.AppliedAt); err != nil {
			_ = rows.Close()
			return err
		}
		if intents[value.IntentID] && results[value.ResultID] {
			out.AgentTodoSettlementEvents = append(out.AgentTodoSettlementEvents, value)
		}
	}
	return rows.Close()
}

func restoreCommunicationState(ctx context.Context, tx *sql.Tx, state model.DurableState) error {
	for _, value := range state.AgentOperations {
		if _, err := tx.ExecContext(ctx, `insert into agent_operations(`+operationColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, operationValues(value)...); err != nil {
			return fmt.Errorf("restore operation %s: %w", value.ID, err)
		}
	}
	for _, value := range state.AgentOperationAttempts {
		if _, err := tx.ExecContext(ctx, `insert into agent_operation_attempts(id,operation_id,attempt,runtime_id,claim_key,state,started_at,updated_at,finished_at,terminal_reason) values(?,?,?,?,?,?,?,?,?,?)`, value.ID, value.OperationID, value.Attempt, value.RuntimeID, value.ClaimKey, value.State, value.StartedAt, value.UpdatedAt, value.FinishedAt, value.TerminalReason); err != nil {
			return fmt.Errorf("restore operation attempt %s: %w", value.ID, err)
		}
	}
	for _, value := range state.AgentCoordinationMessageMeta {
		if _, err := tx.ExecContext(ctx, `insert into coordination_message_meta(message_id,source_operation_id,request_hash,created_at) values(?,?,?,?)`, value.MessageID, value.SourceOperationID, value.RequestHash, value.CreatedAt); err != nil {
			return fmt.Errorf("restore coordination message metadata %s: %w", value.MessageID, err)
		}
	}
	for _, value := range state.AgentCoordinationSendReceipts {
		if _, err := tx.ExecContext(ctx, `insert into coordination_send_receipts(sender_agent_id,idempotency_key,request_hash,message_id,created_at) values(?,?,?,?,?)`, value.SenderAgentID, value.IdempotencyKey, value.RequestHash, value.MessageID, value.CreatedAt); err != nil {
			return fmt.Errorf("restore coordination send receipt %s: %w", value.MessageID, err)
		}
	}
	for _, value := range state.AgentMessageResults {
		if _, err := tx.ExecContext(ctx, `insert into agent_message_results(id,message_id,status,response,error,terminal_reason,legacy_state,created_at) values(?,?,?,?,?,?,?,?)`, value.ID, value.MessageID, value.Status, value.Response, value.Error, value.TerminalReason, value.LegacyState, value.CreatedAt); err != nil {
			return fmt.Errorf("restore message result %s: %w", value.ID, err)
		}
	}
	for _, value := range state.AgentOperationJoins {
		if _, err := tx.ExecContext(ctx, `insert into agent_operation_joins(id,operation_id,message_id,state,deadline_at,failure,created_at,updated_at,resolved_at) values(?,?,?,?,?,?,?,?,?)`, value.ID, value.OperationID, value.MessageID, value.State, value.DeadlineAt, value.Failure, value.CreatedAt, value.UpdatedAt, value.ResolvedAt); err != nil {
			return fmt.Errorf("restore operation join %s: %w", value.ID, err)
		}
	}
	for _, value := range state.AgentInboxReceipts {
		if _, err := tx.ExecContext(ctx, `insert into agent_inbox_receipts(id,agent_id,operation_id,message_id,result_id,kind,state,eligible,runtime_id,claim_key,pi_tool_request_id,attempt,operation_attempt,claimed_at,lease_expires_at,presented_at,acknowledged_at,abandoned_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.AgentID, nullString(value.OperationID), value.MessageID, nullString(value.ResultID), value.Kind, value.State, value.Eligible, value.RuntimeID, value.ClaimKey, value.PiToolRequestID, value.Attempt, value.OperationAttempt, value.ClaimedAt, value.LeaseExpiresAt, value.PresentedAt, value.AcknowledgedAt, value.AbandonedAt, value.CreatedAt, value.UpdatedAt); err != nil {
			return fmt.Errorf("restore inbox receipt %s: %w", value.ID, err)
		}
	}
	for _, value := range state.AgentPiLocalEvents {
		if _, err := tx.ExecContext(ctx, `insert into agent_pi_local_events(id,agent_id,operation_id,operation_attempt,event_id,kind,state,payload,created_at,acknowledged_at) values(?,?,?,?,?,?,?,?,?,?)`, value.ID, value.AgentID, nullString(value.OperationID), value.OperationAttempt, value.EventID, value.Kind, value.State, value.Payload, value.CreatedAt, value.AcknowledgedAt); err != nil {
			return fmt.Errorf("restore Pi-local event %s: %w", value.ID, err)
		}
	}
	for _, value := range state.AgentTodoLinkIntents {
		if _, err := tx.ExecContext(ctx, `insert into todo_link_intents(id,message_id,operation_id,todo_id,policy,state,created_at,applied_at) values(?,?,?,?,?,?,?,?)`, value.ID, value.MessageID, nullString(value.OperationID), value.TodoID, value.Policy, value.State, value.CreatedAt, value.AppliedAt); err != nil {
			return fmt.Errorf("restore TODO link intent %s: %w", value.ID, err)
		}
	}
	for _, value := range state.AgentTodoSettlementEvents {
		if _, err := tx.ExecContext(ctx, `insert into todo_settlement_events(id,intent_id,result_id,state,snapshot,created_at,applied_at) values(?,?,?,?,?,?,?)`, value.ID, value.IntentID, value.ResultID, value.State, value.Snapshot, value.CreatedAt, value.AppliedAt); err != nil {
			return fmt.Errorf("restore TODO settlement event %s: %w", value.ID, err)
		}
	}
	return nil
}

func validateCommunicationState(state model.DurableState) error {
	agents := make(map[string]bool, len(state.Agents))
	messages := make(map[string]bool, len(state.Messages))
	for _, value := range state.Agents {
		agents[value.ID] = true
	}
	for _, value := range state.Messages {
		messages[value.ID] = true
	}
	operations := make(map[string]bool, len(state.AgentOperations))
	for _, value := range state.AgentOperations {
		if value.ID == "" || operations[value.ID] || !agents[value.AgentID] || (value.Kind != "direct" && value.Kind != "inbound") || !validOperationState(value.State) || value.CausalRunID == "" || value.Attempt < 0 {
			return fmt.Errorf("checkpoint has an invalid coordination operation")
		}
		if value.ParentMessageID != "" && !messages[value.ParentMessageID] {
			return fmt.Errorf("checkpoint operation %s has an unknown parent message", value.ID)
		}
		operations[value.ID] = true
	}
	attempts := make(map[string]bool, len(state.AgentOperationAttempts))
	for _, value := range state.AgentOperationAttempts {
		if value.ID == "" || attempts[value.ID] || !operations[value.OperationID] || value.Attempt <= 0 || value.RuntimeID == "" || !validAttemptState(value.State) {
			return fmt.Errorf("checkpoint has an invalid operation attempt")
		}
		attempts[value.ID] = true
	}
	results := make(map[string]bool, len(state.AgentMessageResults))
	resultMessages := make(map[string]bool, len(state.AgentMessageResults))
	for _, value := range state.AgentMessageResults {
		if value.ID == "" || results[value.ID] || !messages[value.MessageID] || resultMessages[value.MessageID] || !validResultStatus(value.Status) || value.LegacyState != "" && value.LegacyState != "legacy_suppressed_unknown" {
			return fmt.Errorf("checkpoint has an invalid immutable message result")
		}
		results[value.ID], resultMessages[value.MessageID] = true, true
	}
	joins := make(map[string]bool, len(state.AgentOperationJoins))
	for _, value := range state.AgentOperationJoins {
		if value.ID == "" || joins[value.ID] || !operations[value.OperationID] || !messages[value.MessageID] || !validJoinState(value.State) {
			return fmt.Errorf("checkpoint has an invalid operation join")
		}
		joins[value.ID] = true
	}
	receipts := make(map[string]bool, len(state.AgentInboxReceipts))
	for _, value := range state.AgentInboxReceipts {
		if value.ID == "" || receipts[value.ID] || !agents[value.AgentID] || value.OperationID != "" && !operations[value.OperationID] || value.MessageID != "" && !messages[value.MessageID] || value.ResultID != "" && !results[value.ResultID] || !validReceiptKind(value.Kind) || !validReceiptState(value.State) {
			return fmt.Errorf("checkpoint has an invalid inbox receipt")
		}
		receipts[value.ID] = true
	}
	messageMeta := make(map[string]bool, len(state.AgentCoordinationMessageMeta))
	for _, value := range state.AgentCoordinationMessageMeta {
		if value.MessageID == "" || messageMeta[value.MessageID] || !messages[value.MessageID] || value.SourceOperationID != "" && !operations[value.SourceOperationID] || value.RequestHash == "" {
			return fmt.Errorf("checkpoint has invalid coordination message metadata")
		}
		messageMeta[value.MessageID] = true
	}
	sendReceipts := make(map[string]bool, len(state.AgentCoordinationSendReceipts))
	for _, value := range state.AgentCoordinationSendReceipts {
		key := value.SenderAgentID + "\x00" + value.IdempotencyKey
		if value.SenderAgentID == "" || !agents[value.SenderAgentID] || value.IdempotencyKey == "" || value.RequestHash == "" || !messageMeta[value.MessageID] || sendReceipts[key] {
			return fmt.Errorf("checkpoint has an invalid coordination send receipt")
		}
		sendReceipts[key] = true
	}
	localEvents := make(map[string]bool, len(state.AgentPiLocalEvents))
	for _, value := range state.AgentPiLocalEvents {
		if value.ID == "" || localEvents[value.ID] || !agents[value.AgentID] || value.OperationID != "" && !operations[value.OperationID] || value.EventID == "" || value.Kind == "" || value.State != "pending" && value.State != "acknowledged" {
			return fmt.Errorf("checkpoint has an invalid Pi-local event")
		}
		localEvents[value.ID] = true
	}
	intents := make(map[string]bool, len(state.AgentTodoLinkIntents))
	for _, value := range state.AgentTodoLinkIntents {
		if value.ID == "" || intents[value.ID] || !messages[value.MessageID] || value.OperationID != "" && !operations[value.OperationID] || value.TodoID <= 0 || value.Policy != "complete_on_success" && value.Policy != "annotate" || value.State != "pending" && value.State != "applied" && value.State != "failed" {
			return fmt.Errorf("checkpoint has an invalid TODO link intent")
		}
		intents[value.ID] = true
	}
	events := make(map[string]bool, len(state.AgentTodoSettlementEvents))
	for _, value := range state.AgentTodoSettlementEvents {
		if value.ID == "" || events[value.ID] || !intents[value.IntentID] || !results[value.ResultID] || value.State != "pending" && value.State != "applied" && value.State != "failed" {
			return fmt.Errorf("checkpoint has an invalid TODO settlement event")
		}
		events[value.ID] = true
	}
	return nil
}

func validOperationState(value string) bool {
	switch value {
	case "ready", "claimed", "running", "waiting", "settling", "settled", "failed", "canceled", "expired":
		return true
	}
	return false
}

func validAttemptState(value string) bool {
	return value == "claimed" || value == "running" || value == "parked" || value == "settled" || value == "failed" || value == "recovered"
}

func validResultStatus(value string) bool {
	return value == "completed" || value == "failed" || value == "canceled" || value == "expired"
}

func validJoinState(value string) bool {
	switch value {
	case "open", "ready", "acknowledged", "failed", "expired", "detached", "canceled":
		return true
	}
	return false
}

func validReceiptKind(value string) bool {
	return value == "request" || value == "result" || value == "blocker" || value == "control"
}

func validReceiptState(value string) bool {
	return value == "pending" || value == "claimed" || value == "presented" || value == "acknowledged" || value == "abandoned"
}
