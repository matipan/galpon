package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/matipan/galpon/internal/model"
)

const (
	configMarker             = "# Galpon command center"
	configEndMarker          = "# End Galpon command center"
	agentReportSource        = "galpon"
	contextualMetadataSource = "galpon-context"
	contextualMetadataTTL    = "10000"
)

type Adapter struct{ Bin string }

func (a Adapter) Name() string { return "herdr" }
func (a Adapter) Context() string {
	if value := strings.TrimSpace(os.Getenv("HERDR_SESSION")); value != "" {
		return value
	}
	return "default"
}

func (a Adapter) OpenTerminal(ctx context.Context, ws model.Workspace, wt model.Worktree, label string, command []string) (string, error) {
	workspaceID, created, err := a.ensureWorkspace(ctx, ws, wt, true)
	if err != nil {
		return "", err
	}
	var paneID string
	if created {
		out, err := a.run(ctx, "pane", "list", "--workspace", workspaceID)
		if err != nil {
			return "", err
		}
		paneID = parseID(out, "pane_id")
	} else {
		out, err := a.run(ctx, "tab", "create", "--workspace", workspaceID, "--cwd", wt.Path, "--label", firstNonEmpty(label, ws.Title), "--focus")
		if err != nil {
			return "", err
		}
		paneID = parseID(out, "pane_id")
	}
	if len(command) > 0 {
		if paneID == "" {
			return "", fmt.Errorf("herdr did not return a pane ID")
		}
		args := append([]string{"pane", "run", paneID}, command...)
		if _, err := a.run(ctx, args...); err != nil {
			return "", err
		}
	}
	if paneID == "" {
		return "", fmt.Errorf("herdr did not return a terminal pane ID")
	}
	if label == "" {
		label = ws.Title
	}
	if _, err := a.run(ctx, "pane", "rename", paneID, label); err != nil {
		return "", err
	}
	if _, err := a.run(ctx, "pane", "report-metadata", paneID, "--source", "galpon-display", "--title", label); err != nil {
		return "", err
	}
	paneInfo, err := a.run(ctx, "pane", "get", paneID)
	if err != nil {
		return "", err
	}
	if tabID := parseID(paneInfo, "tab_id"); tabID != "" {
		if _, err := a.run(ctx, "tab", "rename", tabID, label); err != nil {
			return "", err
		}
	}
	return workspaceID, nil
}

func (a Adapter) OpenAgent(ctx context.Context, ws model.Workspace, wt model.Worktree, agent model.Agent, command []string, focus bool) (string, string, bool, error) {
	workspaceID, created, err := a.ensureWorkspace(ctx, ws, wt, focus)
	if err != nil {
		return "", "", false, err
	}
	paneID := ""
	newPane := false
	if agent.Renderer == a.Name() && agent.RendererContext == a.Context() {
		paneID = strings.TrimSpace(agent.RendererID)
		if paneID != "" {
			if _, err := a.run(ctx, "pane", "get", paneID); err != nil {
				paneID = ""
			}
		}
	}
	if paneID == "" && created {
		out, err := a.run(ctx, "pane", "list", "--workspace", workspaceID)
		if err != nil {
			return "", "", false, err
		}
		paneID = parseID(out, "pane_id")
		newPane = paneID != ""
	}
	if paneID == "" {
		focusFlag := "--no-focus"
		if focus {
			focusFlag = "--focus"
		}
		out, err := a.run(ctx, "tab", "create", "--workspace", workspaceID, "--cwd", wt.Path, "--label", agent.Title, focusFlag)
		if err != nil {
			return "", "", false, err
		}
		paneID = parseID(out, "pane_id")
		newPane = paneID != ""
	}
	if paneID == "" {
		return "", "", false, fmt.Errorf("herdr did not return an agent pane ID")
	}
	if _, err := a.run(ctx, "pane", "rename", paneID, agent.Title); err != nil {
		return "", "", false, err
	}
	if _, err := a.run(ctx, "pane", "report-metadata", paneID, "--source", "galpon-display", "--title", agent.Title, "--display-agent", agent.Title); err != nil {
		return "", "", false, err
	}
	paneInfo, err := a.run(ctx, "pane", "get", paneID)
	if err != nil {
		return "", "", false, err
	}
	tabID := parseID(paneInfo, "tab_id")
	if tabID == "" {
		return "", "", false, fmt.Errorf("herdr did not return the agent tab ID")
	}
	if focus {
		if _, err := a.run(ctx, "workspace", "focus", workspaceID); err != nil {
			return "", "", false, err
		}
		if _, err := a.run(ctx, "tab", "focus", tabID); err != nil {
			return "", "", false, err
		}
		if err := a.focusPane(ctx, paneID); err != nil {
			return "", "", false, err
		}
	}
	start := newPane || (agent.Status != "running" && agent.Status != "starting" && agent.Status != "idle")
	if start && len(command) > 0 {
		args := append([]string{"pane", "run", paneID}, command...)
		if _, err := a.run(ctx, args...); err != nil {
			return "", "", false, err
		}
	}
	// pane run resets a new tab to its generated label. Name the tab only after
	// the agent command starts so the durable visible name is the agent title.
	if _, err := a.run(ctx, "tab", "rename", tabID, agent.Title); err != nil {
		return "", "", false, err
	}
	return workspaceID, paneID, start, nil
}

func (a Adapter) CloseAgent(ctx context.Context, agent model.Agent) error {
	if agent.Renderer != a.Name() || agent.RendererContext != a.Context() || strings.TrimSpace(agent.RendererID) == "" {
		return nil
	}
	// Renderer IDs are disposable view references. If the pane is already gone,
	// the durable agent can still be cleaned.
	paneInfo, err := a.run(ctx, "pane", "get", agent.RendererID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil
		}
		return err
	}
	if tabID := parseID(paneInfo, "tab_id"); tabID != "" {
		_, err = a.run(ctx, "tab", "close", tabID)
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "last tab") {
			if workspaceID := parseID(paneInfo, "workspace_id"); workspaceID != "" {
				_, err = a.run(ctx, "workspace", "close", workspaceID)
			}
		}
		return err
	}
	_, err = a.run(ctx, "pane", "close", agent.RendererID)
	return err
}

func (a Adapter) ReportAgent(ctx context.Context, agent model.Agent, status, message string) error {
	if !a.canReport(agent) {
		return nil
	}
	metadataErr := a.clearContextualMetadata(ctx, agent)
	if status == "stopped" {
		_, err := a.run(ctx, "pane", "release-agent", agent.RendererID, "--source", agentReportSource, "--agent", agent.Title)
		return errors.Join(metadataErr, err)
	}
	herdrStatus := "unknown"
	switch status {
	case "idle":
		herdrStatus = "idle"
	case "starting", "running":
		herdrStatus = "working"
	case "failed":
		herdrStatus = "blocked"
	}
	return errors.Join(metadataErr, a.reportAgentState(ctx, agent, herdrStatus, message))
}

// ReportContextualActivity projects durable delegated work only onto the
// current managed pane. The Pi extension calls this method only while Pi is
// idle. Galpon never reports done; Herdr derives done from its seen state.
func (a Adapter) ReportContextualActivity(ctx context.Context, agent model.Agent, active int) error {
	if !a.canReport(agent) {
		return nil
	}
	if active <= 0 {
		return errors.Join(a.clearContextualMetadata(ctx, agent), a.reportAgentState(ctx, agent, "idle", ""))
	}
	label := fmt.Sprintf("delegating · %d active", active)
	_, metadataErr := a.run(ctx, "pane", "report-metadata", agent.RendererID,
		"--source", contextualMetadataSource,
		"--agent", agent.Title,
		"--applies-to-source", agentReportSource,
		"--state-label", "working="+label,
		"--ttl-ms", contextualMetadataTTL)
	return errors.Join(metadataErr, a.reportAgentState(ctx, agent, "working", label))
}

func (a Adapter) canReport(agent model.Agent) bool {
	return agent.Renderer == a.Name() && agent.RendererContext == a.Context() && strings.TrimSpace(agent.RendererID) != ""
}

func (a Adapter) clearContextualMetadata(ctx context.Context, agent model.Agent) error {
	_, err := a.run(ctx, "pane", "report-metadata", agent.RendererID,
		"--source", contextualMetadataSource,
		"--agent", agent.Title,
		"--applies-to-source", agentReportSource,
		"--clear-state-labels")
	return err
}

func (a Adapter) reportAgentState(ctx context.Context, agent model.Agent, state, message string) error {
	args := []string{"pane", "report-agent", agent.RendererID, "--source", agentReportSource, "--agent", agent.Title, "--state", state, "--agent-session-id", agent.SessionID}
	if agent.SessionPath != "" {
		args = append(args, "--agent-session-path", agent.SessionPath)
	}
	if message != "" {
		args = append(args, "--message", message)
	}
	_, err := a.run(ctx, args...)
	return err
}

func (a Adapter) ensureWorkspace(ctx context.Context, ws model.Workspace, wt model.Worktree, focus bool) (string, bool, error) {
	workspaceID := ""
	if ws.Renderer == a.Name() && ws.RendererContext == a.Context() {
		workspaceID = strings.TrimSpace(ws.RendererID)
	}
	if workspaceID != "" {
		if _, err := a.run(ctx, "workspace", "get", workspaceID); err != nil {
			workspaceID = ""
		}
	}
	if workspaceID == "" {
		focusFlag := "--no-focus"
		if focus {
			focusFlag = "--focus"
		}
		out, err := a.run(ctx, "workspace", "create", "--cwd", wt.Path, "--label", ws.Title, focusFlag)
		if err != nil {
			return "", false, err
		}
		workspaceID = parseID(out, "workspace_id")
		if workspaceID == "" {
			return "", false, fmt.Errorf("herdr did not return the new workspace ID: %s", strings.TrimSpace(out))
		}
		return workspaceID, true, nil
	}
	if focus {
		if _, err := a.run(ctx, "workspace", "focus", workspaceID); err != nil {
			return "", false, err
		}
	}
	return workspaceID, false, nil
}

func (a Adapter) focusPane(ctx context.Context, target string) error {
	opposite := map[string]string{"left": "right", "right": "left", "up": "down", "down": "up"}
	for _, direction := range []string{"left", "right", "up", "down"} {
		out, err := a.run(ctx, "pane", "neighbor", "--pane", target, "--direction", direction)
		if err != nil {
			continue
		}
		neighbor := parseID(out, "neighbor_pane_id")
		if neighbor == "" {
			continue
		}
		_, err = a.run(ctx, "pane", "focus", "--pane", neighbor, "--direction", opposite[direction])
		return err
	}
	// A pane without a neighbor is the only pane in its tab. Focusing the
	// workspace already focused it, so no second pane operation is necessary.
	return nil
}

func (a Adapter) run(ctx context.Context, args ...string) (string, error) {
	commandArgs := args
	if rendererContext := a.Context(); rendererContext != "default" {
		commandArgs = append([]string{"--session", rendererContext}, args...)
	}
	cmd := exec.CommandContext(ctx, a.Bin, commandArgs...)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("herdr %s: %w: %s", strings.Join(commandArgs, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func parseID(output, key string) string {
	var value any
	if json.Unmarshal([]byte(output), &value) != nil {
		return strings.TrimSpace(output)
	}
	return findStringKey(value, key)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func findStringKey(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if found, ok := typed[key].(string); ok && strings.TrimSpace(found) != "" {
			return found
		}
		for _, childKey := range []string{"result", "workspace", "root_pane", "pane", "workspaces", "panes"} {
			if child, ok := typed[childKey]; ok {
				if found := findStringKey(child, key); found != "" {
					return found
				}
			}
		}
		for _, child := range typed {
			if found := findStringKey(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findStringKey(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func PopupConfig(binary string) string {
	if strings.TrimSpace(binary) == "" {
		binary = "galpon"
	}
	commands := []struct {
		key  string
		args []string
	}{
		{key: "ctrl+k"},
		{key: "ctrl+n", args: []string{"herdr", "new-agent"}},
		{key: "ctrl+o", args: []string{"herdr", "operations"}},
	}
	var output strings.Builder
	for index, binding := range commands {
		if index > 0 {
			output.WriteByte('\n')
		}
		command := shellQuote(binary)
		for _, arg := range binding.args {
			command += " " + shellQuote(arg)
		}
		fmt.Fprintf(&output, "[[keys.command]]\nkey = %s\ntype = \"popup\"\ncommand = %s\nwidth = \"88%%\"\nheight = \"88%%\"\n", strconv.Quote(binding.key), strconv.Quote(command))
	}
	return output.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func managedPopupBlock(binary string) string {
	return configMarker + "\n" + PopupConfig(binary) + configEndMarker + "\n"
}

func InstallPopup(configPath, binary string) error {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return err
	}
	content := removeManagedPopupBlocks(string(data))
	content = strings.TrimRight(content, "\n")
	if content != "" {
		content += "\n\n"
	}
	content += managedPopupBlock(binary)
	temp := configPath + ".galpon.tmp"
	if err := os.WriteFile(temp, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, configPath); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

func removeManagedPopupBlocks(content string) string {
	lines := strings.SplitAfter(content, "\n")
	var output strings.Builder
	for index := 0; index < len(lines); {
		if strings.TrimSpace(lines[index]) != configMarker {
			output.WriteString(lines[index])
			index++
			continue
		}
		start := index
		index++
		if end, ok := currentManagedBlockEnd(lines, index); ok {
			index = end
			continue
		}
		// The first installer generated one table without an end marker.
		if end, ok := legacyManagedBlockEnd(lines, index); ok {
			index = end
			continue
		}
		// A marker without a recognized block is unrelated text.
		output.WriteString(lines[start])
	}
	return output.String()
}

func currentManagedBlockEnd(lines []string, start int) (int, bool) {
	tables := 0
	for index := start; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		switch {
		case line == configEndMarker:
			return index + 1, tables > 0
		case line == "":
			continue
		case line == "[[keys.command]]":
			tables++
			if tables > 3 {
				return start, false
			}
		case isManagedCommandField(line):
			continue
		default:
			return start, false
		}
	}
	return start, false
}

func legacyManagedBlockEnd(lines []string, start int) (int, bool) {
	index := start
	for index < len(lines) && strings.TrimSpace(lines[index]) == "" {
		index++
	}
	if index >= len(lines) || strings.TrimSpace(lines[index]) != "[[keys.command]]" {
		return start, false
	}
	index++
	seenHeight := false
	for index < len(lines) {
		line := strings.TrimSpace(lines[index])
		if !isManagedCommandField(line) {
			break
		}
		index++
		if strings.HasPrefix(line, "height =") {
			seenHeight = true
			break
		}
	}
	return index, seenHeight
}

func isManagedCommandField(line string) bool {
	return strings.HasPrefix(line, "key =") || strings.HasPrefix(line, "type =") || strings.HasPrefix(line, "command =") || strings.HasPrefix(line, "width =") || strings.HasPrefix(line, "height =")
}
