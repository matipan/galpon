package sessionfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const piHistoryHeader = `{"type":"session","version":3,"id":"source","cwd":"/source"}` + "\n"

func historyFile(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readHistory(t *testing.T, path, harness string) *History {
	t.Helper()
	history, err := ReadHistory(path, harness)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.Close() })
	return history
}

func historyMessages(t *testing.T, history *History, context bool) []HistoryMessage {
	t.Helper()
	refs := history.archive
	if context {
		refs = history.context
	}
	var out []HistoryMessage
	if err := history.walk(refs, func(message HistoryMessage) error { out = append(out, message); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHistoryKeepsActiveMessagesAndCompletedToolEvidence(t *testing.T) {
	data := piHistoryHeader + `{"type":"message","id":"u","parentId":null,"message":{"role":"user","content":"Keep port 4173 — exactly."}}
{"type":"message","id":"call","parentId":"u","message":{"role":"assistant","content":[{"type":"toolCall","id":"read-call","name":"read","arguments":{"offset":9007199254740993}},{"type":"toolCall","id":"other-call","name":"stat","arguments":{}}]}}
{"type":"message","id":"other-result","parentId":"call","message":{"role":"toolResult","toolCallId":"other-call","toolName":"stat","content":"parallel result","isError":false}}
{"type":"message","id":"interleaved","parentId":"other-result","message":{"role":"assistant","content":"One tool finished"}}
{"type":"message","id":"result","parentId":"interleaved","message":{"role":"toolResult","toolCallId":"read-call","toolName":"read","content":[{"type":"text","text":"receipt: sapphire"}],"isError":false}}
{"type":"message","id":"answer","parentId":"result","message":{"role":"assistant","content":[{"type":"text","text":"The receipt is saved."}]}}
{"type":"message","id":"abandoned","parentId":"answer","message":{"role":"user","content":"Do not copy this abandoned branch"}}
{"type":"message","id":"active","parentId":"answer","message":{"role":"user","content":"Use the active branch"}}
{"type":"custom","customType":"galpon-operation","id":"state","parentId":"active","data":{"operation":"do-not-copy-operation"}}
{"type":"message","id":"final","parentId":"state","message":{"role":"assistant","content":[{"type":"text","text":"Active answer"}]}}
`
	path := historyFile(t, data)
	history := readHistory(t, path, "pi")
	messages := historyMessages(t, history, false)
	if len(messages) != 9 || messages[0].Role != "user" || messages[0].Text != "Keep port 4173 — exactly." || messages[8].Text != "Active answer" {
		t.Fatalf("active history = %#v", messages)
	}
	for index, want := range map[int]string{1: "9007199254740993", 2: "other-call", 3: "parallel result", 4: "One tool finished", 5: "receipt: sapphire"} {
		message := messages[index]
		if message.Role != "assistant" || !strings.Contains(message.Text, want) {
			t.Fatalf("history order lost at %d: %#v; want %q", index, message, want)
		}
		if index != 4 && !strings.Contains(message.Text, "not executable") {
			t.Fatal("tool evidence has no historical label")
		}
	}
	for _, message := range historyMessages(t, history, true) {
		if strings.Contains(message.Text, "abandoned") || strings.Contains(message.Text, "do-not-copy-operation") {
			t.Fatal("active context included an abandoned branch or runtime ownership")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != data {
		t.Fatal("reading history modified the source")
	}
}

func TestHistoryPreservesLargeConversation(t *testing.T) {
	text := "Start of conversation. " + strings.Repeat("Saved conversation text. ", 2<<20) + " End of conversation."
	path := historyFile(t, piHistoryHeader+`{"type":"message","id":"u","parentId":null,"message":{"role":"user","content":"`+text+`"}}
{"type":"message","id":"a","parentId":"u","message":{"role":"assistant","content":"Saved in full."}}
`)
	history := readHistory(t, path, "pi")
	messages := historyMessages(t, history, false)
	if len(messages) != 2 || messages[0].Role != "user" || messages[0].Text != text || messages[1].Text != "Saved in full." {
		t.Fatal("large conversation was not preserved in full")
	}
}

func TestHistorySnapshotRetainsInterruptedTurnsAndIgnoresLaterAppends(t *testing.T) {
	prefix := piHistoryHeader + `{"type":"message","id":"u","parentId":null,"message":{"role":"user","content":"Still waiting"}}
{"type":"message","id":"call","parentId":"u","message":{"role":"assistant","content":[{"type":"toolCall","id":"pending","name":"bash","arguments":{"command":"must not replay"}}]}}
`
	path := historyFile(t, prefix+`{"type":"message"`)
	history := readHistory(t, path, "pi")
	if err := os.WriteFile(path, []byte(prefix+`{"type":"message","id":"later","parentId":"call","message":{"role":"toolResult","toolCallId":"pending","content":"later result"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	messages := historyMessages(t, history, true)
	if len(messages) != 2 || messages[0].Text != "Still waiting" || !strings.Contains(messages[1].Text, "must not replay") || !strings.Contains(messages[1].Text, "not executable") {
		t.Fatalf("interrupted snapshot changed: %#v", messages)
	}
}

func TestHistoryCompactionAndEditsKeepArchiveSeparateFromContext(t *testing.T) {
	path := historyFile(t, piHistoryHeader+`{"type":"message","id":"u","parentId":null,"message":{"role":"user","content":"Archived question"}}
{"type":"message","id":"a","parentId":"u","message":{"role":"assistant","content":"Archived answer"}}
{"type":"message","id":"kept","parentId":"a","message":{"role":"user","content":"Original kept question"}}
{"type":"compaction","id":"compact","parentId":"kept","summary":"Current summary","firstKeptEntryId":"kept"}
{"type":"context_edit","id":"edit","parentId":"compact","targetId":"kept","replacement":{"content":"Edited kept question"}}
{"type":"message","id":"shell","parentId":"edit","message":{"role":"bashExecution","command":"pwd","output":"/private","excludeFromContext":true}}
{"type":"branch_summary","id":"branch","parentId":"shell","summary":"Work from another branch"}
`)
	history := readHistory(t, path, "pi")
	archive := historyMessages(t, history, false)
	context := historyMessages(t, history, true)
	if len(archive) != 6 || archive[0].Text != "Archived question" || archive[2].Text != "Original kept question" || !strings.Contains(archive[4].Text, "/private") {
		t.Fatalf("archive = %#v", archive)
	}
	if len(context) != 3 || context[0].Text != "Current summary" || context[1].Text != "Edited kept question" || context[2].Text != "Work from another branch" {
		t.Fatalf("context = %#v", context)
	}
	for _, harness := range []string{"pi", "claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			seed := filepath.Join(t.TempDir(), "session.jsonl")
			if err := WriteHistory(seed, harness, "/target", history); err != nil {
				t.Fatal(err)
			}
			converted := readHistory(t, seed, harness)
			current := historyMessages(t, converted, true)
			if len(current) != len(context) {
				t.Fatalf("converted context = %#v", current)
			}
			for index, want := range context {
				if got := current[index]; got.Text != want.Text || got.Role != want.Role || got.Human != want.Human {
					t.Fatalf("message %d = %#v, want %#v", index, got, want)
				}
			}
		})
	}
}

func TestHistoryRejectsInvalidSavedRecords(t *testing.T) {
	for _, test := range []struct{ name, records, want string }{
		{"missing ancestry", `{"type":"message","id":"a","parentId":"missing","message":{"role":"user","content":"hello"}}`, "ancestry"},
		{"cyclic ancestry", `{"type":"message","id":"a","parentId":"a","message":{"role":"user","content":"hello"}}`, "ancestry"},
		{"invalid ancestry", `{"type":"message","id":"a","parentId":false,"message":{"role":"assistant","content":"hello"}}`, "ancestry"},
		{"invalid text", `{"type":"message","id":"a","parentId":null,"message":{"role":"assistant","content":[{"type":"text","text":false}]}}`, "invalid text"},
	} {
		t.Run(test.name, func(t *testing.T) {
			history, err := ReadHistory(historyFile(t, piHistoryHeader+test.records+"\n"), "pi")
			if history != nil {
				_ = history.Close()
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
