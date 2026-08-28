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

type agentSwitcherState string

const (
	resultAgent      resultKind = "agent"
	resultWorkspace  resultKind = "workspace"
	resultWorktree   resultKind = "worktree"
	resultRepository resultKind = "repository"
	resultDisclosure resultKind = "disclosure"

	agentStateActive  agentSwitcherState = "active"
	agentStateChanged agentSwitcherState = "changed"
	agentStateWorking agentSwitcherState = "working"
	agentStateIdle    agentSwitcherState = "idle"
	agentStateFailed  agentSwitcherState = "failed"
)

type searchResult struct {
	Kind            resultKind
	ID              string
	Title           string
	Detail          string
	WorkspaceID     string
	WorkspaceTitle  string
	WorktreeID      string
	Delegated       bool
	CreatorTitle    string
	ParentAgentID   string
	DelegatedCount  int
	Depth           int
	ActivityAt      int64
	AgentState      agentSwitcherState
	Disclosure      string
	DisclosureCount int
	SortTitle       string
	SortOrder       int
	Score           int
}

type worktreeResultCandidate struct {
	worktree   model.Worktree
	baseTitle  string
	sortTitle  string
	sortOrder  int
	detail     string
	searchText string
}

func buildResults(d model.Dashboard, query string) []searchResult {
	var out []searchResult
	for _, ws := range d.Workspaces {
		if score, ok := fuzzyScore(ws.Title, query); ok {
			out = append(out, searchResult{Kind: resultWorkspace, ID: ws.ID, Title: ws.Title, Detail: "durable workspace", WorkspaceID: ws.ID, Score: score})
		}
	}
	agentTitles := make(map[string]string, len(d.Agents))
	delegatedCounts := make(map[string]int)
	agentActivity := effectiveAgentActivity(d.Agents)
	for _, agent := range d.Agents {
		agentTitles[agent.ID] = agent.Title
		if agent.IsBackground() && agent.CreatedByAgentID != "" {
			delegatedCounts[agent.CreatedByAgentID]++
		}
	}
	for _, agent := range d.Agents {
		if score, ok := fuzzyScore(agent.Title, query); ok {
			workspaceTitle := "Unknown workspace"
			if ws, ok := d.Workspace(agent.WorkspaceID); ok {
				workspaceTitle = ws.Title
			}
			creatorTitle := agentTitles[agent.CreatedByAgentID]
			var details []string
			if agent.Role != "" {
				details = append(details, agent.Role)
			}
			if agent.IsBackground() && creatorTitle != "" {
				details = append(details, "by "+creatorTitle)
			}
			state := agentState(agent)
			details = append(details, string(state))
			out = append(out, searchResult{Kind: resultAgent, ID: agent.ID, Title: agent.Title, Detail: strings.Join(details, "  ·  "), WorkspaceID: agent.WorkspaceID, WorkspaceTitle: workspaceTitle, WorktreeID: agent.Placement.PrimaryWorktreeID, Delegated: agent.IsBackground(), CreatorTitle: creatorTitle, ParentAgentID: agent.CreatedByAgentID, DelegatedCount: delegatedCounts[agent.ID], ActivityAt: agentActivity[agent.ID], AgentState: state, Score: score})
		}
	}
	repos := map[string]model.Repository{}
	for _, repo := range d.Repositories {
		repos[repo.ID] = repo
	}
	owners := worktreeOwnerTitles(d.Agents)
	worktreeActivity := make(map[string]int64)
	for _, agent := range d.Agents {
		for _, worktreeID := range agentWorktreeIDs(agent) {
			worktreeActivity[worktreeID] = max(worktreeActivity[worktreeID], agent.UpdatedAt)
		}
	}
	var worktrees []worktreeResultCandidate
	for _, wt := range d.Worktrees {
		ws, ok := d.Workspace(wt.WorkspaceID)
		if !ok {
			continue
		}
		repo := repos[wt.RepositoryID]
		ownerTitles := owners[wt.ID]
		ignoredBranchLabels := append([]string{ws.Title, repo.Title, "worktree"}, ownerTitles...)
		branchLabel, branchSearchText := readableWorktreeBranch(wt.Branch, ignoredBranchLabels...)
		identity := branchLabel
		if len(ownerTitles) != 0 {
			identity = strings.Join(ownerTitles, " + ")
		}
		if identity == "" {
			identity = "Workspace worktree"
		}
		title := strings.Join([]string{ws.Title, repo.Title, identity}, " · ")
		searchLabels := []string{ws.Title, repo.Title}
		searchLabels = append(searchLabels, ownerTitles...)
		if branchLabel != "" {
			searchLabels = append(searchLabels, branchLabel, branchSearchText)
		}
		worktrees = append(worktrees, worktreeResultCandidate{
			worktree:   wt,
			baseTitle:  title,
			sortTitle:  title,
			detail:     branchLabel,
			searchText: strings.Join(searchLabels, " · "),
		})
	}
	makeWorktreeTitlesDistinct(worktrees)
	for _, candidate := range worktrees {
		if score, ok := fuzzyScore(candidate.searchText, query); ok {
			wt := candidate.worktree
			out = append(out, searchResult{Kind: resultWorktree, ID: wt.ID, Title: candidate.baseTitle, Detail: candidate.detail, WorkspaceID: wt.WorkspaceID, WorktreeID: wt.ID, ActivityAt: max(wt.CreatedAt, worktreeActivity[wt.ID]), SortTitle: candidate.sortTitle, SortOrder: candidate.sortOrder, Score: score})
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
			if out[i].ActivityAt != out[j].ActivityAt {
				return out[i].ActivityAt > out[j].ActivityAt
			}
			if left, right := strings.ToLower(out[i].Title), strings.ToLower(out[j].Title); left != right {
				return left < right
			}
			if left, right := strings.ToLower(out[i].WorkspaceTitle), strings.ToLower(out[j].WorkspaceTitle); left != right {
				return left < right
			}
			return out[i].ID < out[j].ID
		}
		if out[i].Kind == resultWorktree {
			if left, right := strings.ToLower(out[i].SortTitle), strings.ToLower(out[j].SortTitle); left != right {
				return left < right
			}
			if out[i].SortTitle == out[j].SortTitle && out[i].SortOrder != out[j].SortOrder {
				return out[i].SortOrder < out[j].SortOrder
			}
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

func agentState(agent model.Agent) agentSwitcherState {
	switch agent.Status {
	case "running", "starting":
		return agentStateWorking
	case "failed":
		return agentStateFailed
	}
	if strings.TrimSpace(agent.RendererID) != "" {
		return agentStateActive
	}
	if agent.Status == "idle" && agent.CreatedAt > 0 && agent.UpdatedAt > agent.CreatedAt {
		return agentStateChanged
	}
	return agentStateIdle
}

func resultOrder(item searchResult) int {
	switch item.Kind {
	case resultAgent:
		return 0
	case resultWorkspace:
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

func effectiveAgentActivity(agents []model.Agent) map[string]int64 {
	activity := make(map[string]int64, len(agents))
	children := make(map[string][]string)
	for _, agent := range agents {
		activity[agent.ID] = agent.UpdatedAt
		if agent.CreatedByAgentID != "" {
			children[agent.CreatedByAgentID] = append(children[agent.CreatedByAgentID], agent.ID)
		}
	}
	resolved := make(map[string]bool, len(agents))
	visiting := make(map[string]bool, len(agents))
	var resolve func(string) int64
	resolve = func(id string) int64 {
		if resolved[id] {
			return activity[id]
		}
		if visiting[id] {
			return activity[id]
		}
		visiting[id] = true
		for _, childID := range children[id] {
			activity[id] = max(activity[id], resolve(childID))
		}
		delete(visiting, id)
		resolved[id] = true
		return activity[id]
	}
	for _, agent := range agents {
		resolve(agent.ID)
	}
	return activity
}

func agentWorktreeIDs(agent model.Agent) []string {
	worktreeIDs := make(map[string]bool, len(agent.Placement.Worktrees)+1)
	if agent.Placement.PrimaryWorktreeID != "" {
		worktreeIDs[agent.Placement.PrimaryWorktreeID] = true
	}
	for _, assignment := range agent.Placement.Worktrees {
		if assignment.WorktreeID != "" {
			worktreeIDs[assignment.WorktreeID] = true
		}
	}
	out := make([]string, 0, len(worktreeIDs))
	for worktreeID := range worktreeIDs {
		out = append(out, worktreeID)
	}
	return out
}

func worktreeOwnerTitles(agents []model.Agent) map[string][]string {
	owners := make(map[string]map[string]bool)
	for _, agent := range agents {
		title := strings.TrimSpace(agent.Title)
		if title == "" {
			continue
		}
		for _, worktreeID := range agentWorktreeIDs(agent) {
			if owners[worktreeID] == nil {
				owners[worktreeID] = make(map[string]bool)
			}
			owners[worktreeID][title] = true
		}
	}
	out := make(map[string][]string, len(owners))
	for worktreeID, titles := range owners {
		for title := range titles {
			out[worktreeID] = append(out[worktreeID], title)
		}
		sort.Slice(out[worktreeID], func(i, j int) bool {
			left, right := strings.ToLower(out[worktreeID][i]), strings.ToLower(out[worktreeID][j])
			if left != right {
				return left < right
			}
			return out[worktreeID][i] < out[worktreeID][j]
		})
	}
	return out
}

func readableWorktreeBranch(branch string, ignoredLabels ...string) (string, string) {
	parts := strings.Split(strings.TrimSpace(branch), "/")
	generated := len(parts) > 0 && strings.EqualFold(strings.TrimSpace(parts[0]), "galpon")
	ignored := make(map[string]bool, len(ignoredLabels))
	if generated {
		parts = parts[1:]
		for _, label := range ignoredLabels {
			ignored[branchLabelKey(label)] = true
		}
	}
	cleaned := make([]string, 0, len(parts))
	readable := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if generated {
			part = trimGeneratedIDSuffix(part)
		}
		if part == "" || generated && ignored[branchLabelKey(part)] {
			continue
		}
		cleaned = append(cleaned, part)
		words := strings.FieldsFunc(part, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if len(words) != 0 {
			readable = append(readable, strings.Join(words, " "))
		}
	}
	return strings.Join(readable, " / "), strings.Join(cleaned, "/")
}

func branchLabelKey(value string) string {
	words := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(value)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return strings.Join(words, "-")
}

func trimGeneratedIDSuffix(value string) string {
	separator := strings.LastIndex(value, "-")
	if separator < 0 || len(value)-separator-1 != 8 {
		return value
	}
	for _, r := range value[separator+1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return value
		}
	}
	return strings.TrimSuffix(value[:separator], "-")
}

func makeWorktreeTitlesDistinct(candidates []worktreeResultCandidate) {
	groups := make(map[string][]int)
	for index, candidate := range candidates {
		groups[candidate.baseTitle] = append(groups[candidate.baseTitle], index)
	}
	for _, indexes := range groups {
		if len(indexes) < 2 {
			continue
		}
		sort.SliceStable(indexes, func(i, j int) bool {
			left := candidates[indexes[i]].worktree
			right := candidates[indexes[j]].worktree
			if left.CreatedAt != right.CreatedAt {
				return left.CreatedAt < right.CreatedAt
			}
			return left.ID < right.ID
		})
		for number, index := range indexes {
			candidates[index].sortOrder = number + 1
			candidates[index].baseTitle += fmt.Sprintf(" · %d", number+1)
		}
	}
}

// fuzzyScore matches only human-facing titles and labels. Exact, prefix,
// word-prefix, and contiguous matches form explicit relevance tiers before
// subsequences. IDs, paths, status details, and conversation content stay private.
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
