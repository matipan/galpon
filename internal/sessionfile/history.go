package sessionfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

type HistoryPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	MIME string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"`
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

type HistoryMessage struct {
	Role  string        `json:"role"`
	Human bool          `json:"human,omitempty"`
	Text  string        `json:"text,omitempty"`
	Parts []HistoryPart `json:"parts,omitempty"`
	Time  string        `json:"time,omitempty"`
}

type historyRef struct {
	offset, size int64
	human        bool
}

// History keeps content on disk. Memory holds entry identities and message
// offsets, not the full transcript or all of its decoded images.
// Close releases the private snapshot after WriteHistory finishes.
type History struct {
	Harness                       string
	originalPath, root, cwd, leaf string
	source, messages              *os.File
	entries                       []historyEntry
	byID                          map[string]int
	refs                          []historyRef
	archive, context              []int
	claudeContext                 []int
	byEntry                       map[int][]int
	mediaFiles                    map[string]HistoryPart
	checkpoint                    bool
}

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func str(v any) string            { s, _ := v.(string); return s }
func array(v any) []any           { a, _ := v.([]any); return a }

// ReadHistory freezes complete saved records. The source may keep appending
// while the fork is converted; it is never stopped or modified.
func ReadHistory(path, harness string) (_ *History, err error) {
	if harness != "pi" && harness != "claude" && harness != "codex" {
		return nil, fmt.Errorf("unsupported source harness %q", harness)
	}
	root, err := os.MkdirTemp("", "galpon-history-*")
	if err != nil {
		return nil, err
	}
	h := &History{Harness: harness, originalPath: path, root: root, byID: make(map[string]int), byEntry: make(map[int][]int), mediaFiles: make(map[string]HistoryPart)}
	defer func() {
		if err != nil {
			_ = h.Close()
		}
	}()
	snapshot := filepath.Join(root, "source.jsonl")
	for attempt := 0; attempt < 3; attempt++ {
		err = snapshotHistory(path, snapshot)
		if !errors.Is(err, errHistoryChanged) {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	h.source, err = os.Open(snapshot)
	if err != nil {
		return nil, err
	}
	h.messages, err = os.OpenFile(filepath.Join(root, "messages.jsonl"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := h.indexSource(); err != nil {
		return nil, err
	}
	if harness != "pi" {
		if _, err := NativeID(harness, io.NewSectionReader(h.source, 0, 1<<63-1)); err != nil {
			return nil, err
		}
	}
	branch, err := h.branch()
	if err != nil {
		return nil, err
	}
	for _, index := range branch {
		row, err := h.entry(index)
		if err != nil {
			return nil, err
		}
		messages, err := h.convertRecord(row)
		if err != nil {
			return nil, fmt.Errorf("convert %s history at byte %d: %w", harness, h.entries[index].offset, err)
		}
		refs, err := h.storeMessages(messages, h.humanInput(row))
		if err != nil {
			return nil, err
		}
		h.byEntry[index] = refs
		h.archive = append(h.archive, refs...)
	}
	switch harness {
	case "pi":
		err = h.projectPi(branch)
	case "claude":
		err = h.projectClaude(branch)
	case "codex":
		err = h.projectCodex(branch)
	}
	if err != nil {
		return nil, err
	}
	h.checkpoint = !slices.Equal(h.archive, h.context)
	return h, nil
}

func (h *History) Close() error {
	if h.source != nil {
		_ = h.source.Close()
	}
	if h.messages != nil {
		_ = h.messages.Close()
	}
	return os.RemoveAll(h.root)
}

func (h *History) storeMessages(messages []HistoryMessage, human bool) ([]int, error) {
	var refs []int
	for i, message := range messages {
		if message.Text == "" && len(message.Parts) == 0 {
			continue
		}
		message.Human = human && i == 0
		offset, err := h.messages.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, err
		}
		if err := json.NewEncoder(h.messages).Encode(message); err != nil {
			return nil, err
		}
		end, err := h.messages.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		refs = append(refs, len(h.refs))
		h.refs = append(h.refs, historyRef{offset: offset, size: end - offset, human: human && i == 0})
	}
	return refs, nil
}

func (h *History) entryMessages(index int) ([]int, error) {
	if refs, ok := h.byEntry[index]; ok {
		return refs, nil
	}
	row, err := h.entry(index)
	if err != nil {
		return nil, err
	}
	messages, err := h.convertRecord(row)
	if err != nil {
		return nil, err
	}
	refs, err := h.storeMessages(messages, h.humanInput(row))
	if err == nil {
		h.byEntry[index] = refs
	}
	return refs, err
}

func (h *History) message(ref int) (HistoryMessage, error) {
	position := h.refs[ref]
	var message HistoryMessage
	err := json.NewDecoder(io.NewSectionReader(h.messages, position.offset, position.size)).Decode(&message)
	return message, err
}

func (h *History) walk(refs []int, visit func(HistoryMessage) error) error {
	for _, ref := range refs {
		message, err := h.message(ref)
		if err != nil {
			return err
		}
		if err := visit(message); err != nil {
			return err
		}
	}
	return nil
}
