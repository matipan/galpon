package nativeagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type bridgeCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	RequestID string         `json:"requestId"`
}

func localClient(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}}, Timeout: 6 * time.Minute}
}

func bridgeRequest(ctx context.Context, client *http.Client, path string, body any) (json.RawMessage, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://galpon"+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	value, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("native Galpon runtime: %s", strings.TrimSpace(string(value)))
	}
	if !json.Valid(value) {
		return nil, fmt.Errorf("native Galpon runtime returned invalid JSON")
	}
	return value, nil
}

// RunMCP serves a single native harness connection. Its private socket fixes the
// agent and runtime identities; neither is accepted from model tool arguments.
func RunMCP(ctx context.Context, socket string, channel bool, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client := localClient(socket)
	defer client.CloseIdleConnections()
	var writeMu sync.Mutex
	send := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return json.NewEncoder(output).Encode(value)
	}
	var polling sync.Once
	var calls sync.WaitGroup
	var callMu sync.Mutex
	cancels := make(map[string]context.CancelFunc)
	connection := uuid.NewString()
	reader := bufio.NewScanner(input)
	reader.Buffer(make([]byte, 64<<10), 16<<20)
	for reader.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(reader.Bytes(), &request); err != nil {
			return fmt.Errorf("decode MCP request: %w", err)
		}
		if len(request.ID) == 0 {
			if request.Method == "notifications/cancelled" {
				var value struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				_ = json.Unmarshal(request.Params, &value)
				callMu.Lock()
				if stop := cancels[string(value.RequestID)]; stop != nil {
					stop()
				}
				callMu.Unlock()
			}
			if channel && request.Method == "notifications/initialized" {
				polling.Do(func() {
					calls.Go(func() {
						for ctx.Err() == nil {
							value, err := bridgeRequest(ctx, client, "/channel", nil)
							if err != nil {
								cancel()
								return
							}
							if string(value) != "null" {
								if send(map[string]any{"jsonrpc": "2.0", "method": "notifications/claude/channel", "params": value}) != nil {
									cancel()
									return
								}
							}
						}
					})
				})
			}
			continue
		}
		callContext, stop := context.WithCancel(ctx)
		callMu.Lock()
		cancels[string(request.ID)] = stop
		callMu.Unlock()
		calls.Go(func() {
			defer stop()
			defer func() { callMu.Lock(); delete(cancels, string(request.ID)); callMu.Unlock() }()
			var result any
			var callErr error
			switch request.Method {
			case "initialize":
				var params struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				_ = json.Unmarshal(request.Params, &params)
				capabilities := map[string]any{"tools": map[string]any{}}
				if channel {
					capabilities["experimental"] = map[string]any{"claude/channel": map[string]any{}}
				}
				result = map[string]any{"protocolVersion": params.ProtocolVersion, "capabilities": capabilities, "serverInfo": map[string]any{"name": "galpon", "version": "1.0.0"}}
			case "ping":
				result = map[string]any{}
			case "tools/list":
				result = map[string]any{"tools": tools()}
			case "tools/call":
				var params bridgeCall
				callErr = json.Unmarshal(request.Params, &params)
				if callErr == nil {
					params.RequestID = connection + ":" + string(request.ID)
					result, callErr = bridgeRequest(callContext, client, "/tool", params)
				}
				if callErr != nil {
					result = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": callErr.Error()}}}
					callErr = nil
				}
			case "resources/list":
				result = map[string]any{"resources": []any{}}
			case "resources/templates/list":
				result = map[string]any{"resourceTemplates": []any{}}
			case "prompts/list":
				result = map[string]any{"prompts": []any{}}
			default:
				callErr = fmt.Errorf("unsupported MCP method %q", request.Method)
			}
			response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
			if callErr != nil {
				delete(response, "result")
				response["error"] = map[string]any{"code": -32601, "message": callErr.Error()}
			}
			if send(response) != nil {
				cancel()
			}
		})
	}
	cancel()
	calls.Wait()
	return reader.Err()
}

func RunHook(ctx context.Context, socket string, input io.Reader, output io.Writer) error {
	var value json.RawMessage
	if err := json.NewDecoder(io.LimitReader(input, 16<<20)).Decode(&value); err != nil {
		return err
	}
	client := localClient(socket)
	defer client.CloseIdleConnections()
	result, err := bridgeRequest(ctx, client, "/hook", value)
	if err != nil {
		return err
	}
	_, err = output.Write(append(result, '\n'))
	return err
}
