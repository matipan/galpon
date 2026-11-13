package tui

import (
	"strings"

	"github.com/matipan/galpon/internal/model"
)

// refreshAgentChoice adds the entered value to the existing selector. URL
// matching is exact source lookup; normal search still uses titles only.
func (m *Model) refreshAgentChoice() {
	query := strings.TrimSpace(m.choiceInput.Value())
	switch m.choice.Kind {
	case choiceAgentWorkspace:
		m.choice.Options = nil
		exact := false
		for _, workspace := range m.dashboard.Workspaces {
			m.choice.Options = append(m.choice.Options, choiceOption{Label: workspace.Title, Detail: workspace.Status, Value: workspace.ID})
			exact = exact || strings.EqualFold(strings.TrimSpace(workspace.Title), query)
		}
		if query != "" && !exact {
			m.choice.Options = append(m.choice.Options, choiceOption{Label: query, Detail: "Create with agent", Value: query, Input: true})
		}
	case choiceAgentRepository:
		m.choice.Options = nil
		isURL := model.ValidateRepositoryURL(query) == nil
		for _, repository := range m.dashboard.Repositories {
			option := choiceOption{Label: repository.Title, Detail: repository.DefaultBranch, Value: repository.ID}
			if isURL {
				if !repository.HasSourceURL(query) {
					continue
				}
				option.Matched = true
				for _, remote := range repository.Remotes {
					if model.RepositorySourceKey(remote.FetchURL) == model.RepositorySourceKey(query) {
						option.Remote = remote.Name
						break
					}
				}
			}
			m.choice.Options = append(m.choice.Options, option)
		}
		if isURL && len(m.choice.Options) == 0 {
			m.choice.Options = append(m.choice.Options, choiceOption{Label: query, Detail: "Add with agent", Value: query, Input: true, Matched: true})
		}
	}
}
