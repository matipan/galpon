package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type GH struct{}

func gh(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	if info, err := os.Stat(cwd); err == nil && info.IsDir() {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return out, nil
}
func (GH) Issue(ctx context.Context, cwd, url string) (Issue, error) {
	out, err := gh(ctx, cwd, "issue", "view", url, "--json", "title,body,url")
	if err != nil {
		return Issue{}, err
	}
	var v struct{ Title, Body, URL string }
	err = json.Unmarshal(out, &v)
	return Issue(v), err
}
func parsePR(out []byte) (PullRequest, error) {
	var v struct {
		Number            int
		URL, State        string
		IsDraft           bool
		MergedAt          any
		StatusCheckRollup []struct{ Conclusion, Status string }
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return PullRequest{}, err
	}
	checks := "none"
	if len(v.StatusCheckRollup) != 0 {
		checks = "success"
		for _, c := range v.StatusCheckRollup {
			state := strings.ToUpper(c.Conclusion)
			if state == "FAILURE" || state == "CANCELLED" || state == "TIMED_OUT" {
				checks = "failure"
				break
			}
			if state == "" || state == "PENDING" || strings.ToUpper(c.Status) != "COMPLETED" {
				checks = "pending"
			}
		}
	}
	return PullRequest{Number: v.Number, URL: v.URL, State: v.State, Checks: checks, Merged: v.MergedAt != nil || strings.EqualFold(v.State, "merged")}, nil
}
func (GH) FindPullRequest(ctx context.Context, cwd, head string) (PullRequest, error) {
	out, err := gh(ctx, cwd, "pr", "list", "--head", head, "--state", "all", "--limit", "1", "--json", "number,url,state,isDraft,mergedAt,statusCheckRollup")
	if err != nil {
		return PullRequest{}, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		return PullRequest{}, err
	}
	if len(raw) == 0 {
		return PullRequest{}, nil
	}
	return parsePR(raw[0])
}
func (GH) CreatePullRequest(ctx context.Context, cwd, head, title, body string) (PullRequest, error) {
	out, err := gh(ctx, cwd, "pr", "create", "--head", head, "--title", title, "--body", body)
	if err != nil {
		return PullRequest{}, err
	}
	url := strings.TrimSpace(string(out))
	view, err := gh(ctx, cwd, "pr", "view", url, "--json", "number,url,state,isDraft,mergedAt,statusCheckRollup")
	if err != nil {
		return PullRequest{URL: url}, nil
	}
	return parsePR(view)
}
func (GH) PullRequest(ctx context.Context, cwd string, number int) (PullRequest, error) {
	out, err := gh(ctx, cwd, "pr", "view", strconv.Itoa(number), "--json", "number,url,state,isDraft,mergedAt,statusCheckRollup")
	if err != nil {
		return PullRequest{}, err
	}
	return parsePR(out)
}
func (GH) Merge(ctx context.Context, cwd string, number int) error {
	_, err := gh(ctx, cwd, "pr", "merge", strconv.Itoa(number), "--auto", "--squash")
	return err
}
