package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/piagent"
	"github.com/matipan/galpon/internal/sessionfile"
)

func TestCrossHarnessCreationSavesAnIndependentSnapshot(t *testing.T) {
	a := communicationRuntimeTestApp(t)
	ctx := t.Context()
	if err := a.Store.PutWorkspace(ctx, model.Workspace{ID: "work", Title: "Work", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	source, err := a.CreateAgent(ctx, CreateAgentRequest{Title: "Source", WorkspaceID: "work", Harness: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source.Placement.CWD, "source.jsonl")
	data := `{"type":"session","version":3,"id":"source"}
{"type":"message","id":"u","parentId":null,"message":{"role":"user","content":"original question"}}
{"type":"message","id":"a","parentId":"u","message":{"role":"assistant","content":[{"type":"text","text":"original answer"}]}}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.RegisterAgentRuntime(ctx, source.ID, "source-runtime", source.SessionID, path); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.StopAgentRuntime(ctx, source.ID, "source-runtime", ""); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"claude", "codex"} {
		fork, err := a.CreateAgent(ctx, CreateAgentRequest{Title: target + " fork", WorkspaceID: "work", Harness: target, ContextAgentID: source.ID})
		if err != nil {
			t.Fatal(err)
		}
		seed, err := piagent.ForkSource(a.Config.StateDir, fork, func(string) model.Agent {
			t.Fatal("converted snapshot must not depend on the live source")
			return model.Agent{}
		})
		if err != nil {
			t.Fatal(err)
		}
		saved, err := os.ReadFile(seed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sessionfile.NativeID(target, bytes.NewReader(saved)); err != nil {
			t.Fatalf("target has no native seed: %v", err)
		}
		if fork.ContextAgentID != source.ID || fork.Harness() != target || fork.SessionPath != "" || !bytes.Contains(saved, []byte("original question")) || !bytes.Contains(saved, []byte("original answer")) {
			t.Fatalf("fork did not retain its context: %#v", fork)
		}
	}
	// An unavailable source does not invalidate an already-created snapshot.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	dashboard, err := a.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range dashboard.Agents {
		if fork.ContextAgentID == source.ID {
			seed, err := piagent.ForkSource(a.Config.StateDir, fork, func(string) model.Agent { return source })
			if err != nil || seed == path || seed == "" {
				t.Fatalf("snapshot depends on its source: %q, %v", seed, err)
			}
		}
	}
	before := len(dashboard.Agents)
	_, err = a.CreateAgent(ctx, CreateAgentRequest{Title: "Missing source", WorkspaceID: "work", Harness: "codex", ContextAgentID: source.ID})
	if err == nil || !strings.Contains(err.Error(), "cross-harness fork") {
		t.Fatalf("missing snapshot source error = %v", err)
	}
	dashboard, err = a.Store.Dashboard(ctx)
	if err != nil || len(dashboard.Agents) != before {
		t.Fatal("failed conversion left an agent behind")
	}
}
