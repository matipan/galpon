package sessionfile

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type historyWriter struct {
	file                         *os.File
	history                      *History
	harness, cwd, session, stamp string
	parent                       any
	turn, final, assets          string
	piEntries                    []historySlot
}

// WriteHistory creates a native session with fresh identities. Prior records
// remain in native history; checkpoints or model-only edits select the context.
// Images are embedded and other attachments are copied into the new agent.
func WriteHistory(path, harness, cwd string, history *History) (err error) {
	if harness != "pi" && harness != "claude" && harness != "codex" {
		return fmt.Errorf("unsupported target harness %q", harness)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	archive := filepath.Join(filepath.Dir(filepath.Dir(path)), "history")
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(path)
			_ = os.RemoveAll(archive)
		}
	}()
	if err := os.MkdirAll(archive, 0o700); err != nil {
		return err
	}
	if err := copyHistoryFile(filepath.Join(archive, "source.jsonl"), io.NewSectionReader(history.source, 0, 1<<63-1)); err != nil {
		return err
	}
	writer := &historyWriter{file: file, history: history, harness: harness, cwd: cwd, session: uuid.NewString(), stamp: time.Now().UTC().Format(time.RFC3339Nano), assets: archive}
	switch harness {
	case "pi":
		err = writer.write(map[string]any{"type": "session", "version": 3, "id": writer.session, "cwd": cwd, "timestamp": writer.stamp})
	case "codex":
		err = writer.codex("session_meta", map[string]any{"id": writer.session, "timestamp": writer.stamp, "cwd": cwd, "originator": "galpon", "cli_version": "0.0.0", "source": "cli", "history_mode": "legacy"}, writer.stamp)
	case "claude":
		id := uuid.NewString()
		err = writer.write(map[string]any{"type": "system", "subtype": "galpon_history", "uuid": id, "parentUuid": nil, "sessionId": writer.session, "cwd": cwd, "timestamp": writer.stamp, "content": "Imported conversation history"})
		writer.parent = id
	}
	if err != nil {
		return err
	}
	if err := history.walk(history.archive, writer.message); err != nil {
		return err
	}
	if err := writer.complete(); err != nil {
		return err
	}
	if history.checkpoint {
		if err := writer.checkpoint(); err != nil {
			return err
		}
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	for _, directory := range []string{archive, filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
		folder, err := os.Open(directory)
		if err != nil {
			return err
		}
		err = folder.Sync()
		_ = folder.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func copyHistoryFile(path string, input io.Reader) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if _, err := io.Copy(file, input); err != nil {
		return err
	}
	return file.Sync()
}

func (w *historyWriter) write(value any) error { return json.NewEncoder(w.file).Encode(value) }
func (w *historyWriter) codex(kind string, payload any, stamp string) error {
	return w.write(map[string]any{"type": kind, "payload": payload, "timestamp": stamp})
}

func (w *historyWriter) content(item HistoryMessage) ([]any, error) {
	parts := item.Parts
	if len(parts) == 0 && item.Text != "" {
		parts = []HistoryPart{{Type: "text", Text: item.Text}}
	}
	var out []any
	text := func(value string) {
		kind := "text"
		if w.harness == "codex" {
			kind = "input_text"
			if item.Role == "assistant" {
				kind = "output_text"
			}
		}
		out = append(out, map[string]any{"type": kind, "text": value})
	}
	for _, part := range parts {
		switch part.Type {
		case "text":
			text(part.Text)
		case "image":
			switch w.harness {
			case "pi":
				out = append(out, map[string]any{"type": "image", "mimeType": part.MIME, "data": part.Data})
			case "claude":
				source := map[string]any{"type": "base64", "media_type": part.MIME, "data": part.Data}
				if part.Path != "" {
					source = map[string]any{"type": "url", "url": part.Path}
				}
				out = append(out, map[string]any{"type": "image", "source": source})
			case "codex":
				location := part.Path
				if location == "" {
					location = "data:" + part.MIME + ";base64," + part.Data
				}
				out = append(out, map[string]any{"type": "input_image", "image_url": location})
			}
		case "file":
			location := part.Path
			if part.Data != "" {
				digest := sha256.Sum256([]byte(part.Data))
				name := hex.EncodeToString(digest[:]) + "-" + filepath.Base(part.Name)
				location = filepath.Join(w.assets, name)
				if _, err := os.Stat(location); os.IsNotExist(err) {
					if err := copyHistoryFile(location, base64.NewDecoder(base64.StdEncoding, strings.NewReader(part.Data))); err != nil {
						return nil, err
					}
				} else if err != nil {
					return nil, err
				}
			}
			text(fmt.Sprintf("[Historical attachment: %s; media type: %s; saved at %s]", part.Name, part.MIME, location))
			if w.harness == "claude" && part.MIME == "application/pdf" && part.Data != "" {
				out = append(out, map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": part.MIME, "data": part.Data}})
			}
		}
	}
	return out, nil
}

func (w *historyWriter) message(item HistoryMessage) error {
	stamp := item.Time
	if stamp == "" {
		stamp = w.stamp
	}
	id := uuid.NewString()
	content, err := w.content(item)
	if err != nil {
		return err
	}
	switch w.harness {
	case "pi":
		message := map[string]any{"role": item.Role, "content": content, "timestamp": time.Now().UnixMilli(), "galponHistoricalContext": item.Role == "user" && !item.Human}
		if item.Role == "assistant" {
			message["api"], message["provider"], message["model"], message["stopReason"] = "openai-responses", "galpon-history", w.history.Harness, "stop"
			message["usage"] = map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 0, "cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}}
		}
		err = w.write(map[string]any{"type": "message", "id": id, "parentId": w.parent, "timestamp": stamp, "message": message})
		w.piEntries = append(w.piEntries, historySlot{id: id, role: item.Role})
	case "claude":
		message := map[string]any{"role": item.Role, "content": content}
		if item.Role == "assistant" {
			message["type"], message["id"], message["model"] = "message", "msg_"+id, "galpon-history"
			message["stop_reason"], message["stop_sequence"] = "end_turn", nil
			message["usage"] = map[string]any{"input_tokens": 0, "output_tokens": 0}
		}
		err = w.write(map[string]any{"type": item.Role, "uuid": id, "parentUuid": w.parent, "sessionId": w.session, "cwd": w.cwd, "timestamp": stamp, "isSidechain": false, "galponHistoricalContext": item.Role == "user" && !item.Human, "userType": "external", "message": message})
	case "codex":
		if item.Role == "user" || w.turn == "" {
			if err := w.complete(); err != nil {
				return err
			}
			w.turn, w.final = uuid.NewString(), ""
			if err := w.codex("event_msg", map[string]any{"type": "task_started", "turn_id": w.turn, "model_context_window": nil, "collaboration_mode_kind": "default"}, stamp); err != nil {
				return err
			}
		}
		payload := codexHistoryMessage(item, content, id)
		object(payload["internal_chat_message_metadata_passthrough"])["turn_id"] = w.turn
		if item.Role == "assistant" {
			payload["phase"], w.final = "final_answer", item.Text
		}
		if err := w.codex("response_item", payload, stamp); err != nil {
			return err
		}
		if item.Role == "user" && item.Human {
			err = w.codex("event_msg", map[string]any{"type": "user_message", "message": item.Text, "images": []any{}, "local_images": []any{}, "text_elements": []any{}}, stamp)
		} else if item.Role == "assistant" {
			err = w.codex("event_msg", map[string]any{"type": "agent_message", "message": item.Text, "phase": "final_answer"}, stamp)
		}
	}
	w.parent = id
	return err
}

func codexHistoryMessage(item HistoryMessage, content []any, id string) map[string]any {
	metadata := make(map[string]any)
	if item.Role == "user" {
		kinds := make([]string, len(content))
		for i, part := range content {
			kinds[i] = "compaction.summary"
			if item.Human {
				kinds[i] = "user.text"
				if object(part)["type"] == "input_image" {
					kinds[i] = "user.image"
				}
			}
		}
		metadata["content_item_kinds"] = kinds
	}
	return map[string]any{"type": "message", "id": "msg_" + id, "role": item.Role, "content": content, "internal_chat_message_metadata_passthrough": metadata}
}

func (w *historyWriter) complete() error {
	if w.harness != "codex" || w.turn == "" {
		return nil
	}
	err := w.codex("event_msg", map[string]any{"type": "task_complete", "turn_id": w.turn, "last_agent_message": w.final}, w.stamp)
	w.turn = ""
	return err
}

func (w *historyWriter) checkpoint() error {
	const summary = "Saved effective context from the source conversation follows. Earlier records remain in the conversation history."
	id := uuid.NewString()
	switch w.harness {
	case "pi":
		return w.piProjection()
	case "claude":
		if err := w.write(map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": id, "parentUuid": nil, "logicalParentUuid": w.parent, "sessionId": w.session, "timestamp": w.stamp, "content": summary, "compactMetadata": map[string]any{"trigger": "manual", "preTokens": 0}}); err != nil {
			return err
		}
		w.parent = id
		if err := w.claudeArchiveNotices(); err != nil {
			return err
		}
	case "codex":
		if _, err := fmt.Fprintf(w.file, "{\"type\":\"compacted\",\"timestamp\":%q,\"payload\":{\"message\":%q,\"replacement_history\":[", w.stamp, summary); err != nil {
			return err
		}
		first := true
		if err := w.history.walk(w.history.context, func(item HistoryMessage) error {
			content, err := w.content(item)
			if err != nil {
				return err
			}
			if !first {
				if _, err := w.file.WriteString(","); err != nil {
					return err
				}
			}
			first = false
			payload := codexHistoryMessage(item, content, uuid.NewString())
			if item.Role == "assistant" {
				payload["phase"] = "final_answer"
			}
			data, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			_, err = w.file.Write(data)
			return err
		}); err != nil {
			return err
		}
		_, err := w.file.WriteString("]}}\n")
		return err
	}
	return w.history.walk(w.history.context, w.message)
}
