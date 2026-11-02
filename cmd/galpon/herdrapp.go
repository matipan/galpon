package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/herdr"
	"github.com/matipan/galpon/internal/herdrapp"
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
		return herdrapp.Adapter{}
	}
	return herdr.Adapter{Bin: cfg.HerdrBin}
}

func herdrAppCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("herdr-app needs install or new-agent")
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
	renderer := herdrapp.Adapter{}
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
