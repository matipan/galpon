package harness

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/matipan/galpon/internal/config"
)

const maxStructuredEvent = 4 << 20

func Command(cfg config.Config, executable, id, cwd, sessionID, systemInstructions string, resume bool, additionalDirs []string) ([]string, error) {
	id, err := Normalize(id)
	if err != nil {
		return nil, err
	}
	binary, err := Binary(cfg, id)
	if err != nil {
		return nil, err
	}
	switch id {
	case Codex:
		mcpCommand := "mcp_servers.galpon.command=" + strconv.Quote(executable)
		mcpArgs := `mcp_servers.galpon.args=["mcp","serve"]`
		args := []string{binary, "--sandbox", "workspace-write", "--ask-for-approval", "never", "-c", mcpCommand, "-c", mcpArgs, "-c", "developer_instructions=" + strconv.Quote(systemInstructions)}
		if cfg.CodexModel != "" {
			args = append(args, "--model", cfg.CodexModel)
		}
		args = append(args, "--cd", cwd)
		for _, dir := range additionalDirs {
			args = append(args, "--add-dir", dir)
		}
		args = append(args, "exec")
		if resume && sessionID != "" {
			args = append(args, "resume", "--json", "--skip-git-repo-check", sessionID, "-")
		} else {
			args = append(args, "--json", "--color", "never", "--skip-git-repo-check", "-")
		}
		return args, nil
	case Claude:
		mcp, marshalErr := json.Marshal(map[string]any{"mcpServers": map[string]any{"galpon": map[string]any{"type": "stdio", "command": executable, "args": []string{"mcp", "serve"}}}})
		if marshalErr != nil {
			return nil, marshalErr
		}
		args := []string{binary, "-p", "--input-format", "text", "--output-format", "stream-json", "--verbose", "--permission-mode", "acceptEdits", "--append-system-prompt", systemInstructions, "--strict-mcp-config", "--mcp-config", string(mcp)}
		if cfg.ClaudeModel != "" {
			args = append(args, "--model", cfg.ClaudeModel)
		}
		for _, dir := range additionalDirs {
			args = append(args, "--add-dir", dir)
		}
		if resume && sessionID != "" {
			args = append(args, "--resume", sessionID)
		} else if sessionID != "" {
			args = append(args, "--session-id", sessionID)
		}
		return args, nil
	default:
		return nil, fmt.Errorf("native Pi runtime adapter is required")
	}
}

type StructuredResult struct {
	SessionID string
	Response  string
}

func ParseStructured(id string, input io.Reader) (StructuredResult, error) {
	id, err := Normalize(id)
	if err != nil {
		return StructuredResult{}, err
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), maxStructuredEvent)
	var out StructuredResult
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return out, fmt.Errorf("decode %s structured output: %w", id, err)
		}
		switch id {
		case Codex:
			if event["type"] == "thread.started" {
				out.SessionID, _ = event["thread_id"].(string)
			}
			if event["type"] == "item.completed" {
				item, _ := event["item"].(map[string]any)
				if item["type"] == "agent_message" {
					if text, ok := item["text"].(string); ok {
						out.Response = text
					}
				}
			}
		case Claude:
			if session, ok := event["session_id"].(string); ok && session != "" {
				out.SessionID = session
			}
			if event["type"] == "result" {
				if text, ok := event["result"].(string); ok {
					out.Response = text
				}
				if failed, _ := event["is_error"].(bool); failed {
					return out, fmt.Errorf("structured Claude Code error: %s", bounded(textValue(event["result"]), 4096))
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("read %s structured output: %w", id, err)
	}
	if strings.TrimSpace(out.Response) == "" {
		return out, fmt.Errorf("%s returned no final response", id)
	}
	return out, nil
}

func SessionPath(cfg config.Config, agentID, id, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	name := fmt.Sprintf("%s-%x.json", id, sha256.Sum256([]byte(sessionID)))
	return filepath.Join(cfg.StateDir, "agents", agentID, "sessions", name)
}

func bounded(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func textValue(value any) string {
	text, _ := value.(string)
	return text
}
