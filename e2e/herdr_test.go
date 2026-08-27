package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/herdr"
	"github.com/matipan/galpon/internal/model"
)

func TestRealHerdrWorkspaceAndTerminalAdapter(t *testing.T) {
	herdrBin, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("Herdr is not installed")
	}
	session := fmt.Sprintf("galpon-e2e-%d", time.Now().UnixNano())
	configPath := filepath.Join(t.TempDir(), "config.toml")
	env := append(os.Environ(), "HERDR_CONFIG_PATH="+configPath, "HERDR_SESSION="+session)
	if err := herdr.InstallPopup(configPath, "galpon"); err != nil {
		t.Fatal(err)
	}
	herdrCommand(t, herdrBin, env, "config", "check")

	server := exec.Command(herdrBin, "--session", session, "server")
	server.Env = env
	server.Stdout = io.Discard
	server.Stderr = io.Discard
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop := exec.Command(herdrBin, "--session", session, "server", "stop")
		stop.Env = env
		_ = stop.Run()
		_, _ = server.Process.Wait()
		remove := exec.Command(herdrBin, "session", "delete", session)
		remove.Env = env
		_ = remove.Run()
	}()

	waitForHerdr(t, herdrBin, session, env)
	t.Setenv("HERDR_CONFIG_PATH", configPath)
	t.Setenv("HERDR_SESSION", session)

	worktree := t.TempDir()
	adapter := herdr.Adapter{Bin: herdrBin}
	ws := model.Workspace{ID: "workspace", Title: "Galpon adapter E2E"}
	wt := model.Worktree{ID: "worktree", WorkspaceID: ws.ID, Path: worktree}
	workspaceID, err := adapter.OpenTerminal(context.Background(), ws, wt, "Adapter terminal", nil)
	if err != nil {
		t.Fatal(err)
	}
	if workspaceID == "" {
		t.Fatal("adapter returned no workspace ID")
	}
	ws.Renderer = adapter.Name()
	ws.RendererContext = adapter.Context()
	ws.RendererID = workspaceID
	if _, err := adapter.OpenTerminal(context.Background(), ws, wt, "Second terminal", nil); err != nil {
		t.Fatal(err)
	}

	output := herdrCommand(t, herdrBin, env, "--session", session, "pane", "list", "--workspace", workspaceID)
	var envelope struct {
		Result struct {
			Panes []json.RawMessage `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode pane list: %v\n%s", err, output)
	}
	if len(envelope.Result.Panes) != 2 {
		t.Fatalf("pane count = %d, want 2: %s", len(envelope.Result.Panes), output)
	}
	popupOutput := herdrCommand(t, herdrBin, env, "--session", session, "workspace", "create", "--cwd", worktree, "--label", "Galpon popup", "--focus")
	var popup struct {
		Result struct {
			RootPane struct {
				PaneID      string `json:"pane_id"`
				WorkspaceID string `json:"workspace_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(popupOutput, &popup); err != nil {
		t.Fatal(err)
	}
	if popup.Result.RootPane.PaneID == "" {
		t.Fatalf("popup workspace = %s", popupOutput)
	}
	t.Setenv("HERDR_PANE_ID", popup.Result.RootPane.PaneID)
	t.Setenv("HERDR_WORKSPACE_ID", popup.Result.RootPane.WorkspaceID)
	env = append(env, "HERDR_PANE_ID="+popup.Result.RootPane.PaneID, "HERDR_WORKSPACE_ID="+popup.Result.RootPane.WorkspaceID)

	agentA := model.Agent{ID: "agent-a", WorkspaceID: ws.ID, Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: wt.ID}, Title: "Agent A", Kind: "pi", Status: "stopped", SessionID: "11111111-1111-4111-8111-111111111111"}
	_, paneA, _, err := adapter.OpenAgent(context.Background(), ws, wt, agentA, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	tabA := assertHerdrPaneName(t, herdrBin, env, paneA, agentA.Title)
	agentA.Renderer = adapter.Name()
	agentA.RendererContext = adapter.Context()
	agentA.RendererID = paneA
	agentB := model.Agent{ID: "agent-b", WorkspaceID: ws.ID, Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: wt.ID}, Title: "Agent B", Kind: "pi", Status: "stopped", SessionID: "22222222-2222-4222-8222-222222222222"}
	_, paneB, _, err := adapter.OpenAgent(context.Background(), ws, wt, agentB, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	tabB := assertHerdrPaneName(t, herdrBin, env, paneB, agentB.Title)
	if tabA == tabB {
		t.Fatalf("agents share tab %s; want one named tab per agent", tabA)
	}
	current := herdrCommand(t, herdrBin, env, "--session", session, "pane", "get", paneB)
	if !bytes.Contains(current, []byte(`"focused":true`)) {
		t.Fatalf("pane %s was not focused: %s", paneB, current)
	}
	if _, _, _, err := adapter.OpenAgent(context.Background(), ws, wt, agentA, nil, true); err != nil {
		t.Fatal(err)
	}
	current = herdrCommand(t, herdrBin, env, "--session", session, "pane", "get", paneA)
	if !bytes.Contains(current, []byte(`"focused":true`)) {
		t.Fatalf("pane %s was not focused: %s", paneA, current)
	}
	if err := adapter.ReportAgent(context.Background(), agentA, "running", "working"); err != nil {
		t.Fatal(err)
	}
	if status := herdrAgentStatus(t, herdrBin, env, paneA); status != "working" {
		t.Fatalf("normal Pi lifecycle status = %q, want working", status)
	}
	// Contextual work starts and settles while this pane is unseen. Galpon
	// reports only working and idle. Herdr must derive done, then clear it when
	// the user sees the tab again.
	herdrCommand(t, herdrBin, env, "tab", "focus", tabB)
	if err := adapter.ReportContextualActivity(context.Background(), agentA, 2); err != nil {
		t.Fatal(err)
	}
	if status := herdrAgentStatus(t, herdrBin, env, paneA); status != "working" {
		t.Fatalf("contextual lifecycle status = %q, want working", status)
	}
	if err := adapter.ReportContextualActivity(context.Background(), agentA, 0); err != nil {
		t.Fatal(err)
	}
	if status := herdrAgentStatus(t, herdrBin, env, paneA); status != "done" {
		t.Fatalf("unseen contextual completion = %q, want Herdr-derived done", status)
	}
	herdrCommand(t, herdrBin, env, "tab", "focus", tabA)
	if status := herdrAgentStatus(t, herdrBin, env, paneA); status != "idle" {
		t.Fatalf("seen contextual completion = %q, want idle", status)
	}
	if err := adapter.CloseAgent(context.Background(), agentA); err != nil {
		t.Fatal(err)
	}
	closed := exec.Command(herdrBin, "--session", session, "pane", "get", paneA)
	closed.Env = env
	if err := closed.Run(); err == nil {
		t.Fatalf("closed agent pane %s still exists", paneA)
	}
}

func herdrAgentStatus(t *testing.T, bin string, env []string, paneID string) string {
	t.Helper()
	output := herdrCommand(t, bin, env, "agent", "get", paneID)
	var envelope struct {
		Result struct {
			Agent struct {
				Status string `json:"agent_status"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode agent status: %v\n%s", err, output)
	}
	return envelope.Result.Agent.Status
}

func assertHerdrPaneName(t *testing.T, bin string, env []string, paneID, want string) string {
	t.Helper()
	output := herdrCommand(t, bin, env, "pane", "get", paneID)
	var envelope struct {
		Result struct {
			Pane struct {
				Label        string `json:"label"`
				Title        string `json:"title"`
				DisplayAgent string `json:"display_agent"`
				TabID        string `json:"tab_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode pane: %v\n%s", err, output)
	}
	if envelope.Result.Pane.Label != want || envelope.Result.Pane.Title != want || envelope.Result.Pane.DisplayAgent != want {
		t.Fatalf("pane names = label %q, title %q, display agent %q; want %q", envelope.Result.Pane.Label, envelope.Result.Pane.Title, envelope.Result.Pane.DisplayAgent, want)
	}
	tabOutput := herdrCommand(t, bin, env, "tab", "get", envelope.Result.Pane.TabID)
	var tabEnvelope struct {
		Result struct {
			Tab struct {
				Label string `json:"label"`
			} `json:"tab"`
		} `json:"result"`
	}
	if err := json.Unmarshal(tabOutput, &tabEnvelope); err != nil {
		t.Fatalf("decode tab: %v\n%s", err, tabOutput)
	}
	if tabEnvelope.Result.Tab.Label != want {
		t.Fatalf("tab label = %q, want %q", tabEnvelope.Result.Tab.Label, want)
	}
	return envelope.Result.Pane.TabID
}

func waitForHerdr(t *testing.T, bin, session string, env []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cmd := exec.Command(bin, "--session", session, "status", "server", "--json")
		cmd.Env = env
		if err := cmd.Run(); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Herdr server did not start")
}

func herdrCommand(t *testing.T, bin string, env []string, args ...string) []byte {
	t.Helper()
	if len(args) > 0 && args[0] != "--session" && args[0] != "config" {
		for _, value := range env {
			if session, found := strings.CutPrefix(value, "HERDR_SESSION="); found && session != "" {
				args = append([]string{"--session", session}, args...)
				break
			}
		}
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("herdr %v: %v: %s", args, err, stderr.String())
	}
	return stdout.Bytes()
}
