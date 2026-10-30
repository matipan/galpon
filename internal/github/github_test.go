package github

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestSearchReadsEveryPage(t *testing.T) {
	pages := []string{
		`{"data":{"search":{"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"},"nodes":[
  {"number":7,"title":"Fix","url":"https://github.com/acme/widgets/pull/7","isDraft":false,"state":"OPEN",
   "createdAt":"2026-10-01T10:00:00Z","updatedAt":"2026-10-02T10:00:00Z","reviewDecision":"REVIEW_REQUIRED","mergeable":"MERGEABLE",
   "author":{"login":"octocat"},"repository":{"nameWithOwner":"acme/widgets"},"headRefName":"fix","headRepository":{"nameWithOwner":"octocat/widgets"},
   "commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE"}}}]},
   "reviewThreads":{"nodes":[{"isResolved":true},{"isResolved":false}]}}]}}}`,
		`{"data":{"search":{"pageInfo":{"hasNextPage":false,"endCursor":"cursor-2"},"nodes":[
  {"number":8,"title":"Docs","url":"https://github.com/acme/widgets/pull/8","state":"OPEN","createdAt":"2026-10-01T10:00:00Z","updatedAt":"2026-10-01T10:00:00Z",
   "repository":{"nameWithOwner":"acme/widgets"},"headRefName":"docs","commits":{"nodes":[]},"reviewThreads":{"nodes":[]}}]}}}`,
	}
	var afters []string
	client := Client{Run: func(_ context.Context, args ...string) ([]byte, error) {
		after := ""
		for _, arg := range args {
			if value, ok := strings.CutPrefix(arg, "after="); ok {
				after = value
			}
		}
		afters = append(afters, after)
		return []byte(pages[len(afters)-1]), nil
	}}
	pullRequests, err := client.Search(context.Background(), "is:pr is:open author:octocat", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(afters, []string{"", "cursor-1"}) {
		t.Fatalf("page cursors = %q, want the first page and then cursor-1", afters)
	}
	if len(pullRequests) != 2 {
		t.Fatalf("pull requests = %d, want 2", len(pullRequests))
	}
	first := pullRequests[0]
	if first.Checks != "FAILURE" || first.OpenThreads != 1 || first.HeadRepository != "octocat/widgets" || first.Author != "octocat" {
		t.Fatalf("first pull request = %+v", first)
	}
	if pullRequests[1].Checks != "" || pullRequests[1].Author != "" {
		t.Fatalf("a pull request without checks or author = %+v", pullRequests[1])
	}
}

func TestRepositoryNameReadsGitHubRemotes(t *testing.T) {
	for url, want := range map[string]string{
		"git@github.com:Acme/Widgets.git":      "acme/widgets",
		"https://github.com/acme/widgets":      "acme/widgets",
		"ssh://git@github.com/acme/widgets/":   "acme/widgets",
		"git@gitlab.com:acme/widgets.git":      "",
		"/home/user/code/widgets":              "",
		"https://github.com/acme/widgets/pull": "",
	} {
		if got := RepositoryName(url); got != want {
			t.Errorf("RepositoryName(%q) = %q, want %q", url, got, want)
		}
	}
}
