package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
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
		return fmt.Errorf("herdr-app needs install, new-agent, view, inspect, open, terminal, refresh or route")
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
		fmt.Println("Installed the Galpon mods and the Ctrl-K and Ctrl-Alt-N keys in", home)
		keys := make([]string, 0, len(replaced))
		for key := range replaced {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("%s was %q before\n", key, replaced[key])
		}
		return nil
	case "new-agent":
		return runHerdrAppNewAgent(cfg)
	case "view":
		return herdrAppView(cfg, args[1:])
	case "open":
		return herdrAppOpen(cfg, args[1:])
	case "inspect":
		return herdrAppInspect(cfg, args[1:])
	case "terminal":
		return herdrAppTerminal(cfg, args[1:])
	case "refresh":
		return herdrAppRefresh(cfg)
	case "route":
		return herdrAppRoute(cfg, args[1:])
	default:
		return fmt.Errorf("unknown herdr-app command %q", args[0])
	}
}

// runHerdrAppNewAgent opens the New Agent form for the workspace of the
// focused app terminal. The Galpon mod passes the terminal and its space. A
// terminal outside Galpon opens the command center.
func runHerdrAppNewAgent(cfg config.Config) error {
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	renderer := herdrapp.Adapter{Sync: true}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dashboard, err := client.Dashboard(ctx)
	if err != nil {
		return err
	}
	route := tui.StartupRoute{Target: tui.StartupNewAgent}
	if address := os.Getenv("GALPON_APP_TERMINAL"); address != "" {
		for _, agent := range dashboard.Agents {
			if agent.Renderer == herdrapp.Name && agent.RendererID == address {
				route.AgentID, route.WorkspaceID = agent.ID, agent.WorkspaceID
			}
		}
	}
	if route.WorkspaceID == "" {
		if id, ok := herdrapp.WorkspaceOfSpace(os.Getenv("GALPON_APP_SPACE")); ok {
			if _, ok := dashboard.Workspace(id); ok {
				route.WorkspaceID = id
			}
		}
	}
	if route.WorkspaceID == "" {
		return tui.Run(client, renderer)
	}
	return tui.RunWithStartup(client, renderer, route)
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
	fields, ok := tui.NativeFields(dashboard, pullRequests, pullRequestsErr, args[0], args[1])
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

// herdrAppTerminal opens a terminal at a managed worktree, in the space of
// its workspace.
func herdrAppTerminal(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("herdr-app terminal needs a worktree ID")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return modError(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	dashboard, err := client.Dashboard(ctx)
	if err != nil {
		return modError(err)
	}
	worktree, ok := dashboard.Worktree(args[0])
	if !ok {
		return modError(fmt.Errorf("the worktree is no longer available"))
	}
	workspace, ok := dashboard.Workspace(worktree.WorkspaceID)
	if !ok {
		return modError(fmt.Errorf("the workspace of the worktree is no longer available"))
	}
	repository, _ := dashboard.Repository(worktree.RepositoryID)
	if _, err := (herdrapp.Adapter{Sync: true}).OpenTerminal(ctx, workspace, worktree, workspace.Title+" · "+repository.Title, nil); err != nil {
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

// herdrAppRoute opens one terminal screen of the command center, for the
// forms that the native mod does not draw.
func herdrAppRoute(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("herdr-app route needs new-agent, fork-agent, new-repository or operations")
	}
	flags := flag.NewFlagSet("herdr-app route", flag.ContinueOnError)
	workspaceID := flags.String("workspace", "", "workspace ID")
	agentID := flags.String("agent", "", "agent ID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	route := tui.StartupRoute{WorkspaceID: *workspaceID, AgentID: *agentID}
	switch args[0] {
	case "new-agent":
		route.Target = tui.StartupNewAgent
	case "fork-agent":
		route.Target = tui.StartupForkAgent
	case "new-repository":
		route.Target = tui.StartupNewRepository
	case "operations":
		route.Target = tui.StartupOperations
	default:
		return fmt.Errorf("unknown herdr-app route %q", args[0])
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	if route.Target == tui.StartupNewAgent && route.WorkspaceID == "" {
		return tui.Run(client, herdrapp.Adapter{Sync: true})
	}
	return tui.RunWithStartup(client, herdrapp.Adapter{Sync: true}, route)
}
