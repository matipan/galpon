package factory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

type Reconciler struct {
	Store  *Store
	Galpon *app.Client
	GitHub GitHub
	mu     sync.Mutex
}

func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Tick(ctx)
		}
	}
}
func (r *Reconciler) Tick(ctx context.Context) {
	if !r.mu.TryLock() {
		return
	}
	defer r.mu.Unlock()
	orders, err := r.Store.List(ctx)
	if err != nil {
		return
	}
	for _, w := range orders {
		if w.Status == "active" {
			if err := r.reconcile(ctx, w); err != nil {
				_ = r.Store.Fail(ctx, w.ID, err)
				_ = r.Store.Event(ctx, w.ID, "blocked", err.Error())
			}
		}
	}
}

func (r *Reconciler) Delete(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	runs, err := r.Store.Runs(ctx, id)
	if err != nil {
		return err
	}
	stopped := map[string]bool{}
	for _, run := range runs {
		if run.Status != "running" || run.AgentID == "" || stopped[run.AgentID] || r.Galpon == nil {
			continue
		}
		view, viewErr := r.Galpon.Agent(ctx, run.AgentID)
		if viewErr != nil {
			return fmt.Errorf("inspect active agent before feature deletion: %w", viewErr)
		}
		stopped[run.AgentID] = true
		if view.Agent.RuntimeID == "" || view.Agent.Status == "stopped" || view.Agent.Status == "failed" {
			continue
		}
		if stopErr := r.Galpon.StopRuntime(ctx, run.AgentID, view.Agent.RuntimeID, "Factory feature deleted by operator"); stopErr != nil {
			return fmt.Errorf("stop active agent before feature deletion: %w", stopErr)
		}
	}
	return r.Store.Delete(ctx, id)
}

func (r *Reconciler) reconcile(ctx context.Context, w WorkOrder) error {
	switch w.Stage {
	case StageIntake:
		if w.IssueURL != "" {
			issue, err := r.GitHub.Issue(ctx, w.RepositoryPath, w.IssueURL)
			if err != nil {
				return err
			}
			if strings.TrimSpace(w.Request) == "" {
				w.Request = issue.Body
			}
			if strings.TrimSpace(w.Title) == "" {
				w.Title = issue.Title
			}
			_, err = r.Store.db.ExecContext(ctx, `UPDATE work_orders SET title=?,request=?,updated_at=? WHERE id=?`, w.Title, w.Request, time.Now().UnixMilli(), w.ID)
			if err != nil {
				return err
			}
		}
		if strings.TrimSpace(w.Request) == "" {
			return fmt.Errorf("feature request is empty")
		}
		if err := r.Store.SetStage(ctx, w.ID, StagePlanning, "active"); err != nil {
			return err
		}
		return r.Store.Event(ctx, w.ID, "stage", "Planning started")
	case StagePlanning:
		run, ok, err := r.latestRun(ctx, w.ID, "planner-feedback")
		if err != nil {
			return err
		}
		if !ok {
			run, ok, err = r.run(ctx, w.ID, "planner", "")
			if err != nil {
				return err
			}
			if !ok {
				return r.launch(ctx, w, "planner", "Planner", planningPrompt(w), app.AgentPlacementRequest{Type: "worktrees", Worktrees: []app.AgentPlacementWorktreeRequest{{RepositoryID: w.RepositoryID, Ref: w.BaseRef, FetchFirst: true}}})
			}
		}
		done, result, err := r.poll(ctx, run)
		if err != nil {
			return err
		}
		if !done {
			return nil
		}
		if err := r.Store.SetPlan(ctx, w.ID, result); err != nil {
			return err
		}
		if err := r.Store.SetStage(ctx, w.ID, StagePlanApproval, "waiting"); err != nil {
			return err
		}
		message := "Plan is ready for approval"
		if run.Kind == "planner-feedback" {
			message = "Revised plan is ready for approval"
		}
		return r.Store.Event(ctx, w.ID, "approval", message)
	case StageImplementation:
		run, ok, err := r.run(ctx, w.ID, "developer", "")
		if err != nil {
			return err
		}
		if !ok {
			return r.launch(ctx, w, "developer", "Developer", implementationPrompt(w), app.AgentPlacementRequest{Type: "worktrees", Worktrees: []app.AgentPlacementWorktreeRequest{{RepositoryID: w.RepositoryID, Ref: w.BaseRef, FetchFirst: true}}})
		}
		if w.DeveloperID == "" {
			if err := r.Store.SetDeveloper(ctx, w.ID, run.AgentID); err != nil {
				return err
			}
		}
		done, _, err := r.poll(ctx, run)
		if err != nil {
			return err
		}
		if !done {
			return nil
		}
		view, err := r.Galpon.Agent(ctx, run.AgentID)
		if err != nil {
			return err
		}
		commit, path, err := agentCommit(ctx, view)
		if err != nil {
			return err
		}
		if err := r.Store.SetDeveloper(ctx, w.ID, run.AgentID); err != nil {
			return err
		}
		if err := r.Store.SetCommit(ctx, w.ID, commit); err != nil {
			return err
		}
		if err := r.Store.SetStage(ctx, w.ID, StageHumanTest, "waiting"); err != nil {
			return err
		}
		return r.Store.Event(ctx, w.ID, "test", fmt.Sprintf("Implementation %s is ready for human testing in %s", short(commit), path))
	case StageReview:
		return r.reconcileReviews(ctx, w)
	case StageReviewFixes:
		return r.reconcileFixes(ctx, w)
	case StagePRCI:
		return r.reconcilePR(ctx, w)
	case StageWaitingForApproval:
		if w.PRNumber == 0 {
			return nil
		}
		view, err := r.Galpon.Agent(ctx, w.DeveloperID)
		if err != nil {
			return err
		}
		commit, path, err := agentCommit(ctx, view)
		if err != nil {
			return err
		}
		if commit != w.Commit {
			if err := r.invalidateChangedCommit(ctx, w, commit); err != nil {
				return err
			}
			return nil
		}
		pr, err := r.GitHub.PullRequest(ctx, path, w.PRNumber)
		if err != nil {
			return err
		}
		if pr.Merged {
			if err := r.Store.SetStage(ctx, w.ID, StageComplete, "complete"); err != nil {
				return err
			}
			return r.Store.Event(ctx, w.ID, "complete", "Pull request merged")
		}
		if pr.Checks != w.CIStatus {
			_ = r.Store.SetCI(ctx, w.ID, pr.Checks)
		}
	}
	return nil
}
func (r *Reconciler) launch(ctx context.Context, w WorkOrder, kind, role, prompt string, placement app.AgentPlacementRequest) error {
	promptHash := sha256.Sum256([]byte(prompt))
	key := fmt.Sprintf("factory:%s:%s:%s:%x", w.ID, kind, w.Commit, promptHash[:8])
	result, err := r.Galpon.CreateFactoryAgent(ctx, app.CreateFactoryAgentRequest{Agent: app.CreateAgentRequest{Title: w.Title + " · " + role, Role: "Factory " + role, WorkspaceID: w.WorkspaceID, Placement: placement}, Prompt: prompt}, key)
	if err != nil {
		return err
	}
	if err := r.Store.PutRun(ctx, AgentRun{WorkOrderID: w.ID, AgentID: result.Agent.ID, Kind: kind, Commit: w.Commit, Status: "running"}); err != nil {
		return err
	}
	return r.Store.Event(ctx, w.ID, "agent", role+" agent started")
}
func (r *Reconciler) run(ctx context.Context, id, kind, commit string) (AgentRun, bool, error) {
	runs, err := r.Store.Runs(ctx, id)
	if err != nil {
		return AgentRun{}, false, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Kind == kind && runs[i].Commit == commit {
			return runs[i], true, nil
		}
	}
	return AgentRun{}, false, nil
}
func (r *Reconciler) latestRun(ctx context.Context, id, kind string) (AgentRun, bool, error) {
	runs, err := r.Store.Runs(ctx, id)
	if err != nil {
		return AgentRun{}, false, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Kind == kind {
			return runs[i], true, nil
		}
	}
	return AgentRun{}, false, nil
}
func (r *Reconciler) poll(ctx context.Context, run AgentRun) (bool, string, error) {
	if run.Status == "completed" && run.Result != "" {
		return true, run.Result, nil
	}
	view, err := r.Galpon.Agent(ctx, run.AgentID)
	if err != nil {
		return false, "", err
	}
	for _, m := range view.Messages {
		if run.Status == "running" && run.Result != "" && m.ID != run.Result {
			continue
		}
		if m.Status == "completed" && m.Response != "" {
			if len(m.Response) > 128<<10 {
				return false, "", fmt.Errorf("%s agent result exceeds 128 KiB", run.Kind)
			}
			run.Status = "completed"
			run.Result = m.Response
			if err := r.Store.PutRun(ctx, run); err != nil {
				return false, "", err
			}
			return true, m.Response, nil
		}
		if m.Status == "failed" {
			return false, "", fmt.Errorf("%s agent failed: %s", run.Kind, m.LastError)
		}
	}
	return false, "", nil
}
func (r *Reconciler) reconcileReviews(ctx context.Context, w WorkOrder) error {
	view, err := r.Galpon.Agent(ctx, w.DeveloperID)
	if err != nil {
		return err
	}
	commit, _, err := agentCommit(ctx, view)
	if err != nil {
		return err
	}
	if commit != w.Commit {
		return r.invalidateChangedCommit(ctx, w, commit)
	}
	kinds := []string{"review-general", "review-simplicity", "review-security"}
	all := true
	changes := false
	for _, kind := range kinds {
		run, ok, err := r.run(ctx, w.ID, kind, w.Commit)
		if err != nil {
			return err
		}
		if !ok {
			all = false
			prompt := reviewPrompt(w, kind)
			if err := r.launch(ctx, w, kind, strings.TrimPrefix(kind, "review-"), prompt, app.AgentPlacementRequest{Type: "agent", SourceAgentID: w.DeveloperID}); err != nil {
				return err
			}
			continue
		}
		done, result, err := r.poll(ctx, run)
		if err != nil {
			return err
		}
		if !done {
			all = false
			continue
		}
		reviewView, err := r.Galpon.Agent(ctx, run.AgentID)
		if err != nil {
			return err
		}
		reviewCommit, _, err := agentCommit(ctx, reviewView)
		if err != nil {
			return fmt.Errorf("%s did not keep its review worktree clean: %w", kind, err)
		}
		if reviewCommit != w.Commit {
			return fmt.Errorf("%s changed the commit under review", kind)
		}
		if !reviewApproved(result) {
			changes = true
		}
	}
	if !all {
		return nil
	}
	if changes {
		if err := r.Store.SetStage(ctx, w.ID, StageReviewFixes, "active"); err != nil {
			return err
		}
		return r.Store.Event(ctx, w.ID, "review", "Changes requested; developer is fixing them")
	}
	if err := r.Store.SetStage(ctx, w.ID, StagePRCI, "active"); err != nil {
		return err
	}
	return r.Store.Event(ctx, w.ID, "review", "All three independent reviews approved this commit")
}
func (r *Reconciler) reconcileFixes(ctx context.Context, w WorkOrder) error {
	run, ok, err := r.run(ctx, w.ID, "fixer", w.Commit)
	if err != nil {
		return err
	}
	if !ok {
		runs, _ := r.Store.Runs(ctx, w.ID)
		var notes []string
		for _, v := range runs {
			if strings.HasPrefix(v.Kind, "review-") && v.Commit == w.Commit && !reviewApproved(v.Result) {
				notes = append(notes, v.Kind+":\n"+v.Result)
			}
		}
		message, err := r.Galpon.Send(ctx, w.DeveloperID, "Factory reviews requested fixes. Address all valid findings, run all required tests, and commit the fixes.\n\n"+strings.Join(notes, "\n\n"))
		if err != nil {
			return err
		}
		return r.Store.PutRun(ctx, AgentRun{WorkOrderID: w.ID, AgentID: w.DeveloperID, Kind: "fixer", Commit: w.Commit, Status: "running", Result: message.ID})
	}
	done, _, err := r.poll(ctx, run)
	if err != nil {
		return err
	}
	if !done {
		return nil
	}
	view, err := r.Galpon.Agent(ctx, w.DeveloperID)
	if err != nil {
		return err
	}
	commit, _, err := agentCommit(ctx, view)
	if err != nil {
		return err
	}
	if commit == w.Commit {
		return fmt.Errorf("review fixes completed without a new commit")
	}
	if err := r.Store.SetCommit(ctx, w.ID, commit); err != nil {
		return err
	}
	if err := r.Store.SetStage(ctx, w.ID, StageHumanTest, "waiting"); err != nil {
		return err
	}
	return r.Store.Event(ctx, w.ID, "test", "Review fixes are ready for human testing; previous approvals are invalid")
}
func (r *Reconciler) reconcilePR(ctx context.Context, w WorkOrder) error {
	view, err := r.Galpon.Agent(ctx, w.DeveloperID)
	if err != nil {
		return err
	}
	commit, _, err := agentCommit(ctx, view)
	if err != nil {
		return err
	}
	if commit != w.Commit {
		return r.invalidateChangedCommit(ctx, w, commit)
	}
	wt := view.Worktrees[0]
	if w.PRNumber == 0 {
		if out, err := git(ctx, wt.Path, "push", "-u", remote(wt.SourceRemote), wt.Branch); err != nil {
			return fmt.Errorf("push branch: %s", out)
		}
		pr, err := r.GitHub.FindPullRequest(ctx, wt.Path, wt.Branch)
		if err != nil {
			return err
		}
		if pr.Number == 0 {
			pr, err = r.GitHub.CreatePullRequest(ctx, wt.Path, wt.Branch, w.Title, prBody(w))
			if err != nil {
				return err
			}
		} else if strings.EqualFold(pr.State, "closed") && !pr.Merged {
			return fmt.Errorf("the pull request is closed without a merge")
		}
		if err := r.Store.SetPR(ctx, w.ID, pr.URL, pr.Number, pr.Checks); err != nil {
			return err
		}
		return r.Store.Event(ctx, w.ID, "pr", "Pull request created: "+pr.URL)
	}
	pr, err := r.GitHub.PullRequest(ctx, wt.Path, w.PRNumber)
	if err != nil {
		return err
	}
	if pr.Merged {
		if err := r.Store.SetStage(ctx, w.ID, StageComplete, "complete"); err != nil {
			return err
		}
		return r.Store.Event(ctx, w.ID, "complete", "Pull request merged")
	}
	_ = r.Store.SetCI(ctx, w.ID, pr.Checks)
	if pr.Checks == "success" || pr.Checks == "none" {
		if err := r.Store.SetStage(ctx, w.ID, StageWaitingForApproval, "waiting"); err != nil {
			return err
		}
		return r.Store.Event(ctx, w.ID, "approval", "Checks passed; merge approval is required")
	}
	if pr.Checks == "failure" {
		return fmt.Errorf("pull request checks failed")
	}
	return nil
}
func (r *Reconciler) invalidateChangedCommit(ctx context.Context, w WorkOrder, commit string) error {
	if err := r.Store.SetCommit(ctx, w.ID, commit); err != nil {
		return err
	}
	if err := r.Store.SetStage(ctx, w.ID, StageHumanTest, "waiting"); err != nil {
		return err
	}
	return r.Store.Event(ctx, w.ID, "test", "The implementation commit changed; testing and review approvals were invalidated")
}

func agentCommit(ctx context.Context, view model.AgentView) (string, string, error) {
	if len(view.Worktrees) == 0 {
		return "", "", fmt.Errorf("agent has no worktree")
	}
	path := view.Worktrees[0].Path
	status, err := git(ctx, path, "status", "--porcelain")
	if err != nil {
		return "", "", err
	}
	if status != "" {
		return "", "", fmt.Errorf("agent worktree has uncommitted changes")
	}
	commit, err := git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	return commit, path, nil
}

func git(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
func remote(v string) string {
	if strings.TrimSpace(v) == "" {
		return "origin"
	}
	return v
}
func reviewApproved(result string) bool {
	lines := strings.Split(strings.TrimSpace(result), "\n")
	return len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "FACTORY_REVIEW: APPROVED"
}
func short(v string) string {
	if len(v) > 8 {
		return v[:8]
	}
	return v
}
func planningPrompt(w WorkOrder) string {
	return "You are the planning agent for a Galpon Factory feature. Analyze the repository and request. Do not modify files. Return a complete, concrete implementation plan with affected files, risks, tests, and acceptance criteria. Request:\n\n" + w.Request
}
func implementationPrompt(w WorkOrder) string {
	return "Implement this approved Factory plan completely. Preserve existing behavior, add tests, run the project verification, and commit all changes. Do not open or merge a pull request.\n\nREQUEST:\n" + w.Request + "\n\nAPPROVED PLAN:\n" + w.Plan
}
func reviewPrompt(w WorkOrder, kind string) string {
	return "Independently review commit " + w.Commit + " for " + strings.TrimPrefix(kind, "review-") + " concerns. Inspect the diff and run useful checks. Do not modify files. End with exactly one line: FACTORY_REVIEW: APPROVED or FACTORY_REVIEW: CHANGES_REQUESTED. Before that line, give actionable findings. Request:\n" + w.Request
}
func prBody(w WorkOrder) string {
	return "## Why\n\n- " + w.Request + "\n\n## What\n\n- Implements the approved Galpon Factory plan.\n\n## How\n\n- Implemented and verified by a dedicated developer agent.\n- Reviewed independently for general quality, simplicity, and cybersecurity.\n\n## Testing\n\n- Project checks were run by the developer agent.\n\n## Notes\n\n- Factory feature: `" + w.ID + "`\n- Approved commit: `" + w.Commit + "`\n"
}
