package herdr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/model"
)

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

func TestPopupConfigIsDirectLargeCtrlKPopup(t *testing.T) {
	config := PopupConfig("/tmp/galpon")
	for _, want := range []string{`key = "ctrl+k"`, `type = "popup"`, `width = "88%"`, `height = "88%"`, `command = "/tmp/galpon"`} {
		if !strings.Contains(config, want) {
			t.Errorf("config omitted %q:\n%s", want, config)
		}
	}
}

func TestInstallPopupPreservesConfigAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "herdr", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("theme = \"tokyo-night\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallPopup(path, "galpon"); err != nil {
		t.Fatal(err)
	}
	if err := InstallPopup(path, "galpon"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "theme = \"tokyo-night\"") {
		t.Fatal("existing Herdr config was lost")
	}
	if strings.Count(text, configMarker) != 1 {
		t.Fatalf("marker count = %d", strings.Count(text, configMarker))
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
