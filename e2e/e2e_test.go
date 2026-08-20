package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

func TestRealPiHerdrDurableAgentWorkflow(t *testing.T) {
	piBin, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	herdrBin, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("Herdr is not installed")
	}

	var calls atomic.Int64
	var imageInputSeen atomic.Bool
	var workerTarget atomic.Value
	workerTarget.Store("")
	var promptedWorkspace atomic.Value
	promptedWorkspace.Store("")
	var promptedRepository atomic.Value
	promptedRepository.Store("")
	var promptedCreateIssued atomic.Bool
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
		calls.Add(1)
		if containsInputImage(request) {
			imageInputSeen.Store(true)
		}
		prompt, outputs := responseInput(request)
		switch {
		case strings.Contains(prompt, "Completed correlated result") && strings.Contains(prompt, "Prompted worker result"):
			writeTextResponse(w, "Automatic result received")
		case strings.Contains(prompt, "Post-promotion check"):
			writeTextResponse(w, "Promoted worker reply")
		case strings.Contains(prompt, "Run the prompted check"):
			writeTextResponse(w, "Prompted worker result")
		case strings.Contains(prompt, "Create a worker with an initial prompt") && promptedCreateIssued.CompareAndSwap(false, true):
			writeToolResponse(w, "galpon_create_agent", map[string]any{
				"title": "Prompted Worker", "workspace": promptedWorkspace.Load().(string),
				"role": "implementer", "repository": promptedRepository.Load().(string),
				"prompt": "Run the prompted check",
			})
		case strings.Contains(prompt, "Create a worker with an initial prompt"):
			if !strings.Contains(outputs[len(outputs)-1], `"initialMessage"`) {
				http.Error(w, "create_agent result has no initial message: "+outputs[len(outputs)-1], http.StatusBadRequest)
				return
			}
			writeTextResponse(w, "Prompted creation complete")
		case strings.Contains(prompt, "Do the delegated check"):
			writeTextResponse(w, "Worker result")
		case strings.Contains(prompt, "Ask the worker for a delegated check") && len(outputs) == 0:
			writeToolResponse(w, "galpon_send_agent", map[string]any{"agent": workerTarget.Load().(string), "prompt": "Do the delegated check"})
		case strings.Contains(prompt, "Ask the worker for a delegated check") && len(outputs) == 1:
			messageID := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`).FindString(outputs[0])
			if messageID == "" {
				http.Error(w, "send_agent result has no message ID: "+outputs[0], http.StatusBadRequest)
				return
			}
			writeToolResponse(w, "galpon_await_agent", map[string]any{"message_id": messageID})
		case strings.Contains(prompt, "Ask the worker for a delegated check"):
			writeTextResponse(w, "Delegation complete")
		case strings.Contains(prompt, "Resume and reply again"):
			writeTextResponse(w, "Resumed Pi reply")
		default:
			writeTextResponse(w, "First Pi reply")
		}
	}))
	defer mock.Close()

	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	piHome := filepath.Join(root, "pi")
	writePiConfig(t, piHome, mock.URL)
	session := fmt.Sprintf("galpon-pi-e2e-%d", time.Now().UnixNano())
	herdrConfig := filepath.Join(root, "herdr.toml")
	env := append(os.Environ(),
		"GALPON_STATE_DIR="+stateDir,
		"GALPON_PI_BIN="+piBin,
		"GALPON_PI_PROVIDER=galpon-mock",
		"GALPON_PI_MODEL=mock-model",
		"GALPON_HERDR_BIN="+herdrBin,
		"HERDR_CONFIG_PATH="+herdrConfig,
		"HERDR_SESSION="+session,
		"PI_CODING_AGENT_DIR="+piHome,
		"PI_OFFLINE=1",
		"NO_COLOR=",
	)
	stopHerdr := startTestHerdr(t, herdrBin, session, env)
	defer stopHerdr()

	bin := filepath.Join(root, "galpon")
	runRaw(t, "..", nil, "go", "build", "-o", bin, "./cmd/galpon")
	defer func() { _ = runCommand("", env, bin, "daemon", "stop") }()
	repositoryPath := createRepository(t, root)
	forkPath := createNamedRepository(t, root, "fork")

	var repo model.Repository
	decodeCommand(t, &repo, runRaw(t, "", env, bin, "repo", "add", repositoryPath, "--remote", "matipan="+forkPath, "--push-remote", "matipan"))
	if repo.DefaultRemote != "origin" || repo.PushRemote != "matipan" || len(repo.Remotes) != 2 {
		t.Fatalf("repository remotes = %#v", repo)
	}
	reviewPath := createNamedRepository(t, root, "review")
	decodeCommand(t, &repo, runRaw(t, "", env, bin, "repo", "remote", "add", repo.ID, "review", reviewPath))
	if repo.PushRemote != "matipan" || len(repo.Remotes) != 3 {
		t.Fatalf("repository remotes after add = %#v", repo)
	}
	remoteList := runRaw(t, "", env, bin, "repo", "remote", "list", repo.Title)
	if !strings.Contains(remoteList, `"pushRemote": "matipan"`) || !strings.Contains(remoteList, forkPath) {
		t.Fatalf("remote list = %s", remoteList)
	}
	var manual app.CreateWorktreeResult
	decodeCommand(t, &manual, runRaw(t, "", env, bin, "worktree", "create", "--repo", repo.ID, "--workspace-title", "Manual E2E"))
	if manual.Workspace.Title != "Manual E2E" || manual.Worktree.Lifecycle != "workspace" {
		t.Fatalf("manual worktree = %#v", manual)
	}
	if _, err := os.Stat(filepath.Join(manual.Worktree.Path, "README.md")); err != nil {
		t.Fatalf("manual managed worktree: %v", err)
	}
	var openedManual model.Worktree
	decodeCommand(t, &openedManual, runRaw(t, "", env, bin, "worktree", "open", manual.Worktree.ID))
	if openedManual.ID != manual.Worktree.ID {
		t.Fatalf("opened manual worktree = %#v", openedManual)
	}

	var workspace model.Workspace
	decodeCommand(t, &workspace, runRaw(t, "", env, bin, "workspace", "create", "E2E work"))
	secondaryPath := createNamedRepository(t, root, "secondary")
	var secondaryRepo model.Repository
	decodeCommand(t, &secondaryRepo, runRaw(t, "", env, bin, "repo", "add", secondaryPath))

	var captain model.Agent
	decodeCommand(t, &captain, runRaw(t, "", env, bin, "agent", "create", "Captain", "--workspace", workspace.ID, "--role", "coordinator", "--repo", repo.ID, "--secondary", secondaryRepo.ID))
	if captain.Kind != "pi" || captain.Role != "coordinator" || captain.SessionID != captain.ID || captain.RendererID == "" || len(captain.Placement.Worktrees) != 2 {
		t.Fatalf("Pi agent = %#v", captain)
	}
	var captainCreated model.AgentView
	decodeCommand(t, &captainCreated, runRaw(t, "", env, bin, "agent", "show", captain.ID))
	if len(captainCreated.Worktrees) != 2 {
		t.Fatalf("captain placement = %#v", captainCreated)
	}
	for _, worktree := range captainCreated.Worktrees {
		if _, err := os.Stat(filepath.Join(worktree.Path, "README.md")); err != nil {
			t.Fatalf("managed worktree: %v", err)
		}
	}
	if got := strings.TrimSpace(runRaw(t, captainCreated.Worktrees[0].Path, nil, "git", "config", "--get", "remote.pushDefault")); got != "matipan" {
		t.Fatalf("managed worktree push remote = %q", got)
	}
	first := sendMessage(t, bin, env, captain.ID, "Reply from the mock")
	firstView := waitForMessage(t, bin, env, captain.ID, first.ID, "First Pi reply")
	waitForMirroredConversation(t, stateDir, captain.ID, "Reply from the mock", "First Pi reply")

	var imageData bytes.Buffer
	pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixel.Set(0, 0, color.RGBA{R: 40, G: 80, B: 120, A: 255})
	if err := png.Encode(&imageData, pixel); err != nil {
		t.Fatal(err)
	}
	imageClient := app.NewClient(filepath.Join(stateDir, "galpon.sock"))
	imageMessage, err := imageClient.SendCompanionImages(t.Context(), captain.ID, "Review this image", "e2e-image", []model.ImageAttachment{{
		Name: "pixel.png", Data: base64.StdEncoding.EncodeToString(imageData.Bytes()),
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitForMessage(t, bin, env, captain.ID, imageMessage.ID, "First Pi reply")
	if !imageInputSeen.Load() {
		t.Fatal("Pi provider request did not contain the Companion image")
	}
	if firstView.Agent.SessionPath == "" {
		t.Fatal("Pi session path was not persisted")
	}
	if _, err := os.Stat(firstView.Agent.SessionPath); err != nil {
		t.Fatalf("Pi session file: %v", err)
	}
	assertHerdrPaneName(t, herdrBin, env, firstView.Agent.RendererID, captain.Title)
	paneANSI := herdrCommand(t, herdrBin, env, "--session", session, "pane", "read", firstView.Agent.RendererID, "--source", "recent", "--format", "ansi")
	if !bytes.Contains(paneANSI, []byte("38;2;18;171;52")) && !bytes.Contains(paneANSI, []byte("38;2;171;205;239")) {
		t.Fatalf("Pi pane does not contain the configured system palette: %q", paneANSI)
	}
	firstRuntime := firstView.Agent.RuntimeID

	herdrCommand(t, herdrBin, env, "--session", session, "pane", "close", firstView.Agent.RendererID)
	decodeCommand(t, &captain, runRaw(t, "", env, bin, "agent", "open", captain.ID))
	waitForRuntimeChange(t, bin, env, captain.ID, firstRuntime)
	second := sendMessage(t, bin, env, captain.ID, "Resume and reply again")
	secondView := waitForMessage(t, bin, env, captain.ID, second.ID, "Resumed Pi reply")
	if secondView.Agent.SessionID != firstView.Agent.SessionID || secondView.Agent.SessionPath != firstView.Agent.SessionPath {
		t.Fatalf("Pi did not resume the same session: before=%#v after=%#v", firstView.Agent, secondView.Agent)
	}

	var worker model.Agent
	decodeCommand(t, &worker, runRaw(t, "", env, bin, "agent", "create", "Worker", "--workspace", workspace.ID, "--role", "implementer", "--context-agent", captain.ID, "--repo", repo.ID))
	if worker.ContextAgentID != captain.ID || worker.Placement.PrimaryWorktreeID == captain.Placement.PrimaryWorktreeID {
		t.Fatalf("worker did not receive independent context and placement: %#v", worker)
	}
	workerTarget.Store(worker.ID)
	delegation := sendMessage(t, bin, env, captain.ID, "Ask the worker for a delegated check")
	waitForMessage(t, bin, env, captain.ID, delegation.ID, "Delegation complete")
	workerView := waitForAgentResponse(t, bin, env, worker.ID, "Worker result")
	delegated := false
	for _, message := range workerView.Messages {
		if message.SenderAgentID == captain.ID && message.Prompt == "Do the delegated check" && message.Status == "completed" {
			delegated = true
		}
	}
	if !delegated {
		t.Fatalf("worker did not receive the captain message: %#v", workerView.Messages)
	}

	promptedWorkspace.Store(workspace.ID)
	promptedRepository.Store(repo.ID)
	creation := sendMessage(t, bin, env, captain.ID, "Create a worker with an initial prompt")
	waitForMessage(t, bin, env, captain.ID, creation.ID, "Prompted creation complete")
	promptedClient := app.NewClient(filepath.Join(stateDir, "galpon.sock"))
	dashboard, err := promptedClient.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var promptedWorker model.Agent
	for _, agent := range dashboard.Agents {
		if agent.Title == "Prompted Worker" {
			promptedWorker = agent
			break
		}
	}
	if promptedWorker.ID == "" || promptedWorker.CreatedByAgentID != captain.ID || promptedWorker.Presentation != "background" || promptedWorker.RendererID != "" {
		t.Fatalf("prompted worker = %#v", promptedWorker)
	}
	promptedView := waitForAgentResponse(t, bin, env, promptedWorker.ID, "Prompted worker result")
	prompted := false
	for _, message := range promptedView.Messages {
		if message.SenderAgentID == captain.ID && message.Prompt == "Run the prompted check" && message.Status == "completed" {
			prompted = true
		}
	}
	if !prompted {
		t.Fatalf("prompted worker did not receive its initial message: %#v", promptedView.Messages)
	}
	waitForAgentResponse(t, bin, env, captain.ID, "Automatic result received")
	backgroundSessionPath := promptedView.Agent.SessionPath
	promotedWorker, err := promptedClient.OpenAgent(t.Context(), promptedWorker.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if promotedWorker.Presentation != "foreground" || promotedWorker.RendererID == "" || promotedWorker.SessionPath != backgroundSessionPath {
		t.Fatalf("promoted worker = %#v, background session = %q", promotedWorker, backgroundSessionPath)
	}
	promotedView := waitForRuntimeChange(t, bin, env, promptedWorker.ID, promptedView.Agent.RuntimeID)
	postPromotion := sendMessage(t, bin, env, promptedWorker.ID, "Post-promotion check")
	postPromotionView := waitForMessage(t, bin, env, promptedWorker.ID, postPromotion.ID, "Promoted worker reply")
	if promotedView.Agent.SessionID != promptedView.Agent.SessionID || postPromotionView.Agent.SessionPath != backgroundSessionPath {
		t.Fatalf("promoted Pi did not resume the same session: before=%#v after=%#v", promptedView.Agent, postPromotionView.Agent)
	}

	snapshot := runRaw(t, "", env, bin, "snapshot")
	for _, want := range []string{"GALPÓN", "WORKSPACES", "AGENTS", "WORKTREES", "Manual E2E", "Captain", "Worker", "\x1b["} {
		if !strings.Contains(snapshot, want) {
			t.Fatalf("snapshot omitted %q", want)
		}
	}
	config := runRaw(t, "", env, bin, "herdr", "config")
	if !strings.Contains(config, `key = "ctrl+k"`) || !strings.Contains(config, `type = "popup"`) {
		t.Fatalf("Herdr config = %s", config)
	}
	client := app.NewClient(filepath.Join(stateDir, "galpon.sock"))
	deleted, err := client.DeleteResource(t.Context(), "agent", worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Hidden.Agents != 1 {
		t.Fatalf("worker deletion = %#v", deleted)
	}
	closedPane := exec.Command(herdrBin, "--session", session, "pane", "get", workerView.Agent.RendererID)
	closedPane.Env = env
	if err := closedPane.Run(); err == nil {
		t.Fatalf("deleted worker pane %s still exists", workerView.Agent.RendererID)
	}

	captainView := waitForAgentIdle(t, bin, env, captain.ID)
	herdrCommand(t, herdrBin, env, "--session", session, "pane", "send-text", captainView.Agent.RendererID, "/finish")
	herdrCommand(t, herdrBin, env, "--session", session, "pane", "send-keys", captainView.Agent.RendererID, "enter")
	herdrCommand(t, herdrBin, env, "--session", session, "pane", "wait-output", captainView.Agent.RendererID, "--match", "Finish Captain?", "--timeout", "5000")
	herdrCommand(t, herdrBin, env, "--session", session, "pane", "send-keys", captainView.Agent.RendererID, "enter")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pane := exec.Command(herdrBin, "--session", session, "pane", "get", captainView.Agent.RendererID)
		pane.Env = env
		dashboard, dashboardErr := client.Dashboard(t.Context())
		_, visible := dashboard.Agent(captain.ID)
		if pane.Run() != nil && dashboardErr == nil && !visible {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if dashboard, err := client.Dashboard(t.Context()); err != nil {
		t.Fatal(err)
	} else if _, visible := dashboard.Agent(captain.ID); visible {
		logData, _ := os.ReadFile(filepath.Join(stateDir, "galpon.log"))
		t.Fatalf("finished captain is still visible: %#v\n%s", dashboard.Agents, logData)
	}
	finishedPane := exec.Command(herdrBin, "--session", session, "pane", "get", captainView.Agent.RendererID)
	finishedPane.Env = env
	if err := finishedPane.Run(); err == nil {
		t.Fatalf("finished captain pane %s still exists", captainView.Agent.RendererID)
	}
	if calls.Load() != 12 {
		t.Fatalf("mock response calls = %d, want 12", calls.Load())
	}
}

func TestCheckpointCommandRoundTrip(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "galpon")
	runRaw(t, "..", nil, "go", "build", "-o", bin, "./cmd/galpon")
	checkpointPath := filepath.Join(root, "migration.galpon")
	sourceState := filepath.Join(root, "source-state")
	targetState := filepath.Join(root, "target-state")
	sourceEnv := append(os.Environ(), "GALPON_STATE_DIR="+sourceState, "GALPON_CHECKPOINT_PASSPHRASE=isolated test passphrase")
	targetEnv := append(os.Environ(), "GALPON_STATE_DIR="+targetState, "GALPON_CHECKPOINT_PASSPHRASE=isolated test passphrase")
	defer func() { _ = runCommand("", sourceEnv, bin, "daemon", "stop") }()
	defer func() { _ = runCommand("", targetEnv, bin, "daemon", "stop") }()

	repositoryPath := createNamedRepository(t, root, "checkpoint-source")
	var repository model.Repository
	decodeCommand(t, &repository, runRaw(t, "", sourceEnv, bin, "repo", "add", repositoryPath))
	var created app.CreateWorktreeResult
	decodeCommand(t, &created, runRaw(t, "", sourceEnv, bin, "worktree", "create", "--repo", repository.ID, "--workspace-title", "Migrated work"))
	if err := os.WriteFile(filepath.Join(created.Worktree.Path, "README.md"), []byte("local checkpoint change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created.Worktree.Path, "notes.txt"), []byte("untracked checkpoint file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unmanagedDirectory := filepath.Join(root, "unmanaged-agent-directory")
	if err := os.MkdirAll(unmanagedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceClient := app.NewClient(filepath.Join(sourceState, "galpon.sock"))
	unmanagedAgent, err := sourceClient.CreateAgent(t.Context(), app.CreateAgentRequest{
		Title: "Unmanaged agent", WorkspaceID: created.Workspace.ID,
		Placement: app.AgentPlacementRequest{Type: "none", CWD: unmanagedDirectory},
	})
	if err != nil {
		t.Fatal(err)
	}

	var checkpointResult app.CheckpointResult
	decodeCommand(t, &checkpointResult, runRaw(t, "", sourceEnv, bin, "checkpoint", "create", "--allow-local-remotes", checkpointPath))
	if checkpointResult.Resources.Worktrees != 1 || checkpointResult.Resources.Agents != 1 || checkpointResult.DirtyWorktrees != 1 || checkpointResult.GitRefs != 1 || checkpointResult.UnmanagedDirectories != 1 {
		t.Fatalf("checkpoint result = %#v", checkpointResult)
	}
	if err := os.RemoveAll(unmanagedDirectory); err != nil {
		t.Fatal(err)
	}
	var restoreResult app.RestoreCheckpointResult
	decodeCommand(t, &restoreResult, runRaw(t, "", targetEnv, bin, "checkpoint", "restore", checkpointPath))
	if restoreResult.ID != checkpointResult.ID || restoreResult.Resources.Worktrees != 1 || restoreResult.Resources.Agents != 1 || restoreResult.UnmanagedDirectories != 1 {
		t.Fatalf("restore result = %#v", restoreResult)
	}
	client := app.NewClient(filepath.Join(targetState, "galpon.sock"))
	dashboard, err := client.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Workspaces) != 1 || dashboard.Workspaces[0].Title != "Migrated work" || len(dashboard.Worktrees) != 1 || len(dashboard.Agents) != 1 {
		t.Fatalf("restored dashboard = %#v", dashboard)
	}
	if dashboard.Agents[0].ID != unmanagedAgent.ID || dashboard.Agents[0].Placement.CWD != unmanagedDirectory {
		t.Fatalf("restored unmanaged agent = %#v", dashboard.Agents[0])
	}
	if entries, err := os.ReadDir(unmanagedDirectory); err != nil || len(entries) != 0 {
		t.Fatalf("restored unmanaged directory = %#v, %v", entries, err)
	}
	restored := dashboard.Worktrees[0]
	if data, err := os.ReadFile(filepath.Join(restored.Path, "README.md")); err != nil || string(data) != "local checkpoint change\n" {
		t.Fatalf("restored README = %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(restored.Path, "notes.txt")); err != nil || string(data) != "untracked checkpoint file\n" {
		t.Fatalf("restored notes = %q, %v", data, err)
	}
	status := runRaw(t, restored.Path, nil, "git", "status", "--porcelain=v1")
	if !strings.Contains(status, " M README.md") || !strings.Contains(status, "?? notes.txt") {
		t.Fatalf("restored status = %q", status)
	}
}

func TestSoftDeleteAndCleanupCommand(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	env := append(os.Environ(), "GALPON_STATE_DIR="+stateDir)
	bin := filepath.Join(root, "galpon")
	runRaw(t, "..", nil, "go", "build", "-o", bin, "./cmd/galpon")
	defer func() { _ = runCommand("", env, bin, "daemon", "stop") }()

	source := createNamedRepository(t, root, "cleanup-source")
	var repository model.Repository
	decodeCommand(t, &repository, runRaw(t, "", env, bin, "repo", "add", source))
	client := app.NewClient(filepath.Join(stateDir, "galpon.sock"))
	deleted, err := client.DeleteResource(t.Context(), "repository", repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Hidden.Repositories != 1 {
		t.Fatalf("delete result = %#v", deleted)
	}
	if snapshot := runRaw(t, "", env, bin, "snapshot"); strings.Contains(snapshot, "cleanup-source") {
		t.Fatalf("deleted repository remained visible: %s", snapshot)
	}
	var cleaned model.CleanupResult
	decodeCommand(t, &cleaned, runRaw(t, "", env, bin, "cleanup"))
	if cleaned.Removed.Repositories != 1 {
		t.Fatalf("cleanup result = %#v", cleaned)
	}
	if _, err := os.Stat(repository.MirrorPath); !os.IsNotExist(err) {
		t.Fatalf("repository mirror still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "README.md")); err != nil {
		t.Fatalf("source checkout was removed: %v", err)
	}
	var readded model.Repository
	decodeCommand(t, &readded, runRaw(t, "", env, bin, "repo", "add", source))
	if readded.ID == repository.ID {
		t.Fatalf("repository was not re-created: %#v", readded)
	}
}

func startTestHerdr(t *testing.T, bin, session string, env []string) func() {
	t.Helper()
	server := exec.Command(bin, "--session", session, "server")
	server.Env = env
	server.Stdout = os.Stderr
	server.Stderr = os.Stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	waitForHerdr(t, bin, session, env)
	return func() {
		stop := exec.Command(bin, "--session", session, "server", "stop")
		stop.Env = env
		_ = stop.Run()
		_, _ = server.Process.Wait()
		remove := exec.Command(bin, "session", "delete", session)
		remove.Env = env
		_ = remove.Run()
	}
}

func containsInputImage(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if typed["type"] == "input_image" {
			return true
		}
		for _, child := range typed {
			if containsInputImage(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsInputImage(child) {
				return true
			}
		}
	}
	return false
}

func responseInput(request map[string]any) (string, []string) {
	input, _ := request["input"].([]any)
	var prompt string
	var outputs []string
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if item["type"] == "function_call_output" {
			outputs = append(outputs, fmt.Sprint(item["output"]))
		}
		if item["role"] != "user" {
			continue
		}
		content, _ := item["content"].([]any)
		for _, rawPart := range content {
			part, _ := rawPart.(map[string]any)
			if part["type"] == "input_text" {
				prompt += fmt.Sprint(part["text"])
			}
		}
	}
	return prompt, outputs
}

func writeTextResponse(w http.ResponseWriter, text string) {
	writeResponse(w, map[string]any{
		"type": "message", "role": "assistant", "phase": "final_answer", "id": "msg_" + strings.ReplaceAll(text, " ", "_"), "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
	})
}

func writeToolResponse(w http.ResponseWriter, name string, args map[string]any) {
	arguments, _ := json.Marshal(args)
	writeResponse(w, map[string]any{"type": "function_call", "id": "item_" + name, "call_id": "call_" + name, "name": name, "arguments": string(arguments)})
}

func writeResponse(w http.ResponseWriter, item map[string]any) {
	id := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	events := []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id}},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": map[string]any{
			"id": id, "status": "completed", "output": []any{item},
			"usage": map[string]any{"input_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 4, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 14},
		}},
	}
	for _, event := range events {
		data, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
	}
}

func writePiConfig(t *testing.T, home, baseURL string) {
	t.Helper()
	themeDir := filepath.Join(home, "themes")
	if err := os.MkdirAll(themeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	theme, err := os.ReadFile(filepath.Join("testdata", "system-theme.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "galpon-e2e-system.json"), theme, 0o600); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"theme": "galpon-e2e-system"})
	if err := os.WriteFile(filepath.Join(home, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"providers": map[string]any{"galpon-mock": map[string]any{
		"baseUrl": baseURL + "/v1", "api": "openai-responses", "apiKey": "test", "authHeader": true,
		"models": []any{map[string]any{
			"id": "mock-model", "name": "Galpon mock", "reasoning": false, "input": []string{"text", "image"},
			"contextWindow": 128000, "maxTokens": 4096,
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
		}},
	}}}
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(home, "models.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func sendMessage(t *testing.T, bin string, env []string, agentID, prompt string) model.AgentMessage {
	t.Helper()
	var message model.AgentMessage
	decodeCommand(t, &message, runRaw(t, "", env, bin, "agent", "send", agentID, prompt))
	return message
}

func waitForMessage(t *testing.T, bin string, env []string, agentID, messageID, want string) model.AgentView {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last model.AgentView
	for time.Now().Before(deadline) {
		decodeCommand(t, &last, runRaw(t, "", env, bin, "agent", "show", agentID))
		for _, message := range last.Messages {
			if message.ID == messageID && message.Status == "completed" && strings.Contains(message.Response, want) {
				return last
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for message %s with %q: %#v", messageID, want, last)
	return model.AgentView{}
}

func waitForAgentResponse(t *testing.T, bin string, env []string, agentID, want string) model.AgentView {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var view model.AgentView
		decodeCommand(t, &view, runRaw(t, "", env, bin, "agent", "show", agentID))
		for _, message := range view.Messages {
			if message.Status == "completed" && strings.Contains(message.Response, want) {
				return view
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for agent %s response %q", agentID, want)
	return model.AgentView{}
}

func waitForAgentIdle(t *testing.T, bin string, env []string, agentID string) model.AgentView {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var view model.AgentView
		decodeCommand(t, &view, runRaw(t, "", env, bin, "agent", "show", agentID))
		if view.Agent.RuntimeID != "" && view.Agent.Status == "idle" {
			return view
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Pi runtime did not become idle for agent %s", agentID)
	return model.AgentView{}
}

func waitForRuntimeChange(t *testing.T, bin string, env []string, agentID, oldRuntime string) model.AgentView {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var view model.AgentView
		decodeCommand(t, &view, runRaw(t, "", env, bin, "agent", "show", agentID))
		if view.Agent.RuntimeID != "" && view.Agent.RuntimeID != oldRuntime && view.Agent.Status == "idle" {
			return view
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Pi runtime did not restart for agent %s", agentID)
	return model.AgentView{}
}

func waitForMirroredConversation(t *testing.T, stateDir, agentID, userText, assistantText string) {
	t.Helper()
	st, err := store.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, err := st.ConversationEvents(context.Background(), agentID)
		if err != nil {
			t.Fatal(err)
		}
		foundUser, foundAssistant := false, false
		for _, event := range events {
			foundUser = foundUser || event.Kind == "user_message" && strings.Contains(event.Content, userText)
			foundAssistant = foundAssistant || event.Kind == "assistant_message_end" && strings.Contains(event.Content, assistantText)
		}
		if foundUser && foundAssistant {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	events, _ := st.ConversationEvents(context.Background(), agentID)
	t.Fatalf("Pi conversation was not mirrored: %#v", events)
}

func createRepository(t *testing.T, root string) string {
	return createNamedRepository(t, root, "repo")
}

func createNamedRepository(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	runRaw(t, "", nil, "git", "init", "-b", "main", path)
	runRaw(t, path, nil, "git", "config", "user.name", "Galpon E2E")
	runRaw(t, path, nil, "git", "config", "user.email", "galpon@example.invalid")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRaw(t, path, nil, "git", "add", "README.md")
	runRaw(t, path, nil, "git", "commit", "-m", "fixture")
	return path
}

func decodeCommand(t *testing.T, out any, text string) {
	t.Helper()
	if err := json.Unmarshal([]byte(text), out); err != nil {
		t.Fatalf("decode command output: %v\n%s", err, text)
	}
}

func runRaw(t *testing.T, cwd string, env []string, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v\n%s", bin, args, err, stderr.String())
	}
	return stdout.String()
}

func runCommand(cwd string, env []string, bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	cmd.Env = env
	return cmd.Run()
}
