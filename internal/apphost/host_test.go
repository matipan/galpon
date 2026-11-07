package apphost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A tiny PNG of 3 x 2 pixels: the host reads its size from the header.
var testPNG = func() []byte {
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 13, 'I', 'H', 'D', 'R', 0, 0, 0, 3, 0, 0, 0, 2, 8, 6, 0, 0, 0}
	return data
}()

func TestMain(m *testing.M) {
	if os.Getenv("APPHOST_FAKE_PI") == "1" {
		fakePi()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakePi speaks a small part of the Pi RPC protocol: it answers the state
// commands, runs a prompt as one turn with an image, and asks a question
// for the prompt "ask".
func fakePi() {
	out := json.NewEncoder(os.Stdout)
	write := func(value any) { _ = out.Encode(value) }
	write(map[string]any{"type": "extension_ui_request", "id": "w1", "method": "setWidget", "widgetKey": BridgePrefix + "galpon", "widgetLines": []string{`{"agentId":"agent","title":"Helper"}`}})
	var messages []any
	input := bufio.NewScanner(os.Stdin)
	input.Buffer(make([]byte, 1<<20), 16<<20)
	for input.Scan() {
		var command map[string]any
		if json.Unmarshal(input.Bytes(), &command) != nil {
			continue
		}
		id, _ := command["id"].(string)
		kind, _ := command["type"].(string)
		respond := func(data any) {
			write(map[string]any{"type": "response", "id": id, "command": kind, "success": true, "data": data})
		}
		switch kind {
		case "get_messages":
			respond(map[string]any{"messages": messages})
		case "get_state":
			respond(map[string]any{"thinkingLevel": "high", "sessionId": "session"})
		case "get_session_stats", "get_available_thinking_levels":
			respond(map[string]any{})
		case "get_commands":
			respond(map[string]any{"commands": []any{map[string]any{"name": "compact"}}})
		case "prompt":
			text, _ := command["message"].(string)
			write(map[string]any{"type": "agent_start"})
			if text == "ask" {
				write(map[string]any{"type": "extension_ui_request", "id": "d1", "method": "select", "title": "Which one?", "options": []string{"a", "b"}})
				respond(nil)
				continue
			}
			user := map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}
			reply := map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "done"},
				map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(testPNG)},
			}}
			messages = append(messages, user, reply)
			write(map[string]any{"type": "message_end", "message": user})
			write(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "done"}})
			write(map[string]any{"type": "message_end", "message": reply})
			write(map[string]any{"type": "agent_end", "messages": messages})
			write(map[string]any{"type": "agent_settled"})
			respond(nil)
		case "extension_ui_response":
			write(map[string]any{"type": "agent_settled"})
		}
	}
}

// statusLog keeps the OSC 7501 reports of the host.
type statusLog struct {
	mu   sync.Mutex
	text strings.Builder
}

func (s *statusLog) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text.Write(data)
}

func (s *statusLog) states() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, part := range strings.Split(s.text.String(), "\x1b]7501;")[1:] {
		fields := strings.TrimSuffix(part, "\x1b\\")
		state, _, _ := strings.Cut(strings.TrimPrefix(fields, "state="), ":")
		out = append(out, state)
	}
	return out
}

type testHost struct {
	t      *testing.T
	host   *Host
	status *statusLog
	done   chan error
	home   string
	dir    string
}

func startHost(t *testing.T) *testHost {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	status := &statusLog{}
	host, err := New(Options{
		AgentID: "agent", Dir: filepath.Join(root, "state"), AppHome: filepath.Join(root, "home"), CWD: root,
		Command: []string{executable}, Env: append(os.Environ(), "APPHOST_FAKE_PI=1"), Status: status,
		Rename: func(_ context.Context, title string) (string, error) { return strings.TrimSpace(title), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &testHost{t: t, host: host, status: status, done: make(chan error, 1), home: filepath.Join(root, "home"), dir: filepath.Join(root, "state")}
	go func() { h.done <- host.Run(context.Background()) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := ReadInfo(h.dir); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the host did not write host.json")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() {
		host.Stop()
		select {
		case err := <-h.done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			t.Error("the host did not stop")
		}
	})
	return h
}

func (h *testHost) request(method, path string, body any, token string) (int, map[string]any) {
	h.t.Helper()
	var reader io.Reader
	if text, ok := body.(string); ok {
		reader = strings.NewReader(text)
	} else if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	info, ok := ReadInfo(h.dir)
	if !ok {
		h.t.Fatal("no host.json")
	}
	request, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", info.Port, path), reader)
	request.Header.Set(TokenHeader, token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(response.Body).Decode(&out)
	return response.StatusCode, out
}

func (h *testHost) call(method, path string, body any) map[string]any {
	h.t.Helper()
	status, out := h.request(method, path, body, h.host.Token())
	if status >= 300 {
		h.t.Fatalf("%s %s: %d %v", method, path, status, out)
	}
	return out
}

// eventsUntil polls until an event of the type arrives and returns every
// event so far.
func (h *testHost) eventsUntil(kind string) []map[string]any {
	h.t.Helper()
	var all []map[string]any
	since := 0.0
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := h.call(http.MethodGet, fmt.Sprintf("/events?since=%d&wait=2000", int(since)), nil)
		since, _ = out["seq"].(float64)
		events, _ := out["events"].([]any)
		for _, item := range events {
			event, _ := item.(map[string]any)
			all = append(all, event)
			if event["type"] == kind {
				return all
			}
		}
	}
	h.t.Fatalf("no %s event in %v", kind, all)
	return nil
}

func (h *testHost) waitState(want string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if states := h.status.states(); len(states) > 0 && states[len(states)-1] == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("the host reported %v, not %s", h.status.states(), want)
}

func TestHostRunsATurnForTheTab(t *testing.T) {
	h := startHost(t)
	if status, _ := h.request(http.MethodGet, "/state", nil, "wrong"); status != http.StatusUnauthorized {
		t.Fatalf("a request without the token got %d", status)
	}
	if state := h.call(http.MethodGet, "/state", nil); state["alive"] != true || state["agentId"] != "agent" {
		t.Fatalf("state: %v", state)
	}
	h.call(http.MethodPost, "/rpc", map[string]any{"type": "prompt", "message": "hello"})
	events := h.eventsUntil("agent_settled")
	var image map[string]any
	for _, event := range events {
		if event["type"] == "extension_ui_request" && strings.HasPrefix(fmt.Sprint(event["widgetKey"]), BridgePrefix) {
			t.Fatalf("a bridge widget reached the tab: %v", event)
		}
		if event["type"] != "message_end" {
			continue
		}
		message, _ := event["message"].(map[string]any)
		for _, item := range message["content"].([]any) {
			if block, _ := item.(map[string]any); block["type"] == "image" {
				image = block
			}
		}
	}
	if image == nil || image["data"] != nil || image["width"] != 3.0 || image["height"] != 2.0 {
		t.Fatalf("the image did not become a file: %v", image)
	}
	if data, err := os.ReadFile(filepath.Join(h.home, fmt.Sprint(image["path"]))); err != nil || !bytes.Equal(data, testPNG) {
		t.Fatalf("image file: %v", err)
	}
	copied := h.call(http.MethodGet, "/image?path="+fmt.Sprint(image["path"]), nil)
	if copied["data"] != base64.StdEncoding.EncodeToString(testPNG) {
		t.Fatalf("a screen could not copy the image: %v", copied)
	}
	// After the run the agent is done until the person looks at the tab.
	h.waitState("done")
	h.call(http.MethodPost, "/seen", nil)
	h.waitState("idle")
	snapshot := h.call(http.MethodGet, "/snapshot", nil)
	bridge, _ := snapshot["bridge"].(map[string]any)
	galpon, _ := bridge["galpon"].(map[string]any)
	if snapshot["total"] != 2.0 || galpon["title"] != "Helper" {
		t.Fatalf("snapshot: total %v bridge %v", snapshot["total"], bridge)
	}
	renamed := h.call(http.MethodPost, "/galpon/rename", map[string]string{"title": " Reviewer "})
	if renamed["title"] != "Reviewer" {
		t.Fatalf("rename: %v", renamed)
	}
	if states := h.status.states(); !containsInOrder(states, "working", "done", "idle") {
		t.Fatalf("reports: %v", states)
	}
}

func TestHostHoldsADialogForThePerson(t *testing.T) {
	h := startHost(t)
	h.call(http.MethodPost, "/rpc", map[string]any{"type": "prompt", "message": "ask"})
	h.eventsUntil("extension_ui_request")
	h.waitState("blocked")
	dialogs, _ := h.call(http.MethodGet, "/snapshot", nil)["dialogs"].([]any)
	if len(dialogs) != 1 {
		t.Fatalf("open dialogs: %v", dialogs)
	}
	h.call(http.MethodPost, "/ui", map[string]any{"id": "d1", "value": "b"})
	h.eventsUntil("dialog_closed")
	if status, _ := h.request(http.MethodPost, "/ui", map[string]any{"id": "d1", "value": "b"}, h.host.Token()); status != http.StatusNotFound {
		t.Fatalf("a second answer got %d", status)
	}
}

func TestHostJoinsTheChunksOfAScreen(t *testing.T) {
	h := startHost(t)
	body, _ := json.Marshal(map[string]any{"type": "prompt", "message": "from the laptop"})
	half := len(body) / 2
	h.call(http.MethodPost, "/chunk?id=up1&index=1&count=2&to=/rpc", string(body[half:]))
	h.call(http.MethodPost, "/chunk?id=up1&index=0&count=2&to=/rpc", string(body[:half]))
	events := h.eventsUntil("agent_settled")
	found := false
	for _, event := range events {
		message, _ := event["message"].(map[string]any)
		if event["type"] == "message_end" && message["role"] == "user" && strings.Contains(fmt.Sprint(message["content"]), "from the laptop") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the joined prompt did not run: %v", events)
	}
}

func TestHostStopsOnRequest(t *testing.T) {
	h := startHost(t)
	h.call(http.MethodPost, "/shutdown", nil)
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
		h.done <- nil
	case <-time.After(15 * time.Second):
		t.Fatal("the host did not stop")
	}
	if _, err := os.Stat(filepath.Join(h.dir, "host.json")); !os.IsNotExist(err) {
		t.Fatalf("host.json stayed: %v", err)
	}
}

func containsInOrder(values []string, want ...string) bool {
	for _, value := range values {
		if len(want) > 0 && value == want[0] {
			want = want[1:]
		}
	}
	return len(want) == 0
}
