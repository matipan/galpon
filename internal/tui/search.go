package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/matipan/galpon/internal/model"
)

type resultKind string

const (
	resultWorkspace  resultKind = "workspace"
	resultAgent      resultKind = "agent"
	resultWorktree   resultKind = "worktree"
	resultRepository resultKind = "repository"
)

type searchResult struct {
	Kind           resultKind
	ID             string
	Title          string
	Detail         string
	WorkspaceID    string
	WorkspaceTitle string
	WorktreeID     string
	Score          int
}

func buildResults(d model.Dashboard, query string) []searchResult {
	var out []searchResult
	for _, ws := range d.Workspaces {
		if score, ok := fuzzyScore(ws.Title, query); ok {
			out = append(out, searchResult{Kind: resultWorkspace, ID: ws.ID, Title: ws.Title, Detail: "durable workspace", WorkspaceID: ws.ID, Score: score})
		}
	}
	for _, agent := range d.Agents {
		if score, ok := fuzzyScore(agent.Title, query); ok {
			workspaceTitle := "Unknown workspace"
			if ws, ok := d.Workspace(agent.WorkspaceID); ok {
				workspaceTitle = ws.Title
			}
			detail := workspaceTitle + "  ·  " + agent.Status
			if agent.Role != "" {
				detail = agent.Role + "  ·  " + detail
			}
			out = append(out, searchResult{Kind: resultAgent, ID: agent.ID, Title: agent.Title, Detail: detail, WorkspaceID: agent.WorkspaceID, WorkspaceTitle: workspaceTitle, WorktreeID: agent.Placement.PrimaryWorktreeID, Score: score})
		}
	}
	repos := map[string]model.Repository{}
	for _, repo := range d.Repositories {
		repos[repo.ID] = repo
	}
	for _, wt := range d.Worktrees {
		ws, ok := d.Workspace(wt.WorkspaceID)
		if !ok {
			continue
		}
		repo := repos[wt.RepositoryID]
		title := ws.Title + " · " + repo.Title
		if score, ok := fuzzyScore(title, query); ok {
			out = append(out, searchResult{Kind: resultWorktree, ID: wt.ID, Title: title, Detail: wt.Branch, WorkspaceID: wt.WorkspaceID, WorktreeID: wt.ID, Score: score})
		}
	}
	for _, repository := range d.Repositories {
		if score, ok := fuzzyScore(repository.Title, query); ok {
			detail := remoteCount(len(repository.Remotes))
			if repository.DefaultBranch != "" {
				detail = repository.DefaultBranch + "  ·  " + detail
			}
			out = append(out, searchResult{Kind: resultRepository, ID: repository.ID, Title: repository.Title, Detail: detail, Score: score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return groupOrder(out[i].Kind) < groupOrder(out[j].Kind)
		}
		if out[i].Kind == resultAgent {
			if left, right := strings.ToLower(out[i].WorkspaceTitle), strings.ToLower(out[j].WorkspaceTitle); left != right {
				return left < right
			}
			if out[i].WorkspaceID != out[j].WorkspaceID {
				return out[i].WorkspaceID < out[j].WorkspaceID
			}
			if left, right := strings.ToLower(out[i].Title), strings.ToLower(out[j].Title); left != right {
				return left < right
			}
			return out[i].ID < out[j].ID
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if left, right := strings.ToLower(out[i].Title), strings.ToLower(out[j].Title); left != right {
			return left < right
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func groupOrder(kind resultKind) int {
	switch kind {
	case resultWorkspace:
		return 0
	case resultAgent:
		return 1
	case resultWorktree:
		return 2
	default:
		return 3
	}
}
func groupTitle(kind resultKind) string {
	switch kind {
	case resultWorkspace:
		return "WORKSPACES"
	case resultAgent:
		return "AGENTS"
	case resultWorktree:
		return "WORKTREES"
	default:
		return "REPOSITORIES"
	}
}

func remoteCount(count int) string {
	if count == 1 {
		return "1 remote"
	}
	return fmt.Sprintf("%d remotes", count)
}

// fuzzyScore matches only the human-facing title. Consecutive and word-start
// matches rank first, while IDs, paths, and conversation content stay private.
func fuzzyScore(title, query string) (int, bool) {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return 0, true
	}
	runes := []rune(strings.ToLower(title))
	needle := []rune(query)
	at := 0
	score := 0
	last := -2
	for index, r := range runes {
		if at >= len(needle) {
			break
		}
		if r != needle[at] {
			continue
		}
		score += 10
		if index == last+1 {
			score += 12
		}
		if index == 0 || !unicode.IsLetter(runes[index-1]) && !unicode.IsDigit(runes[index-1]) {
			score += 8
		}
		last = index
		at++
	}
	if at != len(needle) {
		return 0, false
	}
	score -= len(runes) - len(needle)
	return score, true
}
