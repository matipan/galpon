package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/model"
)

const factoryFileRefreshInterval = 5 * time.Second

type factoryFileChange struct {
	Path      string
	Added     int
	Removed   int
	Binary    bool
	Magnitude int
}

func (m *FactoryModel) clearFactoryFileChanges() {
	m.fileChanges = nil
	m.filesForOrder = ""
	m.filesForAgent = ""
	m.filesLoading = false
	m.filesErr = nil
}

func (m *FactoryModel) loadFactoryFileChanges() tea.Cmd {
	order, ok := m.current()
	if !ok || m.agent == nil || len(m.agent.Worktrees) == 0 || m.filesLoading {
		return nil
	}
	workOrderID := order.ID
	agentID := m.agent.Agent.ID
	worktrees := append([]model.Worktree(nil), m.agent.Worktrees...)
	repositories := append([]model.Repository(nil), m.dashboard.Repositories...)
	m.filesLoading = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		changes, err := factoryChangedFiles(ctx, worktrees, repositories)
		return factoryFileChangesLoaded{workOrderID: workOrderID, agentID: agentID, changes: changes, err: err}
	}
}

func factoryChangedFiles(ctx context.Context, worktrees []model.Worktree, repositories []model.Repository) ([]factoryFileChange, error) {
	repositoryByID := make(map[string]model.Repository, len(repositories))
	for _, repository := range repositories {
		repositoryByID[repository.ID] = repository
	}
	changes := make([]factoryFileChange, 0, 16)
	var failures []error
	for _, worktree := range worktrees {
		if strings.TrimSpace(worktree.Path) == "" {
			continue
		}
		repository := repositoryByID[worktree.RepositoryID]
		baseRef := strings.TrimSpace(worktree.BaseRef)
		if baseRef == "" {
			baseRef = strings.TrimSpace(repository.DefaultBranch)
		}
		worktreeChanges, err := factoryWorktreeChanges(ctx, worktree.Path, baseRef)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", worktree.Path, err))
			continue
		}
		name := repository.Title
		if name == "" {
			name = filepath.Base(worktree.Path)
		}
		for index := range worktreeChanges {
			if len(worktrees) > 1 {
				worktreeChanges[index].Path = name + "/" + worktreeChanges[index].Path
			}
		}
		changes = append(changes, worktreeChanges...)
	}
	sort.SliceStable(changes, func(left, right int) bool {
		if changes[left].Magnitude != changes[right].Magnitude {
			return changes[left].Magnitude > changes[right].Magnitude
		}
		return changes[left].Path < changes[right].Path
	})
	if len(changes) > 100 {
		changes = changes[:100]
	}
	return changes, errors.Join(failures...)
}

func factoryWorktreeChanges(ctx context.Context, root, baseRef string) ([]factoryFileChange, error) {
	ref := baseRef
	if ref == "" || exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", "--quiet", ref+"^{commit}").Run() != nil {
		ref = "HEAD"
	}
	output, err := exec.CommandContext(ctx, "git", "-C", root, "diff", "--numstat", "--no-renames", ref, "--").Output()
	if err != nil {
		return nil, fmt.Errorf("read tracked changes: %w", err)
	}
	changes := make([]factoryFileChange, 0, 16)
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		change := factoryFileChange{Path: parts[2]}
		if parts[0] == "-" || parts[1] == "-" {
			change.Binary = true
		} else {
			change.Added, _ = strconv.Atoi(parts[0])
			change.Removed, _ = strconv.Atoi(parts[1])
		}
		change.Magnitude = change.Added + change.Removed
		seen[change.Path] = true
		changes = append(changes, change)
	}
	untracked, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("read untracked files: %w", err)
	}
	for _, item := range bytes.Split(untracked, []byte{0}) {
		path := string(item)
		if path == "" || seen[path] || !safeFactoryRelativePath(path) {
			continue
		}
		added, binary, countErr := factoryUntrackedLineCount(filepath.Join(root, filepath.FromSlash(path)))
		if countErr != nil {
			continue
		}
		changes = append(changes, factoryFileChange{Path: path, Added: added, Binary: binary, Magnitude: added})
	}
	return changes, nil
}

func safeFactoryRelativePath(path string) bool {
	cleaned := filepath.Clean(filepath.FromSlash(path))
	return cleaned != "." && !filepath.IsAbs(cleaned) && cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

func factoryUntrackedLineCount(path string) (int, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, false, err
	}
	if !info.Mode().IsRegular() {
		return 0, true, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, 64<<10)
	lines, total := 0, 0
	var last byte
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			chunk := buffer[:read]
			if bytes.IndexByte(chunk, 0) >= 0 {
				return 0, true, nil
			}
			lines += bytes.Count(chunk, []byte{'\n'})
			total += read
			last = chunk[len(chunk)-1]
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return 0, false, readErr
		}
	}
	if total > 0 && last != '\n' {
		lines++
	}
	return lines, false, nil
}
