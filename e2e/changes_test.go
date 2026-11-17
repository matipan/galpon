package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/changes"
	"github.com/matipan/galpon/internal/changes/diff"
	"github.com/matipan/galpon/internal/model"
)

// The Changes view reads the work of an agent through the daemon: the
// checkout of its placement, its branch and commits, and the diff of the
// branch with its uncommitted work. A viewed mark follows the content of a
// file.
func TestChangesViewReadsTheWorkOfAnAgent(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+testConfigDir(t),
		"GALPON_STATE_DIR="+stateDir,
		"GIT_AUTHOR_NAME=Galpon E2E", "GIT_AUTHOR_EMAIL=galpon@example.invalid",
		"GIT_COMMITTER_NAME=Galpon E2E", "GIT_COMMITTER_EMAIL=galpon@example.invalid",
		"NO_COLOR=",
	)
	bin := filepath.Join(root, "galpon")
	runRaw(t, "..", nil, "go", "build", "-o", bin, "./cmd/galpon")
	defer func() { _ = runCommand("", env, bin, "daemon", "stop") }()

	source := createRepository(t, root)
	if err := os.WriteFile(filepath.Join(source, "billing.go"), []byte("package billing\n\nfunc Total() int {\n\treturn 1\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRaw(t, source, nil, "git", "add", "billing.go")
	runRaw(t, source, nil, "git", "commit", "-m", "Add billing")
	var repository model.Repository
	decodeCommand(t, &repository, runRaw(t, "", env, bin, "repo", "add", source))
	var workspace model.Workspace
	decodeCommand(t, &workspace, runRaw(t, "", env, bin, "workspace", "create", "Billing"))
	client := app.NewClient(filepath.Join(stateDir, "galpon.sock"))
	agent, err := client.CreateAgent(t.Context(), app.CreateAgentRequest{Title: "Invoices", WorkspaceID: workspace.ID,
		Placement: app.AgentPlacementRequest{Type: "worktrees", Worktrees: []app.AgentPlacementWorktreeRequest{{RepositoryID: repository.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := client.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var worktree model.Worktree
	for _, candidate := range dashboard.Worktrees {
		if candidate.ID == agent.Placement.Worktrees[0].WorktreeID {
			worktree = candidate
		}
	}
	if worktree.Path == "" {
		t.Fatalf("no worktree for the placement %#v", agent.Placement)
	}

	// The agent commits one change, then edits the file again and adds a
	// file that Git does not track yet.
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(worktree.Path, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("billing.go", "package billing\n\nfunc Total() int {\n\treturn 2\n}\n")
	runRaw(t, worktree.Path, env, "git", "commit", "-am", "Return two")
	write("billing.go", "package billing\n\nfunc Total() int {\n\treturn 2\n}\n\nfunc Tax() int {\n\treturn 3\n}\n")
	write("notes.txt", "draft\n")

	var work changes.Work
	decodeCommand(t, &work, runRaw(t, "", env, bin, "herdr-app", "changes", "work", agent.ID))
	if len(work.Repositories) != 1 || len(work.Repositories[0].Checkouts) != 1 {
		t.Fatalf("work %#v, want one checkout", work)
	}
	checkout := work.Repositories[0].Checkouts[0]
	if checkout.Dirty.Unstaged != 1 || checkout.Dirty.Untracked != 1 {
		t.Fatalf("dirty %#v, want one changed and one untracked file", checkout.Dirty)
	}
	if len(checkout.Branches) == 0 {
		t.Fatalf("checkout %#v has no branch", checkout)
	}
	branch := checkout.Branches[0]
	if !branch.Live || branch.State != changes.StateLive || branch.Ahead != 1 || len(branch.Commits) != 1 || branch.Commits[0].Subject != "Return two" {
		t.Fatalf("branch %#v, want the live branch with one commit", branch)
	}
	if work.Default == nil || work.Default.Set != "branch:"+branch.Name {
		t.Fatalf("default %#v, want the branch total of %s", work.Default, branch.Name)
	}

	readDiff := func(args ...string) changes.Diff {
		t.Helper()
		var out changes.Diff
		base := []string{"herdr-app", "changes", "diff", agent.ID, "--checkout", checkout.Key, "--set", "branch:" + branch.Name, "--base", branch.Base.MergeBase}
		decodeCommand(t, &out, runRaw(t, "", env, bin, append(base, args...)...))
		return out
	}
	total := readDiff()
	files := map[string]changes.File{}
	for _, file := range total.Files {
		files[file.Path] = file
	}
	billing, notes := files["billing.go"], files["notes.txt"]

	// The view reads it all in one call: the work, the change set that it
	// resolves to, here the default, which follows the checkout, the diff
	// of that set, and a fingerprint of each checkout.
	var view struct {
		Selection    *changes.Pick     `json:"selection"`
		Diff         *changes.Diff     `json:"diff"`
		Fingerprints map[string]string `json:"fingerprints"`
	}
	decodeCommand(t, &view, runRaw(t, "", env, bin, "herdr-app", "changes", "view", agent.ID))
	if view.Selection == nil || !view.Selection.Follow || view.Selection.Set != "branch:"+branch.Name {
		t.Fatalf("view selection %#v, want the branch total of %s that follows the checkout", view.Selection, branch.Name)
	}
	if view.Diff == nil || len(view.Diff.Files) != len(total.Files) || view.Fingerprints[worktree.Path] == "" {
		t.Fatalf("view diff %#v and fingerprints %v, want the diff of the branch total and a fingerprint", view.Diff, view.Fingerprints)
	}
	if billing.Added != 5 || billing.Removed != 1 {
		t.Fatalf("billing.go +%d -%d, want +5 -1", billing.Added, billing.Removed)
	}
	// The rows that only the worktree has carry the uncommitted mark; the
	// committed edit does not.
	var uncommitted []string
	for _, row := range billing.Rows {
		if row.Kind == diff.RowAdded && row.Flags&diff.FlagUncommitted != 0 {
			var text strings.Builder
			for _, seg := range row.Segs {
				text.WriteString(seg.Text)
			}
			uncommitted = append(uncommitted, text.String())
		}
	}
	marked := strings.Join(uncommitted, "\n")
	if !strings.Contains(marked, "func Tax() int {") || !strings.Contains(marked, "return 3") || strings.Contains(marked, "return 2") {
		t.Fatalf("uncommitted rows %q, want the lines of Tax and not the committed edit", uncommitted)
	}
	if notes.Status != "untracked" {
		t.Fatalf("notes.txt is %q, want untracked", notes.Status)
	}
	if !strings.Contains(entityNames(billing), "added Tax") {
		t.Fatalf("billing.go entities %s, want the added function Tax", entityNames(billing))
	}

	marks := `[{"checkout":"` + checkout.Key + `","path":"billing.go","content":"` + billing.Content + `","viewed":true}]`
	runRaw(t, "", env, bin, "herdr-app", "changes", "mark", agent.ID, marks)
	for _, file := range readDiff().Files {
		if file.Path == "billing.go" && !file.Viewed {
			t.Fatal("billing.go is not viewed after the mark")
		}
	}
	before := runRaw(t, "", env, bin, "herdr-app", "changes", "fingerprint", worktree.Path)
	write("billing.go", "package billing\n\nfunc Total() int {\n\treturn 2\n}\n\nfunc Tax() int {\n\treturn 4\n}\n")
	if after := runRaw(t, "", env, bin, "herdr-app", "changes", "fingerprint", worktree.Path); after == before {
		t.Fatal("the fingerprint did not change after an edit")
	}
	for _, file := range readDiff().Files {
		if file.Path == "billing.go" && (file.Viewed || !file.Changed) {
			t.Fatalf("billing.go %#v, want it changed after the view", file)
		}
	}
}

func entityNames(file changes.File) string {
	var names []string
	for _, entity := range file.Entities {
		names = append(names, entity.Change+" "+entity.Name)
	}
	return strings.Join(names, ", ")
}
