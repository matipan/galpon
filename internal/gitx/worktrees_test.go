package gitx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func worktreeTestRepository(t *testing.T, remote string) model.Repository {
	t.Helper()
	inspection, err := InspectRepository(context.Background(), remote)
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(t.TempDir(), "mirror.git")
	if err := CreateMirror(context.Background(), inspection.Remotes, inspection.DefaultRemote, inspection.PushRemote, mirror); err != nil {
		t.Fatal(err)
	}
	return model.Repository{ID: "repo", Title: inspection.Title, MirrorPath: mirror, DefaultRemote: inspection.DefaultRemote, PushRemote: inspection.PushRemote, Remotes: inspection.Remotes, DefaultBranch: inspection.DefaultBranch}
}

func commitFile(t *testing.T, cwd, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cwd, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runTest(t, cwd, "git", "add", name)
	runTest(t, cwd, "git", "-c", "user.name=Galpon Test", "-c", "user.email=galpon@example.invalid", "commit", "-m", name)
	return runTest(t, cwd, "git", "rev-parse", "HEAD")
}

func TestAddWorktreeFetchesAReferenceThatOnlyTheRemoteHas(t *testing.T) {
	remote := createRemoteFixture(t, "pull-remote")
	runTest(t, remote, "git", "switch", "-c", "contributor")
	pullHead := commitFile(t, remote, "change.txt", "pull request\n")
	runTest(t, remote, "git", "update-ref", "refs/pull/7/head", pullHead)
	runTest(t, remote, "git", "switch", "main")
	runTest(t, remote, "git", "branch", "-D", "contributor")
	repository := worktreeTestRepository(t, remote)

	path := filepath.Join(t.TempDir(), "review")
	added, err := AddWorktree(context.Background(), repository, WorktreeRequest{Path: path, Base: "pull/7/head", Detach: true, Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if added.Head != pullHead || added.Base != "pull/7/head" || added.CreatedBranch {
		t.Fatalf("added = %#v, want head %s", added, pullHead)
	}
	checkout, err := WorktreeCheckout(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !checkout.Detached || checkout.Head != pullHead {
		t.Fatalf("checkout = %#v", checkout)
	}
	if refs := runTest(t, "", "git", "--git-dir", repository.MirrorPath, "for-each-ref", "refs/galpon/fetch"); refs != "" {
		t.Fatalf("temporary fetch references remain: %s", refs)
	}
}

func TestAddWorktreeContinuesARemoteBranchWithTheSameName(t *testing.T) {
	remote := createRemoteFixture(t, "branch-remote")
	runTest(t, remote, "git", "switch", "-c", "feature")
	featureHead := commitFile(t, remote, "feature.txt", "feature\n")
	runTest(t, remote, "git", "switch", "main")
	repository := worktreeTestRepository(t, remote)

	path := filepath.Join(t.TempDir(), "feature")
	added, err := AddWorktree(context.Background(), repository, WorktreeRequest{Path: path, Branch: "feature", Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if added.Head != featureHead || !added.CreatedBranch {
		t.Fatalf("added = %#v, want head %s", added, featureHead)
	}
	if upstream := runTest(t, path, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); upstream != "origin/feature" {
		t.Fatalf("upstream = %q", upstream)
	}
	if _, err := AddWorktree(context.Background(), repository, WorktreeRequest{Path: filepath.Join(t.TempDir(), "again"), Branch: "feature"}); err == nil {
		t.Fatal("a branch that another worktree has checked out was checked out again")
	}
}

func TestRemoveAgentWorktreeDeletesOnlyBranchesThatARemoteHas(t *testing.T) {
	remote := createRemoteFixture(t, "remove-remote")
	repository := worktreeTestRepository(t, remote)
	ctx := context.Background()

	published := filepath.Join(t.TempDir(), "published")
	if _, err := AddWorktree(ctx, repository, WorktreeRequest{Path: published, Branch: "published"}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "local")
	if _, err := AddWorktree(ctx, repository, WorktreeRequest{Path: local, Branch: "local-work"}); err != nil {
		t.Fatal(err)
	}
	localHead := commitFile(t, local, "local.txt", "not pushed\n")
	if err := os.WriteFile(filepath.Join(local, "draft.txt"), []byte("draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RemoveAgentWorktree(ctx, repository, local, []string{"local-work"}, false); err != ErrWorktreeChanges {
		t.Fatalf("removal with uncommitted changes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(local, "draft.txt")); err != nil {
		t.Fatalf("refused removal changed the worktree: %v", err)
	}
	removed, err := RemoveAgentWorktree(ctx, repository, local, []string{"local-work"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.KeptBranches) != 1 || removed.KeptBranches[0] != "local-work" || len(removed.DeletedBranches) != 0 {
		t.Fatalf("local removal = %#v", removed)
	}
	if head := runTest(t, "", "git", "--git-dir", repository.MirrorPath, "rev-parse", "refs/heads/local-work"); head != localHead {
		t.Fatalf("kept branch points to %s, want %s", head, localHead)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("removed worktree still exists: %v", err)
	}

	removed, err = RemoveAgentWorktree(ctx, repository, published, []string{"published"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.DeletedBranches) != 1 || removed.DeletedBranches[0] != "published" || len(removed.KeptBranches) != 0 {
		t.Fatalf("published removal = %#v", removed)
	}
}

func TestCheckpointRestoresTheCheckedOutBranchAndADetachedHead(t *testing.T) {
	remote := createRemoteFixture(t, "checkout-remote")
	repository := worktreeTestRepository(t, remote)
	ctx := context.Background()

	switched := model.Worktree{ID: "switched", RepositoryID: repository.ID, Path: filepath.Join(t.TempDir(), "switched"), Branch: "galpon/stored"}
	if _, err := AddWorktree(ctx, repository, WorktreeRequest{Path: switched.Path, Branch: switched.Branch}); err != nil {
		t.Fatal(err)
	}
	runTest(t, switched.Path, "git", "switch", "-c", "feature")
	switchedHead := commitFile(t, switched.Path, "feature.txt", "feature\n")
	detached := model.Worktree{ID: "detached", RepositoryID: repository.ID, Path: filepath.Join(t.TempDir(), "detached")}
	added, err := AddWorktree(ctx, repository, WorktreeRequest{Path: detached.Path, Detach: true})
	if err != nil {
		t.Fatal(err)
	}

	restoredRepository := worktreeTestRepository(t, remote)
	for _, worktree := range []model.Worktree{switched, detached} {
		snapshot, err := PushCheckpoint(ctx, repository, worktree, "checkpoint")
		if err != nil {
			t.Fatal(err)
		}
		restored := worktree
		restored.Path = filepath.Join(t.TempDir(), "restored-"+worktree.ID)
		if err := RestoreCheckpoint(ctx, restoredRepository, restored, snapshot); err != nil {
			t.Fatal(err)
		}
		checkout, err := WorktreeCheckout(ctx, restored.Path)
		if err != nil {
			t.Fatal(err)
		}
		switch worktree.ID {
		case "switched":
			if checkout.Branch != "feature" || checkout.Head != switchedHead {
				t.Fatalf("restored switched worktree = %#v, want feature at %s", checkout, switchedHead)
			}
		case "detached":
			if !checkout.Detached || checkout.Head != added.Head {
				t.Fatalf("restored detached worktree = %#v, want %s", checkout, added.Head)
			}
		}
	}
}
