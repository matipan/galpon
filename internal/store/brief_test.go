package store

import (
	"context"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestAgentBriefSkipsAssistantEndsWithoutText(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Brief", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "agent", WorkspaceID: "ws", Title: "Agent", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", SessionID: "session", RuntimeID: "runtime", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgent(ctx, agent, nil); err != nil {
		t.Fatal(err)
	}

	brief, err := s.AgentBrief(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if brief != (model.AgentBrief{}) {
		t.Fatalf("brief without events = %+v, want empty", brief)
	}

	events := []model.ConversationEvent{
		{EventID: "1", Kind: "user_message", Role: "user", Content: "Plan the release", CreatedAt: now + 1},
		{EventID: "2", Kind: "assistant_message_end", Role: "assistant", Content: "Here is the plan.", CreatedAt: now + 2},
		{EventID: "3", Kind: "user_message", Role: "user", Content: "Ship it", CreatedAt: now + 3},
		{EventID: "4", Kind: "assistant_message_end", Role: "assistant", Content: "Shipped.", CreatedAt: now + 4},
		// A turn that ends with a tool call has no text.
		{EventID: "5", Kind: "assistant_message_end", Role: "assistant", CreatedAt: now + 5},
		{EventID: "6", Kind: "tool_execution_end", ToolName: "bash", CreatedAt: now + 6},
	}
	if _, err := s.PutConversationEvents(ctx, agent.ID, agent.RuntimeID, events); err != nil {
		t.Fatal(err)
	}
	brief, err = s.AgentBrief(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := model.AgentBrief{Prompt: "Ship it", PromptAt: now + 3, Reply: "Shipped.", ReplyAt: now + 4}
	if brief != want {
		t.Fatalf("brief = %+v, want %+v", brief, want)
	}
}
