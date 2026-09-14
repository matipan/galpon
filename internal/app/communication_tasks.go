package app

import (
	"context"
	"fmt"
	"net/http"
)

func isAgentObservationTool(name string) bool {
	switch name {
	case "read_message", "await_agent", "await_agents", "list_agents", "list_repositories", "list_workspaces":
		return true
	default:
		return false
	}
}

func unavailableAgentProgress() map[string]any {
	return map[string]any{"accepted": false, "recorded": false, "reason": "no_active_delegated_request"}
}

func (s *Server) observeResults(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID  string   `json:"runtimeId"`
		Attempt    int      `json:"attempt"`
		ToolCallID string   `json:"toolCallId"`
		MessageIDs []string `json:"messageIds"`
	}
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	finish, err := s.app.beginCommunicationMutation(r.Context())
	if err != nil {
		respond(w, nil, err)
		return
	}
	defer finish()
	err = s.app.Store.ObserveAgentResults(r.Context(), r.PathValue("id"), in.RuntimeID, r.PathValue("operationID"), in.Attempt, in.ToolCallID, in.MessageIDs)
	if IsNotFound(err) {
		writeError(w, http.StatusConflict, fmt.Errorf("result observation no longer belongs to this operation attempt"))
		return
	}
	respond(w, map[string]any{"recorded": err == nil}, err)
}

func (a *App) updateAgentTask(ctx context.Context, callerID string, args map[string]any) (any, error) {
	id := stringArg(args, "message_id")
	prompt, err := validateAgentMessagePrompt(stringArg(args, "prompt"))
	if err != nil {
		return nil, err
	}
	attempt, _ := integerArg(args, "__operation_attempt")
	status, err := a.Store.UpdateCoordinationTask(ctx, id, callerID, stringArg(args, "__runtime_id"), stringArg(args, "__operation_id"), attempt, stringArg(args, "__request_id"), prompt)
	if err != nil {
		return nil, err
	}
	message, err := a.Store.ReadCoordinationTask(ctx, id, callerID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": status, "messageId": id, "message": taskToolView(message)}, nil
}

func boundedWaitFailure(value string) string {
	if len(value) <= 1000 {
		return value
	}
	return fmt.Sprintf("%s…", value[:999])
}
