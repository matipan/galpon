package store

import (
	"context"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestConversationPageOmitsPrivateReasoning(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{
		ID: "agent", WorkspaceID: "ws", Title: "Worker", Presentation: "foreground",
		Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi",
		Status: "running", SessionID: "agent", RuntimeID: "runtime", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutAgent(ctx, agent, nil); err != nil {
		t.Fatal(err)
	}
	events := []model.ConversationEvent{
		{EventID: "user", RuntimeSeq: 1, Kind: "user_message", Role: "user", Content: "Message [delivery message-id]: Check it", CreatedAt: now},
		{EventID: "reasoning", RuntimeSeq: 2, Kind: "assistant_reasoning_end", Role: "assistant", Content: "private chain", CreatedAt: now + 1},
		{EventID: "answer", RuntimeSeq: 3, Kind: "assistant_message_end", Role: "assistant", Content: "Done", CreatedAt: now + 2},
	}
	if _, err := s.PutConversationEvents(ctx, agent.ID, agent.RuntimeID, events); err != nil {
		t.Fatal(err)
	}
	page, hasMore, err := s.ConversationEventsPage(ctx, agent.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(page) != 2 || page[0].Kind != "user_message" || page[1].Kind != "assistant_message_end" {
		t.Fatalf("visible conversation page = %#v, hasMore %v", page, hasMore)
	}
	promptSequence, err := s.ConversationDeliveryPromptSequence(ctx, agent.ID, "message-id")
	if err != nil || promptSequence != page[0].Sequence {
		t.Fatalf("delivery prompt sequence = %d, %v", promptSequence, err)
	}
	if _, err := s.PutConversationEvents(ctx, agent.ID, agent.RuntimeID, []model.ConversationEvent{{
		EventID: "answer-two", RuntimeSeq: 4, Kind: "assistant_message_end", Role: "assistant", Content: "Done", CreatedAt: now + 3,
	}}); err != nil {
		t.Fatal(err)
	}
	sequences, err := s.ConversationAssistantEndSequences(ctx, agent.ID, "Done", 0, now, now+10)
	if err != nil || len(sequences) != 2 || sequences[0] >= sequences[1] {
		t.Fatalf("matching assistant response sequences = %#v, %v", sequences, err)
	}
	anchored, err := s.ConversationAssistantEndSequences(ctx, agent.ID, "Done", sequences[0], now, now+10)
	if err != nil || len(anchored) != 1 || anchored[0] != sequences[1] {
		t.Fatalf("anchored assistant response sequences = %#v, %v", anchored, err)
	}
}
