package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func TestMirrorAndManagedWorktree(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "source")
	runTest(t, "", "git", "init", "-b", "main", root)
	runTest(t, root, "git", "config", "user.name", "Galpon Test")
	runTest(t, root, "git", "config", "user.email", "galpon@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTest(t, root, "git", "add", "README.md")
	runTest(t, root, "git", "commit", "-m", "fixture")
	inspection, err := InspectRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SourcePath != root || inspection.Remotes[0].FetchURL != root || inspection.Title != "source" || inspection.DefaultBranch != "main" {
		t.Fatalf("inspect = %#v", inspection)
	}
	mirror := filepath.Join(t.TempDir(), "source.git")
	if err := CreateMirror(ctx, inspection.Remotes, inspection.DefaultRemote, inspection.PushRemote, mirror); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "worktree")
	repo := model.Repository{Title: inspection.Title, MirrorPath: mirror, DefaultRemote: inspection.DefaultRemote, PushRemote: inspection.PushRemote, Remotes: inspection.Remotes, DefaultBranch: inspection.DefaultBranch}
	if err := CreateWorktree(ctx, repo, worktree, "galpon/test", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "README.md")); err != nil {
		t.Fatal(err)
	}
	if got := runTest(t, worktree, "git", "branch", "--show-current"); got != "galpon/test" {
		t.Fatalf("branch = %q", got)
	}
}

func TestRemoteRepositoryInputAndWorktree(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "source")
	runTest(t, "", "git", "init", "-b", "main", root)
	runTest(t, root, "git", "config", "user.name", "Galpon Test")
	runTest(t, root, "git", "config", "user.email", "galpon@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTest(t, root, "git", "add", "README.md")
	runTest(t, root, "git", "commit", "-m", "fixture")

	remote := "file://" + root
	inspection, err := InspectRepository(ctx, remote)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SourcePath != remote || inspection.Remotes[0].FetchURL != remote || inspection.Title != "source" || inspection.DefaultBranch != "" {
		t.Fatalf("inspect remote = %#v", inspection)
	}
	mirror := filepath.Join(t.TempDir(), "source.git")
	if err := CreateMirror(ctx, inspection.Remotes, inspection.DefaultRemote, inspection.PushRemote, mirror); err != nil {
		t.Fatal(err)
	}
	branch := DefaultBranch(ctx, mirror, inspection.DefaultRemote)
	if branch != "main" {
		t.Fatalf("default branch = %q", branch)
	}
	worktree := filepath.Join(t.TempDir(), "worktree")
	repo := model.Repository{Title: inspection.Title, MirrorPath: mirror, DefaultRemote: inspection.DefaultRemote, PushRemote: inspection.PushRemote, Remotes: inspection.Remotes, DefaultBranch: branch}
	if err := CreateWorktree(ctx, repo, worktree, "galpon/remote", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "README.md")); err != nil {
		t.Fatal(err)
	}
}

func TestInspectSCPRemoteDoesNotTreatItAsALocalPath(t *testing.T) {
	remote := "git@github.com:dagger/dagger.io"
	inspection, err := InspectRepository(context.Background(), remote)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SourcePath != remote || inspection.Remotes[0].FetchURL != remote || inspection.Title != "dagger.io" || inspection.DefaultBranch != "" {
		t.Fatalf("inspect remote = %#v", inspection)
	}
}

func TestMirrorKeepsMultipleRemotesAndDefaultPushRemote(t *testing.T) {
	ctx := context.Background()
	upstream := createRemoteFixture(t, "upstream")
	fork := createRemoteFixture(t, "fork")
	remotes := []model.RepositoryRemote{
		{Name: "origin", FetchURL: upstream, PushURL: upstream},
		{Name: "matipan", FetchURL: fork, PushURL: fork},
	}
	mirror := filepath.Join(t.TempDir(), "source.git")
	if err := CreateMirror(ctx, remotes, "origin", "matipan", mirror); err != nil {
		t.Fatal(err)
	}
	if got := runTest(t, mirror, "git", "remote", "get-url", "origin"); got != upstream {
		t.Fatalf("origin = %q", got)
	}
	if got := runTest(t, mirror, "git", "remote", "get-url", "matipan"); got != fork {
		t.Fatalf("matipan = %q", got)
	}
	if got := runTest(t, mirror, "git", "config", "--get", "remote.pushDefault"); got != "matipan" {
		t.Fatalf("push default = %q", got)
	}
}

func createRemoteFixture(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	runTest(t, "", "git", "init", "-b", "main", root)
	runTest(t, root, "git", "config", "user.name", "Galpon Test")
	runTest(t, root, "git", "config", "user.email", "galpon@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTest(t, root, "git", "add", "README.md")
	runTest(t, root, "git", "commit", "-m", name)
	return root
}

func runTest(t *testing.T, cwd, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", bin, args, err, out)
	}
	return string(bytesTrim(out))
}
func bytesTrim(value []byte) []byte {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\n' || value[start] == '\r' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\n' || value[end-1] == '\r' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}
