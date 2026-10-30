package github

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func writeConversation(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendConversation(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func createdNumbers(creations []Creation) []int {
	var out []int
	for _, creation := range creations {
		out = append(out, creation.Number)
	}
	slices.Sort(out)
	return out
}

// Each harness saves a tool call and its result in its own format.
const (
	piCreate = `{"type":"message","timestamp":"2026-10-01T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"pi-1","name":"bash","arguments":{"command":"git push -u origin feature && gh pr create --fill"}}]}}
{"type":"message","timestamp":"2026-10-01T10:00:04.000Z","message":{"role":"toolResult","toolCallId":"pi-1","toolName":"bash","content":[{"type":"text","text":"remote: https://github.com/acme/widgets/pull/new/feature\nhttps://github.com/acme/widgets/pull/1\n"}]}}
`
	claudeCreate = `{"type":"assistant","timestamp":"2026-10-01T11:00:00.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"gh pr create --title Fix --body Fix"}}]}}
{"type":"user","timestamp":"2026-10-01T11:00:03.000Z","message":{"role":"user","content":[{"tool_use_id":"toolu_2","type":"tool_result","content":"https://github.com/acme/widgets/pull/2"}]}}
`
	codexCreate = `{"timestamp":"2026-10-01T12:00:00.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"gh pr create --fill\"]}","call_id":"call_3"}}
{"timestamp":"2026-10-01T12:00:02.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_3","output":"https://github.com/acme/widgets/pull/3\n"}}
`
	// A Pi codemode result keeps the calls of the script and the text that
	// the script returned, but not the output of each call.
	codemodeCreate = `{"type":"message","timestamp":"2026-10-01T14:00:00.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"pi-5","name":"codemode","arguments":{"code":"return await tools.bash({command: 'gh pr create --fill'})"}}]}}
{"type":"message","timestamp":"2026-10-01T14:00:05.000Z","message":{"role":"toolResult","toolCallId":"pi-5","toolName":"codemode","content":[{"type":"text","text":"{\"output\":\"https://github.com/acme/widgets/pull/5\\n\",\"exit_code\":0}"}],"nestedCalls":{"complete":true,"calls":[{"id":"pi-5/1","name":"bash","status":"ok","arguments":{"command":"gh pr create --fill"}}]}}}
`
	// When the returned text has more URLs than gh pr create calls, the
	// created pull request is not known.
	codemodeList = `{"type":"message","timestamp":"2026-10-01T15:00:05.000Z","message":{"role":"toolResult","toolCallId":"pi-6","toolName":"codemode","content":[{"type":"text","text":"created https://github.com/acme/widgets/pull/6, open: https://github.com/acme/widgets/pull/7"}],"nestedCalls":{"complete":true,"calls":[{"id":"pi-6/1","name":"bash","status":"ok","arguments":{"command":"gh pr create --fill"}},{"id":"pi-6/2","name":"bash","status":"ok","arguments":{"command":"gh pr list"}}]}}}
`
	// A search for the text is not a gh pr create command.
	textSearch = `{"type":"message","timestamp":"2026-10-01T13:00:00.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"pi-4","name":"bash","arguments":{"command":"rg -n 'gh pr create' notes"}}]}}
{"type":"message","timestamp":"2026-10-01T13:00:01.000Z","message":{"role":"toolResult","toolCallId":"pi-4","toolName":"bash","content":[{"type":"text","text":"notes:1: gh pr create made https://github.com/acme/widgets/pull/4\n"}]}}
`
)

func TestScanConversationFindsPullRequestsThatGhPrCreateOpened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeConversation(t, path, piCreate+claudeCreate+codexCreate+codemodeCreate+codemodeList+textSearch)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	state, creations, err := ScanConversation(path, ScanState{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := createdNumbers(creations), []int{1, 2, 3, 5}; !slices.Equal(got, want) {
		t.Fatalf("created pull requests = %v, want %v", got, want)
	}
	for _, creation := range creations {
		if creation.Repository != "acme/widgets" {
			t.Fatalf("repository = %q, want acme/widgets", creation.Repository)
		}
		if creation.Number == 1 && !creation.At.Equal(time.Date(2026, 10, 1, 10, 0, 4, 0, time.UTC)) {
			t.Fatalf("creation time = %s, want the time of the saved result", creation.At)
		}
	}
	if state.ReadBytes != info.Size() || len(state.Pending) != 0 {
		t.Fatalf("state = %+v, want the full file and no pending calls", state)
	}
}

func TestScanConversationContinuesAfterNewLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	lines := []byte(piCreate)
	callEnd := slices.Index(lines, '\n') + 1
	// The result line is still being written.
	writeConversation(t, path, string(lines[:callEnd+20]))
	state, creations, err := ScanConversation(path, ScanState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(creations) != 0 || state.ReadBytes != int64(callEnd) || !slices.Equal(state.Pending, []string{"pi-1"}) {
		t.Fatalf("first scan = %+v, %v; want only the complete call line", state, creations)
	}
	appendConversation(t, path, string(lines[callEnd+20:]))
	state, creations, err = ScanConversation(path, state)
	if err != nil {
		t.Fatal(err)
	}
	if got := createdNumbers(creations); !slices.Equal(got, []int{1}) || len(state.Pending) != 0 {
		t.Fatalf("second scan = %+v, %v; want pull request 1", state, got)
	}
	state, creations, err = ScanConversation(path, state)
	if err != nil || len(creations) != 0 {
		t.Fatalf("a scan without new lines found %v, %v", creations, err)
	}
}

func TestScanConversationReadsARewrittenFileAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeConversation(t, path, claudeCreate)
	_, creations, err := ScanConversation(path, ScanState{ReadBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := createdNumbers(creations); !slices.Equal(got, []int{2}) {
		t.Fatalf("created pull requests = %v, want [2]", got)
	}
}
