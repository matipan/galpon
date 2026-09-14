package app

import "github.com/matipan/galpon/internal/model"

// The model sees task state, not transport leases, joins, or receipt duties.
// Keep the existing ID fields so saved request handles remain usable.
type agentTaskView struct {
	ID            string `json:"id"`
	SenderAgentID string `json:"senderAgentId,omitempty"`
	SenderTitle   string `json:"senderTitle,omitempty"`
	TargetAgentID string `json:"targetAgentId"`
	Status        string `json:"status"`
	Response      string `json:"response,omitempty"`
	Error         string `json:"error,omitempty"`
	Attempt       int    `json:"attempt"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
	CompletedAt   int64  `json:"completedAt,omitempty"`
}

func taskToolView(message model.AgentMessage) agentTaskView {
	return agentTaskView{ID: message.ID, SenderAgentID: message.SenderAgentID, SenderTitle: message.SenderTitle, TargetAgentID: message.TargetAgentID,
		Status: message.Status, Response: message.Response, Error: message.Error, Attempt: message.Attempt,
		CreatedAt: message.CreatedAt, UpdatedAt: message.UpdatedAt, CompletedAt: message.CompletedAt}
}

type agentWaitView struct {
	agentTaskView
	MessageID           string                `json:"messageId"`
	WaitStatus          string                `json:"waitStatus"`
	MessageStatus       string                `json:"messageStatus"`
	TargetRuntimeStatus string                `json:"targetRuntimeStatus"`
	WaitError           *model.AgentWaitError `json:"waitError,omitempty"`
}

func waitToolView(value model.AgentWaitResult) agentWaitView {
	return agentWaitView{agentTaskView: taskToolView(value.AgentMessage), MessageID: value.MessageID, WaitStatus: value.WaitStatus,
		MessageStatus: value.MessageStatus, TargetRuntimeStatus: value.TargetRuntimeStatus, WaitError: value.WaitError}
}

func agentToolResultView(value any) any {
	switch result := value.(type) {
	case model.AgentMessage:
		return taskToolView(result)
	case model.AgentWaitResult:
		return waitToolView(result)
	case model.AgentWaitManyResult:
		outcomes := make([]agentWaitView, len(result.Outcomes))
		for index, outcome := range result.Outcomes {
			outcomes[index] = waitToolView(outcome)
		}
		return struct {
			Status     string          `json:"status"`
			ReturnWhen string          `json:"returnWhen"`
			Completed  int             `json:"completed"`
			Total      int             `json:"total"`
			Outcomes   []agentWaitView `json:"outcomes"`
		}{result.Status, result.ReturnWhen, result.Completed, result.Total, outcomes}
	case CreateAgentToolResult:
		var initial *agentTaskView
		if result.InitialMessage != nil {
			view := taskToolView(*result.InitialMessage)
			initial = &view
		}
		return struct {
			model.Agent
			InitialMessage *agentTaskView `json:"initialMessage,omitempty"`
		}{result.Agent, initial}
	default:
		return value
	}
}
