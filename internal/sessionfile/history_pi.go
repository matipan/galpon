package sessionfile

import "github.com/google/uuid"

type historySlot struct{ id, role string }

// Pi context edits change model content without removing the original messages
// from its terminal history. Match roles in order, then append any required
// slots. Each replacement keeps the effective message's role and content.
func (w *historyWriter) piProjection() error {
	original := len(w.piEntries)
	assigned := make(map[string]int)
	cursor := 0
	for _, ref := range w.history.context {
		message, err := w.history.message(ref)
		if err != nil {
			return err
		}
		for cursor < len(w.piEntries) && w.piEntries[cursor].role != message.Role {
			cursor++
		}
		if cursor == len(w.piEntries) {
			if err := w.message(message); err != nil {
				return err
			}
		}
		assigned[w.piEntries[cursor].id] = ref
		cursor++
	}
	for index, slot := range w.piEntries[:original] {
		ref, exists := assigned[slot.id]
		if exists && ref == w.history.archive[index] {
			continue
		}
		var replacement any
		historical := true
		if exists {
			message, err := w.history.message(ref)
			if err != nil {
				return err
			}
			content, err := w.content(message)
			if err != nil {
				return err
			}
			replacement = map[string]any{"content": content}
			historical = !message.Human
		}
		id := uuid.NewString()
		if err := w.write(map[string]any{"type": "context_edit", "id": id, "parentId": w.parent, "timestamp": w.stamp, "targetId": slot.id, "replacement": replacement, "galponHistoricalContext": historical}); err != nil {
			return err
		}
		w.parent = id
	}
	return nil
}
