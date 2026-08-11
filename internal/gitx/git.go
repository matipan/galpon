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

func run(ctx context.Context, cwd, bin string, args ...string) (string, error) {
	return runEnv(ctx, cwd, nil, bin, args...)
}

func runEnv(ctx context.Context, cwd string, extraEnv []string, bin string, args ...string) (string, error) {
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
	return strings.TrimSpace(stdout.String()), nil
}
