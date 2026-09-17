package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/model"
)

const MaxPlanBytes = 48 << 10

type PlanHandoff struct {
	SourceAgentID string `json:"sourceAgentId"`
	RevisionID    string `json:"revisionId"`
	Plan          string `json:"plan"`
}

type CreatePlanAgentRequest struct {
	PlanHandoff
	Agent CreateAgentRequest `json:"agent"`
}

type PlanAgentResult struct {
	AgentID string `json:"agentId"`
}

func (p PlanHandoff) Validate() error {
	if _, err := uuid.Parse(p.SourceAgentID); err != nil {
		return fmt.Errorf("plan source agent ID is invalid")
	}
	if _, err := uuid.Parse(p.RevisionID); err != nil {
		return fmt.Errorf("plan revision ID is invalid")
	}
	if !utf8.ValidString(p.Plan) || strings.TrimSpace(p.Plan) == "" || len(p.Plan) > MaxPlanBytes || strings.Count(p.Plan, "\n") >= 2048 {
		return fmt.Errorf("plan must contain at most 48 KiB and 2048 lines of Markdown")
	}
	for _, r := range p.Plan {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return fmt.Errorf("plan contains terminal control characters")
		}
	}
	return nil
}

func (p PlanHandoff) Prompt() string {
	return "The user approved this plan for implementation. Plan mode is off. Implement the plan in this agent's placement. Read its AGENTS.md instructions first. Use repository-relative paths; do not edit the planning agent's worktrees. Do not start other agents or contact them unless the user explicitly requested that work. Report progress and the final result to the user here.\n\nSource agent: " + p.SourceAgentID + "\nPlan revision: " + p.RevisionID + "\n\n" + p.Plan + "\n\n[End of approved plan]"
}

// CreatePlanAgent is a user action, not an agent tool. Reserve the durable
// identity before file placement, commit its creation marker with the agent,
// then queue a direct user prompt before opening the foreground view.
func (a *App) CreatePlanAgent(ctx context.Context, request CreatePlanAgentRequest) (PlanAgentResult, error) {
	if err := request.Validate(); err != nil {
		return PlanAgentResult{}, err
	}
	finish, err := a.beginCommunicationMutation(ctx)
	if err != nil {
		return PlanAgentResult{}, err
	}
	defer finish()
	if _, err := a.Store.Agent(ctx, request.SourceAgentID); err != nil {
		return PlanAgentResult{}, fmt.Errorf("plan source agent is not available: %w", err)
	}
	if request.Agent.ContextAgentID != "" {
		return PlanAgentResult{}, fmt.Errorf("a plan implementation starts with a fresh conversation")
	}
	agent, err := a.createPlanAgent(ctx, request)
	if err != nil {
		return PlanAgentResult{}, err
	}
	key := "plan:" + request.SourceAgentID + ":" + request.RevisionID
	if _, _, err := a.enqueueAgentMessageIdempotent(ctx, "", agent.ID, request.Prompt(), key); err != nil {
		return PlanAgentResult{}, err
	}
	if _, err := a.OpenAgent(ctx, agent.ID, true); err != nil {
		return PlanAgentResult{}, fmt.Errorf("plan agent was saved but its view did not open; retry /plan delegate: %w", err)
	}
	return PlanAgentResult{AgentID: agent.ID}, nil
}

func (a *App) createPlanAgent(ctx context.Context, request CreatePlanAgentRequest) (model.Agent, error) {
	a.agentMutationMu.Lock()
	defer a.agentMutationMu.Unlock()
	launch, err := a.Store.ReservePlanLaunch(ctx, model.PlanLaunch{SourceAgentID: request.SourceAgentID, RevisionID: request.RevisionID, PlanHash: fmt.Sprintf("%x", sha256.Sum256([]byte(request.Plan))), AgentID: uuid.NewString()})
	if err != nil {
		return model.Agent{}, err
	}
	if launch.Created {
		agent, err := a.Store.Agent(ctx, launch.AgentID)
		if err != nil {
			return model.Agent{}, fmt.Errorf("the plan's implementation agent is no longer available; restore it or submit a new plan revision")
		}
		return agent, nil
	}
	request.Agent.planLaunch = &launch
	request.Agent.Placement.exactRefs = true
	request.Agent.CreatedByAgentID = ""
	request.Agent.Presentation = "foreground"
	request.Agent.ContextAgentID = ""
	return a.createAgent(ctx, request.Agent)
}
