package store

import (
	"context"
	"database/sql"
	"time"
)

// DelegatedActivityStatus is a safe aggregate of durable delegated work below
// one agent. It does not contain IDs, titles, prompts, paths, or result text.
type DelegatedActivityStatus struct {
	ActiveDelegatedAgents   int `json:"activeDelegatedAgents"`
	ActiveDelegatedRequests int `json:"activeDelegatedRequests"`
	WaitingJoinedWork       int `json:"waitingJoinedWork"`
	ActiveDelegatedWork     int `json:"activeDelegatedWork"`
}

// DelegatedActivity returns the durable contextual activity below one agent.
// The request count uses authoritative operation state when an operation exists
// and fresh legacy message state otherwise. Waiting work counts each parked
// source operation once, even when it has more than one open join.
func (s *Store) DelegatedActivity(ctx context.Context, creatorID string) (DelegatedActivityStatus, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DelegatedActivityStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixMilli()
	const lineage = `with recursive lineage(id,is_descendant) as (
  select id,0 from agents
  where id=? and not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id)
  union all
  select agent.id,1 from agents agent join lineage on agent.created_by_agent_id=lineage.id
  where not exists (select 1 from deleted_items where kind='agent' and resource_id=agent.id)
)`
	var out DelegatedActivityStatus
	if err := tx.QueryRowContext(ctx, lineage+`
select count(*) from agents agent join lineage on lineage.id=agent.id
where lineage.is_descendant=1 and agent.presentation='background' and agent.status in ('starting','running')`, creatorID).Scan(&out.ActiveDelegatedAgents); err != nil {
		return DelegatedActivityStatus{}, err
	}
	if err := tx.QueryRowContext(ctx, lineage+`
select count(distinct message.id)
from agent_messages message join lineage sender on sender.id=message.sender_agent_id
where message.kind='request'
  and message.status in ('queued','delivered')
  and not exists (select 1 from deleted_items where kind='agent' and resource_id=message.target_agent_id)
  and (
    exists (
      select 1 from agent_operations operation
      where operation.parent_message_id=message.id
        and operation.state in ('ready','claimed','running','waiting','settling')
        and (operation.deadline_at=0 or operation.deadline_at>?)
    )
    or not exists (select 1 from agent_operations operation where operation.parent_message_id=message.id)
      and (
        message.status='queued' and (message.processing_deadline_at=0 or message.processing_deadline_at>?)
        or message.status='delivered' and message.lease_expires_at>?
          and (message.processing_deadline_at=0 or message.processing_deadline_at>?)
      )
  )`, creatorID, now, now, now, now).Scan(&out.ActiveDelegatedRequests); err != nil {
		return DelegatedActivityStatus{}, err
	}
	if err := tx.QueryRowContext(ctx, lineage+`
select count(distinct operation.id)
from agent_operations operation join lineage owner on owner.id=operation.agent_id
where operation.state='waiting'
  and (operation.deadline_at=0 or operation.deadline_at>?)
  and exists (select 1 from agent_operation_joins dependency where dependency.operation_id=operation.id and dependency.state='open')`, creatorID, now).Scan(&out.WaitingJoinedWork); err != nil {
		return DelegatedActivityStatus{}, err
	}
	out.ActiveDelegatedWork = out.ActiveDelegatedAgents + out.ActiveDelegatedRequests + out.WaitingJoinedWork
	if err := tx.Commit(); err != nil {
		return DelegatedActivityStatus{}, err
	}
	return out, nil
}

// ActiveDelegatedAgentCount preserves the original status counter contract.
func (s *Store) ActiveDelegatedAgentCount(ctx context.Context, creatorID string) (int, error) {
	status, err := s.DelegatedActivity(ctx, creatorID)
	return status.ActiveDelegatedAgents, err
}
