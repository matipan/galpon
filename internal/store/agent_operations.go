package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

const (
	AgentOperationsMaxCurrent            = 64
	AgentOperationsMaxAttention          = 32
	AgentOperationsMaxRecentResults      = 32
	AgentOperationsMaxRecentCoordination = 64
)

// AgentOperations builds a selected-agent view from the stored work and
// coordination facts. It does not read message bodies or protocol payloads.
func (s *Store) AgentOperations(ctx context.Context, agentID string) (model.AgentOperations, error) {
	agentID = strings.TrimSpace(agentID)
	agent, err := s.Agent(ctx, agentID)
	if err != nil {
		return model.AgentOperations{}, err
	}
	workspace, err := s.workspaceOperations(ctx, agent.WorkspaceID, agentID)
	if err != nil {
		return model.AgentOperations{}, err
	}

	role := ""
	if strings.TrimSpace(agent.Role) != "" {
		role = boundedWorkTitle(agent.Role)
	}
	out := model.AgentOperations{
		Version: workspace.Version,
		Agent: model.OperationsAgent{
			ID: agent.ID, Title: boundedWorkTitle(agent.Title), Role: role,
			Status: agent.Status, Presentation: agent.Presentation, UpdatedAt: agent.UpdatedAt,
		},
		Workspace: workspace.Workspace,
		Current:   []model.WorkItem{}, Attention: []model.WorkItem{}, RecentResults: []model.WorkItem{},
		RecentCoordination: []model.OperationsTimelineFact{},
		Truncation: model.AgentOperationsTruncation{
			MaxCurrent: AgentOperationsMaxCurrent, MaxAttention: AgentOperationsMaxAttention,
			MaxRecentResults: AgentOperationsMaxRecentResults, MaxRecentCoordination: AgentOperationsMaxRecentCoordination,
			SourceTruncated: workspace.Truncation.SourceTruncated || workspace.Truncation.RootsOmitted > 0 || workspace.Truncation.ItemsOmitted > 0 || workspace.Truncation.TimelineOmitted > 0,
		},
	}

	all := make([]model.WorkItem, 0)
	var visit func([]model.WorkItem)
	visit = func(items []model.WorkItem) {
		for _, stored := range items {
			item := stored
			children := item.Children
			item.Children = nil
			switch {
			case item.TargetAgentID == agentID:
				item.Direction = "received"
			case item.DelegatorAgentID == agentID:
				item.Direction = "delegated"
			default:
				visit(children)
				continue
			}
			all = append(all, item)
			visit(children)
		}
	}
	visit(workspace.Work)

	knownWork := make(map[string]bool, len(all))
	for _, item := range all {
		knownWork[item.ID] = true
		if item.Direction == "received" {
			out.Summary.Received++
		} else {
			out.Summary.Delegated++
		}
		if agentOperationsIsCurrent(item) {
			out.Current = append(out.Current, item)
		}
		if agentOperationsNeedsAttention(item) {
			out.Attention = append(out.Attention, item)
		}
		if agentOperationsHasResult(item) {
			out.RecentResults = append(out.RecentResults, item)
		}
		if item.Observation.State == "failed" || item.Observation.State == "canceled" || item.Observation.State == "expired" {
			out.Summary.Failures++
		}
	}
	out.Summary.Current = len(out.Current)
	out.Summary.NeedsAttention = len(out.Attention)
	out.Summary.Results = len(out.RecentResults)

	sortAgentOperationsCurrent(out.Current)
	sortAgentOperationsAttention(out.Attention)
	sortAgentOperationsRecent(out.RecentResults)
	out.Current, out.Truncation.CurrentOmitted = boundedAgentOperationsItems(out.Current, AgentOperationsMaxCurrent)
	out.Attention, out.Truncation.AttentionOmitted = boundedAgentOperationsItems(out.Attention, AgentOperationsMaxAttention)
	out.RecentResults, out.Truncation.RecentResultsOmitted = boundedAgentOperationsItems(out.RecentResults, AgentOperationsMaxRecentResults)

	for _, fact := range workspace.Timeline {
		if knownWork[fact.WorkID] {
			out.RecentCoordination = append(out.RecentCoordination, fact)
		}
	}
	if len(out.RecentCoordination) > AgentOperationsMaxRecentCoordination {
		out.Truncation.RecentCoordinationOmitted = len(out.RecentCoordination) - AgentOperationsMaxRecentCoordination
		out.RecentCoordination = out.RecentCoordination[:AgentOperationsMaxRecentCoordination]
	}

	now := time.Now().UnixMilli()
	cutoff := now - WorkSettledVisibility.Milliseconds()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return model.AgentOperations{}, err
	}
	defer func() { _ = tx.Rollback() }()
	v2, err := communicationV2ProjectionEnabled(ctx, tx)
	if err == nil {
		err = scanAgentOperationsQueue(ctx, tx, agentID, now, v2, &out.Queue)
	}
	if err == nil && v2 {
		out.DirectOperations, err = loadDirectOperationFacts(ctx, tx, "agent", agentID, now, cutoff)
	}
	currentIDs := map[string]string{}
	for _, item := range out.Current {
		if item.Direction == "received" && item.Observation.State == "started" && item.Observation.Lease == "fresh" {
			currentIDs[agentID] = item.ID
			break
		}
	}
	if err == nil {
		out.Activity, err = operationsActivityLane(ctx, tx, currentIDs, v2)
	}
	if err != nil {
		return model.AgentOperations{}, err
	}
	if len(currentIDs) == 0 {
		out.Activity = nil
	}
	if err := tx.Commit(); err != nil {
		return model.AgentOperations{}, err
	}

	for _, item := range out.Current {
		if item.Direction != "received" {
			continue
		}
		delivery := operationsDeliveryFromItem(item)
		if out.Agent.CurrentDelivery == nil && item.Observation.State == "started" && item.Observation.Lease == "fresh" {
			out.Agent.CurrentDelivery = &delivery
		}
		if out.Agent.ObservedDelivery == nil {
			out.Agent.ObservedDelivery = &delivery
		}
	}
	if out.Agent.ObservedDelivery == nil {
		for _, item := range append(append([]model.WorkItem{}, out.Attention...), out.RecentResults...) {
			if item.Direction == "received" {
				delivery := operationsDeliveryFromItem(item)
				out.Agent.ObservedDelivery = &delivery
				break
			}
		}
	}
	out.Truncation.Truncated = out.Truncation.SourceTruncated || out.Truncation.CurrentOmitted > 0 || out.Truncation.AttentionOmitted > 0 || out.Truncation.RecentResultsOmitted > 0 || out.Truncation.RecentCoordinationOmitted > 0
	return out, nil
}

func scanAgentOperationsQueue(ctx context.Context, tx *sql.Tx, agentID string, now int64, v2 bool, out *model.OperationsQueue) error {
	if v2 {
		return tx.QueryRowContext(ctx, `select
			coalesce(sum(case when kind='request' and state='pending' and eligible=1 then 1 else 0 end),0),
			coalesce(sum(case when kind='request' and state in ('claimed','presented') then 1 else 0 end),0),
			coalesce(sum(case when kind='request' and state in ('claimed','presented') and lease_expires_at>? then 1 else 0 end),0),
			coalesce(sum(case when kind in ('result','blocker') and state='pending' and eligible=1 then 1 else 0 end),0),
			coalesce(sum(case when kind in ('result','blocker') and state='pending' then 1 else 0 end),0),
			coalesce(sum(case when kind in ('result','blocker') and state in ('claimed','presented') then 1 else 0 end),0),
			coalesce(sum(case when state='claimed' then 1 else 0 end),0),
			coalesce(sum(case when state='presented' then 1 else 0 end),0),
			coalesce(sum(case when state='acknowledged' then 1 else 0 end),0)
			from agent_inbox_receipts where agent_id=?`, now, agentID).
			Scan(&out.InboundQueued, &out.InboundClaimed, &out.InboundClaimedFresh, &out.ResultsReady, &out.ResultDeliveries, &out.ResultClaims, &out.ReceiptsClaimed, &out.ReceiptsPresented, &out.ReceiptsAcknowledged)
	}
	if err := tx.QueryRowContext(ctx, `select
		coalesce(sum(case when status='queued' and (kind='request' or notification_state='pending') then 1 else 0 end),0),
		coalesce(sum(case when status='delivered' then 1 else 0 end),0),
		coalesce(sum(case when status='delivered' and lease_expires_at>? and (processing_deadline_at=0 or processing_deadline_at>?) then 1 else 0 end),0),
		coalesce(sum(case when kind='result' and status='queued' and notification_state='pending' then 1 else 0 end),0),
		coalesce(sum(case when kind='result' and status='delivered' then 1 else 0 end),0)
		from agent_messages where target_agent_id=?`, now, now, agentID).
		Scan(&out.InboundQueued, &out.InboundClaimed, &out.InboundClaimedFresh, &out.ResultDeliveries, &out.ResultClaims); err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, `select count(*) from lifecycle_events where recipient_agent_id=? and event_type='message.result' and status='pending'`, agentID).Scan(&out.ResultsReady)
}

func operationsDeliveryFromItem(item model.WorkItem) model.OperationsDelivery {
	return model.OperationsDelivery{WorkID: item.ID, Title: item.Title, Observation: item.Observation, Checkpoint: item.Checkpoint, UpdatedAt: item.UpdatedAt}
}

func boundedAgentOperationsItems(items []model.WorkItem, limit int) ([]model.WorkItem, int) {
	if len(items) <= limit {
		return items, 0
	}
	return items[:limit], len(items) - limit
}

func agentOperationsIsCurrent(item model.WorkItem) bool {
	return (item.Observation.State == "started" && item.Observation.Lease != "stale") || item.Observation.State == "waiting" || item.Observation.State == "queued"
}

func agentOperationsNeedsAttention(item model.WorkItem) bool {
	return item.Priority == "reported_blocker" || item.Priority == "stale_observation" || item.Priority == "recent_failure" || item.Observation.State == "failed" || item.Observation.State == "canceled" || item.Observation.State == "expired"
}

func agentOperationsHasResult(item model.WorkItem) bool {
	if item.Result != nil {
		return true
	}
	switch item.Observation.State {
	case "completed", "failed", "canceled", "expired":
		return true
	default:
		return false
	}
}

func sortAgentOperationsCurrent(items []model.WorkItem) {
	rank := func(item model.WorkItem) int {
		switch item.Observation.State {
		case "started":
			return 0
		case "waiting":
			return 1
		default:
			return 2
		}
	}
	slices.SortStableFunc(items, func(left, right model.WorkItem) int {
		if order := rank(left) - rank(right); order != 0 {
			return order
		}
		return compareAgentOperationsRecent(left, right)
	})
}

func sortAgentOperationsAttention(items []model.WorkItem) {
	rank := func(item model.WorkItem) int {
		switch item.Priority {
		case "reported_blocker":
			return 0
		case "recent_failure":
			return 1
		default:
			return 2
		}
	}
	slices.SortStableFunc(items, func(left, right model.WorkItem) int {
		if order := rank(left) - rank(right); order != 0 {
			return order
		}
		return compareAgentOperationsRecent(left, right)
	})
}

func sortAgentOperationsRecent(items []model.WorkItem) {
	slices.SortStableFunc(items, compareAgentOperationsRecent)
}

func compareAgentOperationsRecent(left, right model.WorkItem) int {
	if left.UpdatedAt > right.UpdatedAt {
		return -1
	}
	if left.UpdatedAt < right.UpdatedAt {
		return 1
	}
	return strings.Compare(left.ID, right.ID)
}
