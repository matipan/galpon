package model

import (
	"regexp"
	"strconv"
	"strings"
)

// PullRequestReference identifies a PR. An empty repository matches its number
// in every repository.
type PullRequestReference struct {
	Repository string
	Number     int
}

func (r PullRequestReference) String() string { return PullRequestKey(r.Repository, r.Number) }

var (
	pullRequestNumber = regexp.MustCompile(`^#?([0-9]+)$`)
	pullRequestName   = regexp.MustCompile(`^([a-z0-9_.-]+/[a-z0-9_.-]+)#([0-9]+)$`)
	pullRequestURL    = regexp.MustCompile(`^https?://github\.com/([a-z0-9_.-]+/[a-z0-9_.-]+)/pull/([0-9]+)(?:/[^?#\s]*)?(?:[?#][^\s]*)?$`)
)

// ParsePullRequestReference accepts a number, owner/repo#number, or a GitHub
// PR URL. URL tabs, query parameters and comment fragments do not change the PR.
func ParsePullRequestReference(query string) (PullRequestReference, bool) {
	query = strings.ToLower(strings.TrimSpace(query))
	var repository, number string
	if match := pullRequestNumber.FindStringSubmatch(query); match != nil {
		number = match[1]
	} else if match := pullRequestName.FindStringSubmatch(query); match != nil {
		repository, number = match[1], match[2]
	} else if match := pullRequestURL.FindStringSubmatch(query); match != nil {
		repository, number = match[1], match[2]
	} else {
		return PullRequestReference{}, false
	}
	n, err := strconv.ParseInt(number, 10, 32)
	if err != nil || n < 1 {
		return PullRequestReference{}, false
	}
	return PullRequestReference{Repository: repository, Number: int(n)}, true
}

// PullRequestCreator is the recorded creator of a PR, independent of the
// GitHub sync and the current branch. Hidden agents keep their hidden state.
type PullRequestCreator struct {
	Repository     string `json:"repository"`
	Number         int    `json:"number"`
	Agent          Agent  `json:"agent"`
	WorkspaceTitle string `json:"workspaceTitle"`
}

func (c PullRequestCreator) Ref() string { return PullRequestKey(c.Repository, c.Number) }
