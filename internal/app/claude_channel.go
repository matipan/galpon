package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/matipan/galpon/internal/model"
)

// agentScreenRenderer is an optional renderer capability. It reads the
// visible text of an agent terminal and presses keys in that terminal.
type agentScreenRenderer interface {
	ReadAgentScreen(context.Context, model.Agent) (string, error)
	PressAgentKey(context.Context, model.Agent, string) error
}

// claudeChannelWarning holds the lines of the warning that Claude Code shows,
// in this order, each time it starts with the Galpon development channel.
// The last line shows that the first choice is selected.
var claudeChannelWarning = []string{
	"WARNING: Loading development channels",
	"Channels: server:galpon",
	"❯ 1. I am using this for local development",
}

// claudeChannelWarningSelected reports a screen that shows the Claude Code
// development channel warning for the Galpon channel alone, with "I am using
// this for local development" selected.
func claudeChannelWarningSelected(screen string) bool {
	next := 0
	for _, line := range strings.Split(screen, "\n") {
		if next < len(claudeChannelWarning) && strings.TrimSpace(line) == claudeChannelWarning[next] {
			next++
		}
	}
	return next == len(claudeChannelWarning)
}

// AcceptClaudeChannelWarning presses Enter in the terminal of a Claude Code
// agent when its screen shows the development channel warning for the Galpon
// channel, with the first choice selected. Claude Code shows this warning each
// time its terminal starts, and it ignores channel input until someone accepts
// it. Galpon adds this channel to the Claude Code command itself. Only the
// current runtime of the agent can ask. The result is true when Galpon pressed
// Enter.
func (a *App) AcceptClaudeChannelWarning(ctx context.Context, agentID, runtimeID string) (bool, error) {
	matches, err := a.Store.AgentRuntimeMatches(ctx, agentID, runtimeID)
	if err != nil {
		return false, err
	}
	if !matches {
		return false, fmt.Errorf("runtime %s is stale for agent %s", runtimeID, agentID)
	}
	renderer, ok := a.Renderer.(agentScreenRenderer)
	if !ok {
		return false, nil
	}
	agent, err := a.Store.Agent(ctx, agentID)
	if err != nil || agent.Harness() != model.HarnessClaude {
		return false, err
	}
	screen, err := renderer.ReadAgentScreen(ctx, agent)
	if err != nil || !claudeChannelWarningSelected(screen) {
		return false, err
	}
	if err := renderer.PressAgentKey(ctx, agent, "enter"); err != nil {
		return false, err
	}
	if a.Logger != nil {
		a.Logger.Printf("accepted the Claude Code development channel warning for agent %s", agentID)
	}
	return true, nil
}
