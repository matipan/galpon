package harnessmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestConnectionLocalJSONRPCIDsGetGlobalDurablePrefixes(t *testing.T) {
	id := json.RawMessage(`1`)
	first := durableRequestID("connection-a", id)
	second := durableRequestID("connection-b", id)
	if first == second || first != durableRequestID("connection-a", id) {
		t.Fatalf("durable request IDs = %q, %q", first, second)
	}
}

func TestMutationIdentitySurvivesMCPRestartAndJSONRPCIDChanges(t *testing.T) {
	first := durableToolRequestID("delivery-message", "send_agent", map[string]any{"agent": "worker", "prompt": "work"}, json.RawMessage(`1`))
	second := durableToolRequestID("delivery-message", "send_agent", map[string]any{"prompt": "work", "agent": "worker"}, json.RawMessage(`99`))
	if first != second {
		t.Fatalf("restart changed mutation identity: %q != %q", first, second)
	}
	independent := durableToolRequestID("other-delivery", "send_agent", map[string]any{"agent": "worker", "prompt": "work"}, json.RawMessage(`1`))
	if first == independent {
		t.Fatal("different durable invocation scopes collided")
	}
	keyedFirst := durableToolRequestID("delivery-message", "send_agent", map[string]any{"agent": "worker", "prompt": "one", "idempotency_key": "operation"}, json.RawMessage(`1`))
	keyedConflict := durableToolRequestID("delivery-message", "send_agent", map[string]any{"agent": "worker", "prompt": "two", "idempotency_key": "operation"}, json.RawMessage(`2`))
	if keyedFirst != keyedConflict {
		t.Fatal("stable operation key did not preserve the receipt identity")
	}
}

func TestToolCatalogIsHarnessNeutralAndExplicitlyOmitsPiTodo(t *testing.T) {
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n")
	var output bytes.Buffer
	if err := (MCPServer{}).Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"galpon_create_agent", "galpon_send_agent", "galpon_report_progress", "galpon_await_agents", "codex", "claude"} {
		if !strings.Contains(text, want) {
			t.Errorf("MCP catalog omitted %q: %s", want, text)
		}
	}
	if strings.Contains(text, `"name":"todo"`) {
		t.Fatalf("MCP catalog claimed Pi TODO parity: %s", text)
	}
}
