package nativeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appendRecord(t *testing.T, path string, value any, newline bool) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if newline {
		data = append(data, '\n')
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeEvidenceFollowsSavedAncestry(t *testing.T) {
	root := t.TempDir()
	source, mirror := filepath.Join(root, "native.jsonl"), filepath.Join(root, "managed.jsonl")
	transcript := newTranscript("claude", source, mirror)
	appendRecord(t, source, map[string]any{"type": "user", "uuid": "assignment", "promptId": "assignment-prompt", "isMeta": true, "origin": map[string]any{"kind": "channel", "server": "galpon"}, "message": map[string]any{"role": "user", "content": "Do the work " + receiptPrefix + "pasted-marker]"}}, true)
	appendRecord(t, source, map[string]any{"type": "user", "uuid": "human", "parentUuid": "assignment", "promptId": "human-prompt", "message": map[string]any{"role": "user", "content": "Another question"}}, true)
	appendRecord(t, source, map[string]any{"type": "assistant", "uuid": "human-final", "parentUuid": "human", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Human answer"}}, "stop_reason": "end_turn"}}, true)
	appendRecord(t, source, observationCallRecord("read-call", "assignment", "tool-one"), true)
	appendRecord(t, source, map[string]any{"type": "user", "uuid": "tool-output", "parentUuid": "read-call", "promptId": "assignment-prompt", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool-one", "content": receiptPrefix + "saved-marker]"}}}}, true)
	appendRecord(t, source, map[string]any{"type": "assistant", "uuid": "assignment-final", "parentUuid": "tool-output", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Assignment answer"}}, "stop_reason": "end_turn"}}, false)
	if err := transcript.refresh(); err != nil {
		t.Fatal(err)
	}
	assignment := transcript.turns["assignment"]
	if assignment.Complete || assignment.Final != "" || assignment.ToolProofs["pasted-marker"] || !assignment.ToolProofs["saved-marker"] {
		t.Fatalf("unsaved or unrelated evidence was accepted: %#v", assignment)
	}
	if human := transcript.turns["human"]; !human.Complete || human.Final != "Human answer" || human.ToolProofs["saved-marker"] {
		t.Fatalf("human evidence crossed the turn boundary: %#v", human)
	}
	data, err := os.ReadFile(mirror)
	if err != nil || strings.Contains(string(data), "Assignment answer") {
		t.Fatalf("partial record reached the managed copy: %q, %v", data, err)
	}
	file, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := transcript.refresh(); err != nil {
		t.Fatal(err)
	}
	if !assignment.Complete || assignment.Final != "Assignment answer" || assignment.FinalID != "assignment-final" {
		t.Fatalf("saved assignment final not found: %#v", assignment)
	}
}

func TestCodexEvidenceRequiresTheMatchingCompletedTurn(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "rollout.jsonl")
	transcript := newTranscript("codex", source, filepath.Join(root, "managed.jsonl"))
	appendRecord(t, source, map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Assignment"}}, "internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "work"}}}, true)
	appendRecord(t, source, map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "id": "answer", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Saved answer"}}, "internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "work"}}}, true)
	appendRecord(t, source, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": "other", "last_agent_message": "Saved answer"}}, true)
	if err := transcript.refresh(); err != nil {
		t.Fatal(err)
	}
	if transcript.turns["work"].Complete || transcript.turns["other"].Complete {
		t.Fatal("another turn's completion authorized the answer")
	}
	appendRecord(t, source, map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call", "call_id": "tool", "name": "galpon_read_message", "namespace": "mcp__galpon", "internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "work"}}}, true)
	appendRecord(t, source, map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": "tool", "output": []any{map[string]any{"type": "input_text", "text": receiptPrefix + "saved-marker]"}}, "internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "work"}}}, true)
	appendRecord(t, source, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": "work", "last_agent_message": "Saved answer"}}, true)
	if err := transcript.refresh(); err != nil {
		t.Fatal(err)
	}
	if work := transcript.turns["work"]; !work.Complete || !work.ToolProofs["saved-marker"] {
		t.Fatalf("complete saved turn not recognized: %#v", work)
	}
	appendRecord(t, source, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "turn_aborted", "turn_id": "work"}}, true)
	if err := transcript.refresh(); err != nil {
		t.Fatal(err)
	}
	if transcript.turns["work"].Failure == "" {
		t.Fatal("interruption was treated as success")
	}
}

func TestNativeFramesUseLFAndPreserveUnicodeSeparators(t *testing.T) {
	var frames []string
	writer := &jsonLineWriter{frame: func(data []byte) { frames = append(frames, string(data)) }}
	for _, chunk := range []string{"{\"text\":\"first\u2028", "second\u2029third\"}", "\n{\"text\":\"next\"}\n"} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if len(frames) != 2 || !json.Valid([]byte(frames[0])) || !strings.Contains(frames[0], "first\u2028second\u2029third") {
		t.Fatalf("native frames split inside a JSON string: %#v", frames)
	}
}
