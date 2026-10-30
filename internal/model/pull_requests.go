package model

import (
	"strconv"
	"strings"
)

// PullRequest is a GitHub pull request of the signed-in gh user, or a pull
// request that asks for that user's review. Galpon adds the repository, the
// agent, and the worktree that it links to the pull request.
type PullRequest struct {
	Repository      string `json:"repository"`
	Number          int    `json:"number"`
	Title           string `json:"title"`
	URL             string `json:"url"`
	Author          string `json:"author,omitempty"`
	Draft           bool   `json:"draft,omitempty"`
	State           string `json:"state"`
	HeadRepository  string `json:"headRepository,omitempty"`
	HeadRef         string `json:"headRef,omitempty"`
	Checks          string `json:"checks,omitempty"`
	Review          string `json:"review,omitempty"`
	Mergeable       string `json:"mergeable,omitempty"`
	OpenThreads     int    `json:"openThreads,omitempty"`
	CreatedAt       int64  `json:"createdAt"`
	UpdatedAt       int64  `json:"updatedAt"`
	MergedAt        int64  `json:"mergedAt,omitempty"`
	ReviewRequested bool   `json:"reviewRequested,omitempty"`

	RepositoryID string `json:"repositoryId,omitempty"`
	AgentID      string `json:"agentId,omitempty"`
	AgentTitle   string `json:"agentTitle,omitempty"`
	// AgentLink is "created" when the agent conversation ran gh pr create, or
	// "branch" when the agent has the pull request branch checked out.
	AgentLink  string `json:"agentLink,omitempty"`
	WorktreeID string `json:"worktreeId,omitempty"`
}

func (p PullRequest) Key() string { return PullRequestKey(p.Repository, p.Number) }

// PullRequestKey identifies a pull request by its lowercase GitHub repository
// name and number.
func PullRequestKey(repository string, number int) string {
	return strings.ToLower(repository) + "#" + strconv.Itoa(number)
}

type PullRequests struct {
	Login        string        `json:"login,omitempty"`
	FetchedAt    int64         `json:"fetchedAt,omitempty"`
	Error        string        `json:"error,omitempty"`
	Refreshing   bool          `json:"refreshing,omitempty"`
	PullRequests []PullRequest `json:"pullRequests"`
}
