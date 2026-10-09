package nativeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type codexDriver struct {
	options           launchOptions
	process           *childProcess
	connection        *websocket.Conn
	cancel            context.CancelFunc
	mu                sync.Mutex
	pending           map[string]chan json.RawMessage
	closed            chan struct{}
	invalidSession    bool
	id, path, address string
}

func startCodex(ctx context.Context, options launchOptions) (driver, error) {
	binary, err := executable(options.Config.CodexBin, "codex")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	value := &codexDriver{options: options, cancel: cancel, pending: make(map[string]chan json.RawMessage), closed: make(chan struct{})}
	socket := filepath.Join(options.TempDir, "codex.sock")
	value.address = "unix://" + socket
	mcpArgs, _ := json.Marshal([]string{"runtime", "mcp", options.Socket})
	commandJSON, _ := json.Marshal(options.Executable)
	args := []string{"app-server", "--listen", value.address,
		"-c", "mcp_servers.galpon.command=" + string(commandJSON), "-c", "mcp_servers.galpon.args=" + string(mcpArgs),
		"-c", "mcp_servers.galpon.tool_timeout_sec=330"}
	for _, tool := range tools() {
		args = append(args, "-c", "mcp_servers.galpon.tools."+tool.Name+".approval_mode=\"approve\"")
	}
	command := exec.Command(binary, args...)
	command.Dir, command.Env = options.Directory, options.Environment
	command.Stdout, command.Stderr = options.Errors, options.Errors
	value.process, err = startNativeChild(ctx, options, command, true)
	if err != nil {
		cancel()
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			value.Close()
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-value.process.done:
			return nil, fmt.Errorf("native Codex app-server exited before opening its socket")
		case <-time.After(25 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("native Codex app-server did not open its socket")
		}
	}
	httpClient := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	value.connection, _, err = websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: httpClient})
	if err != nil {
		return nil, fmt.Errorf("connect to Codex app-server: %w", err)
	}
	value.connection.SetReadLimit(32 << 20)
	go value.read(ctx)
	if _, err := value.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "galpon", "version": "1.0.0"}, "capabilities": map[string]any{"experimentalApi": true}}); err != nil {
		return nil, err
	}
	if err := value.write(ctx, map[string]any{"method": "initialized"}); err != nil {
		return nil, err
	}
	configuration, err := value.call(ctx, "config/read", map[string]any{"cwd": options.Directory, "includeLayers": false})
	if err != nil {
		return nil, err
	}
	var configured struct {
		Config struct {
			DeveloperInstructions string `json:"developer_instructions"`
		} `json:"config"`
	}
	if err := json.Unmarshal(configuration, &configured); err != nil {
		return nil, err
	}
	instructions := nativeSystemInstructions(options)
	if configured.Config.DeveloperInstructions != "" {
		instructions = configured.Config.DeveloperInstructions + "\n\n" + instructions
	}
	params := map[string]any{"cwd": options.Directory, "developerInstructions": instructions}
	method := "thread/start"
	if options.Agent.SessionID == "" && options.Source.SessionPath == "" {
		// JSONL history keeps the complete conversation in the managed backup.
		params["historyMode"] = "legacy"
	}
	if options.Agent.SessionID != "" {
		method, params["threadId"] = "thread/resume", options.Agent.SessionID
		if options.Agent.SessionPath != "" {
			params["path"] = options.Agent.SessionPath
		}
	} else if options.Source.SessionPath != "" {
		method = "thread/fork"
		params["threadId"], params["path"] = options.Source.SessionID, options.Source.SessionPath
	}
	if method == "thread/resume" {
		if err := value.clearQueued(ctx, options.Agent.SessionID); err != nil {
			return nil, err
		}
	}
	result, err := value.call(ctx, method, params)
	if err != nil && method == "thread/resume" && options.Agent.SessionPath == "" && strings.Contains(err.Error(), "no rollout found") {
		delete(params, "threadId")
		params["historyMode"] = "legacy"
		result, err = value.call(ctx, "thread/start", params)
	}
	if err != nil {
		return nil, err
	}
	var response struct {
		Thread struct{ ID, Path string } `json:"thread"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return nil, err
	}
	value.mu.Lock()
	value.id, value.path = response.Thread.ID, response.Thread.Path
	value.mu.Unlock()
	if value.id == "" || value.path == "" {
		return nil, fmt.Errorf("native Codex did not return a durable thread ID and rollout path")
	}
	success = true
	return value, nil
}

func (d *codexDriver) Session() (string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.invalidSession {
		return "", ""
	}
	return d.id, d.path
}

func (d *codexDriver) write(ctx context.Context, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return d.connection.Write(ctx, websocket.MessageText, body)
}

func (d *codexDriver) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id := uuid.NewString()
	reply := make(chan json.RawMessage, 1)
	d.mu.Lock()
	d.pending[id] = reply
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.pending, id); d.mu.Unlock() }()
	if err := d.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.closed:
		return nil, fmt.Errorf("native Codex app-server connection closed during %s", method)
	case data := <-reply:
		var response struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, err
		}
		if response.Error != nil {
			return nil, fmt.Errorf("native Codex %s: %s", method, response.Error.Message)
		}
		return response.Result, nil
	}
}

func (d *codexDriver) read(ctx context.Context) {
	defer close(d.closed)
	for {
		_, data, err := d.connection.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				emit(ctx, d.options.Events, nativeEvent{Kind: "fatal", Failure: "Codex app-server connection closed: " + err.Error()})
			}
			return
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(data, &message) != nil {
			continue
		}
		if len(message.ID) > 0 && message.Method == "" {
			var id string
			_ = json.Unmarshal(message.ID, &id)
			d.mu.Lock()
			reply := d.pending[id]
			d.mu.Unlock()
			if reply != nil {
				select {
				case reply <- data:
				default:
				}
			}
			continue
		}
		if len(message.ID) > 0 {
			if d.options.Background {
				_ = d.write(ctx, map[string]any{"id": message.ID, "error": map[string]any{"code": -32000, "message": "This action requires the native terminal. Open the agent in Galpon and approve it there."}})
			}
			continue
		}
		d.mu.Lock()
		threadID := d.id
		d.mu.Unlock()
		if threadID == "" {
			continue
		}
		if message.Method == "thread/started" {
			thread := objectValue(message.Params["thread"])
			if id := stringValue(thread["id"]); id != "" && id != threadID && objectValue(thread["source"])["subAgent"] == nil {
				d.mu.Lock()
				d.invalidSession = true
				d.mu.Unlock()
				emit(ctx, d.options.Events, nativeEvent{Kind: "fatal", Failure: "The native session changed. Use Galpon to create or open another agent."})
				d.cancel()
				return
			}
		}
		if id := stringValue(message.Params["threadId"]); id != "" && id != threadID {
			continue
		}
		switch message.Method {
		case "turn/started":
			turn := objectValue(message.Params["turn"])
			emit(ctx, d.options.Events, nativeEvent{Kind: "start", ID: stringValue(turn["id"])})
		case "item/completed":
			item := objectValue(message.Params["item"])
			if item["type"] == "userMessage" {
				emit(ctx, d.options.Events, nativeEvent{Kind: "input", ID: stringValue(message.Params["turnId"]), Input: contentText(item["content"])})
			}
		case "turn/completed":
			turn := objectValue(message.Params["turn"])
			event := nativeEvent{Kind: "finish", ID: stringValue(turn["id"])}
			if turn["status"] != "completed" {
				event.Failure = fmt.Sprintf("Codex turn %s: %v", stringValue(turn["status"]), turn["error"])
			}
			emit(ctx, d.options.Events, event)
		}
	}
}

func (d *codexDriver) Submit(ctx context.Context, input nativeInput, token string) (string, error) {
	items := []any{map[string]any{"type": "text", "text": input.Text, "text_elements": []any{}}}
	for _, image := range input.Images {
		if image.Data == "" {
			return "", fmt.Errorf("image %s has no saved data", image.ID)
		}
		items = append(items, map[string]any{"type": "image", "url": "data:" + image.MimeType + ";base64," + image.Data})
	}
	queued, err := d.call(ctx, "thread/queue/add", map[string]any{"threadId": d.id, "input": items, "clientUserMessageId": "galpon:" + d.options.Agent.ID + ":" + token})
	if err != nil {
		return "", err
	}
	var submission struct {
		QueuedSubmission struct {
			ID string `json:"id"`
		} `json:"queuedSubmission"`
	}
	if err := json.Unmarshal(queued, &submission); err != nil {
		return "", err
	}
	if submission.QueuedSubmission.ID == "" {
		return "", fmt.Errorf("native Codex returned no queued submission ID")
	}
	// Queue admission starts a separate turn when the thread becomes idle.
	// turn/start can instead steer a native user turn that is already active.
	return token, nil
}

func (d *codexDriver) Terminal(ctx context.Context) error {
	if d.options.Background {
		return d.process.wait(ctx)
	}
	binary, err := executable(d.options.Config.CodexBin, "codex")
	if err != nil {
		return err
	}
	command := exec.Command(binary, "--remote", d.address, "resume", d.id)
	command.Dir = d.options.Directory
	command.Stdin, command.Stdout, command.Stderr = d.options.Input, d.options.Output, d.options.Errors
	process, err := startChild(ctx, command, false)
	if err != nil {
		return err
	}
	defer process.close()
	return process.wait(ctx)
}

func (d *codexDriver) Close() {
	d.cancel()
	if d.connection != nil {
		_ = d.connection.CloseNow()
	}
	if d.process != nil {
		d.process.close()
	}
}
