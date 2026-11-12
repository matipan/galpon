package nativeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClaudeHistoryReadsASessionAsTheTabShowsIt reads a saved Claude Code
// session with the records that a tab meets, and checks the messages that
// the tab gets after a reload.
func TestClaudeHistoryReadsASessionAsTheTabShowsIt(t *testing.T) {
	delivery := strings.Join([]string{
		"Galpon runtime instructions.",
		deliveryMarker("token-1"),
		"Message from Planner (request):\nReview the parser.",
	}, "\n\n")
	taskNotice := "<task-notification>\n<task-id>task-2</task-id>\n<tool-use-id>call-2</tool-use-id>\n<output-file>/tmp/claude/tasks/task-2.output</output-file>\n<status>failed</status>\n<summary>Background command \"Run planner tests\" failed (exit code 1)</summary>\n<result>compiler failed</result>\n</task-notification>"
	records := []map[string]any{
		{"type": "user", "uuid": "u1", "timestamp": "2026-11-08T10:00:00Z", "message": map[string]any{"role": "user", "content": "Fix the parser"}},
		// One answer comes as one record for each content block.
		{"type": "assistant", "uuid": "a1", "timestamp": "2026-11-08T10:00:01Z", "message": map[string]any{"id": "m1", "model": "claude-opus-5-5", "content": []any{map[string]any{"type": "text", "text": "I fix it."}}}},
		{"type": "assistant", "uuid": "a2", "timestamp": "2026-11-08T10:00:02Z", "message": map[string]any{"id": "m1", "model": "claude-opus-5-5", "stop_reason": "tool_use", "content": []any{
			map[string]any{"type": "tool_use", "id": "call-1", "name": "Edit", "input": map[string]any{"file_path": "/work/parser.go", "old_string": "a", "new_string": "b"}},
		}}},
		{"type": "user", "uuid": "r1", "timestamp": "2026-11-08T10:00:03Z", "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "call-1", "content": "The file was edited."},
		}}, "toolUseResult": map[string]any{"filePath": "/work/parser.go", "structuredPatch": []any{map[string]any{
			"oldStart": 1, "oldLines": 1, "newStart": 1, "newLines": 1, "lines": []any{"-a", "+b"},
		}}}},
		// A message that the person sent while the run worked.
		{"type": "attachment", "uuid": "q1", "timestamp": "2026-11-08T10:00:04Z", "attachment": map[string]any{"type": "queued_command", "prompt": "Also run the tests", "commandMode": "prompt"}},
		{"type": "assistant", "uuid": "a3", "timestamp": "2026-11-08T10:00:05Z", "message": map[string]any{"id": "m2", "model": "claude-opus-5-5", "stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": "Done."}}}},
		{"type": "user", "uuid": "t1", "timestamp": "2026-11-08T10:00:06Z", "origin": map[string]any{"kind": "task-notification"}, "message": map[string]any{"role": "user", "content": "<task-notification>\n<task-id>task-1</task-id>\n<status>completed</status>\n<summary>Agent \"Check things\" finished</summary>\n<result>All good.</result>\n</task-notification>"}},
		{"type": "attachment", "uuid": "t2", "timestamp": "2026-11-08T10:00:07Z", "attachment": map[string]any{"type": "queued_command", "commandMode": "task-notification", "origin": map[string]any{"kind": "task-notification", "producer": "session-task"}, "prompt": taskNotice}},
		// The same completion can appear in both saved notification forms.
		{"type": "user", "uuid": "t2-copy", "timestamp": "2026-11-08T10:00:07Z", "origin": map[string]any{"kind": "task-notification"}, "message": map[string]any{"role": "user", "content": taskNotice}},
		// User examples keep their role, including input queued during a run.
		{"type": "user", "uuid": "example", "timestamp": "2026-11-08T10:00:08Z", "message": map[string]any{"role": "user", "content": taskNotice}},
		{"type": "attachment", "uuid": "queued-example", "timestamp": "2026-11-08T10:00:09Z", "attachment": map[string]any{"type": "queued_command", "commandMode": "prompt", "prompt": taskNotice}},
		{"type": "user", "uuid": "d1", "timestamp": "2026-11-08T10:01:00Z", "message": map[string]any{"role": "user", "content": delivery}},
		{"type": "assistant", "uuid": "a4", "timestamp": "2026-11-08T10:01:01Z", "message": map[string]any{"id": "m3", "model": "claude-opus-5-5", "content": []any{map[string]any{"type": "text", "text": "I start the rev"}}}},
		{"type": "user", "uuid": "i1", "timestamp": "2026-11-08T10:01:02Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "[Request interrupted by user]"}}}},
		{"type": "system", "subtype": "compact_boundary", "uuid": "c1", "timestamp": "2026-11-08T10:02:00Z", "compactMetadata": map[string]any{"trigger": "manual", "preTokens": 12000}},
		{"type": "user", "uuid": "s1", "timestamp": "2026-11-08T10:02:01Z", "isCompactSummary": true, "message": map[string]any{"role": "user", "content": "This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation.\n\nThe parser is fixed.\n\nIf you need specific details from before compaction, read the transcript."}},
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	var lines []string
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	messages, total, err := claudeHistory(path, 100, map[string]claudeTurnCost{"a3": {Cost: 0.25}})
	if err != nil {
		t.Fatal(err)
	}
	type shown struct{ role, stop, text string }
	var got []shown
	for _, raw := range messages {
		message := raw.(map[string]any)
		text := contentText(message["content"])
		switch message["role"] {
		case "custom":
			if notices := anyList(objectValue(message["details"])["notices"]); len(notices) > 0 {
				notice := objectValue(notices[0])
				text = stringValue(notice["kind"]) + " " + stringValue(notice["description"]) + " " + stringValue(notice["status"]) + ": " + stringValue(notice["tail"])
			}
		case "compactionSummary":
			text = stringValue(message["summary"])
		case "toolResult":
			text = stringValue(message["toolName"]) + " " + stringValue(objectValue(message["details"])["diff"])
		}
		got = append(got, shown{stringValue(message["role"]), stringValue(message["stopReason"]), text})
	}
	want := []shown{
		{"user", "", "Fix the parser"},
		{"assistant", "toolUse", "I fix it."},
		{"toolResult", "", "Edit @@ -1,1 +1,1 @@\n-a\n+b"},
		{"user", "", "Also run the tests"},
		{"assistant", "stop", "Done."},
		{"custom", "", "agent Check things completed: All good."},
		{"custom", "", "command Run planner tests failed: compiler failed"},
		{"user", "", taskNotice},
		{"user", "", taskNotice},
		{"custom", "", "Work request from Galpón agent Planner [delivery token-1]:\n\nReview the parser."},
		{"assistant", "aborted", "I start the rev"},
		{"compactionSummary", "", "The parser is fixed."},
	}
	if total != len(want) || len(got) != len(want) {
		t.Fatalf("history has %d messages (total %d), want %d:\n%#v", len(got), total, len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("message %d is %#v, want %#v", index, got[index], want[index])
		}
	}
	// The cost of a run is on its last answer, so the tab adds it to the run.
	usage := objectValue(messages[4].(map[string]any)["usage"])
	if cost := objectValue(usage["cost"])["total"]; cost != 0.25 {
		t.Errorf("the last answer of the run has cost %v, want 0.25", cost)
	}
}
