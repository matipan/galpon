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

// prepareClaudeTerminal saves the Claude Code settings that a person makes on
// first use, so that its terminal opens without onboarding or trust screens.
func prepareClaudeTerminal(t *testing.T, instance *nativeInstance, cwd string) {
	t.Helper()
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
	settings["projects"] = map[string]any{cwd: map[string]any{"hasTrustDialogAccepted": true}}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// logPaneOnFailure saves the terminal output of a failed test. It reads the
// pane that pane holds when the test ends.
func logPaneOnFailure(t *testing.T, instance *nativeInstance, herdr string, pane *string) {
	t.Cleanup(func() {
		if t.Failed() {
			command := exec.Command(herdr, "--session", instance.herdrSession, "pane", "read", *pane, "--lines", "100")
			command.Env = instance.env
			data, _ := command.CombinedOutput()
			t.Logf("native Claude terminal: %s", data)
		}
	})
}

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
	prepareClaudeTerminal(t, instance, instance.idle(t, created.ID).Placement.CWD)
	opened, err := instance.client.OpenAgent(t.Context(), created.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	pane := opened.RendererID
	logPaneOnFailure(t, instance, binaries["herdr"], &pane)
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
		writerStopped := false
		for _, candidate := range view.Messages {
			if candidate.ID == message.ID && candidate.Status == "failed" && view.Agent.RuntimeID == "" {
				writerStopped = true
				break
			}
		}
		if writerStopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("writer exit did not fail the blocked delivery: %#v", view.Agent)
		}
		time.Sleep(100 * time.Millisecond)
	}
	opened, err = instance.client.OpenAgent(t.Context(), created.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	pane = opened.RendererID
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "for shortcuts", "--timeout", "20000")
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-text", pane, "NATIVE_REMEMBER after_reopen")
	time.Sleep(400 * time.Millisecond)
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-keys", pane, "enter")
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "remembered:after_reopen", "--timeout", "20000")
	reopened := instance.idle(t, created.ID)
	if reopened.RuntimeID == "" || reopened.RuntimeID == runtimeID || reopened.LastError != "" {
		t.Fatalf("agent did not resume after its unstarted delivery failed: %#v", reopened)
	}
}

func TestGalponAcceptsTheClaudeChannelWarningInTheAgentTerminal(t *testing.T) {
	binaries := nativeBinaries(t)
	if binaries["herdr"] == "" {
		t.Skip("Herdr is not installed")
	}
	mock := httptest.NewServer(&nativeModel{})
	t.Cleanup(mock.Close)
	keyPath := ""
	instance := newNativeInstance(t, binaries, mock.URL, "", true, func(instance *nativeInstance) {
		// Claude Code shows its development channel warning only to a claude.ai
		// login. This command comes first in PATH. It shows the warning screen of
		// Claude Code 2.1.314, saves the key that it gets, and then runs the real
		// Claude Code if that key is Enter.
		keyPath = filepath.Join(instance.root, "warning-key")
		quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }
		script := `#!/bin/sh
case " $* " in
*" --dangerously-load-development-channels server:galpon "*)
	printf '\033[2J\033[H'
	printf '%s\n' '  WARNING: Loading development channels' '' \
		'  --dangerously-load-development-channels is for local channel development only.' '' \
		'  Channels: server:galpon' '' \
		'  ❯ 1. I am using this for local development' '    2. Exit' '' \
		'  Enter to confirm · Esc to cancel'
	stty raw -echo
	key=$(dd bs=1 count=1 2>/dev/null | od -An -tx1 | tr -d ' \n')
	stty sane
	printf '%s' "$key" > ` + quote(keyPath) + `
	[ "$key" = 0d ] || exit 1
	;;
esac
exec ` + quote(binaries["claude"]) + ` "$@"
`
		bin := filepath.Join(instance.root, "bin")
		if err := os.MkdirAll(bin, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		for index, value := range instance.env {
			if strings.HasPrefix(value, "PATH=") {
				instance.env[index] = "PATH=" + bin + string(os.PathListSeparator) + strings.TrimPrefix(value, "PATH=")
			}
		}
	})
	created := instance.create(t, map[string]any{"title": "claude", "harness": "claude"})
	prepareClaudeTerminal(t, instance, instance.idle(t, created.ID).Placement.CWD)
	opened, err := instance.client.OpenAgent(t.Context(), created.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	pane := opened.RendererID
	logPaneOnFailure(t, instance, binaries["herdr"], &pane)

	// Nobody touches the terminal. Galpon accepts the warning, and Claude Code
	// starts.
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "for shortcuts", "--timeout", "30000")
	key, err := os.ReadFile(keyPath)
	if err != nil || string(key) != "0d" {
		t.Fatalf("key on the warning = %q, %v; want Enter", key, err)
	}
	log, err := os.ReadFile(filepath.Join(instance.state, "galpon.log"))
	if err != nil || !strings.Contains(string(log), "accepted the Claude Code development channel warning for agent "+created.ID) {
		t.Fatalf("the daemon did not accept the warning: %v\n%s", err, log)
	}
}
