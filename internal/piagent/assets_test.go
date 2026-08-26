package piagent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

func TestMaterializeInstallsPiExtensionAndRemovesObsoleteTheme(t *testing.T) {
	stateDir := t.TempDir()
	obsoleteTheme := filepath.Join(stateDir, "runtime", "pi", "galpon-tokyonight-moon.json")
	if err := os.MkdirAll(filepath.Dir(obsoleteTheme), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obsoleteTheme, []byte("obsolete"), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := Materialize(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(obsoleteTheme); !os.IsNotExist(err) {
		t.Fatalf("obsolete Pi theme still exists: %v", err)
	}
	extension, err := os.ReadFile(values.Extension)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"galpon_create_workspace", "galpon_create_agent", "galpon_cleanup_agents", "agent_ids", "galpon_send_agent", "todo_id", "todo_policy", "galpon:todo:link:v1", "galpon:todo:settle:v1", "galpon_await_agent", "galpon_await_agents", "message_ids", "return_when", `registerCommand("finish"`, `registerCommand("operations"`, "ctx.ui.custom<void>", "OperationsCockpit", `/v1/workspaces/${encodeURIComponent(workspaceId)}/operations`, `/v1/runtime/agents/${agentId}/finish`, "ctx.shutdown()", "GALPON_PI_EXTENSION", "watchFile(extensionPath", `registerCommand("galpon-reload-extension"`, "expandPromptTemplates: true", "unwatchFile(extensionPath)", `event.reason !== "reload"`} {
		if !strings.Contains(string(extension), name) {
			t.Errorf("extension omitted %s", name)
		}
	}
	for _, want := range []string{"provides optional tools", "roles and names do not have special built-in behavior", "only when the user requests coordination", "Create a new workspace only for work that a foreground agent will own", "Always create background delegated agents in your current workspace", "Never create a workspace for a background delegated agent", "one queued cross-agent message per Pi turn", "Initial work request to queue before the new agent starts", "result then includes initialMessage", "inform act for one-way coordination", "Normally omit result_mode", "Galpón selects join during an active inbound delivery and notify during a direct user turn", "Set result_mode to notify only for detached work", "Progress reports are only for active inbound delegated requests", "not direct user turns or completed-result notifications", "Never create a synchronous wait cycle", "one global timeout", "does not cancel unfinished agent work", "outcomes stay in message ID order", "queued or delivered result is still pending", "settled without a final text response", "response closed before it completed", "only when the user explicitly asks for cleanup", "select the exact relevant IDs", "completed correlated result", "Do not use galpon_send_agent to return the current delivery result", "Galpón records and routes the final response automatically", "accepted: false", "recorded: false", "no_active_delegated_request", "Progress was not recorded because this turn is not an active delegated request delivery"} {
		if !strings.Contains(string(extension), want) {
			t.Errorf("extension prompt omitted %q", want)
		}
	}
	for _, unwanted := range []string{"when separate work is useful", "A captain is", "Use galpon_send_agent to delegate", "galpon_cleanup_created_agents"} {
		if strings.Contains(string(extension), unwanted) {
			t.Errorf("extension prompt still encourages delegation with %q", unwanted)
		}
	}
	if strings.Contains(string(extension), "setTheme(") {
		t.Error("extension overrides Pi's configured theme")
	}
	if strings.Contains(string(extension), "setActiveTools(") {
		t.Error("extension overwrites another extension's active tools")
	}
}

func TestMaterializedExtensionMirrorsPiConversation(t *testing.T) {
	values, err := Materialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	extension, err := os.ReadFile(values.Extension)
	if err != nil {
		t.Fatal(err)
	}
	source := string(extension)
	for _, want := range []string{
		`/conversation-events`,
		`{ runtimeId, events }`,
		`runtimeSeq`,
		`createdAt: number`,
		`isError?: boolean`,
		`"user_message"`,
		`"assistant_message_start"`,
		`"assistant_text_delta"`,
		`"assistant_message_end"`,
		`"tool_execution_start"`,
		`"tool_execution_update"`,
		`"tool_execution_end"`,
		`"compaction_start"`,
		`"compaction_end"`,
		`ctx.sessionManager.getBranch()`,
		`stablePiEventId`,
		`maxPendingConversationEvents`,
		`maxConversationBatchBytes`,
		`maxConversationContentBytes`,
		`retryBatches`,
		`One invalid event must not discard`,
		`A permanently invalid batch must not block later session events`,
		`conversationMirror.stop()`,
		`update?.type !== "text_delta"`,
		`part.source?.mediaType`,
		`part.source?.data`,
		`messages.flatMap(deliveryImages)`,
		`return [{ type: "image" as const, mimeType, data }]`,
		`pi.on("context"`,
		`event.messages.map(canonicalMessageImages)`,
		`[invalid image omitted]`,
		`conversationImages`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("conversation mirror omitted %q", want)
		}
	}
	if strings.Contains(source, "assistant_reasoning") || strings.Contains(source, `update?.type === "thinking_delta"`) {
		t.Error("conversation mirror still exports private reasoning")
	}
	if strings.Contains(source, `return [{ type: "image" as const, source:`) {
		t.Error("delivery images use Pi's non-canonical provider image shape")
	}
	for _, want := range []string{
		`Type.Literal("request")`,
		`Type.Literal("query")`,
		`Type.Literal("inform")`,
		`Type.Literal("join")`,
		`Type.Literal("notify")`,
		`one-way information`,
		`currentMessageId: activeMessageIds[0] ?? ""`,
		`claimId: claimKey`,
		`attempt: message.attempt`,
		`/renew`,
		`renewActiveLeases`,
		`completion_pending`,
		`recoverableCompletions`,
		`releaseStaleDeliveryAttempt`,
		`maxDeliveryBatchMessages`,
		`maxDeliveryResponseBytes`,
		`injectionPending`,
		`awaitInterrupts`,
		`awaitedMessageCounts`,
		`beginAwaitingMessages`,
		`finishAwaitingMessages`,
		`raceAgentWait`,
		`const interruptingAwait = awaitInterrupts.size !== 0`,
		`if (interruptingAwait) return`,
		`await/steer live-lock`,
		`ensureRegistered`,
		`registrationDelay`,
		`delegatedStatusPollMs`,
		`/delegated-status`,
		`🛖  ${workspaceTitle}  ·  🤖 ${value}`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("delivery reliability omitted %q", want)
		}
	}
	for _, want := range []string{
		`GALPON_PROTOCOL_GENERATION`,
		`protocolGeneration`,
		`/operations/direct`,
		`operationId: activeOperation.id`,
		`operationAttempt: activeOperation.attempt`,
		`/operations/claim`,
		`/receipts/take`,
		`receipt_persisted`,
		`/present`,
		`/todos/links/`,
		`/todos/settlements/`,
		`event.source === "extension"`,
		`communication maintenance is active`,
		`independent notification`,
		`Resume the same Pi objective`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("generation-2 Pi contract omitted %q", want)
		}
	}
	for _, unwanted := range []string{`conversationMirror.enqueue(conversationEvent("agent_start"))`, `conversationMirror.enqueue(conversationEvent("agent_end"))`, `conversationMirror.enqueue(conversationEvent("agent_settled"))`} {
		if strings.Contains(source, unwanted) {
			t.Errorf("conversation mirror exports unwanted event %q", unwanted)
		}
	}
}

type workDockHarnessResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func executeWorkDockHarness(t *testing.T, forceFailure bool) (workDockHarnessResult, []byte, error) {
	t.Helper()
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	path, err := filepath.Abs(filepath.Join("testdata", "work-dock-test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(t.TempDir(), "result.json")
	command := exec.Command(pi, "--list-models", "--extension", path)
	command.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"PI_CODING_AGENT_DIR="+t.TempDir(),
		"PI_TELEMETRY=0",
		"GALPON_WORK_DOCK_TEST_RESULT="+resultPath,
	)
	if forceFailure {
		command.Env = append(command.Env, "GALPON_WORK_DOCK_FORCE_FAILURE=1")
	}
	output, commandErr := command.CombinedOutput()
	data, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		t.Fatalf("Work Dock Pi harness did not write its result: %v\ncommand error: %v\n%s", readErr, commandErr, output)
	}
	var result workDockHarnessResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Work Dock Pi harness wrote an invalid result: %v\n%s", err, data)
	}
	return result, output, commandErr
}

func TestGenerationTwoPiContractHarness(t *testing.T) {
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	path, err := filepath.Abs(filepath.Join("testdata", "communication-v2-test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	resultPath := filepath.Join(root, "result.json")
	socketPath := filepath.Join(root, "galpon.sock")
	command := exec.Command(pi, "--list-models", "--extension", path)
	command.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"PI_CODING_AGENT_DIR="+t.TempDir(),
		"PI_TELEMETRY=0",
		"GALPON_SOCKET="+socketPath,
		"GALPON_AGENT_ID=agent",
		"GALPON_RUNTIME_ID=runtime",
		"GALPON_PROTOCOL_GENERATION=2",
		"GALPON_COMMUNICATION_V2_TEST_RESULT="+resultPath,
	)
	output, commandErr := command.CombinedOutput()
	data, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		t.Fatalf("communication v2 Pi harness did not write its result: %v\ncommand error: %v\n%s", readErr, commandErr, output)
	}
	var result workDockHarnessResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("communication v2 Pi harness wrote an invalid result: %v\n%s", err, data)
	}
	if commandErr != nil || !result.OK {
		t.Fatalf("communication v2 Pi harness failed: command error: %v; assertion: %s\n%s", commandErr, result.Error, output)
	}
}

func TestWorkDockExactLayoutLifecycleAndNoUI(t *testing.T) {
	result, output, commandErr := executeWorkDockHarness(t, false)
	if commandErr != nil || !result.OK {
		t.Fatalf("Work Dock Pi harness failed: command error: %v; assertion: %s\n%s", commandErr, result.Error, output)
	}
}

func TestOperationsCockpitUsesPublicBoundedPiTUI(t *testing.T) {
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	path, err := filepath.Abs(filepath.Join("testdata", "operations-cockpit-test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(t.TempDir(), "result.json")
	command := exec.Command(pi, "--list-models", "--extension", path)
	command.Env = append(os.Environ(),
		"PI_CODING_AGENT_DIR="+t.TempDir(),
		"PI_TELEMETRY=0",
		"GALPON_WORKSPACE_ID=workspace",
		"GALPON_OPERATIONS_COCKPIT_TEST_RESULT="+resultPath,
	)
	output, commandErr := command.CombinedOutput()
	data, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		t.Fatalf("Operations Pi harness did not write its result: %v\ncommand error: %v\n%s", readErr, commandErr, output)
	}
	var result workDockHarnessResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Operations Pi harness wrote an invalid result: %v\n%s", err, data)
	}
	if commandErr != nil || !result.OK {
		t.Fatalf("Operations Pi harness failed: command error: %v; assertion: %s\n%s", commandErr, result.Error, output)
	}
}

func TestWorkDockHarnessDetectsForcedAssertionFailure(t *testing.T) {
	result, output, _ := executeWorkDockHarness(t, true)
	if result.OK || !strings.Contains(result.Error, "forced Work Dock assertion failure") {
		t.Fatalf("Work Dock Pi harness missed a forced assertion failure: %#v\n%s", result, output)
	}
}

func TestCommandUsesExactDurableSessionWithProjectTrust(t *testing.T) {
	cfg := config.Config{StateDir: "/state", PiBin: "/bin/pi", PiProvider: "openai-codex", PiModel: "gpt-test"}
	args := Command(cfg, Assets{Extension: "/state/pi.ts", TodoExtension: "/state/todo/index.ts"}, model.Agent{ID: "agent-id", SessionID: "session-id", SessionPath: "/state/session.jsonl", Title: "Builder"}, "")
	for _, want := range []string{"/bin/pi", "--approve", "--provider", "openai-codex", "--session-id", "session-id", "--session-dir", filepath.Join("/state", "agents", "agent-id", "sessions"), "--extension", "/state/pi.ts", "/state/todo/index.ts", "--model", "gpt-test"} {
		if !slices.Contains(args, want) {
			t.Errorf("Pi command omitted %q: %#v", want, args)
		}
	}
	for _, unwanted := range []string{"--no-themes", "--theme", "/state/moon.json"} {
		if slices.Contains(args, unwanted) {
			t.Errorf("Pi command overrides configured themes with %q: %#v", unwanted, args)
		}
	}
}

func TestBackgroundCommandUsesPersistentRPCMode(t *testing.T) {
	cfg := config.Config{StateDir: "/state", PiBin: "/bin/pi", PiProvider: "openai-codex"}
	args := BackgroundCommand(cfg, Assets{Extension: "/state/pi.ts"}, model.Agent{ID: "agent-id", SessionID: "session-id", Title: "Worker"}, "")
	mode := slices.Index(args, "--mode")
	if mode < 0 || mode+1 >= len(args) || args[mode+1] != "rpc" || !slices.Contains(args, "session-id") {
		t.Fatalf("background Pi command = %#v", args)
	}
}

func TestCommandForksContextIntoExactDurableSession(t *testing.T) {
	cfg := config.Config{StateDir: "/state", PiBin: "/bin/pi", PiProvider: "openai-codex"}
	agent := model.Agent{ID: "agent-id", SessionID: "agent-id", ContextAgentID: "source", Title: "Builder"}
	args := Command(cfg, Assets{Extension: "/state/pi.ts"}, agent, "/source/session.jsonl")
	for _, want := range []string{"--fork", "/source/session.jsonl", "--session-id", "agent-id"} {
		if !slices.Contains(args, want) {
			t.Errorf("Pi fork command omitted %q: %#v", want, args)
		}
	}
}
