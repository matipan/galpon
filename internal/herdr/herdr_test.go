package herdr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func TestContextualActivityUsesCurrentPaneSafeLabelsAndNeverDone(t *testing.T) {
	t.Setenv("HERDR_SESSION", "")
	root := t.TempDir()
	logPath := filepath.Join(root, "calls.log")
	bin := filepath.Join(root, "herdr")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + logPath + "\"\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := Adapter{Bin: bin}
	agent := model.Agent{Title: "Parent", SessionID: "session", SessionPath: "/sessions/current.jsonl", Renderer: adapter.Name(), RendererContext: adapter.Context(), RendererID: "pane-1"}
	if err := adapter.ReportContextualActivity(t.Context(), agent, 2); err != nil {
		t.Fatal(err)
	}
	if err := adapter.ReportAgent(t.Context(), agent, "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := adapter.ReportContextualActivity(t.Context(), agent, 0); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	want := []string{
		"pane report-metadata pane-1 --source galpon-context --agent Parent --applies-to-source galpon --state-label working=delegating · 2 active --ttl-ms 10000",
		"pane report-agent pane-1 --source galpon --agent Parent --state working --agent-session-id session --agent-session-path /sessions/current.jsonl --message delegating · 2 active",
		"pane report-metadata pane-1 --source galpon-context --agent Parent --applies-to-source galpon --clear-state-labels",
		"pane report-agent pane-1 --source galpon --agent Parent --state working --agent-session-id session --agent-session-path /sessions/current.jsonl",
		"pane report-metadata pane-1 --source galpon-context --agent Parent --applies-to-source galpon --clear-state-labels",
		"pane report-agent pane-1 --source galpon --agent Parent --state idle --agent-session-id session --agent-session-path /sessions/current.jsonl",
	}
	if len(lines) != len(want) {
		t.Fatalf("Herdr calls = %q", lines)
	}
	for index := range want {
		if lines[index] != want[index] {
			t.Errorf("call %d = %q, want %q", index, lines[index], want[index])
		}
	}
	if strings.Contains(string(calls), "--state done") {
		t.Fatalf("Galpon reported done: %s", calls)
	}
}

func TestContextualMetadataFailureDoesNotSuppressAgentState(t *testing.T) {
	t.Setenv("HERDR_SESSION", "")
	root := t.TempDir()
	logPath := filepath.Join(root, "calls.log")
	bin := filepath.Join(root, "herdr")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
case "$*" in
  "pane report-metadata "*) exit 1 ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := Adapter{Bin: bin}
	agent := model.Agent{Title: "Parent", SessionID: "session", Renderer: adapter.Name(), RendererContext: adapter.Context(), RendererID: "pane-1"}
	if err := adapter.ReportContextualActivity(t.Context(), agent, 1); err == nil {
		t.Fatal("expected the metadata error")
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "pane report-agent pane-1 --source galpon --agent Parent --state working") {
		t.Fatalf("metadata failure suppressed lifecycle state: %s", calls)
	}
}

func TestCloseAgentClosesWorkspaceWhenHerdrRejectsItsLastTab(t *testing.T) {
	t.Setenv("HERDR_SESSION", "")
	root := t.TempDir()
	logPath := filepath.Join(root, "calls.log")
	bin := filepath.Join(root, "herdr")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
case "$*" in
  "pane get pane-1") printf '%s\n' '{"pane":{"tab_id":"tab-1","workspace_id":"workspace-1"}}' ;;
  "tab close tab-1") printf '%s\n' 'cannot close the last tab in a workspace' >&2; exit 1 ;;
  "workspace close workspace-1") printf '%s\n' '{}' ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := Adapter{Bin: bin}
	agent := model.Agent{Renderer: adapter.Name(), RendererContext: adapter.Context(), RendererID: "pane-1"}
	if err := adapter.CloseAgent(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pane get pane-1", "tab close tab-1", "workspace close workspace-1"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("Herdr calls omitted %q:\n%s", want, calls)
		}
	}
}

func TestPopupConfigOpensLargeGalponPopups(t *testing.T) {
	config := PopupConfig()
	for _, binding := range []string{
		"key = \"ctrl+k\"\ntype = \"popup\"\ncommand = \"'galpon'\"\n",
		"key = \"ctrl+alt+n\"\ntype = \"popup\"\ncommand = \"'galpon' 'herdr' 'new-agent'\"\n",
	} {
		if strings.Count(config, binding) != 1 {
			t.Errorf("config does not contain this binding once:\n%s\nconfig:\n%s", binding, config)
		}
	}
	tables := strings.Count(config, "[[keys.command]]")
	for _, field := range []string{"description = \"Galpon: ", `width = "88%"`, `height = "88%"`} {
		if strings.Count(config, field) != tables {
			t.Errorf("%d of %d bindings have %q:\n%s", strings.Count(config, field), tables, field, config)
		}
	}
}

// Herdr UI runs a global Herdr binding before an open popup gets the key. A
// global binding on a key that Galpon's screens read makes that key fail in
// Galpon's own popups.
func TestPopupConfigLeavesGalponScreenKeysToThePopups(t *testing.T) {
	config := PopupConfig()
	for _, key := range []string{"ctrl+n", "ctrl+p", "ctrl+s", "ctrl+f", "ctrl+g", "ctrl+h", "ctrl+r", "ctrl+space", "alt+s"} {
		if strings.Contains(config, `key = "`+key+`"`) {
			t.Errorf("config binds %s, which Galpon's screens read:\n%s", key, config)
		}
	}
}

func TestInstallPopupPreservesConfigUpgradesAndReplacesManagedBlocks(t *testing.T) {
	legacy := configMarker + `
[[keys.command]]
key = "ctrl+k"
type = "popup"
command = "/old/galpon"
width = "88%"
height = "88%"
`
	currentTables := `[[keys.command]]
key = "ctrl+k"
type = "popup"
command = "'galpon'"
width = "88%"
height = "88%"

[[keys.command]]
key = "ctrl+n"
type = "popup"
command = "'galpon' 'herdr' 'new-agent'"
width = "88%"
height = "88%"

[[keys.command]]
key = "ctrl+s"
type = "popup"
command = "'galpon' 'herdr' 'new-repository'"
width = "88%"
height = "88%"
`
	current := configMarker + "\n" + currentTables + configEndMarker + "\n"
	// A TOML settings writer put [ui] keys between the marker and the tables,
	// and copied the marker comment above each key.
	settingsInside := "[ui]\nwindow_title = \"{workspace}\"\n\n" + configMarker + "\nagent_panel_sort = \"spaces\"\n\n" + configMarker +
		"\nstatus_indicators = \"symbols\"\n" + currentTables + configEndMarker + "\n\n[ui.toast]\ndelivery = \"system\"\n"
	tests := []struct {
		name    string
		initial string
	}{
		{name: "existing config", initial: "theme = \"tokyo-night\"\n[terminal]\nscrollback_limit_bytes = 9000\n"},
		{name: "legacy block", initial: "theme = \"tokyo-night\"\n\n" + legacy + "[ui]\nshow_agent_labels_on_pane_borders = true\n"},
		{name: "current and duplicate blocks", initial: "theme = \"tokyo-night\"\n\n" + current + "\n" + current + "[ui]\nshow_agent_labels_on_pane_borders = true\n"},
		{name: "legacy and current blocks", initial: "theme = \"tokyo-night\"\n\n" + legacy + "[ui]\nshow_agent_labels_on_pane_borders = true\n\n" + current},
		{name: "settings inside the managed block", initial: "theme = \"tokyo-night\"\n\n" + settingsInside},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "herdr", "config.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.initial), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := InstallPopup(path); err != nil {
				t.Fatal(err)
			}
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := InstallPopup(path); err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(first) != string(second) {
				t.Fatal("the second install changed the current managed block")
			}
			text := string(second)
			for _, preserved := range []string{"theme = \"tokyo-night\""} {
				if !strings.Contains(text, preserved) {
					t.Fatalf("existing setting %q was lost:\n%s", preserved, text)
				}
			}
			if strings.Contains(test.initial, "[ui]") && !strings.Contains(text, "[ui]") {
				t.Fatalf("the setting after the old block was lost:\n%s", text)
			}
			if strings.Count(text, configMarker) != 1 || strings.Count(text, configEndMarker) != 1 {
				t.Fatalf("managed boundary counts are wrong:\n%s", text)
			}
			if !strings.Contains(text, managedPopupBlock()) {
				t.Fatalf("the config does not contain the current managed block:\n%s", text)
			}
			if got, want := strings.Count(text, "[[keys.command]]"), strings.Count(PopupConfig(), "[[keys.command]]"); got != want {
				t.Fatalf("the config has %d key commands, but Galpon installs %d; an earlier Galpon binding remained:\n%s", got, want, text)
			}
			if strings.Contains(test.initial, "status_indicators") {
				ui, toast := strings.Index(text, "[ui]\n"), strings.Index(text, "[ui.toast]")
				for _, setting := range []string{`agent_panel_sort = "spaces"`, `status_indicators = "symbols"`} {
					if at := strings.Index(text, setting); at < ui || at > toast {
						t.Fatalf("setting %q is not in the [ui] table:\n%s", setting, text)
					}
				}
			}
			if strings.Contains(text, "/opt/") || strings.Contains(text, "/mise/") {
				t.Fatalf("the managed block retained a version-specific Galpon path:\n%s", text)
			}
		})
	}
}

func TestParseIDHandlesJSONAndPlainOutput(t *testing.T) {
	id := "workspace-7"
	inputs := []struct{ raw, key string }{
		{`{"workspace_id":"` + id + `"}`, "workspace_id"},
		{`{"result":{"type":"workspace_created","workspace":{"workspace_id":"` + id + `"}}}`, "workspace_id"},
		{`{"result":{"type":"pane_list","panes":[{"pane_id":"pane-4"}]}}`, "pane_id"},
		{id, "workspace_id"},
	}
	for _, input := range inputs {
		want := id
		if input.key == "pane_id" {
			want = "pane-4"
		}
		if got := parseID(input.raw, input.key); got != want {
			t.Errorf("parseID(%q)=%q, want %q", input.raw, got, want)
		}
	}
}
