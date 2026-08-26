package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func assertNoActiveAgentCoordination(ctx context.Context, tx *sql.Tx, agentID string) error {
	var count int
	err := tx.QueryRowContext(ctx, `select
  (select count(*) from agent_operations operation where (operation.agent_id=? or operation.parent_message_id in (select id from agent_messages where sender_agent_id=? or target_agent_id=?)) and state not in ('settled','failed','canceled','expired'))+
  (select count(*) from agent_inbox_receipts receipt where (receipt.agent_id=? or receipt.message_id in (select id from agent_messages where sender_agent_id=? or target_agent_id=?)) and state not in ('acknowledged','abandoned'))+
  (select count(*) from agent_operation_joins dependency join agent_operations operation on operation.id=dependency.operation_id join agent_messages message on message.id=dependency.message_id where (operation.agent_id=? or message.sender_agent_id=? or message.target_agent_id=?) and dependency.state in ('open','ready'))+
  (select count(*) from todo_link_intents intent join agent_messages message on message.id=intent.message_id where (message.sender_agent_id=? or message.target_agent_id=?) and intent.state='pending')+
  (select count(*) from todo_settlement_events where agent_id=? and acknowledged_at=0 and state<>'failed')`, agentID, agentID, agentID, agentID, agentID, agentID, agentID, agentID, agentID, agentID, agentID, agentID).Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("agent %s has active durable coordination state", agentID)
	}
	return nil
}

func deleteAgentCoordinationState(ctx context.Context, tx *sql.Tx, agentID string) error {
	statements := []string{
		`delete from todo_settlement_events where intent_id in (select intent.id from todo_link_intents intent join agent_messages message on message.id=intent.message_id where message.sender_agent_id=? or message.target_agent_id=?)`,
		`delete from todo_link_intents where message_id in (select id from agent_messages where sender_agent_id=? or target_agent_id=?)`,
		`delete from agent_inbox_receipts where agent_id=? or message_id in (select id from agent_messages where sender_agent_id=? or target_agent_id=?)`,
		`delete from agent_operation_joins where operation_id in (select id from agent_operations where agent_id=?) or message_id in (select id from agent_messages where sender_agent_id=? or target_agent_id=?)`,
		`delete from agent_pi_local_events where agent_id=?`,
		`delete from coordination_send_receipts where message_id in (select id from agent_messages where sender_agent_id=? or target_agent_id=?)`,
		`delete from coordination_message_meta where message_id in (select id from agent_messages where sender_agent_id=? or target_agent_id=?)`,
		`delete from agent_message_results where message_id in (select id from agent_messages where sender_agent_id=? or target_agent_id=?)`,
		`delete from agent_operation_attempts where operation_id in (select id from agent_operations where agent_id=? or parent_message_id in (select id from agent_messages where sender_agent_id=? or target_agent_id=?))`,
		`delete from agent_operations where agent_id=? or parent_message_id in (select id from agent_messages where sender_agent_id=? or target_agent_id=?)`,
		`delete from agent_runtime_protocol_generations where agent_id=?`,
	}
	for _, statement := range statements {
		marks := strings.Count(statement, "?")
		args := make([]any, marks)
		for index := range args {
			args[index] = agentID
		}
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return err
		}
	}
	return nil
}

func pruneTerminalCoordinationRuns(ctx context.Context, tx *sql.Tx, cutoff int64, limit int64) (int64, error) {
	query := `select message.run_id from agent_messages message where message.run_id<>'' group by message.run_id
having max(message.updated_at)<?
  and sum(case when message.status in ('queued','delivered') then 1 else 0 end)=0
  and exists(select 1 from coordination_message_meta meta join agent_messages member on member.id=meta.message_id where member.run_id=message.run_id)
  and not exists(select 1 from agent_operations operation where operation.causal_run_id=message.run_id and operation.state not in ('settled','failed','canceled','expired'))
  and not exists(select 1 from agent_inbox_receipts receipt left join agent_messages receipt_message on receipt_message.id=receipt.message_id left join agent_operations receipt_operation on receipt_operation.id=receipt.operation_id where (receipt_message.run_id=message.run_id or receipt_operation.causal_run_id=message.run_id) and receipt.state not in ('acknowledged','abandoned'))
  and not exists(select 1 from agent_operation_joins dependency join agent_operations operation on operation.id=dependency.operation_id where operation.causal_run_id=message.run_id and dependency.state in ('open','ready'))
  and not exists(select 1 from todo_link_intents intent join agent_messages intent_message on intent_message.id=intent.message_id where intent_message.run_id=message.run_id and intent.state='pending')
  and not exists(select 1 from todo_settlement_events event join todo_link_intents intent on intent.id=event.intent_id join agent_messages event_message on event_message.id=intent.message_id where event_message.run_id=message.run_id and event.state<>'failed' and event.acknowledged_at=0)
  and not exists(select 1 from agent_pi_local_events event join agent_operations operation on operation.id=event.operation_id where operation.causal_run_id=message.run_id and event.state<>'acknowledged')
order by max(message.updated_at),message.run_id`
	args := []any{cutoff}
	if limit > 0 {
		query += ` limit ?`
		args = append(args, limit)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	var runIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		runIDs = append(runIDs, id)
	}
	if err := rows.Close(); err != nil || len(runIDs) == 0 {
		return 0, err
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(runIDs)), ",")
	values := make([]any, len(runIDs))
	for index := range runIDs {
		values[index] = runIDs[index]
	}
	messageScope := `select id from agent_messages where run_id in (` + marks + `)`
	operationScope := `select id from agent_operations where causal_run_id in (` + marks + `)`
	statements := []string{
		`delete from todo_settlement_events where intent_id in (select id from todo_link_intents where message_id in (` + messageScope + `))`,
		`delete from todo_link_intents where message_id in (` + messageScope + `)`,
		`delete from agent_inbox_receipts where message_id in (` + messageScope + `) or operation_id in (` + operationScope + `)`,
		`delete from agent_operation_joins where message_id in (` + messageScope + `) or operation_id in (` + operationScope + `)`,
		`delete from agent_pi_local_events where operation_id in (` + operationScope + `)`,
		`delete from coordination_send_receipts where message_id in (` + messageScope + `)`,
		`delete from coordination_message_meta where message_id in (` + messageScope + `)`,
		`delete from agent_message_results where message_id in (` + messageScope + `)`,
		`delete from agent_operation_attempts where operation_id in (` + operationScope + `)`,
		`delete from agent_operations where id in (` + operationScope + `)`,
	}
	for _, statement := range statements {
		copies := strings.Count(statement, marks)
		statementArgs := make([]any, 0, copies*len(values))
		for index := 0; index < copies; index++ {
			statementArgs = append(statementArgs, values...)
		}
		if _, err := tx.ExecContext(ctx, statement, statementArgs...); err != nil {
			return 0, err
		}
	}
	deleteArgs := append([]any(nil), values...)
	result, err := tx.ExecContext(ctx, `delete from agent_messages where run_id in (`+marks+`)`, deleteArgs...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func assertDeletedAgentsHaveNoActiveCoordination(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `select resource_id from deleted_items where kind='agent' order by resource_id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, strings.TrimSpace(id))
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := assertNoActiveAgentCoordination(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}
