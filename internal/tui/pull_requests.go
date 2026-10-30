package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/github"
	"github.com/matipan/galpon/internal/model"
)

const resultPullRequest resultKind = "pull-request"

// Groups follow the next step: you, the agent, or nobody yet.
const (
	pullRequestNeedsYou = iota
	pullRequestAgentTurn
	pullRequestWaiting
	pullRequestMerged
	pullRequestStale
)

var pullRequestGroups = []string{"NEEDS YOU", "AGENT'S TURN", "WAITING", "MERGED · WORKTREE LEFT", "NO ACTIVITY FOR 30 DAYS"}

// staleAfter moves forgotten pull requests out of the groups that need action.
const staleAfter = 30 * 24 * time.Hour

// The popup reads the daemon data often while it is open. The daemon decides
// when to call GitHub.
const (
	pullRequestPoll        = 3 * time.Second
	pullRequestRefreshPoll = time.Second
)

type pullRequestRow struct {
	PR     model.PullRequest
	Group  int // The displayed group.
	State  int // The group from the GitHub state, also for a stale row.
	Reason string
	Label  string // The repository title, or owner/name for another remote.
}

func (r pullRequestRow) activity() int64 {
	if r.State == pullRequestMerged {
		return r.PR.MergedAt
	}
	return r.PR.UpdatedAt
}

func (r pullRequestRow) ref() string { return fmt.Sprintf("%s#%d", r.Label, r.PR.Number) }

type pullRequestsMsg struct {
	value model.PullRequests
	err   error
}

func (m *Model) maybePollPullRequests() tea.Cmd {
	if m.client == nil {
		return nil
	}
	interval := pullRequestPoll
	if m.pullRequests.Refreshing {
		interval = pullRequestRefreshPoll
	}
	if time.Since(m.pullRequestsRead) < interval {
		return nil
	}
	m.pullRequestsRead = time.Now()
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		value, err := client.PullRequests(ctx)
		return pullRequestsMsg{value: value, err: err}
	}
}

// refreshPullRequests asks the daemon to call GitHub now.
func (m *Model) refreshPullRequests() tea.Cmd {
	if m.client == nil {
		return nil
	}
	m.pullRequests.Refreshing = true
	m.pullRequestsRead = time.Now()
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := client.RefreshPullRequests(ctx); err != nil {
			return pullRequestsMsg{err: err}
		}
		value, err := client.PullRequests(ctx)
		return pullRequestsMsg{value: value, err: err}
	}
}

func (m *Model) applyPullRequests(value pullRequestsMsg) {
	m.pullRequestsLoaded = true
	m.pullRequestsErr = value.err
	if value.err == nil {
		m.pullRequests = value.value
	} else {
		m.pullRequests.Refreshing = false
	}
	m.refreshResults()
}

// togglePullRequests opens Pull requests from any view. From Pull requests,
// it returns to the earlier view.
func (m *Model) togglePullRequests() {
	next := resultPullRequest
	if m.controlKind == resultPullRequest {
		next = m.previousControlKind
		if next == "" || next == resultPullRequest {
			next = resultAgent
		}
	} else {
		m.previousControlKind = m.controlKind
	}
	m.query.SetValue("")
	m.setControlView(next)
}

func classifyPullRequest(pr model.PullRequest) (int, string) {
	switch {
	case pr.State == "MERGED":
		return pullRequestMerged, "merged · its worktree is still on the branch"
	case pr.ReviewRequested:
		return pullRequestNeedsYou, "review requested by " + pr.Author
	case pr.Mergeable == "CONFLICTING":
		return pullRequestAgentTurn, "merge conflicts"
	case pr.Checks == "FAILURE" || pr.Checks == "ERROR":
		return pullRequestAgentTurn, "checks fail"
	case pr.Review == "CHANGES_REQUESTED":
		return pullRequestAgentTurn, "changes requested"
	case pr.OpenThreads > 0 && pr.Review != "APPROVED":
		return pullRequestAgentTurn, fmt.Sprintf("%d open review threads", pr.OpenThreads)
	case pr.Draft:
		return pullRequestWaiting, "draft"
	case pr.Checks == "PENDING" || pr.Checks == "EXPECTED":
		return pullRequestWaiting, "checks in progress"
	case pr.Review == "APPROVED":
		return pullRequestNeedsYou, "approved · ready to merge"
	default:
		return pullRequestWaiting, "waiting for review"
	}
}

// rebuildPullRequestRows groups the pull requests that the daemon linked.
func (m *Model) rebuildPullRequestRows() {
	now := time.Now()
	rows := make([]pullRequestRow, 0, len(m.pullRequests.PullRequests))
	for _, pr := range m.pullRequests.PullRequests {
		row := pullRequestRow{PR: pr, Label: m.pullRequestLabel(pr)}
		row.State, row.Reason = classifyPullRequest(pr)
		row.Group = row.State
		if row.State != pullRequestMerged && now.Sub(time.UnixMilli(pr.UpdatedAt)) > staleAfter {
			row.Group = pullRequestStale
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Group != rows[j].Group {
			return rows[i].Group < rows[j].Group
		}
		return rows[i].activity() > rows[j].activity()
	})
	m.pullRequestRows = rows
}

// pullRequestLabel uses the short repository title for the default remote. A
// pull request in another remote, such as a fork, shows its GitHub name.
func (m Model) pullRequestLabel(pr model.PullRequest) string {
	repository, ok := m.dashboard.Repository(pr.RepositoryID)
	if !ok {
		return pr.Repository
	}
	for _, remote := range repository.Remotes {
		if remote.Name == repository.DefaultRemote && github.RepositoryName(remote.FetchURL) == strings.ToLower(pr.Repository) {
			return repository.Title
		}
	}
	return pr.Repository
}

func (m Model) pullRequestRow(key string) (pullRequestRow, bool) {
	for _, row := range m.pullRequestRows {
		if row.PR.Key() == key {
			return row, true
		}
	}
	return pullRequestRow{}, false
}

func (m Model) pullRequestResults() []searchResult {
	rows := make([]searchResult, 0, len(m.pullRequestRows))
	for _, row := range m.pullRequestRows {
		rows = append(rows, searchResult{Kind: resultPullRequest, ID: row.PR.Key(), Title: row.PR.Title, Detail: row.Reason})
	}
	return rows
}

// pullRequestAttention counts the rows that need a person or an agent.
func (m Model) pullRequestAttention() int {
	count := 0
	for _, row := range m.pullRequestRows {
		if row.Group == pullRequestNeedsYou || row.Group == pullRequestAgentTurn {
			count++
		}
	}
	return count
}

// agentPullRequests returns the open pull requests of an agent, most urgent first.
func (m Model) agentPullRequests(agentID string) []pullRequestRow {
	var out []pullRequestRow
	for _, row := range m.pullRequestRows {
		if row.PR.AgentID == agentID && row.State != pullRequestMerged {
			out = append(out, row)
		}
	}
	rank := map[int]int{pullRequestAgentTurn: 0, pullRequestNeedsYou: 1, pullRequestWaiting: 2, pullRequestStale: 3}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Group] < rank[out[j].Group] })
	return out
}

// pullRequestMark gives a stale row the mark of its state in a quiet color.
func pullRequestMark(row pullRequestRow) (string, lipgloss.Color) {
	mark, color := pullRequestStateMark(row)
	if row.Group == pullRequestStale {
		color = Tokyo.Comment
	}
	return mark, color
}

func pullRequestStateMark(row pullRequestRow) (string, lipgloss.Color) {
	switch {
	case row.State == pullRequestMerged:
		return consoleMark(iconSuccess), Tokyo.Comment
	case row.State == pullRequestAgentTurn:
		return consoleMark(iconFailure), Tokyo.Red
	case row.PR.ReviewRequested:
		return consoleMark(iconAttention), Tokyo.Yellow
	case row.State == pullRequestNeedsYou:
		return consoleMark(iconSuccess), Tokyo.Green
	case row.PR.Checks == "PENDING" || row.PR.Checks == "EXPECTED":
		return consoleGlyph("◐", "*"), Tokyo.Yellow
	default:
		return consoleMark(iconPending), Tokyo.Muted
	}
}

// agentPullRequestBadge is a short state such as "#5455 × +2" for agent rows.
func (m Model) agentPullRequestBadge(agentID string) (string, lipgloss.Color) {
	rows := m.agentPullRequests(agentID)
	if len(rows) == 0 {
		return "", ""
	}
	mark, color := pullRequestMark(rows[0])
	badge := fmt.Sprintf("#%d %s", rows[0].PR.Number, mark)
	if len(rows) > 1 {
		badge += fmt.Sprintf(" +%d", len(rows)-1)
	}
	return badge, color
}

func (m Model) pullRequestAgentTitle(pr model.PullRequest) string {
	if pr.AgentID == "" {
		return ""
	}
	if agent, ok := m.dashboard.Agent(pr.AgentID); ok {
		return agent.Title
	}
	if pr.AgentTitle != "" {
		return pr.AgentTitle + " (hidden)"
	}
	return ""
}

func (m Model) pullRequestRowView(item searchResult, width int, style lipgloss.Style, prefix string) string {
	row, ok := m.pullRequestRow(item.ID)
	if !ok {
		return consoleFillRow(style.Render(prefix+consoleText(item.Title)), width, style)
	}
	paint := func(text string, color lipgloss.Color) string { return style.Foreground(color).Render(text) }
	mark, color := pullRequestMark(row)
	agent := m.pullRequestAgentTitle(row.PR)
	if agent == "" {
		agent = consoleGlyph("—", "-")
	}
	age := strings.TrimSuffix(operationsObservedAge(row.activity()), " ago")
	refWidth := 0
	for _, other := range m.pullRequestRows {
		refWidth = max(refWidth, lipgloss.Width(other.ref()))
	}
	refWidth = min(refWidth, 24)
	agentWidth, ageWidth := min(24, max(10, width/5)), 4
	titleWidth := width - lipgloss.Width(prefix) - 2 - refWidth - 2 - agentWidth - 2 - ageWidth - 2
	if titleWidth < 16 {
		agentWidth, titleWidth = 0, titleWidth+agentWidth+2
	}
	text := style.Render(prefix) + paint(mark+" ", color) + paint(consoleCell(row.ref(), refWidth), Tokyo.Muted) + style.Render("  "+consoleCell(consoleText(row.PR.Title), max(4, titleWidth))+"  ")
	if agentWidth > 0 {
		text += style.Render(consoleCell(consoleText(agent), agentWidth)) + style.Render("  ")
	}
	text += paint(consoleCell(age, ageWidth), Tokyo.Muted)
	return consoleFillRow(text, width, style)
}

func pullRequestChecks(state string) string {
	switch state {
	case "SUCCESS":
		return "pass"
	case "FAILURE", "ERROR":
		return "fail"
	case "PENDING", "EXPECTED":
		return "in progress"
	case "":
		return "none"
	}
	return strings.ToLower(state)
}

func pullRequestReview(state string) string {
	switch state {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes requested"
	case "REVIEW_REQUIRED":
		return "required"
	case "":
		return "not required"
	}
	return strings.ToLower(state)
}

func pullRequestMerge(state string) string {
	switch state {
	case "MERGEABLE":
		return "no conflicts"
	case "CONFLICTING":
		return "conflicts"
	}
	return "not computed yet"
}

func (m Model) pullRequestStateLine(item searchResult) string {
	row, ok := m.pullRequestRow(item.ID)
	if !ok {
		return ""
	}
	mark, color := pullRequestMark(row)
	return lipgloss.NewStyle().Foreground(color).Render(mark + " " + row.Reason)
}

func (m Model) pullRequestFields(item searchResult, field func(string, string)) {
	row, ok := m.pullRequestRow(item.ID)
	if !ok {
		return
	}
	pr := row.PR
	field("PR", fmt.Sprintf("%s#%d", pr.Repository, pr.Number))
	if title := m.pullRequestAgentTitle(pr); title != "" {
		state := ""
		if agent, ok := m.dashboard.Agent(pr.AgentID); ok {
			_, state, _ = consoleState(agent)
			state = " · " + state
		}
		field("AGENT", title+state)
		switch pr.AgentLink {
		case "created":
			field("LINK", "Opened it with gh pr create")
		case "branch":
			field("LINK", "Has the branch checked out")
		}
	} else if !pr.ReviewRequested {
		field("AGENT", "No Galpon agent opened it")
	}
	if pr.ReviewRequested {
		field("AUTHOR", pr.Author)
	}
	if row.State != pullRequestMerged {
		field("CHECKS", pullRequestChecks(pr.Checks))
		field("REVIEW", pullRequestReview(pr.Review))
		if pr.OpenThreads > 0 {
			field("THREADS", fmt.Sprintf("%d open", pr.OpenThreads))
		}
		field("MERGE", pullRequestMerge(pr.Mergeable))
	}
	branch := pr.HeadRef
	if pr.HeadRepository != "" && !strings.EqualFold(pr.HeadRepository, pr.Repository) {
		branch = strings.SplitN(pr.HeadRepository, "/", 2)[0] + ":" + branch
	}
	field("BRANCH", branch)
	if worktree, ok := m.dashboard.Worktree(pr.WorktreeID); ok {
		field("WORKTREE", worktree.Path)
	}
	if row.State == pullRequestMerged {
		field("MERGED", operationsObservedAge(pr.MergedAt))
	} else {
		field("UPDATED", operationsObservedAge(pr.UpdatedAt))
		field("OPENED", operationsObservedAge(pr.CreatedAt))
	}
	field("URL", pr.URL)
}

func (m Model) pullRequestError() string {
	var apiErr *app.APIError
	if errors.As(m.pullRequestsErr, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return "This Galpon daemon does not have pull requests yet. Run galpon daemon restart."
	}
	if m.pullRequestsErr != nil {
		return "Galpon: " + m.pullRequestsErr.Error()
	}
	if m.pullRequests.Error != "" {
		return "GitHub: " + m.pullRequests.Error
	}
	return ""
}

// hasGitHubRemote is false when the daemon has no reason to call GitHub.
func (m Model) hasGitHubRemote() bool {
	for _, repository := range m.dashboard.Repositories {
		for _, remote := range repository.Remotes {
			if github.RepositoryName(remote.FetchURL) != "" {
				return true
			}
		}
	}
	return false
}

func (m Model) pullRequestNotice() string {
	if message := m.pullRequestError(); message != "" {
		return message
	}
	if !m.hasGitHubRemote() {
		return "No Galpon repository has a GitHub remote."
	}
	if m.pullRequests.Refreshing {
		return consoleActivityMark() + " Getting pull requests from GitHub"
	}
	if m.pullRequests.FetchedAt == 0 {
		return "No GitHub data yet · ctrl+r gets it"
	}
	return "From GitHub " + operationsObservedAge(m.pullRequests.FetchedAt) + " · Galpon repositories only · ctrl+r refreshes"
}

func (m Model) pullRequestEmpty() string {
	switch {
	case !m.pullRequestsLoaded || m.pullRequests.Refreshing && m.pullRequests.FetchedAt == 0:
		return "Getting pull requests from GitHub…"
	case m.pullRequestError() != "" && m.pullRequests.FetchedAt == 0:
		return "GitHub data is not available. Make sure that gh is installed and signed in, then press ctrl+r."
	case !m.hasGitHubRemote():
		return "Pull requests appear here when a Galpon repository has a GitHub remote."
	}
	return "No open pull requests in Galpon repositories."
}

// openPullRequest opens the agent of a pull request, or the pull request on
// GitHub when no visible agent is linked.
func (m *Model) openPullRequest(item searchResult) tea.Cmd {
	row, ok := m.pullRequestRow(item.ID)
	if !ok {
		return nil
	}
	if _, ok := m.dashboard.Agent(row.PR.AgentID); ok {
		return m.openAgent(row.PR.AgentID)
	}
	if row.PR.AgentID != "" {
		m.status = "The agent of " + row.ref() + " is hidden · press ctrl+h to show hidden agents, or ctrl+o to open GitHub"
		return nil
	}
	return m.openPullRequestOnGitHub(item)
}

func (m *Model) openPullRequestOnGitHub(item searchResult) tea.Cmd {
	row, ok := m.pullRequestRow(item.ID)
	if !ok {
		return nil
	}
	m.status = "Opened " + row.ref() + " on GitHub"
	url := row.PR.URL
	return func() tea.Msg { return actionMsg{err: openURL(url)} }
}

// openURL starts the system opener in its own session, so the browser stays
// open after the popup closes.
func openURL(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	command := exec.Command(name, url)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("open %s: %w", url, err)
	}
	go func() { _ = command.Wait() }()
	return nil
}
