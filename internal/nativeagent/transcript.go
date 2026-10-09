package nativeagent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/matipan/galpon/internal/model"
)

const receiptPrefix = "[Galpon saved-result marker: "

var receiptPattern = regexp.MustCompile(`\[Galpon saved-result marker: ([a-zA-Z0-9-]+)\]`)

type turnEvidence struct {
	ID          string
	PromptID    string
	Input       string
	Inputs      []string
	Final       string
	FinalID     string
	Complete    bool
	Failure     string
	ToolProofs  map[string]bool
	ResultCalls map[string]bool
}

// transcript reads complete native records and syncs them to the managed copy.
// Incomplete records cannot establish delivery or result persistence.
type transcript struct {
	kind, source, mirror string
	offset               int64
	roots                map[string]string
	promptRoots          map[string]string
	compactRoot          string
	turns                map[string]*turnEvidence
	order                []string
	events               []model.ConversationEvent
}

func newTranscript(kind, source, mirror string) *transcript {
	return &transcript{kind: kind, source: source, mirror: mirror, roots: make(map[string]string), promptRoots: make(map[string]string), turns: make(map[string]*turnEvidence)}
}

func (t *transcript) refresh() error {
	file, err := os.Open(t.source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() < t.offset {
		return fmt.Errorf("native conversation was truncated during the runtime")
	}
	if info.Size() == t.offset {
		return nil
	}
	if _, err := file.Seek(t.offset, io.SeekStart); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.mirror), 0o700); err != nil {
		return err
	}
	var mirror *os.File
	initial := t.offset == 0
	if t.source != t.mirror {
		if initial {
			mirror, err = os.CreateTemp(filepath.Dir(t.mirror), ".snapshot-*")
		} else {
			mirror, err = os.OpenFile(t.mirror, os.O_CREATE|os.O_WRONLY, 0o600)
		}
		if err != nil {
			return err
		}
		defer func() {
			_ = mirror.Close()
			if initial {
				_ = os.Remove(mirror.Name())
			}
		}()
		if err := mirror.Truncate(t.offset); err != nil {
			return err
		}
		if _, err := mirror.Seek(t.offset, io.SeekStart); err != nil {
			return err
		}
	}
	reader := bufio.NewReader(io.LimitReader(file, info.Size()-t.offset))
	var records [][]byte
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
		if !json.Valid(line) {
			return fmt.Errorf("invalid native conversation record at byte %d", t.offset)
		}
		if mirror != nil {
			if _, err := mirror.Write(line); err != nil {
				return err
			}
		}
		records = append(records, line)
	}
	if mirror != nil {
		if err := mirror.Sync(); err != nil {
			return err
		}
	} else if err := file.Sync(); err != nil {
		return err
	}
	if initial && len(records) > 0 {
		if mirror != nil {
			if err := mirror.Close(); err != nil {
				return err
			}
			if err := os.Rename(mirror.Name(), t.mirror); err != nil {
				return err
			}
		}
		directory, err := os.Open(filepath.Dir(t.mirror))
		if err != nil {
			return err
		}
		defer func() { _ = directory.Close() }()
		if err := directory.Sync(); err != nil {
			return err
		}
	}
	for _, line := range records {
		t.ingest(line)
		t.offset += int64(len(line))
	}
	return nil
}

func (t *transcript) turn(id string) *turnEvidence {
	if id == "" {
		return nil
	}
	if value := t.turns[id]; value != nil {
		return value
	}
	value := &turnEvidence{ID: id, ToolProofs: make(map[string]bool), ResultCalls: make(map[string]bool)}
	t.turns[id] = value
	t.order = append(t.order, id)
	return value
}

func (t *transcript) ingest(line []byte) {
	var row map[string]any
	if json.Unmarshal(line, &row) != nil {
		return
	}
	t.conversation(row)
	if t.kind == "claude" {
		t.claudeEntry(row)
	} else {
		t.codexEntry(row)
	}
}

func (t *transcript) claudeEntry(row map[string]any) {
	if row["isSidechain"] == true {
		return
	}
	id := stringValue(row["uuid"])
	root := t.roots[stringValue(row["parentUuid"])]
	promptID := stringValue(row["promptId"])
	if root == "" {
		root = t.roots[stringValue(row["logicalParentUuid"])]
	}
	if root == "" {
		root = t.promptRoots[promptID]
	}
	if row["type"] == "system" && (row["subtype"] == "compact_boundary" || row["compactMetadata"] != nil) {
		// Compaction can omit its logical parent. Only an unambiguous unfinished
		// main-thread turn can supply the missing boundary identity.
		if root == "" {
			root = t.unfinishedClaudeTurn()
		}
		t.compactRoot = root
		if id != "" {
			t.roots[id] = root
		}
		return
	}
	if row["isCompactSummary"] == true && root == "" {
		root = t.compactRoot
	}
	message := objectValue(row["message"])
	role := stringValue(message["role"])
	content := message["content"]
	toolResult := false
	if blocks, ok := content.([]any); ok {
		for _, raw := range blocks {
			if objectValue(raw)["type"] == "tool_result" {
				toolResult = true
			}
		}
	}
	if role == "user" && !toolResult && isClaudeInput(row) && id != "" {
		text := contentText(content)
		if strings.Contains(text, "[Request interrupted by user") {
			if turn := t.turn(root); turn != nil {
				turn.Failure = "Claude Code was interrupted"
			}
		} else {
			t.compactRoot = ""
			root = id
			turn := t.turn(root)
			turn.Input = text
			turn.PromptID = promptID
			if promptID != "" {
				t.promptRoots[promptID] = root
			}
		}
	}
	if id != "" && root != "" {
		t.roots[id] = root
	}
	turn := t.turn(root)
	if turn == nil {
		return
	}
	if role == "assistant" {
		blocks, _ := content.([]any)
		for _, raw := range blocks {
			block := objectValue(raw)
			name := stringValue(block["name"])
			if block["type"] == "tool_use" && strings.HasPrefix(name, "mcp__galpon__") && isObservationTool(strings.TrimPrefix(name, "mcp__galpon__")) {
				turn.ResultCalls[stringValue(block["id"])] = true
			}
		}
	}
	if role == "assistant" && message["stop_reason"] == "end_turn" {
		turn.Final, turn.FinalID, turn.Complete = contentText(content), id, true
	}
	if toolResult {
		for _, raw := range content.([]any) {
			block := objectValue(raw)
			if block["type"] == "tool_result" && block["is_error"] != true && turn.ResultCalls[stringValue(block["tool_use_id"])] {
				turn.recordToolProofs(contentText(block["content"]))
			}
		}
	}
}

func (t *transcript) codexEntry(row map[string]any) {
	payload := objectValue(row["payload"])
	meta := objectValue(payload["internal_chat_message_metadata_passthrough"])
	id := stringValue(meta["turn_id"])
	if id == "" {
		id = stringValue(payload["turn_id"])
	}
	turn := t.turn(id)
	if turn == nil {
		return
	}
	switch row["type"] {
	case "response_item":
		switch payload["type"] {
		case "message":
			switch payload["role"] {
			case "user":
				// Context and steered input can share this turn. Retain each saved
				// input so neither can erase the delivery evidence.
				turn.Input = contentText(payload["content"])
				turn.Inputs = append(turn.Inputs, turn.Input)
			case "assistant":
				if payload["phase"] == "final_answer" {
					turn.Final, turn.FinalID = contentText(payload["content"]), stringValue(payload["id"])
				}
			}
		case "function_call":
			if payload["namespace"] == "mcp__galpon" && isObservationTool(stringValue(payload["name"])) {
				turn.ResultCalls[stringValue(payload["call_id"])] = true
			}
		case "function_call_output":
			if turn.ResultCalls[stringValue(payload["call_id"])] {
				turn.recordToolProofs(contentText(payload["output"]))
			}
		}
	case "event_msg":
		switch payload["type"] {
		case "task_complete":
			turn.Complete = turn.FinalID != "" && turn.Final == stringValue(payload["last_agent_message"])
		case "turn_aborted":
			turn.Failure = "Codex was interrupted"
		}
	}
}

func (t *turnEvidence) hasInput(text string) bool {
	if strings.Contains(t.Input, text) {
		return true
	}
	for _, input := range t.Inputs {
		if strings.Contains(input, text) {
			return true
		}
	}
	return false
}

func (t *transcript) unfinishedClaudeTurn() string {
	root := ""
	for _, id := range t.order {
		turn := t.turns[id]
		if turn.Input == "" || turn.Complete || turn.Failure != "" {
			continue
		}
		if root != "" {
			return ""
		}
		root = id
	}
	return root
}

func isObservationTool(name string) bool {
	return name == "galpon_read_message" || name == "galpon_await_agent" || name == "galpon_await_agents"
}

func (t *turnEvidence) recordToolProofs(text string) {
	for _, match := range receiptPattern.FindAllStringSubmatch(text, -1) {
		t.ToolProofs[match[1]] = true
	}
}

func contentText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	var out []string
	if blocks, ok := value.([]any); ok {
		for _, raw := range blocks {
			block := objectValue(raw)
			if text, ok := block["text"].(string); ok {
				out = append(out, text)
			}
		}
	}
	return strings.Join(out, "\n")
}

func isClaudeInput(row map[string]any) bool {
	if row["isCompactSummary"] == true {
		return false
	}
	if row["isMeta"] != true {
		return true
	}
	origin := objectValue(row["origin"])
	return origin["kind"] == "channel" && origin["server"] == "galpon"
}

func stringValue(value any) string         { text, _ := value.(string); return text }
func objectValue(value any) map[string]any { object, _ := value.(map[string]any); return object }

func atomicJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicFile(path, append(data, '\n'))
}

func atomicFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".native-*")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
