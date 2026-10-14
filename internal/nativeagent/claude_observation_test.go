package nativeagent

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func claudeTaskNotice(task, tool, status, result string) string {
	return fmt.Sprintf("<task-notification>\n<task-id>%s</task-id>\n<status>%s</status>\n<summary>MCP task %s (galpon/%s) %s.</summary>\n<result>\n%s\n</result>\n</task-notification>", task, status, task, tool, status, result)
}

func TestClaudeBackgroundResultAcknowledgement(t *testing.T) {
	for _, scenario := range []string{"consumed", "steered", "queued only", "other tool", "other server", "unmatched call", "other task", "other notification tool", "other turn", "sidechain", "tool error", "failed task", "other producer", "pasted notice", "marker in result body"} {
		t.Run(scenario, func(t *testing.T) {
			controller, daemon, source := controllerFixture(t)
			controller.jobs = []*runtimeJob{{
				Operation: model.AgentOperation{ID: "operation", AgentID: "agent", Kind: "direct", RuntimeID: controller.runtimeID, Attempt: 1, ProtocolGeneration: 3},
				NativeID:  "request", Started: true,
				Observations: []observation{{Marker: "result-marker", MessageIDs: []string{"message:child"}}},
			}}
			step := func() {
				t.Helper()
				if err := controller.step(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			appendRecord(t, source, userRecord("request", "", "Ask the reviewer"), true)
			call := observationCallRecord("call", "request", "await-call")
			tool := "galpon_await_agent"
			if scenario == "other tool" {
				tool = "galpon_list_agents"
			}
			name := "mcp__galpon__" + tool
			if scenario == "other server" {
				name = "mcp__other__" + tool
			}
			objectValue(call["message"])["content"].([]any)[0].(map[string]any)["name"] = name
			appendRecord(t, source, call, true)
			background := fmt.Sprintf("MCP tool \"galpon/%s\" is still running after 120s. It was moved to the background as task task-one and keeps running; you'll receive a notification with the result when it completes.", tool)
			if scenario == "steered" {
				background = fmt.Sprintf("MCP tool \"galpon/%s\" was moved to the background as task task-one so that a message that arrived while it was running can reach you; it was not interrupted and keeps running; you'll receive a notification with the result when it completes.", tool)
			}
			callID := "await-call"
			if scenario == "unmatched call" {
				callID = "unrelated-call"
			}
			appendRecord(t, source, map[string]any{"type": "user", "uuid": "background", "parentUuid": "call", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": callID, "is_error": scenario == "tool error", "content": []any{map[string]any{"type": "text", "text": background}}}}}}, true)
			task, noticeTool, status := "task-one", tool, "completed"
			if scenario == "other task" {
				task = "task-two"
			}
			if scenario == "other notification tool" {
				noticeTool = "galpon_read_message"
			}
			if scenario == "failed task" {
				status = "failed"
			}
			result := `{"waitStatus":"completed","response":"Saved answer"}` + "\n" + receiptPrefix + "result-marker]"
			if scenario == "marker in result body" {
				result = `{"response":"` + receiptPrefix + `result-marker]"}`
			}
			prompt := claudeTaskNotice(task, noticeTool, status, result)
			appendRecord(t, source, map[string]any{"type": "queue-operation", "operation": "enqueue", "content": prompt}, true)
			step()
			if len(daemon.matching("/observe-results")) != 0 {
				t.Fatal("queued output was acknowledged before entering the conversation")
			}
			parent := "background"
			if scenario == "other turn" {
				appendRecord(t, source, userRecord("other", parent, "Another question"), true)
				parent = "other"
			}
			producer := "session-task"
			if scenario == "other producer" {
				producer = "other"
			}
			notice := map[string]any{"type": "attachment", "uuid": "notice", "parentUuid": parent, "isSidechain": scenario == "sidechain", "attachment": map[string]any{
				"type": "queued_command", "commandMode": "task-notification", "origin": map[string]any{"kind": "task-notification", "producer": producer},
				"source_uuid": "command", "delivery_id": "delivery", "prompt": prompt,
			}}
			if scenario == "pasted notice" {
				notice = userRecord("notice", parent, prompt)
			}
			if scenario != "queued only" {
				appendRecord(t, source, notice, false)
				step()
				if len(daemon.matching("/observe-results")) != 0 {
					t.Fatal("an incomplete notification acknowledged the result")
				}
				file, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := file.WriteString("\n")
				closeErr := file.Close()
				if writeErr != nil || closeErr != nil {
					t.Fatalf("complete notification: %v, %v", writeErr, closeErr)
				}
			}
			for range 2 {
				step()
			}
			want := 0
			if scenario == "consumed" || scenario == "steered" {
				want = 1
			}
			calls := daemon.matching("/observe-results")
			if len(calls) != want {
				t.Fatalf("result acknowledgements = %d, want %d", len(calls), want)
			}
			if want == 1 {
				body := calls[0].Body
				ids, _ := body["messageIds"].([]any)
				if body["toolCallId"] != "result-marker" || len(ids) != 1 || ids[0] != "message:child" {
					t.Fatalf("wrong result acknowledged: %#v", body)
				}
				// Recovery reconstructs proof from disk and uses the current fence.
				controller.runtimeID = "recovered-runtime"
				controller.jobs[0].Operation.Attempt = 2
				controller.jobs[0].Observations[0].Recorded = false
				controller.transcript = newTranscript("claude", source, strings.TrimSuffix(source, ".jsonl")+"-replayed.jsonl")
				for range 2 {
					step()
				}
				calls = daemon.matching("/observe-results")
				if len(calls) != 2 || calls[1].Body["runtimeId"] != "recovered-runtime" || calls[1].Body["attempt"] != float64(2) {
					t.Fatalf("recovered observation = %#v", calls)
				}
			}
		})
	}
}
