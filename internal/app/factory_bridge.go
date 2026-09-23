package app

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/matipan/galpon/internal/model"
)

// CreateFactoryAgentRequest is the narrow integration boundary used by the
// optional Factory sidecar. It creates an ordinary background Galpon agent.
type CreateFactoryAgentRequest struct {
	Agent  CreateAgentRequest `json:"agent"`
	Prompt string             `json:"prompt"`
}

type CreateFactoryAgentResult struct {
	Agent          model.Agent        `json:"agent"`
	InitialMessage model.AgentMessage `json:"initialMessage"`
	StartPending   bool               `json:"startPending"`
}

// CreateFactoryAgent does not add a new agent kind or lifecycle. It only admits
// one idempotent, user-owned background launch and then uses the normal agent,
// message, and background runtime paths.
func (a *App) CreateFactoryAgent(ctx context.Context, idempotencyKey string, request CreateFactoryAgentRequest) (CreateFactoryAgentResult, error) {
	finish, err := a.beginCommunicationMutation(ctx)
	if err != nil {
		return CreateFactoryAgentResult{}, err
	}
	defer finish()

	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.Prompt == "" {
		return CreateFactoryAgentResult{}, fmt.Errorf("factory agent prompt is required")
	}
	if utf8.RuneCountInString(request.Prompt) > companionPromptLimit {
		return CreateFactoryAgentResult{}, fmt.Errorf("factory agent prompt exceeds limits")
	}
	request.Agent.CreatedByAgentID = ""
	request.Agent.Presentation = "background"
	request.Agent.planLaunch = nil

	var cached CreateFactoryAgentResult
	fresh, err := a.admitCompanionMutation(ctx, idempotencyKey, "factory_create_agent", request, &cached)
	if err != nil || !fresh {
		return cached, err
	}

	agent, err := a.CreateAgent(ctx, request.Agent)
	if err != nil {
		return CreateFactoryAgentResult{}, err
	}
	message, _, err := a.enqueueAgentMessageIdempotent(ctx, "", agent.ID, request.Prompt, "factory-initial:"+idempotencyKey)
	if err != nil {
		return CreateFactoryAgentResult{}, err
	}
	started, startErr := a.StartBackgroundAgent(ctx, agent.ID)
	if startErr == nil {
		agent = started
	}
	result := CreateFactoryAgentResult{Agent: agent, InitialMessage: message, StartPending: startErr != nil}
	return result, a.completeCompanionMutation(ctx, idempotencyKey, result)
}
