// Package herdrapp shows Galpon agents and terminals in the Herdr desktop app
// (herdr-app). herdr-core runs the terminals, and the hc command controls it.
// The app window puts a terminal in a space and a tab by its herdr.space and
// herdr.tab labels, also when the window opens later. Names and focus need
// window commands, which work only while the window is open.
package herdrapp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

// Name is the renderer name that Galpon saves for agents in the app.
const Name = "herdr-app"

// agentLabel marks the terminal of a Galpon agent with the agent ID.
const agentLabel = "galpon.agent"

// errNoWindow means that the app window is not open.
var errNoWindow = errors.New("the Herdr app window is not open")

// Adapter is the Galpon renderer for the Herdr app.
type Adapter struct {
	// HC and App are the hc and herdr-app executables. Empty values use PATH,
	// then ~/.local/bin.
	HC, App string
	// Logger records window commands that fail. It can be nil.
	Logger *log.Logger
	// Sync names, places and focuses a new terminal before OpenAgent or
	// OpenTerminal returns. A short command needs it, because it can exit
	// before a background task ends. The daemon does it in the background.
	Sync bool
	// Screen means that a screen asked for the terminal: another computer
	// that shows this host in its own app window. The screen focuses the
	// terminal there, so the window on this computer keeps its focus.
	Screen bool
	// Placed, when set, gets the place of each terminal that OpenAgent or
	// OpenTerminal shows.
	Placed func(Placement)
}

func (a Adapter) Name() string { return Name }

// Context is the herdr-core session. It selects the core, as it does for hc.
func (a Adapter) Context() string {
	if session := strings.TrimSpace(os.Getenv("HC_SESSION")); session != "" {
		return session
	}
	return "default"
}

// SpaceID is the app space that holds the terminals of a Galpon workspace.
func SpaceID(workspaceID string) string { return "galpon-" + workspaceID }

// WorkspaceOfSpace returns the Galpon workspace ID of an app space ID.
func WorkspaceOfSpace(spaceID string) (string, bool) {
	return strings.CutPrefix(spaceID, "galpon-")
}

func agentTabID(agentID string) string { return "galpon-" + agentID }

// OpenAgent shows the agent terminal, and starts the command when the agent
// has no running terminal. It returns no workspace ID, because the app space
// comes from the Galpon workspace ID.
func (a Adapter) OpenAgent(ctx context.Context, ws model.Workspace, wt model.Worktree, agent model.Agent, command []string, focus bool) (string, string, bool, error) {
	current, err := a.agentTerminal(ctx, agent)
	if err != nil {
		return "", "", false, err
	}
	place := placement{Placement: AgentPlacement(ws, agent), focus: focus}
	started := false
	switch {
	case current == nil:
		labels := []string{"herdr.space=" + place.Space, "herdr.tab=" + place.Tab, agentLabel + "=" + agent.ID}
		place.Terminal, err = a.start(ctx, wt.Path, terminalName(agent), labels, shellCommand(command, false))
		if err != nil {
			return "", "", false, err
		}
		started = true
	default:
		place.Terminal = current.Address
		// The person can move the terminal to another tab or space.
		if space := current.Labels["herdr.space"]; space != "" {
			place.Space = space
		}
		if tab := current.Labels["herdr.tab"]; tab != "" {
			place.Tab = tab
		}
		// After a core restart, a kept terminal holds a new shell.
		if agent.Status != "running" && agent.Status != "starting" && agent.Status != "idle" && current.onlyShell() {
			if _, err := a.hc(ctx, "send", current.Address, shellLine(command)); err != nil {
				return "", "", false, err
			}
			started = true
		}
	}
	a.arrange(place)
	return "", place.Terminal, started, nil
}

// OpenTerminal opens a project terminal in a new tab of the workspace space.
// A command runs in the shell, and the shell stays after the command ends.
func (a Adapter) OpenTerminal(ctx context.Context, ws model.Workspace, wt model.Worktree, label string, command []string) (string, error) {
	if label == "" {
		label = ws.Title
	}
	place := placement{Placement: Placement{Space: SpaceID(ws.ID), SpaceName: ws.Title, Tab: "galpon-terminal-" + randomID(), TabName: label}, focus: true}
	var err error
	place.Terminal, err = a.start(ctx, wt.Path, "", []string{"herdr.space=" + place.Space, "herdr.tab=" + place.Tab}, shellCommand(command, true))
	if err != nil {
		return "", err
	}
	a.arrange(place)
	return "", nil
}

// CloseAgent ends the live terminals of the agent. The window then removes
// their panes.
func (a Adapter) CloseAgent(ctx context.Context, agent model.Agent) error {
	if agent.Renderer != a.Name() || agent.RendererContext != a.Context() {
		return nil
	}
	terminals, err := a.agentTerminals(ctx, agent.ID)
	if err != nil {
		return err
	}
	for _, terminal := range terminals {
		if _, err := a.hc(ctx, "kill", terminal.Address); err != nil {
			// The terminal can end between the list and the kill.
			if record, getErr := a.get(ctx, terminal.Address); getErr == nil && record.State == "live" {
				return err
			}
		}
	}
	return nil
}

// ReportAgent has nothing to do. The app shows the status that Pi, Claude Code
// and Codex report themselves.
func (a Adapter) ReportAgent(context.Context, model.Agent, string, string) error { return nil }

// ReadAgentScreen returns the visible text of the agent terminal.
func (a Adapter) ReadAgentScreen(ctx context.Context, agent model.Agent) (string, error) {
	if agent.Renderer != a.Name() || agent.RendererContext != a.Context() || agent.RendererID == "" {
		return "", nil
	}
	out, err := a.hc(ctx, "read", agent.RendererID)
	return string(out), err
}

// PressAgentKey sends one key press, such as "enter", to the agent terminal.
func (a Adapter) PressAgentKey(ctx context.Context, agent model.Agent, key string) error {
	if agent.Renderer != a.Name() || agent.RendererContext != a.Context() || agent.RendererID == "" {
		return fmt.Errorf("agent %s has no terminal in herdr-core session %s", agent.ID, a.Context())
	}
	_, err := a.hc(ctx, "keys", agent.RendererID, key)
	return err
}

type terminalRecord struct {
	Address    string            `json:"address"`
	State      string            `json:"state"`
	PID        int               `json:"pid"`
	Labels     map[string]string `json:"labels"`
	Foreground *struct {
		PID  int    `json:"pid"`
		Name string `json:"name"`
	} `json:"foreground"`
}

// onlyShell reports a terminal whose own shell runs and nothing in it.
func (t terminalRecord) onlyShell() bool {
	if t.Foreground == nil || t.Foreground.PID != t.PID {
		return false
	}
	switch t.Foreground.Name {
	case "sh", "bash", "zsh", "fish", "dash", "ksh", "nu":
		return true
	}
	return false
}

// agentTerminal returns the live terminal of the agent, or nil.
func (a Adapter) agentTerminal(ctx context.Context, agent model.Agent) (*terminalRecord, error) {
	terminals, err := a.agentTerminals(ctx, agent.ID)
	if err != nil || len(terminals) == 0 {
		return nil, err
	}
	address := terminals[0].Address
	for _, terminal := range terminals {
		if agent.Renderer == a.Name() && terminal.Address == agent.RendererID {
			address = terminal.Address
		}
	}
	record, err := a.get(ctx, address)
	if err != nil || record.State != "live" {
		return nil, err
	}
	return &record, nil
}

func (a Adapter) agentTerminals(ctx context.Context, agentID string) ([]terminalRecord, error) {
	out, err := a.hc(ctx, "ls", "--json", "--label", agentLabel+"="+agentID)
	if err != nil {
		return nil, err
	}
	var list struct {
		Terminals []terminalRecord `json:"terminals"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("read hc terminal list: %w", err)
	}
	live := list.Terminals[:0]
	for _, terminal := range list.Terminals {
		if terminal.State == "live" {
			live = append(live, terminal)
		}
	}
	return live, nil
}

func (a Adapter) get(ctx context.Context, address string) (terminalRecord, error) {
	out, err := a.hc(ctx, "get", address, "--json")
	if err != nil {
		return terminalRecord{}, err
	}
	var record terminalRecord
	if err := json.Unmarshal(out, &record); err != nil {
		return terminalRecord{}, fmt.Errorf("read hc terminal %s: %w", address, err)
	}
	return record, nil
}

// start runs argv in a new terminal and returns its address. The terminal has
// no parent, so herdr-core types no notes about it into another terminal, and
// it goes away when its program ends.
func (a Adapter) start(ctx context.Context, dir, name string, labels, argv []string) (string, error) {
	args := []string{"run", "--bg", "--json", "--no-parent", "--no-notify", "--rm", "--cwd", dir}
	if name != "" {
		args = append(args, "--name", name)
	}
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	out, err := a.hc(ctx, append(append(args, "--"), argv...)...)
	if err != nil {
		return "", err
	}
	var started struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(out, &started); err != nil || started.Address == "" {
		return "", fmt.Errorf("hc did not return a terminal address: %s", strings.TrimSpace(string(out)))
	}
	return started.Address, nil
}

// binary finds a command of the app. The app installer puts hc and herdr-app
// in ~/.local/bin, and the daemon can have a PATH without that folder.
func binary(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".local", "bin", name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return path
		}
	}
	return name
}

func (a Adapter) hc(ctx context.Context, args ...string) ([]byte, error) {
	bin := a.HC
	if bin == "" {
		bin = binary("hc")
	}
	command := exec.CommandContext(ctx, bin, args...)
	// hc gives a new terminal the identity of its caller. Use the identity of
	// the app's own terminals, not the identity of the daemon's environment.
	command.Env = append(environment("HC_TERMINAL_ID", "TERM", "TERM_PROGRAM", "COLORTERM"),
		"TERM=xterm-256color", "TERM_PROGRAM=ghostty", "COLORTERM=truecolor")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("hc %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()+stdout.String()))
	}
	return stdout.Bytes(), nil
}

// Placement is where the app shows a terminal that Galpon opened: the
// terminal address, and the IDs and names of its space and tab. The address
// and the IDs are the ones on the computer of the terminal.
type Placement struct {
	Terminal  string `json:"terminal"`
	Space     string `json:"space"`
	SpaceName string `json:"spaceName"`
	Tab       string `json:"tab"`
	TabName   string `json:"tabName"`
	// Agent is true for the terminal of a Galpon agent. The app focuses an
	// agent with agent.focus, and another terminal with pane.focus.
	Agent bool `json:"agent,omitempty"`
}

// AgentPlacement is the place that OpenAgent gives the terminal of an agent
// in workspace ws. A terminal that the person moved keeps the space and the
// tab of its labels.
func AgentPlacement(ws model.Workspace, agent model.Agent) Placement {
	return Placement{
		Terminal: agent.RendererID, Space: SpaceID(ws.ID), SpaceName: ws.Title,
		Tab: agentTabID(agent.ID), TabName: agent.Title, Agent: true,
	}
}

// placement is a Placement, and whether the window focuses the terminal.
type placement struct {
	Placement
	focus bool
}

// arrange names the space and the tab and focuses the terminal, while the
// window is open. The window places a new terminal after its next core
// listing, so the names are tried again for some seconds.
func (a Adapter) arrange(place placement) {
	if a.Placed != nil {
		a.Placed(place.Placement)
	}
	if a.Screen {
		place.focus = false
	}
	if a.Sync {
		a.arrangeNow(place)
		return
	}
	go a.arrangeNow(place)
}

func (a Adapter) arrangeNow(place placement) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for {
		err := a.window(ctx, "tab.rename", map[string]any{"id": place.Tab, "name": place.TabName})
		if err == nil {
			break
		}
		if errors.Is(err, errNoWindow) {
			return
		}
		select {
		case <-ctx.Done():
			a.logf("name tab %s in the Herdr app: %v", place.Tab, err)
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err := a.window(ctx, "space.rename", map[string]any{"id": place.Space, "name": place.SpaceName}); err != nil {
		a.logf("name space %s in the Herdr app: %v", place.Space, err)
	}
	if place.focus {
		command := "pane.focus"
		if place.Agent {
			command = "agent.focus"
		}
		if err := a.window(ctx, command, map[string]any{"terminal": place.Terminal}); err != nil {
			a.logf("focus terminal %s in the Herdr app: %v", place.Terminal, err)
		}
	}
}

// window runs one app window command. Arguments go as one JSON object,
// because herdr-app reads a plain value that looks like JSON as JSON.
func (a Adapter) window(ctx context.Context, name string, args map[string]any) error {
	bin := a.App
	if bin == "" {
		bin = binary("herdr-app")
	}
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, bin, "do", name, string(data))
	command.Env = environment("HC_TERMINAL_ID")
	out, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	if strings.Contains(string(out), "no Herdr window") {
		return errNoWindow
	}
	return fmt.Errorf("herdr-app do %s: %w: %s", name, err, strings.TrimSpace(string(out)))
}

func (a Adapter) logf(format string, args ...any) {
	if a.Logger != nil {
		a.Logger.Printf(format, args...)
	}
}

// environment returns the process environment without the given keys.
func environment(remove ...string) []string {
	var out []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		skip := false
		for _, name := range remove {
			skip = skip || key == name
		}
		if !skip {
			out = append(out, value)
		}
	}
	return out
}

// shellCommand runs command in the person's interactive shell, so it gets the
// same environment as a command in a Herdr pane. With keepShell, the shell
// stays after the command ends. DISABLE_AUTO_UPDATE stops the oh-my-zsh
// update question, which otherwise holds the command until someone answers.
func shellCommand(command []string, keepShell bool) []string {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/sh"
	}
	if len(command) == 0 {
		return []string{shell}
	}
	line := shellLine(command)
	if keepShell {
		line += "; exec " + shellQuote(shell) + " -i"
	}
	switch filepath.Base(shell) {
	case "sh", "bash", "zsh", "fish", "dash", "ksh":
		return []string{"env", "DISABLE_AUTO_UPDATE=true", shell, "-i", "-c", line}
	}
	return []string{"/bin/sh", "-c", line}
}

func shellLine(command []string) string {
	quoted := make([]string, len(command))
	for index, arg := range command {
		quoted[index] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@%+,", r))
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// terminalName is an hc name for the agent terminal: a short form of the title
// and the start of the agent ID. hc names have 1 to 32 characters from a-z,
// 0-9, '-', '_' and '.'.
func terminalName(agent model.Agent) string {
	var slug strings.Builder
	for _, r := range strings.ToLower(agent.Title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			slug.WriteRune(r)
		case slug.Len() > 0 && !strings.HasSuffix(slug.String(), "-"):
			slug.WriteByte('-')
		}
	}
	name := strings.Trim(slug.String(), "-")
	if len(name) > 23 {
		name = strings.Trim(name[:23], "-")
	}
	id := strings.ReplaceAll(agent.ID, "-", "")
	if len(id) > 8 {
		id = id[:8]
	}
	if name == "" {
		name = "galpon"
	}
	return name + "-" + id
}

func randomID() string {
	var value [6]byte
	_, _ = rand.Read(value[:])
	return hex.EncodeToString(value[:])
}
