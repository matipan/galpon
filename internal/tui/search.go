package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

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
	Delegated      bool
	CreatorTitle   string
	Score          int
}

func buildResults(d model.Dashboard, query string) []searchResult {
	var out []searchResult
	for _, ws := range d.Workspaces {
		if score, ok := fuzzyScore(ws.Title, query); ok {
			out = append(out, searchResult{Kind: resultWorkspace, ID: ws.ID, Title: ws.Title, Detail: "durable workspace", WorkspaceID: ws.ID, Score: score})
		}
	}
	agentTitles := make(map[string]string, len(d.Agents))
	for _, agent := range d.Agents {
		agentTitles[agent.ID] = agent.Title
	}
	for _, agent := range d.Agents {
		if score, ok := fuzzyScore(agent.Title, query); ok {
			workspaceTitle := "Unknown workspace"
			if ws, ok := d.Workspace(agent.WorkspaceID); ok {
				workspaceTitle = ws.Title
			}
			creatorTitle := agentTitles[agent.CreatedByAgentID]
			details := []string{workspaceTitle}
			if agent.Role != "" {
				details = append(details, agent.Role)
			}
			if agent.IsBackground() && creatorTitle != "" {
				details = append(details, "by "+creatorTitle)
			}
			if agent.Status != "" {
				details = append(details, agent.Status)
			}
			out = append(out, searchResult{Kind: resultAgent, ID: agent.ID, Title: agent.Title, Detail: strings.Join(details, "  ·  "), WorkspaceID: agent.WorkspaceID, WorkspaceTitle: workspaceTitle, WorktreeID: agent.Placement.PrimaryWorktreeID, Delegated: agent.IsBackground(), CreatorTitle: creatorTitle, Score: score})
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
	queryActive := normalizedSearchText(query) != ""
	sort.SliceStable(out, func(i, j int) bool {
		if left, right := resultOrder(out[i]), resultOrder(out[j]); left != right {
			return left < right
		}
		if queryActive && out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Kind == resultAgent {
			if left, right := strings.ToLower(out[i].Title), strings.ToLower(out[j].Title); left != right {
				return left < right
			}
			if left, right := strings.ToLower(out[i].WorkspaceTitle), strings.ToLower(out[j].WorkspaceTitle); left != right {
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

func resultOrder(item searchResult) int {
	if item.Kind == resultAgent && item.Delegated {
		return 4
	}
	switch item.Kind {
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

func normalizedSearchText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

// fuzzyScore matches only the human-facing title. Exact, prefix, word-prefix,
// and contiguous matches form explicit relevance tiers before subsequences.
// IDs, paths, status details, and conversation content stay private.
func fuzzyScore(title, query string) (int, bool) {
	title = normalizedSearchText(title)
	query = normalizedSearchText(query)
	if query == "" {
		return 0, true
	}
	if title == query {
		return 1_000_000 - len([]rune(title)), true
	}
	if strings.HasPrefix(title, query) {
		return 900_000 - len([]rune(title)), true
	}
	bestContiguous := -1
	for offset := 0; offset < len(title); {
		relative := strings.Index(title[offset:], query)
		if relative < 0 {
			break
		}
		index := offset + relative
		tier := 700_000
		previous, _ := utf8.DecodeLastRuneInString(title[:index])
		if index == 0 || !unicode.IsLetter(previous) && !unicode.IsDigit(previous) {
			tier = 800_000
		}
		score := tier - len([]rune(title[:index]))*100 - len([]rune(title))
		if score > bestContiguous {
			bestContiguous = score
		}
		_, size := utf8.DecodeRuneInString(title[index:])
		offset = index + size
	}
	if bestContiguous >= 0 {
		return bestContiguous, true
	}

	runes := []rune(title)
	needle := []rune(query)
	at := 0
	first := -1
	last := -1
	wordStarts := 0
	for index, r := range runes {
		if at >= len(needle) {
			break
		}
		if r != needle[at] {
			continue
		}
		if first < 0 {
			first = index
		}
		if index == 0 || !unicode.IsLetter(runes[index-1]) && !unicode.IsDigit(runes[index-1]) {
			wordStarts++
		}
		last = index
		at++
	}
	if at != len(needle) {
		return 0, false
	}
	span := last - first + 1
	gaps := span - len(needle)
	return 500_000 + wordStarts*1_000 - gaps*100 - first*10 - len(runes), true
}
