package nativeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The saved history of a Codex thread drops the commands that code mode runs.
// The snapshot of the tab puts them back from the journal of the app view, in
// their places, and gives the turn its tokens.
func TestCodexHistoryPutsJournalStepsBackInTheirTurn(t *testing.T) {
	var turns []any
	if err := json.Unmarshal([]byte(`[
		{"id": "turn-1", "status": "completed", "startedAt": 1700000000, "items": [
			{"type": "userMessage", "id": "item-1", "content": [{"type": "text", "text": "Read the readme"}]},
			{"type": "agentMessage", "id": "item-2", "text": "Let me read it."},
			{"type": "agentMessage", "id": "item-3", "text": "It is a demo."}
		]},
		{"id": "turn-2", "status": "interrupted", "startedAt": 1700000100, "items": [
			{"type": "userMessage", "id": "item-4", "content": [{"type": "text", "text": "Write a long answer"}]}
		]}
	]`), &turns); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(t.TempDir(), "codex-steps.jsonl")
	steps := `{"turn":"turn-1","after":2,"item":{"type":"commandExecution","id":"exec-1","command":"/usr/bin/zsh -lc 'cat README.md'","status":"completed","commandActions":[{"type":"read","command":"cat README.md","name":"README.md","path":"/work/README.md"}],"aggregatedOutput":"# Demo\n","exitCode":0}}
{"turn":"turn-1","usage":{"input":1000,"output":60,"cacheRead":200}}
`
	if err := os.WriteFile(journal, []byte(steps), 0o600); err != nil {
		t.Fatal(err)
	}
	messages := codexHistory(turns, readCodexSteps(journal), "/work", "gpt-6.1-sol", 0)

	var shown []string
	for _, raw := range messages {
		message := objectValue(raw)
		switch message["role"] {
		case "user":
			shown = append(shown, "user: "+contentText(message["content"]))
		case "assistant":
			line := "assistant(" + stringValue(message["stopReason"]) + "): " + contentText(message["content"])
			for _, block := range anyList(message["content"]) {
				if call := objectValue(block); call["type"] == "toolCall" {
					line += " call " + stringValue(call["name"]) + " " + stringValue(objectValue(call["arguments"])["path"])
				}
			}
			shown = append(shown, line)
		case "toolResult":
			shown = append(shown, "result "+stringValue(message["toolName"])+": "+strings.TrimSpace(contentText(message["content"])))
		}
	}
	want := []string{
		"user: Read the readme",
		"assistant(toolUse): Let me read it. call read README.md",
		"result read: # Demo",
		"assistant(stop): It is a demo.",
		"user: Write a long answer",
		"assistant(aborted): ",
	}
	if strings.Join(shown, "\n") != strings.Join(want, "\n") {
		t.Fatalf("history:\n%s\nwant:\n%s", strings.Join(shown, "\n"), strings.Join(want, "\n"))
	}
	usage := objectValue(objectValue(messages[3])["usage"])
	if usage["input"] != 1000.0 || usage["output"] != 60.0 || usage["cacheRead"] != 200.0 {
		t.Fatalf("the last answer of the turn has no tokens of the turn: %v", usage)
	}
}

// A file change of Codex becomes an edit or a write step with a unified diff,
// so that the Changes panel of the tab counts its lines.
func TestCodexFileChangeStepsHaveUnifiedDiffs(t *testing.T) {
	var item map[string]any
	if err := json.Unmarshal([]byte(`{"type": "fileChange", "id": "patch-1", "status": "completed", "changes": [
		{"path": "/work/notes.txt", "kind": {"type": "add"}, "diff": "first line\nsecond line\n"},
		{"path": "/work/README.md", "kind": {"type": "update", "move_path": null}, "diff": "@@ -2,1 +2,1 @@\n-old\n+new\n"}
	]}`), &item); err != nil {
		t.Fatal(err)
	}
	calls := codexCalls(item, "/work")
	if len(calls) != 2 || calls[0].Name != "write" || calls[0].Args["path"] != "notes.txt" || calls[1].Name != "edit" || calls[1].Args["path"] != "README.md" {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].ID == calls[1].ID {
		t.Fatalf("the steps of one change share the ID %q", calls[0].ID)
	}
	_, added, _ := codexResult(item, 0)
	if added["diff"] != "@@ -0,0 +1,2 @@\n+first line\n+second line" {
		t.Fatalf("new file diff = %q", added["diff"])
	}
	_, updated, _ := codexResult(item, 1)
	if updated["diff"] != "@@ -2,1 +2,1 @@\n-old\n+new" {
		t.Fatalf("update diff = %q", updated["diff"])
	}
}
