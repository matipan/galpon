package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type nativeModel struct {
	mu       sync.Mutex
	requests map[string]int
	blocked  map[string]chan struct{}
}

type nativeAnswer struct {
	text, tool, namespace, search string
	block                         chan struct{}
	args                          map[string]any
}

var nativeCommand = regexp.MustCompile(`NATIVE_(REMEMBER|RECALL|SEND|NOTIFY|WORK|BLOCK|IMAGE) ([a-zA-Z0-9_-]+)(?: ([a-zA-Z0-9_-]+))?`)
var nativeMessageID = regexp.MustCompile(`message:[a-f0-9]{64}`)

func nativeObject(value any) map[string]any { object, _ := value.(map[string]any); return object }
func nativeString(value any) string         { text, _ := value.(string); return text }
func nativeText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	var texts []string
	for _, raw := range nativeArray(value) {
		block := nativeObject(raw)
		if block["type"] == "tool_result" {
			continue
		}
		if text := nativeString(block["text"]); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}
func nativeArray(value any) []any { items, _ := value.([]any); return items }

type nativeTool struct{ name, namespace string }

func nativeTools(value any, namespace string) []nativeTool {
	var result []nativeTool
	for _, raw := range nativeArray(value) {
		tool := nativeObject(raw)
		if tool["type"] == "namespace" {
			result = append(result, nativeTools(tool["tools"], nativeString(tool["name"]))...)
			continue
		}
		name := nativeString(tool["name"])
		if name == "" {
			name = nativeString(nativeObject(tool["function"])["name"])
		}
		if name != "" {
			result = append(result, nativeTool{name, namespace})
		}
	}
	return result
}

func (m *nativeModel) answer(body map[string]any) (nativeAnswer, error) {
	messages := nativeArray(body["input"])
	if body["messages"] != nil {
		messages = nativeArray(body["messages"])
	}
	var history strings.Builder
	var command []string
	last := 0
	currentInput := ""
	for i, raw := range messages {
		message := nativeObject(raw)
		if message["role"] != "user" {
			continue
		}
		text := nativeText(message["content"])
		history.WriteString(text)
		matches := nativeCommand.FindAllStringSubmatch(text, -1)
		if len(matches) > 0 {
			command = matches[len(matches)-1]
			last = i
			currentInput = text
		}
	}
	if len(command) == 0 {
		return nativeAnswer{text: "Fixture ready"}, nil
	}
	m.mu.Lock()
	if m.requests == nil {
		m.requests = make(map[string]int)
	}
	m.requests[command[0]]++
	count := m.requests[command[0]]
	m.mu.Unlock()
	if count > 16 {
		return nativeAnswer{}, fmt.Errorf("mock request budget exceeded: %s", command[0])
	}
	available := nativeTools(body["tools"], "")
	var outputs []any
	for i, raw := range messages {
		item := nativeObject(raw)
		if item["type"] == "tool_search_output" || item["type"] == "additional_tools" {
			available = append(available, nativeTools(item["tools"], "")...)
		}
		if i <= last {
			continue
		}
		if item["type"] == "function_call_output" {
			outputs = append(outputs, item["output"])
		}
		for _, rawBlock := range nativeArray(item["content"]) {
			block := nativeObject(rawBlock)
			if block["type"] == "tool_result" && block["is_error"] != true {
				outputs = append(outputs, block["content"])
			}
		}
	}
	wire, _ := json.Marshal(outputs)
	output := string(wire)
	answer := nativeAnswer{}
	var wanted string
	switch command[1] {
	case "IMAGE":
		if !nativeHasImage(messages[last:]) {
			return nativeAnswer{}, fmt.Errorf("native model request lost the attached image")
		}
		return nativeAnswer{text: "image:" + command[2]}, nil
	case "REMEMBER":
		return nativeAnswer{text: "remembered:" + command[2]}, nil
	case "RECALL":
		if !strings.Contains(history.String(), "NATIVE_REMEMBER "+command[2]) {
			return nativeAnswer{}, fmt.Errorf("native conversation lost its remembered value")
		}
		return nativeAnswer{text: "recalled:" + command[2]}, nil
	case "BLOCK":
		if !strings.Contains(history.String(), "NATIVE_REMEMBER heron_"+command[2]) {
			return nativeAnswer{}, fmt.Errorf("recovered native turn lost its conversation")
		}
		if count == 1 {
			return nativeAnswer{block: m.blocked[command[2]]}, nil
		}
		return nativeAnswer{text: "recovered:" + command[2]}, nil
	case "WORK":
		if strings.Contains(output, "checkpoint:"+command[2]) {
			return nativeAnswer{text: "done:" + command[2]}, nil
		}
		wanted = "galpon_report_progress"
		answer.args = map[string]any{"phase": "working", "summary": "checkpoint:" + command[2]}
	case "SEND", "NOTIFY":
		if strings.Contains(output, "done:"+command[3]) || strings.Contains(currentInput, "done:"+command[3]) {
			return nativeAnswer{text: "received:" + command[3]}, nil
		}
		if ids := nativeMessageID.FindAllString(output, -1); len(ids) > 0 {
			if command[1] == "NOTIFY" {
				return nativeAnswer{text: "Waiting for the delegated result"}, nil
			}
			wanted = "galpon_await_agent"
			answer.args = map[string]any{"message_id": ids[len(ids)-1], "timeout_seconds": 20}
		} else {
			wanted = "galpon_send_agent"
			answer.args = map[string]any{"agent": command[2], "prompt": "NATIVE_WORK " + command[3]}
		}
	}
	for _, tool := range available {
		if strings.HasSuffix(tool.name, wanted) {
			answer.tool, answer.namespace = tool.name, tool.namespace
			return answer, nil
		}
	}
	for _, raw := range nativeArray(body["tools"]) {
		if nativeObject(raw)["type"] == "tool_search" {
			answer.search = "galpon " + wanted
			return answer, nil
		}
	}
	return nativeAnswer{}, fmt.Errorf("native harness did not expose %s", wanted)
}

func nativeHasImage(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		if item["type"] == "input_image" && item["image_url"] != nil || item["type"] == "image" && nativeObject(item["source"])["data"] != nil {
			return true
		}
		for _, field := range item {
			if nativeHasImage(field) {
				return true
			}
		}
	case []any:
		for _, field := range item {
			if nativeHasImage(field) {
				return true
			}
		}
	}
	return false
}

func (m *nativeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") && !strings.HasSuffix(r.URL.Path, "/messages") {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	answer, err := m.answer(body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if answer.block != nil {
		close(answer.block)
		<-r.Context().Done()
		return
	}
	var events []map[string]any
	id := fmt.Sprintf("native_%d", time.Now().UnixNano())
	if strings.HasSuffix(r.URL.Path, "/messages") {
		events = append(events, map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant", "model": body["model"], "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 20, "output_tokens": 0, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}}})
		reason := "end_turn"
		if answer.tool != "" {
			arguments, _ := json.Marshal(answer.args)
			events = append(events, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_" + id, "name": answer.tool, "input": map[string]any{}}}, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(arguments)}})
			reason = "tool_use"
		} else {
			events = append(events, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": answer.text}})
		}
		events = append(events, map[string]any{"type": "content_block_stop", "index": 0}, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 5}}, map[string]any{"type": "message_stop"})
	} else {
		item := map[string]any{"type": "message", "id": "item_" + id, "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer.text, "annotations": []any{}}}}
		if answer.search != "" {
			item = map[string]any{"type": "tool_search_call", "id": "item_" + id, "call_id": "call_" + id, "execution": "client", "status": "completed", "arguments": map[string]any{"query": answer.search, "limit": 5}}
		} else if answer.tool != "" {
			arguments, _ := json.Marshal(answer.args)
			item = map[string]any{"type": "function_call", "id": "item_" + id, "call_id": "call_" + id, "name": answer.tool, "arguments": string(arguments), "status": "completed"}
			if answer.namespace != "" {
				item["namespace"] = answer.namespace
			}
		}
		events = []map[string]any{{"type": "response.created", "response": map[string]any{"id": "resp_" + id, "status": "in_progress", "output": []any{}}}, {"type": "response.output_item.added", "output_index": 0, "item": item}, {"type": "response.output_item.done", "output_index": 0, "item": item}, {"type": "response.completed", "response": map[string]any{"id": "resp_" + id, "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 20, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 5, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 25}}}}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for _, event := range events {
		data, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
	}
}
