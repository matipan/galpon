package nativeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

func (t *transcript) conversation(row map[string]any) {
	created, err := time.Parse(time.RFC3339Nano, stringValue(row["timestamp"]))
	if err != nil {
		created = time.Now()
	}
	add := func(kind, id, role, content, tool, callID string, failed bool) {
		if id == "" {
			return
		}
		if len(content) > 64<<10 {
			content = content[:(64<<10)-3]
			for !utf8.ValidString(content) {
				content = content[:len(content)-1]
			}
			content += "…"
		}
		entryID := t.kind + ":" + id
		t.events = append(t.events, model.ConversationEvent{EventID: entryID + ":" + kind, PiEntryID: entryID, Kind: kind, Role: role, Content: content,
			ToolName: tool, ToolCallID: callID, IsError: failed, CreatedAt: created.UnixMilli()})
	}
	if t.kind == model.HarnessClaude {
		if row["isSidechain"] == true {
			return
		}
		id := stringValue(row["uuid"])
		message := objectValue(row["message"])
		blocks, _ := message["content"].([]any)
		toolOutput := false
		for _, raw := range blocks {
			block := objectValue(raw)
			switch block["type"] {
			case "tool_use":
				input, _ := json.Marshal(block["input"])
				callID := stringValue(block["id"])
				add("tool_execution_start", id+":"+callID, "tool", string(input), stringValue(block["name"]), callID, false)
			case "tool_result":
				toolOutput = true
				callID := stringValue(block["tool_use_id"])
				add("tool_execution_end", id+":"+callID, "tool", contentText(block["content"]), "", callID, block["is_error"] == true)
			}
		}
		text := contentText(message["content"])
		if message["role"] == "user" && !toolOutput && isClaudeInput(row) {
			add("user_message", id, "user", text, "", "", false)
		}
		if message["role"] == "assistant" && text != "" {
			add("assistant_message_end", id, "assistant", text, "", "", false)
		}
		return
	}
	if row["type"] != "response_item" {
		return
	}
	payload := objectValue(row["payload"])
	id := stringValue(payload["id"])
	if id == "" {
		if call := stringValue(payload["call_id"]); call != "" {
			id = "call:" + call
		} else {
			id = fmt.Sprintf("record:%d", t.offset)
		}
	}
	switch payload["type"] {
	case "message":
		role := stringValue(payload["role"])
		if role == "user" {
			add("user_message", id, role, contentText(payload["content"]), "", "", false)
		}
		if role == "assistant" {
			add("assistant_message_end", id, role, contentText(payload["content"]), "", "", false)
		}
	case "function_call":
		add("tool_execution_start", id, "tool", stringValue(payload["arguments"]), stringValue(payload["name"]), stringValue(payload["call_id"]), false)
	case "function_call_output":
		add("tool_execution_end", id, "tool", contentText(payload["output"]), "", stringValue(payload["call_id"]), false)
	}
}

func (c *runtimeController) flushConversation(ctx context.Context) error {
	for len(c.transcript.events) > 0 {
		batch := c.transcript.events[:min(100, len(c.transcript.events))]
		for i := range batch {
			if batch[i].Kind != "user_message" {
				continue
			}
			for _, job := range c.jobs {
				if job.Token == "" || !strings.Contains(batch[i].Content, deliveryMarker(job.Token)) {
					continue
				}
				batch[i].IsAgentDelivery = true
				batch[i].DeliveryKind = job.DeliveryKind
				batch[i].DeliverySenderTitle = job.SenderTitle
				if job.DisplayPrompt != "" {
					batch[i].Content = job.DisplayPrompt
				}
				break
			}
		}
		if err := c.client.RuntimeConversation(ctx, c.agent.ID, app.ConversationEventsRequest{RuntimeID: c.runtimeID, Events: batch}); err != nil {
			return err
		}
		c.transcript.events = c.transcript.events[len(batch):]
	}
	return nil
}
