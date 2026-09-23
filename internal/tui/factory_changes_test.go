package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/matipan/galpon/internal/factory"
	"github.com/matipan/galpon/internal/model"
)

func TestFactoryChangedFilesIncludesCommittedWorkingAndUntrackedLines(t *testing.T) {
	root := t.TempDir()
	runFactoryGit(t, root, "init")
	runFactoryGit(t, root, "config", "user.email", "factory@example.test")
	runFactoryGit(t, root, "config", "user.name", "Factory Test")
	writeFactoryTestFile(t, root, "internal/app.go", "one\ntwo\n")
	runFactoryGit(t, root, "add", ".")
	runFactoryGit(t, root, "commit", "-m", "base")
	base := strings.TrimSpace(runFactoryGit(t, root, "rev-parse", "HEAD"))

	writeFactoryTestFile(t, root, "internal/app.go", "one\nchanged\nthree\n")
	runFactoryGit(t, root, "add", "internal/app.go")
	runFactoryGit(t, root, "commit", "-m", "implement feature")
	writeFactoryTestFile(t, root, "docs/mission.md", "mission\nacceptance\n")
	changes, err := factoryChangedFiles(context.Background(), []model.Worktree{{RepositoryID: "repo", Path: root, BaseRef: base}}, []model.Repository{{ID: "repo", Title: "galpon"}})
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]factoryFileChange, len(changes))
	for _, change := range changes {
		byPath[change.Path] = change
	}
	tracked, ok := byPath["internal/app.go"]
	if !ok || tracked.Added != 2 || tracked.Removed != 1 || tracked.Binary {
		t.Fatalf("tracked change = %#v", tracked)
	}
	untracked, ok := byPath["docs/mission.md"]
	if !ok || untracked.Added != 2 || untracked.Removed != 0 || untracked.Binary {
		t.Fatalf("untracked change = %#v", untracked)
	}
}

func TestFactoryLiveAgentShowsPerFileChangeSummary(t *testing.T) {
	order := factory.WorkOrder{ID: "feature", Title: "Invitations", Stage: factory.StageImplementation, Status: "active"}
	m := &FactoryModel{
		width: 150, height: 34, orders: []factory.WorkOrder{order}, surface: "detail",
		detailRuns:    []factory.AgentRun{{WorkOrderID: order.ID, AgentID: "developer", Kind: "developer", Status: "running"}},
		agent:         &model.AgentView{Agent: model.Agent{ID: "developer"}, Worktrees: []model.Worktree{{Path: "/worktree"}}},
		filesForOrder: order.ID,
		filesForAgent: "developer",
		fileChanges: []factoryFileChange{
			{Path: "internal/invitations.go", Added: 12, Removed: 3},
			{Path: "internal/invitations_test.go", Added: 28},
		},
	}
	view := ansi.Strip(m.consoleAgentView(30, 32))
	for _, expected := range []string{"CHANGED FILES", "…/invitations.go", "+12 -3", "…/invitations_test.go", "+40 -3"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("live agent file summary does not contain %q:\n%s", expected, view)
		}
	}
	if factoryFileRefreshInterval != 5*time.Second {
		t.Fatalf("file refresh interval = %s", factoryFileRefreshInterval)
	}
}

func runFactoryGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func writeFactoryTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
