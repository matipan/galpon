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
	"sync/atomic"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

// A real Pi agent uses the Galpon tools to put a feature worktree in its own
// folder and to remove it again.
func TestPiAgentAddsAndRemovesAWorktreeInItsFolder(t *testing.T) {
	piBin, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	herdrBin, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("Herdr is not installed")
	}
	var prompted, instructed atomic.Bool
	var addedPath atomic.Value
	addedPath.Store("")
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
		if body, _ := json.Marshal(request); strings.Contains(string(body), "You are the durable Galpón agent") {
			prompted.Store(true)
			instructed.Store(instructed.Load() || strings.Contains(string(body), "Your Galpón folder is"))
		}
		prompt, outputs := responseInput(request)
		if !strings.Contains(prompt, "Start the invoice feature") {
			http.Error(w, "unexpected prompt: "+prompt, http.StatusBadRequest)
			return
		}
		switch len(outputs) {
		case 0:
			writeToolResponseID(w, "galpon_add_worktree", "add", map[string]any{"branch": "feature/invoices"})
		case 1:
			var added app.AddAgentWorktreeResult
			if err := json.Unmarshal([]byte(outputs[0]), &added); err != nil || added.Worktree.Path == "" {
				http.Error(w, "galpon_add_worktree result: "+outputs[0], http.StatusBadRequest)
				return
			}
			addedPath.Store(added.Worktree.Path)
			writeToolResponseID(w, "galpon_remove_worktree", "remove", map[string]any{"worktree": added.Worktree.Path})
		default:
			var removed app.RemoveAgentWorktreeResult
			if err := json.Unmarshal([]byte(outputs[1]), &removed); err != nil || !slices.Equal(removed.DeletedBranches, []string{"feature/invoices"}) {
				http.Error(w, "galpon_remove_worktree result: "+outputs[1], http.StatusBadRequest)
				return
			}
			writeTextResponse(w, "Invoice worktree removed")
		}
	}))
	defer mock.Close()

	root := t.TempDir()
	piHome := filepath.Join(root, "pi")
	writePiConfig(t, piHome, mock.URL)
	testShell := filepath.Join(root, "test-shell")
	if err := os.WriteFile(testShell, []byte("#!/bin/sh\nexec /bin/bash --noprofile --norc \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	session := fmt.Sprintf("galpon-worktree-e2e-%d", time.Now().UnixNano())
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

	var repository model.Repository
	decodeCommand(t, &repository, runRaw(t, "", env, bin, "repo", "add", createRepository(t, root)))
	var workspace model.Workspace
	decodeCommand(t, &workspace, runRaw(t, "", env, bin, "workspace", "create", "Billing"))
	agent, err := app.NewClient(filepath.Join(stateDir, "galpon.sock")).CreateAgent(t.Context(), app.CreateAgentRequest{Title: "Invoices", WorkspaceID: workspace.ID,
		Placement: app.AgentPlacementRequest{Type: "worktrees", Worktrees: []app.AgentPlacementWorktreeRequest{{RepositoryID: repository.ID}}}})
	if err != nil {
		t.Fatal(err)
	}

	message := sendMessage(t, bin, env, agent.ID, "Start the invoice feature")
	view := waitForMessage(t, bin, env, agent.ID, message.ID, "Invoice worktree removed")
	// Pi 1.0.4 sends no extension system prompt in this flow, so this check
	// applies only when the Galpon prompt reaches the model.
	if prompted.Load() && !instructed.Load() {
		t.Fatal("the Galpon system prompt did not contain the folder instructions")
	}
	if len(view.Worktrees) != 1 {
		t.Fatalf("placement after removal = %#v", view.Worktrees)
	}
	folder := filepath.Dir(view.Worktrees[0].Path)
	path := addedPath.Load().(string)
	if filepath.Dir(path) != folder {
		t.Fatalf("added worktree %s is outside the agent folder %s", path, folder)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removed worktree still exists: %v", err)
	}
	if info, err := os.Stat(filepath.Join(folder, "scratch")); err != nil || !info.IsDir() {
		t.Fatalf("agent scratch folder: %v", err)
	}
	if err := runCommand("", nil, "git", "--git-dir", repository.MirrorPath, "show-ref", "--verify", "--quiet", "refs/heads/feature/invoices"); err == nil {
		t.Fatal("the removed worktree branch still exists")
	}
}
