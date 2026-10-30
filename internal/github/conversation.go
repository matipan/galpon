package github

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ScanState is how far Galpon has read one saved conversation file.
type ScanState struct {
	ReadBytes int64
	// Pending holds the call IDs of gh pr create calls that have no saved
	// result yet.
	Pending []string
}

// Creation is a pull request URL that a gh pr create call returned.
type Creation struct {
	Repository string
	Number     int
	At         time.Time
}

var pullRequestURL = regexp.MustCompile(`https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([0-9]+)`)

// createCommand finds gh pr create where a shell starts a command. A search
// for the text, such as rg 'gh pr create', does not match.
var createCommand = regexp.MustCompile(`(?:^|[;&|(\n]|\$\()\s*(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*(?:command\s+)?gh\s+pr\s+create\b`)

// ScanConversation reads the complete JSON lines after state.ReadBytes. Pi,
// Claude Code, and Codex save a tool call and its result with a shared call
// ID. A gh pr create call and its result can be in different scans. A file
// that is shorter than the read position is read again from the start.
func ScanConversation(path string, state ScanState) (ScanState, []Creation, error) {
	file, err := os.Open(path)
	if err != nil {
		return state, nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return state, nil, err
	}
	if info.Size() < state.ReadBytes {
		state = ScanState{}
	}
	if _, err := file.Seek(state.ReadBytes, io.SeekStart); err != nil {
		return state, nil, err
	}
	pending := make(map[string]bool, len(state.Pending))
	for _, id := range state.Pending {
		pending[id] = true
	}
	var creations []Creation
	reader := bufio.NewReaderSize(file, 1<<20)
	offset := state.ReadBytes
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break // The next scan reads an incomplete last line again.
		}
		offset += int64(len(line))
		if !bytes.Contains(line, []byte("gh pr create")) && !bytes.Contains(line, []byte("/pull/")) {
			continue
		}
		var record any
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		at := recordTime(record)
		walk(record, func(object map[string]any) bool {
			if id := createCallID(object); id != "" {
				pending[id] = true
				return true
			}
			id := resultCallID(object)
			if id == "" {
				return true
			}
			creations = append(creations, nestedCreations(object, at)...)
			if pending[id] {
				delete(pending, id)
				matches := pullRequestURL.FindAllStringSubmatch(text(object["content"])+text(object["output"]), -1)
				if len(matches) > 0 {
					// gh pr create prints the new URL last.
					last := matches[len(matches)-1]
					number, _ := strconv.Atoi(last[2])
					creations = append(creations, Creation{Repository: last[1], Number: number, At: at})
				}
			}
			// Calls inside a tool result are part of that tool's output, not
			// calls of this conversation.
			return false
		})
	}
	next := ScanState{ReadBytes: offset}
	for id := range pending {
		next.Pending = append(next.Pending, id)
	}
	sort.Strings(next.Pending)
	return next, creations, nil
}

// nestedCreations returns the pull requests that the gh pr create calls of a
// Pi codemode script opened. The codemode result keeps the calls of the
// script, but not their output. Only the text that the script returned can
// hold the URLs, and that text can also hold other pull request URLs. Thus the
// URLs count only when there are not more of them than gh pr create calls.
func nestedCreations(result map[string]any, at time.Time) []Creation {
	nested, _ := result["nestedCalls"].(map[string]any)
	calls, _ := nested["calls"].([]any)
	creates := 0
	for _, call := range calls {
		if object, ok := call.(map[string]any); ok && createCallID(object) != "" {
			creates++
		}
	}
	if creates == 0 {
		return nil
	}
	var creations []Creation
	seen := map[string]bool{}
	for _, match := range pullRequestURL.FindAllStringSubmatch(text(result["content"]), -1) {
		if seen[strings.ToLower(match[0])] {
			continue
		}
		seen[strings.ToLower(match[0])] = true
		number, _ := strconv.Atoi(match[2])
		creations = append(creations, Creation{Repository: match[1], Number: number, At: at})
	}
	if len(creations) > creates {
		return nil
	}
	return creations
}

// createCallID returns the ID of a tool call that runs gh pr create.
func createCallID(object map[string]any) string {
	if !createCommand.MatchString(shellCommand(object)) {
		return ""
	}
	if id, _ := object["call_id"].(string); id != "" {
		return id
	}
	id, _ := object["id"].(string)
	return id
}

// shellCommand returns the command of a shell tool call. Pi and Claude Code
// keep the arguments as an object. Codex keeps them as a JSON string, and its
// command can be an argument list such as ["bash", "-lc", "..."].
func shellCommand(object map[string]any) string {
	arguments, ok := object["arguments"]
	if !ok {
		arguments, ok = object["input"]
	}
	if !ok {
		return ""
	}
	if encoded, isString := arguments.(string); isString {
		var decoded any
		if json.Unmarshal([]byte(encoded), &decoded) != nil {
			return ""
		}
		arguments = decoded
	}
	fields, _ := arguments.(map[string]any)
	for _, key := range []string{"command", "cmd"} {
		switch command := fields[key].(type) {
		case string:
			return command
		case []any:
			if len(command) > 0 {
				last, _ := command[len(command)-1].(string)
				return last
			}
		}
	}
	return ""
}

// resultCallID returns the call ID of a tool result.
func resultCallID(object map[string]any) string {
	for _, key := range []string{"toolCallId", "tool_use_id"} {
		if id, _ := object[key].(string); id != "" {
			return id
		}
	}
	if kind, _ := object["type"].(string); strings.HasSuffix(kind, "_output") {
		id, _ := object["call_id"].(string)
		return id
	}
	return ""
}

// walk calls visit for each object in value, parents first. It does not walk
// into an object for which visit returns false.
func walk(value any, visit func(map[string]any) bool) {
	switch typed := value.(type) {
	case map[string]any:
		if !visit(typed) {
			return
		}
		for _, child := range typed {
			walk(child, visit)
		}
	case []any:
		for _, child := range typed {
			walk(child, visit)
		}
	}
}

func text(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		data, _ := json.Marshal(typed)
		return string(data)
	}
}

func recordTime(record any) time.Time {
	object, _ := record.(map[string]any)
	switch value := object["timestamp"].(type) {
	case string:
		at, _ := time.Parse(time.RFC3339Nano, value)
		return at
	case float64:
		return time.UnixMilli(int64(value))
	}
	return time.Time{}
}
