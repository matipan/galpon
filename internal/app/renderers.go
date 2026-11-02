package app

import (
	"context"
	"errors"
	"fmt"
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
		busy := agent.Status == "running" || agent.Status == "starting"
		if !busy {
			// A runtime that registers again after a daemon restart shows idle,
			// also during a turn. Its started operation shows the turn.
			if busy, err = a.Store.AgentHasStartedOperation(ctx, agent.ID); err != nil {
				return agent, err
			}
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
