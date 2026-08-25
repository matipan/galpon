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

func TestToolCatalogIsHarnessNeutralAndExplicitlyOmitsPiTodo(t *testing.T) {
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n")
	var output bytes.Buffer
	if err := (MCPServer{}).Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"galpon_create_agent", "galpon_send_agent", "galpon_await_agents", "codex", "claude"} {
		if !strings.Contains(text, want) {
			t.Errorf("MCP catalog omitted %q: %s", want, text)
		}
	}
	if strings.Contains(text, `"name":"todo"`) {
		t.Fatalf("MCP catalog claimed Pi TODO parity: %s", text)
	}
}
