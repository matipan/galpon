package sessionfile

import (
	"strings"

	"github.com/google/uuid"
)

// Claude loads the context after its last compaction into the terminal. Native
// informational records show the full archive in conversation order without
// sending it to the model. The context appears as a separate section below it.
func (w *historyWriter) claudeArchiveNotices() error {
	if err := w.history.walk(w.history.archive, func(message HistoryMessage) error {
		var text strings.Builder
		text.WriteString("Archived " + message.Role + " message from " + w.history.Harness + " (read-only history):\n")
		text.WriteString(message.Text)
		for _, part := range message.Parts {
			if part.Type != "text" {
				text.WriteString("\n[Saved attachment: " + part.Name + "; " + part.MIME + ". Original data is in the saved conversation history.]")
			}
		}
		return w.claudeNotice(text.String())
	}); err != nil {
		return err
	}
	return w.claudeNotice("Active context for this fork follows. Messages above are a read-only archive; retained messages can also appear below.")
}

func (w *historyWriter) claudeNotice(content string) error {
	id := uuid.NewString()
	if err := w.write(map[string]any{"type": "system", "subtype": "informational", "level": "info", "uuid": id, "parentUuid": w.parent, "sessionId": w.session, "cwd": w.cwd, "timestamp": w.stamp, "content": content}); err != nil {
		return err
	}
	w.parent = id
	return nil
}
