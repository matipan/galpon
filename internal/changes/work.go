// Package changes reads the Git work of an agent for the Changes view: the
// checkouts of its placement, the branches that hold its work, their commits,
// and the diff of a change set. It only reads the worktrees. It writes Git
// objects only for the versions of files that the person marks as viewed, and
// it keeps its own state in the Galpon state folder.
package changes

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matipan/galpon/internal/github"
	"github.com/matipan/galpon/internal/model"
)

// Branch states.
const (
	StateLive       = "live"
	StateReview     = "review"
	StateLocal      = "local"
	StateUnfinished = "unfinished"
	StateMerged     = "merged"
	StateClosed     = "closed"
)

// idleAfter is how long a branch without a pull request stays ongoing after
// its last commit.
const idleAfter = 14 * 24 * time.Hour

// maxCommits is the most commits that a branch lists.
const maxCommits = 50

// branchReaders is the most branches of a checkout that read Git at once,
// and checkoutReaders the most checkouts.
const (
	branchReaders   = 6
	checkoutReaders = 4
)

// Source is what the view knows from Galpon about an agent.
type Source struct {
	Agent        model.Agent
	Dashboard    model.Dashboard
	PullRequests []model.PullRequest
	StateDir     string
	Now          time.Time
}

// Work is the Git work of an agent.
type Work struct {
	AgentID      string       `json:"agentId"`
	AgentTitle   string       `json:"agentTitle"`
	Repositories []Repository `json:"repositories"`
	Default      *Selection   `json:"default,omitempty"`
	Notices      []string     `json:"notices,omitempty"`
}

// Selection names a change set: a checkout, a branch and a set.
type Selection struct {
	Checkout string `json:"checkout"`
	Branch   string `json:"branch"`
	Set      string `json:"set"`
}

// Repository groups the checkouts of one repository.
type Repository struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Checkouts []Checkout `json:"checkouts"`
}

// Checkout is a worktree of the placement, or the folder of the agent when it
// is in a Git repository.
type Checkout struct {
	Key        string    `json:"key"`
	WorktreeID string    `json:"worktreeId,omitempty"`
	Path       string    `json:"path"`
	Primary    bool      `json:"primary,omitempty"`
	External   bool      `json:"external,omitempty"`
	Missing    bool      `json:"missing,omitempty"`
	SharedWith int       `json:"sharedWith,omitempty"`
	Head       Head      `json:"head"`
	Operation  string    `json:"operation,omitempty"`
	Dirty      Dirty     `json:"dirty"`
	Upstream   *Upstream `json:"upstream,omitempty"`
	Branches   []Branch  `json:"branches"`
	Error      string    `json:"error,omitempty"`
	repository model.Repository
	baseRef    string
	created    string
	// first is true for the first checkout of its repository in the
	// placement: pull requests without a worktree go there.
	first bool
}

// Head is what a checkout has checked out.
type Head struct {
	Branch   string `json:"branch,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Detached bool   `json:"detached,omitempty"`
}

// Dirty counts the uncommitted files of a checkout.
type Dirty struct {
	Staged     int `json:"staged"`
	Unstaged   int `json:"unstaged"`
	Untracked  int `json:"untracked"`
	Conflicted int `json:"conflicted"`
}

// Upstream is the remote branch that a branch follows.
type Upstream struct {
	Name   string `json:"name"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
}

// Branch is a line of work of the agent: a local branch, or a detached
// checkout.
type Branch struct {
	Key          string       `json:"key"`
	Name         string       `json:"name,omitempty"`
	Tip          string       `json:"tip"`
	Live         bool         `json:"live,omitempty"`
	Detached     bool         `json:"detached,omitempty"`
	State        string       `json:"state"`
	Reason       string       `json:"reason,omitempty"`
	PullRequest  *PullRequest `json:"pullRequest,omitempty"`
	Base         Base         `json:"base"`
	Ahead        int          `json:"ahead"`
	Unpushed     int          `json:"unpushed"`
	LastActivity int64        `json:"lastActivity"`
	Commits      []Commit     `json:"commits"`
	MoreCommits  bool         `json:"moreCommits,omitempty"`
}

// PullRequest is the pull request of a branch.
type PullRequest struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	State      string `json:"state"`
	Draft      bool   `json:"draft,omitempty"`
	Checks     string `json:"checks,omitempty"`
	Review     string `json:"review,omitempty"`
	Attention  string `json:"attention,omitempty"`
	Reason     string `json:"reason,omitempty"`
	source     model.PullRequest
	mine       bool
}

// Source gives the Galpon record of the pull request.
func (p PullRequest) Source() model.PullRequest { return p.source }

// Base is what a branch compares with.
type Base struct {
	Ref       string `json:"ref,omitempty"`
	MergeBase string `json:"mergeBase,omitempty"`
	Behind    int    `json:"behind"`
	StackedOn string `json:"stackedOn,omitempty"`
	Missing   bool   `json:"missing,omitempty"`
}

// Commit is a commit of a branch, after its base.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Time    int64  `json:"time"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Pushed  bool   `json:"pushed,omitempty"`
}

// LoadWork reads the Git work of the agent of src.
func LoadWork(ctx context.Context, src Source) Work {
	work := Work{AgentID: src.Agent.ID, AgentTitle: src.Agent.Title}
	checkouts := placementCheckouts(ctx, src)
	if len(checkouts) == 0 {
		if src.Agent.Placement.CWD != "" {
			work.Notices = append(work.Notices, "No Git repository in "+src.Agent.Placement.CWD+".")
		} else {
			work.Notices = append(work.Notices, "This agent has no worktree.")
		}
		return work
	}
	squash := loadSquashCache(src.StateDir)
	// Checkouts read in parallel too; the work keeps the placement order.
	var group sync.WaitGroup
	slots := make(chan struct{}, checkoutReaders)
	for index := range checkouts {
		group.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() {
				<-slots
				group.Done()
			}()
			readCheckout(ctx, src, &checkouts[index], squash)
		}()
	}
	group.Wait()
	byRepository := map[string]int{}
	for _, checkout := range checkouts {
		key := checkout.repository.ID
		if key == "" {
			key = "folder:" + checkout.Path
		}
		index, ok := byRepository[key]
		if !ok {
			title := checkout.repository.Title
			if title == "" {
				title = filepath.Base(checkout.Path)
			}
			work.Repositories = append(work.Repositories, Repository{ID: checkout.repository.ID, Title: title})
			index = len(work.Repositories) - 1
			byRepository[key] = index
		}
		work.Repositories[index].Checkouts = append(work.Repositories[index].Checkouts, checkout)
	}
	squash.save()
	for i := range work.Repositories {
		dedupeBranches(work.Repositories[i].Checkouts)
	}
	work.Default = defaultSelection(work)
	return work
}

// dedupeBranches keeps each branch of a repository in one checkout: the one
// that shows it, else the first one. The checkouts of a repository share
// their branches, so a branch can be in the history of several of them.
func dedupeBranches(checkouts []Checkout) {
	live := map[string]bool{}
	for _, checkout := range checkouts {
		for _, branch := range checkout.Branches {
			if branch.Live && branch.Name != "" {
				live[branch.Name] = true
			}
		}
	}
	seen := map[string]bool{}
	for i := range checkouts {
		kept := checkouts[i].Branches[:0]
		for _, branch := range checkouts[i].Branches {
			if branch.Name != "" && !branch.Live && (live[branch.Name] || seen[branch.Name]) {
				continue
			}
			seen[branch.Name] = true
			kept = append(kept, branch)
		}
		checkouts[i].Branches = kept
	}
}

// placementCheckouts lists the checkouts of the placement: its worktrees in
// placement order, else the folder of the agent when it is in a repository.
func placementCheckouts(ctx context.Context, src Source) []Checkout {
	worktrees := map[string]model.Worktree{}
	for _, worktree := range src.Dashboard.Worktrees {
		worktrees[worktree.ID] = worktree
	}
	repositories := map[string]model.Repository{}
	for _, repository := range src.Dashboard.Repositories {
		repositories[repository.ID] = repository
	}
	placed := append([]model.AgentWorktree(nil), src.Agent.Placement.Worktrees...)
	sort.SliceStable(placed, func(i, j int) bool { return placed[i].Position < placed[j].Position })
	var out []Checkout
	firsts := map[string]bool{}
	for index, item := range placed {
		worktree, ok := worktrees[item.WorktreeID]
		if !ok {
			continue
		}
		checkout := Checkout{
			Key: worktree.ID, WorktreeID: worktree.ID, Path: worktree.Path,
			repository: repositories[worktree.RepositoryID],
		}
		checkout.Primary = worktree.ID == src.Agent.Placement.PrimaryWorktreeID || (src.Agent.Placement.PrimaryWorktreeID == "" && index == 0)
		checkout.baseRef = worktree.BaseRef
		checkout.created = worktree.Branch
		checkout.first = !firsts[worktree.RepositoryID]
		firsts[worktree.RepositoryID] = true
		for _, agent := range src.Dashboard.Agents {
			if agent.ID == src.Agent.ID {
				continue
			}
			for _, other := range agent.Placement.Worktrees {
				if other.WorktreeID == worktree.ID {
					checkout.SharedWith++
				}
			}
		}
		out = append(out, checkout)
	}
	if len(out) == 0 && src.Agent.Placement.CWD != "" {
		if top, err := git(ctx, src.Agent.Placement.CWD, "rev-parse", "--show-toplevel"); err == nil {
			out = append(out, Checkout{Key: "folder", Path: strings.TrimSpace(top), Primary: true, External: true, first: true})
		}
	}
	return out
}

// readCheckout fills a checkout from Git: its head, its uncommitted files and
// its branches.
func readCheckout(ctx context.Context, src Source, checkout *Checkout, squash *squashCache) {
	checkout.Branches = []Branch{}
	if info, err := os.Stat(checkout.Path); err != nil || !info.IsDir() {
		checkout.Missing = true
		return
	}
	status, err := git(ctx, checkout.Path, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		checkout.Error = err.Error()
		return
	}
	readStatus(status, checkout)
	checkout.Operation = operation(ctx, checkout.Path)
	bases := baseCandidates(ctx, checkout)
	refs := localBranches(ctx, checkout.Path)
	pulls := agentPullRequests(src, checkout)
	candidates := branchCandidates(ctx, checkout, refs, pulls)
	// Each branch takes a few Git commands, so the branches of a checkout
	// read in parallel, and keep their order.
	filled := make([]Branch, len(candidates))
	var group sync.WaitGroup
	slots := make(chan struct{}, branchReaders)
	for index, name := range candidates {
		ref := refs[name]
		branch := Branch{Key: name, Name: name, Tip: ref.commit, LastActivity: ref.time * 1000}
		branch.Live = !checkout.Head.Detached && checkout.Head.Branch == name
		if pull, ok := pulls[name]; ok {
			branch.PullRequest = &pull
		}
		group.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() {
				<-slots
				group.Done()
			}()
			fillBranch(ctx, src, checkout, &branch, bases, squash)
			filled[index] = branch
		}()
	}
	group.Wait()
	for _, branch := range filled {
		if branch.State != "" {
			checkout.Branches = append(checkout.Branches, branch)
		}
	}
	if checkout.Head.Detached && checkout.Head.Commit != "" {
		branch := Branch{Key: "detached:" + checkout.Head.Commit, Tip: checkout.Head.Commit, Live: true, Detached: true}
		if out, err := git(ctx, checkout.Path, "log", "-1", "--format=%ct", checkout.Head.Commit); err == nil {
			seconds, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
			branch.LastActivity = seconds * 1000
		}
		fillBranch(ctx, src, checkout, &branch, bases, squash)
		if branch.State == "" {
			branch.State = StateLive
		}
		checkout.Branches = append([]Branch{branch}, checkout.Branches...)
	}
	stackBranches(ctx, checkout)
	sortBranches(checkout.Branches)
}

func readStatus(status string, checkout *Checkout) {
	for _, entry := range strings.Split(status, "\x00") {
		switch {
		case strings.HasPrefix(entry, "# branch.oid "):
			if oid := strings.TrimPrefix(entry, "# branch.oid "); oid != "(initial)" {
				checkout.Head.Commit = oid
			}
		case strings.HasPrefix(entry, "# branch.head "):
			head := strings.TrimPrefix(entry, "# branch.head ")
			if head == "(detached)" {
				checkout.Head.Detached = true
			} else {
				checkout.Head.Branch = head
			}
		case strings.HasPrefix(entry, "# branch.upstream "):
			checkout.Upstream = &Upstream{Name: strings.TrimPrefix(entry, "# branch.upstream ")}
		case strings.HasPrefix(entry, "# branch.ab "):
			if checkout.Upstream != nil {
				fields := strings.Fields(strings.TrimPrefix(entry, "# branch.ab "))
				if len(fields) == 2 {
					checkout.Upstream.Ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
					checkout.Upstream.Behind, _ = strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
				}
			}
		case strings.HasPrefix(entry, "1 ") || strings.HasPrefix(entry, "2 "):
			fields := strings.Fields(entry)
			if len(fields) > 1 && len(fields[1]) == 2 {
				if fields[1][0] != '.' {
					checkout.Dirty.Staged++
				}
				if fields[1][1] != '.' {
					checkout.Dirty.Unstaged++
				}
			}
		case strings.HasPrefix(entry, "u "):
			checkout.Dirty.Conflicted++
		case strings.HasPrefix(entry, "? "):
			checkout.Dirty.Untracked++
		}
	}
}

// operation names a Git operation that waits in a checkout: a rebase, a
// merge, a cherry-pick, a revert or a bisect.
func operation(ctx context.Context, dir string) string {
	out, err := git(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return ""
	}
	gitDir := strings.TrimSpace(out)
	for _, item := range []struct{ file, name string }{
		{"rebase-merge", "rebase"}, {"rebase-apply", "rebase"}, {"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"}, {"BISECT_LOG", "bisect"},
	} {
		if _, err := os.Stat(filepath.Join(gitDir, item.file)); err == nil {
			return item.name
		}
	}
	return ""
}

// baseCandidates gives the refs that branches of a checkout can compare
// with, in order of preference: the default branch of the default remote,
// the stored base of the worktree, the same branch on the other remotes, and
// the upstream of the head. A fork can have a main far behind the main of
// the default remote, so a branch takes the candidate with the most recent
// merge base.
func baseCandidates(ctx context.Context, checkout *Checkout) []string {
	repository := checkout.repository
	var candidates []string
	add := func(ref string) {
		for _, known := range candidates {
			if known == ref {
				return
			}
		}
		if _, ok := revParse(ctx, checkout.Path, ref); ok {
			candidates = append(candidates, ref)
		}
	}
	if repository.DefaultRemote != "" && repository.DefaultBranch != "" {
		add("refs/remotes/" + repository.DefaultRemote + "/" + repository.DefaultBranch)
	}
	if base := strings.TrimSpace(checkout.baseRef); base != "" && !looksLikeCommit(base) {
		switch {
		case strings.HasPrefix(base, "refs/remotes/"):
			add(base)
		case strings.HasPrefix(base, "refs/"):
		default:
			add("refs/remotes/" + base)
			if repository.DefaultRemote != "" {
				add("refs/remotes/" + repository.DefaultRemote + "/" + base)
			}
		}
	}
	if checkout.External {
		if out, err := git(ctx, checkout.Path, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
			add(strings.TrimSpace(out))
		}
		add("refs/remotes/origin/main")
		add("refs/remotes/origin/master")
	}
	if checkout.Upstream != nil && len(candidates) == 0 {
		add("refs/remotes/" + checkout.Upstream.Name)
	}
	// A base that is the branch itself is no base.
	out := candidates[:0]
	for _, ref := range candidates {
		if checkout.Head.Branch != "" && strings.HasSuffix(ref, "/"+checkout.Head.Branch) && !strings.HasSuffix(ref, "/"+repository.DefaultBranch) {
			continue
		}
		out = append(out, ref)
	}
	return out
}

// closestBase picks the candidate base with the fewest commits of the tip
// after its merge base. It gives the ref, the merge base and that count.
func closestBase(ctx context.Context, dir string, candidates []string, tip string) (string, string, int, bool) {
	bestRef, bestBase, bestAhead, found := "", "", 0, false
	for _, ref := range candidates {
		mb, ok := mergeBase(ctx, dir, ref, tip)
		if !ok {
			continue
		}
		ahead := countRange(ctx, dir, mb+".."+tip)
		if !found || ahead < bestAhead {
			bestRef, bestBase, bestAhead, found = ref, mb, ahead, true
		}
	}
	return bestRef, bestBase, bestAhead, found
}

func looksLikeCommit(value string) bool {
	if len(value) < 7 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

type localRef struct {
	commit   string
	upstream string
	time     int64
}

func localBranches(ctx context.Context, dir string) map[string]localRef {
	out, err := git(ctx, dir, "for-each-ref", "--format=%(refname:short)%00%(objectname)%00%(upstream:short)%00%(committerdate:unix)", "refs/heads")
	refs := map[string]localRef{}
	if err != nil {
		return refs
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(line, "\x00")
		if len(fields) != 4 {
			continue
		}
		seconds, _ := strconv.ParseInt(fields[3], 10, 64)
		refs[fields[0]] = localRef{commit: fields[1], upstream: fields[2], time: seconds}
	}
	return refs
}

// agentPullRequests gives the pull requests of the agent in the repository of
// a checkout, by head branch.
func agentPullRequests(src Source, checkout *Checkout) map[string]PullRequest {
	out := map[string]PullRequest{}
	names := map[string]bool{}
	for _, remote := range checkout.repository.Remotes {
		if name := github.RepositoryName(remote.FetchURL); name != "" {
			names[name] = true
		}
	}
	mine := func(pr model.PullRequest) bool {
		return pr.AgentID == src.Agent.ID || (checkout.WorktreeID != "" && pr.WorktreeID == checkout.WorktreeID)
	}
	for _, pr := range src.PullRequests {
		if pr.HeadRef == "" {
			continue
		}
		sameRepository := (checkout.repository.ID != "" && pr.RepositoryID == checkout.repository.ID) || names[strings.ToLower(pr.Repository)]
		if !sameRepository {
			continue
		}
		// The pull request of this agent wins; among others, the newest.
		if current, ok := out[pr.HeadRef]; ok {
			if mine(current.source) && !mine(pr) {
				continue
			}
			if mine(current.source) == mine(pr) && current.source.UpdatedAt >= pr.UpdatedAt {
				continue
			}
		}
		out[pr.HeadRef] = PullRequest{
			Repository: pr.Repository, Number: pr.Number, Title: pr.Title, URL: pr.URL, State: pr.State,
			Draft: pr.Draft, Checks: pr.Checks, Review: pr.Review, source: pr, mine: mine(pr),
		}
	}
	return out
}

// branchCandidates lists the branches of a checkout that can hold work of the
// agent: the current branch, the branch that Galpon created, the branches
// that the checkout had checked out, and the branches of the pull requests of
// the agent. Only local branches that exist count, and not the default
// branch.
func branchCandidates(ctx context.Context, checkout *Checkout, refs map[string]localRef, pulls map[string]PullRequest) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || name == checkout.repository.DefaultBranch || name == "HEAD" {
			return
		}
		if _, ok := refs[name]; !ok {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	if !checkout.Head.Detached {
		add(checkout.Head.Branch)
	}
	add(checkout.created)
	if reflog, err := git(ctx, checkout.Path, "reflog", "show", "--format=%gs", "-n", "300", "HEAD"); err == nil {
		for _, line := range strings.Split(reflog, "\n") {
			move, ok := strings.CutPrefix(line, "checkout: moving from ")
			if !ok {
				continue
			}
			from, to, _ := strings.Cut(move, " to ")
			add(to)
			add(from)
		}
	}
	// The local branches of a mirror hold the branches of every agent, so
	// only the pull requests of this agent add branches.
	names := make([]string, 0, len(pulls))
	for name, pull := range pulls {
		linked := pull.source.WorktreeID
		if pull.mine && (linked == checkout.WorktreeID || (linked == "" && checkout.first)) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		add(name)
	}
	return out
}

// fillBranch sets the base, the counts, the state and the commits of a
// branch. It leaves the state empty for a branch without work that the
// checkout does not show now.
func fillBranch(ctx context.Context, src Source, checkout *Checkout, branch *Branch, bases []string, squash *squashCache) {
	dir := checkout.Path
	tip := branch.Tip
	base, mb, ahead, ok := closestBase(ctx, dir, bases, tip)
	if ok {
		branch.Base.Ref, branch.Base.MergeBase, branch.Ahead = base, mb, ahead
		branch.Base.Behind = countRange(ctx, dir, tip+".."+base)
	} else {
		branch.Base.Missing = true
	}
	if branch.Base.Missing {
		branch.Ahead = countRange(ctx, dir, tip)
	}
	dirty := checkout.Dirty.Staged+checkout.Dirty.Unstaged+checkout.Dirty.Untracked+checkout.Dirty.Conflicted > 0
	pr := branch.PullRequest
	switch {
	case pr != nil && pr.State == "OPEN":
		branch.State = StateReview
	case pr != nil && pr.State == "MERGED":
		branch.State, branch.Reason = StateMerged, "PR #"+strconv.Itoa(pr.Number)+" merged"
	case pr != nil && pr.State == "CLOSED":
		branch.State, branch.Reason = StateClosed, "PR #"+strconv.Itoa(pr.Number)+" closed"
	case branch.Ahead == 0:
		if branch.Live {
			branch.State = StateLive
		}
	case !branch.Base.Missing && squash.merged(ctx, dir, base, branch.Base.MergeBase, tip):
		branch.State, branch.Reason = StateMerged, "squash merged into "+shortRef(base)
	case branch.Live:
		branch.State = StateLive
	case src.Now.Sub(time.UnixMilli(branch.LastActivity)) > idleAfter:
		branch.State = StateUnfinished
	default:
		branch.State = StateLocal
	}
	// A done branch that the checkout still shows with uncommitted work is
	// ongoing work.
	if branch.Live && dirty && (branch.State == StateMerged || branch.State == StateClosed) {
		branch.State = StateLive
	}
	if branch.State == "" {
		return
	}
	branch.Commits, branch.MoreCommits, branch.Unpushed = branchCommits(ctx, dir, branch)
}

func shortRef(ref string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ref, "refs/remotes/"), "refs/heads/")
}

// branchCommits lists the commits of a branch after its base, newest first,
// with their line counts and whether a remote has them.
func branchCommits(ctx context.Context, dir string, branch *Branch) ([]Commit, bool, int) {
	rng := branch.Tip
	if branch.Base.MergeBase != "" {
		rng = branch.Base.MergeBase + ".." + branch.Tip
	}
	out, err := git(ctx, dir, "log", "--format=%x1e%H%x00%ct%x00%s", "--shortstat", "-n", strconv.Itoa(maxCommits+1), rng)
	if err != nil {
		return []Commit{}, false, 0
	}
	commits := []Commit{}
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		head, stat, _ := strings.Cut(record, "\n")
		fields := strings.SplitN(head, "\x00", 3)
		if len(fields) != 3 {
			continue
		}
		seconds, _ := strconv.ParseInt(fields[1], 10, 64)
		commit := Commit{SHA: fields[0], Time: seconds * 1000, Subject: fields[2]}
		commit.Added, commit.Removed = parseShortstat(stat)
		commits = append(commits, commit)
	}
	more := len(commits) > maxCommits
	if more {
		commits = commits[:maxCommits]
	}
	unpushed := map[string]bool{}
	if list, err := git(ctx, dir, "rev-list", "-n", "1000", rng, "--not", "--remotes"); err == nil {
		for _, sha := range strings.Fields(list) {
			unpushed[sha] = true
		}
	}
	for i := range commits {
		commits[i].Pushed = !unpushed[commits[i].SHA]
	}
	return commits, more, len(unpushed)
}

func parseShortstat(stat string) (int, int) {
	added, removed := 0, 0
	for _, part := range strings.Split(stat, ",") {
		fields := strings.Fields(part)
		if len(fields) < 2 {
			continue
		}
		n, _ := strconv.Atoi(fields[0])
		switch {
		case strings.HasPrefix(fields[1], "insertion"):
			added = n
		case strings.HasPrefix(fields[1], "deletion"):
			removed = n
		}
	}
	return added, removed
}

// stackBranches puts a branch on top of another ongoing branch of the same
// checkout when it starts from that branch: its base becomes that branch, so
// its total shows only its own work.
func stackBranches(ctx context.Context, checkout *Checkout) {
	ongoing := func(b Branch) bool {
		return b.State == StateLive || b.State == StateReview || b.State == StateLocal || b.State == StateUnfinished
	}
	for i := range checkout.Branches {
		branch := &checkout.Branches[i]
		if !ongoing(*branch) || branch.Base.MergeBase == "" {
			continue
		}
		best := -1
		bestAhead := 0
		for j, parent := range checkout.Branches {
			if i == j || !ongoing(parent) || parent.Detached || parent.Ahead == 0 || parent.Tip == branch.Tip {
				continue
			}
			if !isAncestor(ctx, checkout.Path, parent.Tip, branch.Tip) {
				continue
			}
			// The closest parent has the most commits of its own.
			if best < 0 || parent.Ahead > bestAhead {
				best, bestAhead = j, parent.Ahead
			}
		}
		if best < 0 {
			continue
		}
		parent := checkout.Branches[best]
		branch.Base.StackedOn = parent.Name
		branch.Base.MergeBase = parent.Tip
		branch.Ahead = countRange(ctx, checkout.Path, parent.Tip+".."+branch.Tip)
		branch.Commits, branch.MoreCommits, branch.Unpushed = branchCommits(ctx, checkout.Path, branch)
	}
}

// sortBranches puts the branch that the checkout shows first, then branches in
// review, then the rest by last activity.
func sortBranches(branches []Branch) {
	rank := func(b Branch) int {
		switch {
		case b.Live && b.State != StateMerged && b.State != StateClosed:
			return 0
		case b.State == StateReview:
			return 1
		case b.State == StateLocal:
			return 2
		case b.State == StateUnfinished:
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(branches, func(i, j int) bool {
		if rank(branches[i]) != rank(branches[j]) {
			return rank(branches[i]) < rank(branches[j])
		}
		return branches[i].LastActivity > branches[j].LastActivity
	})
}

// defaultSelection picks the change set that the view opens: the branch total
// of the live branch of the primary checkout, else of the first ongoing
// branch.
func defaultSelection(work Work) *Selection {
	var fallback *Selection
	for _, repository := range work.Repositories {
		for _, checkout := range repository.Checkouts {
			for _, branch := range checkout.Branches {
				if branch.State == StateMerged || branch.State == StateClosed {
					continue
				}
				selection := &Selection{Checkout: checkout.Key, Branch: branch.Key, Set: "branch:" + branch.Name}
				if branch.Detached {
					selection.Set = "detached"
				}
				if checkout.Primary && branch.Live {
					return selection
				}
				if fallback == nil {
					fallback = selection
				}
			}
		}
	}
	return fallback
}
