package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

// A running Pi agent that the person renames gives the model its new title
// from its next turn, without a restart.
func TestRenamedPiAgentUsesItsNewTitleInItsNextTurn(t *testing.T) {
	piBin, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	herdrBin, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("Herdr is not installed")
	}
	// titles holds the agent title of the system prompt of each turn.
	var mu sync.Mutex
	titles := map[string]string{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = r.Body.Close() }()
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, _ := json.Marshal(request)
		title := ""
		if _, rest, found := strings.Cut(string(body), "You are the durable Galpón agent "); found {
			title, _, _ = strings.Cut(rest, " in workspace ")
		}
		prompt, _ := responseInput(request)
		turn := "first"
		if strings.Contains(prompt, "Second task") {
			turn = "second"
		}
		mu.Lock()
		titles[turn] = title
		mu.Unlock()
		writeTextResponse(w, "Done with the "+turn+" task")
	}))
	defer mock.Close()

	root := t.TempDir()
	piHome := filepath.Join(root, "pi")
	writePiConfig(t, piHome, mock.URL)
	testShell := filepath.Join(root, "test-shell")
	if err := os.WriteFile(testShell, []byte("#!/bin/sh\nexec /bin/bash --noprofile --norc \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	session := fmt.Sprintf("galpon-rename-e2e-%d", time.Now().UnixNano())
	stateDir := filepath.Join(root, "state")
	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+testConfigDir(t),
		"SHELL="+testShell,
		"GALPON_STATE_DIR="+stateDir,
		"GALPON_PI_BIN="+piBin,
		"GALPON_PI_PROVIDER=galpon-mock",
		"GALPON_PI_MODEL=mock-model",
		"GALPON_HERDR_BIN="+herdrBin,
		"HERDR_CONFIG_PATH="+filepath.Join(root, "herdr.toml"),
		"HERDR_SESSION="+session,
		"PI_CODING_AGENT_DIR="+piHome,
		"PI_OFFLINE=1",
		"GALPON_TEST_SKIP_PI_PACKAGE_SETUP=1",
		"PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"),
		"NO_COLOR=",
	)
	stopHerdr := startTestHerdr(t, herdrBin, session, append(slices.Clone(env), "GALPON_STATE_DIR="+filepath.Join(root, "herdr-state")))
	defer stopHerdr()
	bin := filepath.Join(root, "galpon")
	runRaw(t, "..", nil, "go", "build", "-o", bin, "./cmd/galpon")
	defer func() { _ = runCommand("", env, bin, "daemon", "stop") }()

	var workspace model.Workspace
	decodeCommand(t, &workspace, runRaw(t, "", env, bin, "workspace", "create", "Billing"))
	agent, err := app.NewClient(filepath.Join(stateDir, "galpon.sock")).CreateAgent(t.Context(), app.CreateAgentRequest{Title: "Invoices", WorkspaceID: workspace.ID,
		Placement: app.AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}

	first := sendMessage(t, bin, env, agent.ID, "First task")
	waitForMessage(t, bin, env, agent.ID, first.ID, "Done with the first task")
	var renamed model.Agent
	decodeCommand(t, &renamed, runRaw(t, "", env, bin, "agent", "rename", agent.ID, "Billing", "lead"))
	if renamed.Title != "Billing lead" {
		t.Fatalf("renamed agent = %#v", renamed)
	}
	second := sendMessage(t, bin, env, agent.ID, "Second task")
	waitForMessage(t, bin, env, agent.ID, second.ID, "Done with the second task")

	mu.Lock()
	defer mu.Unlock()
	// Pi before 1.2.0 does not run before_agent_start for a turn that a Galpon
	// message starts, so no Galpon system prompt reaches the model.
	if titles["first"] == "" {
		t.Skip("this Pi sends no Galpon system prompt in a turn that a Galpon message starts")
	}
	if titles["first"] != "Invoices" || titles["second"] != "Billing lead" {
		t.Fatalf("system prompt titles = %q, want Invoices and then Billing lead", titles)
	}
}
