package store

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/matipan/galpon/internal/model"
)

const (
	OperationsMaxAgents   = 128
	OperationsMaxRoots    = 64
	OperationsMaxItems    = 256
	OperationsMaxTimeline = 128
)

type operationsRoot struct {
	item model.WorkItem
	rank int
}

// WorkspaceOperations projects one bounded, read-only workspace cockpit from
// current durable facts. It does not read prompts, conversation content, paths,
// session IDs, runtime IDs, or runtime tool data.
func (s *Store) WorkspaceOperations(ctx context.Context, workspaceID string) (model.WorkspaceOperations, error) {
	dashboard, err := s.Dashboard(ctx)
	if err != nil {
		return model.WorkspaceOperations{}, err
	}
	workspace, ok := dashboard.Workspace(strings.TrimSpace(workspaceID))
	if !ok {
		return model.WorkspaceOperations{}, fmt.Errorf("workspace not found")
	}
	out := model.WorkspaceOperations{
		Version:   1,
		Workspace: model.OperationsWorkspace{ID: workspace.ID, Title: boundedWorkTitle(workspace.Title)},
		Agents:    []model.OperationsAgent{},
		Work:      []model.WorkItem{},
		Timeline:  []model.OperationsTimelineFact{},
		Truncation: model.OperationsTruncation{
			MaxAgents: OperationsMaxAgents, MaxRoots: OperationsMaxRoots,
			MaxItems: OperationsMaxItems, MaxTimeline: OperationsMaxTimeline,
		},
	}

	agents := make([]model.Agent, 0)
	for _, agent := range dashboard.Agents {
		if agent.WorkspaceID == workspace.ID {
			agents = append(agents, agent)
		}
	}
	out.Summary.Agents = len(agents)
	slices.SortStableFunc(agents, func(left, right model.Agent) int {
		leftRank, rightRank := operationsAgentRank(left.Status), operationsAgentRank(right.Status)
		if leftRank != rightRank {
			return leftRank - rightRank
		}
		if left.UpdatedAt != right.UpdatedAt {
			if left.UpdatedAt > right.UpdatedAt {
				return -1
			}
			return 1
		}
		if value := strings.Compare(strings.ToLower(left.Title), strings.ToLower(right.Title)); value != 0 {
			return value
		}
		return strings.Compare(left.ID, right.ID)
	})
	if len(agents) > OperationsMaxAgents {
		out.Truncation.AgentsOmitted = len(agents) - OperationsMaxAgents
		agents = agents[:OperationsMaxAgents]
	}
	for _, agent := range agents {
		if agent.Status == "running" || agent.Status == "starting" {
			out.Summary.ActiveAgents++
		}
		role := ""
		if strings.TrimSpace(agent.Role) != "" {
			role = boundedWorkTitle(agent.Role)
		}
		out.Agents = append(out.Agents, model.OperationsAgent{
			ID: agent.ID, Title: boundedWorkTitle(agent.Title), Role: role,
			Status: agent.Status, Presentation: agent.Presentation, UpdatedAt: agent.UpdatedAt,
		})
	}

	candidates := make([]operationsRoot, 0)
	sourceTruncated := false
	for _, agent := range agents {
		projection, projectionErr := s.AgentWork(ctx, agent.ID, false)
		if projectionErr != nil {
			return model.WorkspaceOperations{}, projectionErr
		}
		sourceTruncated = sourceTruncated || projection.Truncated
		for _, item := range projection.Items {
			item.DelegatorTitle = boundedWorkTitle(agent.Title)
			candidates = append(candidates, operationsRoot{item: item})
		}
	}

	// AgentWork intentionally gives each delegator a useful local tree. A
	// workspace view removes roots that already occur below another root.
	contained := make(map[string]bool)
	rootSeen := make(map[string]bool)
	for _, candidate := range candidates {
		markOperationDescendants(candidate.item.Children, contained)
	}
	unique := make([]operationsRoot, 0, len(candidates))
	for _, candidate := range candidates {
		if contained[candidate.item.ID] || rootSeen[candidate.item.ID] {
			continue
		}
		rootSeen[candidate.item.ID] = true
		candidate.rank = classifyOperationsTree(&candidate.item)
		unique = append(unique, candidate)
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
	if len(unique) > OperationsMaxRoots {
		out.Truncation.RootsOmitted = len(unique) - OperationsMaxRoots
		unique = unique[:OperationsMaxRoots]
	}
	remaining := OperationsMaxItems
	for _, root := range unique {
		if remaining == 0 {
			out.Truncation.RootsOmitted++
			continue
		}
		item, used := copyBoundedOperationsTree(root.item, &remaining)
		if used {
			out.Work = append(out.Work, item)
		}
	}
	returnedItems := 0
	for _, root := range out.Work {
		returnedItems += countOperationsItems(root)
	}
	out.Truncation.ItemsOmitted = max(0, totalItems-returnedItems)
	out.Truncation.SourceTruncated = sourceTruncated

	current := make(map[string]model.OperationsDelivery)
	for _, root := range unique {
		collectCurrentDeliveries(root.item, current)
	}
	for index := range out.Agents {
		if delivery, found := current[out.Agents[index].ID]; found {
			value := delivery
			out.Agents[index].CurrentDelivery = &value
		}
	}

	facts := make([]model.OperationsTimelineFact, 0)
	for _, root := range out.Work {
		collectOperationsTimeline(root, &facts)
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
	out.Timeline = facts
	out.Truncation.Truncated = out.Truncation.AgentsOmitted > 0 || out.Truncation.RootsOmitted > 0 ||
		out.Truncation.ItemsOmitted > 0 || out.Truncation.TimelineOmitted > 0 || sourceTruncated
	return out, nil
}

func operationsAgentRank(status string) int {
	switch status {
	case "running":
		return 0
	case "starting":
		return 1
	case "failed":
		return 2
	case "idle":
		return 3
	default:
		return 4
	}
}

func markOperationDescendants(items []model.WorkItem, contained map[string]bool) {
	for _, item := range items {
		contained[item.ID] = true
		markOperationDescendants(item.Children, contained)
	}
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
	if item.Observation.State == "started" && item.Observation.Lease != "stale" {
		return 1
	}
	if item.Observation.State == "queued" {
		return 2
	}
	if item.Observation.Lease == "stale" {
		return 3
	}
	if item.Observation.State == "failed" || item.Observation.State == "canceled" || item.Observation.State == "expired" {
		return 4
	}
	return 5
}

func operationsPriorityLabel(rank int) string {
	return []string{"reported_blocker", "active", "queued", "stale_observation", "recent_failure", "recent_completion"}[min(max(rank, 0), 5)]
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
	*remaining = *remaining - 1
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
	case item.Observation.State == "started" && item.Observation.Lease != "stale":
		summary.ActiveWork++
	case item.Observation.State == "queued":
		summary.QueuedWork++
	case item.Observation.Lease == "stale":
		summary.StaleObservations++
	case item.Observation.State == "failed" || item.Observation.State == "canceled" || item.Observation.State == "expired":
		summary.RecentFailures++
	case item.Observation.State == "completed":
		summary.RecentCompletions++
	}
	for _, child := range item.Children {
		collectOperationsSummary(child, summary)
	}
}

func collectCurrentDeliveries(item model.WorkItem, current map[string]model.OperationsDelivery) {
	if item.Active() && item.TargetAgentID != "" {
		candidate := model.OperationsDelivery{WorkID: item.ID, Title: item.Title, Observation: item.Observation, Checkpoint: item.Checkpoint}
		existing, found := current[item.TargetAgentID]
		if !found || candidate.Observation.ObservedAt > existing.Observation.ObservedAt ||
			(candidate.Observation.ObservedAt == existing.Observation.ObservedAt && candidate.WorkID < existing.WorkID) {
			current[item.TargetAgentID] = candidate
		}
	}
	for _, child := range item.Children {
		collectCurrentDeliveries(child, current)
	}
}

func collectOperationsTimeline(item model.WorkItem, facts *[]model.OperationsTimelineFact) {
	for _, event := range item.Timeline {
		*facts = append(*facts, model.OperationsTimelineFact{
			WorkID: item.ID, WorkTitle: item.Title, TargetTitle: item.TargetTitle,
			Kind: event.Kind, Label: event.Label, Source: event.Source, CreatedAt: event.CreatedAt,
		})
	}
	for _, child := range item.Children {
		collectOperationsTimeline(child, facts)
	}
}
