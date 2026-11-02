package screen

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/herdrapp"
)

const testToken = "0123456789abcdef0123456789abcdef"

func message(token, body string) string { return prefix + token + ";" + body + "\a" }

// filterAll runs Filter over the writes, one write at a time, and returns the
// output and the messages.
func filterAll(t *testing.T, writes ...string) (string, []Message) {
	t.Helper()
	read, write := io.Pipe()
	var out bytes.Buffer
	var got []Message
	done := make(chan error, 1)
	go func() {
		done <- Filter(&out, read, testToken, func(m Message) { got = append(got, m) })
	}()
	for _, data := range writes {
		if _, err := write.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	_ = write.Close()
	if err := <-done; err != nil {
		t.Fatalf("Filter: %v", err)
	}
	return out.String(), got
}

func TestFilterKeepsOutputThatHasNoMessages(t *testing.T) {
	// Escape sequences of a TUI, with the start of other OSC sequences.
	output := "\x1b[?1049h\x1b[38;2;130;170;255mGalpon\x1b[0m\x1b]0;title\a\x1b]7;file:///home\x1b\\\x1b]77x;\r\n€ ok\x1b"
	got, messages := filterAll(t, output)
	if got != output || len(messages) != 0 {
		t.Fatalf("output = %q with %d messages, want it unchanged", got, len(messages))
	}
}

func TestFilterTakesOutMessagesAndKeepsTheOutputAround(t *testing.T) {
	place := `{"place":{"terminal":"t2","space":"galpon-w","spaceName":"Work","tab":"galpon-a","tabName":"Fixer","agent":true}}`
	got, messages := filterAll(t, "before\x1b[1m"+message(testToken, place)+"\x1b[0mafter"+message(testToken, `{"url":"https://github.com/a/b/pull/1"}`))
	if got != "before\x1b[1m\x1b[0mafter" {
		t.Fatalf("output = %q", got)
	}
	want := []Message{
		{Place: &herdrapp.Placement{Terminal: "t2", Space: "galpon-w", SpaceName: "Work", Tab: "galpon-a", TabName: "Fixer", Agent: true}},
		{URL: "https://github.com/a/b/pull/1"},
	}
	if len(messages) != 2 || *messages[0].Place != *want[0].Place || messages[1].URL != want[1].URL {
		t.Fatalf("messages = %+v, want %+v", messages, want)
	}
}

func TestFilterTakesOutAMessageThatComesInParts(t *testing.T) {
	data := "frame\x1b[2J" + message(testToken, `{"url":"https://example.com/x;y"}`) + "\x1b[Hnext"
	var writes []string
	for _, b := range []byte(data) {
		writes = append(writes, string([]byte{b}))
	}
	got, messages := filterAll(t, writes...)
	if got != "frame\x1b[2J\x1b[Hnext" || len(messages) != 1 || messages[0].URL != "https://example.com/x;y" {
		t.Fatalf("output = %q, messages = %+v", got, messages)
	}
}

func TestFilterDropsMessagesOfOtherCommands(t *testing.T) {
	// Agent output can print a message, but not with the token of this
	// command.
	got, messages := filterAll(t, "a"+message("ffffffffffffffffffffffffffffffff", `{"url":"https://example.com"}`)+"b"+message(testToken, "not json")+"c")
	if got != "abc" || len(messages) != 0 {
		t.Fatalf("output = %q, messages = %+v", got, messages)
	}
}

func TestFilterWritesASequenceThatIsTooLongForAMessage(t *testing.T) {
	long := prefix + testToken + ";" + strings.Repeat("x", maxMessage)
	got, messages := filterAll(t, long, "\aend")
	if got != long+"\aend" || len(messages) != 0 {
		t.Fatalf("got %d bytes with %d messages, want the %d bytes unchanged", len(got), len(messages), len(long)+4)
	}
}

func TestFilterWritesTheStartOfAPrefixWhenTheOutputStops(t *testing.T) {
	read, write := io.Pipe()
	out := &lockedBuffer{}
	go func() { _ = Filter(out, read, testToken, func(Message) {}) }()
	defer func() { _ = write.Close() }()
	// A prompt can end with bytes that also start a message.
	if _, err := write.Write([]byte("prompt \x1b]7")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for out.String() != "prompt \x1b]7" {
		if time.Now().After(deadline) {
			t.Fatalf("output = %q while the stream stays open, want all bytes", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
