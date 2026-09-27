package store

import (
	"context"
	"fmt"
	"strings"
)

// CoordinationChangeMark changes after row changes on the daemon connection
// and after commits from another process. It does not read coordination tables.
func (s *Store) CoordinationChangeMark(ctx context.Context) (string, error) {
	var changes, version int64
	err := s.db.QueryRowContext(ctx, `select total_changes(), (select data_version from pragma_data_version())`).Scan(&changes, &version)
	return fmt.Sprintf("%d:%d", changes, version), err
}

// CoordinationWakeFingerprints summarizes the durable state that can make an
// idle runtime's claim succeed. A changed agent value means that the runtime
// should run its normal claim sequence. The values are not claim decisions:
// extra changes cause one empty claim, and the runtime keeps a periodic claim.
func (s *Store) CoordinationWakeFingerprints(ctx context.Context, now int64) (string, map[string]string, error) {
	generation, complete, maintenance, err := protocolState(ctx, s.db)
	if err != nil {
		return "", nil, err
	}
	var expired bool
	// Lease expiry is time-based. Report it so waiting runtimes run recovery.
	if err := s.db.QueryRowContext(ctx, `select
exists(select 1 from agent_operations where state in ('claimed','running') and lease_expires_at>0 and lease_expires_at<=?)
or exists(select 1 from agent_inbox_receipts where state in ('claimed','presented') and lease_expires_at>0 and lease_expires_at<=?)
or exists(select 1 from todo_link_intents where state='pending' and runtime_id<>'' and lease_expires_at>0 and lease_expires_at<=?)
or exists(select 1 from todo_settlement_events where state in ('pending','applied') and acknowledged_at=0 and runtime_id<>'' and lease_expires_at>0 and lease_expires_at<=?)`,
		now, now, now, now).Scan(&expired); err != nil {
		return "", nil, err
	}
	global := fmt.Sprintf("p%d:%t:%t:e%t", generation, complete, maintenance, expired)
	rows, err := s.db.QueryContext(ctx, `select 'a', id, runtime_id, 0, 0 from agents where runtime_id<>''
union all
select 'o', agent_id, '', count(*), coalesce(max(updated_at),0) from agent_operations where state='ready' group by agent_id
union all
select 'r', agent_id, '', count(*), coalesce(max(updated_at),0) from agent_inbox_receipts where state='pending' and eligible=1 and operation_id is null group by agent_id
union all
select 's', event.agent_id, '', count(*), coalesce(max(event.created_at),0)+coalesce(sum(event.attempt),0) from todo_settlement_events event where event.state in ('pending','applied') and event.acknowledged_at=0 and event.runtime_id='' and exists(select 1 from todo_link_intents intent where intent.id=event.intent_id and intent.state='applied') group by event.agent_id
union all
select 'i', message.sender_agent_id, '', count(*), coalesce(max(intent.created_at),0) from todo_link_intents intent join agent_messages message on message.id=intent.message_id where intent.state='pending' and intent.runtime_id='' and message.sender_agent_id<>'' group by message.sender_agent_id`)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = rows.Close() }()
	parts := map[string]*strings.Builder{}
	for rows.Next() {
		var kind, agentID, text string
		var count, marker int64
		if err := rows.Scan(&kind, &agentID, &text, &count, &marker); err != nil {
			return "", nil, err
		}
		part := parts[agentID]
		if part == nil {
			part = &strings.Builder{}
			parts[agentID] = part
		}
		fmt.Fprintf(part, "%s%s:%d:%d;", kind, text, count, marker)
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	agents := make(map[string]string, len(parts))
	for agentID, part := range parts {
		agents[agentID] = part.String()
	}
	return global, agents, nil
}
