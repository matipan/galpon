package e2e

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/sessionfile"
)

func TestCompactedForksKeepArchiveAndEffectiveContext(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real harness test")
	}
	binaries := nativeBinaries(t)
	endpoint := httptest.NewServer(&nativeModel{})
	t.Cleanup(endpoint.Close)
	instance := newNativeInstance(t, binaries, endpoint.URL, "", false, useDefaultClaudeHome)
	sources := make(map[string]model.Agent)
	for _, kind := range []string{"pi", "claude", "codex"} {
		source, err := instance.client.CreateAgent(t.Context(), app.CreateAgentRequest{Title: "Saved " + kind, Harness: kind, WorkspaceID: instance.workspace.ID})
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join("testdata", "history", kind+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		id := source.SessionID
		if kind == "pi" {
			parts := strings.SplitN(string(data), "\n", 2)
			var header map[string]any
			if err := json.Unmarshal([]byte(parts[0]), &header); err != nil {
				t.Fatal(err)
			}
			header["id"] = id
			first, err := json.Marshal(header)
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(string(first) + "\n" + parts[1])
		} else {
			id, err = sessionfile.NativeID(kind, strings.NewReader(string(data)))
			if err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(instance.root, "saved-"+kind+".jsonl")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		runtime := "saved-source-" + kind
		if err := instance.client.PrepareRuntime(t.Context(), source.ID, runtime); err != nil {
			t.Fatal(err)
		}
		if _, err := instance.client.RegisterRuntime(t.Context(), source.ID, runtime, id, path, 3); err != nil {
			t.Fatal(err)
		}
		sources[kind] = source
	}
	for _, sourceKind := range []string{"pi", "claude", "codex"} {
		for _, targetKind := range []string{"pi", "claude", "codex"} {
			if sourceKind == targetKind {
				continue
			}
			t.Run(sourceKind+"_to_"+targetKind, func(t *testing.T) {
				key := sourceKind + "_to_" + targetKind
				created := instance.create(t, map[string]any{"title": key, "harness": targetKind, "context_agent": sources[sourceKind].ID, "prompt": "NATIVE_RICH " + key})
				waitNativeMessage(t, instance.client, created.ID, created.InitialMessage.ID, "rich:"+key)
				agent := instance.idle(t, created.ID)
				checkSavedArchive := func() {
					t.Helper()
					data, err := os.ReadFile(agent.SessionPath)
					if err != nil || !strings.Contains(string(data), "ARCHIVED_REPLY") || !strings.Contains(string(data), "IMAGE_INPUT_SAVED") || !strings.Contains(string(data), "IMAGE_TOOL_SAVED") {
						t.Fatalf("saved native archive is incomplete: %v", err)
					}
				}
				checkSavedArchive()
				if targetKind != "pi" {
					instance.killController(t, created.ID)
					message := instance.send(t, created.ID, "NATIVE_RICH "+key)
					waitNativeMessage(t, instance.client, created.ID, message.ID, "rich:"+key)
					agent = instance.idle(t, created.ID)
					checkSavedArchive()
				}
			})
		}
	}
}
