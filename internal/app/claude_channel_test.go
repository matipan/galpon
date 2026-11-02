package app

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

// screenRenderer is a renderer whose agent terminal shows a fixed screen.
type screenRenderer struct {
	cleanupRenderer
	screen  string
	reads   int
	pressed []string
}

func (r *screenRenderer) ReadAgentScreen(context.Context, model.Agent) (string, error) {
	r.reads++
	return r.screen, nil
}

func (r *screenRenderer) PressAgentKey(_ context.Context, _ model.Agent, key string) error {
	r.pressed = append(r.pressed, key)
	return nil
}

// claudeWarningScreen is the screen of Claude Code 2.1.314 when it starts
// with --dangerously-load-development-channels server:galpon.
const claudeWarningScreen = `────────────────────────────────────────────────────────────────────────
  WARNING: Loading development channels

  --dangerously-load-development-channels is for local channel development only. Do not use this option to run
  channels you have downloaded off the internet.

  Please use --channels to run a list of approved channels.

  Channels: server:galpon

  ❯ 1. I am using this for local development
    2. Exit

  Enter to confirm · Esc to cancel
`

func TestGalponAcceptsOnlyTheClaudeWarningForItsOwnChannel(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	renderer := &screenRenderer{cleanupRenderer: cleanupRenderer{name: "test", context: "test"}}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Claude work"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Claude", Harness: "claude", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Store.RegisterAgentRuntime(ctx, agent.ID, "terminal-runtime", agent.SessionID, ""); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, screen string
		accept       bool
	}{
		{"the warning for the Galpon channel", claudeWarningScreen, true},
		{"Exit selected", strings.NewReplacer("❯ 1.", "  1.", "  2. Exit", "❯ 2. Exit").Replace(claudeWarningScreen), false},
		{"another channel", strings.Replace(claudeWarningScreen, "server:galpon", "server:galpon, server:other", 1), false},
		{"folder trust", "  Do you trust the files in this folder?\n\n  ❯ 1. Yes, proceed\n    2. No, exit\n", false},
		{"open Claude Code input", "▎ Channels (experimental) messages from server:galpon inject directly in this session\n────\n❯ \n────\n", false},
	} {
		renderer.screen, renderer.pressed = test.screen, nil
		accepted, err := application.AcceptClaudeChannelWarning(ctx, agent.ID, "terminal-runtime")
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		want := []string(nil)
		if test.accept {
			want = []string{"enter"}
		}
		if accepted != test.accept || strings.Join(renderer.pressed, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: accepted %t, pressed %q", test.name, accepted, renderer.pressed)
		}
	}

	// A runtime that is no longer the agent runtime cannot press keys.
	renderer.screen, renderer.reads, renderer.pressed = claudeWarningScreen, 0, nil
	if _, err := application.AcceptClaudeChannelWarning(ctx, agent.ID, "old-runtime"); err == nil || renderer.reads != 0 || len(renderer.pressed) != 0 {
		t.Fatalf("old runtime: error %v, screen reads %d, pressed %q", err, renderer.reads, renderer.pressed)
	}
}
