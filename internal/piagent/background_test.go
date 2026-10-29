package piagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type backgroundRequestMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
}

func (m backgroundRequestMessage) text() string {
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(m.Content, &parts)
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(part.Text)
	}
	return builder.String()
}

type backgroundToolCall struct {
	id   string
	name string
	args map[string]any
}

func writeBackgroundToolCalls(w http.ResponseWriter, calls ...backgroundToolCall) {
	deltas := []any{}
	for index, call := range calls {
		args, _ := json.Marshal(call.args)
		deltas = append(deltas, map[string]any{"index": index, "id": call.id, "type": "function", "function": map[string]any{"name": call.name, "arguments": string(args)}})
	}
	writeBackgroundChunks(w, map[string]any{"role": "assistant", "tool_calls": deltas}, "tool_calls")
}

func writeBackgroundText(w http.ResponseWriter, text string) {
	writeBackgroundChunks(w, map[string]any{"role": "assistant", "content": text}, "stop")
}

func writeBackgroundChunks(w http.ResponseWriter, delta map[string]any, finish string) {
	w.Header().Set("Content-Type", "text/event-stream")
	chunk, _ := json.Marshal(map[string]any{"id": "background", "object": "chat.completion.chunk", "created": 1, "model": "mock", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
	end, _ := json.Marshal(map[string]any{"id": "background", "object": "chat.completion.chunk", "created": 1, "model": "mock", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunk, end)
}

// The model gets each background notice at its next tool call while it works,
// and as a new turn while it is idle. The steps run against a real Pi in RPC
// mode with a local mock model.
func TestBackgroundTasksReachTheModelWithRealPi(t *testing.T) {
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	var mu sync.Mutex
	requests := map[string][]backgroundRequestMessage{}
	saved := func(label string) []backgroundRequestMessage {
		mu.Lock()
		defer mu.Unlock()
		return requests[label]
	}
	monitorID := regexp.MustCompile(`Monitor (m[0-9a-f]{7}) started`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []backgroundRequestMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Messages) == 0 {
			t.Errorf("invalid model request: %v", err)
			return
		}
		messages := payload.Messages
		last := messages[len(messages)-1]
		step := ""
		for _, message := range messages {
			if message.Role == "user" && strings.HasPrefix(message.text(), "STEP ") {
				step = strings.TrimPrefix(message.text(), "STEP ")
			}
		}
		record := func(label string) {
			mu.Lock()
			defer mu.Unlock()
			requests[label] = messages
		}
		prompt := last.Role == "user" && strings.HasPrefix(last.text(), "STEP ")
		notice := last.Role == "user" && strings.Contains(last.text(), "<task-notification>")
		switch {
		case prompt && step == "mid":
			writeBackgroundToolCalls(w,
				backgroundToolCall{"mid-monitor", "Monitor", map[string]any{"description": "mid events", "command": "sleep 0.3; printf 'MID-1\\nMID-2\\n'; sleep 30"}},
				backgroundToolCall{"mid-wait", "bash", map[string]any{"command": "sleep 1.5"}},
			)
		case notice && step == "mid":
			record("mid notice")
			id := ""
			for _, message := range messages {
				if match := monitorID.FindStringSubmatch(message.text()); match != nil {
					id = match[1]
				}
			}
			writeBackgroundToolCalls(w, backgroundToolCall{"mid-stop", "TaskStop", map[string]any{"task_id": id}})
		case last.Role == "tool" && last.ToolCallID == "mid-stop":
			record("mid stop")
			writeBackgroundText(w, "mid done")
		case prompt && step == "idle":
			writeBackgroundToolCalls(w, backgroundToolCall{"idle-job", "bash", map[string]any{"command": "sleep 0.5; echo IDLE-DONE; exit 3", "run_in_background": true, "description": "idle job"}})
		case last.Role == "tool" && last.ToolCallID == "idle-job":
			record("idle start")
			writeBackgroundText(w, "waiting for the idle job")
		case notice && step == "idle":
			record("idle notice")
			writeBackgroundText(w, "idle notice handled")
		case prompt && step == "noise":
			writeBackgroundToolCalls(w,
				backgroundToolCall{"noise-monitor", "Monitor", map[string]any{"description": "noisy", "command": "seq 1 100000; sleep 30"}},
				backgroundToolCall{"noise-wait", "bash", map[string]any{"command": "sleep 1"}},
			)
		case notice && step == "noise":
			record("noise notice")
			writeBackgroundText(w, "noise done")
		case prompt && step == "reload":
			writeBackgroundToolCalls(w, backgroundToolCall{"reload-monitor", "Monitor", map[string]any{"description": "after reload", "command": "sleep 2; echo AFTER-RELOAD", "persistent": true}})
		case last.Role == "tool" && last.ToolCallID == "reload-monitor":
			writeBackgroundText(w, "reload armed")
		case notice && step == "reload":
			record("reload notice")
			writeBackgroundText(w, "reload notice handled")
		case prompt && step == "answer":
			writeBackgroundToolCalls(w, backgroundToolCall{"answer-monitor", "Monitor", map[string]any{"description": "late event", "command": "sleep 0.3; echo LATE-EVENT"}})
		case last.Role == "tool" && last.ToolCallID == "answer-monitor":
			// The monitor ends while Pi streams the final answer.
			time.Sleep(time.Second)
			writeBackgroundText(w, "FINAL ANSWER TEXT")
		case notice && step == "answer":
			record("answer notice")
			writeBackgroundText(w, "EVENT REPLY TEXT")
		default:
			t.Errorf("unexpected model request in step %q: %s: %s", step, last.Role, last.text())
			writeBackgroundText(w, "unexpected")
		}
	}))
	defer server.Close()

	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(agentDir, "models.json"), map[string]any{"providers": map[string]any{"background-test": map[string]any{
		"baseUrl": server.URL + "/v1", "api": "openai-completions", "apiKey": "test-only",
		"models": []any{map[string]any{"id": "mock", "name": "Local mock", "reasoning": false, "input": []string{"text"}, "contextWindow": 32768, "maxTokens": 4096, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}},
	}}})
	fixture, err := filepath.Abs(filepath.Join("testdata", "background-rpc-test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(root, "answers.jsonl")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, pi, "--mode", "rpc", "--provider", "background-test", "--model", "mock", "--extension", fixture)
	command.Dir = root
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMPDIR=" + root, "LANG=C.UTF-8", "PI_CODING_AGENT_DIR=" + agentDir, "PI_TELEMETRY=0", "GALPON_BACKGROUND_TEST_RESULT=" + resultPath}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	events := make(chan map[string]any, 1024)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
		for scanner.Scan() {
			var event map[string]any
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				events <- event
			}
		}
	}()
	defer func() {
		// Closing stdin ends Pi in order, so that it stops the remaining tasks.
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = command.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}()
	sequence := 0
	send := func(text string) {
		t.Helper()
		sequence++
		if err := json.NewEncoder(stdin).Encode(map[string]any{"id": fmt.Sprint(sequence), "type": "prompt", "message": text}); err != nil {
			t.Fatal(err)
		}
	}
	settled := func(label string) {
		t.Helper()
		timeout := time.After(30 * time.Second)
		for {
			select {
			case event, ok := <-events:
				if !ok {
					t.Fatalf("Pi stopped before %s settled\n%s", label, stderr.String())
				}
				if event["type"] == "extension_error" || event["type"] == "response" && event["success"] == false {
					t.Fatalf("Pi reported an error during %s: %#v\n%s", label, event, stderr.String())
				}
				if event["type"] == "agent_settled" {
					return
				}
			case <-timeout:
				t.Fatalf("%s did not settle\n%s", label, stderr.String())
			}
		}
	}
	notices := func(label string) (string, backgroundRequestMessage) {
		t.Helper()
		messages := saved(label)
		if len(messages) < 2 {
			t.Fatalf("the model did not get the %s", label)
		}
		return messages[len(messages)-1].text(), messages[len(messages)-2]
	}

	// A notice reaches a working agent at its next tool call. Lines that
	// arrive together come as one notice.
	send("STEP mid")
	settled("the mid-run step")
	text, previous := notices("mid notice")
	if previous.Role != "tool" {
		t.Errorf("the mid-run notice did not follow a tool result: %s", previous.Role)
	}
	if strings.Count(text, "<task-notification>") != 1 || !strings.Contains(text, "<status>running</status>") || !strings.Contains(text, "MID-1\nMID-2") {
		t.Errorf("the monitor lines did not arrive as one running event:\n%s", text)
	}
	if saved("mid stop") == nil {
		t.Error("TaskStop did not stop the monitor")
	}

	// The end of a background command starts a new turn for an idle agent.
	send("STEP idle")
	settled("the idle step")
	if saved("idle notice") != nil {
		t.Error("the background command notice came before the agent was idle")
	}
	settled("the idle notice")
	text, previous = notices("idle notice")
	if previous.Role != "assistant" || !strings.Contains(previous.text(), "waiting for the idle job") {
		t.Errorf("the idle notice did not start a new turn after the answer: %s %q", previous.Role, previous.text())
	}
	for _, want := range []string{"<task-type>background command</task-type>", "<description>idle job</description>", "<status>failed</status>", "exited with code 3", "IDLE-DONE"} {
		if !strings.Contains(text, want) {
			t.Errorf("the background command notice omitted %q:\n%s", want, text)
		}
	}

	// A flood of lines stops the monitor with one notice.
	send("STEP noise")
	settled("the noise step")
	text, _ = notices("noise notice")
	for _, want := range []string{"<status>stopped</status>", "Galpon stopped this monitor", "more lines are in the output file"} {
		if !strings.Contains(text, want) {
			t.Errorf("the noisy monitor notice omitted %q:\n%s", want, text)
		}
	}

	// A monitor continues through an extension reload.
	send("STEP reload")
	settled("the reload step")
	send("/background-reload")
	settled("the notice after the reload")
	text, _ = notices("reload notice")
	if !strings.Contains(text, "AFTER-RELOAD") {
		t.Errorf("the monitor event after the reload did not arrive:\n%s", text)
	}

	// A notice that continues the run after its final answer keeps that answer.
	send("STEP answer")
	settled("the answer step")
	if saved("answer notice") == nil {
		t.Fatal("the late notice did not continue the run after the final answer")
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var answer struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Answer != "FINAL ANSWER TEXT" {
		t.Errorf("the saved answer is %q, not the final answer before the notice", answer.Answer)
	}
}
