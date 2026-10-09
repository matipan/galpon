package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

func TestNativeHarnessCoordinationAndControllerRecovery(t *testing.T) {
	fixture := &nativeModel{blocked: map[string]chan struct{}{"claude": make(chan struct{}), "codex": make(chan struct{})}}
	mock := httptest.NewServer(fixture)
	t.Cleanup(mock.Close)
	instance := newNativeInstance(t, nativeBinaries(t), mock.URL, "", false)
	agents := make(map[string]model.Agent)
	for _, kind := range []string{"pi", "claude", "codex"} {
		created := instance.create(t, map[string]any{"title": kind, "harness": kind, "prompt": "NATIVE_REMEMBER heron_" + kind})
		agents[kind] = created.Agent
		waitNativeMessage(t, instance.client, created.ID, created.InitialMessage.ID, "remembered:heron_"+kind)
	}
	for _, source := range []string{"pi", "claude", "codex"} {
		for _, target := range []string{"pi", "claude", "codex"} {
			if source == target {
				continue
			}
			token := source + "_to_" + target
			message := instance.send(t, agents[source].ID, "NATIVE_SEND "+target+" "+token)
			waitNativeMessage(t, instance.client, agents[source].ID, message.ID, "received:"+token)
		}
	}
	for _, kind := range []string{"claude", "codex"} {
		message := instance.send(t, agents[kind].ID, "NATIVE_NOTIFY pi "+kind+"_notify")
		waitNativeMessage(t, instance.client, agents[kind].ID, message.ID, "received:"+kind+"_notify")
	}
	for _, kind := range []string{"claude", "codex"} {
		for _, active := range []bool{false, true} {
			t.Logf("recover %s active=%t", kind, active)
			agent := agents[kind]
			var message model.AgentMessage
			want := "recalled:heron_" + kind
			if active {
				message = instance.send(t, agent.ID, "NATIVE_BLOCK "+kind)
				select {
				case <-fixture.blocked[kind]:
				case <-time.After(15 * time.Second):
					t.Fatal("native turn did not reach the mock model")
				}
				want = "recovered:" + kind
			}
			before := instance.killController(t, agent.ID)
			if !active {
				message = instance.send(t, agent.ID, "NATIVE_RECALL heron_"+kind)
			}
			resumed := waitNativeMessage(t, instance.client, agent.ID, message.ID, want)
			if resumed.Agent.RuntimeID == before.RuntimeID || resumed.Agent.SessionID != before.SessionID {
				t.Fatalf("controller recovery changed the native conversation: %#v", resumed.Agent)
			}
		}
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(instance.state, "galpon.db"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	for _, kind := range []string{"pi", "claude", "codex"} {
		var acknowledged, checkpoints int
		if err := database.QueryRow(`select count(*) from agent_operation_joins j join agent_operations o on o.id=j.operation_id where o.agent_id=? and j.state='acknowledged'`, agents[kind].ID).Scan(&acknowledged); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRow(`select count(*) from work_progress_events p join agent_messages m on m.id=p.message_id where m.target_agent_id=?`, agents[kind].ID).Scan(&checkpoints); err != nil {
			t.Fatal(err)
		}
		wantAcknowledged, wantCheckpoints := 3, 2
		if kind == "pi" {
			wantAcknowledged, wantCheckpoints = 2, 4
		}
		if acknowledged != wantAcknowledged || checkpoints != wantCheckpoints {
			t.Fatalf("%s did not save its child results and delegated checkpoints: acknowledged=%d checkpoints=%d", kind, acknowledged, checkpoints)
		}
		var mirrored int
		if err := database.QueryRow(`select count(*) from conversation_events where agent_id=? and kind='assistant_message_end' and content like '%received:%'`, agents[kind].ID).Scan(&mirrored); err != nil {
			t.Fatal(err)
		}
		if kind != "pi" && mirrored < 2 {
			t.Fatalf("%s replies are missing from Companion", kind)
		}
	}
}

func TestNativeConversationForkAndTransfer(t *testing.T) {
	mock := httptest.NewServer(&nativeModel{})
	t.Cleanup(mock.Close)
	binaries := nativeBinaries(t)
	source := newNativeInstance(t, binaries, mock.URL, "", false)
	target := newNativeInstance(t, binaries, mock.URL, source.binary, false)
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			created := source.create(t, map[string]any{"title": kind, "harness": kind})
			empty := source.idle(t, created.ID)
			if empty.SessionPath != "" {
				t.Fatal("an unused native agent advertised a saved conversation")
			}
			source.killController(t, created.ID)
			message := source.send(t, created.ID, "NATIVE_REMEMBER heron_"+kind)
			waitNativeMessage(t, source.client, created.ID, message.ID, "remembered:heron_"+kind)
			var imageData bytes.Buffer
			if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
				t.Fatal(err)
			}
			imageMessage, err := source.client.SendCompanionImages(t.Context(), created.ID, "NATIVE_IMAGE "+kind, "image-"+kind, []model.ImageAttachment{{Name: "pixel.png", Data: base64.StdEncoding.EncodeToString(imageData.Bytes())}})
			if err != nil {
				t.Fatal(err)
			}
			waitNativeMessage(t, source.client, created.ID, imageMessage.ID, "image:"+kind)
			original := source.idle(t, created.ID)
			fork := source.create(t, map[string]any{"title": kind + " fork", "context_agent": created.ID, "prompt": "NATIVE_RECALL heron_" + kind})
			forked := waitNativeMessage(t, source.client, fork.ID, fork.InitialMessage.ID, "recalled:heron_"+kind)
			if forked.Agent.Kind != kind || forked.Agent.SessionID == original.SessionID {
				t.Fatalf("fork did not get a separate native session: %#v", forked.Agent)
			}
			archive := filepath.Join(source.root, kind+".tar.gz")
			if _, err := source.client.ExportConversation(t.Context(), original.ID, archive); err != nil {
				t.Fatal(err)
			}
			imported, err := target.client.ImportConversation(t.Context(), app.ImportConversationRequest{Path: archive, Title: kind + " imported", WorkspaceID: target.workspace.ID})
			if err != nil {
				t.Fatal(err)
			}
			if imported.Agent.Kind != kind {
				t.Fatalf("import changed the harness: %#v", imported.Agent)
			}
			// This fixture has no native terminal. Run the imported agent without a
			// renderer to test the archive against an independent harness home.
			state, err := store.Open(target.state)
			if err != nil {
				t.Fatal(err)
			}
			err = state.SetAgentPresentation(t.Context(), imported.Agent.ID, "background")
			_ = state.Close()
			if err != nil {
				t.Fatal(err)
			}
			message = target.send(t, imported.Agent.ID, "NATIVE_RECALL heron_"+kind)
			restored := waitNativeMessage(t, target.client, imported.Agent.ID, message.ID, "recalled:heron_"+kind)
			if restored.Agent.SessionID == original.SessionID {
				t.Fatal("import reused the source session identity")
			}
		})
	}
}

func TestCodexNativeTerminalAndControllerShareConversation(t *testing.T) {
	binaries := nativeBinaries(t)
	if binaries["herdr"] == "" {
		t.Skip("Herdr is not installed")
	}
	mock := httptest.NewServer(&nativeModel{})
	t.Cleanup(mock.Close)
	instance := newNativeInstance(t, binaries, mock.URL, "", true)
	worker := instance.create(t, map[string]any{"title": "claude", "harness": "claude", "prompt": "NATIVE_REMEMBER heron_claude"})
	waitNativeMessage(t, instance.client, worker.ID, worker.InitialMessage.ID, "remembered:heron_claude")
	created := instance.create(t, map[string]any{"title": "codex", "harness": "codex", "prompt": "NATIVE_REMEMBER heron_codex"})
	waitNativeMessage(t, instance.client, created.ID, created.InitialMessage.ID, "remembered:heron_codex")
	before := instance.idle(t, created.ID)
	configuration, err := os.OpenFile(filepath.Join(instance.root, "codex", "config.toml"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(configuration, "\n[projects.%q]\ntrust_level = \"trusted\"\n", before.Placement.CWD)
	_ = configuration.Close()
	if err != nil {
		t.Fatal(err)
	}
	opened, err := instance.client.OpenAgent(t.Context(), created.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	pane := opened.RendererID
	t.Cleanup(func() {
		if t.Failed() {
			command := exec.Command(binaries["herdr"], "--session", instance.herdrSession, "pane", "read", pane, "--lines", "100")
			command.Env = instance.env
			output, _ := command.CombinedOutput()
			t.Logf("native terminal:\n%s", output)
		}
	})
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "remembered:heron_codex", "--timeout", "20000")
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-text", pane, "NATIVE_RECALL heron_codex")
	time.Sleep(400 * time.Millisecond)
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-keys", pane, "enter")
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "recalled:heron_codex", "--timeout", "20000")
	instance.idle(t, created.ID)
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-text", pane, "NATIVE_SEND claude codex_native_user")
	time.Sleep(400 * time.Millisecond)
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-keys", pane, "enter")
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "received:codex_native_user", "--timeout", "20000")
	instance.idle(t, created.ID)
	message := instance.send(t, created.ID, "NATIVE_RECALL heron_codex")
	after := waitNativeMessage(t, instance.client, created.ID, message.ID, "recalled:heron_codex")
	if after.Agent.SessionID != before.SessionID || after.Agent.IsBackground() {
		t.Fatalf("native terminal changed the durable conversation: %#v", after.Agent)
	}
}

func TestCodexUserInputDuringDelegation(t *testing.T) {
	binaries := nativeBinaries(t)
	if binaries["herdr"] == "" {
		t.Skip("Herdr is not installed")
	}
	ready, resume := make(chan struct{}), make(chan struct{})
	fixture := &nativeModel{blocked: map[string]chan struct{}{"steering": ready}, resume: map[string]chan struct{}{"steering": resume}}
	mock := httptest.NewServer(fixture)
	t.Cleanup(mock.Close)
	instance := newNativeInstance(t, binaries, mock.URL, "", true)
	created := instance.create(t, map[string]any{"title": "codex", "harness": "codex", "prompt": "NATIVE_REMEMBER heron_codex"})
	waitNativeMessage(t, instance.client, created.ID, created.InitialMessage.ID, "remembered:heron_codex")
	before := instance.idle(t, created.ID)
	configuration, err := os.OpenFile(filepath.Join(instance.root, "codex", "config.toml"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(configuration, "\n[projects.%q]\ntrust_level = \"trusted\"\n", before.Placement.CWD)
	_ = configuration.Close()
	if err != nil {
		t.Fatal(err)
	}
	opened, err := instance.client.OpenAgent(t.Context(), created.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	pane := opened.RendererID
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "wait-output", pane, "--match", "remembered:heron_codex", "--timeout", "20000")
	value, err := instance.client.RuntimeTool(t.Context(), "send_agent", app.RuntimeToolRequest{AgentID: instance.sender.ID, RuntimeID: "fixture-runtime", RequestID: "steer-request", OperationID: instance.operation.ID, OperationAttempt: instance.operation.Attempt, ProtocolGeneration: 3, Args: map[string]any{"agent": created.ID, "prompt": "NATIVE_STEER steering"}})
	if err != nil {
		t.Fatal(err)
	}
	var message model.AgentMessage
	if err := json.Unmarshal(value, &message); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		t.Fatal("delegated model turn did not start")
	}
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-text", pane, "Also keep the human constraint")
	time.Sleep(400 * time.Millisecond)
	herdrCommand(t, binaries["herdr"], instance.env, "--session", instance.herdrSession, "pane", "send-keys", pane, "enter")
	close(resume)
	view := waitNativeMessage(t, instance.client, created.ID, message.ID, "done:steering")
	data, err := os.ReadFile(view.Agent.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	var delivered, steered string
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var row map[string]any
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		payload := nativeObject(row["payload"])
		if row["type"] != "response_item" || payload["role"] != "user" {
			continue
		}
		text := nativeText(payload["content"])
		turn := nativeString(nativeObject(payload["internal_chat_message_metadata_passthrough"])["turn_id"])
		if strings.Contains(text, "NATIVE_STEER steering") {
			delivered = turn
		}
		if strings.Contains(text, "Also keep the human constraint") {
			steered = turn
		}
	}
	if delivered == "" || steered != delivered {
		t.Fatalf("native input did not share the delegated turn: delivery=%q, steering=%q", delivered, steered)
	}
}

type nativeInstance struct {
	root, state, binary, herdrSession string
	env                               []string
	client                            *app.Client
	workspace                         model.Workspace
	sender                            model.Agent
	operation                         model.AgentOperation
	request                           int
}

func nativeBinaries(t *testing.T) map[string]string {
	t.Helper()
	binaries := make(map[string]string)
	for _, kind := range []string{"pi", "claude", "codex"} {
		path, err := exec.LookPath(kind)
		if err != nil {
			if os.Getenv("GALPON_REQUIRE_NATIVE_TESTS") == "1" {
				t.Fatalf("required harness %s is unavailable: %v", kind, err)
			}
			t.Skipf("%s is not installed", kind)
		}
		binaries[kind] = path
	}
	binaries["herdr"], _ = exec.LookPath("herdr")
	return binaries
}

func newNativeInstance(t *testing.T, binaries map[string]string, baseURL, binary string, terminal bool) *nativeInstance {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "galpon-native-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	instance := &nativeInstance{root: root, binary: binary}
	if instance.binary == "" {
		instance.binary = filepath.Join(instance.root, "galpon")
		runRaw(t, "..", nil, "go", "build", "-o", instance.binary, "./cmd/galpon")
	}
	instance.state = filepath.Join(instance.root, "state")
	piHome, claudeHome, codexHome := filepath.Join(instance.root, "pi"), filepath.Join(instance.root, "claude"), filepath.Join(instance.root, "codex")
	writePiConfig(t, piHome, baseURL)
	for _, path := range []string{claudeHome, codexHome} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	codexConfig := fmt.Sprintf(`model = "gpt-5.6"
model_provider = "mock"
model_supports_reasoning = false
approval_policy = "never"
sandbox_mode = "read-only"
[model_providers.mock]
name = "Mock"
base_url = %q
wire_api = "responses"
requires_openai_auth = false
request_max_retries = 0
stream_max_retries = 0
[analytics]
enabled = false
`, baseURL+"/v1")
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(codexConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	// Harness configuration and credentials come only from this fixture.
	instance.env = []string{"PATH=" + filepath.Dir(instance.binary) + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + instance.root, "SHELL=/bin/sh", "LANG=C.UTF-8", "TERM=xterm-256color", "NO_COLOR=1",
		"GALPON_STATE_DIR=" + instance.state,
		"GALPON_PI_PROVIDER=galpon-mock", "GALPON_PI_MODEL=mock-model", "GALPON_TEST_SKIP_PI_PACKAGE_SETUP=1",
		"PI_CODING_AGENT_DIR=" + piHome, "PI_OFFLINE=1", "CLAUDE_CONFIG_DIR=" + claudeHome, "CODEX_HOME=" + codexHome,
		"ANTHROPIC_API_KEY=fixture-only", "ANTHROPIC_BASE_URL=" + baseURL, "ANTHROPIC_MODEL=claude-sonnet-4-5",
		"DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "ENABLE_TOOL_SEARCH=false"}
	if terminal {
		instance.herdrSession = fmt.Sprintf("galpon-native-%d", time.Now().UnixNano())
		instance.env = append(instance.env, "HERDR_SESSION="+instance.herdrSession, "GALPON_HERDR_BIN="+binaries["herdr"])
		t.Cleanup(startTestHerdr(t, binaries["herdr"], instance.herdrSession, instance.env))
	}
	command := exec.Command(instance.binary, "serve")
	command.Env = instance.env
	logFile, err := os.Create(filepath.Join(instance.root, "daemon-output.log"))
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	instance.client = app.NewClient(filepath.Join(instance.state, "galpon.sock"))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if instance.client.Shutdown(ctx) != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
		_ = logFile.Close()
		if t.Failed() {
			files, _ := filepath.Glob(filepath.Join(instance.state, "agents", "*", "native-journal.json"))
			for _, path := range files {
				if data, err := os.ReadFile(path); err == nil {
					t.Logf("journal %s: %s", filepath.Base(filepath.Dir(path)), data)
				}
			}
			if data, err := os.ReadFile(filepath.Join(instance.state, "galpon.log")); err == nil {
				t.Logf("isolated daemon log:\n%s", data)
			}
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		protocol, err := instance.client.CommunicationProtocol(t.Context())
		if err == nil && protocol.Complete && !protocol.Maintenance && protocol.Generation == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("isolated daemon did not start")
		}
		time.Sleep(25 * time.Millisecond)
	}
	instance.workspace, err = instance.client.CreateWorkspace(t.Context(), app.CreateWorkspaceRequest{Title: "Harness coordination"})
	if err != nil {
		t.Fatal(err)
	}
	instance.sender, err = instance.client.CreateAgent(t.Context(), app.CreateAgentRequest{Title: "Fixture sender", WorkspaceID: instance.workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.client.PrepareRuntime(t.Context(), instance.sender.ID, "fixture-runtime"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.client.RegisterRuntime(t.Context(), instance.sender.ID, "fixture-runtime", instance.sender.SessionID, filepath.Join(instance.root, "fixture.jsonl"), 3); err != nil {
		t.Fatal(err)
	}
	instance.operation, err = instance.client.RegisterDirectOperation(t.Context(), instance.sender.ID, app.DirectOperationRequest{RuntimeID: "fixture-runtime", UserEntryID: "fixture-input", ProtocolGeneration: 3})
	if err != nil {
		t.Fatal(err)
	}
	return instance
}

func (n *nativeInstance) create(t *testing.T, args map[string]any) app.CreateAgentToolResult {
	t.Helper()
	if err := n.client.RenewCoordinationOperation(t.Context(), n.sender.ID, n.operation.ID, "fixture-runtime", n.operation.Attempt); err != nil {
		t.Fatal(err)
	}
	n.request++
	value, err := n.client.RuntimeTool(t.Context(), "create_agent", app.RuntimeToolRequest{AgentID: n.sender.ID, RuntimeID: "fixture-runtime", RequestID: fmt.Sprintf("create-%d", n.request), OperationID: n.operation.ID, OperationAttempt: n.operation.Attempt, ProtocolGeneration: 3, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	var created app.CreateAgentToolResult
	if err := json.Unmarshal(value, &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatalf("created agent = %s", value)
	}
	if _, hasPrompt := args["prompt"]; hasPrompt && created.InitialMessage == nil {
		t.Fatalf("initial assignment is missing: %s", value)
	}
	return created
}

func (n *nativeInstance) send(t *testing.T, agentID, prompt string) model.AgentMessage {
	return sendMessage(t, n.binary, n.env, agentID, prompt)
}

func (n *nativeInstance) idle(t *testing.T, agentID string) model.Agent {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		view, err := n.client.Agent(t.Context(), agentID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Agent.Status == "idle" && view.Agent.RuntimeID != "" {
			if view.Agent.Harness() == model.HarnessPi {
				return view.Agent
			}
			if _, err := os.Stat(filepath.Join(n.state, "agents", agentID, "native-process.json")); err == nil {
				return view.Agent
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("native runtime did not become idle")
	return model.Agent{}
}

func (n *nativeInstance) killController(t *testing.T, agentID string) model.Agent {
	t.Helper()
	view, err := n.client.Agent(t.Context(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	var process struct {
		PID       int    `json:"pid"`
		RuntimeID string `json:"runtimeId"`
	}
	data, err := os.ReadFile(filepath.Join(n.state, "agents", agentID, "native-process.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &process); err != nil {
		t.Fatal(err)
	}
	if process.RuntimeID != view.Agent.RuntimeID || process.PID <= 0 {
		t.Fatalf("controller identity does not match the registered runtime: %#v", process)
	}
	child, err := os.FindProcess(process.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Kill(); err != nil {
		t.Fatal(err)
	}
	return view.Agent
}

func waitNativeMessage(t *testing.T, client *app.Client, agentID, messageID, want string) model.AgentView {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		view, err := client.Agent(t.Context(), agentID)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range view.Messages {
			if message.ID != messageID {
				continue
			}
			if message.Status == "failed" {
				t.Fatalf("native request failed: %s", message.Error)
			}
			if message.Status == "completed" {
				if !strings.Contains(message.Response, want) {
					t.Fatalf("native response = %q, want %q", message.Response, want)
				}
				return view
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if view, err := client.Agent(t.Context(), agentID); err == nil {
		t.Logf("native timeout state: %#v", view.Agent)
	}
	t.Fatalf("native request %s did not complete", messageID)
	return model.AgentView{}
}
