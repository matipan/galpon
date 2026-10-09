package nativeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/model"
)

type claudeDriver struct {
	options  launchOptions
	process  *childProcess
	id, path string
	mu       sync.Mutex
	current  string
}

func startClaude(ctx context.Context, options launchOptions) (driver, error) {
	binary, err := executable(options.Config.ClaudeBin, "claude")
	if err != nil {
		return nil, err
	}
	configDirectory := os.Getenv("CLAUDE_CONFIG_DIR")
	if configDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		configDirectory = filepath.Join(home, ".claude")
	}
	id := options.Agent.SessionID
	if id == "" {
		id = uuid.NewString()
	}
	projectName := "galpon-" + options.Agent.ID
	projectDirectory := filepath.Join(configDirectory, "projects", projectName)
	value := &claudeDriver{options: options, id: id, path: filepath.Join(projectDirectory, id+".jsonl")}
	mcpPath := filepath.Join(options.TempDir, "claude-mcp.json")
	mcpArgs := []string{"runtime", "mcp", options.Socket}
	if !options.Background {
		mcpArgs = append(mcpArgs, "channel")
	}
	if err := atomicJSON(mcpPath, map[string]any{"mcpServers": map[string]any{"galpon": map[string]any{"command": options.Executable, "args": mcpArgs}}}); err != nil {
		return nil, err
	}
	args := []string{"--mcp-config", mcpPath, "--allowedTools", "mcp__galpon__*", "--append-system-prompt", nativeSystemInstructions(options)}
	if options.Agent.SessionPath != "" {
		if err := restoreClaudeSession(options.Agent.SessionPath, value.path); err != nil {
			return nil, err
		}
		args = append(args, "--resume", id)
	} else if options.Source.SessionPath != "" {
		sourcePath := filepath.Join(projectDirectory, options.Source.SessionID+".jsonl")
		if err := restoreClaudeSession(options.Source.SessionPath, sourcePath); err != nil {
			return nil, err
		}
		args = append(args, "--resume", options.Source.SessionID, "--fork-session", "--session-id", id)
	} else if info, err := os.Stat(value.path); err == nil && info.Size() > 0 {
		args = append(args, "--resume", id)
	} else {
		args = append(args, "--session-id", id)
	}
	if options.Background {
		args = append(args, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--replay-user-messages")
	} else {
		settingsPath := filepath.Join(options.TempDir, "claude-hooks.json")
		hookCommand := shellQuote(options.Executable) + " runtime hook " + shellQuote(options.Socket)
		hooks := make(map[string]any)
		for _, name := range []string{"SessionStart", "UserPromptSubmit", "Stop", "StopFailure", "SessionEnd"} {
			hooks[name] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": hookCommand, "timeout": 10}}}}
		}
		if err := atomicJSON(settingsPath, map[string]any{"hooks": hooks}); err != nil {
			return nil, err
		}
		args = append(args, "--settings", settingsPath, "--dangerously-load-development-channels", "server:galpon")
	}
	command := exec.Command(binary, args...)
	command.Dir, command.Env = options.Directory, append(options.Environment, "CLAUDE_CODE_PROJECT_DIR_NAME="+projectName)
	command.Stderr = options.Errors
	if options.Background {
		command.Stdout = &jsonLineWriter{frame: func(data []byte) {
			var event map[string]any
			if json.Unmarshal(data, &event) != nil {
				return
			}
			value.mu.Lock()
			current := value.current
			value.mu.Unlock()
			switch event["type"] {
			case "user":
				emit(ctx, options.Events, nativeEvent{Kind: "input", ID: current, Input: contentText(objectValue(event["message"])["content"])})
			case "result":
				result := nativeEvent{Kind: "finish", ID: current, Final: stringValue(event["result"])}
				if event["is_error"] == true {
					result.Failure = "Claude Code failed: " + stringValue(event["subtype"])
				}
				emit(ctx, options.Events, result)
			}
		}}
	} else {
		command.Stdin, command.Stdout = options.Input, options.Output
	}
	value.process, err = startNativeChild(ctx, options, command, options.Background)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func (d *claudeDriver) Session() (string, string) { return d.id, d.path }

func (d *claudeDriver) Submit(ctx context.Context, input nativeInput, token string) (string, error) {
	if !d.options.Background {
		if len(input.Images) > 0 {
			return "", fmt.Errorf("foreground Claude Code channel deliveries accept text only; attach images in its native terminal or use a background agent")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case d.options.Channel <- map[string]any{"content": input.Text, "meta": map[string]any{"request_id": token, "sender": "galpon"}}:
			return token, nil
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.current = token
	var content any = input.Text
	if len(input.Images) > 0 {
		blocks := []any{map[string]any{"type": "text", "text": input.Text}}
		for _, image := range input.Images {
			if image.Data == "" {
				return "", fmt.Errorf("image %s has no saved data", image.ID)
			}
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": image.MimeType, "data": image.Data}})
		}
		content = blocks
	}
	d.process.mu.Lock()
	defer d.process.mu.Unlock()
	err := json.NewEncoder(d.process.input).Encode(map[string]any{"type": "user", "uuid": token, "session_id": d.id, "parent_tool_use_id": nil, "message": map[string]any{"role": "user", "content": content}})
	return token, err
}

func (d *claudeDriver) Terminal(ctx context.Context) error { return d.process.wait(ctx) }
func (d *claudeDriver) Close()                             { d.process.close() }

func restoreClaudeSession(source, target string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return fmt.Errorf("saved Claude conversation has no complete final record")
	}
	native, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	complete := native[:bytes.LastIndexByte(native, '\n')+1]
	if bytes.HasPrefix(complete, data) {
		if len(complete) == len(native) {
			return nil
		}
		return atomicFile(target, complete)
	}
	if !bytes.HasPrefix(data, complete) {
		return fmt.Errorf("native Claude conversation and its managed snapshot have different histories")
	}
	return atomicFile(target, data)
}

type jsonLineWriter struct {
	pending []byte
	frame   func([]byte)
}

func (w *jsonLineWriter) Write(data []byte) (int, error) {
	w.pending = append(w.pending, data...)
	for {
		index := bytes.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		w.frame(w.pending[:index])
		w.pending = w.pending[index+1:]
	}
	if len(w.pending) > 32<<20 {
		return 0, fmt.Errorf("native JSON frame exceeds 32 MiB")
	}
	return len(data), nil
}

func runtimeInstructions(agent model.Agent) string {
	role := ""
	if agent.Role != "" {
		role = "\nYour role: " + agent.Role
	}
	return "You are the durable Galpon agent " + agent.Title + ". Your harness is " + agent.Harness() + ". Work on the user's task yourself by default. Create agents, delegate, or contact another agent only when the user explicitly requests it. Use the Galpon MCP tools for durable coordination. Local task lists do not link Galpon assignments. For an inbound assignment, return the complete result in your final response, not in a message to the sender. Galpon records and routes that response. A delivered result is a notification, not a new assignment. Never create a circular wait. The harness and session belong to this agent; use Galpon to create or open a different agent." + role
}

func nativeSystemInstructions(options launchOptions) string {
	text := runtimeInstructions(options.Agent) + "\nWorking directory: " + options.Directory
	for _, worktree := range options.Worktrees {
		text += "\nPlacement worktree: " + worktree.Path + " (branch " + worktree.Branch + ")"
	}
	return text
}
