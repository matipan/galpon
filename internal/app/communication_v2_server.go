package app

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/model"
)

func (s *Server) communicationProtocol(w http.ResponseWriter, r *http.Request) {
	value, err := s.app.CommunicationProtocolState(r.Context())
	respond(w, value, err)
}

func (s *Server) upgradeCommunication(w http.ResponseWriter, r *http.Request) {
	// This endpoint is available only on the owner-only Unix socket. The browser
	// companion does not proxy it and has no upgrade mutation control.
	var in struct {
		Generation     int `json:"generation"`
		KnownTodoLinks []struct {
			MessageID string `json:"messageId"`
			TodoID    int64  `json:"todoId"`
			Policy    string `json:"policy"`
		} `json:"knownTodoLinks"`
		IdleTimeoutSeconds    int `json:"idleTimeoutSeconds"`
		BarrierTimeoutSeconds int `json:"barrierTimeoutSeconds"`
	}
	if !decode(w, r, &in) {
		return
	}
	request := CommunicationUpgradeRequest{Generation: in.Generation}
	for _, link := range in.KnownTodoLinks {
		request.KnownTodoLinks = append(request.KnownTodoLinks, model.AgentTodoLinkIntent{MessageID: strings.TrimSpace(link.MessageID), TodoID: link.TodoID, Policy: strings.TrimSpace(link.Policy)})
	}
	if in.IdleTimeoutSeconds > 0 {
		request.IdleTimeout = time.Duration(in.IdleTimeoutSeconds) * time.Second
	}
	if in.BarrierTimeoutSeconds > 0 {
		request.BarrierTimeout = time.Duration(in.BarrierTimeoutSeconds) * time.Second
	}
	value, err := s.app.UpgradeCommunicationV2(r.Context(), request)
	respond(w, value, err)
}

func (s *Server) recoverCommunicationRuntime(w http.ResponseWriter, r *http.Request) {
	// This bounded operator endpoint is available only on the owner-only Unix
	// socket. The browser companion does not proxy maintenance controls.
	var in struct {
		AgentID   string `json:"agentId"`
		RuntimeID string `json:"runtimeId"`
	}
	if !decode(w, r, &in) {
		return
	}
	value, err := s.app.RecoverCommunicationRuntime(r.Context(), in.AgentID, in.RuntimeID)
	respond(w, value, err)
}

func (s *Server) directOperation(w http.ResponseWriter, r *http.Request) {
	var in DirectOperationRequest
	if !decode(w, r, &in) {
		return
	}
	if !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	value, err := s.app.RegisterDirectOperation(r.Context(), r.PathValue("id"), in)
	respond(w, value, err)
}

func (s *Server) claimOperation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID          string `json:"runtimeId"`
		ClaimID            string `json:"claimId"`
		ProtocolGeneration int    `json:"protocolGeneration"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	value, err := s.app.ClaimCoordinationOperation(r.Context(), r.PathValue("id"), in.RuntimeID, in.ClaimID, in.ProtocolGeneration)
	respond(w, map[string]any{"delivery": value}, err)
}

func (s *Server) reconcileOperationOwnership(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID          string   `json:"runtimeId"`
		ProtocolGeneration int      `json:"protocolGeneration"`
		OperationIDs       []string `json:"operationIds"`
	}
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	value, err := s.app.ReconcileOperationOwnership(r.Context(), r.PathValue("id"), in.RuntimeID, in.ProtocolGeneration, in.OperationIDs)
	respond(w, value, err)
}

func (s *Server) startOperation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID string `json:"runtimeId"`
		Attempt   int    `json:"attempt"`
	}
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	err := s.app.StartCoordinationOperation(r.Context(), r.PathValue("id"), in.RuntimeID, r.PathValue("operationID"), in.Attempt)
	respond(w, map[string]any{"started": err == nil}, err)
}

func (s *Server) renewOperation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID string `json:"runtimeId"`
		Attempt   int    `json:"attempt"`
	}
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	err := s.app.RenewCoordinationOperation(r.Context(), r.PathValue("id"), in.RuntimeID, r.PathValue("operationID"), in.Attempt)
	respond(w, map[string]any{"renewed": err == nil}, err)
}

func (s *Server) settleOperation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID string `json:"runtimeId"`
		Attempt   int    `json:"attempt"`
		Response  string `json:"response"`
		Error     string `json:"error"`
	}
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	value, err := s.app.SettleCoordinationOperation(r.Context(), r.PathValue("id"), in.RuntimeID, r.PathValue("operationID"), in.Attempt, in.Response, in.Error)
	respond(w, value, err)
}

func (s *Server) takeReceipts(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID     string `json:"runtimeId"`
		Attempt       int    `json:"attempt"`
		ToolRequestID string `json:"toolRequestId"`
	}
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	value, err := s.app.TakeCoordinationReceipts(r.Context(), r.PathValue("id"), in.RuntimeID, r.PathValue("operationID"), in.Attempt, in.ToolRequestID)
	respond(w, value, err)
}

func (s *Server) presentReceipt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID     string `json:"runtimeId"`
		Attempt       int    `json:"attempt"`
		ToolRequestID string `json:"toolRequestId"`
	}
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	err := s.app.Store.MarkAgentInboxReceiptPresented(r.Context(), r.PathValue("receiptID"), r.PathValue("operationID"), in.RuntimeID, in.Attempt, in.ToolRequestID)
	respond(w, map[string]any{"presented": err == nil}, err)
}

func (s *Server) ackReceipt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID string `json:"runtimeId"`
		Attempt   int    `json:"attempt"`
	}
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	err := s.app.Store.AcknowledgeAgentInboxReceipt(r.Context(), r.PathValue("receiptID"), r.PathValue("operationID"), in.RuntimeID, in.Attempt)
	respond(w, map[string]any{"acknowledged": err == nil}, err)
}

func (s *Server) putLocalEvent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID          string `json:"runtimeId"`
		Attempt            int    `json:"attempt"`
		EventID            string `json:"eventId"`
		Kind               string `json:"kind"`
		Payload            string `json:"payload"`
		ProtocolGeneration int    `json:"protocolGeneration"`
	}
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	if len(in.Payload) > 64<<10 || len(in.EventID) > 200 || len(in.Kind) > 100 {
		respond(w, nil, invalidRequestf("local event exceeds safe limits"))
		return
	}
	value, inserted, err := s.app.Store.PutAgentPiLocalEvent(r.Context(), model.AgentPiLocalEvent{
		ID: uuid.NewString(), AgentID: r.PathValue("id"), OperationID: r.PathValue("operationID"),
		OperationAttempt: in.Attempt, EventID: in.EventID, Kind: in.Kind, Payload: in.Payload,
		RuntimeID: in.RuntimeID, ProtocolGeneration: in.ProtocolGeneration, CreatedAt: time.Now().UnixMilli(),
	})
	respond(w, map[string]any{"event": value, "inserted": inserted}, err)
}

func (s *Server) ackLocalEvent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID string `json:"runtimeId"`
		Attempt   int    `json:"attempt"`
	}
	if !decode(w, r, &in) || !s.runtimeMatches(w, r, in.RuntimeID) {
		return
	}
	err := s.app.Store.AcknowledgeAgentPiLocalEvent(r.Context(), r.PathValue("eventID"), r.PathValue("id"), r.PathValue("operationID"), in.RuntimeID, in.Attempt)
	respond(w, map[string]any{"acknowledged": err == nil}, err)
}

func (s *Server) runtimeMatches(w http.ResponseWriter, r *http.Request, runtimeID string) bool {
	if strings.TrimSpace(runtimeID) == "" {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("runtime ID is required"))
		return false
	}
	matches, err := s.app.Store.AgentRuntimeMatches(r.Context(), r.PathValue("id"), strings.TrimSpace(runtimeID))
	if err != nil {
		respond(w, nil, err)
		return false
	}
	if !matches {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("runtime is not registered for this agent"))
		return false
	}
	return true
}
