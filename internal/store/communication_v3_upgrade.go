package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CurrentCommunicationProtocolGeneration is the protocol generation used by
// new runtimes. Generation 2 remains a valid explicit migration target for
// legacy fixtures and recovery tools.
const CurrentCommunicationProtocolGeneration = 3

// CommunicationV3RuntimeRecoveryOptions records the two external facts that
// the store cannot prove: a verified pre-migration backup exists and process
// inspection found no live agent runtime for this store.
type CommunicationV3RuntimeRecoveryOptions struct {
	Generation       int
	BackupVerified   bool
	ProcessesStopped bool
}

// BeginCommunicationV3Upgrade starts or resumes the generation-3 drain. It is
// the only transition that can reopen a completed generation-2 protocol row.
// An upgrade for another generation remains fenced for operator recovery.
func (s *Store) BeginCommunicationV3Upgrade(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, pending, complete, maintenance, err := protocolStateDetails(ctx, tx)
	if err != nil {
		return err
	}
	var draining int
	var writer string
	if err := tx.QueryRowContext(ctx, `select draining,maintenance_writer from communication_protocol_state where singleton=1`).Scan(&draining, &writer); err != nil {
		return err
	}
	if current > CurrentCommunicationProtocolGeneration {
		return fmt.Errorf("communication protocol generation %d is newer than supported generation %d", current, CurrentCommunicationProtocolGeneration)
	}
	if current == CurrentCommunicationProtocolGeneration {
		if complete && !maintenance && draining == 0 {
			return tx.Commit()
		}
		if pending != CurrentCommunicationProtocolGeneration {
			return fmt.Errorf("communication generation %d is already upgrading", pending)
		}
		return tx.Commit()
	}
	if pending != 0 && pending != CurrentCommunicationProtocolGeneration {
		return fmt.Errorf("communication generation %d is already upgrading", pending)
	}
	if maintenance || writer != "" {
		return fmt.Errorf("communication generation %d has an interrupted maintenance phase", pending)
	}
	if current < 1 || current > 2 {
		return fmt.Errorf("communication protocol generation %d cannot upgrade to generation %d", current, CurrentCommunicationProtocolGeneration)
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update communication_protocol_state
set cutover_complete=0,maintenance=0,draining=1,recovery_pending=0,
    pending_generation=?,maintenance_writer='',updated_at=?
where singleton=1 and generation=? and maintenance=0 and maintenance_writer=''`, CurrentCommunicationProtocolGeneration, now, current)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return err
		}
		return fmt.Errorf("communication generation %d drain could not start", CurrentCommunicationProtocolGeneration)
	}
	return tx.Commit()
}

// RecoverStoppedCommunicationRuntimes releases ownership held by runtimes that
// process inspection proved are stopped. It keeps IDs, terminal messages,
// immutable results, queued work, TODO records, and event history unchanged.
// The recovery audit permanently fences each old runtime identity.
func (s *Store) RecoverStoppedCommunicationRuntimes(ctx context.Context, options CommunicationV3RuntimeRecoveryOptions) (int, error) {
	if options.Generation != CurrentCommunicationProtocolGeneration || !options.BackupVerified || !options.ProcessesStopped {
		return 0, fmt.Errorf("stopped runtime recovery needs generation %d, a verified backup, and confirmed stopped processes", CurrentCommunicationProtocolGeneration)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	current, pending, complete, maintenance, err := protocolStateDetails(ctx, tx)
	if err != nil {
		return 0, err
	}
	if !maintenance || pending != options.Generation || complete && current != options.Generation {
		return 0, fmt.Errorf("stopped runtime recovery requires generation %d maintenance", options.Generation)
	}
	var writer string
	if err := tx.QueryRowContext(ctx, `select maintenance_writer from communication_protocol_state where singleton=1`).Scan(&writer); err != nil {
		return 0, err
	}
	if writer != "" {
		return 0, fmt.Errorf("communication maintenance has another internal writer")
	}
	writer = "v3_runtime_recovery"
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `update communication_protocol_state set maintenance_writer=?,updated_at=? where singleton=1 and maintenance=1 and maintenance_writer=''`, writer, now); err != nil {
		return 0, err
	}

	type runtime struct {
		agentID, runtimeID                      string
		deliveries, operations, receipts, links int
		settlements                             int
	}
	rows, err := tx.QueryContext(ctx, `select agent_id,runtime_id from (
  select id as agent_id,runtime_id from agents where runtime_id<>''
  union select target_agent_id,runtime_id from agent_messages where runtime_id<>'' and status='delivered'
  union select agent_id,runtime_id from agent_operations where runtime_id<>'' and state in ('claimed','running','settling')
  union select agent_id,runtime_id from agent_inbox_receipts where runtime_id<>'' and state in ('claimed','presented')
  union select operation.agent_id,intent.runtime_id from todo_link_intents intent join agent_operations operation on operation.id=intent.operation_id where intent.runtime_id<>'' and intent.state='pending'
  union select message.sender_agent_id,intent.runtime_id from todo_link_intents intent join agent_messages message on message.id=intent.message_id join agents on agents.id=message.sender_agent_id where intent.operation_id is null and intent.runtime_id<>'' and intent.state='pending'
  union select agent_id,runtime_id from todo_settlement_events where runtime_id<>'' and state in ('pending','applied') and acknowledged_at=0
) order by agent_id,runtime_id`)
	if err != nil {
		return 0, err
	}
	var runtimes []runtime
	for rows.Next() {
		var value runtime
		if err := rows.Scan(&value.agentID, &value.runtimeID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		runtimes = append(runtimes, value)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for index := range runtimes {
		value := &runtimes[index]
		queries := []struct {
			query string
			out   *int
			args  []any
		}{
			{`select count(*) from agent_messages where target_agent_id=? and runtime_id=? and status='delivered'`, &value.deliveries, []any{value.agentID, value.runtimeID}},
			{`select count(*) from agent_operations where agent_id=? and runtime_id=? and state in ('claimed','running','settling')`, &value.operations, []any{value.agentID, value.runtimeID}},
			{`select count(*) from agent_inbox_receipts where agent_id=? and runtime_id=? and state in ('claimed','presented')`, &value.receipts, []any{value.agentID, value.runtimeID}},
			{`select count(*) from todo_link_intents where runtime_id=? and state='pending' and (exists(select 1 from agent_operations operation where operation.id=todo_link_intents.operation_id and operation.agent_id=?) or operation_id is null and exists(select 1 from agent_messages message where message.id=todo_link_intents.message_id and message.sender_agent_id=?))`, &value.links, []any{value.runtimeID, value.agentID, value.agentID}},
			{`select count(*) from todo_settlement_events where agent_id=? and runtime_id=? and state in ('pending','applied') and acknowledged_at=0`, &value.settlements, []any{value.agentID, value.runtimeID}},
		}
		for _, query := range queries {
			if err := tx.QueryRowContext(ctx, query.query, query.args...).Scan(query.out); err != nil {
				return 0, err
			}
		}
		if _, err := tx.ExecContext(ctx, `insert or ignore into communication_runtime_recoveries(agent_id,runtime_id,generation,deliveries,operations,receipts,todo_links,todo_settlements,recovered_at) values(?,?,?,?,?,?,?,?,?)`, value.agentID, value.runtimeID, options.Generation, value.deliveries, value.operations, value.receipts, value.links, value.settlements, now); err != nil {
			return 0, err
		}
	}

	if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state='recovered',terminal_reason='generation_3_upgrade',finished_at=?,updated_at=? where state in ('claimed','running')`, now, now); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update agent_operations set state='ready',runtime_id='',claim_key='',lease_expires_at=0,last_error='recovered stopped runtime during generation 3 upgrade',updated_at=? where state in ('claimed','running','settling')`, now); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update agent_messages set status='queued',notification_state=case when kind='result' then 'pending' else notification_state end,terminal_reason='',runtime_id='',claim_key='',lease_expires_at=0,last_error='recovered stopped runtime during generation 3 upgrade',updated_at=? where status='delivered'`, now); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='pending',runtime_id='',claim_key='',pi_tool_request_id='',lease_expires_at=0,operation_attempt=0,updated_at=? where state in ('claimed','presented')`, now); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update todo_link_intents set runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,last_error='recovered stopped runtime during generation 3 upgrade' where state='pending' and runtime_id<>''`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update todo_settlement_events set runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,last_error='recovered stopped runtime during generation 3 upgrade' where state in ('pending','applied') and acknowledged_at=0 and runtime_id<>''`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `delete from agent_runtime_protocol_generations`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `delete from agent_runtime_launches`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update agents set status='stopped',runtime_id='',last_error='',updated_at=? where runtime_id<>''`, now); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update communication_protocol_state set maintenance_writer='',updated_at=? where singleton=1 and maintenance_writer=?`, now, writer); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(runtimes), nil
}

// UpgradeCommunicationV3 moves all generation-owned coordination records to
// generation 3 in one transaction. Completed result payloads and all durable
// identities are not rewritten. A legacy generation-1 database uses the
// existing semantic backfill with generation 3 as its explicit target.
func (s *Store) UpgradeCommunicationV3(ctx context.Context, options CommunicationCutoverOptions) (CommunicationCutoverResult, error) {
	out := CommunicationCutoverResult{Generation: options.Generation}
	if options.Generation != CurrentCommunicationProtocolGeneration || !options.MaintenanceConfirmed || !options.BackupVerified || !options.SafeIdleConfirmed {
		return out, fmt.Errorf("generation %d upgrade needs maintenance mode, a verified backup, and a safe idle point", CurrentCommunicationProtocolGeneration)
	}
	current, _, _, err := s.CommunicationProtocolState(ctx)
	if err != nil {
		return out, err
	}
	if current < 2 {
		if _, err := s.BackfillCommunicationV2(ctx, options); err != nil {
			return out, err
		}
		// The v2 backfill creates TODO-gated request receipts. Generation 3
		// dispatches the request independently, so finish that bounded change
		// through the same complete-maintenance retry path below.
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
		if current != options.Generation {
			return out, fmt.Errorf("communication protocol generation %d is already active", current)
		}
		if maintenance {
			result, err := tx.ExecContext(ctx, `update communication_protocol_state set maintenance_writer='v3_upgrade',updated_at=? where singleton=1 and generation=? and cutover_complete=1 and maintenance=1 and maintenance_writer=''`, time.Now().UnixMilli(), options.Generation)
			if err != nil {
				return out, err
			}
			if count, err := result.RowsAffected(); err != nil || count != 1 {
				if err != nil {
					return out, err
				}
				return out, fmt.Errorf("communication maintenance has another internal writer")
			}
			if err := makeUnappliedTodoRequestReceiptsEligible(ctx, tx, time.Now().UnixMilli()); err != nil {
				return out, err
			}
			if _, err := tx.ExecContext(ctx, `update communication_protocol_state set maintenance_writer='',updated_at=? where singleton=1 and maintenance_writer='v3_upgrade'`, time.Now().UnixMilli()); err != nil {
				return out, err
			}
		}
		out, err := communicationV3CutoverCounts(ctx, tx, options.Generation)
		if err != nil {
			return out, err
		}
		return out, tx.Commit()
	}
	if current != 2 || pending != options.Generation || !maintenance {
		return out, fmt.Errorf("generation %d upgrade requires a completed generation-2 source in maintenance", options.Generation)
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update communication_protocol_state set maintenance_writer='v3_upgrade',updated_at=? where singleton=1 and maintenance=1 and pending_generation=? and maintenance_writer=''`, now, options.Generation)
	if err != nil {
		return out, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return out, err
		}
		return out, fmt.Errorf("communication maintenance has another internal writer")
	}
	for _, table := range []string{
		"agent_operations",
		// agent_message_results is deliberately absent. Its trigger makes the
		// full row immutable, including the source protocol generation.
		"agent_inbox_receipts",
		"agent_operation_joins",
		"agent_pi_local_events",
		"todo_link_intents",
		"todo_settlement_events",
	} {
		if _, err := tx.ExecContext(ctx, `update `+table+` set protocol_generation=? where protocol_generation<>?`, options.Generation, options.Generation); err != nil {
			return out, fmt.Errorf("upgrade %s protocol generation: %w", table, err)
		}
	}
	if err := makeUnappliedTodoRequestReceiptsEligible(ctx, tx, now); err != nil {
		return out, err
	}
	if err := rejectStoredOperationCycles(ctx, tx); err != nil {
		return out, err
	}
	var foreignKeyViolation string
	err = tx.QueryRowContext(ctx, `select coalesce((select "table"||':'||rowid||':'||parent from pragma_foreign_key_check limit 1),'')`).Scan(&foreignKeyViolation)
	if err != nil {
		return out, err
	}
	if foreignKeyViolation != "" {
		return out, fmt.Errorf("generation %d upgrade foreign key check failed: %s", options.Generation, foreignKeyViolation)
	}
	result, err = tx.ExecContext(ctx, `update communication_protocol_state set generation=?,cutover_complete=1,maintenance=1,draining=0,pending_generation=?,maintenance_writer='',updated_at=? where singleton=1 and generation=2 and cutover_complete=0 and maintenance=1 and pending_generation=? and maintenance_writer='v3_upgrade'`, options.Generation, options.Generation, now, options.Generation)
	if err != nil {
		return out, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return out, err
		}
		return out, fmt.Errorf("generation %d cutover state changed during upgrade", options.Generation)
	}
	out, err = communicationV3CutoverCounts(ctx, tx, options.Generation)
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

func makeUnappliedTodoRequestReceiptsEligible(ctx context.Context, tx *sql.Tx, now int64) error {
	_, err := tx.ExecContext(ctx, `update agent_inbox_receipts set eligible=1,updated_at=?
where kind='request' and state='pending' and eligible=0 and exists (
  select 1 from todo_link_intents intent where intent.message_id=agent_inbox_receipts.message_id and intent.state='pending'
)`, now)
	return err
}

func communicationV3CutoverCounts(ctx context.Context, tx interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, generation int) (CommunicationCutoverResult, error) {
	out := CommunicationCutoverResult{Generation: generation}
	queries := []struct {
		query string
		out   *int
	}{
		{`select count(*) from coordination_message_meta`, &out.Messages},
		{`select count(*) from agent_operations where protocol_generation=?`, &out.Operations},
		{`select count(*) from agent_message_results`, &out.Results},
		{`select count(*) from agent_inbox_receipts where protocol_generation=?`, &out.Receipts},
		{`select count(*) from agent_operation_joins where protocol_generation=?`, &out.Joins},
		{`select count(*) from todo_link_intents where protocol_generation=?`, &out.TodoLinks},
	}
	for _, item := range queries {
		if item.query == `select count(*) from coordination_message_meta` || item.query == `select count(*) from agent_message_results` {
			if err := tx.QueryRowContext(ctx, item.query).Scan(item.out); err != nil {
				return out, err
			}
			continue
		}
		if err := tx.QueryRowContext(ctx, item.query, generation).Scan(item.out); err != nil {
			return out, err
		}
	}
	return out, nil
}
