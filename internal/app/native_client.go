package app

import (
	"context"
	"encoding/json"
	"net/url"
)

// RuntimeToolRequest carries identities assigned by a runtime, not by the model.
type RuntimeToolRequest struct {
	AgentID            string         `json:"agentId"`
	RuntimeID          string         `json:"runtimeId"`
	RequestID          string         `json:"requestId"`
	ToolCallID         string         `json:"toolCallId"`
	OperationID        string         `json:"operationId"`
	OperationAttempt   int            `json:"operationAttempt"`
	ProtocolGeneration int            `json:"protocolGeneration"`
	Args               map[string]any `json:"args"`
}

func (c *Client) RuntimeTool(ctx context.Context, name string, request RuntimeToolRequest) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.post(ctx, "/v1/runtime/tools/"+url.PathEscape(name), request, &out)
	return out, err
}

func (c *Client) ObserveRuntimeResults(ctx context.Context, agentID, operationID, runtimeID string, attempt int, toolCallID string, messageIDs []string) error {
	return c.post(ctx, "/v1/runtime/agents/"+agentID+"/operations/"+url.PathEscape(operationID)+"/observe-results", map[string]any{
		"runtimeId": runtimeID, "attempt": attempt, "toolCallId": toolCallID, "messageIds": messageIDs,
	}, nil)
}

func (c *Client) RuntimeStatus(ctx context.Context, agentID, runtimeID, status, failure string) error {
	return c.post(ctx, "/v1/runtime/agents/"+agentID+"/status", map[string]any{"runtimeId": runtimeID, "status": status, "error": failure}, nil)
}

func (c *Client) WaitRuntimeCoordination(ctx context.Context, agentID, runtimeID, since string, _ int) (CoordinationWake, error) {
	var out CoordinationWake
	err := c.post(ctx, "/v1/runtime/agents/"+agentID+"/coordination/wait", map[string]any{
		"runtimeId": runtimeID, "since": since, "timeoutMs": 15000,
	}, &out)
	return out, err
}

func (c *Client) RuntimeConversation(ctx context.Context, agentID string, request ConversationEventsRequest) error {
	return c.post(ctx, "/v1/runtime/agents/"+agentID+"/conversation-events", request, nil)
}
