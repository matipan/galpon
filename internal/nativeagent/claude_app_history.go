package nativeagent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// The agent tab of the Herdr app draws the messages of Pi RPC. This file
// gives the parts of a Claude Code conversation in that form: the saved
// conversation for a snapshot, the Galpon deliveries as the tab shows them,
// the diffs of the file tools and the names of the Galpon tools.

// claudeTurnCost is what one turn cost and how long it ran. Claude Code
// reports the cost of a turn only in its result, so Galpon keeps it by the
// last assistant record of the turn.
type claudeTurnCost struct {
	Cost       float64 `json:"cost"`
	DurationMS int64   `json:"durationMs,omitempty"`
}

// claudeToolName gives the name that the tab shows for a Claude Code tool.
// The Galpon tools come from the MCP server "galpon" of the runtime.
func claudeToolName(name string) string {
	if strings.HasPrefix(name, "mcp__galpon__") {
		return strings.TrimPrefix(name, "mcp__galpon__")
	}
	return name
}

// claudeTimestamp reads the time of a saved record in milliseconds.
func claudeTimestamp(value any) int64 {
	if at, err := time.Parse(time.RFC3339Nano, stringValue(value)); err == nil {
		return at.UnixMilli()
	}
	return 0
}

// claudeStopReason gives the stop reason of Pi for the stop reason of the API.
func claudeStopReason(reason string, calls bool) string {
	switch reason {
	case "tool_use":
		return "toolUse"
	case "max_tokens":
		return "length"
	case "refusal":
		return "error"
	case "end_turn", "stop_sequence", "pause_turn":
		return "stop"
	}
	if calls {
		return "toolUse"
	}
	return "stop"
}

// claudeUsage gives the usage of Pi for the usage of the API.
func claudeUsage(usage map[string]any, cost float64) map[string]any {
	number := func(key string) float64 { value, _ := usage[key].(float64); return value }
	input, output := number("input_tokens"), number("output_tokens")
	cacheRead, cacheWrite := number("cache_read_input_tokens"), number("cache_creation_input_tokens")
	return map[string]any{
		"input": input, "output": output, "cacheRead": cacheRead, "cacheWrite": cacheWrite,
		"totalTokens": input + output + cacheRead + cacheWrite, "cost": map[string]any{"total": cost},
	}
}

// claudeContent gives the content blocks of Pi for content of the API: text
// and images. Images keep their base64 data; the app host saves them as files.
func claudeContent(value any) []any {
	if text, ok := value.(string); ok {
		return []any{map[string]any{"type": "text", "text": text}}
	}
	blocks, _ := value.([]any)
	out := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		block := objectValue(raw)
		switch block["type"] {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": stringValue(block["text"])})
		case "image":
			source := objectValue(block["source"])
			if data := stringValue(source["data"]); data != "" {
				out = append(out, map[string]any{"type": "image", "data": data, "mimeType": stringValue(source["media_type"])})
			}
		case "tool_reference", "document":
			if name := stringValue(block["tool_name"]); name != "" {
				out = append(out, map[string]any{"type": "text", "text": name})
			}
		}
	}
	return out
}

// claudeAssistantBlock gives the content block of Pi for an assistant block of
// the API, or nil for a block that the tab does not draw.
func claudeAssistantBlock(block map[string]any) map[string]any {
	switch block["type"] {
	case "text":
		return map[string]any{"type": "text", "text": stringValue(block["text"])}
	case "thinking":
		return map[string]any{"type": "thinking", "thinking": stringValue(block["thinking"])}
	case "redacted_thinking":
		return map[string]any{"type": "thinking", "thinking": "", "redacted": true}
	case "tool_use", "server_tool_use":
		arguments := block["input"]
		if arguments == nil {
			arguments = map[string]any{}
		}
		return map[string]any{"type": "toolCall", "id": stringValue(block["id"]), "name": claudeToolName(stringValue(block["name"])), "arguments": arguments}
	}
	return nil
}

// claudeToolDetails gives the details of a tool result that the tab reads:
// the diff of a file tool, and the task of a background command.
func claudeToolDetails(result any) map[string]any {
	values := objectValue(result)
	if values == nil {
		return nil
	}
	details := map[string]any{}
	if diff := claudeDiff(values); diff != "" {
		details["diff"] = diff
	}
	for _, key := range []string{"backgroundTaskId", "taskId", "task_id"} {
		if id := stringValue(values[key]); id != "" {
			details["task_id"] = id
			break
		}
	}
	if len(details) == 0 {
		return nil
	}
	return details
}

const maxDiffLines = 2000

// claudeDiff gives a unified diff of the change of an Edit or a Write: the
// hunks of its structured patch, or every line of a file that it created.
func claudeDiff(result map[string]any) string {
	var lines []string
	for _, raw := range anyList(result["structuredPatch"]) {
		hunk := objectValue(raw)
		number := func(key string) int { value, _ := hunk[key].(float64); return int(value) }
		lines = append(lines, fmt.Sprintf("@@ -%d,%d +%d,%d @@", number("oldStart"), number("oldLines"), number("newStart"), number("newLines")))
		for _, line := range anyList(hunk["lines"]) {
			lines = append(lines, stringValue(line))
		}
	}
	if len(lines) == 0 && result["type"] == "create" {
		content := strings.TrimSuffix(stringValue(result["content"]), "\n")
		if content != "" {
			added := strings.Split(content, "\n")
			lines = append(lines, fmt.Sprintf("@@ -0,0 +1,%d @@", len(added)))
			for _, line := range added {
				lines = append(lines, "+"+line)
			}
		}
	}
	if len(lines) > maxDiffLines {
		lines = append(lines[:maxDiffLines], fmt.Sprintf("… %d more lines", len(lines)-maxDiffLines))
	}
	return strings.Join(lines, "\n")
}

func anyList(value any) []any { list, _ := value.([]any); return list }

// A delivery prompt of the native runtime has the runtime instructions, the
// delivery marker, then its parts: the message of the sender, coordination
// notices and results. See runtimeController.claim.
var (
	deliveryMessage = regexp.MustCompile(`(?s)\A\s*Message from (.*?) \(([a-z_]+)\):\n`)
	deliveryNotice  = regexp.MustCompile(`Coordination notice \(([^)]*)\) for ([A-Za-z0-9:_-]+)\. Use galpon_read_message to inspect it within the original task's scope\.`)
	deliveryResult  = regexp.MustCompile(`(?s)Result for ([A-Za-z0-9:_-]+) \(([^)]*)\):\n(.*?)\nThis is a result notification\. Continue the original task within its authorized scope\.`)
)

// deliveryHeaders are the first lines of Galpon deliveries in Pi, by the act
// of the message. The tab reads them back into delivery cards.
var deliveryHeaders = map[string]string{"request": "Work request", "query": "Question", "question": "Question", "inform": "One-way information"}

// nativeDeliveryDisplay gives the text of a native delivery prompt as Pi
// shows a Galpon delivery, without the runtime instructions. It reports false
// for a prompt that is not a delivery.
func nativeDeliveryDisplay(prompt string) (string, bool) {
	match := deliveryPattern.FindStringSubmatchIndex(prompt)
	if match == nil {
		return "", false
	}
	token := prompt[match[2]:match[3]]
	rest := prompt[match[1]:]
	var parts []string
	// The message of the sender ends where the first notice or result starts.
	end := len(rest)
	for _, pattern := range []*regexp.Regexp{deliveryNotice, deliveryResult} {
		if index := pattern.FindStringIndex(rest); index != nil && index[0] < end {
			end = index[0]
		}
	}
	if head := deliveryMessage.FindStringSubmatchIndex(rest[:end]); head != nil {
		sender, act := rest[head[2]:head[3]], rest[head[4]:head[5]]
		header := deliveryHeaders[act]
		if header == "" {
			header = "Message"
		}
		body := strings.TrimSpace(rest[head[1]:end])
		// A message of the person has no sender agent.
		from := ""
		if sender = strings.TrimSpace(sender); sender != "" {
			from = " from Galpón agent " + sender
		}
		parts = append(parts, fmt.Sprintf("%s%s [delivery %s]:\n\n%s", header, from, token, body))
	}
	for _, notice := range deliveryNotice.FindAllStringSubmatch(rest, -1) {
		parts = append(parts, fmt.Sprintf("Message [delivery %s]:\n\nGalpon has a %s notice for message %s.", token, notice[1], notice[2]))
	}
	for _, result := range deliveryResult.FindAllStringSubmatch(rest, -1) {
		body := strings.TrimSpace(result[3])
		kind := "result"
		if status := strings.ToLower(result[2]); status == "failed" || status == "blocked" || status == "expired" || status == "canceled" {
			kind = "blocker"
		}
		parts = append(parts, fmt.Sprintf("Durable %s for assignment %s:\n\n%s", kind, result[1], body))
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("Message [delivery %s]:\n\n%s", token, strings.TrimSpace(rest)))
	}
	return strings.Join(parts, "\n\n---\n\n"), true
}

// claudeCommandMessage is the conversation message of the output of a local
// command of Claude Code, such as /context.
func claudeCommandMessage(command, args, output string, at int64) map[string]any {
	name := ""
	if command != "" {
		name = strings.TrimSpace("/" + command + " " + args)
	}
	return map[string]any{"role": "custom", "customType": "claude-command", "content": stripANSI(output), "display": true,
		"details": map[string]any{"command": name}, "timestamp": at}
}

// claudeLocalOutput gives the output of a local command of Claude Code, such
// as /context, from its saved message, or false for another message.
func claudeLocalOutput(text string) (string, bool) {
	text = strings.TrimSpace(text)
	for _, tag := range []string{"local-command-stdout", "local-command-stderr"} {
		open, closing := "<"+tag+">", "</"+tag+">"
		if strings.HasPrefix(text, open) {
			return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, open), closing)), true
		}
	}
	return "", false
}

var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// claudeInterrupt reports the saved text of a stopped turn.
func claudeInterrupt(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "[Request interrupted by user")
}

// claudeHistory reads a saved Claude Code conversation as Pi messages for the
// snapshot of the agent tab. It returns at most limit messages, the newest,
// and how many there are. costs gives the cost of each turn by the record of
// its last answer.
func claudeHistory(path string, limit int, costs map[string]claudeTurnCost) ([]any, int, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []any{}, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	history := &claudeHistoryReader{limit: limit, costs: costs, names: map[string]string{}}
	reader := bufio.NewReaderSize(file, 1<<20)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var row map[string]any
			if json.Unmarshal(line, &row) == nil {
				history.add(row)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, 0, readErr
		}
	}
	history.flush()
	return history.messages, history.total, nil
}

type claudeHistoryReader struct {
	limit, total int
	messages     []any
	costs        map[string]claudeTurnCost
	// names of the tool calls by ID, for their results.
	names map[string]string
	// The assistant message that its records build: Claude Code saves one
	// record for each content block of an answer.
	pending             map[string]any
	pendingID, lastUUID string
	compactTokens       float64
}

func (h *claudeHistoryReader) push(message map[string]any) {
	h.total++
	h.messages = append(h.messages, message)
	if h.limit > 0 && len(h.messages) > h.limit*2 {
		h.messages = append([]any(nil), h.messages[len(h.messages)-h.limit:]...)
	}
}

func (h *claudeHistoryReader) flush() {
	if h.pending != nil {
		if cost, ok := h.costs[h.lastUUID]; ok {
			usage := objectValue(h.pending["usage"])
			usage["cost"] = map[string]any{"total": cost.Cost}
		}
		h.push(h.pending)
		h.pending, h.pendingID, h.lastUUID = nil, "", ""
	}
	if h.limit > 0 && len(h.messages) > h.limit {
		h.messages = h.messages[len(h.messages)-h.limit:]
	}
}

func (h *claudeHistoryReader) add(row map[string]any) {
	if row["isSidechain"] == true {
		return
	}
	at := claudeTimestamp(row["timestamp"])
	switch row["type"] {
	case "system":
		if row["subtype"] == "local_command" {
			output, ok := claudeLocalOutput(stringValue(row["content"]))
			if !ok || output == "" {
				return
			}
			run := objectValue(row["commandRun"])
			h.flush()
			h.push(claudeCommandMessage(stringValue(run["command"]), stringValue(run["args"]), output, at))
			return
		}
		if row["subtype"] == "compact_boundary" {
			h.flush()
			metadata := objectValue(row["compactMetadata"])
			if metadata == nil {
				metadata = objectValue(row["compact_metadata"])
			}
			h.compactTokens, _ = metadata["preTokens"].(float64)
			if h.compactTokens == 0 {
				h.compactTokens, _ = metadata["pre_tokens"].(float64)
			}
		}
		return
	case "assistant":
		h.assistant(row, at)
		return
	case "attachment":
		// A message that the person sent while Claude Code worked reaches
		// the model inside the run.
		attachment := objectValue(row["attachment"])
		if attachment["type"] != "queued_command" || firstText(stringValue(attachment["commandMode"]), "prompt") != "prompt" {
			return
		}
		text := contentText(attachment["prompt"])
		if strings.TrimSpace(text) == "" {
			return
		}
		h.flush()
		if display, ok := nativeDeliveryDisplay(text); ok {
			h.push(map[string]any{"role": "custom", "customType": "galpon-operation", "content": display, "display": true, "timestamp": at})
			return
		}
		h.push(map[string]any{"role": "user", "content": claudeContent(attachment["prompt"]), "timestamp": at})
		return
	case "user":
	default:
		return
	}
	message := objectValue(row["message"])
	content := message["content"]
	var results []map[string]any
	for _, raw := range anyList(content) {
		if block := objectValue(raw); block["type"] == "tool_result" {
			results = append(results, block)
		}
	}
	if len(results) > 0 {
		h.flush()
		details := claudeToolDetails(row["toolUseResult"])
		for _, block := range results {
			id := stringValue(block["tool_use_id"])
			h.push(map[string]any{"role": "toolResult", "toolCallId": id, "toolName": h.names[id], "content": claudeContent(block["content"]),
				"details": details, "isError": block["is_error"] == true, "timestamp": at})
		}
		return
	}
	text := contentText(content)
	if row["isCompactSummary"] == true {
		h.flush()
		h.push(map[string]any{"role": "compactionSummary", "summary": claudeSummary(text), "tokensBefore": h.compactTokens, "timestamp": at})
		return
	}
	if objectValue(row["origin"])["kind"] == "task-notification" || strings.HasPrefix(strings.TrimSpace(text), "<task-notification>") {
		if notice, ok := claudeTaskEnd(text, at); ok {
			h.flush()
			h.push(notice)
		}
		return
	}
	if claudeInterrupt(text) {
		// The answer that streamed when the person stopped the run ends as
		// stopped, as in the live tab.
		if h.pending != nil {
			h.pending["stopReason"] = "aborted"
			h.flush()
			return
		}
		h.push(map[string]any{"role": "assistant", "content": []any{}, "stopReason": "aborted", "usage": claudeUsage(nil, 0), "provider": claudeProvider, "timestamp": at})
		return
	}
	if !isClaudeInput(row) {
		return
	}
	h.flush()
	if display, ok := nativeDeliveryDisplay(text); ok {
		h.push(map[string]any{"role": "custom", "customType": "galpon-operation", "content": display, "display": true, "timestamp": at})
		return
	}
	h.push(map[string]any{"role": "user", "content": claudeContent(content), "timestamp": at})
}

func (h *claudeHistoryReader) assistant(row map[string]any, at int64) {
	message := objectValue(row["message"])
	id := stringValue(message["id"])
	if h.pending != nil && (id == "" || id != h.pendingID) {
		h.flush()
	}
	if h.pending == nil {
		h.pending = map[string]any{"role": "assistant", "content": []any{}, "provider": claudeProvider, "model": stringValue(message["model"]), "timestamp": at}
		h.pendingID = id
	}
	content := h.pending["content"].([]any)
	calls := false
	for _, raw := range anyList(message["content"]) {
		block := claudeAssistantBlock(objectValue(raw))
		if block == nil {
			continue
		}
		if block["type"] == "toolCall" {
			h.names[stringValue(block["id"])] = stringValue(block["name"])
		}
		content = append(content, block)
	}
	for _, raw := range content {
		if objectValue(raw)["type"] == "toolCall" {
			calls = true
		}
	}
	h.pending["content"] = content
	h.pending["usage"] = claudeUsage(objectValue(message["usage"]), 0)
	h.pending["stopReason"] = claudeStopReason(stringValue(message["stop_reason"]), calls)
	if message["model"] == "<synthetic>" || row["isApiErrorMessage"] == true {
		h.pending["stopReason"] = "error"
		h.pending["errorMessage"] = contentText(message["content"])
		h.pending["content"] = []any{}
	}
	h.lastUUID = stringValue(row["uuid"])
}

var (
	taskTag    = regexp.MustCompile(`(?s)<(task-id|status|summary|result)>(.*?)</(?:task-id|status|summary|result)>`)
	quotedName = regexp.MustCompile(`"([^"]+)"`)
)

// claudeTaskEnd reads the notice that Claude Code gives the model when a
// background task or a subagent ends, as the notice of a background task of
// the tab.
func claudeTaskEnd(text string, at int64) (map[string]any, bool) {
	fields := map[string]string{}
	for _, match := range taskTag.FindAllStringSubmatch(text, -1) {
		if _, seen := fields[match[1]]; !seen {
			fields[match[1]] = strings.TrimSpace(match[2])
		}
	}
	if fields["task-id"] == "" {
		return nil, false
	}
	kind, name := "command", fields["summary"]
	if strings.HasPrefix(name, "Agent ") {
		kind = "agent"
	}
	if match := quotedName.FindStringSubmatch(name); match != nil {
		name = match[1]
	}
	return claudeTaskMessage(fields["task-id"], kind, name, fields["status"], fields["result"], at), true
}

// claudeTaskMessage is the conversation message of the end of a background
// task or a subagent.
func claudeTaskMessage(id, kind, description, status, result string, at int64) map[string]any {
	switch status {
	case "completed", "done", "":
		status = "completed"
	case "killed", "stopped", "cancelled":
		status = "stopped"
	default:
		status = "failed"
	}
	notice := map[string]any{"id": id, "kind": kind, "description": firstText(description, id), "status": status}
	if result = strings.TrimSpace(result); result != "" {
		if len(result) > 4000 {
			result = result[:4000] + "…"
		}
		notice["tail"] = result
	}
	return map[string]any{"role": "custom", "customType": "galpon-background", "content": "", "display": true,
		"details": map[string]any{"notices": []any{notice}}, "timestamp": at}
}

var (
	reminderBlock  = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>\s*`)
	summaryOpening = regexp.MustCompile(`^This session is being continued from a previous conversation that ran out of context\.[^\n]*\n*`)
	summaryClosing = regexp.MustCompile(`(?s)\n*If you need specific details from before compaction.*$`)
)

// claudeSummary gives the text of a compaction summary of Claude Code
// without the lines that only the model reads.
func claudeSummary(text string) string {
	text = summaryOpening.ReplaceAllString(strings.TrimSpace(text), "")
	text = reminderBlock.ReplaceAllString(text, "")
	text = summaryClosing.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// stripANSI removes the terminal styles of local command output.
func stripANSI(text string) string { return ansiSequence.ReplaceAllString(text, "") }
