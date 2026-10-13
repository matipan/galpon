package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

func TestConfiguredHarnessIsSharedByCreationAndForms(t *testing.T) {
	for _, kind := range []string{"pi", "codex"} {
		t.Run(kind, func(t *testing.T) {
			a := communicationRuntimeTestApp(t)
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", root)
			t.Setenv("GALPON_STATE_DIR", a.Config.StateDir)
			if kind != "pi" {
				directory := filepath.Join(root, "galpon")
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte(`default_harness = "codex"`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			a.Config, err = config.Load()
			if err != nil {
				t.Fatal(err)
			}
			putCommunicationAgent(t, a, "caller")
			a.backgroundStart = func(context.Context, model.Agent) error { return nil }
			server := NewServer(a)
			for _, path := range []string{"/v1/dashboard", "/v1/dashboard?hidden=1", "/v1/companion/dashboard"} {
				response := httptest.NewRecorder()
				server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				var view model.Dashboard
				if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || response.Code != http.StatusOK || view.DefaultHarness != kind {
					t.Fatalf("form defaults for %s: %s, %v", path, response.Body.String(), err)
				}
			}
			for _, override := range []string{"", "pi"} {
				body := `{"title":"New agent","workspaceId":"workspace","harness":"` + override + `"}`
				response := httptest.NewRecorder()
				server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/agents", strings.NewReader(body)))
				var agent model.Agent
				want := kind
				if override != "" {
					want = override
				}
				if err := json.Unmarshal(response.Body.Bytes(), &agent); err != nil || response.Code != http.StatusOK || agent.Kind != want {
					t.Fatalf("creation used a different default: %s, %v", response.Body.String(), err)
				}
				result, err := a.handleAgentTool(t.Context(), "caller", "create_agent", map[string]any{"title": "Delegated worker", "harness": override})
				if err != nil {
					t.Fatal(err)
				}
				if child := result.(CreateAgentToolResult).Agent; child.Kind != want {
					t.Fatalf("tool creation: %#v", child)
				}
			}
			a.Config.DefaultHarness = "claude"
			caller, err := a.Store.Agent(t.Context(), "caller")
			if err != nil || caller.Kind != "pi" {
				t.Fatalf("default changed an existing agent: %#v, %v", caller, err)
			}
		})
	}
}
