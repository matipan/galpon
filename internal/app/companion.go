package app

import (
	"cmp"
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

const (
	companionConversationPageSize = 40
	companionMessagePageSize      = 10
)

type CompanionAgentState struct {
	Agent           model.Agent          `json:"agent"`
	Messages        []model.AgentMessage `json:"messages"`
	WorkspaceTitle  string               `json:"workspaceTitle"`
	HasMoreMessages bool                 `json:"hasMoreMessages"`
	MessageBefore   string               `json:"messageBefore,omitempty"`
	MessagePageIDs  []string             `json:"messagePageIds,omitempty"`
}

type CompanionBackend interface {
	CompanionDashboard(context.Context) (model.Dashboard, error)
	CompanionAgent(context.Context, string, []string, string, bool) (CompanionAgentState, error)
	SendCompanion(context.Context, string, string, string) (model.AgentMessage, error)
	CreateAgentFromSource(context.Context, CreateAgentFromSourceRequest, string) (CreateAgentFromSourceResult, error)
}

type CompanionRepository struct {
	ID    string `json:"id"`
	Title string `json:"title"`
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
	ID               string           `json:"id"`
	WorkspaceID      string           `json:"workspaceId"`
	Title            string           `json:"title"`
	Role             string           `json:"role,omitempty"`
	Status           string           `json:"status"`
	CanCopyPlacement bool             `json:"canCopyPlacement"`
	WorkspaceTitle   string           `json:"workspaceTitle,omitempty"`
	CreatedAt        int64            `json:"createdAt"`
	UpdatedAt        int64            `json:"updatedAt"`
	DelegatedAgents  []CompanionAgent `json:"delegatedAgents,omitempty"`
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
	Cursor        int64                 `json:"cursor"`
	AudioMessages bool                  `json:"audioMessages"`
	Repositories  []CompanionRepository `json:"repositories"`
	Workspaces    []CompanionWorkspace  `json:"workspaces"`
}

type CompanionAgentDetail struct {
	Cursor                    int64                     `json:"cursor"`
	Agent                     CompanionAgent            `json:"agent"`
	Timeline                  []model.ConversationEvent `json:"timeline"`
	HasMore                   bool                      `json:"hasMore"`
	ConversationHasMore       bool                      `json:"conversationHasMore"`
	MessageHasMore            bool                      `json:"messageHasMore"`
	Before                    int64                     `json:"before,omitempty"`
	MessageBefore             string                    `json:"messageBefore,omitempty"`
	MirroredDeliveryResponses []string                  `json:"mirroredDeliveryResponses,omitempty"`
	MessagePageIDs            []string                  `json:"messagePageIds,omitempty"`
	DelegatedAgents           []CompanionAgent          `json:"delegatedAgents,omitempty"`
}

type CompanionCreateResult struct {
	Agent          CompanionAgent   `json:"agent"`
	InitialMessage CompanionMessage `json:"initialMessage"`
	StartPending   bool             `json:"startPending"`
}

type CompanionAudioResult struct {
	Message    CompanionMessage `json:"message"`
	Transcript string           `json:"transcript"`
	Language   string           `json:"language"`
}

type CompanionServer struct {
	store            *store.Store
	backend          CompanionBackend
	origin           string
	host             string
	streams          chan struct{}
	http             *http.Server
	Logger           *log.Logger
	TailscaleUser    string
	audioTranscriber companionAudioTranscriber
}

func NewCompanionServer(st *store.Store, backend CompanionBackend, allowedOrigin string) *CompanionServer {
	origin := strings.TrimSpace(allowedOrigin)
	originURL, _ := url.Parse(origin)
	s := &CompanionServer{
		store: st, backend: backend, origin: origin, host: originURL.Host,
		streams: make(chan struct{}, 4), audioTranscriber: newVoxtypeAudioTranscriber(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/bootstrap", s.bootstrap)
	mux.HandleFunc("GET /api/v1/agents/{id}", s.agent)
	mux.HandleFunc("GET /api/v1/events", s.events)
	mux.HandleFunc("POST /api/v1/agents/{id}/messages", s.sendMessage)
	mux.HandleFunc("POST /api/v1/agents/{id}/audio-messages", s.sendAudioMessage)
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
		cacheControl := "no-store"
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			// Asset names are stable across upgrades. Let browsers store them, but
			// require validation so one release cannot load an old module graph.
			cacheControl = "no-cache"
		}
		w.Header().Set("Cache-Control", cacheControl)
		if r.URL.Path == "/manifest.webmanifest" {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(self), geolocation=()")
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
	out := CompanionBootstrap{
		Cursor: sequence, AudioMessages: s.audioTranscriber != nil,
		Repositories: []CompanionRepository{}, Workspaces: []CompanionWorkspace{},
	}
	for _, repository := range dashboard.Repositories {
		out.Repositories = append(out.Repositories, CompanionRepository{ID: repository.ID, Title: boundedPublicLabel(repository.Title)})
	}
	workspaceIndex := make(map[string]int, len(dashboard.Workspaces))
	workspaceTitles := make(map[string]string, len(dashboard.Workspaces))
	for _, workspace := range dashboard.Workspaces {
		workspaceIndex[workspace.ID] = len(out.Workspaces)
		workspaceTitles[workspace.ID] = boundedPublicLabel(workspace.Title)
		out.Workspaces = append(out.Workspaces, safeWorkspace(workspace))
	}
	children := companionDelegatedChildren(dashboard.Agents)
	for _, agent := range dashboard.Agents {
		if agent.IsBackground() {
			continue
		}
		if index, ok := workspaceIndex[agent.WorkspaceID]; ok {
			out.Workspaces[index].Agents = append(out.Workspaces[index].Agents, safeAgentTree(agent, children, workspaceTitles, map[string]bool{}))
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
	events := []model.ConversationEvent{}
	hasMore := false
	if before > 0 || requestedMessageBefore == "" {
		events, hasMore, err = s.store.ConversationEventsPage(r.Context(), agentID, before, companionConversationPageSize)
		if err != nil {
			s.internalError(w, http.StatusInternalServerError, "could not read the conversation", err)
			return
		}
		events, hasMore, err = s.completeConversationContext(r.Context(), agentID, events, hasMore)
		if err != nil {
			s.internalError(w, http.StatusInternalServerError, "could not complete the conversation window", err)
			return
		}
	}
	events, byteLimited := boundConversationPage(events, 4<<20)
	hasMore = hasMore || byteLimited
	representedMessageIDs := conversationDeliveryIDs(events)
	includeMessagePage := before == 0 || requestedMessageBefore != ""
	view, err := s.backend.CompanionAgent(r.Context(), agentID, representedMessageIDs, requestedMessageBefore, includeMessagePage)
	if err != nil {
		s.companionBackendError(w, err)
		return
	}
	if includeMessagePage && len(view.MessagePageIDs) == 0 {
		for _, message := range view.Messages {
			if message.TargetAgentID == view.Agent.ID && !slices.Contains(representedMessageIDs, message.ID) {
				view.MessagePageIDs = append(view.MessagePageIDs, message.ID)
			}
		}
	}
	messages := companionPageMessages(view.Messages, events, before == 0 || requestedMessageBefore != "", view.Agent.ID)
	for index := range events {
		events[index].AgentID = ""
		events[index].RuntimeSeq = 0
		events[index].PiEntryID = ""
		if _, canonicalResponse := deliveryEventMessageID(events[index].EventID, ":response"); !canonicalResponse {
			events[index].EventID = fmt.Sprintf("event-%d", events[index].Sequence)
		}
		events[index].Content = boundedTimelineContent(events[index].Content)
	}
	// Conversation sequence is authoritative for Pi stream order. Producer
	// timestamps are not monotonic during one assistant message. Replace a
	// mirrored delivery at its durable Pi sequence instead of removing it and
	// reinserting it by timestamp. This keeps its tools after its prompt.
	promptSequences := make(map[string]int64)
	for _, event := range events {
		if event.Sequence <= 0 || event.Kind != "user_message" {
			continue
		}
		for _, match := range deliveryMarkerPattern.FindAllStringSubmatch(event.Content, -1) {
			promptSequences[match[1]] = event.Sequence
		}
	}
	conversation, _ := replaceMirroredDeliveryPrompts(events, messages, view.Agent.ID)
	var trimmedLeadingContext bool
	conversation, trimmedLeadingContext = trimConversationToFirstUser(conversation)
	hasMore = hasMore || trimmedLeadingContext
	retainedSequences := make(map[int64]bool, len(conversation))
	for _, event := range conversation {
		retainedSequences[event.Sequence] = true
	}
	for messageID, sequence := range promptSequences {
		if !retainedSequences[sequence] {
			delete(promptSequences, messageID)
		}
	}

	messageIDs := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.TargetAgentID == view.Agent.ID {
			messageIDs = append(messageIDs, message.ID)
		}
	}
	storedPromptSequences, err := s.store.ConversationDeliveryPromptSequences(r.Context(), view.Agent.ID, messageIDs)
	if err != nil {
		s.internalError(w, http.StatusInternalServerError, "could not match conversation prompts", err)
		return
	}

	synthetic := make([]model.ConversationEvent, 0, len(messages)*2)
	anchoredPrompts := make([]model.ConversationEvent, 0)
	mirroredDeliveryResponses := make([]string, 0)
	claimedResponseSequences := make(map[int64]bool)
	claimedPromptSequences := make(map[int64]bool)
	renderedFallbackPromptSequences := make(map[int64]bool)
	for _, message := range messages {
		if message.TargetAgentID != view.Agent.ID {
			continue
		}
		promptSequence := promptSequences[message.ID]
		promptInWindow := promptSequence > 0
		if promptSequence == 0 {
			promptSequence = storedPromptSequences[message.ID]
		}
		// A result consumed by galpon_await_agent(s) is a tool result in Pi, not a
		// user turn. Show a result notification only when Pi actually mirrored it.
		legacyAwaitConsumed := message.Kind == "result" && message.Status == "completed" && strings.TrimSpace(message.Response) == "consumed by await"
		if promptSequence == 0 && message.Kind == "result" && (message.NotificationState == "suppressed" || legacyAwaitConsumed) {
			continue
		}
		if promptSequence == 0 {
			synthetic = append(synthetic, model.ConversationEvent{
				EventID: "delivery:" + message.ID + ":prompt", ClientRequestID: message.IdempotencyKey, Kind: "delivery_" + message.Status,
				Role: "user", Content: boundedTimelineContent(message.Prompt), IsAgentDelivery: message.SenderAgentID != "",
				DeliveryKind: agentDeliveryKind(message), DeliverySenderTitle: agentDeliverySenderTitle(message), CreatedAt: message.CreatedAt,
			})
		}
		if message.Status != "completed" && message.Status != "failed" {
			if before == 0 && promptSequence > 0 && !promptInWindow {
				anchoredPrompts = append(anchoredPrompts, model.ConversationEvent{
					Sequence: promptSequence, IsAnchor: true, EventID: "delivery:" + message.ID + ":prompt",
					ClientRequestID: message.IdempotencyKey, Kind: "delivery_" + message.Status,
					Role: "user", Content: boundedTimelineContent(message.Prompt), IsAgentDelivery: message.SenderAgentID != "",
					DeliveryKind: agentDeliveryKind(message), DeliverySenderTitle: agentDeliverySenderTitle(message), CreatedAt: message.CreatedAt,
				})
			}
			continue
		}
		responseText := strings.TrimSpace(message.Response)
		responseSequence := int64(0)
		responseRepresented := false
		if responseText != "" && promptSequence > 0 {
			responseSequences, matchErr := s.store.ConversationAssistantEndSequences(r.Context(), view.Agent.ID, responseText, promptSequence, message.CreatedAt, message.UpdatedAt)
			if matchErr != nil {
				s.internalError(w, http.StatusInternalServerError, "could not match the conversation response", matchErr)
				return
			}
			responseSequence, responseRepresented = claimLastResponseSequence(responseSequences, claimedResponseSequences)
		}
		if responseRepresented {
			claimedPromptSequences[promptSequence] = true
			responseInWindow := false
			for index := range conversation {
				if conversation[index].Sequence == responseSequence {
					conversation[index].EventID = "delivery:" + message.ID + ":response"
					responseInWindow = true
					break
				}
			}
			if before == 0 && responseInWindow && !promptInWindow {
				anchoredPrompts = append(anchoredPrompts, model.ConversationEvent{
					Sequence: promptSequence, IsAnchor: true, EventID: "delivery:" + message.ID + ":prompt",
					ClientRequestID: message.IdempotencyKey, Kind: "delivery_" + message.Status,
					Role: "user", Content: boundedTimelineContent(message.Prompt), IsAgentDelivery: message.SenderAgentID != "",
					DeliveryKind: agentDeliveryKind(message), DeliverySenderTitle: agentDeliverySenderTitle(message), CreatedAt: message.CreatedAt,
				})
			}
			mirroredDeliveryResponses = append(mirroredDeliveryResponses, message.ID)
			continue
		}
		if promptSequence > 0 && claimedPromptSequences[promptSequence] {
			mirroredDeliveryResponses = append(mirroredDeliveryResponses, message.ID)
			continue
		}
		// A prompt outside this conversation page belongs on an older page with
		// its real Pi turn. Do not append it, or its fallback response, to the
		// newest page as a detached block.
		if promptSequence > 0 && !promptInWindow {
			continue
		}
		response := boundedTimelineContent(responseText)
		if response == "" && message.Status == "failed" {
			response = "The agent could not complete this request."
		}
		if response != "" && promptSequence > 0 && renderedFallbackPromptSequences[promptSequence] {
			mirroredDeliveryResponses = append(mirroredDeliveryResponses, message.ID)
			continue
		}
		if response != "" {
			if promptSequence > 0 {
				renderedFallbackPromptSequences[promptSequence] = true
			}
			synthetic = append(synthetic, model.ConversationEvent{
				EventID: "delivery:" + message.ID + ":response", Kind: "assistant_message_end",
				Role: "assistant", Content: response,
				IsError: message.Status == "failed", CreatedAt: message.UpdatedAt,
			})
		}
	}
	anchorIDs := make(map[string]bool, len(anchoredPrompts))
	for _, anchor := range anchoredPrompts {
		anchorIDs[anchor.EventID] = true
	}
	conversation = append(conversation, anchoredPrompts...)
	slices.SortStableFunc(conversation, func(a, b model.ConversationEvent) int {
		return cmp.Compare(a.Sequence, b.Sequence)
	})
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
	droppedConversation := slices.ContainsFunc(droppedTimeline, func(event model.ConversationEvent) bool {
		return event.Sequence > 0 && !anchorIDs[event.EventID]
	})
	conversationHasMore := hasMore || droppedConversation
	messageHasMore := view.HasMoreMessages || droppedSynthetic
	agent := safeAgent(view.Agent)
	agent.WorkspaceTitle = boundedPublicLabel(view.WorkspaceTitle)
	dashboard, dashboardErr := s.backend.CompanionDashboard(r.Context())
	if dashboardErr != nil {
		s.internalError(w, http.StatusBadGateway, "could not read delegated agents", dashboardErr)
		return
	}
	workspaceTitles := make(map[string]string, len(dashboard.Workspaces))
	for _, workspace := range dashboard.Workspaces {
		workspaceTitles[workspace.ID] = boundedPublicLabel(workspace.Title)
	}
	children := companionDelegatedChildren(dashboard.Agents)
	delegatedAgents := make([]CompanionAgent, 0, len(children[view.Agent.ID]))
	for _, child := range children[view.Agent.ID] {
		delegatedAgents = append(delegatedAgents, safeAgentTree(child, children, workspaceTitles, map[string]bool{view.Agent.ID: true}))
	}
	nextBefore := int64(0)
	for _, event := range timeline {
		if event.Sequence > 0 && !anchorIDs[event.EventID] {
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
			if slices.Contains(representedMessageIDs, message.ID) {
				continue
			}
			promptID := "delivery:" + message.ID + ":prompt"
			if slices.ContainsFunc(timeline, func(event model.ConversationEvent) bool { return event.EventID == promptID }) {
				messageBefore = companionMessageCursor(message.CreatedAt, message.ID)
				break
			}
		}
		if messageBefore == "" && len(view.MessagePageIDs) > 0 {
			for index := len(messages) - 1; index >= 0; index-- {
				message := messages[index]
				if slices.Contains(view.MessagePageIDs, message.ID) {
					// This boundary is newer than the newest fetched row, so
					// the exclusive query retries the complete dropped page.
					messageBefore = companionMessageCursor(message.CreatedAt+1, "page")
					break
				}
			}
		}
	}
	conversationHasMore = conversationHasMore && nextBefore > 0
	messageHasMore = messageHasMore && messageBefore != ""
	retainedMessagePageIDs := make([]string, 0, len(view.MessagePageIDs))
	for _, messageID := range view.MessagePageIDs {
		promptID := "delivery:" + messageID + ":prompt"
		if slices.ContainsFunc(timeline, func(event model.ConversationEvent) bool { return event.EventID == promptID }) {
			retainedMessagePageIDs = append(retainedMessagePageIDs, messageID)
		}
	}
	companionJSON(w, http.StatusOK, CompanionAgentDetail{
		Cursor: sequence, Agent: agent, Timeline: timeline,
		HasMore: conversationHasMore || messageHasMore, ConversationHasMore: conversationHasMore, MessageHasMore: messageHasMore,
		Before: nextBefore, MessageBefore: messageBefore, MirroredDeliveryResponses: mirroredDeliveryResponses,
		MessagePageIDs: retainedMessagePageIDs, DelegatedAgents: delegatedAgents,
	})
}

func trimConversationToFirstUser(events []model.ConversationEvent) ([]model.ConversationEvent, bool) {
	if len(events) == 0 || events[0].Role == "user" {
		return events, false
	}
	for index := 1; index < len(events); index++ {
		if events[index].Role == "user" {
			return events[index:], true
		}
	}
	return events, false
}

func claimLastResponseSequence(sequences []int64, claimed map[int64]bool) (int64, bool) {
	for index := len(sequences) - 1; index >= 0; index-- {
		sequence := sequences[index]
		if !claimed[sequence] {
			claimed[sequence] = true
			return sequence, true
		}
	}
	return 0, false
}

func (s *CompanionServer) completeConversationContext(ctx context.Context, agentID string, events []model.ConversationEvent, hasMore bool) ([]model.ConversationEvent, bool, error) {
	const maximumContextEvents = 200
	for hasMore && len(events) < maximumContextEvents && conversationWindowStartsMidStream(events) {
		if len(events) == 0 {
			break
		}
		pageSize := min(companionConversationPageSize, maximumContextEvents-len(events))
		older, olderHasMore, err := s.store.ConversationEventsPage(ctx, agentID, events[0].Sequence, pageSize)
		if err != nil {
			return nil, false, err
		}
		if len(older) == 0 {
			break
		}
		events = append(older, events...)
		hasMore = olderHasMore
	}
	return events, hasMore, nil
}

func conversationWindowStartsMidStream(events []model.ConversationEvent) bool {
	assistantStarted := false
	toolStarts := make(map[string]bool)
	for _, event := range events {
		switch event.Kind {
		case "assistant_message_start":
			assistantStarted = true
		case "assistant_text_delta":
			if !assistantStarted {
				return true
			}
		case "assistant_message_end":
			assistantStarted = false
		case "tool_execution_start":
			toolStarts[event.ToolCallID] = true
		case "tool_execution_update":
			if !toolStarts[event.ToolCallID] {
				return true
			}
		case "tool_execution_end":
			if !toolStarts[event.ToolCallID] {
				return true
			}
			delete(toolStarts, event.ToolCallID)
		}
	}
	return false
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
	if start > 0 && start < len(events) && events[start].Sequence > 0 && events[start-1].Sequence == events[start].Sequence {
		sequence := events[start].Sequence
		for start < len(events) && events[start].Sequence == sequence {
			start++
		}
	}
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
			sequence := events[candidate].Sequence
			for candidate > 0 && events[candidate-1].Sequence == sequence {
				candidate--
			}
			groupEnd := candidate
			groupSize := 0
			for groupEnd < len(events) && events[groupEnd].Sequence == sequence {
				groupSize += eventSize(events[groupEnd])
				groupEnd++
			}
			group := events[candidate:groupEnd]
			tail := events[groupEnd:]
			tailStart := suffixStart(tail, maxBytes-groupSize)
			kept = append(append([]model.ConversationEvent(nil), group...), tail[tailStart:]...)
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

var deliveryMarkerPattern = regexp.MustCompile(`\[delivery ([A-Za-z0-9:_-]{1,64})\]`)

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
	maxMessages := min(len(messages), 200)
	out := make([]model.AgentMessage, 0, maxMessages)
	for _, message := range messages {
		if message.TargetAgentID != targetAgentID {
			continue
		}
		represented := representedIDs[message.ID]
		if len(out) < maxMessages && (represented || initial && (message.Status == "queued" || message.Status == "delivered")) {
			out = append(out, message)
		}
	}
	if initial {
		for index := len(messages) - 1; index >= 0 && len(out) < maxMessages; index-- {
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

func oneDeliveryValue(values map[string]bool, fallback string) string {
	if len(values) != 1 {
		return fallback
	}
	for value := range values {
		return value
	}
	return fallback
}

func agentDeliveryKind(message model.AgentMessage) string {
	if kind := strings.TrimSpace(message.Kind); kind != "" {
		return kind
	}
	return "request"
}

func agentDeliverySenderTitle(message model.AgentMessage) string {
	if message.SenderAgentID == "" {
		return ""
	}
	if title := strings.TrimSpace(message.SenderTitle); title != "" {
		return boundedPublicLabel(title)
	}
	return "Agent"
}

func replaceMirroredDeliveryPrompts(events []model.ConversationEvent, messages []model.AgentMessage, targetAgentID string) ([]model.ConversationEvent, map[string]bool) {
	byID := make(map[string]model.AgentMessage, len(messages))
	for _, message := range messages {
		if message.TargetAgentID == targetAgentID {
			byID[message.ID] = message
		}
	}
	replaced := make(map[string]bool)
	conversation := make([]model.ConversationEvent, 0, len(events))
	for _, event := range events {
		if event.Kind != "user_message" {
			conversation = append(conversation, event)
			continue
		}
		matched := make([]model.AgentMessage, 0)
		for _, match := range deliveryMarkerPattern.FindAllStringSubmatch(event.Content, -1) {
			if message, ok := byID[match[1]]; ok {
				matched = append(matched, message)
				replaced[message.ID] = true
			}
		}
		if len(matched) == 0 {
			conversation = append(conversation, event)
			continue
		}
		for first := 0; first < len(matched); {
			isAgentDelivery := matched[first].SenderAgentID != ""
			last := first + 1
			for last < len(matched) && (matched[last].SenderAgentID != "") == isAgentDelivery {
				last++
			}
			run := matched[first:last]
			row := event
			row.EventID = "delivery:" + run[0].ID + ":prompt"
			row.ClientRequestID = run[0].IdempotencyKey
			row.Kind = "delivery_" + run[0].Status
			row.Role = "user"
			row.CreatedAt = run[0].CreatedAt
			row.IsAgentDelivery = isAgentDelivery
			row.DeliveryKind = ""
			row.DeliverySenderTitle = ""
			visiblePrompts := make([]string, 0, len(run))
			senderTitles := make(map[string]bool)
			deliveryKinds := make(map[string]bool)
			for _, message := range run {
				visiblePrompts = append(visiblePrompts, message.Prompt)
				if isAgentDelivery {
					senderTitles[agentDeliverySenderTitle(message)] = true
					deliveryKinds[agentDeliveryKind(message)] = true
				}
			}
			row.Content = boundedTimelineContent(strings.Join(visiblePrompts, "\n\n---\n\n"))
			if isAgentDelivery {
				row.DeliverySenderTitle = oneDeliveryValue(senderTitles, "Multiple agents")
				row.DeliveryKind = oneDeliveryValue(deliveryKinds, "message")
			}
			conversation = append(conversation, row)
			first = last
		}
	}
	return conversation, replaced
}

func mergeSyntheticTimeline(conversation, synthetic []model.ConversationEvent) []model.ConversationEvent {
	// A delivery without a durable Pi event has no stream position yet. Keep it
	// after the current stream until Pi mirrors its user message. Timestamp
	// insertion can place queued work inside an active turn and then move it when
	// the durable event arrives.
	out := make([]model.ConversationEvent, 0, len(conversation)+len(synthetic))
	out = append(out, conversation...)
	out = append(out, synthetic...)
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

func (s *CompanionServer) sendAudioMessage(w http.ResponseWriter, r *http.Request) {
	key, ok := s.checkMutation(w, r)
	if !ok {
		return
	}
	if s.audioTranscriber == nil {
		companionError(w, http.StatusServiceUnavailable, "audio transcription is not available")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		companionError(w, http.StatusUnsupportedMediaType, "Content-Type must be multipart/form-data")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, companionAudioLimit)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		companionError(w, http.StatusBadRequest, "audio message is invalid or too large")
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	language := strings.ToLower(strings.TrimSpace(r.FormValue("language")))
	if language == "" {
		language = "en"
	}
	if language != "en" && language != "es" {
		companionError(w, http.StatusUnprocessableEntity, "audio language must be en or es")
		return
	}
	audio, _, err := r.FormFile("audio")
	if err != nil {
		companionError(w, http.StatusBadRequest, "audio file is required")
		return
	}
	defer func() { _ = audio.Close() }()

	transcript, err := s.audioTranscriber.Transcribe(r.Context(), audio, language)
	if err != nil {
		s.logError("transcribe companion audio", err)
		switch {
		case errors.Is(err, errInvalidCompanionAudio):
			companionError(w, http.StatusUnprocessableEntity, "audio could not be read")
		case errors.Is(err, errCompanionAudioEmpty):
			companionError(w, http.StatusUnprocessableEntity, "no speech was detected")
		default:
			companionError(w, http.StatusServiceUnavailable, "audio could not be transcribed")
		}
		return
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		companionError(w, http.StatusUnprocessableEntity, "no speech was detected")
		return
	}
	if utf8.RuneCountInString(transcript) > companionPromptLimit {
		companionError(w, http.StatusUnprocessableEntity, "transcript is too long")
		return
	}
	message, err := s.backend.SendCompanion(r.Context(), r.PathValue("id"), transcript, key)
	if err != nil {
		s.companionBackendError(w, err)
		return
	}
	companionJSON(w, http.StatusOK, CompanionAudioResult{Message: safeMessage(message), Transcript: transcript, Language: language})
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
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Prompt) == "" {
		companionError(w, http.StatusUnprocessableEntity, "title and prompt are required")
		return
	}
	if strings.TrimSpace(in.SourceAgentID) == "" && (strings.TrimSpace(in.WorkspaceID) == "" || len(in.RepositoryIDs) == 0) {
		companionError(w, http.StatusUnprocessableEntity, "workspace and repository are required")
		return
	}
	if strings.TrimSpace(in.SourceAgentID) != "" && len(in.RepositoryIDs) > 0 {
		companionError(w, http.StatusUnprocessableEntity, "choose repositories or a source agent, not both")
		return
	}
	if len(in.RepositoryIDs) > 8 {
		companionError(w, http.StatusUnprocessableEntity, "at most eight repositories can be selected")
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

func companionDelegatedChildren(agents []model.Agent) map[string][]model.Agent {
	children := make(map[string][]model.Agent)
	for _, agent := range agents {
		if agent.IsBackground() && agent.CreatedByAgentID != "" {
			children[agent.CreatedByAgentID] = append(children[agent.CreatedByAgentID], agent)
		}
	}
	return children
}

func safeAgentTree(value model.Agent, children map[string][]model.Agent, workspaceTitles map[string]string, visiting map[string]bool) CompanionAgent {
	out := safeAgent(value)
	out.WorkspaceTitle = workspaceTitles[value.WorkspaceID]
	if visiting[value.ID] {
		return out
	}
	next := make(map[string]bool, len(visiting)+1)
	for id := range visiting {
		next[id] = true
	}
	next[value.ID] = true
	for _, child := range children[value.ID] {
		if !next[child.ID] {
			out.DelegatedAgents = append(out.DelegatedAgents, safeAgentTree(child, children, workspaceTitles, next))
		}
	}
	return out
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
