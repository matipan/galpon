package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	companionweb "github.com/matipan/galpon/internal/companion/web"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

type CompanionAgentState struct {
	Agent           model.Agent          `json:"agent"`
	Messages        []model.AgentMessage `json:"messages"`
	WorkspaceTitle  string               `json:"workspaceTitle"`
	HasMoreMessages bool                 `json:"hasMoreMessages"`
	MessageBefore   string               `json:"messageBefore,omitempty"`
}

type CompanionBackend interface {
	CompanionDashboard(context.Context) (model.Dashboard, error)
	CompanionAgent(context.Context, string, []string, string) (CompanionAgentState, error)
	SendCompanion(context.Context, string, string, string) (model.AgentMessage, error)
	CreateAgentFromSource(context.Context, CreateAgentFromSourceRequest, string) (CreateAgentFromSourceResult, error)
}

type CompanionWorkspace struct {
	ID        string           `json:"id"`
	Title     string           `json:"title"`
	Status    string           `json:"status"`
	CreatedAt int64            `json:"createdAt"`
	UpdatedAt int64            `json:"updatedAt"`
	Agents    []CompanionAgent `json:"agents"`
}

type CompanionAgent struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspaceId"`
	Title            string `json:"title"`
	Role             string `json:"role,omitempty"`
	Status           string `json:"status"`
	CanCopyPlacement bool   `json:"canCopyPlacement"`
	WorkspaceTitle   string `json:"workspaceTitle,omitempty"`
	CreatedAt        int64  `json:"createdAt"`
	UpdatedAt        int64  `json:"updatedAt"`
}

type CompanionMessage struct {
	ID            string `json:"id"`
	SenderAgentID string `json:"senderAgentId,omitempty"`
	TargetAgentID string `json:"targetAgentId"`
	Prompt        string `json:"prompt"`
	Status        string `json:"status"`
	Response      string `json:"response,omitempty"`
	Failed        bool   `json:"failed,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

type CompanionBootstrap struct {
	Cursor     int64                `json:"cursor"`
	Workspaces []CompanionWorkspace `json:"workspaces"`
}

type CompanionAgentDetail struct {
	Cursor                    int64                     `json:"cursor"`
	Agent                     CompanionAgent            `json:"agent"`
	Timeline                  []model.ConversationEvent `json:"timeline"`
	HasMore                   bool                      `json:"hasMore"`
	Before                    int64                     `json:"before,omitempty"`
	MessageBefore             string                    `json:"messageBefore,omitempty"`
	MirroredDeliveryResponses []string                  `json:"mirroredDeliveryResponses,omitempty"`
}

type CompanionCreateResult struct {
	Agent          CompanionAgent   `json:"agent"`
	InitialMessage CompanionMessage `json:"initialMessage"`
	StartPending   bool             `json:"startPending"`
}

type CompanionServer struct {
	store         *store.Store
	backend       CompanionBackend
	origin        string
	host          string
	streams       chan struct{}
	http          *http.Server
	Logger        *log.Logger
	TailscaleUser string
}

func NewCompanionServer(st *store.Store, backend CompanionBackend, allowedOrigin string) *CompanionServer {
	origin := strings.TrimSpace(allowedOrigin)
	originURL, _ := url.Parse(origin)
	s := &CompanionServer{store: st, backend: backend, origin: origin, host: originURL.Host, streams: make(chan struct{}, 4)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/bootstrap", s.bootstrap)
	mux.HandleFunc("GET /api/v1/agents/{id}", s.agent)
	mux.HandleFunc("GET /api/v1/events", s.events)
	mux.HandleFunc("POST /api/v1/agents/{id}/messages", s.sendMessage)
	mux.HandleFunc("POST /api/v1/agents", s.createAgent)
	mux.Handle("/", http.FileServer(http.FS(companionweb.Assets)))
	s.http = &http.Server{
		Handler: s.companionHeaders(mux), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
	}
	return s
}

func (s *CompanionServer) Serve(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid companion listen address: %w", err)
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("companion listener must use 127.0.0.1")
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return err
	}
	err = s.http.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *CompanionServer) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *CompanionServer) companionHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if s.host == "" || !strings.EqualFold(strings.TrimSpace(r.Host), s.host) {
			companionError(w, http.StatusMisdirectedRequest, "request host is not allowed")
			return
		}
		if strings.TrimSpace(r.Header.Get("Tailscale-Funnel-Request")) != "" {
			companionError(w, http.StatusForbidden, "Tailscale Funnel is not allowed")
			return
		}
		if s.TailscaleUser != "" && r.Header.Get("Tailscale-User-Login") != s.TailscaleUser {
			companionError(w, http.StatusUnauthorized, "Tailscale identity is not allowed")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
				companionError(w, http.StatusForbidden, "cross-site API requests are not allowed")
				return
			}
			if requestOrigin := strings.TrimSpace(r.Header.Get("Origin")); requestOrigin != "" && requestOrigin != s.origin {
				companionError(w, http.StatusForbidden, "request origin is not allowed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *CompanionServer) bootstrap(w http.ResponseWriter, r *http.Request) {
	// Capture the replay cursor before the projection. A mutation that overlaps
	// the projection then has a later event, so SSE cannot miss it.
	sequence, err := s.store.CompanionSequence(r.Context())
	if err != nil {
		s.internalError(w, http.StatusInternalServerError, "could not read companion sequence", err)
		return
	}
	dashboard, err := s.backend.CompanionDashboard(r.Context())
	if err != nil {
		s.internalError(w, http.StatusBadGateway, "could not read Galpon state", err)
		return
	}
	out := CompanionBootstrap{Cursor: sequence, Workspaces: []CompanionWorkspace{}}
	workspaceIndex := make(map[string]int, len(dashboard.Workspaces))
	for _, workspace := range dashboard.Workspaces {
		workspaceIndex[workspace.ID] = len(out.Workspaces)
		out.Workspaces = append(out.Workspaces, safeWorkspace(workspace))
	}
	for _, agent := range dashboard.Agents {
		if index, ok := workspaceIndex[agent.WorkspaceID]; ok {
			out.Workspaces[index].Agents = append(out.Workspaces[index].Agents, safeAgent(agent))
		}
	}
	companionJSON(w, http.StatusOK, out)
}

func (s *CompanionServer) agent(w http.ResponseWriter, r *http.Request) {
	// Read the cursor first for the same replay handoff rule as bootstrap.
	sequence, err := s.store.CompanionSequence(r.Context())
	if err != nil {
		s.internalError(w, http.StatusInternalServerError, "could not read companion sequence", err)
		return
	}
	before, err := nonNegativeQueryInt(r, "before")
	if err != nil {
		companionError(w, http.StatusBadRequest, "before must be a non-negative integer")
		return
	}
	requestedMessageBefore := strings.TrimSpace(r.URL.Query().Get("messageBefore"))
	if len(requestedMessageBefore) > 200 {
		companionError(w, http.StatusBadRequest, "messageBefore is invalid")
		return
	}
	if _, _, err := parseCompanionMessageCursor(requestedMessageBefore); err != nil {
		companionError(w, http.StatusBadRequest, "messageBefore is invalid")
		return
	}
	agentID := r.PathValue("id")
	events, hasMore, err := s.store.ConversationEventsPage(r.Context(), agentID, before, 100)
	if err != nil {
		s.internalError(w, http.StatusInternalServerError, "could not read the conversation", err)
		return
	}
	events, byteLimited := boundConversationPage(events, 4<<20)
	hasMore = hasMore || byteLimited
	view, err := s.backend.CompanionAgent(r.Context(), agentID, conversationDeliveryIDs(events), requestedMessageBefore)
	if err != nil {
		s.companionBackendError(w, err)
		return
	}
	messages := companionPageMessages(view.Messages, events, before == 0, view.Agent.ID)
	for index := range events {
		events[index].AgentID = ""
		events[index].RuntimeSeq = 0
		events[index].PiEntryID = ""
		events[index].EventID = fmt.Sprintf("event-%d", events[index].Sequence)
		events[index].Content = boundedTimelineContent(events[index].Content)
	}
	// Conversation sequence is authoritative for Pi stream order. Producer
	// timestamps are not monotonic during one assistant message.
	conversation := make([]model.ConversationEvent, 0, len(events))
	for _, event := range events {
		replacedByDelivery := false
		if event.Kind == "user_message" {
			for _, message := range messages {
				if message.TargetAgentID == view.Agent.ID && strings.Contains(event.Content, "[delivery "+message.ID+"]") {
					replacedByDelivery = true
				}
			}
		}
		if !replacedByDelivery {
			conversation = append(conversation, event)
		}
	}

	synthetic := make([]model.ConversationEvent, 0, len(messages)*2)
	mirroredDeliveryResponses := make([]string, 0)
	for _, message := range messages {
		if message.TargetAgentID != view.Agent.ID {
			continue
		}
		synthetic = append(synthetic, model.ConversationEvent{
			EventID: "delivery:" + message.ID + ":prompt", Kind: "delivery_" + message.Status,
			Role: "user", Content: boundedTimelineContent(message.Prompt), CreatedAt: message.CreatedAt,
		})
		if message.Status != "completed" && message.Status != "failed" {
			continue
		}
		responseText := strings.TrimSpace(message.Response)
		responseRepresented := false
		if responseText != "" {
			responseRepresented, err = s.store.HasConversationAssistantEnd(r.Context(), view.Agent.ID, responseText, message.CreatedAt, message.UpdatedAt+60_000)
			if err != nil {
				s.internalError(w, http.StatusInternalServerError, "could not match the conversation response", err)
				return
			}
		}
		if responseRepresented {
			mirroredDeliveryResponses = append(mirroredDeliveryResponses, message.ID)
			continue
		}
		response := boundedTimelineContent(responseText)
		if response == "" && message.Status == "failed" {
			response = "The agent could not complete this request."
		}
		if response != "" {
			synthetic = append(synthetic, model.ConversationEvent{
				EventID: "delivery:" + message.ID + ":response", Kind: "assistant_message_end",
				Role: "assistant", Content: response,
				IsError: message.Status == "failed", CreatedAt: message.UpdatedAt,
			})
		}
	}
	slices.SortStableFunc(synthetic, func(a, b model.ConversationEvent) int {
		if a.CreatedAt < b.CreatedAt {
			return -1
		}
		if a.CreatedAt > b.CreatedAt {
			return 1
		}
		return strings.Compare(a.EventID, b.EventID)
	})
	timeline := mergeSyntheticTimeline(conversation, synthetic)
	timeline, droppedTimeline := boundPublicTimeline(timeline, (4<<20)-(16<<10))
	droppedSynthetic := slices.ContainsFunc(droppedTimeline, func(event model.ConversationEvent) bool {
		return strings.HasPrefix(event.EventID, "delivery:")
	})
	hasMore = hasMore || view.HasMoreMessages || len(droppedTimeline) > 0
	agent := safeAgent(view.Agent)
	agent.WorkspaceTitle = boundedPublicLabel(view.WorkspaceTitle)
	nextBefore := int64(0)
	for _, event := range timeline {
		if event.Sequence > 0 {
			nextBefore = event.Sequence
			break
		}
	}
	messageBefore := ""
	if view.HasMoreMessages {
		messageBefore = view.MessageBefore
	}
	if droppedSynthetic {
		messageBefore = requestedMessageBefore
		for _, message := range messages {
			promptID := "delivery:" + message.ID + ":prompt"
			if slices.ContainsFunc(timeline, func(event model.ConversationEvent) bool { return event.EventID == promptID }) {
				messageBefore = companionMessageCursor(message.CreatedAt, message.ID)
				break
			}
		}
	}
	if nextBefore == 0 && messageBefore == "" {
		hasMore = false
	}
	companionJSON(w, http.StatusOK, CompanionAgentDetail{
		Cursor: sequence, Agent: agent, Timeline: timeline, HasMore: hasMore, Before: nextBefore,
		MessageBefore: messageBefore, MirroredDeliveryResponses: mirroredDeliveryResponses,
	})
}

func nonNegativeQueryInt(r *http.Request, name string) (int64, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, errors.New("invalid non-negative integer")
	}
	return parsed, nil
}

func boundConversationPage(events []model.ConversationEvent, maxBytes int) ([]model.ConversationEvent, bool) {
	total := 0
	start := 0
	for index := len(events) - 1; index >= 0; index-- {
		size := len(events[index].Content) + len(events[index].EventID) + len(events[index].ToolName) + len(events[index].ToolCallID) + 128
		if total+size > maxBytes && index < len(events)-1 {
			start = index + 1
			return events[start:], true
		}
		total += size
	}
	return events, false
}

func boundPublicTimeline(events []model.ConversationEvent, maxBytes int) ([]model.ConversationEvent, []model.ConversationEvent) {
	eventSize := func(event model.ConversationEvent) int {
		encoded, err := json.Marshal(event)
		if err != nil {
			return 0
		}
		return len(encoded) + 1
	}
	suffixStart := func(values []model.ConversationEvent, budget int) int {
		total := 0
		for index := len(values) - 1; index >= 0; index-- {
			if total+eventSize(values[index]) > budget && index < len(values)-1 {
				return index + 1
			}
			total += eventSize(values[index])
		}
		return 0
	}
	start := suffixStart(events, maxBytes)
	if start == 0 {
		return events, nil
	}
	kept := append([]model.ConversationEvent(nil), events[start:]...)
	dropped := append([]model.ConversationEvent(nil), events[:start]...)
	if !slices.ContainsFunc(kept, func(event model.ConversationEvent) bool { return event.Sequence > 0 }) {
		candidate := -1
		for index := start - 1; index >= 0; index-- {
			if events[index].Sequence > 0 {
				candidate = index
				break
			}
		}
		if candidate >= 0 {
			tail := events[candidate+1:]
			tailStart := suffixStart(tail, maxBytes-eventSize(events[candidate]))
			kept = append([]model.ConversationEvent{events[candidate]}, tail[tailStart:]...)
			dropped = append(append([]model.ConversationEvent(nil), events[:candidate]...), tail[:tailStart]...)
		}
	}
	droppedPrompts := make(map[string]bool)
	for _, event := range dropped {
		if id, ok := deliveryEventMessageID(event.EventID, ":prompt"); ok {
			droppedPrompts[id] = true
		}
	}
	completeGroups := kept[:0]
	for _, event := range kept {
		if id, ok := deliveryEventMessageID(event.EventID, ":response"); ok && droppedPrompts[id] {
			dropped = append(dropped, event)
			continue
		}
		completeGroups = append(completeGroups, event)
	}
	return completeGroups, dropped
}

func deliveryEventMessageID(eventID, suffix string) (string, bool) {
	if !strings.HasPrefix(eventID, "delivery:") || !strings.HasSuffix(eventID, suffix) {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(eventID, "delivery:"), suffix), true
}

func companionMessageCursor(createdAt int64, id string) string {
	if createdAt <= 0 || id == "" {
		return ""
	}
	return strconv.FormatInt(createdAt, 10) + "." + id
}

func parseCompanionMessageCursor(value string) (int64, string, error) {
	if value == "" {
		return 0, "", nil
	}
	createdText, id, ok := strings.Cut(value, ".")
	createdAt, err := strconv.ParseInt(createdText, 10, 64)
	if !ok || err != nil || createdAt <= 0 || id == "" || len(id) > 64 {
		return 0, "", errors.New("invalid companion message cursor")
	}
	return createdAt, id, nil
}

var deliveryMarkerPattern = regexp.MustCompile(`\[delivery ([0-9a-fA-F-]{36})\]`)

func conversationDeliveryIDs(events []model.ConversationEvent) []string {
	ids := make([]string, 0)
	seen := make(map[string]bool)
	for _, event := range events {
		for _, match := range deliveryMarkerPattern.FindAllStringSubmatch(event.Content, -1) {
			if !seen[match[1]] {
				seen[match[1]] = true
				ids = append(ids, match[1])
			}
		}
	}
	return ids
}

func companionPageMessages(messages []model.AgentMessage, events []model.ConversationEvent, initial bool, targetAgentID string) []model.AgentMessage {
	representedIDs := make(map[string]bool)
	for _, id := range conversationDeliveryIDs(events) {
		representedIDs[id] = true
	}
	out := make([]model.AgentMessage, 0, 100)
	for _, message := range messages {
		if message.TargetAgentID != targetAgentID {
			continue
		}
		represented := representedIDs[message.ID]
		if len(out) < 100 && (represented || initial && (message.Status == "queued" || message.Status == "delivered")) {
			out = append(out, message)
		}
	}
	if initial {
		for index := len(messages) - 1; index >= 0 && len(out) < 100; index-- {
			message := messages[index]
			if slices.ContainsFunc(out, func(existing model.AgentMessage) bool { return existing.ID == message.ID }) {
				continue
			}
			out = append(out, message)
		}
		slices.SortStableFunc(out, func(a, b model.AgentMessage) int {
			if a.CreatedAt < b.CreatedAt {
				return -1
			}
			if a.CreatedAt > b.CreatedAt {
				return 1
			}
			return strings.Compare(a.ID, b.ID)
		})
	}
	return out
}

func boundedTimelineContent(value string) string {
	const limit = 64 << 10
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit-80], "�") + "\n\n[Companion output truncated to 65536 bytes]"
}

func mergeSyntheticTimeline(conversation, synthetic []model.ConversationEvent) []model.ConversationEvent {
	out := append([]model.ConversationEvent(nil), conversation...)
	for _, item := range synthetic {
		index := len(out)
		for candidate, existing := range out {
			if existing.CreatedAt > item.CreatedAt {
				index = candidate
				break
			}
		}
		out = append(out, model.ConversationEvent{})
		copy(out[index+1:], out[index:])
		out[index] = item
	}
	return out
}

func (s *CompanionServer) sendMessage(w http.ResponseWriter, r *http.Request) {
	key, ok := s.checkMutation(w, r)
	if !ok {
		return
	}
	var in struct {
		Prompt string `json:"prompt"`
	}
	if !decodeCompanion(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Prompt) == "" {
		companionError(w, http.StatusUnprocessableEntity, "prompt is required")
		return
	}
	if utf8.RuneCountInString(in.Prompt) > companionPromptLimit {
		companionError(w, http.StatusUnprocessableEntity, "prompt is too long")
		return
	}
	message, err := s.backend.SendCompanion(r.Context(), r.PathValue("id"), in.Prompt, key)
	if err != nil {
		s.companionBackendError(w, err)
		return
	}
	companionJSON(w, http.StatusOK, safeMessage(message))
}

func (s *CompanionServer) createAgent(w http.ResponseWriter, r *http.Request) {
	key, ok := s.checkMutation(w, r)
	if !ok {
		return
	}
	var in CreateAgentFromSourceRequest
	if !decodeCompanion(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.SourceAgentID) == "" || strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Prompt) == "" {
		companionError(w, http.StatusUnprocessableEntity, "sourceAgentId, title, and prompt are required")
		return
	}
	if utf8.RuneCountInString(in.Title) > companionTitleLimit || utf8.RuneCountInString(in.Role) > companionRoleLimit || utf8.RuneCountInString(in.Prompt) > companionPromptLimit {
		companionError(w, http.StatusUnprocessableEntity, "agent title, role, or prompt is too long")
		return
	}
	result, err := s.backend.CreateAgentFromSource(r.Context(), in, key)
	if err != nil {
		s.companionBackendError(w, err)
		return
	}
	companionJSON(w, http.StatusOK, CompanionCreateResult{Agent: safeAgent(result.Agent), InitialMessage: safeMessage(result.InitialMessage), StartPending: result.StartPending})
}

func (s *CompanionServer) checkMutation(w http.ResponseWriter, r *http.Request) (string, bool) {
	if s.origin == "" || strings.TrimSpace(r.Header.Get("Origin")) != s.origin {
		companionError(w, http.StatusForbidden, "request origin is not allowed")
		return "", false
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		companionError(w, http.StatusPreconditionRequired, "a valid Idempotency-Key is required")
		return "", false
	}
	return key, true
}

func (s *CompanionServer) events(w http.ResponseWriter, r *http.Request) {
	select {
	case s.streams <- struct{}{}:
		defer func() { <-s.streams }()
	default:
		companionError(w, http.StatusTooManyRequests, "too many companion event streams")
		return
	}
	afterText := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if afterText == "" {
		afterText = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	after := int64(0)
	var err error
	if afterText != "" {
		after, err = strconv.ParseInt(afterText, 10, 64)
		if err != nil || after < 0 {
			companionError(w, http.StatusBadRequest, "after must be a non-negative integer")
			return
		}
	}
	minimum, maximum, err := s.store.CompanionEventRange(r.Context())
	if err != nil {
		s.internalError(w, http.StatusInternalServerError, "could not read companion event range", err)
		return
	}
	reset := after > maximum || minimum > 0 && after < minimum-1
	flusher, ok := w.(http.Flusher)
	if !ok {
		companionError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	poll := time.NewTicker(250 * time.Millisecond)
	heartbeat := time.NewTicker(15 * time.Second)
	lifetime := time.NewTimer(30 * time.Minute)
	defer poll.Stop()
	defer heartbeat.Stop()
	defer lifetime.Stop()
	controller := http.NewResponseController(w)
	if reset {
		event := model.CompanionEvent{Sequence: maximum, Type: "reset", CreatedAt: time.Now().UnixMilli()}
		data, _ := json.Marshal(event)
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprintf(w, "id: %d\nevent: reset\ndata: %s\n\n", maximum, data); err != nil {
			return
		}
		flusher.Flush()
		after = maximum
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-lifetime.C:
			return
		default:
		}
		events, err := s.store.CompanionEventsAfter(r.Context(), after, 100)
		if err != nil {
			s.logError("stream companion events", err)
			return
		}
		for _, event := range events {
			data, _ := json.Marshal(event)
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, data); err != nil {
				return
			}
			after = event.Sequence
		}
		if len(events) > 0 {
			flusher.Flush()
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-lifetime.C:
			return
		case <-poll.C:
		case <-heartbeat.C:
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func decodeCompanion(w http.ResponseWriter, r *http.Request, value any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		companionError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	defer func() { _ = r.Body.Close() }()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		companionError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		companionError(w, http.StatusBadRequest, "request body must contain one JSON value")
		return false
	}
	return true
}

func safeWorkspace(value model.Workspace) CompanionWorkspace {
	return CompanionWorkspace{ID: value.ID, Title: boundedPublicLabel(value.Title), Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, Agents: []CompanionAgent{}}
}

func safeAgent(value model.Agent) CompanionAgent {
	return CompanionAgent{ID: value.ID, WorkspaceID: value.WorkspaceID, Title: boundedPublicLabel(value.Title), Role: boundedPublicLabel(value.Role), Status: value.Status, CanCopyPlacement: value.Placement.Type == "worktrees" && len(value.Placement.Worktrees) > 0, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func boundedPublicLabel(value string) string {
	const limit = 1024
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit-32], "�") + "…"
}

func safeMessage(value model.AgentMessage) CompanionMessage {
	return CompanionMessage{ID: value.ID, SenderAgentID: value.SenderAgentID, TargetAgentID: value.TargetAgentID, Prompt: value.Prompt, Status: value.Status, Response: value.Response, Failed: value.Status == "failed", CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func (s *CompanionServer) companionBackendError(w http.ResponseWriter, err error) {
	s.logError("companion backend request", err)
	var apiError *APIError
	if errors.As(err, &apiError) {
		if apiError.StatusCode == http.StatusNotFound {
			companionError(w, http.StatusNotFound, "resource not found")
			return
		}
		if strings.Contains(apiError.Message, "Idempotency-Key") || strings.Contains(apiError.Message, "manual review") {
			companionError(w, http.StatusConflict, apiError.Message)
			return
		}
		switch apiError.Message {
		case "agent title and prompt are required", "prompt is required":
			companionError(w, http.StatusUnprocessableEntity, apiError.Message)
			return
		case "source agent does not have a managed worktree placement":
			companionError(w, http.StatusUnprocessableEntity, "source agent cannot copy a managed placement")
			return
		}
	}
	companionError(w, http.StatusBadGateway, "the Galpon daemon could not complete the request")
}

func (s *CompanionServer) internalError(w http.ResponseWriter, status int, message string, err error) {
	s.logError(message, err)
	companionError(w, status, message)
}

func (s *CompanionServer) logError(operation string, err error) {
	if s.Logger != nil {
		s.Logger.Printf("%s: %v", operation, err)
	}
}

func companionError(w http.ResponseWriter, status int, message string) {
	companionJSON(w, status, map[string]string{"error": message})
}

func companionJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
