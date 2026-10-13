package e2e

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/app"
)

func useDefaultClaudeHome(n *nativeInstance) {
	n.env = slices.DeleteFunc(n.env, func(value string) bool {
		return strings.HasPrefix(value, "CLAUDE_CONFIG_DIR=")
	})
}

func TestCodexFreshTerminalSavesEmptyHistoryWithoutPrompt(t *testing.T) {
	for _, state := range []string{"new", "unsaved-session"} {
		t.Run(state, func(t *testing.T) { checkCodexEmptyTerminal(t, state == "unsaved-session") })
	}
}

func checkCodexEmptyTerminal(t *testing.T, unsaved bool) {
	t.Helper()
	binaries := nativeBinaries(t)
	if binaries["herdr"] == "" {
		t.Skip("Herdr is not installed")
	}
	mock := httptest.NewServer(&nativeModel{})
	t.Cleanup(mock.Close)
	instance := newNativeInstance(t, binaries, mock.URL, "", true)
	agent, err := instance.client.CreateAgent(t.Context(), app.CreateAgentRequest{
		Title: "Fresh Codex", Harness: "codex", WorkspaceID: instance.workspace.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if unsaved {
		db, err := sql.Open("sqlite", filepath.Join(instance.state, "galpon.db"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec("update agents set session_id=? where id=?", "00000000-0000-4000-8000-000000000001", agent.ID)
		_ = db.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	view, err := instance.client.Agent(t.Context(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.OpenFile(filepath.Join(instance.root, "codex", "config.toml"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(config, "\n[projects.%q]\ntrust_level = \"trusted\"\n", view.Agent.Placement.CWD)
	_ = config.Close()
	if err != nil {
		t.Fatal(err)
	}
	opened, err := instance.client.OpenAgent(t.Context(), agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	pane := opened.RendererID
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "gpt-5.6", "--timeout", "20000")
	empty := instance.idle(t, agent.ID)
	if empty.SessionPath == "" {
		t.Fatal("empty foreground thread has no durable metadata")
	}
	data, err := os.ReadFile(empty.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row["type"] == "response_item" && nativeObject(row["payload"])["role"] == "user" {
			t.Fatal("opening an empty terminal submitted a user prompt")
		}
	}
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-keys", pane, "ctrl+d")
	deadline := time.Now().Add(15 * time.Second)
	for {
		current, err := instance.client.Agent(t.Context(), agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Agent.RuntimeID == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("empty Codex terminal did not exit")
		}
		time.Sleep(50 * time.Millisecond)
	}
	opened, err = instance.client.OpenAgent(t.Context(), agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	pane = opened.RendererID
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "gpt-5.6", "--timeout", "20000")
	reopened := instance.idle(t, agent.ID)
	if reopened.SessionID != empty.SessionID {
		t.Fatal("reopening lost the empty thread identity")
	}
	for _, pair := range [][2]string{{"NATIVE_LIST agents", "listed:agents"}, {"NATIVE_REMEMBER first_turn", "remembered:first_turn"}} {
		herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-text", pane, pair[0])
		time.Sleep(400 * time.Millisecond)
		herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-keys", pane, "enter")
		herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", pair[1], "--timeout", "20000")
	}
	message := instance.send(t, agent.ID, "NATIVE_RECALL first_turn")
	completed := waitNativeMessage(t, instance.client, agent.ID, message.ID, "recalled:first_turn")
	if completed.Agent.SessionID != empty.SessionID {
		t.Fatal("terminal and controller used different conversations")
	}
}

func TestClaudeDefaultHomeKeepsConversationAndTools(t *testing.T) {
	binaries := nativeBinaries(t)
	if binaries["herdr"] == "" {
		t.Skip("Herdr is not installed")
	}
	mock := httptest.NewServer(&nativeModel{})
	t.Cleanup(mock.Close)
	instance := newNativeInstance(t, binaries, mock.URL, "", true, useDefaultClaudeHome)
	agent, err := instance.client.CreateAgent(t.Context(), app.CreateAgentRequest{
		Title: "claude", Harness: "claude", WorkspaceID: instance.workspace.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := instance.client.Agent(t.Context(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{
		"hasCompletedOnboarding": true, "theme": "dark", "lastOnboardingVersion": "2.1.278",
		"customApiKeyResponses": map[string]any{"approved": []string{"fixture-only"}, "rejected": []string{}},
		"projects":              map[string]any{view.Agent.Placement.CWD: map[string]any{"hasTrustDialogAccepted": true}},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instance.root, ".claude.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := instance.client.OpenAgent(t.Context(), agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	pane := opened.RendererID
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "for shortcuts", "--timeout", "20000")
	input := func(prompt, reply string) {
		t.Helper()
		herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-text", pane, prompt)
		time.Sleep(400 * time.Millisecond)
		herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-keys", pane, "enter")
		herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", reply, "--timeout", "20000")
	}
	input("NATIVE_REMEMBER default_home", "remembered:default_home")
	input("NATIVE_LIST agents", "listed:agents")
	saved := instance.idle(t, agent.ID)
	if saved.SessionPath == "" {
		t.Fatal("Claude conversation has no managed copy")
	}
	data, err = os.ReadFile(saved.SessionPath)
	if err != nil || !strings.Contains(string(data), "listed:agents") {
		t.Fatalf("managed conversation did not retain the tool result: %v", err)
	}
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-text", pane, "/exit")
	time.Sleep(400 * time.Millisecond)
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-keys", pane, "enter")
	deadline := time.Now().Add(15 * time.Second)
	for {
		current, err := instance.client.Agent(t.Context(), agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Agent.RuntimeID == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Claude did not exit")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// A native-only history must resume even without a registered managed copy.
	db, err := sql.Open("sqlite", filepath.Join(instance.state, "galpon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("update agents set session_path='' where id=?", agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(saved.SessionPath); err != nil {
		t.Fatal(err)
	}
	opened, err = instance.client.OpenAgent(t.Context(), agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	pane = opened.RendererID
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "for shortcuts", "--timeout", "20000")
	input("NATIVE_RECALL default_home", "recalled:default_home")
	input("NATIVE_LIST reopened", "listed:reopened")
	resumed := instance.idle(t, agent.ID)
	if resumed.SessionID != saved.SessionID || resumed.SessionPath == "" {
		t.Fatalf("native-only conversation was not recovered: %#v", resumed)
	}
}
