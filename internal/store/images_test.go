package store

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestDeletingImageOwnersRemovesImageBlobs(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Images", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "agent", WorkspaceID: "ws", Title: "Agent", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", SessionID: "session", RuntimeID: "runtime", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgent(ctx, agent, nil); err != nil {
		t.Fatal(err)
	}
	image := func(id string) model.ImageAttachment {
		data := []byte("private-image-data")
		return model.ImageAttachment{ID: id, MimeType: "image/png", Size: int64(len(data)), Width: 1, Height: 1, Data: base64.StdEncoding.EncodeToString(data)}
	}
	messageImages := []model.ImageAttachment{image("message-image")}
	message := model.AgentMessage{ID: "message", TargetAgentID: agent.ID, Prompt: "look", Images: &messageImages, Status: "queued", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessageWithImages(ctx, message); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `delete from agent_messages where id=?`, message.ID); err != nil {
		t.Fatal(err)
	}
	assertImageBlobCount(t, s, 0)

	event := model.ConversationEvent{EventID: "event", Kind: "user_message", Role: "user", Images: []model.ImageAttachment{image("event-image")}, CreatedAt: now}
	if _, err := s.PutConversationEvents(ctx, agent.ID, agent.RuntimeID, []model.ConversationEvent{event}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `delete from conversation_events where agent_id=? and event_id=?`, agent.ID, event.EventID); err != nil {
		t.Fatal(err)
	}
	assertImageBlobCount(t, s, 0)
}

func assertImageBlobCount(t *testing.T, s *Store, want int) {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`select count(*) from image_blobs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("image blob count = %d, want %d", count, want)
	}
}
