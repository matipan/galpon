package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/model"
)

func TestPullRequestGroupsFollowTheNextStep(t *testing.T) {
	m := New(nil, nil)
	now := time.Now()
	recent, old := now.Add(-time.Hour).UnixMilli(), now.Add(-40*24*time.Hour).UnixMilli()
	open := func(number int, change func(*model.PullRequest)) model.PullRequest {
		pr := model.PullRequest{Repository: "acme/widgets", Number: number, State: "OPEN", Checks: "SUCCESS", Review: "REVIEW_REQUIRED", UpdatedAt: recent, AgentID: "agent"}
		change(&pr)
		return pr
	}
	m.pullRequests.PullRequests = []model.PullRequest{
		open(1, func(pr *model.PullRequest) { pr.Review = "APPROVED" }),
		open(2, func(pr *model.PullRequest) { pr.ReviewRequested, pr.AgentID = true, "" }),
		open(3, func(pr *model.PullRequest) { pr.Checks = "FAILURE" }),
		open(4, func(pr *model.PullRequest) { pr.Review, pr.Mergeable = "APPROVED", "CONFLICTING" }),
		open(5, func(pr *model.PullRequest) { pr.Checks = "PENDING" }),
		open(6, func(*model.PullRequest) {}),
		open(7, func(pr *model.PullRequest) { pr.Checks, pr.UpdatedAt = "FAILURE", old }),
		open(8, func(pr *model.PullRequest) { pr.State, pr.MergedAt = "MERGED", recent }),
	}
	m.rebuildPullRequestRows()
	want := map[int]int{
		1: pullRequestNeedsYou, 2: pullRequestNeedsYou,
		3: pullRequestAgentTurn, 4: pullRequestAgentTurn,
		5: pullRequestWaiting, 6: pullRequestWaiting,
		7: pullRequestStale, 8: pullRequestMerged,
	}
	last := -1
	for _, row := range m.pullRequestRows {
		if row.Group != want[row.PR.Number] {
			t.Errorf("pull request %d is in %s, want %s", row.PR.Number, pullRequestGroups[row.Group], pullRequestGroups[want[row.PR.Number]])
		}
		if row.Group < last {
			t.Errorf("pull request %d in %s comes after a later group", row.PR.Number, pullRequestGroups[row.Group])
		}
		last = row.Group
	}
	if got := m.pullRequestAttention(); got != 4 {
		t.Errorf("rows that need action = %d, want 4", got)
	}
	// The agent badge starts with the pull request that needs the agent.
	if rows := m.agentPullRequests("agent"); len(rows) != 6 || rows[0].Group != pullRequestAgentTurn {
		t.Errorf("agent pull requests = %d, first group %d; want 6 open rows with the agent's turn first", len(rows), rows[0].Group)
	}
}

func TestAltPOpensPullRequestsAndReturnsToTheEarlierView(t *testing.T) {
	m := New(nil, nil)
	altP := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}, Alt: true}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.controlKind != resultPullRequest {
		t.Fatalf("Shift-Tab from Agents opened %q, want Pull requests", m.controlKind)
	}
	m.setControlView(resultWorktree)
	m.query.SetValue("widgets")
	m.updateSwitcher(altP)
	if m.controlKind != resultPullRequest || m.query.Value() != "" {
		t.Fatalf("Alt-P opened %q with search %q, want Pull requests without a search", m.controlKind, m.query.Value())
	}
	m.updateSwitcher(altP)
	if m.controlKind != resultWorktree {
		t.Fatalf("Alt-P from Pull requests opened %q, want Worktrees", m.controlKind)
	}
}
