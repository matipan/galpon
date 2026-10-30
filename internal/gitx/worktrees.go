package gitx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/matipan/galpon/internal/model"
)

// ErrWorktreeChanges means that a removal would discard uncommitted changes.
var ErrWorktreeChanges = errors.New("worktree has uncommitted changes")

// Checkout is what a worktree has checked out now. An agent can change the
// branch after Galpon creates the worktree, so this can differ from the branch
// that Galpon stores.
type Checkout struct {
	Branch   string
	Head     string
	Detached bool
}

// Checkouts reads the checkout of every worktree of one mirror with one Git
// command. The keys are cleaned worktree paths.
func Checkouts(ctx context.Context, mirrorPath string) (map[string]Checkout, error) {
	output, err := runUntrimmed(ctx, "", "git", "--git-dir", mirrorPath, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	out := make(map[string]Checkout)
	path, current, bare := "", Checkout{}, false
	flush := func() {
		if path != "" && !bare && current.Head != "" {
			out[filepath.Clean(path)] = current
		}
		path, current, bare = "", Checkout{}, false
	}
	for _, line := range strings.Split(output, "\x00") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			current.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "detached":
			current.Detached = true
		case line == "bare":
			bare = true
		}
	}
	flush()
	return out, nil
}

// WorktreeCheckout reads the current branch and commit of one worktree.
func WorktreeCheckout(ctx context.Context, path string) (Checkout, error) {
	head, err := run(ctx, path, "git", "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Checkout{}, err
	}
	branch, err := run(ctx, path, "git", "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return Checkout{Head: head, Detached: true}, nil
		}
		return Checkout{}, err
	}
	return Checkout{Branch: branch, Head: head}, nil
}

// WorktreeRequest describes an extra worktree for an agent.
type WorktreeRequest struct {
	Path string
	// Branch is a new or existing local branch. A detached worktree has none.
	Branch string
	// Base is a remote branch, a reference, or a commit. When it is empty, an
	// existing remote branch with the same name is used, then the default branch.
	Base   string
	Remote string
	Detach bool
	Fetch  bool
}

// AddedWorktree reports how AddWorktree created a worktree.
type AddedWorktree struct {
	// Base is the resolved start point: a reference name or a commit.
	Base          string
	Head          string
	CreatedBranch bool
}

// AddWorktree creates a worktree from a managed mirror. It checks out an
// existing local branch, continues a remote branch with the same name, or
// starts a new branch or a detached checkout from a base. A base that the
// mirror does not have, such as pull/123/head, is fetched from the remote.
func AddWorktree(ctx context.Context, repo model.Repository, request WorktreeRequest) (AddedWorktree, error) {
	remote := strings.TrimSpace(request.Remote)
	if remote == "" {
		remote = repo.DefaultRemote
	}
	if !hasRemote(repo.Remotes, remote) {
		return AddedWorktree{}, fmt.Errorf("repository %s has no remote %s", repo.Title, remote)
	}
	branch := strings.TrimSpace(request.Branch)
	base := strings.TrimSpace(request.Base)
	if request.Detach && branch != "" {
		return AddedWorktree{}, fmt.Errorf("a detached worktree has no branch")
	}
	if !request.Detach && branch == "" {
		return AddedWorktree{}, fmt.Errorf("a branch is required for a worktree that is not detached")
	}
	if strings.HasPrefix(base, "-") {
		return AddedWorktree{}, fmt.Errorf("invalid base %q", base)
	}
	if branch != "" {
		if _, err := run(ctx, "", "git", "check-ref-format", "--branch", branch); err != nil {
			return AddedWorktree{}, fmt.Errorf("invalid branch name %q", branch)
		}
	}
	if _, err := os.Lstat(request.Path); err == nil {
		return AddedWorktree{}, fmt.Errorf("worktree path already exists: %s", request.Path)
	} else if !os.IsNotExist(err) {
		return AddedWorktree{}, err
	}
	if err := os.MkdirAll(filepath.Dir(request.Path), 0o700); err != nil {
		return AddedWorktree{}, err
	}
	if request.Fetch {
		if err := fetchRemote(ctx, repo.MirrorPath, remote); err != nil {
			return AddedWorktree{}, err
		}
	}
	_, _ = run(ctx, "", "git", "--git-dir", repo.MirrorPath, "worktree", "prune", "--expire", "now")
	if branch != "" {
		exists, err := gitRevisionExists(ctx, repo.MirrorPath, "refs/heads/"+branch)
		if err != nil {
			return AddedWorktree{}, err
		}
		if exists {
			if base != "" {
				return AddedWorktree{}, fmt.Errorf("branch %s already exists; omit the base to check it out", branch)
			}
			if _, err := run(ctx, "", "git", "--git-dir", repo.MirrorPath, "worktree", "add", request.Path, branch); err != nil {
				return AddedWorktree{}, fmt.Errorf("create worktree: %w", err)
			}
			head, err := run(ctx, request.Path, "git", "rev-parse", "--verify", "HEAD^{commit}")
			return AddedWorktree{Base: "refs/heads/" + branch, Head: head}, err
		}
		if base == "" {
			sameName := "refs/remotes/" + remote + "/" + branch
			if exists, err := gitRevisionExists(ctx, repo.MirrorPath, sameName); err != nil {
				return AddedWorktree{}, err
			} else if exists {
				base = sameName
			}
		}
	}
	start, recorded, temporary, err := resolveWorktreeStart(ctx, repo, remote, base)
	if err != nil {
		return AddedWorktree{}, err
	}
	if temporary != "" {
		defer func() {
			_, _ = run(context.Background(), "", "git", "--git-dir", repo.MirrorPath, "update-ref", "-d", temporary)
		}()
	}
	args := []string{"--git-dir", repo.MirrorPath, "worktree", "add"}
	if request.Detach {
		args = append(args, "--detach", request.Path, start)
	} else {
		args = append(args, "-b", branch, request.Path, start)
	}
	if _, err := run(ctx, "", "git", args...); err != nil {
		return AddedWorktree{}, fmt.Errorf("create worktree: %w", err)
	}
	head, err := run(ctx, request.Path, "git", "rev-parse", "--verify", "HEAD^{commit}")
	return AddedWorktree{Base: recorded, Head: head, CreatedBranch: !request.Detach}, err
}

// resolveWorktreeStart returns the start point for git worktree add and the
// base to record. A base that only the remote has is fetched into a temporary
// reference, which the caller deletes after it creates the worktree.
func resolveWorktreeStart(ctx context.Context, repo model.Repository, remote, base string) (string, string, string, error) {
	for _, candidate := range baseCandidates(base, remote, repo.DefaultBranch) {
		exists, err := gitRevisionExists(ctx, repo.MirrorPath, candidate)
		if err != nil {
			return "", "", "", err
		}
		if exists {
			return candidate, candidate, "", nil
		}
	}
	if base == "" {
		return "", "", "", fmt.Errorf("cannot resolve the default branch of %s", repo.Title)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", "", "", err
	}
	temporary := "refs/galpon/fetch/" + hex.EncodeToString(suffix)
	if _, err := runEnv(ctx, repo.MirrorPath, []string{"GIT_TERMINAL_PROMPT=0"}, "git", "fetch", "--quiet", "--no-tags", remote, "+"+base+":"+temporary); err != nil {
		return "", "", "", fmt.Errorf("cannot find base %s in %s or on remote %s: %w", base, repo.Title, remote, err)
	}
	commit, err := run(ctx, repo.MirrorPath, "git", "rev-parse", "--verify", temporary+"^{commit}")
	if err != nil {
		_, _ = run(context.Background(), "", "git", "--git-dir", repo.MirrorPath, "update-ref", "-d", temporary)
		return "", "", "", fmt.Errorf("base %s is not a commit: %w", base, err)
	}
	return commit, base, temporary, nil
}

// RemovedWorktree reports which local branches a removal deleted or kept.
type RemovedWorktree struct {
	DeletedBranches []string
	KeptBranches    []string
}

// RemoveAgentWorktree removes one extra agent worktree. Without force, it
// stops when the worktree has uncommitted changes. It deletes a local branch
// only when remote-tracking references contain all of its commits, so a
// removal never loses a commit.
func RemoveAgentWorktree(ctx context.Context, repo model.Repository, path string, branches []string, force bool) (RemovedWorktree, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		if !force {
			status, err := runUntrimmed(ctx, path, "git", "status", "--porcelain=v1", "-z", "--untracked-files=normal")
			if err != nil {
				return RemovedWorktree{}, fmt.Errorf("inspect worktree: %w", err)
			}
			if status != "" {
				return RemovedWorktree{}, ErrWorktreeChanges
			}
		}
		if _, err := run(ctx, "", "git", "--git-dir", repo.MirrorPath, "worktree", "remove", "--force", path); err != nil {
			if _, statErr := os.Stat(path); statErr == nil {
				return RemovedWorktree{}, fmt.Errorf("remove worktree: %w", err)
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return RemovedWorktree{}, err
	}
	_, _ = run(ctx, "", "git", "--git-dir", repo.MirrorPath, "worktree", "prune", "--expire", "now")
	var result RemovedWorktree
	seen := make(map[string]bool, len(branches))
	for _, branch := range branches {
		branch = strings.TrimSpace(branch)
		if branch == "" || seen[branch] {
			continue
		}
		seen[branch] = true
		if exists, err := gitRevisionExists(ctx, repo.MirrorPath, "refs/heads/"+branch); err != nil || !exists {
			continue
		}
		unpublished, err := run(ctx, repo.MirrorPath, "git", "rev-list", "--max-count=1", "refs/heads/"+branch, "--not", "--remotes")
		if err != nil || unpublished != "" {
			result.KeptBranches = append(result.KeptBranches, branch)
			continue
		}
		if _, err := run(ctx, "", "git", "--git-dir", repo.MirrorPath, "branch", "-D", branch); err != nil {
			result.KeptBranches = append(result.KeptBranches, branch)
			continue
		}
		result.DeletedBranches = append(result.DeletedBranches, branch)
	}
	return result, nil
}
