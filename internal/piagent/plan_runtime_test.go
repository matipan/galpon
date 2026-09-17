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
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativePlanRealPiRPC(t *testing.T) {
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	const plan = "# RPC plan\n\nKeep exact trailing spaces.  \n"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var payload struct {
			Tools []struct {
				Function struct{ Name string } `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		names := []string{}
		for _, tool := range payload.Tools {
			names = append(names, tool.Function.Name)
		}
		for _, name := range []string{"write", "edit", "bash"} {
			if slices.Contains(names, name) {
				t.Errorf("Plan exposed implementation tool %s to the real provider", name)
			}
		}
		for _, name := range []string{"read", "grep", "find", "ls", "plan_mode_complete", "galpon_test_insight"} {
			if !slices.Contains(names, name) {
				t.Errorf("Plan omitted %s from the real provider request", name)
			}
		}
		args, _ := json.Marshal(map[string]string{"plan": plan})
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := map[string]any{"id": "plan-test", "object": "chat.completion.chunk", "created": 1, "model": "mock", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "plan-call", "type": "function", "function": map[string]any{"name": "plan_mode_complete", "arguments": string(args)}}}}, "finish_reason": nil}}}
		data, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: {\"id\":\"plan-test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", data)
	}))
	defer server.Close()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(agentDir, "models.json"), map[string]any{"providers": map[string]any{"plan-test": map[string]any{
		"baseUrl": server.URL + "/v1", "api": "openai-completions", "apiKey": "test-only",
		"models": []any{map[string]any{"id": "mock", "name": "Local mock", "reasoning": false, "input": []string{"text"}, "contextWindow": 32768, "maxTokens": 4096, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}},
	}}})
	fixture, err := filepath.Abs(filepath.Join("testdata", "plan-rpc-test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(root, "result.json")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, pi, "--mode", "rpc", "--provider", "plan-test", "--model", "mock", "--extension", fixture)
	command.Dir = root
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "LANG=C.UTF-8", "PI_CODING_AGENT_DIR=" + agentDir, "PI_TELEMETRY=0", "GALPON_PLAN_RPC_RESULT=" + resultPath}
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
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	next := func() map[string]any {
		if !scanner.Scan() {
			t.Fatalf("Pi RPC stopped: %v", scanner.Err())
		}
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event["type"] == "extension_error" {
			t.Fatalf("Pi extension error: %#v", event)
		}
		return event
	}
	sequence := 0
	send := func(text string) {
		sequence++
		id := fmt.Sprint(sequence)
		if err := json.NewEncoder(stdin).Encode(map[string]any{"id": id, "type": "prompt", "message": text}); err != nil {
			t.Fatal(err)
		}
		for {
			event := next()
			if event["type"] == "response" && event["id"] == id {
				if event["success"] != true {
					t.Fatalf("RPC command failed: %#v", event)
				}
				return
			}
		}
	}
	type report struct {
		Active      []string `json:"active"`
		Completed   []string `json:"completed"`
		Implemented []string `json:"implemented"`
	}
	readReport := func() report {
		data, err := os.ReadFile(resultPath)
		if err != nil {
			t.Fatal(err)
		}
		var value report
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	send("/plan-probe")
	normal := readReport().Active
	send("/plan")
	if requests.Load() != 0 {
		t.Fatal("/plan started a model turn")
	}
	send("Create a small test plan. Do not implement it.")
	for next()["type"] != "agent_settled" {
	}
	send("/plan-probe")
	value := readReport()
	if requests.Load() != 1 || len(value.Completed) != 1 || value.Completed[0] != plan {
		t.Fatalf("completion did not terminate with the exact plan: calls=%d report=%#v", requests.Load(), value)
	}
	send("/plan do")
	send("/plan-probe")
	value = readReport()
	if !slices.Equal(value.Active, normal) || len(value.Implemented) != 1 || value.Implemented[0] != plan {
		t.Fatalf("implementation did not restore tools or receive the exact plan: %#v", value)
	}
}
