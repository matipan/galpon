package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/terminal"
)

// ErrAgentOpenElsewhere means that an idle agent runs in another terminal app.
// The caller can open it again with OpenAgentOptions.Move to move it.
var ErrAgentOpenElsewhere = errors.New("agent is open in another terminal app")

// OpenAgentOptions selects where an agent opens.
type OpenAgentOptions struct {
	Focus bool
	// Renderer is the terminal app of the caller, such as "herdr" or
	// "herdr-app". When it is empty, the agent opens where it was open last.
	Renderer string
	// Move lets Galpon close an idle agent in another terminal app and open it
	// in Renderer. The agent process restarts with the same conversation.
	Move bool
}

// rendererNamed returns the renderer with this name, or nil.
func (a *App) rendererNamed(name string) terminal.Renderer {
	if a.Renderer != nil && a.Renderer.Name() == name {
		return a.Renderer
	}
	for _, renderer := range a.Renderers {
		if renderer != nil && renderer.Name() == name {
			return renderer
		}
	}
	return nil
}

// agentRenderer returns the renderer that showed the agent last. An agent that
// was never open uses the default renderer.
func (a *App) agentRenderer(agent model.Agent) terminal.Renderer {
	if renderer := a.rendererNamed(agent.Renderer); renderer != nil {
		return renderer
	}
	return a.Renderer
}

// rendererShows reports whether the renderer holds the current view of the
// agent.
func rendererShows(renderer terminal.Renderer, agent model.Agent) bool {
	return renderer != nil && agent.Renderer == renderer.Name() && agent.RendererContext == renderer.Context()
}

func rendererLabel(renderer terminal.Renderer) string {
	switch renderer.Name() {
	case "herdr":
		return "Herdr"
	case "herdr-app":
		return "the Herdr app"
	}
	return renderer.Name()
}

// leaveRenderer closes the agent in the terminal app that shows it, before it
// opens in another one. A busy agent stays where it is. An idle agent moves
// only when the caller confirms, because its process restarts.
func (a *App) leaveRenderer(ctx context.Context, agent model.Agent, previous, next terminal.Renderer, move bool) (model.Agent, error) {
	running, err := a.agentProcessRunning(agent.ID)
	if err != nil {
		return agent, err
	}
	if running {
		busy, err := a.agentWorking(ctx, agent)
		if err != nil {
			return agent, err
		}
		if busy {
			return agent, invalidRequestf("agent %s is working in %s; wait until it is idle, or open it there", agent.Title, rendererLabel(previous))
		}
		if !move {
			return agent, fmt.Errorf("%w: %s is open in %s. Open it again to move it to %s; its process restarts and continues the same conversation", ErrAgentOpenElsewhere, agent.Title, rendererLabel(previous), rendererLabel(next))
		}
	}
	if err := previous.CloseAgent(ctx, agent); err != nil && running {
		return agent, fmt.Errorf("close agent %s in %s: %w", agent.Title, rendererLabel(previous), err)
	}
	if running {
		if err := a.waitAgentProcessesExit(ctx, agent.ID, 10*time.Second); err != nil {
			return agent, fmt.Errorf("agent %s did not stop in %s: %w; close it there", agent.Title, rendererLabel(previous), err)
		}
	}
	if agent.RuntimeID != "" {
		if err := a.Store.StopAgentRuntime(ctx, agent.ID, agent.RuntimeID, ""); err != nil && !IsNotFound(err) {
			return agent, err
		}
	}
	agent, err = a.Store.Agent(ctx, agent.ID)
	if err != nil {
		return agent, err
	}
	// The next renderer starts a new view.
	agent.Renderer, agent.RendererContext, agent.RendererID = "", "", ""
	return agent, nil
}

// agentWorking reports whether an agent with a running process is in a turn.
func (a *App) agentWorking(ctx context.Context, agent model.Agent) (bool, error) {
	if agent.Status == "running" || agent.Status == "starting" {
		return true, nil
	}
	// A runtime that registers again after a daemon restart shows idle, also
	// during a turn. Its started operation shows the turn.
	return a.Store.AgentHasStartedOperation(ctx, agent.ID)
}

// CloseAgentView closes the terminal of an idle agent. The agent stays in
// Galpon, and a message or an open starts it again. A working agent stays
// open, because Galpon would start it again to finish its work.
func (a *App) CloseAgentView(ctx context.Context, id string) (model.Agent, error) {
	unlock := a.lockAgentLifecycle(id)
	defer unlock()
	agent, err := a.Store.Agent(ctx, id)
	if err != nil {
		return model.Agent{}, err
	}
	if agent.IsBackground() {
		return model.Agent{}, invalidRequestf("agent %s works in the background and has no terminal to close", agent.Title)
	}
	running, err := a.agentProcessRunning(agent.ID)
	if err != nil {
		return model.Agent{}, err
	}
	if running {
		busy, err := a.agentWorking(ctx, agent)
		if err != nil {
			return model.Agent{}, err
		}
		if busy {
			return model.Agent{}, invalidRequestf("agent %s is working; wait until it is idle, then close it", agent.Title)
		}
	}
	if strings.TrimSpace(agent.RendererID) != "" {
		renderer := a.agentRenderer(agent)
		if !rendererShows(renderer, agent) {
			return model.Agent{}, fmt.Errorf("cannot close the terminal of agent %s in renderer %s context %s", agent.Title, agent.Renderer, agent.RendererContext)
		}
		if err := renderer.CloseAgent(ctx, agent); err != nil {
			return model.Agent{}, fmt.Errorf("close the terminal of agent %s: %w", agent.Title, err)
		}
	}
	if running {
		if err := a.waitAgentProcessesExit(ctx, agent.ID, 10*time.Second); err != nil {
			return model.Agent{}, fmt.Errorf("agent %s did not stop: %w", agent.Title, err)
		}
	}
	if agent.RuntimeID != "" {
		if err := a.Store.StopAgentRuntime(ctx, agent.ID, agent.RuntimeID, ""); err != nil && !IsNotFound(err) {
			return model.Agent{}, err
		}
	}
	return a.Store.Agent(ctx, agent.ID)
}

// agentProcessRunning reports whether a runtime process of the agent runs.
func (a *App) agentProcessRunning(agentID string) (bool, error) {
	if a.runtimeRunning != nil {
		return a.runtimeRunning(agentID)
	}
	processes, err := communicationAgentProcesses(a.Config.StateDir, a.Config.Socket)
	if err != nil {
		return false, fmt.Errorf("inspect the processes of agent %s: %w", agentID, err)
	}
	for _, process := range processes {
		if process.AgentID == agentID {
			return true, nil
		}
	}
	return false, nil
}

func (a *App) waitAgentProcessesExit(ctx context.Context, agentID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		running, err := a.agentProcessRunning(agentID)
		if err != nil || !running {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("its process is still running after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// SetAgentView chooses where an agent opens: model.ViewApp, a Pi agent in a
// tab of the Herdr app, or model.ViewTerminal. An open idle agent closes in
// its current view first, so that only one Pi runs its session; its next open
// starts it in the new view with the same conversation. A working agent keeps
// its view.
func (a *App) SetAgentView(ctx context.Context, id, view string) (model.Agent, error) {
	switch view {
	case "terminal":
		view = model.ViewTerminal
	case model.ViewTerminal, model.ViewApp:
	default:
		return model.Agent{}, invalidRequestf("unknown view %q: use app or terminal", view)
	}
	unlock := a.lockAgentLifecycle(id)
	defer unlock()
	agent, err := a.Store.Agent(ctx, id)
	if err != nil {
		return model.Agent{}, err
	}
	if view == model.ViewApp && agent.Harness() != model.HarnessPi {
		return model.Agent{}, invalidRequestf("only a Pi agent can open in the app view; %s uses %s", agent.Title, model.HarnessLabel(agent.Harness()))
	}
	if agent.View == view {
		return agent, nil
	}
	if !agent.IsBackground() {
		running, err := a.agentProcessRunning(agent.ID)
		if err != nil {
			return model.Agent{}, err
		}
		if running {
			busy, err := a.agentWorking(ctx, agent)
			if err != nil {
				return model.Agent{}, err
			}
			if busy {
				return model.Agent{}, invalidRequestf("agent %s is working; wait until it is idle, then open it in the other view", agent.Title)
			}
		}
		if strings.TrimSpace(agent.RendererID) != "" {
			if renderer := a.agentRenderer(agent); rendererShows(renderer, agent) {
				if err := renderer.CloseAgent(ctx, agent); err != nil && running {
					return model.Agent{}, fmt.Errorf("close agent %s before it changes its view: %w", agent.Title, err)
				}
			}
		}
		if running {
			if err := a.waitAgentProcessesExit(ctx, agent.ID, 10*time.Second); err != nil {
				return model.Agent{}, fmt.Errorf("agent %s did not stop: %w", agent.Title, err)
			}
		}
		if agent.RuntimeID != "" {
			if err := a.Store.StopAgentRuntime(ctx, agent.ID, agent.RuntimeID, ""); err != nil && !IsNotFound(err) {
				return model.Agent{}, err
			}
		}
	}
	if err := a.Store.SetAgentView(ctx, agent.ID, view); err != nil {
		return model.Agent{}, err
	}
	return a.Store.Agent(ctx, agent.ID)
}
