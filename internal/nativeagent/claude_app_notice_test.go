package nativeagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestClaudeTaskCompletionHasOneBackgroundEntry(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		for _, replayFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replay-first=%t", status, replayFirst), func(t *testing.T) {
				var output bytes.Buffer
				a := &claudeApp{
					appBridge: appBridge{ctx: t.Context(), options: launchOptions{Output: &output}},
					tasks:     map[string]map[string]any{}, prompts: map[string]*claudePrompt{},
				}
				frame := func(event map[string]any) {
					t.Helper()
					data, err := json.Marshal(event)
					if err != nil {
						t.Fatal(err)
					}
					a.frame(data)
				}
				summary := `Background command "Run planner tests" ` + status + " (exit code 0)"
				if status == "failed" {
					summary = `Background command "Run planner tests" failed (exit code 1)`
				}
				text := "<task-notification>\n<task-id>task-1</task-id>\n<tool-use-id>call-1</tool-use-id>\n<output-file>/tmp/claude/tasks/task-1.output</output-file>\n<status>" + status + "</status>\n<summary>" + summary + "</summary>\n</task-notification>"
				replay := func(id, text string) map[string]any {
					return map[string]any{"type": "user", "isReplay": true, "uuid": id,
						"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}}
				}
				// A person can submit the same text as an example. Its prompt
				// identity distinguishes it from an internal Claude replay.
				a.prompts["person"] = &claudePrompt{Text: text}
				frame(replay("person", text))
				quoted := "Explain this notice:\n" + text
				frame(replay("quoted-example", quoted))
				frame(map[string]any{"type": "system", "subtype": "task_started", "task_id": "task-1", "task_type": "local_bash", "description": "Run planner tests"})
				completion := map[string]any{"type": "system", "subtype": "task_notification", "task_id": "task-1", "status": status, "summary": summary}
				if replayFirst {
					frame(replay("notice", text))
					frame(completion)
				} else {
					frame(completion)
					frame(replay("notice", text))
				}
				frame(replay("notice-repeated", text))

				var messages []map[string]any
				for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
					var event map[string]any
					if err := json.Unmarshal([]byte(line), &event); err != nil {
						t.Fatal(err)
					}
					if event["type"] == "message_end" {
						messages = append(messages, objectValue(event["message"]))
					}
				}
				if len(messages) != 3 {
					t.Fatalf("expected two user examples and one task entry, got %d: %s", len(messages), output.String())
				}
				for i, want := range []string{text, quoted} {
					if messages[i]["role"] != "user" || contentText(messages[i]["content"]) != want {
						t.Fatalf("user example %d changed: %+v", i, messages[i])
					}
				}
				message := messages[2]
				notices := anyList(objectValue(message["details"])["notices"])
				if message["role"] != "custom" || message["customType"] != "galpon-background" || len(notices) != 1 {
					t.Fatalf("completion is not a background entry: %+v", message)
				}
				notice := objectValue(notices[0])
				if notice["id"] != "task-1" || notice["kind"] != "command" || notice["description"] != "Run planner tests" || notice["status"] != status {
					t.Fatalf("background entry lost its task facts: %+v", notice)
				}
			})
		}
	}
}
