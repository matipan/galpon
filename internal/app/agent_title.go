package app

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/matipan/galpon/internal/model"
)

// agentTitleLimit is the maximum number of characters in an agent title.
const agentTitleLimit = 120

// renameViewTimeout limits the time that a rename waits for the terminal app
// of the agent.
const renameViewTimeout = 5 * time.Second

// agentTitle gives the title of a new or renamed agent: the input without
// the spaces around it. A title must have visible text and at most
// agentTitleLimit characters. Two agents can have the same title.
func agentTitle(input string) (string, error) {
	title := strings.TrimSpace(input)
	if title == "" {
		return "", invalidRequestf("agent title is required")
	}
	if utf8.RuneCountInString(title) > agentTitleLimit {
		return "", invalidRequestf("agent title exceeds the %d-character limit", agentTitleLimit)
	}
	for _, r := range title {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", invalidRequestf("agent title must use visible text")
		}
	}
	return title, nil
}

// RenameAgent gives an agent a new title. When the agent is open in a
// terminal app, its tab gets the new title too. A tab that the app cannot
// rename keeps its name until the agent opens again, so that failure does
// not stop the rename. A running agent reads its new title itself.
func (a *App) RenameAgent(ctx context.Context, id, title string) (model.Agent, error) {
	title, err := agentTitle(title)
	if err != nil {
		return model.Agent{}, err
	}
	unlock := a.lockAgentLifecycle(id)
	defer unlock()
	agent, err := a.Store.RenameAgent(ctx, id, title)
	if err != nil {
		return model.Agent{}, err
	}
	if strings.TrimSpace(agent.RendererID) == "" {
		return agent, nil
	}
	if renderer := a.agentRenderer(agent); rendererShows(renderer, agent) {
		viewCtx, cancel := context.WithTimeout(ctx, renameViewTimeout)
		defer cancel()
		if err := renderer.RenameAgent(viewCtx, agent); err != nil && a.Logger != nil {
			a.Logger.Printf("rename the terminal of agent %s in %s: %v", agent.ID, rendererLabel(renderer), err)
		}
	}
	return agent, nil
}
