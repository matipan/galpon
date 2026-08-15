package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

var (
	unsafeSlug        = regexp.MustCompile(`[^a-z0-9]+`)
	scpRemotePattern  = regexp.MustCompile(`^([^@]+@)?([^:]+):(.+)$`)
	remoteNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

type RepositoryInspection struct {
	SourcePath    string
	Title         string
	DefaultRemote string
	PushRemote    string
	Remotes       []model.RepositoryRemote
	DefaultBranch string
}

// CheckpointSnapshot identifies the remote Git objects that can recreate one
// managed worktree. A dirty snapshot uses internal commits, but Head remains
// the commit of the user's branch.
type CheckpointSnapshot struct {
	WorktreeID   string `json:"worktreeId"`
	Remote       string `json:"remote"`
	Ref          string `json:"ref"`
	Commit       string `json:"commit"`
	Head         string `json:"head"`
	Upstream     string `json:"upstream,omitempty"`
	IndexTree    string `json:"indexTree"`
	WorktreeTree string `json:"worktreeTree"`
	Dirty        bool   `json:"dirty"`
	IgnoredFiles int    `json:"ignoredFiles"`
}

func Slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(unsafeSlug.ReplaceAllString(value, "-"), "-")
	if value == "" {
		return "work"
	}
	if len(value) > 48 {
		value = strings.Trim(value[:48], "-")
	}
	return value
}

func InspectRepository(ctx context.Context, input string) (RepositoryInspection, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return RepositoryInspection{}, fmt.Errorf("repository path or Git URL is required")
	}
	if _, statErr := os.Stat(input); statErr != nil && isRemote(input) {
		return RepositoryInspection{
			SourcePath:    input,
			Title:         remoteTitle(input),
			DefaultRemote: "origin",
			PushRemote:    "origin",
			Remotes:       []model.RepositoryRemote{{Name: "origin", FetchURL: input, PushURL: input}},
		}, nil
	}
	root, err := run(ctx, "", "git", "-C", input, "rev-parse", "--show-toplevel")
	if err != nil {
		return RepositoryInspection{}, fmt.Errorf("inspect repository: %w", err)
	}
	root, err = filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return RepositoryInspection{}, err
	}
	inspection := RepositoryInspection{SourcePath: root, Title: filepath.Base(root)}
	remoteOutput, _ := run(ctx, "", "git", "-C", root, "remote")
	remoteNames := strings.Fields(remoteOutput)
	if len(remoteNames) == 0 {
		inspection.DefaultRemote = "origin"
		inspection.PushRemote = "origin"
		inspection.Remotes = []model.RepositoryRemote{{Name: "origin", FetchURL: root, PushURL: root}}
	} else {
		inspection.DefaultRemote = remoteNames[0]
		for _, name := range remoteNames {
			if name == "origin" {
				inspection.DefaultRemote = name
				break
			}
		}
		for _, name := range remoteNames {
			fetchURL, fetchErr := run(ctx, "", "git", "-C", root, "remote", "get-url", name)
			if fetchErr != nil || strings.TrimSpace(fetchURL) == "" {
				continue
			}
			pushURL, pushErr := run(ctx, "", "git", "-C", root, "remote", "get-url", "--push", name)
			if pushErr != nil || strings.TrimSpace(pushURL) == "" {
				pushURL = fetchURL
			}
			inspection.Remotes = append(inspection.Remotes, model.RepositoryRemote{Name: name, FetchURL: strings.TrimSpace(fetchURL), PushURL: strings.TrimSpace(pushURL)})
		}
		inspection.PushRemote, _ = run(ctx, "", "git", "-C", root, "config", "--get", "remote.pushDefault")
		inspection.PushRemote = strings.TrimSpace(inspection.PushRemote)
		if inspection.PushRemote == "" {
			inspection.PushRemote = inspection.DefaultRemote
		}
	}
	if len(inspection.Remotes) == 0 {
		return RepositoryInspection{}, fmt.Errorf("repository has no usable Git remotes")
	}
	if !hasRemote(inspection.Remotes, inspection.DefaultRemote) {
		inspection.DefaultRemote = inspection.Remotes[0].Name
	}
	if !hasRemote(inspection.Remotes, inspection.PushRemote) {
		inspection.PushRemote = inspection.DefaultRemote
	}
	inspection.DefaultBranch, _ = run(ctx, "", "git", "-C", root, "symbolic-ref", "--short", "refs/remotes/"+inspection.DefaultRemote+"/HEAD")
	inspection.DefaultBranch = strings.TrimPrefix(strings.TrimSpace(inspection.DefaultBranch), inspection.DefaultRemote+"/")
	if inspection.DefaultBranch == "" {
		inspection.DefaultBranch, _ = run(ctx, "", "git", "-C", root, "branch", "--show-current")
		inspection.DefaultBranch = strings.TrimSpace(inspection.DefaultBranch)
	}
	if inspection.DefaultBranch == "" {
		inspection.DefaultBranch = "main"
	}
	return inspection, nil
}

func CreateMirror(ctx context.Context, remotes []model.RepositoryRemote, defaultRemote, pushRemote, mirrorPath string) error {
	if !hasRemote(remotes, defaultRemote) {
		return fmt.Errorf("default remote %s is not configured", defaultRemote)
	}
	if pushRemote != "" && !hasRemote(remotes, pushRemote) {
		return fmt.Errorf("default push remote %s is not configured", pushRemote)
	}
	if err := os.MkdirAll(filepath.Dir(mirrorPath), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(mirrorPath); err == nil {
		return fmt.Errorf("managed repository already exists: %s", mirrorPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(mirrorPath)
		}
	}()
	// Reftable keeps refs out of filesystem paths. That matters on case-insensitive
	// filesystems when a remote contains branches that differ only by case.
	if _, err := run(ctx, "", "git", "init", "--bare", "--ref-format=reftable", mirrorPath); err != nil {
		return fmt.Errorf("create bare repository (Git 2.45 or newer is required): %w", err)
	}
	for _, remote := range remotes {
		if err := configureRemote(ctx, mirrorPath, remote); err != nil {
			return err
		}
	}
	if pushRemote != "" {
		if _, err := run(ctx, mirrorPath, "git", "config", "remote.pushDefault", pushRemote); err != nil {
			return fmt.Errorf("configure default push remote: %w", err)
		}
	}
	for _, remote := range orderedRemotes(remotes, defaultRemote) {
		if err := fetchRemote(ctx, mirrorPath, remote.Name); err != nil {
			return err
		}
	}
	cleanup = false
	return nil
}

func AddRemote(ctx context.Context, mirrorPath string, remote model.RepositoryRemote, pushDefault bool) error {
	if err := configureRemote(ctx, mirrorPath, remote); err != nil {
		return err
	}
	if err := fetchRemote(ctx, mirrorPath, remote.Name); err != nil {
		_, _ = run(context.Background(), mirrorPath, "git", "remote", "remove", remote.Name)
		return err
	}
	if pushDefault {
		if _, err := run(ctx, mirrorPath, "git", "config", "remote.pushDefault", remote.Name); err != nil {
			return fmt.Errorf("configure default push remote: %w", err)
		}
	}
	return nil
}

func RemoveRemote(ctx context.Context, mirrorPath, name string) error {
	if _, err := run(ctx, mirrorPath, "git", "remote", "remove", name); err != nil {
		return fmt.Errorf("remove repository remote %s: %w", name, err)
	}
	return nil
}

func SetPushRemote(ctx context.Context, mirrorPath, name string) error {
	if _, err := run(ctx, mirrorPath, "git", "config", "remote.pushDefault", name); err != nil {
		return fmt.Errorf("configure default push remote: %w", err)
	}
	return nil
}

func DefaultBranch(ctx context.Context, mirrorPath, remote string) string {
	value, err := run(ctx, mirrorPath, "git", "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD")
	if err == nil {
		if branch := strings.TrimPrefix(strings.TrimSpace(value), remote+"/"); branch != "" {
			return branch
		}
	}
	for _, branch := range []string{"main", "master"} {
		if _, err := run(ctx, mirrorPath, "git", "rev-parse", "--verify", "refs/remotes/"+remote+"/"+branch+"^{commit}"); err == nil {
			return branch
		}
	}
	return "main"
}

func CreateWorktree(ctx context.Context, repo model.Repository, path, branch, baseRef string) error {
	return CreateWorktreeFrom(ctx, repo, path, branch, baseRef, repo.DefaultRemote, true)
}

func CreateWorktreeFrom(ctx context.Context, repo model.Repository, path, branch, baseRef, remote string, fetchFirst bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	remote = strings.TrimSpace(remote)
	if remote == "" {
		remote = repo.DefaultRemote
	}
	if !hasRemote(repo.Remotes, remote) {
		return fmt.Errorf("repository %s has no remote %s", repo.Title, remote)
	}
	if fetchFirst {
		if err := fetchRemote(ctx, repo.MirrorPath, remote); err != nil {
			return err
		}
	}
	candidates := baseCandidates(strings.TrimSpace(baseRef), remote, repo.DefaultBranch)
	var resolved string
	for _, candidate := range candidates {
		if _, err := run(ctx, "", "git", "--git-dir", repo.MirrorPath, "rev-parse", "--verify", candidate+"^{commit}"); err == nil {
			resolved = candidate
			break
		}
	}
	if resolved == "" {
		return fmt.Errorf("cannot resolve base revision for %s", repo.Title)
	}
	_, _ = run(ctx, "", "git", "--git-dir", repo.MirrorPath, "worktree", "prune", "--expire", "now")
	if _, err := run(ctx, "", "git", "--git-dir", repo.MirrorPath, "worktree", "add", "-b", branch, path, resolved); err != nil {
		return fmt.Errorf("create worktree: %w", err)
	}
	return nil
}

func RemoveWorktree(ctx context.Context, repo model.Repository, path, branch string) error {
	if _, err := run(ctx, "", "git", "--git-dir", repo.MirrorPath, "worktree", "remove", "--force", path); err != nil {
		return fmt.Errorf("remove worktree: %w", err)
	}
	if strings.TrimSpace(branch) != "" {
		if _, err := run(ctx, "", "git", "--git-dir", repo.MirrorPath, "branch", "-D", branch); err != nil {
			return fmt.Errorf("remove worktree branch: %w", err)
		}
	}
	return nil
}

// CleanupWorktree is retry-safe for the explicit permanent-cleanup command.
// The managed path or branch can already be absent after an interrupted run.
func CleanupWorktree(ctx context.Context, repo model.Repository, path, branch string) error {
	if _, err := os.Stat(repo.MirrorPath); errors.Is(err, os.ErrNotExist) {
		return os.RemoveAll(path)
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		if _, err := run(ctx, "", "git", "--git-dir", repo.MirrorPath, "worktree", "remove", "--force", path); err != nil {
			if _, statErr := os.Stat(path); statErr == nil {
				return fmt.Errorf("remove worktree: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, _ = run(ctx, "", "git", "--git-dir", repo.MirrorPath, "worktree", "prune", "--expire", "now")
	if strings.TrimSpace(branch) == "" {
		return nil
	}
	if _, err := run(ctx, "", "git", "--git-dir", repo.MirrorPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return nil
	}
	if _, err := run(ctx, "", "git", "--git-dir", repo.MirrorPath, "branch", "-D", branch); err != nil {
		return fmt.Errorf("remove worktree branch: %w", err)
	}
	return nil
}

// PushCheckpoint creates an immutable remote reference for a worktree without
// changing its branch or index. Non-ignored untracked files are part of a dirty
// snapshot. Ignored files are counted but are never uploaded.
func PushCheckpoint(ctx context.Context, repo model.Repository, worktree model.Worktree, checkpointID string) (CheckpointSnapshot, error) {
	if strings.TrimSpace(checkpointID) == "" || strings.TrimSpace(worktree.ID) == "" {
		return CheckpointSnapshot{}, fmt.Errorf("checkpoint and worktree IDs are required")
	}
	if _, err := os.Stat(worktree.Path); err != nil {
		return CheckpointSnapshot{}, fmt.Errorf("open worktree %s: %w", worktree.ID, err)
	}
	if usesLFS, err := worktreeUsesLFS(ctx, worktree.Path); err != nil {
		return CheckpointSnapshot{}, fmt.Errorf("inspect Git LFS attributes in worktree %s: %w", worktree.ID, err)
	} else if usesLFS {
		return CheckpointSnapshot{}, fmt.Errorf("worktree %s uses Git LFS, which checkpoint does not support yet", worktree.ID)
	}
	remote, ok := repositoryRemote(repo, repo.PushRemote)
	if !ok {
		return CheckpointSnapshot{}, fmt.Errorf("repository %s has no push remote %s", repo.Title, repo.PushRemote)
	}
	if unresolved, err := run(ctx, worktree.Path, "git", "ls-files", "--unmerged"); err != nil {
		return CheckpointSnapshot{}, err
	} else if unresolved != "" {
		return CheckpointSnapshot{}, fmt.Errorf("worktree %s has unresolved merge entries", worktree.ID)
	}
	visibleIndex, err := run(ctx, worktree.Path, "git", "diff", "--cached", "--name-only", "--ita-visible-in-index")
	if err != nil {
		return CheckpointSnapshot{}, err
	}
	normalIndex, err := run(ctx, worktree.Path, "git", "diff", "--cached", "--name-only")
	if err != nil {
		return CheckpointSnapshot{}, err
	}
	if visibleIndex != normalIndex {
		return CheckpointSnapshot{}, fmt.Errorf("worktree %s has intent-to-add index entries, which checkpoint does not support", worktree.ID)
	}
	if changed, err := run(ctx, worktree.Path, "git", "diff", "--raw", "HEAD", "--"); err != nil {
		return CheckpointSnapshot{}, err
	} else if diffChangesSubmodule(changed) {
		return CheckpointSnapshot{}, fmt.Errorf("worktree %s has a changed submodule; checkpoint changed submodules separately", worktree.ID)
	}
	if _, err := run(ctx, worktree.Path, "git", "submodule", "foreach", "--quiet", "--recursive", `test -z "$(git status --porcelain=v1 --untracked-files=all)"`); err != nil {
		return CheckpointSnapshot{}, fmt.Errorf("worktree %s has dirty submodule files", worktree.ID)
	}
	head, err := run(ctx, worktree.Path, "git", "rev-parse", "HEAD^{commit}")
	if err != nil {
		return CheckpointSnapshot{}, fmt.Errorf("read worktree %s HEAD: %w", worktree.ID, err)
	}
	upstream, _ := run(ctx, worktree.Path, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	indexTree, err := run(ctx, worktree.Path, "git", "write-tree")
	if err != nil {
		return CheckpointSnapshot{}, fmt.Errorf("snapshot worktree %s index: %w", worktree.ID, err)
	}
	status, err := runUntrimmed(ctx, worktree.Path, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return CheckpointSnapshot{}, fmt.Errorf("inspect worktree %s: %w", worktree.ID, err)
	}
	ignored, err := runUntrimmed(ctx, worktree.Path, "git", "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "--no-empty-directory", "-z")
	if err != nil {
		return CheckpointSnapshot{}, fmt.Errorf("inspect ignored files in worktree %s: %w", worktree.ID, err)
	}

	worktreeTree := indexTree
	commit := head
	dirty := status != ""
	if dirty {
		worktreeTree, err = snapshotWorktreeTree(ctx, worktree.Path, head)
		if err != nil {
			return CheckpointSnapshot{}, fmt.Errorf("snapshot worktree %s files: %w", worktree.ID, err)
		}
		indexCommit, commitErr := checkpointCommit(ctx, repo.MirrorPath, indexTree, head, "Galpon checkpoint index "+checkpointID+" "+worktree.ID)
		if commitErr != nil {
			return CheckpointSnapshot{}, commitErr
		}
		commit, err = checkpointCommit(ctx, repo.MirrorPath, worktreeTree, indexCommit, "Galpon checkpoint worktree "+checkpointID+" "+worktree.ID)
		if err != nil {
			return CheckpointSnapshot{}, err
		}
	}
	checkpointRef := "refs/heads/galpon-checkpoints/" + checkpointID + "/" + worktree.ID
	if _, err := runEnv(ctx, repo.MirrorPath, []string{"GIT_TERMINAL_PROMPT=0"}, "git", "push", "--quiet", repo.PushRemote, commit+":"+checkpointRef); err != nil {
		return CheckpointSnapshot{}, fmt.Errorf("push checkpoint for worktree %s: %w", worktree.ID, err)
	}
	remoteCommit, err := lsRemoteRef(ctx, remote.PushURL, checkpointRef)
	if err != nil {
		deleteCheckpointRefBestEffort(repo, repo.PushRemote, checkpointRef)
		return CheckpointSnapshot{}, fmt.Errorf("verify checkpoint for worktree %s: %w", worktree.ID, err)
	}
	if remoteCommit != commit {
		deleteCheckpointRefBestEffort(repo, repo.PushRemote, checkpointRef)
		return CheckpointSnapshot{}, fmt.Errorf("verify checkpoint for worktree %s: remote has %s, want %s", worktree.ID, remoteCommit, commit)
	}
	if err := verifyCheckpointSource(ctx, worktree.Path, head, indexTree, worktreeTree, dirty); err != nil {
		deleteCheckpointRefBestEffort(repo, repo.PushRemote, checkpointRef)
		return CheckpointSnapshot{}, fmt.Errorf("verify source worktree %s: %w", worktree.ID, err)
	}
	return CheckpointSnapshot{
		WorktreeID: worktree.ID, Remote: repo.PushRemote, Ref: checkpointRef,
		Commit: commit, Head: head, Upstream: upstream, IndexTree: indexTree, WorktreeTree: worktreeTree,
		Dirty: dirty, IgnoredFiles: nulItemCount(ignored),
	}, nil
}

// DeleteCheckpointRef removes a reference that belongs to an incomplete local
// checkpoint operation.
func DeleteCheckpointRef(ctx context.Context, repo model.Repository, snapshot CheckpointSnapshot) error {
	if snapshot.Ref == "" || snapshot.Remote == "" {
		return nil
	}
	_, err := runEnv(ctx, repo.MirrorPath, []string{"GIT_TERMINAL_PROMPT=0"}, "git", "push", "--quiet", snapshot.Remote, ":"+snapshot.Ref)
	return err
}

func deleteCheckpointRefBestEffort(repo model.Repository, remote, ref string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = runEnv(ctx, repo.MirrorPath, []string{"GIT_TERMINAL_PROMPT=0"}, "git", "push", "--quiet", remote, ":"+ref)
}

// RestoreCheckpoint recreates a worktree and its exact staged, unstaged, and
// non-ignored untracked state from one checkpoint reference.
func RestoreCheckpoint(ctx context.Context, repo model.Repository, worktree model.Worktree, snapshot CheckpointSnapshot) error {
	remote, ok := repositoryRemote(repo, snapshot.Remote)
	if !ok {
		return fmt.Errorf("repository %s has no checkpoint remote %s", repo.Title, snapshot.Remote)
	}
	remoteCommit, err := lsRemoteRef(ctx, remote.PushURL, snapshot.Ref)
	if err != nil {
		return fmt.Errorf("find checkpoint for worktree %s: %w", worktree.ID, err)
	}
	if remoteCommit != snapshot.Commit {
		return fmt.Errorf("checkpoint for worktree %s has %s, want %s", worktree.ID, remoteCommit, snapshot.Commit)
	}
	localRef := "refs/galpon/checkpoints/" + worktree.ID
	if _, err := runEnv(ctx, repo.MirrorPath, []string{"GIT_TERMINAL_PROMPT=0"}, "git", "fetch", "--quiet", "--", remote.PushURL, "+"+snapshot.Ref+":"+localRef); err != nil {
		return fmt.Errorf("fetch checkpoint for worktree %s: %w", worktree.ID, err)
	}
	if commit, err := run(ctx, repo.MirrorPath, "git", "rev-parse", localRef+"^{commit}"); err != nil || commit != snapshot.Commit {
		if err != nil {
			return fmt.Errorf("read checkpoint for worktree %s: %w", worktree.ID, err)
		}
		return fmt.Errorf("local checkpoint for worktree %s has %s, want %s", worktree.ID, commit, snapshot.Commit)
	}
	if err := os.MkdirAll(filepath.Dir(worktree.Path), 0o700); err != nil {
		return err
	}
	_, _ = run(ctx, "", "git", "--git-dir", repo.MirrorPath, "worktree", "prune", "--expire", "now")
	if _, err := run(ctx, "", "git", "--git-dir", repo.MirrorPath, "worktree", "add", "-b", worktree.Branch, worktree.Path, snapshot.Head); err != nil {
		return fmt.Errorf("restore worktree %s: %w", worktree.ID, err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = CleanupWorktree(context.Background(), repo, worktree.Path, worktree.Branch)
		}
	}()
	if snapshot.Upstream != "" {
		exists, err := gitRevisionExists(ctx, worktree.Path, snapshot.Upstream)
		if err != nil {
			return fmt.Errorf("inspect upstream for worktree %s: %w", worktree.ID, err)
		}
		if exists {
			if _, err := run(ctx, worktree.Path, "git", "branch", "--set-upstream-to="+snapshot.Upstream, worktree.Branch); err != nil {
				return fmt.Errorf("restore upstream for worktree %s: %w", worktree.ID, err)
			}
		}
	}
	if snapshot.Dirty {
		if err := restoreDirtyState(ctx, worktree.Path, snapshot.IndexTree, snapshot.WorktreeTree); err != nil {
			return fmt.Errorf("restore dirty state for worktree %s: %w", worktree.ID, err)
		}
	}
	complete = true
	return nil
}

func verifyCheckpointSource(ctx context.Context, worktreePath, head, indexTree, worktreeTree string, dirty bool) error {
	currentHead, err := run(ctx, worktreePath, "git", "rev-parse", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if currentHead != head {
		return fmt.Errorf("HEAD changed during checkpoint creation")
	}
	currentIndex, err := run(ctx, worktreePath, "git", "write-tree")
	if err != nil {
		return err
	}
	if currentIndex != indexTree {
		return fmt.Errorf("index changed during checkpoint creation")
	}
	status, err := runUntrimmed(ctx, worktreePath, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	if !dirty {
		if status != "" {
			return fmt.Errorf("files changed during checkpoint creation")
		}
		return nil
	}
	currentTree, err := snapshotWorktreeTree(ctx, worktreePath, head)
	if err != nil {
		return err
	}
	if currentTree != worktreeTree {
		return fmt.Errorf("files changed during checkpoint creation")
	}
	return nil
}

func snapshotWorktreeTree(ctx context.Context, worktreePath, head string) (string, error) {
	indexPath, err := temporaryIndexPath()
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(indexPath) }()
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := runEnv(ctx, worktreePath, env, "git", "read-tree", head); err != nil {
		return "", err
	}
	if _, err := runEnv(ctx, worktreePath, env, "git", "add", "-A", "--"); err != nil {
		return "", err
	}
	return runEnv(ctx, worktreePath, env, "git", "write-tree")
}

func checkpointCommit(ctx context.Context, mirrorPath, tree, parent, message string) (string, error) {
	env := []string{
		"GIT_AUTHOR_NAME=Galpon Checkpoint", "GIT_AUTHOR_EMAIL=checkpoint@galpon.invalid",
		"GIT_COMMITTER_NAME=Galpon Checkpoint", "GIT_COMMITTER_EMAIL=checkpoint@galpon.invalid",
	}
	commit, err := runEnv(ctx, mirrorPath, env, "git", "commit-tree", tree, "-p", parent, "-m", message)
	if err != nil {
		return "", fmt.Errorf("create checkpoint commit: %w", err)
	}
	return commit, nil
}

func restoreDirtyState(ctx context.Context, worktreePath, indexTree, worktreeTree string) error {
	tracked, err := runUntrimmed(ctx, worktreePath, "git", "ls-files", "-z")
	if err != nil {
		return err
	}
	for _, name := range nulItems(tracked) {
		path, err := safeWorktreePath(worktreePath, name)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	indexPath, err := temporaryIndexPath()
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(indexPath) }()
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := runEnv(ctx, worktreePath, env, "git", "read-tree", worktreeTree); err != nil {
		return err
	}
	prefix := filepath.Clean(worktreePath) + string(os.PathSeparator)
	if _, err := runEnv(ctx, worktreePath, env, "git", "checkout-index", "--all", "--force", "--prefix="+prefix); err != nil {
		return err
	}
	if _, err := run(ctx, worktreePath, "git", "read-tree", indexTree); err != nil {
		return err
	}
	return nil
}

func temporaryIndexPath() (string, error) {
	file, err := os.CreateTemp("", "galpon-checkpoint-index-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func safeWorktreePath(root, name string) (string, error) {
	name = filepath.FromSlash(name)
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("invalid git path %q", name)
	}
	path := filepath.Join(root, name)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("git path leaves worktree: %q", name)
	}
	return path, nil
}

func repositoryRemote(repo model.Repository, name string) (model.RepositoryRemote, bool) {
	for _, remote := range repo.Remotes {
		if remote.Name == name {
			if remote.PushURL == "" {
				remote.PushURL = remote.FetchURL
			}
			return remote, true
		}
	}
	return model.RepositoryRemote{}, false
}

func lsRemoteRef(ctx context.Context, remoteURL, ref string) (string, error) {
	output, err := runEnv(ctx, os.TempDir(), []string{"GIT_TERMINAL_PROMPT=0"}, "git", "ls-remote", "--exit-code", "--", remoteURL, ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) < 2 || fields[1] != ref {
		return "", fmt.Errorf("remote did not return %s", ref)
	}
	return fields[0], nil
}

func nulItems(value string) []string {
	parts := strings.Split(value, "\x00")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func nulItemCount(value string) int { return len(nulItems(value)) }

func worktreeUsesLFS(ctx context.Context, worktreePath string) (bool, error) {
	output, err := runUntrimmed(ctx, worktreePath, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".gitattributes", ":(glob)**/.gitattributes")
	if err != nil {
		return false, err
	}
	for _, name := range nulItems(output) {
		path, err := safeWorktreePath(worktreePath, name)
		if err != nil {
			return false, err
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if strings.Contains(string(data), "filter=lfs") {
			return true, nil
		}
	}
	return false, nil
}

func diffChangesSubmodule(value string) bool {
	for _, line := range strings.Split(value, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[0] == ":160000" || fields[1] == "160000") {
			return true
		}
		if strings.HasPrefix(line, ":160000 ") || strings.Contains(line, " 160000 ") {
			return true
		}
	}
	return false
}

func baseCandidates(baseRef, defaultRemote, defaultBranch string) []string {
	if baseRef == "" {
		return []string{"refs/remotes/" + defaultRemote + "/" + defaultBranch, "refs/heads/" + defaultBranch, defaultBranch}
	}
	if branch := strings.TrimPrefix(baseRef, "refs/heads/"); branch != baseRef {
		return []string{baseRef, "refs/remotes/" + defaultRemote + "/" + branch, branch}
	}
	if branch := strings.TrimPrefix(baseRef, "refs/remotes/"+defaultRemote+"/"); branch != baseRef {
		return []string{baseRef, "refs/heads/" + branch, branch}
	}
	if !strings.HasPrefix(baseRef, "refs/") {
		return []string{baseRef, "refs/remotes/" + defaultRemote + "/" + baseRef, "refs/heads/" + baseRef}
	}
	return []string{baseRef}
}

func ValidateRemote(remote model.RepositoryRemote) (model.RepositoryRemote, error) {
	remote.Name = strings.TrimSpace(remote.Name)
	remote.FetchURL = strings.TrimSpace(remote.FetchURL)
	remote.PushURL = strings.TrimSpace(remote.PushURL)
	if !remoteNamePattern.MatchString(remote.Name) {
		return model.RepositoryRemote{}, fmt.Errorf("invalid remote name %q", remote.Name)
	}
	if remote.FetchURL == "" {
		return model.RepositoryRemote{}, fmt.Errorf("remote %s needs a fetch URL", remote.Name)
	}
	if remote.PushURL == "" {
		remote.PushURL = remote.FetchURL
	}
	return remote, nil
}

func configureRemote(ctx context.Context, mirrorPath string, remote model.RepositoryRemote) error {
	remote, err := ValidateRemote(remote)
	if err != nil {
		return err
	}
	if _, err := run(ctx, mirrorPath, "git", "remote", "add", remote.Name, remote.FetchURL); err != nil {
		return fmt.Errorf("configure repository remote %s: %w", remote.Name, err)
	}
	if _, err := run(ctx, mirrorPath, "git", "config", "remote."+remote.Name+".fetch", "+refs/heads/*:refs/remotes/"+remote.Name+"/*"); err != nil {
		return fmt.Errorf("configure repository fetch %s: %w", remote.Name, err)
	}
	if remote.PushURL != remote.FetchURL {
		if _, err := run(ctx, mirrorPath, "git", "remote", "set-url", "--push", remote.Name, remote.PushURL); err != nil {
			return fmt.Errorf("configure repository push %s: %w", remote.Name, err)
		}
	}
	return nil
}

func fetchRemote(ctx context.Context, mirrorPath, name string) error {
	if _, err := runEnv(ctx, mirrorPath, []string{"GIT_TERMINAL_PROMPT=0"}, "git", "fetch", "--quiet", "--prune", name); err != nil {
		return fmt.Errorf("fetch repository remote %s: %w", name, err)
	}
	_, _ = runEnv(ctx, mirrorPath, []string{"GIT_TERMINAL_PROMPT=0"}, "git", "remote", "set-head", name, "-a")
	return nil
}

func orderedRemotes(remotes []model.RepositoryRemote, first string) []model.RepositoryRemote {
	out := make([]model.RepositoryRemote, 0, len(remotes))
	for _, remote := range remotes {
		if remote.Name == first {
			out = append(out, remote)
		}
	}
	for _, remote := range remotes {
		if remote.Name != first {
			out = append(out, remote)
		}
	}
	return out
}

func hasRemote(remotes []model.RepositoryRemote, name string) bool {
	for _, remote := range remotes {
		if remote.Name == name {
			return true
		}
	}
	return false
}

func isRemote(value string) bool {
	if scpRemotePattern.MatchString(value) && !strings.Contains(value, "://") {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && (parsed.Host != "" || parsed.Scheme == "file")
}

// RemoteIsLocal reports whether a remote points to this machine's filesystem.
// Such a remote does not survive an operating system replacement by itself.
func RemoteIsLocal(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	if scpRemotePattern.MatchString(value) && !strings.Contains(value, "://") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return true
	}
	return parsed.Scheme == "file"
}

func remoteTitle(value string) string {
	repositoryPath := ""
	if match := scpRemotePattern.FindStringSubmatch(value); match != nil && !strings.Contains(value, "://") {
		repositoryPath = match[3]
	} else if parsed, err := url.Parse(value); err == nil {
		repositoryPath = parsed.Path
	}
	title := strings.TrimSuffix(pathpkg.Base(strings.TrimRight(repositoryPath, "/")), ".git")
	if title == "" || title == "." || title == "/" {
		return "repository"
	}
	return title
}

func gitRevisionExists(ctx context.Context, cwd, revision string) (bool, error) {
	_, err := run(ctx, cwd, "git", "rev-parse", "--verify", "--quiet", "--end-of-options", revision+"^{commit}")
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func run(ctx context.Context, cwd, bin string, args ...string) (string, error) {
	return runEnv(ctx, cwd, nil, bin, args...)
}

func runEnv(ctx context.Context, cwd string, extraEnv []string, bin string, args ...string) (string, error) {
	value, err := runCommandOutput(ctx, cwd, extraEnv, bin, args...)
	return strings.TrimSpace(value), err
}

func runUntrimmed(ctx context.Context, cwd, bin string, args ...string) (string, error) {
	return runCommandOutput(ctx, cwd, nil, bin, args...)
}

func runCommandOutput(ctx context.Context, cwd string, extraEnv []string, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
