package factory

import "context"

type PullRequest struct {
	Number             int
	URL, State, Checks string
	Merged             bool
}
type Issue struct{ Title, Body, URL string }

type GitHub interface {
	Issue(context.Context, string, string) (Issue, error)
	FindPullRequest(context.Context, string, string) (PullRequest, error)
	CreatePullRequest(context.Context, string, string, string, string) (PullRequest, error)
	PullRequest(context.Context, string, int) (PullRequest, error)
	Merge(context.Context, string, int) error
}
