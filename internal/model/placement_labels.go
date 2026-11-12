package model

import (
	"cmp"
	"slices"
)

// AgentRepositoryTitles lists each placement repository once, primary first.
// Missing worktrees and repositories do not supply a label.
func (d Dashboard) AgentRepositoryTitles(agent Agent) []string {
	assignments := slices.Clone(agent.Placement.Worktrees)
	slices.SortStableFunc(assignments, func(a, b AgentWorktree) int {
		return cmp.Compare(a.Position, b.Position)
	})
	var titles []string
	seen := map[string]bool{}
	add := func(id string) {
		worktree, ok := d.Worktree(id)
		if !ok || seen[worktree.RepositoryID] {
			return
		}
		repository, ok := d.Repository(worktree.RepositoryID)
		if !ok || repository.Title == "" {
			return
		}
		seen[repository.ID] = true
		titles = append(titles, repository.Title)
	}
	add(agent.Placement.PrimaryWorktreeID)
	for _, assignment := range assignments {
		add(assignment.WorktreeID)
	}
	return titles
}
