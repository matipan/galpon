package app

import "net/http"

type todoRuntimeInput struct {
	RuntimeID          string `json:"runtimeId"`
	ClaimID            string `json:"claimId"`
	OperationAttempt   int    `json:"operationAttempt"`
	ProtocolGeneration int    `json:"protocolGeneration"`
	Failure            string `json:"failure"`
	Snapshot           string `json:"snapshot"`
}

func (s *Server) claimTodoLink(w http.ResponseWriter, r *http.Request) {
	var in todoRuntimeInput
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	value, err := s.app.Store.ClaimAgentTodoLinkIntent(r.Context(), r.PathValue("intentID"), r.PathValue("id"), in.RuntimeID, in.ClaimID, in.OperationAttempt)
	respond(w, value, err)
}

func (s *Server) applyTodoLink(w http.ResponseWriter, r *http.Request) {
	var in todoRuntimeInput
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	err := s.app.Store.ApplyAgentTodoLinkIntent(r.Context(), r.PathValue("intentID"), r.PathValue("id"), in.RuntimeID, in.OperationAttempt)
	respond(w, map[string]any{"applied": err == nil}, err)
}

func (s *Server) failTodoLink(w http.ResponseWriter, r *http.Request) {
	var in todoRuntimeInput
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	err := s.app.Store.FailAgentTodoLinkIntent(r.Context(), r.PathValue("intentID"), r.PathValue("id"), in.RuntimeID, in.OperationAttempt, in.Failure)
	respond(w, map[string]any{"failed": err == nil}, err)
}

func (s *Server) claimTodoSettlement(w http.ResponseWriter, r *http.Request) {
	var in todoRuntimeInput
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	value, err := s.app.Store.ClaimAgentTodoSettlementEvent(r.Context(), r.PathValue("id"), in.RuntimeID, in.ClaimID)
	respond(w, value, err)
}

func (s *Server) applyTodoSettlement(w http.ResponseWriter, r *http.Request) {
	var in todoRuntimeInput
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	err := s.app.Store.ApplyAgentTodoSettlementEvent(r.Context(), r.PathValue("eventID"), r.PathValue("id"), in.RuntimeID, in.OperationAttempt, in.Snapshot)
	respond(w, map[string]any{"applied": err == nil}, err)
}

func (s *Server) failTodoSettlement(w http.ResponseWriter, r *http.Request) {
	var in todoRuntimeInput
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	err := s.app.Store.FailAgentTodoSettlementEvent(r.Context(), r.PathValue("eventID"), r.PathValue("id"), in.RuntimeID, in.OperationAttempt, in.Failure)
	respond(w, map[string]any{"failed": err == nil}, err)
}

func (s *Server) ackTodoSettlement(w http.ResponseWriter, r *http.Request) {
	var in todoRuntimeInput
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	err := s.app.Store.AcknowledgeAgentTodoSettlementEvent(r.Context(), r.PathValue("eventID"), r.PathValue("id"), in.RuntimeID, in.OperationAttempt)
	respond(w, map[string]any{"acknowledged": err == nil}, err)
}
