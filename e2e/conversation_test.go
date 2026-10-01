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
	"testing"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

// A conversation moves from one Galpon instance to another, and the imported
// agent continues it with the same history.
func TestConversationExportImportAcrossInstances(t *testing.T) {
	piBin, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	herdrBin, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("Herdr is not installed")
	}
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
		prompt, _ := responseInput(request)
		switch {
		case strings.Contains(prompt, "What was the code word"):
			if !strings.Contains(prompt, "Remember the code word heron") {
				http.Error(w, "the imported history did not reach the model", http.StatusBadRequest)
				return
			}
			writeTextResponse(w, "The code word was heron")
		case strings.Contains(prompt, "Remember the code word heron"):
			writeTextResponse(w, "Noted heron")
		default:
			http.Error(w, "unexpected prompt: "+prompt, http.StatusBadRequest)
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
	session := fmt.Sprintf("galpon-transfer-e2e-%d", time.Now().UnixNano())
	base := append(os.Environ(),
		"SHELL="+testShell,
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
	sourceState, targetState := filepath.Join(root, "source-state"), filepath.Join(root, "target-state")
	sourceEnv := append(slices.Clone(base), "GALPON_STATE_DIR="+sourceState)
	targetEnv := append(slices.Clone(base), "GALPON_STATE_DIR="+targetState)
	// Panes get GALPON_STATE_DIR from the daemon. The Herdr server itself has
	// an unused state directory, so no pane can use the real user state.
	stopHerdr := startTestHerdr(t, herdrBin, session, append(slices.Clone(base), "GALPON_STATE_DIR="+filepath.Join(root, "herdr-state")))
	defer stopHerdr()
	bin := filepath.Join(root, "galpon")
	runRaw(t, "..", nil, "go", "build", "-o", bin, "./cmd/galpon")
	defer func() { _ = runCommand("", sourceEnv, bin, "daemon", "stop") }()
	defer func() { _ = runCommand("", targetEnv, bin, "daemon", "stop") }()

	var sourceWorkspace model.Workspace
	decodeCommand(t, &sourceWorkspace, runRaw(t, "", sourceEnv, bin, "workspace", "create", "Old machine"))
	source, err := app.NewClient(filepath.Join(sourceState, "galpon.sock")).CreateAgent(t.Context(), app.CreateAgentRequest{Title: "Researcher", Role: "reviewer", WorkspaceID: sourceWorkspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	remembered := sendMessage(t, bin, sourceEnv, source.ID, "Remember the code word heron")
	waitForMessage(t, bin, sourceEnv, source.ID, remembered.ID, "Noted heron")
	waitForAgentIdle(t, bin, sourceEnv, source.ID)

	exportPath := filepath.Join(root, "researcher.galpon-conversation")
	var exported app.ExportConversationResult
	decodeCommand(t, &exported, runRaw(t, "", sourceEnv, bin, "agent", "export", "Researcher", exportPath))
	if exported.Agent.Title != "Researcher" || exported.Session.Bytes == 0 {
		t.Fatalf("export result = %#v", exported)
	}

	var targetWorkspace model.Workspace
	decodeCommand(t, &targetWorkspace, runRaw(t, "", targetEnv, bin, "workspace", "create", "New machine"))
	var imported app.ImportConversationResult
	decodeCommand(t, &imported, runRaw(t, "", targetEnv, bin, "agent", "import", exportPath, "--workspace", targetWorkspace.ID))
	agent := imported.Agent
	if agent.Title != "Researcher" || agent.Role != "reviewer" || agent.WorkspaceID != targetWorkspace.ID || agent.RendererID == "" || imported.Source.Workspace != "Old machine" {
		t.Fatalf("import result = %#v", imported)
	}

	// Pi forked the imported session into the new agent's own session.
	view := waitForAgentIdle(t, bin, targetEnv, agent.ID)
	if !strings.HasPrefix(view.Agent.SessionPath, filepath.Join(targetState, "agents", agent.ID, "sessions")+string(os.PathSeparator)) {
		t.Fatalf("imported agent session = %q", view.Agent.SessionPath)
	}
	data, err := os.ReadFile(view.Agent.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(string(data), "\n", 2)[0]), &header); err != nil || header.ID != agent.ID || header.CWD != agent.Placement.CWD {
		t.Fatalf("forked session header = %#v, %v", header, err)
	}
	for _, want := range []string{"Remember the code word heron", "Noted heron", `"conversation_imported"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("forked session has no %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(targetState, "agents", agent.ID, "import")); !os.IsNotExist(err) {
		t.Fatalf("import copy remained after Pi registered its fork: %v", err)
	}
	waitForMirroredConversation(t, targetState, agent.ID, "Remember the code word heron", "Noted heron")

	question := sendMessage(t, bin, targetEnv, agent.ID, "What was the code word?")
	waitForMessage(t, bin, targetEnv, agent.ID, question.ID, "The code word was heron")
}
