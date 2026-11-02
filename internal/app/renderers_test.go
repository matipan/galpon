package app

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/terminal"
)

// appRenderer is a renderer without workspace views, as the Herdr app is.
type appRenderer struct{ cleanupRenderer }

func (r *appRenderer) OpenAgent(_ context.Context, _ model.Workspace, _ model.Worktree, agent model.Agent, _ []string, _ bool) (string, string, bool, error) {
	r.opened = append(r.opened, agent.ID)
	return "", "t1", true, nil
}

// tabRenderer ends the agent process when it closes the agent tab.
type tabRenderer struct {
	cleanupRenderer
	ended func()
}

func (r *tabRenderer) CloseAgent(ctx context.Context, agent model.Agent) error {
	r.ended()
	return r.cleanupRenderer.CloseAgent(ctx, agent)
}

func TestAgentsOpenWhereTheCallerIsAndMoveOnlyWhenIdleAndConfirmed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	running := false
	herdrView := &tabRenderer{cleanupRenderer: cleanupRenderer{name: "herdr", context: "default"}, ended: func() { running = false }}
	appView := &appRenderer{cleanupRenderer: cleanupRenderer{name: "herdr-app", context: "default"}}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), herdrView)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)
	application.Renderers = append(application.Renderers, appView)
	application.runtimeRunning = func(string) (bool, error) { return running, nil }
	herdrClose := func() int { return len(herdrView.closed) }
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Builder", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}

	// An agent that was never open uses the default renderer.
	if opened, err := application.OpenAgent(ctx, agent.ID, false); err != nil || opened.Renderer != "herdr" {
		t.Fatalf("first open = %q, %v", opened.Renderer, err)
	}
	if err := application.Store.RegisterAgentRuntime(ctx, agent.ID, "herdr-runtime", agent.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	running = true

	// A busy agent stays in Herdr, also when the caller confirms the move.
	if err := application.Store.SetAgentRuntimeStatus(ctx, agent.ID, "herdr-runtime", "running", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := application.OpenAgentWith(ctx, agent.ID, OpenAgentOptions{Focus: true, Renderer: "herdr-app", Move: true}); err == nil || !strings.Contains(err.Error(), "is working in Herdr") {
		t.Fatalf("move of a busy agent = %v", err)
	}

	// An idle agent moves only after the caller confirms.
	if err := application.Store.SetAgentRuntimeStatus(ctx, agent.ID, "herdr-runtime", "idle", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := application.OpenAgentWith(ctx, agent.ID, OpenAgentOptions{Focus: true, Renderer: "herdr-app"}); !errors.Is(err, ErrAgentOpenElsewhere) {
		t.Fatalf("open of an idle agent in another app = %v", err)
	}
	if herdrClose() != 0 || len(appView.opened) != 0 {
		t.Fatalf("Galpon changed views before the move: herdr closed %v, app opened %v", herdrView.closed, appView.opened)
	}
	moved, err := application.OpenAgentWith(ctx, agent.ID, OpenAgentOptions{Focus: true, Renderer: "herdr-app", Move: true})
	if err != nil {
		t.Fatal(err)
	}
	if herdrClose() != 1 || len(appView.opened) != 1 || moved.Renderer != "herdr-app" || moved.RendererID != "t1" || moved.RuntimeID != "" {
		t.Fatalf("moved agent = %#v; herdr closed %v, app opened %v", moved, herdrView.closed, appView.opened)
	}

	// Queued work opens the agent where it was open last.
	if opened, err := application.OpenAgent(ctx, agent.ID, false); err != nil || opened.Renderer != "herdr-app" || len(herdrView.opened) != 1 {
		t.Fatalf("wake opened %q, %v; herdr opened %v", opened.Renderer, err, herdrView.opened)
	}
}

func TestAgentDoesNotMoveDuringATurnThatADaemonRestartRecovered(t *testing.T) {
	application := communicationRuntimeTestApp(t)
	if _, err := application.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{Generation: 2, IdleTimeout: time.Second, BarrierTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	herdrView := &cleanupRenderer{name: "herdr", context: "default"}
	appView := &appRenderer{cleanupRenderer: cleanupRenderer{name: "herdr-app", context: "default"}}
	application.Renderer, application.Renderers = herdrView, []terminal.Renderer{appView}
	application.runtimeRunning = func(string) (bool, error) { return true, nil }
	putCommunicationAgent(t, application, "worker")
	registerCommunicationRuntime(t, application, "worker", "worker-runtime")
	if err := application.Store.SetAgentRenderer(t.Context(), "worker", "herdr", "default", "w1:p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.RegisterDirectOperation(t.Context(), "worker", DirectOperationRequest{RuntimeID: "worker-runtime", UserEntryID: "entry", ProtocolGeneration: 2}); err != nil {
		t.Fatal(err)
	}

	// The daemon restarts during the turn. The runtime registers again, and
	// Galpon shows the agent idle while Pi still works on the turn.
	if err := application.Store.RecoverAgentCoordinationState(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := application.Store.SetAgentRuntimeStatus(t.Context(), "worker", "worker-runtime", "idle", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := application.OpenAgentWith(t.Context(), "worker", OpenAgentOptions{Focus: true, Renderer: "herdr-app", Move: true}); err == nil || !strings.Contains(err.Error(), "is working in Herdr") {
		t.Fatalf("move during the turn = %v", err)
	}
	if len(herdrView.closed) != 0 || len(appView.opened) != 0 {
		t.Fatalf("Galpon moved the agent during its turn: herdr closed %v, app opened %v", herdrView.closed, appView.opened)
	}
}
