package nativeagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The agent tab of the Herdr app draws the messages and the tool steps of Pi
// RPC. This file gives the items of a Codex thread in that form, for the live
// events and for the snapshot of a saved thread: a command becomes a bash
// step, or a read, ls or grep step when Codex knows what it does; a file
// change becomes an edit or a write step with its diff; an MCP call of the
// Galpon server keeps the name of its Galpon tool.

const codexProvider = "openai"

// codexCall is one tool step of a Codex item. A file change of several files
// gives one step for each file.
type codexCall struct {
	ID, Name string
	Args     map[string]any
}

// codexToolItems are the item types that run a tool.
var codexToolItems = map[string]bool{
	"commandExecution": true, "fileChange": true, "mcpToolCall": true, "dynamicToolCall": true,
	"webSearch": true, "collabAgentToolCall": true, "imageView": true, "imageGeneration": true,
}

var shellWrapper = regexp.MustCompile(`^(?:\S*/)?(?:bash|zsh|sh|fish)\s+-l?c\s+([\s\S]+)$`)

// codexCommand gives the command of a command item without the shell that
// Codex starts it with: "/usr/bin/zsh -lc 'ls'" is "ls".
func codexCommand(command string) string {
	match := shellWrapper.FindStringSubmatch(strings.TrimSpace(command))
	if match == nil {
		return command
	}
	quoted := strings.TrimSpace(match[1])
	if len(quoted) >= 2 && quoted[0] == '\'' && quoted[len(quoted)-1] == '\'' {
		return strings.ReplaceAll(quoted[1:len(quoted)-1], `'\''`, `'`)
	}
	if len(quoted) >= 2 && quoted[0] == '"' && quoted[len(quoted)-1] == '"' {
		var value string
		if json.Unmarshal([]byte(quoted), &value) == nil {
			return value
		}
	}
	return quoted
}

// relativePath gives a path inside the directory of the agent relative to
// it, and any other path as it is.
func relativePath(path, directory string) string {
	if path == "" || directory == "" {
		return path
	}
	if rest, err := filepath.Rel(directory, path); err == nil && rest != ".." && !strings.HasPrefix(rest, "../") {
		return rest
	}
	return path
}

// codexCalls gives the tool steps of an item, or nil for an item that runs no
// tool.
func codexCalls(item map[string]any, directory string) []codexCall {
	id := stringValue(item["id"])
	switch item["type"] {
	case "commandExecution":
		command := codexCommand(stringValue(item["command"]))
		actions := anyList(item["commandActions"])
		if len(actions) == 1 {
			action := objectValue(actions[0])
			switch action["type"] {
			case "read":
				return []codexCall{{id, "read", map[string]any{"path": relativePath(stringValue(action["path"]), directory), "command": command}}}
			case "listFiles":
				return []codexCall{{id, "ls", map[string]any{"path": firstText(relativePath(stringValue(action["path"]), directory), "."), "command": command}}}
			case "search":
				return []codexCall{{id, "grep", map[string]any{"pattern": stringValue(action["query"]), "path": relativePath(stringValue(action["path"]), directory), "command": command}}}
			}
		}
		return []codexCall{{id, "bash", map[string]any{"command": command}}}
	case "fileChange":
		changes := anyList(item["changes"])
		calls := make([]codexCall, 0, len(changes))
		for index, raw := range changes {
			change := objectValue(raw)
			name := "edit"
			if objectValue(change["kind"])["type"] == "add" {
				name = "write"
			}
			callID := id
			if len(changes) > 1 {
				callID = fmt.Sprintf("%s#%d", id, index)
			}
			calls = append(calls, codexCall{callID, name, map[string]any{"path": relativePath(stringValue(change["path"]), directory)}})
		}
		return calls
	case "mcpToolCall":
		arguments := objectValue(item["arguments"])
		if arguments == nil {
			arguments = map[string]any{}
		}
		server, tool := stringValue(item["server"]), stringValue(item["tool"])
		if server == "galpon" {
			return []codexCall{{id, tool, arguments}}
		}
		return []codexCall{{id, "mcp__" + server + "__" + tool, arguments}}
	case "dynamicToolCall":
		arguments := objectValue(item["arguments"])
		if arguments == nil {
			arguments = map[string]any{}
		}
		return []codexCall{{id, stringValue(item["tool"]), arguments}}
	case "webSearch":
		action := objectValue(item["action"])
		args := map[string]any{"query": firstText(stringValue(item["query"]), stringValue(action["query"]))}
		if queries := anyList(action["queries"]); len(queries) > 0 {
			args["queries"] = queries
		}
		if url := stringValue(action["url"]); url != "" {
			return []codexCall{{id, "fetch_content", map[string]any{"url": url}}}
		}
		return []codexCall{{id, "web_search", args}}
	case "collabAgentToolCall":
		prompt := stringValue(item["prompt"])
		return []codexCall{{id, "agent", map[string]any{"description": firstText(firstLine(prompt), stringValue(item["tool"])), "prompt": prompt}}}
	case "imageView":
		return []codexCall{{id, "view_image", map[string]any{"path": relativePath(stringValue(item["path"]), directory)}}}
	case "imageGeneration":
		return []codexCall{{id, "image_generation", map[string]any{"prompt": stringValue(item["revisedPrompt"])}}}
	}
	return nil
}

// codexResult gives the result of a step of a finished item: its content,
// its details and whether it failed.
func codexResult(item map[string]any, call int) (content []any, details map[string]any, failed bool) {
	status := stringValue(item["status"])
	failed = status == "failed" || status == "declined"
	text := func(value string) []any { return []any{map[string]any{"type": "text", "text": value}} }
	switch item["type"] {
	case "commandExecution":
		output := stringValue(item["aggregatedOutput"])
		if code, ok := item["exitCode"].(float64); ok && code != 0 {
			failed = true
			if strings.TrimSpace(output) == "" {
				output = fmt.Sprintf("Exit code %d", int(code))
			}
		}
		if status == "declined" && output == "" {
			output = "The person declined this command."
		}
		return text(output), nil, failed
	case "fileChange":
		changes := anyList(item["changes"])
		if call >= len(changes) {
			return text(""), nil, failed
		}
		change := objectValue(changes[call])
		diff := codexDiff(change)
		message := "Changed " + filepath.Base(stringValue(change["path"]))
		if status == "declined" {
			message = "The person declined this change."
		}
		if diff == "" {
			return text(message), nil, failed
		}
		return text(message), map[string]any{"diff": diff}, failed
	case "mcpToolCall":
		if failure := objectValue(item["error"]); failure != nil {
			return text(stringValue(failure["message"])), nil, true
		}
		result := objectValue(item["result"])
		if result["isError"] == true {
			failed = true
		}
		return codexMcpContent(anyList(result["content"])), nil, failed
	case "dynamicToolCall":
		var parts []any
		for _, raw := range anyList(item["contentItems"]) {
			block := objectValue(raw)
			if value := firstText(stringValue(block["text"]), stringValue(block["inputText"])); value != "" {
				parts = append(parts, map[string]any{"type": "text", "text": value})
			}
		}
		if item["success"] == false {
			failed = true
		}
		return parts, nil, failed
	case "collabAgentToolCall":
		var lines []string
		for id, raw := range objectValue(item["agentsStates"]) {
			state := objectValue(raw)
			line := id + ": " + stringValue(state["status"])
			if message := stringValue(state["message"]); message != "" {
				line += "\n" + message
			}
			lines = append(lines, line)
		}
		return text(strings.Join(lines, "\n\n")), nil, failed
	case "webSearch":
		return text(""), nil, failed
	}
	return text(""), nil, failed
}

// codexMcpContent gives the content of an MCP result: its text and images.
func codexMcpContent(blocks []any) []any {
	out := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		block := objectValue(raw)
		switch block["type"] {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": stringValue(block["text"])})
		case "image":
			out = append(out, map[string]any{"type": "image", "data": stringValue(block["data"]), "mimeType": stringValue(block["mimeType"])})
		case "resource":
			if text := stringValue(objectValue(block["resource"])["text"]); text != "" {
				out = append(out, map[string]any{"type": "text", "text": text})
			}
		}
	}
	return out
}

// codexDiff gives a unified diff of one file of a file change. Codex gives the
// hunks of an update, and the content of a new or a deleted file.
func codexDiff(change map[string]any) string {
	diff := strings.TrimSuffix(stringValue(change["diff"]), "\n")
	if diff == "" {
		return ""
	}
	var lines []string
	switch objectValue(change["kind"])["type"] {
	case "add":
		added := strings.Split(diff, "\n")
		lines = append(lines, fmt.Sprintf("@@ -0,0 +1,%d @@", len(added)))
		for _, line := range added {
			lines = append(lines, "+"+line)
		}
	case "delete":
		removed := strings.Split(diff, "\n")
		lines = append(lines, fmt.Sprintf("@@ -1,%d +0,0 @@", len(removed)))
		for _, line := range removed {
			lines = append(lines, "-"+line)
		}
	default:
		lines = strings.Split(diff, "\n")
	}
	if len(lines) > maxDiffLines {
		lines = append(lines[:maxDiffLines], fmt.Sprintf("… %d more lines", len(lines)-maxDiffLines))
	}
	return strings.Join(lines, "\n")
}

// codexUserContent gives the content blocks of Pi for the input of a user
// message: text and images.
func codexUserContent(input []any) []any {
	out := make([]any, 0, len(input))
	for _, raw := range input {
		part := objectValue(raw)
		switch part["type"] {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": stringValue(part["text"])})
		case "image":
			url := stringValue(part["url"])
			if head, data, ok := strings.Cut(url, ";base64,"); ok && strings.HasPrefix(head, "data:") {
				out = append(out, map[string]any{"type": "image", "data": data, "mimeType": strings.TrimPrefix(head, "data:")})
			}
		case "localImage":
			out = append(out, map[string]any{"type": "text", "text": "[image " + homePath(stringValue(part["path"])) + "]"})
		case "skill", "mention":
			if name := stringValue(part["name"]); name != "" {
				out = append(out, map[string]any{"type": "text", "text": "$" + name})
			}
		}
	}
	return out
}

// codexUserMessage gives the message that the tab shows for a user message of
// Codex: a Galpon delivery, or a message of the person.
func codexUserMessage(item map[string]any, at int64) map[string]any {
	content := codexUserContent(anyList(item["content"]))
	if display, ok := nativeDeliveryDisplay(contentText(content)); ok {
		return map[string]any{"role": "custom", "customType": "galpon-operation", "content": display, "display": true, "timestamp": at}
	}
	return map[string]any{"role": "user", "content": content, "timestamp": at}
}

// codexReasoning gives the text of a reasoning item: its summary, or its raw
// text when it has no summary.
func codexReasoning(item map[string]any) string {
	var parts []string
	for _, raw := range anyList(item["summary"]) {
		if text := strings.TrimSpace(stringValue(raw)); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		for _, raw := range anyList(item["content"]) {
			if text := strings.TrimSpace(stringValue(raw)); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// codexAssistant is an assistant message of Pi.
func codexAssistant(content []any, stop, modelName string, at int64) map[string]any {
	if content == nil {
		content = []any{}
	}
	return map[string]any{"role": "assistant", "content": content, "usage": claudeUsage(nil, 0), "stopReason": stop,
		"model": modelName, "provider": codexProvider, "timestamp": at}
}

func itemTime(item map[string]any, fallback int64) int64 {
	for _, key := range []string{"completedAtMs", "startedAtMs"} {
		if value, ok := item[key].(float64); ok && value > 0 {
			return int64(value)
		}
	}
	return fallback
}

// codexAnchors are items that the saved history of a thread always keeps.
// The saved history drops the commands and the other tools that code mode
// runs, so the app view keeps them in a journal of steps: each step has the
// number of anchors of its turn before it. Reasoning is not an anchor,
// because the saved history can drop a reasoning item without a summary.
var codexAnchors = map[string]bool{
	"userMessage": true, "agentMessage": true, "fileChange": true, "contextCompaction": true,
}

// codexJournaled tells that the journal keeps a step of the item: a tool
// that the saved history can drop.
func codexJournaled(kind string) bool { return codexToolItems[kind] && kind != "fileChange" }

// codexStep is an entry of the journal: a tool item, or the tokens of a
// turn, which the saved history does not give by turn.
type codexStep struct {
	Turn  string             `json:"turn"`
	After int                `json:"after,omitempty"`
	Item  map[string]any     `json:"item,omitempty"`
	Usage map[string]float64 `json:"usage,omitempty"`
}

// codexStepsLimit is how much of the end of the journal a snapshot reads.
const codexStepsLimit = 8 << 20

// readCodexSteps reads the journal of steps by turn.
func readCodexSteps(path string) map[string][]codexStep {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err == nil && info.Size() > codexStepsLimit {
		if _, err := file.Seek(info.Size()-codexStepsLimit, 0); err != nil {
			return nil
		}
	}
	steps := map[string][]codexStep{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var step codexStep
		if json.Unmarshal(scanner.Bytes(), &step) == nil && step.Turn != "" && (step.Item != nil || step.Usage != nil) {
			steps[step.Turn] = append(steps[step.Turn], step)
		}
	}
	return steps
}

// withSteps gives the items of a saved turn with the steps of the journal in
// their places. A turn whose saved history has its tools needs no journal.
func withSteps(items []any, entries []codexStep) []any {
	var steps []codexStep
	for _, entry := range entries {
		if entry.Item != nil {
			steps = append(steps, entry)
		}
	}
	if len(steps) == 0 {
		return items
	}
	for _, raw := range items {
		if codexJournaled(stringValue(objectValue(raw)["type"])) {
			return items
		}
	}
	out := make([]any, 0, len(items)+len(steps))
	next, anchors := 0, 0
	for _, raw := range items {
		if codexAnchors[stringValue(objectValue(raw)["type"])] {
			for next < len(steps) && steps[next].After <= anchors {
				out = append(out, steps[next].Item)
				next++
			}
			anchors++
		}
		out = append(out, raw)
	}
	for ; next < len(steps); next++ {
		out = append(out, steps[next].Item)
	}
	return out
}

// codexHistory gives the turns of a saved Codex thread as Pi messages for the
// snapshot of the agent tab, with the steps of the journal. It returns at most
// limit messages, the newest.
func codexHistory(turns []any, steps map[string][]codexStep, directory, modelName string, limit int) []any {
	var messages []any
	var pending map[string]any
	flush := func() {
		if pending != nil {
			messages = append(messages, pending)
			pending = nil
		}
	}
	add := func(block map[string]any, at int64) {
		if pending != nil {
			for _, raw := range pending["content"].([]any) {
				if objectValue(raw)["type"] == "toolCall" {
					flush()
					break
				}
			}
		}
		if pending == nil {
			pending = codexAssistant(nil, "stop", modelName, at)
		}
		pending["content"] = append(pending["content"].([]any), block)
	}
	for _, rawTurn := range turns {
		turn := objectValue(rawTurn)
		start := int64(0)
		if value, ok := turn["startedAt"].(float64); ok {
			start = int64(value) * 1000
		}
		first := len(messages)
		for _, rawItem := range withSteps(anyList(turn["items"]), steps[stringValue(turn["id"])]) {
			item := objectValue(rawItem)
			at := itemTime(item, start)
			switch kind := stringValue(item["type"]); {
			case kind == "userMessage":
				flush()
				messages = append(messages, codexUserMessage(item, at))
			case kind == "reasoning":
				if text := codexReasoning(item); text != "" {
					add(map[string]any{"type": "thinking", "thinking": text}, at)
				}
			case kind == "agentMessage" || kind == "plan":
				if text := stringValue(item["text"]); text != "" {
					add(map[string]any{"type": "text", "text": text}, at)
				}
			case kind == "contextCompaction":
				flush()
				messages = append(messages, map[string]any{"role": "compactionSummary", "summary": "", "timestamp": at})
			case codexToolItems[kind]:
				calls := codexCalls(item, directory)
				if pending == nil {
					pending = codexAssistant(nil, "toolUse", modelName, at)
				}
				for _, call := range calls {
					pending["content"] = append(pending["content"].([]any), map[string]any{"type": "toolCall", "id": call.ID, "name": call.Name, "arguments": call.Args})
				}
				pending["stopReason"] = "toolUse"
				flush()
				for index, call := range calls {
					content, details, failed := codexResult(item, index)
					result := map[string]any{"role": "toolResult", "toolCallId": call.ID, "toolName": call.Name, "content": content, "isError": failed, "timestamp": at}
					if details != nil {
						result["details"] = details
					}
					messages = append(messages, result)
				}
			}
		}
		flush()
		// The tokens of the turn go on its last answer, which ends the turn.
		for _, entry := range steps[stringValue(turn["id"])] {
			if entry.Usage == nil {
				continue
			}
			for index := len(messages) - 1; index >= first; index-- {
				if message := objectValue(messages[index]); message["role"] == "assistant" {
					usage := claudeUsage(nil, 0)
					usage["input"], usage["output"], usage["cacheRead"] = entry.Usage["input"], entry.Usage["output"], entry.Usage["cacheRead"]
					usage["totalTokens"] = entry.Usage["input"] + entry.Usage["output"] + entry.Usage["cacheRead"]
					message["usage"] = usage
					break
				}
			}
		}
		switch turn["status"] {
		case "interrupted":
			messages = append(messages, codexAssistant(nil, "aborted", modelName, start))
		case "failed":
			failure := codexAssistant(nil, "error", modelName, start)
			failure["errorMessage"] = firstText(stringValue(objectValue(turn["error"])["message"]), "The turn failed.")
			messages = append(messages, failure)
		}
	}
	if limit > 0 && len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	if messages == nil {
		messages = []any{}
	}
	return messages
}
