package piagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

func TestMaterializeInstallsPiExtensionAndCompleteTheme(t *testing.T) {
	values, err := Materialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	extension, err := os.ReadFile(values.Extension)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"galpon_create_workspace", "galpon_create_agent", "galpon_send_agent", "galpon_await_agent"} {
		if !strings.Contains(string(extension), name) {
			t.Errorf("extension omitted %s", name)
		}
	}
	for _, want := range []string{"provides optional tools", "roles and names do not have special built-in behavior", "only when the user requests coordination", "batches queued cross-agent messages", "Never create a synchronous wait cycle", "queued or delivered result is still pending", "settled without a final text response", "response closed before it completed"} {
		if !strings.Contains(string(extension), want) {
			t.Errorf("extension prompt omitted %q", want)
		}
	}
	for _, unwanted := range []string{"when separate work is useful", "A captain is", "Use galpon_send_agent to delegate"} {
		if strings.Contains(string(extension), unwanted) {
			t.Errorf("extension prompt still encourages delegation with %q", unwanted)
		}
	}
	data, err := os.ReadFile(values.Theme)
	if err != nil {
		t.Fatal(err)
	}
	var theme struct {
		Name   string            `json:"name"`
		Colors map[string]string `json:"colors"`
	}
	if err := json.Unmarshal(data, &theme); err != nil {
		t.Fatal(err)
	}
	if theme.Name != "galpon-tokyonight-moon" || theme.Colors["accent"] != "blue" || theme.Colors["selectedBg"] != "selection" {
		t.Fatalf("theme = %#v", theme)
	}
}

func TestCommandUsesExactDurableSessionWithProjectTrust(t *testing.T) {
	cfg := config.Config{StateDir: "/state", PiBin: "/bin/pi", PiProvider: "openai-codex", PiModel: "gpt-test"}
	args := Command(cfg, Assets{Extension: "/state/pi.ts", Theme: "/state/moon.json"}, model.Agent{ID: "agent-id", SessionID: "session-id", SessionPath: "/state/session.jsonl", Title: "Builder"}, "")
	for _, want := range []string{"/bin/pi", "--approve", "--provider", "openai-codex", "--session-id", "session-id", "--session-dir", filepath.Join("/state", "agents", "agent-id", "sessions"), "--extension", "/state/pi.ts", "--no-themes", "--theme", "/state/moon.json", "--model", "gpt-test"} {
		if !slices.Contains(args, want) {
			t.Errorf("Pi command omitted %q: %#v", want, args)
		}
	}
}

func TestCommandForksContextIntoExactDurableSession(t *testing.T) {
	cfg := config.Config{StateDir: "/state", PiBin: "/bin/pi", PiProvider: "openai-codex"}
	agent := model.Agent{ID: "agent-id", SessionID: "agent-id", ContextAgentID: "source", Title: "Builder"}
	args := Command(cfg, Assets{Extension: "/state/pi.ts", Theme: "/state/moon.json"}, agent, "/source/session.jsonl")
	for _, want := range []string{"--fork", "/source/session.jsonl", "--session-id", "agent-id"} {
		if !slices.Contains(args, want) {
			t.Errorf("Pi fork command omitted %q: %#v", want, args)
		}
	}
}
