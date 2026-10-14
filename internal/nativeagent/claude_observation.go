package nativeagent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var claudeBackgroundNotice = regexp.MustCompile(`^(?:is still running after [0-9]+(?:\.[0-9]+)?s\. It was moved|was moved) to the background as task ([a-zA-Z0-9_-]+) (?:and keeps running;|so that a message that arrived while it was running can reach you;)`)
var claudeTaskHeader = regexp.MustCompile(`^<task-notification>\n<task-id>([a-zA-Z0-9_-]+)</task-id>\n`)
var claudeResultMarker = regexp.MustCompile(`\n\[Galpon saved-result marker: ([a-zA-Z0-9-]+)\]$`)

// Claude returns the local task identity in a replacement tool result when a
// call moves to the background. Only a matched Galpon call can bind that task.
func claudeBackgroundTask(text, tool string) string {
	prefix := "MCP tool \"galpon/" + tool + "\" "
	if !strings.HasPrefix(text, prefix) {
		return ""
	}
	match := claudeBackgroundNotice.FindStringSubmatch(strings.TrimPrefix(text, prefix))
	if match == nil {
		return ""
	}
	return match[1]
}

// A queued notification is not proof of observation. The native attachment
// records its entry into context. Require its producer, task, tool, and owning
// turn to match before accepting the marker appended to the completed result.
func (t *turnEvidence) claudeTaskResult(row map[string]any) {
	attachment := objectValue(row["attachment"])
	origin := objectValue(attachment["origin"])
	if row["type"] != "attachment" || attachment["type"] != "queued_command" || attachment["commandMode"] != "task-notification" || origin["kind"] != "task-notification" || origin["producer"] != "session-task" {
		return
	}
	if stringValue(attachment["source_uuid"]) == "" || stringValue(attachment["delivery_id"]) == "" {
		return
	}
	text := stringValue(attachment["prompt"])
	header := claudeTaskHeader.FindStringSubmatch(text)
	if header == nil {
		return
	}
	task := header[1]
	tool := t.ResultTasks[task]
	if tool == "" {
		return
	}
	prefix := fmt.Sprintf("%s<status>completed</status>\n<summary>MCP task %s (galpon/%s) completed.</summary>\n<result>\n", header[0], task, tool)
	const suffix = "\n</result>\n</task-notification>"
	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, suffix) {
		return
	}
	result := strings.TrimSuffix(strings.TrimPrefix(text, prefix), suffix)
	marker := claudeResultMarker.FindStringSubmatchIndex(result)
	if marker == nil || !json.Valid([]byte(result[:marker[0]])) {
		return
	}
	t.ToolProofs[result[marker[2]:marker[3]]] = true
}
