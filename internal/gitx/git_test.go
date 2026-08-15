package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestCheckpointRestoresDirtyWorktreeWithoutChangingBranchHistory(t *testing.T) {
	ctx := context.Background()
	remote := createRemoteFixture(t, "checkpoint-remote")
	if err := os.WriteFile(filepath.Join(remote, "remove.txt"), []byte("remove me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTest(t, remote, "git", "add", "remove.txt")
	runTest(t, remote, "git", "commit", "-m", "add removable file")
	inspection, err := InspectRepository(ctx, remote)
	if err != nil {
		t.Fatal(err)
	}
	firstMirror := filepath.Join(t.TempDir(), "first.git")
	if err := CreateMirror(ctx, inspection.Remotes, inspection.DefaultRemote, inspection.PushRemote, firstMirror); err != nil {
		t.Fatal(err)
	}
	repository := model.Repository{ID: "repo", Title: inspection.Title, MirrorPath: firstMirror, DefaultRemote: inspection.DefaultRemote, PushRemote: inspection.PushRemote, Remotes: inspection.Remotes, DefaultBranch: inspection.DefaultBranch}
	first := model.Worktree{ID: "worktree", RepositoryID: repository.ID, Path: filepath.Join(t.TempDir(), "first"), Branch: "galpon/dirty", BaseRef: "refs/remotes/origin/main"}
	if err := CreateWorktree(ctx, repository, first.Path, first.Branch, first.BaseRef); err != nil {
		t.Fatal(err)
	}
	head := runTest(t, first.Path, "git", "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(first.Path, "README.md"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTest(t, first.Path, "git", "add", "README.md")
	if err := os.WriteFile(filepath.Join(first.Path, "README.md"), []byte("working\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "notes.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, " leading.txt"), []byte("spaced name\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(first.Path, "remove.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, ".gitignore"), []byte("ignored.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "ignored.txt"), []byte("do not upload\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := runTest(t, first.Path, "git", "status", "--porcelain=v1")
	snapshot, err := PushCheckpoint(ctx, repository, first, "checkpoint-id")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Dirty || snapshot.Head != head || snapshot.Commit == head || snapshot.IgnoredFiles != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if got := runTest(t, first.Path, "git", "status", "--porcelain=v1"); got != before {
		t.Fatalf("checkpoint changed source status:\n%s\nwant:\n%s", got, before)
	}
	if got := runTest(t, first.Path, "git", "rev-parse", "HEAD"); got != head {
		t.Fatalf("checkpoint changed source HEAD to %s", got)
	}

	secondMirror := filepath.Join(t.TempDir(), "second.git")
	if err := CreateMirror(ctx, inspection.Remotes, inspection.DefaultRemote, inspection.PushRemote, secondMirror); err != nil {
		t.Fatal(err)
	}
	restoredRepository := repository
	restoredRepository.MirrorPath = secondMirror
	restored := first
	restored.Path = filepath.Join(t.TempDir(), "restored")
	if err := RestoreCheckpoint(ctx, restoredRepository, restored, snapshot); err != nil {
		t.Fatal(err)
	}
	if got := runTest(t, restored.Path, "git", "rev-parse", "HEAD"); got != head {
		t.Fatalf("restored HEAD = %s, want %s", got, head)
	}
	if snapshot.Upstream != "" {
		if got := runTest(t, restored.Path, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); got != snapshot.Upstream {
			t.Fatalf("restored upstream = %s, want %s", got, snapshot.Upstream)
		}
	}
	if got := runTest(t, restored.Path, "git", "status", "--porcelain=v1"); got != before {
		t.Fatalf("restored status:\n%s\nwant:\n%s", got, before)
	}
	if data, err := os.ReadFile(filepath.Join(restored.Path, "README.md")); err != nil || string(data) != "working\n" {
		t.Fatalf("restored README = %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(restored.Path, "notes.txt")); err != nil || string(data) != "untracked\n" {
		t.Fatalf("restored untracked file = %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(restored.Path, " leading.txt")); err != nil || string(data) != "spaced name\n" {
		t.Fatalf("restored spaced file = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(restored.Path, "ignored.txt")); !os.IsNotExist(err) {
		t.Fatalf("ignored file was restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(restored.Path, "remove.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted tracked file was restored: %v", err)
	}
}

func TestCheckpointPushesCleanUnpublishedCommit(t *testing.T) {
	ctx := context.Background()
	remote := createRemoteFixture(t, "clean-checkpoint-remote")
	inspection, err := InspectRepository(ctx, remote)
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(t.TempDir(), "source.git")
	if err := CreateMirror(ctx, inspection.Remotes, inspection.DefaultRemote, inspection.PushRemote, mirror); err != nil {
		t.Fatal(err)
	}
	repository := model.Repository{ID: "repo", Title: inspection.Title, MirrorPath: mirror, DefaultRemote: inspection.DefaultRemote, PushRemote: inspection.PushRemote, Remotes: inspection.Remotes, DefaultBranch: inspection.DefaultBranch}
	worktree := model.Worktree{ID: "worktree", RepositoryID: repository.ID, Path: filepath.Join(t.TempDir(), "worktree"), Branch: "galpon/clean", BaseRef: "refs/remotes/origin/main"}
	if err := CreateWorktree(ctx, repository, worktree.Path, worktree.Branch, worktree.BaseRef); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree.Path, "local.txt"), []byte("unpublished commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTest(t, worktree.Path, "git", "add", "local.txt")
	runTest(t, worktree.Path, "git", "commit", "-m", "local commit")
	head := runTest(t, worktree.Path, "git", "rev-parse", "HEAD")
	snapshot, err := PushCheckpoint(ctx, repository, worktree, "checkpoint-id")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Dirty || snapshot.Commit != head || snapshot.Head != head {
		t.Fatalf("clean snapshot = %#v", snapshot)
	}

	restoredRepository := repository
	restoredRepository.MirrorPath = filepath.Join(t.TempDir(), "restored.git")
	if err := CreateMirror(ctx, inspection.Remotes, inspection.DefaultRemote, inspection.PushRemote, restoredRepository.MirrorPath); err != nil {
		t.Fatal(err)
	}
	restored := worktree
	restored.Path = filepath.Join(t.TempDir(), "restored")
	if err := RestoreCheckpoint(ctx, restoredRepository, restored, snapshot); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(restored.Path, "local.txt")); err != nil || string(data) != "unpublished commit\n" {
		t.Fatalf("restored local commit = %q, %v", data, err)
	}
	if status := runTest(t, restored.Path, "git", "status", "--porcelain=v1"); status != "" {
		t.Fatalf("restored clean status = %q", status)
	}
}

func TestCheckpointRejectsGitLFSWorktree(t *testing.T) {
	ctx := context.Background()
	remote := createRemoteFixture(t, "lfs-remote")
	inspection, err := InspectRepository(ctx, remote)
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(t.TempDir(), "source.git")
	if err := CreateMirror(ctx, inspection.Remotes, inspection.DefaultRemote, inspection.PushRemote, mirror); err != nil {
		t.Fatal(err)
	}
	repository := model.Repository{ID: "repo", Title: inspection.Title, MirrorPath: mirror, DefaultRemote: inspection.DefaultRemote, PushRemote: inspection.PushRemote, Remotes: inspection.Remotes, DefaultBranch: inspection.DefaultBranch}
	worktree := model.Worktree{ID: "worktree", RepositoryID: repository.ID, Path: filepath.Join(t.TempDir(), "worktree"), Branch: "galpon/lfs", BaseRef: "refs/remotes/origin/main"}
	if err := CreateWorktree(ctx, repository, worktree.Path, worktree.Branch, worktree.BaseRef); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree.Path, ".gitattributes"), []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PushCheckpoint(ctx, repository, worktree, "checkpoint-id"); err == nil || !strings.Contains(err.Error(), "uses Git LFS") {
		t.Fatalf("Git LFS checkpoint error = %v", err)
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

func TestCheckpointRemoteLocality(t *testing.T) {
	for _, test := range []struct {
		value string
		local bool
	}{
		{value: "/home/user/repository", local: true},
		{value: "../repository", local: true},
		{value: "file:///mnt/backup/repository.git", local: true},
		{value: "git@github.com:owner/repository.git", local: false},
		{value: "ssh://git@example.com/repository.git", local: false},
		{value: "https://example.com/repository.git", local: false},
	} {
		if got := RemoteIsLocal(test.value); got != test.local {
			t.Errorf("RemoteIsLocal(%q) = %v, want %v", test.value, got, test.local)
		}
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

func TestMirrorSupportsRemoteBranchesThatDifferOnlyByCase(t *testing.T) {
	ctx := context.Background()
	source := createRemoteFixture(t, "case-branches")
	commit := runTest(t, source, "git", "rev-parse", "HEAD")

	// Packing between updates lets this fixture represent both refs even when
	// the test itself is running on a case-insensitive filesystem.
	runTest(t, source, "git", "update-ref", "refs/heads/FEATURE-123", commit)
	runTest(t, source, "git", "pack-refs", "--all")
	runTest(t, source, "git", "update-ref", "refs/heads/feature-123", commit)
	runTest(t, source, "git", "pack-refs", "--all")

	mirror := filepath.Join(t.TempDir(), "source.git")
	remotes := []model.RepositoryRemote{{Name: "origin", FetchURL: source, PushURL: source}}
	if err := CreateMirror(ctx, remotes, "origin", "origin", mirror); err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"FEATURE-123", "feature-123"} {
		if got := runTest(t, mirror, "git", "rev-parse", "--verify", "refs/remotes/origin/"+branch+"^{commit}"); got != commit {
			t.Fatalf("%s = %q, want %q", branch, got, commit)
		}
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
