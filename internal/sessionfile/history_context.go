package sessionfile

import (
	"strings"
)

func (h *History) humanInput(row map[string]any) bool {
	message := object(row["message"])
	if h.Harness == "codex" {
		message = object(row["payload"])
		if row["type"] != "response_item" || message["type"] != "message" {
			return false
		}
	}
	if message["role"] != "user" || row["isCompactSummary"] == true || row["isMeta"] == true || row["galponHistoricalContext"] == true || message["galponHistoricalContext"] == true {
		return false
	}
	for _, kind := range array(object(message["internal_chat_message_metadata_passthrough"])["content_item_kinds"]) {
		if kind == "compaction.summary" {
			return false
		}
	}
	for _, raw := range array(message["content"]) {
		if object(raw)["type"] == "tool_result" {
			return false
		}
	}
	return !sourceEnvironment(message) && !localClaudeCommand(message)
}

func localClaudeCommand(message map[string]any) bool {
	text := str(message["content"])
	return strings.HasPrefix(text, "<local-command-caveat>") || strings.HasPrefix(text, "<command-name>") || strings.HasPrefix(text, "<local-command-stdout>")
}

func sourceEnvironment(message map[string]any) bool {
	kinds := array(object(message["internal_chat_message_metadata_passthrough"])["content_item_kinds"])
	if len(kinds) > 0 {
		for _, kind := range kinds {
			if strings.HasPrefix(str(kind), "user.") || kind == "compaction.summary" {
				return false
			}
		}
		return message["role"] == "user"
	}
	for _, part := range array(message["content"]) {
		text := str(object(part)["text"])
		if strings.HasPrefix(text, "<environment_context>") || strings.HasPrefix(text, "# AGENTS.md instructions for ") {
			return true
		}
	}
	return false
}

func (h *History) projectPi(branch []int) error {
	compaction := -1
	edits := make(map[string]map[string]any)
	for position, index := range branch {
		row, err := h.entry(index)
		if err != nil {
			return err
		}
		switch row["type"] {
		case "compaction":
			compaction = position
		case "context_edit":
			edits[str(row["targetId"])] = row
		}
	}
	selected := branch
	if compaction >= 0 {
		index := branch[compaction]
		row, err := h.entry(index)
		if err != nil {
			return err
		}
		first := compaction
		for at := 0; at < compaction; at++ {
			if h.entries[branch[at]].id == str(row["firstKeptEntryId"]) {
				first = at
				break
			}
		}
		selected = append([]int{index}, branch[first:compaction]...)
		selected = append(selected, branch[compaction+1:]...)
	}
	for _, index := range selected {
		row, err := h.entry(index)
		if err != nil {
			return err
		}
		if object(row["message"])["excludeFromContext"] == true {
			continue
		}
		refs := h.byEntry[index]
		if edit, ok := edits[h.entries[index].id]; ok {
			replacement := edit["replacement"]
			if replacement == nil {
				continue
			}
			if value := object(replacement); value != nil {
				replacement = value["content"]
			}
			if row["type"] == "custom_message" {
				row["content"] = replacement
			} else if message := object(row["message"]); message != nil {
				message["content"] = replacement
			}
			messages, err := h.convertRecord(row)
			if err != nil {
				return err
			}
			human := h.humanInput(row)
			if historical, ok := edit["galponHistoricalContext"].(bool); ok {
				human = !historical && object(row["message"])["role"] == "user"
			}
			refs, err = h.storeMessages(messages, human)
			if err != nil {
				return err
			}
		}
		h.context = append(h.context, refs...)
	}
	return nil
}
