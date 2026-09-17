package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/terminal"
)

func planTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line == "" {
			continue
		}
		runes := []rune(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return -1
			}
			return r
		}, line))
		return string(runes[:min(120, len(runes))])
	}
	return "Plan implementation"
}

func (m *Model) beginPlanAgentForm() {
	plan := m.startupRoute.Plan
	if plan == nil {
		m.status = "No approved plan was supplied"
		return
	}
	source, ok := m.dashboard.Agent(plan.SourceAgentID)
	if !ok {
		m.status = "The planning agent is not available"
		return
	}
	m.beginAgentFormWithSource(source.WorkspaceID, "", "", "")
	m.agentDraft.Name = planTitle(plan.Plan)
	m.agentDraft.Context = 0
	m.agentDraft.Worktrees = nil
	for _, assignment := range source.Placement.Worktrees {
		worktree, ok := m.dashboard.Worktree(assignment.WorktreeID)
		if !ok {
			m.screen, m.form = screenSwitcher, formNone
			m.err = fmt.Errorf("a plan repository is not available")
			return
		}
		found := false
		for index, repository := range m.dashboard.Repositories {
			if repository.ID != worktree.RepositoryID {
				continue
			}
			ref := repository.DefaultBranch
			if ref != "main" && ref != "master" {
				ref = "main"
			}
			m.agentDraft.Worktrees = append(m.agentDraft.Worktrees, agentWorktreeDraft{Repository: index, Remote: defaultRemoteIndex(repository), Ref: ref, FetchFirst: true})
			found = true
			break
		}
		if !found {
			m.screen, m.form = screenSwitcher, formNone
			m.err = fmt.Errorf("a plan repository is not available")
			return
		}
	}
	m.agentDraft.Placement = 0
	m.status = "Implement the approved plan · fresh conversation · fresh main/master worktrees"
	if len(m.agentDraft.Worktrees) == 0 {
		m.agentDraft.Placement = 2
		m.status = "Choose a placement. The planning agent's directory is not shared."
	}
	for index, field := range m.agentFields() {
		if (len(m.agentDraft.Worktrees) > 0 && field.Kind == agentCreate) || (len(m.agentDraft.Worktrees) == 0 && field.Kind == agentPlacement) {
			m.agentFocus = index
			break
		}
	}
	m.loadAgentInput()
}

func RunPlanAgentForm(client *app.Client, renderer terminal.Renderer, plan app.PlanHandoff) (app.PlanAgentResult, error) {
	applyPalette(configuredPalette())
	program := tea.NewProgram(NewWithStartup(client, renderer, StartupRoute{Target: StartupPlanAgent, Plan: &plan}), tea.WithAltScreen())
	final, err := program.Run()
	if err != nil {
		return app.PlanAgentResult{}, err
	}
	return app.PlanAgentResult{AgentID: final.(Model).planAgentID}, nil
}
