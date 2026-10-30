// Package github reads pull requests with the gh CLI. gh uses its own login,
// so Galpon keeps no GitHub token.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

// Runner runs gh with the arguments and returns its standard output.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// GH runs the gh CLI from PATH.
func GH(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, "gh", args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("gh %s: %s", args[0], message)
	}
	return stdout.Bytes(), nil
}

type Client struct {
	// Run defaults to GH.
	Run Runner
}

func (c Client) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Run == nil {
		return GH(ctx, args...)
	}
	return c.Run(ctx, args...)
}

// Login returns the GitHub login of the gh user.
func (c Client) Login(ctx context.Context) (string, error) {
	output, err := c.run(ctx, "api", "user", "--jq", ".login")
	if err != nil {
		return "", err
	}
	login := strings.TrimSpace(string(output))
	if login == "" {
		return "", fmt.Errorf("gh api user returned no login")
	}
	return login, nil
}

// maxResults limits one search. GitHub search returns pages of 100.
const maxResults = 300

const openFields = `number title url isDraft state createdAt updatedAt mergedAt reviewDecision mergeable
  author { login } repository { nameWithOwner } headRefName headRepository { nameWithOwner }
  commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
  reviewThreads(first: 100) { nodes { isResolved } }`

// Merged pull requests need no review or check state.
const mergedFields = `number title url state createdAt updatedAt mergedAt
  author { login } repository { nameWithOwner } headRefName headRepository { nameWithOwner }`

type owner struct {
	Login         string
	NameWithOwner string
}

type searchNode struct {
	Number         int
	Title          string
	URL            string
	IsDraft        bool
	State          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	MergedAt       *time.Time
	ReviewDecision string
	Mergeable      string
	Author         *owner
	Repository     owner
	HeadRefName    string
	HeadRepository *owner
	Commits        struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct{ State string }
			}
		}
	}
	ReviewThreads struct {
		Nodes []struct{ IsResolved bool }
	}
}

// Search returns the pull requests that match a GitHub search query, such as
// "is:pr is:open author:octocat". Merged searches ask for fewer fields.
func (c Client) Search(ctx context.Context, query string, merged bool) ([]model.PullRequest, error) {
	fields := openFields
	if merged {
		fields = mergedFields
	}
	document := `query($q: String!, $after: String) { search(query: $q, type: ISSUE, first: 100, after: $after) {
  pageInfo { hasNextPage endCursor } nodes { ... on PullRequest { ` + fields + ` } } } }`
	var out []model.PullRequest
	after := ""
	for len(out) < maxResults {
		args := []string{"api", "graphql", "-f", "query=" + document, "-f", "q=" + query}
		if after != "" {
			args = append(args, "-f", "after="+after)
		}
		output, err := c.run(ctx, args...)
		if err != nil {
			return nil, err
		}
		var response struct {
			Data struct {
				Search struct {
					PageInfo struct {
						HasNextPage bool
						EndCursor   string
					}
					Nodes []searchNode
				}
			}
		}
		if err := json.Unmarshal(output, &response); err != nil {
			return nil, fmt.Errorf("read the GitHub search result: %w", err)
		}
		for _, node := range response.Data.Search.Nodes {
			if node.Number != 0 {
				out = append(out, pullRequest(node))
			}
		}
		page := response.Data.Search.PageInfo
		if !page.HasNextPage || page.EndCursor == "" {
			break
		}
		after = page.EndCursor
	}
	return out, nil
}

func pullRequest(node searchNode) model.PullRequest {
	pr := model.PullRequest{
		Repository: node.Repository.NameWithOwner, Number: node.Number, Title: node.Title, URL: node.URL,
		Draft: node.IsDraft, State: node.State, HeadRef: node.HeadRefName, Review: node.ReviewDecision,
		Mergeable: node.Mergeable, CreatedAt: node.CreatedAt.UnixMilli(), UpdatedAt: node.UpdatedAt.UnixMilli(),
	}
	if node.Author != nil {
		pr.Author = node.Author.Login
	}
	if node.HeadRepository != nil {
		pr.HeadRepository = node.HeadRepository.NameWithOwner
	}
	if node.MergedAt != nil {
		pr.MergedAt = node.MergedAt.UnixMilli()
	}
	if commits := node.Commits.Nodes; len(commits) > 0 && commits[0].Commit.StatusCheckRollup != nil {
		pr.Checks = commits[0].Commit.StatusCheckRollup.State
	}
	for _, thread := range node.ReviewThreads.Nodes {
		if !thread.IsResolved {
			pr.OpenThreads++
		}
	}
	return pr
}

var remoteName = regexp.MustCompile(`github\.com[:/]([^/\s]+/[^/\s]+?)(?:\.git)?/?$`)

// RepositoryName returns the lowercase owner/name of a GitHub remote URL, or
// an empty string for another host.
func RepositoryName(remoteURL string) string {
	match := remoteName.FindStringSubmatch(strings.TrimSpace(remoteURL))
	if match == nil {
		return ""
	}
	return strings.ToLower(match[1])
}
