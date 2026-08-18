package piagent

import (
	"os"
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
	for _, name := range []string{"galpon_create_workspace", "galpon_create_agent", "galpon_cleanup_agents", "agent_ids", "galpon_send_agent", "galpon_await_agent", `registerCommand("finish"`, `/v1/runtime/agents/${agentId}/finish`, "ctx.shutdown()"} {
		if !strings.Contains(string(extension), name) {
			t.Errorf("extension omitted %s", name)
		}
	}
	for _, want := range []string{"provides optional tools", "roles and names do not have special built-in behavior", "only when the user requests coordination", "one queued cross-agent message per Pi turn", "Initial work request to queue before the new agent starts", "result then includes initialMessage", "Never create a synchronous wait cycle", "queued or delivered result is still pending", "settled without a final text response", "response closed before it completed", "only when the user explicitly asks for cleanup", "select the exact relevant IDs", "completed correlated result", "Do not use galpon_send_agent to return the current delivery result", "Galpón records and routes the final response automatically"} {
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
	} {
		if !strings.Contains(source, want) {
			t.Errorf("conversation mirror omitted %q", want)
		}
	}
	for _, want := range []string{
		`runtimeId, requestId: toolCallId, args`,
		`claimId: claimKey`,
		`attempt: message.attempt`,
		`/renew`,
		`renewActiveLeases`,
		`completion_pending`,
		`recoverableCompletions`,
		`maxDeliveryBatchMessages`,
		`maxDeliveryResponseBytes`,
		`injectionPending`,
		`awaitInterrupts`,
		`awaitedMessageIds`,
		`ensureRegistered`,
		`registrationDelay`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("delivery reliability omitted %q", want)
		}
	}
	for _, unwanted := range []string{`thinking_delta`, `thinking_start`, `thinking_end`, `conversationMirror.enqueue(conversationEvent("agent_start"))`, `conversationMirror.enqueue(conversationEvent("agent_end"))`, `conversationMirror.enqueue(conversationEvent("agent_settled"))`} {
		if strings.Contains(source, unwanted) {
			t.Errorf("conversation mirror exports unwanted event %q", unwanted)
		}
	}
}

func TestCommandUsesExactDurableSessionWithProjectTrust(t *testing.T) {
	cfg := config.Config{StateDir: "/state", PiBin: "/bin/pi", PiProvider: "openai-codex", PiModel: "gpt-test"}
	args := Command(cfg, Assets{Extension: "/state/pi.ts"}, model.Agent{ID: "agent-id", SessionID: "session-id", SessionPath: "/state/session.jsonl", Title: "Builder"}, "")
	for _, want := range []string{"/bin/pi", "--approve", "--provider", "openai-codex", "--session-id", "session-id", "--session-dir", filepath.Join("/state", "agents", "agent-id", "sessions"), "--extension", "/state/pi.ts", "--model", "gpt-test"} {
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
