package sessionfile

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/uuid"
)

// NativeID validates the format identity before a native conversation is used
// as a resume or fork source. Native session IDs are also used in file names.
func NativeID(kind string, input io.Reader) (string, error) {
	reader := bufio.NewScanner(io.LimitReader(input, 1<<20))
	reader.Buffer(make([]byte, 64<<10), 1<<20)
	for reader.Scan() {
		var row struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
			Payload   struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(reader.Bytes(), &row); err != nil {
			return "", fmt.Errorf("invalid %s conversation record: %w", kind, err)
		}
		id := ""
		switch kind {
		case "codex":
			if row.Type != "session_meta" {
				return "", fmt.Errorf("native Codex conversation must start with session metadata")
			}
			id = row.Payload.ID
		case "claude":
			switch row.Type {
			case "user", "assistant", "queue-operation", "system", "progress", "file-history-snapshot", "summary":
				id = row.SessionID
			default:
				return "", fmt.Errorf("invalid Claude Code conversation record type %q", row.Type)
			}
		default:
			return "", fmt.Errorf("unsupported native conversation harness %q", kind)
		}
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return "", fmt.Errorf("invalid native session ID: %w", err)
			}
			return id, nil
		}
	}
	if err := reader.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("the %s conversation has no native session identity", kind)
}
