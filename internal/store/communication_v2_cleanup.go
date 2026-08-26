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
