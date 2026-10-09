package e2e

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnavailableClaudeChannelsKeepTheNativeTerminalOpen(t *testing.T) {
	binaries := nativeBinaries(t)
	if binaries["herdr"] == "" {
		t.Skip("Herdr is not installed")
	}
	mock := httptest.NewServer(&nativeModel{})
	t.Cleanup(mock.Close)
	instance := newNativeInstance(t, binaries, mock.URL, "", true)
	created := instance.create(t, map[string]any{"title": "claude", "harness": "claude", "prompt": "NATIVE_REMEMBER heron_claude"})
	waitNativeMessage(t, instance.client, created.ID, created.InitialMessage.ID, "remembered:heron_claude")
	before := instance.idle(t, created.ID)
	settingsPath := filepath.Join(instance.root, "claude", ".claude.json")
	settings := map[string]any{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatal(err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	settings["hasCompletedOnboarding"] = true
	settings["theme"] = "dark"
	settings["lastOnboardingVersion"] = "2.1.278"
	settings["customApiKeyResponses"] = map[string]any{"approved": []string{"fixture-only"}, "rejected": []string{}}
	settings["projects"] = map[string]any{before.Placement.CWD: map[string]any{"hasTrustDialogAccepted": true}}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := instance.client.OpenAgent(t.Context(), created.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	pane := opened.RendererID
	t.Cleanup(func() {
		if t.Failed() {
			command := exec.Command(binaries["herdr"], "--session", instance.herdrSession, "pane", "read", pane, "--lines", "100")
			command.Env = instance.env
			data, _ := command.CombinedOutput()
			t.Logf("native Claude terminal: %s", data)
		}
	})
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "for shortcuts", "--timeout", "20000")
	message := instance.send(t, created.ID, "NATIVE_RECALL heron_claude")
	// Keep unsent user input in the editor across the real evidence deadline.
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-text", pane, "NATIVE_RECALL heron_claude")
	deadline := time.Now().Add(80 * time.Second)
	runtimeID := ""
	for {
		view, err := instance.client.Agent(t.Context(), created.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range view.Messages {
			if candidate.ID == message.ID && (candidate.Status == "completed" || candidate.Status == "failed") {
				t.Fatalf("unconfirmed channel delivery settled while the writer was alive: %#v", candidate)
			}
		}
		if strings.Contains(view.Agent.LastError, "paused incoming work") {
			runtimeID = view.Agent.RuntimeID
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing delivery blocker: %#v", view.Agent)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if runtimeID == "" {
		t.Fatal("blocked channel stopped the native runtime")
	}
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-keys", pane, "enter")
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "recalled:heron_claude", "--timeout", "20000")
	view, err := instance.client.Agent(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Agent.RuntimeID != runtimeID {
		t.Fatal("native terminal restarted and lost its input")
	}
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-text", pane, "/exit")
	time.Sleep(400 * time.Millisecond)
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-keys", pane, "enter")
	deadline = time.Now().Add(15 * time.Second)
	for {
		view, err := instance.client.Agent(t.Context(), created.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range view.Messages {
			if candidate.ID == message.ID && candidate.Status == "failed" && view.Agent.RuntimeID == "" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("writer exit did not fail the blocked delivery: %#v", view.Agent)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
