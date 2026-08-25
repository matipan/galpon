package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

const (
	Pi     = "pi"
	Codex  = "codex"
	Claude = "claude"
)

var supported = []string{Pi, Codex, Claude}

func Supported() []string { return append([]string(nil), supported...) }

func Normalize(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "cloud" {
		value = Claude
	}
	for _, candidate := range supported {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("unsupported agent harness %q; choose pi, codex, or claude", value)
}

func Default(cfg config.Config) (string, error) {
	value := strings.TrimSpace(cfg.DefaultHarness)
	if value == "" {
		value = Pi
	}
	return Normalize(value)
}

func Binary(cfg config.Config, id string) (string, error) {
	id, err := Normalize(id)
	if err != nil {
		return "", err
	}
	switch id {
	case Pi:
		return cfg.PiBin, nil
	case Codex:
		return cfg.CodexBin, nil
	default:
		return cfg.ClaudeBin, nil
	}
}

func RequireAvailable(cfg config.Config, id string) (string, error) {
	id, err := Normalize(id)
	if err != nil {
		return "", err
	}
	binary, _ := Binary(cfg, id)
	path, err := exec.LookPath(strings.TrimSpace(binary))
	if err != nil {
		return "", fmt.Errorf("%s harness executable %q is not available in PATH; set %s", id, binary, binaryEnvironment(id))
	}
	if status := AuthenticationStatus(id, path); status == "unauthenticated" {
		return "", fmt.Errorf("%s harness is installed but not authenticated; run %s", id, map[string]string{Codex: "codex login", Claude: "claude auth login"}[id])
	}
	return path, nil
}

type authenticationCacheEntry struct {
	status  string
	checked time.Time
}

var authenticationCache sync.Map

func AuthenticationStatus(id, executable string) string {
	if id == Pi {
		return "provider"
	}
	key := id + "\x00" + executable
	if cached, ok := authenticationCache.Load(key); ok {
		entry := cached.(authenticationCacheEntry)
		if time.Since(entry.checked) < 30*time.Second {
			return entry.status
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var command *exec.Cmd
	switch id {
	case Codex:
		command = exec.CommandContext(ctx, executable, "login", "status")
	case Claude:
		command = exec.CommandContext(ctx, executable, "auth", "status", "--json")
	default:
		return "unknown"
	}
	command.Env = authenticationProbeEnvironment(id)
	output, err := command.CombinedOutput()
	status := "unknown"
	bounded := string(output)
	if len(bounded) > 8<<10 {
		bounded = bounded[:8<<10]
	}
	if id == Codex {
		text := strings.ToLower(strings.TrimSpace(bounded))
		if err == nil && strings.Contains(text, "logged in") {
			status = "authenticated"
		} else if strings.Contains(text, "not logged") || strings.Contains(text, "login required") {
			status = "unauthenticated"
		}
	} else {
		var value struct {
			LoggedIn bool `json:"loggedIn"`
		}
		jsonText := bounded
		if start, end := strings.Index(jsonText, "{"), strings.LastIndex(jsonText, "}"); start >= 0 && end >= start {
			jsonText = jsonText[start : end+1]
		}
		if json.Unmarshal([]byte(jsonText), &value) == nil {
			if value.LoggedIn {
				status = "authenticated"
			} else {
				status = "unauthenticated"
			}
		}
	}
	authenticationCache.Store(key, authenticationCacheEntry{status: status, checked: time.Now()})
	return status
}

func authenticationProbeEnvironment(id string) []string {
	allowed := map[string]bool{"HOME": true, "PATH": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true, "XDG_CACHE_HOME": true, "CODEX_HOME": true, "CLAUDE_CONFIG_DIR": true}
	if id == Codex {
		allowed["OPENAI_API_KEY"] = true
	}
	if id == Claude {
		allowed["ANTHROPIC_API_KEY"] = true
	}
	var out []string
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && (allowed[name] || strings.HasPrefix(name, "LC_")) {
			out = append(out, entry)
		}
	}
	return out
}

func Catalog(cfg config.Config) (string, []model.HarnessInfo) {
	defaultID, err := Default(cfg)
	if err != nil {
		defaultID = Pi
	}
	out := make([]model.HarnessInfo, 0, len(supported))
	for _, id := range supported {
		binary, _ := Binary(cfg, id)
		path, lookupErr := exec.LookPath(strings.TrimSpace(binary))
		info := model.HarnessInfo{ID: id, Label: label(id), Executable: binary, Available: lookupErr == nil, Authentication: "unknown"}
		if lookupErr == nil {
			info.Executable = path
			info.Authentication = AuthenticationStatus(id, path)
			if info.Authentication == "unauthenticated" {
				info.Available = false
			}
		}
		switch id {
		case Pi:
			info.ContextFork = true
			info.TodoIntegration = true
			info.Guidance = "Pi authentication and model access use the configured Pi provider."
		case Codex:
			info.Guidance = "Codex CLI must be authenticated. Run codex login if a model request reports an authentication error."
		case Claude:
			info.Guidance = "Claude Code must be authenticated. Run claude auth login; a Claude subscription or API access can be required."
		}
		if lookupErr != nil {
			info.Guidance = fmt.Sprintf("Install %s or set %s to its executable path.", info.Label, binaryEnvironment(id))
		} else if info.Authentication == "unauthenticated" {
			info.Guidance = fmt.Sprintf("%s is installed but not authenticated. Run %s.", info.Label, map[string]string{Codex: "codex login", Claude: "claude auth login"}[id])
		}
		out = append(out, info)
	}
	return defaultID, out
}

func label(id string) string {
	switch id {
	case Pi:
		return "Pi"
	case Codex:
		return "OpenAI Codex CLI"
	default:
		return "Anthropic Claude Code"
	}
}

func binaryEnvironment(id string) string {
	switch id {
	case Pi:
		return "GALPON_PI_BIN"
	case Codex:
		return "GALPON_CODEX_BIN"
	default:
		return "GALPON_CLAUDE_BIN"
	}
}
