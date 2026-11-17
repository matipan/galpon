package changes

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/changes/diff"
	"github.com/matipan/galpon/internal/model"
)

// fixture is a managed repository like the ones Galpon makes: a bare mirror
// of an origin repository, with a worktree for the agent.
type fixture struct {
	t        *testing.T
	root     string
	origin   string
	mirror   string
	upstream string
	worktree string
	src      Source
}

func runGit(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, root: root, origin: filepath.Join(root, "origin.git"), mirror: filepath.Join(root, "mirror.git"), upstream: filepath.Join(root, "upstream"), worktree: filepath.Join(root, "worktree")}
	runGit(t, root, nil, "init", "--bare", "-b", "main", f.origin)
	runGit(t, root, nil, "clone", f.origin, f.upstream)
	runGit(t, f.upstream, nil, "checkout", "-b", "main")
	write(t, f.upstream, "app/main.go", "package app\n\nfunc Run() int {\n\treturn 1\n}\n")
	write(t, f.upstream, "README.md", "# App\n")
	runGit(t, f.upstream, nil, "add", "-A")
	runGit(t, f.upstream, nil, "commit", "-m", "Start")
	runGit(t, f.upstream, nil, "push", "origin", "main")
	runGit(t, root, nil, "init", "--bare", f.mirror)
	runGit(t, f.mirror, nil, "remote", "add", "origin", f.origin)
	runGit(t, f.mirror, nil, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	runGit(t, f.mirror, nil, "fetch", "origin")
	runGit(t, f.mirror, nil, "worktree", "add", "-b", "feature", f.worktree, "origin/main")
	repository := model.Repository{ID: "repo", Title: "app", MirrorPath: f.mirror, DefaultRemote: "origin", DefaultBranch: "main"}
	worktree := model.Worktree{ID: "wt", RepositoryID: "repo", Path: f.worktree, Branch: "feature", BaseRef: "origin/main"}
	agent := model.Agent{ID: "agent", Title: "Builder", Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: "wt", Worktrees: []model.AgentWorktree{{WorktreeID: "wt"}}}}
	f.src = Source{Agent: agent, Dashboard: model.Dashboard{Repositories: []model.Repository{repository}, Worktrees: []model.Worktree{worktree}, Agents: []model.Agent{agent}}, StateDir: filepath.Join(root, "state"), Now: time.Now()}
	return f
}

func (f *fixture) commit(dir, name, content, message string, env ...string) string {
	write(f.t, dir, name, content)
	runGit(f.t, dir, nil, "add", "-A")
	runGit(f.t, dir, env, "commit", "-m", message)
	return runGit(f.t, dir, nil, "rev-parse", "HEAD")
}

func findBranch(t *testing.T, work Work, name string) Branch {
	t.Helper()
	for _, repository := range work.Repositories {
		for _, checkout := range repository.Checkouts {
			for _, branch := range checkout.Branches {
				if branch.Name == name {
					return branch
				}
			}
		}
	}
	t.Fatalf("branch %s is not in the work %+v", name, work)
	return Branch{}
}

func TestWorkGivesTheStateOfEachBranch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	old := []string{"GIT_COMMITTER_DATE=2026-01-01T10:00:00", "GIT_AUTHOR_DATE=2026-01-01T10:00:00"}

	// A branch that origin squash merged, a branch in review, an idle branch
	// and a branch without commits, each checked out once in the worktree.
	runGit(t, f.worktree, nil, "checkout", "-b", "squashed")
	f.commit(f.worktree, "app/helper.go", "package app\n\nfunc Helper() int { return 2 }\n", "Add helper")
	f.commit(f.worktree, "app/helper.go", "package app\n\nfunc Helper() int { return 3 }\n", "Tune helper")
	runGit(t, f.upstream, nil, "pull", "--quiet", "origin", "main")
	f.commit(f.upstream, "app/helper.go", "package app\n\nfunc Helper() int { return 3 }\n", "Add helper (#1)")
	runGit(t, f.upstream, nil, "push", "origin", "main")
	runGit(t, f.mirror, nil, "fetch", "origin")

	runGit(t, f.worktree, nil, "checkout", "-b", "review", "origin/main")
	f.commit(f.worktree, "app/review.go", "package app\n", "Review work")
	runGit(t, f.worktree, nil, "checkout", "-b", "idle", "origin/main")
	f.commit(f.worktree, "app/idle.go", "package app\n", "Idle work", old...)
	runGit(t, f.worktree, nil, "checkout", "-b", "empty", "origin/main")

	// The live branch, and a branch on top of it.
	runGit(t, f.worktree, nil, "checkout", "feature")
	runGit(t, f.worktree, nil, "reset", "--hard", "origin/main")
	f.commit(f.worktree, "app/main.go", "package app\n\nfunc Run() int {\n\treturn 2\n}\n", "Return two")
	runGit(t, f.worktree, nil, "checkout", "-b", "stacked")
	f.commit(f.worktree, "app/stack.go", "package app\n", "Stacked work")
	runGit(t, f.worktree, nil, "checkout", "feature")
	write(t, f.worktree, "notes.txt", "draft\n")

	f.src.PullRequests = []model.PullRequest{{Repository: "acme/app", Number: 7, State: "OPEN", HeadRef: "review", AgentID: "agent", RepositoryID: "repo"}}
	work := LoadWork(ctx, f.src)

	want := map[string]string{"feature": StateLive, "review": StateReview, "idle": StateUnfinished, "squashed": StateMerged, "stacked": StateLocal}
	for name, state := range want {
		if got := findBranch(t, work, name); got.State != state {
			t.Errorf("%s has state %s, want %s", name, got.State, state)
		}
	}
	checkout := work.Repositories[0].Checkouts[0]
	for _, branch := range checkout.Branches {
		if branch.Name == "empty" {
			t.Errorf("a branch without commits shows: %+v", branch)
		}
	}
	if checkout.Branches[0].Name != "feature" || !checkout.Branches[0].Live {
		t.Errorf("the first branch is %s, want the live branch", checkout.Branches[0].Name)
	}
	if checkout.Dirty.Untracked != 1 {
		t.Errorf("untracked files %d, want 1", checkout.Dirty.Untracked)
	}
	if stacked := findBranch(t, work, "stacked"); stacked.Base.StackedOn != "feature" || stacked.Ahead != 1 {
		t.Errorf("stacked branch base %+v ahead %d, want one commit on top of feature", stacked.Base, stacked.Ahead)
	}
	if feature := findBranch(t, work, "feature"); len(feature.Commits) != 1 || feature.Commits[0].Subject != "Return two" || feature.Commits[0].Pushed {
		t.Errorf("feature commits %+v, want one unpushed commit", feature.Commits)
	}
	if work.Default == nil || work.Default.Branch != "feature" || work.Default.Set != "branch:feature" {
		t.Errorf("default %+v, want the branch total of feature", work.Default)
	}
}

func TestBranchTotalMarksUncommittedLines(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit(f.worktree, "app/main.go", "package app\n\nfunc Run() int {\n\treturn 2\n}\n", "Return two")
	write(t, f.worktree, "app/main.go", "package app\n\nfunc Run() int {\n\treturn 2\n}\n\nfunc Stop() {}\n")
	write(t, f.worktree, "app/new.go", "package app\n\nvar fresh = true\n")
	work := LoadWork(ctx, f.src)
	feature := findBranch(t, work, "feature")
	result, err := LoadDiff(ctx, f.src, Request{Checkout: "wt", Set: "branch:feature", Base: feature.Base.MergeBase})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]File{}
	for _, file := range result.Files {
		files[file.Path] = file
	}
	main, fresh := files["app/main.go"], files["app/new.go"]
	if main.Added != 3 || main.Removed != 1 {
		t.Fatalf("app/main.go +%d -%d, want +3 -1", main.Added, main.Removed)
	}
	uncommitted := map[int]bool{}
	for _, row := range main.Rows {
		if row.Kind == diff.RowAdded && row.Flags&diff.FlagUncommitted != 0 {
			uncommitted[row.New] = true
		}
	}
	if len(uncommitted) != 2 || !uncommitted[7] || uncommitted[4] {
		t.Fatalf("uncommitted rows %v, want lines 6 and 7 only", uncommitted)
	}
	if fresh.Status != "untracked" || !hasClass(fresh, "untracked") {
		t.Fatalf("app/new.go is %s with %v, want an untracked file", fresh.Status, fresh.Classes)
	}
	entities := map[string]string{}
	for _, change := range main.Entities {
		entities[change.Name] = change.Change
	}
	if entities["Run"] != "modified" || entities["Stop"] != "added" {
		t.Fatalf("entities %v, want Run modified and Stop added", entities)
	}
}

func TestCommitAndRangeSets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.commit(f.worktree, "a.txt", "one\n", "First")
	second := f.commit(f.worktree, "b.txt", "two\n", "Second")
	one, err := LoadDiff(ctx, f.src, Request{Checkout: "wt", Set: "commit:" + second})
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Files) != 1 || one.Files[0].Path != "b.txt" {
		t.Fatalf("commit files %+v, want b.txt", one.Files)
	}
	both, err := LoadDiff(ctx, f.src, Request{Checkout: "wt", Set: "range:" + first + ".." + second})
	if err != nil {
		t.Fatal(err)
	}
	if len(both.Files) != 2 {
		t.Fatalf("range files %+v, want a.txt and b.txt", both.Files)
	}
}

func TestViewedMarksFollowTheContent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	write(t, f.worktree, "app/main.go", "package app\n\nfunc Run() int {\n\treturn 2\n}\n")
	request := Request{Checkout: "wt", Set: "uncommitted"}
	first, err := LoadDiff(ctx, f.src, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != 1 || first.Files[0].Viewed {
		t.Fatalf("files %+v, want one file that is not viewed", first.Files)
	}
	if err := SetMarks(ctx, f.src.StateDir, "agent", f.worktree, []Mark{{Checkout: "wt", Path: "app/main.go", Content: first.Files[0].Content, Viewed: true}}); err != nil {
		t.Fatal(err)
	}
	second, _ := LoadDiff(ctx, f.src, request)
	if !second.Files[0].Viewed || second.Totals.Viewed != 1 {
		t.Fatalf("file %+v, want it viewed", second.Files[0])
	}
	write(t, f.worktree, "app/main.go", "package app\n\nfunc Run() int {\n\treturn 2\n}\n\nfunc Later() {}\n")
	third, _ := LoadDiff(ctx, f.src, request)
	if third.Files[0].Viewed || !third.Files[0].Changed {
		t.Fatalf("file %+v, want it changed after the view", third.Files[0])
	}
	news := 0
	for _, row := range third.Files[0].Rows {
		if row.Kind == diff.RowAdded && row.Flags&diff.FlagNew != 0 {
			news++
		}
	}
	if news != 2 {
		t.Fatalf("%d rows are new since the view, want the 2 lines of Later", news)
	}
	onlyNew, _ := LoadDiff(ctx, f.src, Request{Checkout: "wt", Set: "uncommitted", OnlyNew: true})
	if len(onlyNew.Files) != 1 || onlyNew.Files[0].Added != 2 || onlyNew.Files[0].Removed != 0 {
		t.Fatalf("only new %+v, want the 2 added lines since the view", onlyNew.Files)
	}
}

func TestReadsLeaveTheIndexAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	write(t, f.worktree, "app/main.go", "package app\n\nfunc Run() int {\n\treturn 5\n}\n")
	gitDir := runGit(t, f.worktree, nil, "rev-parse", "--absolute-git-dir")
	index := filepath.Join(gitDir, "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	// An agent holds the index lock while the view reads.
	lock := filepath.Join(gitDir, "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	work := LoadWork(ctx, f.src)
	if work.Repositories[0].Checkouts[0].Error != "" {
		t.Fatalf("the work read fails while the index is locked: %s", work.Repositories[0].Checkouts[0].Error)
	}
	if _, err := LoadDiff(ctx, f.src, Request{Checkout: "wt", Set: "branch:feature"}); err != nil {
		t.Fatalf("the diff fails while the index is locked: %v", err)
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("the view changed the index of the checkout")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("the view removed the index lock of the agent")
	}
}

func TestUntrackedFolderIsOneEntryAndGeneratedFilesFold(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		write(t, f.worktree, filepath.Join("out", "run", strings.Repeat("x", i+1)+".json"), "{}\n")
	}
	write(t, f.worktree, "go.sum", "example.com/a v1.0.0 h1:abc=\n")
	write(t, f.worktree, "app/api.pb.go", "// Code generated by protoc-gen-go. DO NOT EDIT.\npackage app\n")
	result, err := LoadDiff(ctx, f.src, Request{Checkout: "wt", Set: "uncommitted"})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]File{}
	for _, file := range result.Files {
		files[file.Path] = file
	}
	if folder := files["out/"]; folder.Count != 30 || !folder.Deferred {
		t.Fatalf("untracked folder %+v, want one entry for 30 files", folder)
	}
	if sum := files["go.sum"]; !hasClass(sum, "lockfile") || !sum.Deferred || sum.Added != 1 {
		t.Fatalf("go.sum %+v, want a folded lockfile that counts its lines", sum)
	}
	if generated := files["app/api.pb.go"]; !hasClass(generated, "generated") || !generated.Deferred {
		t.Fatalf("app/api.pb.go %+v, want a folded generated file", generated)
	}
	last := result.Files[len(result.Files)-1]
	if !folded(last) {
		t.Fatalf("the list ends with %s, want the folded files last", last.Path)
	}
	opened, err := LoadDiff(ctx, f.src, Request{Checkout: "wt", Set: "uncommitted", Files: []string{"go.sum"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range opened.Files {
		if file.Path == "go.sum" && (file.Deferred || len(file.Rows) == 0) {
			t.Fatalf("go.sum %+v, want its rows when the person opens it", file)
		}
	}
}

func TestLessPathPutsFoldersFirst(t *testing.T) {
	paths := []string{"README.md", "internal/x.go", "internal/sub/y.go", "cmd/main.go"}
	want := []string{"cmd/main.go", "internal/sub/y.go", "internal/x.go", "README.md"}
	for i := range paths {
		for j := range paths {
			if i < j && lessPath(paths[i], paths[j]) == lessPath(paths[j], paths[i]) {
				t.Fatalf("lessPath is not a strict order for %s and %s", paths[i], paths[j])
			}
		}
	}
	got := append([]string(nil), paths...)
	for i := 1; i < len(got); i++ {
		for j := i; j > 0 && lessPath(got[j], got[j-1]); j-- {
			got[j], got[j-1] = got[j-1], got[j]
		}
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("order %v, want %v", got, want)
	}
}
