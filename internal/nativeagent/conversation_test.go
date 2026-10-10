package nativeagent

import (
	"path/filepath"
	"testing"
)

func TestCodexSavedMessagesWithoutItemIDsReachTheConversation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "native.jsonl")
	mirror := filepath.Join(root, "managed.jsonl")
	records := []map[string]any{
		{"type": "message", "role": "user", "content": "Repeat this message"},
		{"type": "function_call", "id": "tool-start", "call_id": "tool-call", "name": "galpon_read_message", "arguments": "{}"},
		{"type": "function_call_output", "call_id": "tool-call", "output": "Saved tool output"},
		{"type": "message", "role": "user", "content": "Repeat this message"},
	}
	for _, payload := range records {
		appendRecord(t, source, map[string]any{"type": "response_item", "payload": payload}, true)
	}
	first := newTranscript("codex", source, mirror)
	if err := first.refresh(); err != nil {
		t.Fatal(err)
	}
	if len(first.events) != 4 {
		t.Fatalf("saved messages were lost: %#v", first.events)
	}
	if first.events[0].Kind != "user_message" || first.events[3].Kind != "user_message" || first.events[0].EventID == first.events[3].EventID {
		t.Fatal("distinct user entries were merged")
	}
	if event := first.events[2]; event.Kind != "tool_execution_end" || event.Content != "Saved tool output" || event.ToolCallID != "tool-call" {
		t.Fatalf("tool output was lost: %#v", event)
	}
	replay := newTranscript("codex", source, mirror)
	if err := replay.refresh(); err != nil {
		t.Fatal(err)
	}
	for i, event := range first.events {
		if replay.events[i].EventID != event.EventID {
			t.Fatal("replay changed the saved event identity")
		}
	}
}
