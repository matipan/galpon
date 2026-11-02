package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/herdr"
	"github.com/matipan/galpon/internal/herdrapp"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/terminal"
	"github.com/matipan/galpon/internal/tui"
)

// callerRenderer names the terminal app that runs this command: "herdr-app"
// in a Herdr app terminal, "herdr" in a Herdr pane or popup, else "". Agents
// that this command opens open there. GALPON_RENDERER sets it explicitly.
func callerRenderer() string {
	switch {
	case strings.TrimSpace(os.Getenv("GALPON_RENDERER")) != "":
		return strings.TrimSpace(os.Getenv("GALPON_RENDERER"))
	case os.Getenv("HC_TERMINAL_ID") != "":
		return herdrapp.Name
	case os.Getenv("HERDR_ENV") != "" || os.Getenv("HERDR_PANE_ID") != "" || os.Getenv("HERDR_ACTIVE_PANE_ID") != "":
		return "herdr"
	}
	return ""
}

// callerRendererAdapter returns the renderer that opens project terminals for
// this command.
func callerRendererAdapter(cfg config.Config) terminal.Renderer {
	if callerRenderer() == herdrapp.Name {
		return herdrapp.Adapter{Sync: true}
	}
	return herdr.Adapter{Bin: cfg.HerdrBin}
}

func herdrAppCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("herdr-app needs install, view, inspect, open, close, refresh, sidebar, form, targets, operations or start")
	}
	switch args[0] {
	case "install":
		home, err := herdrapp.Home()
		if err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		replaced, err := herdrapp.Install(home, executable)
		if err != nil {
			return err
		}
		fmt.Println("Installed the Galpon mods, the Galpon layout and the Ctrl-K and Ctrl-Alt-N keys in", home)
		fmt.Println("Press Ctrl+Shift+L in the app to switch to the Galpon layout. In that layout, Ctrl+Alt+J goes to the next agent turn.")
		keys := make([]string, 0, len(replaced))
		for key := range replaced {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("%s was %q before\n", key, replaced[key])
		}
		return nil
	case "view":
		return herdrAppView(cfg, args[1:])
	case "inspect":
		return herdrAppInspect(cfg, args[1:])
	case "open":
		return herdrAppOpen(cfg, args[1:])
	case "close":
		return herdrAppClose(cfg, args[1:])
	case "refresh":
		return herdrAppRefresh(cfg)
	case "sidebar":
		return herdrAppSidebar(cfg, args[1:])
	case "form":
		return herdrAppForm(cfg, args[1:])
	case "targets":
		return herdrAppTargets(cfg, args[1:])
	case "operations":
		return herdrAppOperations(cfg, args[1:])
	case "start":
		return herdrAppStart(cfg, args[1:])
	case "work":
		return herdrAppWork(cfg, args[1:])
	default:
		return fmt.Errorf("unknown herdr-app command %q", args[0])
	}
}

// setCallerRenderer makes agents that this client opens open in the caller's
// terminal app.
func setCallerRenderer(client *app.Client) *app.Client {
	client.Renderer = callerRenderer()
	return client
}

// The native command center mod runs the commands below. Each prints one JSON
// object. An error that the person can act on is a result with "error", so
// that the mod can show it; "code" names the errors that the mod handles.

func printModResult(value any) error {
	return json.NewEncoder(os.Stdout).Encode(value)
}

func modError(err error) error {
	result := map[string]string{"error": err.Error()}
	if app.IsAgentOpenElsewhere(err) {
		result["code"] = "open-elsewhere"
	}
	return printModResult(result)
}

// herdrAppView prints the command center data. --hidden includes hidden
// resources.
func herdrAppView(cfg config.Config, args []string) error {
	client, err := ensureDaemon(cfg)
	if err != nil {
		return modError(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var dashboard model.Dashboard
	if len(args) > 0 && args[0] == "--hidden" {
		dashboard, err = client.DashboardWithHidden(ctx)
	} else {
		dashboard, err = client.Dashboard(ctx)
	}
	if err != nil {
		return modError(err)
	}
	pullRequests, pullRequestsErr := client.PullRequests(ctx)
	return printModResult(tui.BuildNativeView(dashboard, pullRequests, pullRequestsErr))
}

// herdrAppInspect prints the inspector fields of one row: herdr-app inspect
// <kind> <id> [--hidden].
func herdrAppInspect(cfg config.Config, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("herdr-app inspect needs a kind and an ID")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return modError(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var dashboard model.Dashboard
	if len(args) > 2 && args[2] == "--hidden" {
		dashboard, err = client.DashboardWithHidden(ctx)
	} else {
		dashboard, err = client.Dashboard(ctx)
	}
	if err != nil {
		return modError(err)
	}
	pullRequests, pullRequestsErr := client.PullRequests(ctx)
	var operations *model.AgentOperations
	var operationsErr error
	if args[0] == "agent" {
		operationsCtx, cancelOperations := context.WithTimeout(ctx, 3*time.Second)
		value, err := client.AgentOperations(operationsCtx, args[1])
		cancelOperations()
		operations, operationsErr = &value, err
	}
	fields, ok := tui.NativeFields(dashboard, pullRequests, pullRequestsErr, args[0], args[1], operations, operationsErr)
	if !ok {
		return modError(fmt.Errorf("this %s is no longer available", strings.ReplaceAll(args[0], "-", " ")))
	}
	return printModResult(map[string]any{"fields": fields})
}

// herdrAppOpen opens an agent in the Herdr app. --move moves an idle agent
// that runs in Herdr.
func herdrAppOpen(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("herdr-app open needs an agent ID")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return modError(err)
	}
	client.Renderer = herdrapp.Name
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var agent model.Agent
	if len(args) > 1 && args[1] == "--move" {
		agent, err = client.MoveAgent(ctx, args[0])
	} else {
		agent, err = client.OpenAgent(ctx, args[0], true)
	}
	var apiErr *app.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		err = fmt.Errorf("this agent is no longer available")
	}
	if err != nil {
		return modError(err)
	}
	return printModResult(map[string]any{"ok": true, "terminal": agent.RendererID})
}

// herdrAppClose closes the terminal of an idle agent. The agent stays in
// Galpon.
func herdrAppClose(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("herdr-app close needs an agent ID")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return modError(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	_, err = client.CloseAgent(ctx, args[0])
	var apiErr *app.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		if strings.Contains(apiErr.Message, "page not found") {
			err = fmt.Errorf("this Galpon daemon cannot close agents yet; run galpon daemon restart")
		} else {
			err = fmt.Errorf("this agent is no longer available")
		}
	}
	if err != nil {
		return modError(err)
	}
	return printModResult(map[string]bool{"ok": true})
}

// herdrAppRefresh asks the daemon to get the pull requests from GitHub now.
func herdrAppRefresh(cfg config.Config) error {
	client, err := ensureDaemon(cfg)
	if err != nil {
		return modError(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.RefreshPullRequests(ctx); err != nil {
		return modError(err)
	}
	return printModResult(map[string]bool{"ok": true})
}

// herdrAppSidebar prints the Galpon facts of the sidebar in the Galpon
// layout. The sidebar reads them all the time, so the command does not start
// the daemon and does not make it call GitHub.
//
//	sidebar agents [<agent-id>...]   names and the delegated work of the agents
//	sidebar pull-requests            the pull requests that need someone
func herdrAppSidebar(cfg config.Config, args []string) error {
	if len(args) == 0 || args[0] != "agents" && args[0] != "pull-requests" {
		return fmt.Errorf("herdr-app sidebar needs agents or pull-requests")
	}
	client := app.NewClient(cfg.Socket)
	health, cancelHealth := context.WithTimeout(context.Background(), 500*time.Millisecond)
	err := client.Health(health)
	cancelHealth()
	if err != nil {
		return printModResult(map[string]string{"error": "Galpon is not running", "code": "no-daemon"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dashboard, err := client.Dashboard(ctx)
	if err != nil {
		return modError(err)
	}
	if args[0] == "pull-requests" {
		pullRequests, pullRequestsErr := client.StoredPullRequests(ctx)
		return printModResult(tui.BuildNativeSidebar(dashboard, pullRequests, pullRequestsErr, nil, nil))
	}
	ids := args[1:]
	// A daemon without the delegated work route gives an error. The sidebar
	// then shows the status that the app has.
	delegated, _ := client.DelegatedWork(ctx, ids)
	return printModResult(tui.BuildNativeSidebar(dashboard, model.PullRequests{}, nil, ids, delegated))
}

// herdrAppDashboard reads the dashboard for one mod command.
func herdrAppDashboard(client *app.Client) (model.Dashboard, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.Dashboard(ctx)
}

// herdrAppForm prints the first draft of a form, with what the form chooses
// from:
//
//	form agent new|here|fork [<kind> <id>]
//	form agent focus <terminal> <space>
//	form worktree <repository> [--editor]
//	form remote [<kind> <id>]
//
// "focus" starts in the workspace of the app terminal that had the keyboard,
// or of its space. Without a Galpon workspace it prints the code
// "no-workspace".
func herdrAppForm(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("herdr-app form needs agent, worktree or remote")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return modError(err)
	}
	dashboard, err := herdrAppDashboard(client)
	if err != nil {
		return modError(err)
	}
	arg := func(index int) string {
		if index < len(args) {
			return args[index]
		}
		return ""
	}
	var form tui.NativeForm
	switch args[0] {
	case "agent":
		request := tui.NativeAgentRequest{Mode: arg(1), Kind: arg(2), ID: arg(3)}
		if request.Mode == "focus" {
			request.Kind, request.ID = "", ""
			request.WorkspaceID, request.AgentID = appTerminalWorkspace(dashboard, arg(2), arg(3))
			if request.WorkspaceID == "" {
				return printModResult(map[string]string{"error": "the focused terminal is not in a Galpon workspace", "code": "no-workspace"})
			}
		}
		form, err = tui.NativeAgentForm(dashboard, request)
	case "worktree":
		form, err = tui.NativeWorktreeForm(dashboard, arg(1), arg(2) == "--editor")
	case "remote":
		form, err = tui.NativeRemoteForm(dashboard, arg(1), arg(2))
	default:
		return fmt.Errorf("unknown herdr-app form %q", args[0])
	}
	if err != nil {
		return modError(err)
	}
	return printModResult(form)
}

// appTerminalWorkspace finds the Galpon workspace of an app terminal: the
// workspace of the agent in that terminal, else the workspace of its space.
func appTerminalWorkspace(dashboard model.Dashboard, address, space string) (workspaceID, agentID string) {
	if address != "" {
		for _, agent := range dashboard.Agents {
			if agent.Renderer == herdrapp.Name && agent.RendererID == address {
				return agent.WorkspaceID, agent.ID
			}
		}
	}
	if id, ok := herdrapp.WorkspaceOfSpace(space); ok {
		if _, ok := dashboard.Workspace(id); ok {
			return id, ""
		}
	}
	return "", ""
}

// herdrAppTargets prints where a terminal for a row can open: targets <kind>
// <id>.
func herdrAppTargets(cfg config.Config, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("herdr-app targets needs a kind and an ID")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return modError(err)
	}
	dashboard, err := herdrAppDashboard(client)
	if err != nil {
		return modError(err)
	}
	targets, err := tui.NativeTargets(dashboard, args[0], args[1])
	if err != nil {
		return modError(err)
	}
	return printModResult(map[string]any{"targets": targets})
}

// herdrAppOperations prints the Operations view of an agent.
func herdrAppOperations(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("herdr-app operations needs an agent ID")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return modError(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	operations, err := client.AgentOperations(ctx, args[0])
	if err != nil {
		return modError(err)
	}
	return printModResult(tui.BuildNativeOperations(operations))
}

// herdrAppActions are the actions that `herdr-app start` runs, with the JSON
// that each takes.
var herdrAppActions = map[string]bool{
	"agent": true, "worktree": true, "remote": true, "workspace": true, "repository": true,
	"hide": true, "send": true, "terminal": true,
}

// herdrAppJobWait is how long `herdr-app start` waits for a result. The app
// stops a mod command after 30 seconds, so a longer action continues alone.
const herdrAppJobWait = 20 * time.Second

// herdrAppStart runs an action in a separate galpon process: start <action>
// <json>. A clone or a fetch can take longer than the 30 seconds that the app
// gives a mod command, and the app stops a command at that limit. The process
// writes its result to a file. A result that comes in time prints as is;
// otherwise this prints {"job": <file>} and the mod reads the file later.
func herdrAppStart(cfg config.Config, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("herdr-app start needs an action and its JSON")
	}
	if !herdrAppActions[args[0]] {
		return fmt.Errorf("unknown herdr-app action %q", args[0])
	}
	dir := filepath.Join(cfg.StateDir, "herdr-app", "jobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return modError(err)
	}
	removeOldJobs(dir, 24*time.Hour)
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return modError(err)
	}
	result := filepath.Join(dir, hex.EncodeToString(id)+".json")
	logPath := strings.TrimSuffix(result, ".json") + ".log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return modError(err)
	}
	executable, err := os.Executable()
	if err != nil {
		_ = logFile.Close()
		return modError(err)
	}
	command := exec.Command(executable, "herdr-app", "work", result, args[0], args[1])
	command.Stdout, command.Stderr = logFile, logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return modError(err)
	}
	_ = logFile.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case waitErr := <-done:
		data, err := os.ReadFile(result)
		if err != nil {
			detail, _ := os.ReadFile(logPath)
			return modError(fmt.Errorf("galpon stopped before the action completed: %v %s", waitErr, strings.TrimSpace(string(detail))))
		}
		_ = os.Remove(result)
		_ = os.Remove(logPath)
		_, err = os.Stdout.Write(data)
		return err
	case <-time.After(herdrAppJobWait):
		return printModResult(map[string]string{"job": result, "log": logPath})
	}
}

// removeOldJobs removes job files that nobody read.
func removeOldJobs(dir string, age time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > age {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// herdrAppWork runs one action of `herdr-app start` and writes its result
// atomically: work <result file> <action> <json>.
func herdrAppWork(cfg config.Config, args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("herdr-app work needs a result file, an action and its JSON")
	}
	result := func() (result tui.NativeResult) {
		defer func() {
			if recovered := recover(); recovered != nil {
				result = tui.NativeResult{Error: fmt.Sprintf("galpon stopped: %v", recovered)}
			}
		}()
		return herdrAppAction(cfg, args[1], args[2])
	}()
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	temporary := args[0] + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, args[0])
}

func herdrAppAction(cfg config.Config, action, payload string) tui.NativeResult {
	client, err := ensureDaemon(cfg)
	if err != nil {
		return tui.NativeResult{Error: err.Error()}
	}
	client.Renderer = herdrapp.Name
	decode := func(value any) error {
		if err := json.Unmarshal([]byte(payload), value); err != nil {
			return fmt.Errorf("the %s action has bad JSON: %w", action, err)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var dashboard model.Dashboard
	if action == "agent" || action == "worktree" || action == "remote" || action == "terminal" {
		if dashboard, err = herdrAppDashboard(client); err != nil {
			return tui.NativeResult{Error: err.Error()}
		}
	}
	switch action {
	case "agent":
		var draft tui.NativeAgentDraft
		if err := decode(&draft); err != nil {
			return tui.NativeResult{Error: err.Error()}
		}
		return tui.SubmitNativeAgent(client, dashboard, draft)
	case "worktree":
		var draft tui.NativeWorktreeDraft
		if err := decode(&draft); err != nil {
			return tui.NativeResult{Error: err.Error()}
		}
		return tui.SubmitNativeWorktree(client, herdrapp.Adapter{Sync: true}, dashboard, draft)
	case "remote":
		var draft tui.NativeRemoteDraft
		if err := decode(&draft); err != nil {
			return tui.NativeResult{Error: err.Error()}
		}
		return tui.SubmitNativeRemote(client, dashboard, draft)
	case "workspace", "repository":
		var in struct {
			Value string `json:"value"`
		}
		if err := decode(&in); err != nil {
			return tui.NativeResult{Error: err.Error()}
		}
		return tui.SubmitNativeSimple(client, action, in.Value)
	case "hide":
		var in struct {
			Kind   string `json:"kind"`
			ID     string `json:"id"`
			Title  string `json:"title"`
			Hidden bool   `json:"hidden"`
		}
		if err := decode(&in); err != nil {
			return tui.NativeResult{Error: err.Error()}
		}
		return tui.NativeHide(ctx, client, in.Kind, in.ID, in.Title, in.Hidden)
	case "send":
		var in struct {
			AgentID string `json:"agentId"`
			Text    string `json:"text"`
		}
		if err := decode(&in); err != nil {
			return tui.NativeResult{Error: err.Error()}
		}
		return tui.NativeSend(ctx, client, in.AgentID, in.Text)
	case "terminal":
		var in struct {
			Target tui.NativeTarget `json:"target"`
			Editor bool             `json:"editor"`
		}
		if err := decode(&in); err != nil {
			return tui.NativeResult{Error: err.Error()}
		}
		if err := tui.OpenNativeTarget(herdrapp.Adapter{Sync: true}, dashboard, in.Target, in.Editor); err != nil {
			return tui.NativeResult{Error: err.Error()}
		}
		return tui.NativeResult{OK: true, Close: true}
	default:
		return tui.NativeResult{Error: fmt.Sprintf("unknown herdr-app action %q", action)}
	}
}
