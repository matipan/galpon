package harnessmcp

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/app"
)

type MCPServer struct {
	Client           *app.Client
	AgentID          string
	RuntimeID        string
	Capability       string
	InvocationPrefix string
	CurrentMessageID string
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (s MCPServer) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if s.InvocationPrefix == "" {
		s.InvocationPrefix = uuid.NewString()
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request mcpRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		result, err := s.handle(ctx, request)
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		if err != nil {
			response["error"] = map[string]any{"code": -32000, "message": err.Error()}
		} else {
			response["result"] = result
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s MCPServer) handle(ctx context.Context, request mcpRequest) (any, error) {
	switch request.Method {
	case "initialize":
		return map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "galpon", "version": "1"}}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools()}, nil
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return nil, fmt.Errorf("invalid tool arguments")
		}
		name := strings.TrimPrefix(params.Name, "galpon_")
		var value any
		requestID := durableRequestID(s.InvocationPrefix, request.ID)
		if err := s.Client.RuntimeTool(ctx, name, s.AgentID, s.RuntimeID, s.Capability, requestID, s.CurrentMessageID, params.Arguments, &value); err != nil {
			return map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}, nil
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}, nil
	default:
		return nil, fmt.Errorf("method not found")
	}
}

func durableRequestID(prefix string, id json.RawMessage) string {
	return prefix + ":" + fmt.Sprintf("%x", sha256.Sum256(id))
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) != 0 {
		out["required"] = required
	}
	return out
}

func mcpTools() []map[string]any {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return []map[string]any{
		{"name": "galpon_list_repositories", "description": "List repositories managed by Galpon.", "inputSchema": objectSchema(map[string]any{})},
		{"name": "galpon_list_workspaces", "description": "List durable Galpon workspaces.", "inputSchema": objectSchema(map[string]any{})},
		{"name": "galpon_list_agents", "description": "List durable Galpon agents and runtime state.", "inputSchema": objectSchema(map[string]any{})},
		{"name": "galpon_create_workspace", "description": "Create a durable workspace for future foreground agents.", "inputSchema": objectSchema(map[string]any{"title": text("Workspace title")}, "title")},
		{"name": "galpon_create_agent", "description": "Create and start a durable background agent. The harness defaults to the Galpon default.", "inputSchema": objectSchema(map[string]any{
			"title": text("Agent title"), "workspace": text("Workspace ID or exact title"), "role": text("Optional role"), "prompt": text("Optional initial work request"),
			"harness":       map[string]any{"type": "string", "enum": []string{"pi", "codex", "claude"}, "description": "Agent harness"},
			"context_agent": text("Existing same-harness context source; only Pi context forks are currently supported"), "repository": text("Primary repository ID or title"), "remote": text("Primary source remote"), "ref": text("Primary source reference"),
			"placement_agent": text("Agent whose placement is copied"), "share": map[string]any{"type": "boolean"}, "cwd": text("Existing absolute external directory"),
			"secondary":   map[string]any{"type": "array", "maxItems": 7, "items": objectSchema(map[string]any{"repository": text("Secondary repository ID or title"), "remote": text("Source remote"), "ref": text("Source reference")}, "repository")},
			"result_mode": map[string]any{"type": "string", "enum": []string{"join", "notify"}},
		}, "title", "workspace")},
		{"name": "galpon_send_agent", "description": "Send durable work to another agent through the harness-neutral Galpon queue.", "inputSchema": objectSchema(map[string]any{
			"agent": text("Target agent ID or exact title"), "prompt": text("Message text"), "act": map[string]any{"type": "string", "enum": []string{"request", "query", "inform"}}, "result_mode": map[string]any{"type": "string", "enum": []string{"join", "notify"}},
		}, "agent", "prompt")},
		{"name": "galpon_read_message", "description": "Read durable message state.", "inputSchema": objectSchema(map[string]any{"message_id": text("Message ID")}, "message_id")},
		{"name": "galpon_await_agent", "description": "Wait for one durable message to settle.", "inputSchema": objectSchema(map[string]any{"message_id": text("Message ID"), "timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 300}}, "message_id")},
		{"name": "galpon_await_agents", "description": "Wait for several durable messages.", "inputSchema": objectSchema(map[string]any{"message_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 16}, "return_when": map[string]any{"type": "string", "enum": []string{"any", "all"}}, "timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 300}}, "message_ids", "return_when")},
		{"name": "galpon_cleanup_agents", "description": "Permanently remove selected descendant agents.", "inputSchema": objectSchema(map[string]any{"agent_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1}}, "agent_ids")},
	}
}
