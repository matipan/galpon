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

func TestPopupConfigHasThreeDirectLargePopups(t *testing.T) {
	config := PopupConfig("/tmp/Galpon Tools/galpon")
	for _, key := range []string{"ctrl+k", "ctrl+n", "ctrl+o"} {
		if strings.Count(config, `key = "`+key+`"`) != 1 {
			t.Errorf("binding %q count is not one:\n%s", key, config)
		}
	}
	for _, want := range []string{`type = "popup"`, `command = "'/tmp/Galpon Tools/galpon'"`, `command = "'/tmp/Galpon Tools/galpon' 'herdr' 'new-agent'"`, `command = "'/tmp/Galpon Tools/galpon' 'herdr' 'operations'"`} {
		if !strings.Contains(config, want) {
			t.Errorf("config omitted %q:\n%s", want, config)
		}
	}
	if strings.Count(config, `width = "88%"`) != 3 || strings.Count(config, `height = "88%"`) != 3 {
		t.Fatalf("popup sizes are not 88%% for all bindings:\n%s", config)
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
	current := configMarker + `
[[keys.command]]
key = "ctrl+k"
type = "popup"
command = "old"
width = "88%"
height = "88%"
` + configEndMarker + "\n"
	tests := []struct {
		name    string
		initial string
	}{
		{name: "existing config", initial: "theme = \"tokyo-night\"\n[terminal]\nscrollback_limit_bytes = 9000\n"},
		{name: "legacy block", initial: "theme = \"tokyo-night\"\n\n" + legacy + "[ui]\nshow_agent_labels_on_pane_borders = true\n"},
		{name: "current and duplicate blocks", initial: "theme = \"tokyo-night\"\n\n" + current + "\n" + current + "[ui]\nshow_agent_labels_on_pane_borders = true\n"},
		{name: "legacy and current blocks", initial: "theme = \"tokyo-night\"\n\n" + legacy + "[ui]\nshow_agent_labels_on_pane_borders = true\n\n" + current},
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
			binary := "/opt/Galpon Tools/galpon"
			if err := InstallPopup(path, binary); err != nil {
				t.Fatal(err)
			}
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := InstallPopup(path, binary); err != nil {
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
			for _, key := range []string{"ctrl+k", "ctrl+n", "ctrl+o"} {
				if strings.Count(text, `key = "`+key+`"`) != 1 {
					t.Fatalf("binding %q count is not one:\n%s", key, text)
				}
			}
			if !strings.Contains(text, `command = "'/opt/Galpon Tools/galpon' 'herdr' 'new-agent'"`) {
				t.Fatalf("the binary path was not quoted safely:\n%s", text)
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
