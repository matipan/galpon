package sessionfile

import (
	"encoding/json"
	"fmt"
	"strings"
)

func historicalText(harness, kind string, value any) string {
	data, _ := json.MarshalIndent(value, "", "  ")
	return "Historical " + kind + " from " + harness + " (saved record; not executable):\n" + string(data)
}

func (h *History) parts(content any) ([]HistoryPart, error) {
	if text, ok := content.(string); ok {
		return []HistoryPart{{Type: "text", Text: text}}, nil
	}
	if content == nil {
		return nil, nil
	}
	blocks, ok := content.([]any)
	if !ok {
		return []HistoryPart{{Type: "text", Text: historicalText(h.Harness, "content", content)}}, nil
	}
	var parts []HistoryPart
	for _, value := range blocks {
		block := object(value)
		switch block["type"] {
		case "text", "input_text", "output_text", "summary_text", "reasoning_text":
			text, ok := block["text"].(string)
			if !ok {
				return nil, fmt.Errorf("invalid text content")
			}
			parts = append(parts, HistoryPart{Type: "text", Text: text})
		case "image", "input_image", "image_url", "local_image", "document", "input_audio", "audio", "input_file", "file":
			if source := object(block["source"]); source["type"] == "content" {
				nested, err := h.parts(source["content"])
				if err != nil {
					return nil, err
				}
				parts = append(parts, nested...)
				continue
			}
			part, err := h.media(block)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case "resource", "resource_link":
			resource := block
			if block["type"] == "resource" {
				resource = object(block["resource"])
			}
			if text, ok := resource["text"].(string); ok {
				parts = append(parts, HistoryPart{Type: "text", Text: "Historical resource " + str(resource["uri"]) + ":\n" + text})
				continue
			}
			media := map[string]any{"type": "file", "mimeType": resource["mimeType"], "data": resource["blob"], "filename": resource["name"]}
			if str(resource["blob"]) == "" {
				media["url"] = resource["uri"]
			}
			part, err := h.media(media)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case "thinking", "reasoning":
			text := str(block["thinking"])
			if text == "" {
				text = str(block["text"])
			}
			if text != "" {
				parts = append(parts, HistoryPart{Type: "text", Text: "Historical reasoning from " + h.Harness + ":\n" + text})
			}
		case "redacted_thinking":
			parts = append(parts, HistoryPart{Type: "text", Text: "[The source provider stored an opaque reasoning block. Its original record is retained in the fork's source snapshot.]"})
		default:
			parts = append(parts, HistoryPart{Type: "text", Text: historicalText(h.Harness, "content", value)})
		}
	}
	return parts, nil
}

func historyMessage(role, timestamp string, parts []HistoryPart) HistoryMessage {
	var text []string
	for _, part := range parts {
		if part.Type == "text" {
			text = append(text, part.Text)
		}
	}
	return HistoryMessage{Role: role, Text: strings.Join(text, "\n"), Parts: parts, Time: timestamp}
}

func (h *History) convertMessage(message map[string]any, timestamp string) ([]HistoryMessage, error) {
	role := str(message["role"])
	if role == "system" || role == "developer" {
		return nil, nil
	}
	if role == "custom" || role == "hookMessage" {
		role = "user"
	}
	if role == "branchSummary" || role == "compactionSummary" {
		return []HistoryMessage{{Role: "user", Text: str(message["summary"]), Time: timestamp}}, nil
	}
	if role == "bashExecution" {
		return []HistoryMessage{{Role: "user", Text: historicalText(h.Harness, "shell command", message), Time: timestamp}}, nil
	}
	if role == "toolResult" {
		parts, err := h.parts(message["content"])
		if err != nil {
			return nil, err
		}
		prefix := map[string]any{"type": "result", "callId": message["toolCallId"], "name": message["toolName"], "isError": message["isError"]}
		parts = append([]HistoryPart{{Type: "text", Text: historicalText(h.Harness, "tool record", prefix)}}, parts...)
		return []HistoryMessage{historicalMediaMessage(timestamp, parts)}, nil
	}
	if role != "user" && role != "assistant" {
		return []HistoryMessage{{Role: "assistant", Text: historicalText(h.Harness, "message", message), Time: timestamp}}, nil
	}
	var out []HistoryMessage
	var pending []any
	flush := func() error {
		parts, err := h.parts(pending)
		if err != nil {
			return err
		}
		if len(parts) != 0 {
			value := historyMessage(role, timestamp, parts)
			if role == "assistant" {
				for _, part := range parts {
					if part.Type != "text" {
						value = historicalMediaMessage(timestamp, parts)
						break
					}
				}
			}
			out = append(out, value)
		}
		pending = nil
		return nil
	}
	content := message["content"]
	if text, ok := content.(string); ok {
		return []HistoryMessage{{Role: role, Text: text, Time: timestamp}}, nil
	}
	if content != nil && array(content) == nil {
		return []HistoryMessage{{Role: role, Text: historicalText(h.Harness, "message content", content), Time: timestamp}}, nil
	}
	for _, raw := range array(content) {
		block := object(raw)
		switch block["type"] {
		case "tool_use", "toolCall", "server_tool_use":
			if err := flush(); err != nil {
				return nil, err
			}
			out = append(out, HistoryMessage{Role: "assistant", Text: historicalText(h.Harness, "tool record", block), Time: timestamp})
		case "tool_result":
			if err := flush(); err != nil {
				return nil, err
			}
			parts, err := h.parts(block["content"])
			if err != nil {
				return nil, err
			}
			prefix := map[string]any{"type": "result", "callId": block["tool_use_id"], "isError": block["is_error"]}
			parts = append([]HistoryPart{{Type: "text", Text: historicalText(h.Harness, "tool record", prefix)}}, parts...)
			out = append(out, historicalMediaMessage(timestamp, parts))
		default:
			pending = append(pending, raw)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

func historicalMediaMessage(timestamp string, parts []HistoryPart) HistoryMessage {
	role := "assistant"
	for _, part := range parts {
		if part.Type != "text" {
			role = "user"
			parts = append([]HistoryPart{{Type: "text", Text: "[Historical attachment from the source conversation; not a new user request.]"}}, parts...)
			break
		}
	}
	return historyMessage(role, timestamp, parts)
}

func (h *History) convertRecord(row map[string]any) ([]HistoryMessage, error) {
	typ, timestamp := str(row["type"]), str(row["timestamp"])
	summary := func(text string) ([]HistoryMessage, error) {
		if text == "" {
			return nil, nil
		}
		return []HistoryMessage{{Role: "user", Text: text, Time: timestamp}}, nil
	}
	switch h.Harness {
	case "pi":
		switch typ {
		case "message":
			return h.convertMessage(object(row["message"]), timestamp)
		case "custom_message":
			return h.convertMessage(map[string]any{"role": "user", "content": row["content"]}, timestamp)
		case "compaction", "branch_summary":
			return summary(str(row["summary"]))
		case "session", "custom", "usage", "label", "session_info", "model_change", "thinking_level_change", "context_edit":
			return nil, nil
		}
	case "claude":
		switch typ {
		case "user", "assistant":
			return h.convertMessage(object(row["message"]), timestamp)
		case "attachment":
			attachment := object(row["attachment"])
			switch attachment["type"] {
			case "environment", "model", "session_context", "date", "total_tokens_reminder", "prompt_snapshot", "agent_listing_delta", "skill_listing", "remote_session_change":
				return nil, nil
			}
			if attachment["content"] != nil {
				return h.convertMessage(map[string]any{"role": "user", "content": attachment["content"]}, timestamp)
			}
			return []HistoryMessage{{Role: "assistant", Text: historicalText(h.Harness, "attachment", attachment), Time: timestamp}}, nil
		case "system":
			if row["subtype"] == "local_command" {
				return summary(str(row["content"]))
			}
			return nil, nil
		case "progress", "file-history-snapshot", "queue-operation", "last-prompt", "saved_hook_context", "turn_duration", "summary", "atis-latch", "mode", "cost-state", "attribution-snapshot":
			return nil, nil
		}
	case "codex":
		payload := object(row["payload"])
		switch typ {
		case "session_meta", "turn_context", "token_usage_record", "world_state", "retained_context", "event_msg":
			return nil, nil
		case "compacted":
			return summary(str(payload["message"]))
		case "response_item":
			switch payload["type"] {
			case "message":
				return h.convertMessage(payload, timestamp)
			case "function_call_output", "custom_tool_call_output":
				parts, err := h.parts(payload["output"])
				if err != nil {
					return nil, err
				}
				parts = append([]HistoryPart{{Type: "text", Text: historicalText(h.Harness, "tool record", map[string]any{"type": "result", "callId": payload["call_id"], "name": payload["name"]})}}, parts...)
				return []HistoryMessage{historicalMediaMessage(timestamp, parts)}, nil
			case "reasoning":
				parts, err := h.parts(payload["summary"])
				if err != nil {
					return nil, err
				}
				more, err := h.parts(payload["content"])
				parts = append(parts, more...)
				if err != nil || len(parts) == 0 {
					return nil, err
				}
				parts = append([]HistoryPart{{Type: "text", Text: "Historical reasoning from Codex:"}}, parts...)
				return []HistoryMessage{historyMessage("assistant", timestamp, parts)}, nil
			case "image_generation_call":
				part, err := h.media(map[string]any{"type": "image", "mimeType": "image/png", "data": payload["result"]})
				if err != nil {
					return nil, err
				}
				return []HistoryMessage{historicalMediaMessage(timestamp, []HistoryPart{{Type: "text", Text: "Historical image generation: " + str(payload["revised_prompt"])}, part})}, nil
			case "compaction", "compaction_summary", "context_compaction", "configuration_update":
				return nil, nil
			default:
				return []HistoryMessage{{Role: "assistant", Text: historicalText(h.Harness, "tool record", payload), Time: timestamp}}, nil
			}
		}
	}
	return []HistoryMessage{{Role: "assistant", Text: historicalText(h.Harness, "conversation record", row), Time: timestamp}}, nil
}
