package sessionfile

import (
	"fmt"
	"strconv"
)

func opaqueCodexCheckpoint(payload map[string]any) bool {
	for _, item := range array(payload["replacement_history"]) {
		value := object(item)
		if (value["type"] == "compaction" || value["type"] == "context_compaction" || value["type"] == "compaction_summary") && str(value["encrypted_content"]) != "" {
			return true
		}
	}
	return false
}

// Checkpoints in rolled-back turns cannot select the active context. A saved
// window with resume metadata is an explicit replay boundary in Codex.
func (h *History) codexCheckpoint(branch []int) (int, error) {
	starts := false
	latest := -1
	paginated := false
	for position, index := range branch {
		row, err := h.entry(index)
		if err != nil {
			return -1, err
		}
		payload := object(row["payload"])
		starts = starts || (row["type"] == "event_msg" && payload["type"] == "task_started")
		paginated = paginated || (row["type"] == "session_meta" && payload["history_mode"] == "paginated")
		if row["type"] == "compacted" {
			latest = position
		}
	}
	if latest >= 0 {
		row, err := h.entry(branch[latest])
		if err != nil {
			return -1, err
		}
		payload := object(row["payload"])
		if array(payload["replacement_history"]) != nil && payload["window_number"] != nil && (payload["resume_metadata"] != nil || paginated) && !opaqueCodexCheckpoint(payload) {
			return latest, nil
		}
	}
	pending, candidate := 0, -1
	human := false
	for position := len(branch) - 1; position >= 0; position-- {
		row, err := h.entry(branch[position])
		if err != nil {
			return -1, err
		}
		payload := object(row["payload"])
		if row["type"] == "event_msg" && payload["type"] == "thread_rolled_back" {
			count, err := rollbackCount(payload)
			if err != nil {
				return -1, err
			}
			pending += count
		}
		if row["type"] == "compacted" && candidate < 0 && array(payload["replacement_history"]) != nil && !opaqueCodexCheckpoint(payload) {
			candidate = position
		}
		input := h.humanInput(row)
		human = human || input || (row["type"] == "event_msg" && payload["type"] == "user_message")
		if (starts && row["type"] == "event_msg" && payload["type"] == "task_started") || (!starts && input) {
			if pending == 0 && candidate >= 0 {
				return candidate, nil
			}
			if pending > 0 && human {
				pending--
			}
			candidate, human = -1, false
		}
	}
	if pending == 0 {
		return candidate, nil
	}
	return -1, nil
}

func rollbackCount(payload map[string]any) (int, error) {
	count, err := strconv.Atoi(fmt.Sprint(payload["num_turns"]))
	if err != nil || count < 0 {
		return 0, fmt.Errorf("invalid saved rollback count")
	}
	return count, nil
}

func (h *History) projectCodex(branch []int) error {
	checkpoint, err := h.codexCheckpoint(branch)
	if err != nil {
		return err
	}
	selected := branch
	if checkpoint >= 0 {
		selected = branch[checkpoint:]
	}
	for position, index := range selected {
		row, err := h.entry(index)
		if err != nil {
			return err
		}
		payload := object(row["payload"])
		switch row["type"] {
		case "compacted":
			items, replacement := payload["replacement_history"].([]any)
			if replacement {
				if opaqueCodexCheckpoint(payload) {
					// Only the source provider can decode this state. Keep readable
					// history and show the fallback in the target, without claiming
					// that it has the same compacted context.
					refs, err := h.storeMessages([]HistoryMessage{{Role: "user", Text: "[The source provider's context checkpoint is encrypted. This fork uses saved readable history before that checkpoint. Exact compacted context is not available to another provider; the target may need its own compaction.]"}}, false)
					if err != nil {
						return err
					}
					h.context = append(h.context, refs...)
					continue
				}
				if checkpoint < 0 || position != 0 {
					continue
				}
				h.context = nil
				for _, item := range items {
					nested := map[string]any{"type": "response_item", "timestamp": row["timestamp"], "payload": item}
					if sourceEnvironment(object(item)) {
						continue
					}
					messages, err := h.convertRecord(nested)
					if err != nil {
						return err
					}
					refs, err := h.storeMessages(messages, h.humanInput(nested))
					if err != nil {
						return err
					}
					h.context = append(h.context, refs...)
				}
			} else {
				var kept []int
				for _, ref := range h.context {
					if h.refs[ref].human {
						kept = append(kept, ref)
					}
				}
				h.context = append(kept, h.byEntry[index]...)
			}
		case "event_msg":
			if payload["type"] == "thread_rolled_back" {
				count, err := rollbackCount(payload)
				if err != nil {
					return err
				}
				for at := len(h.context) - 1; at >= 0 && count > 0; at-- {
					if h.refs[h.context[at]].human {
						h.context = h.context[:at]
						count--
					}
				}
			}
		default:
			if !sourceEnvironment(payload) {
				h.context = append(h.context, h.byEntry[index]...)
			}
		}
	}
	return nil
}
