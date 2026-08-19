package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/checkpoint"
	"github.com/matipan/galpon/internal/gitx"
	"github.com/matipan/galpon/internal/model"
)

type CheckpointResult struct {
	ID                   string                     `json:"id"`
	Path                 string                     `json:"path"`
	CreatedAt            time.Time                  `json:"createdAt"`
	Resources            model.ResourceCounts       `json:"resources"`
	GitRefs              int                        `json:"gitRefs"`
	DirtyWorktrees       int                        `json:"dirtyWorktrees"`
	IgnoredFiles         int                        `json:"ignoredFiles"`
	UnmanagedDirectories int                        `json:"unmanagedDirectories"`
	Worktrees            []CheckpointWorktreeResult `json:"worktrees"`
}

type CheckpointWorktreeResult struct {
	ID           string `json:"id"`
	Ref          string `json:"ref"`
	Commit       string `json:"commit"`
	Dirty        bool   `json:"dirty"`
	IgnoredFiles int    `json:"ignoredFiles"`
}

type RestoreCheckpointResult struct {
	ID                   string               `json:"id"`
	Path                 string               `json:"path"`
	Resources            model.ResourceCounts `json:"resources"`
	GitRefs              int                  `json:"gitRefs"`
	UnmanagedDirectories int                  `json:"unmanagedDirectories"`
}

// CreateCheckpoint pushes immutable Git references and then writes the
// encrypted logical state file. Soft-deleted resources are not included.
func (a *App) CreateCheckpoint(ctx context.Context, filePath, passphrase string, allowLocalRemotes bool) (CheckpointResult, error) {
	a.agentMutationMu.Lock()
	defer a.agentMutationMu.Unlock()

	var result CheckpointResult
	absolutePath, err := filepath.Abs(strings.TrimSpace(filePath))
	if err != nil {
		return result, err
	}
	if absolutePath == a.Config.StateDir || pathInside(a.Config.StateDir, absolutePath) {
		return result, fmt.Errorf("checkpoint file must be outside the Galpon state directory")
	}
	if _, err := os.Stat(absolutePath); err == nil {
		return result, fmt.Errorf("checkpoint file already exists: %s", absolutePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if strings.TrimSpace(passphrase) == "" {
		return result, fmt.Errorf("checkpoint passphrase is required")
	}
	state, err := a.Store.DurableState(ctx)
	if err != nil {
		return result, err
	}
	result.Worktrees = make([]CheckpointWorktreeResult, 0, len(state.Worktrees))
	for _, agent := range state.Agents {
		if agent.RuntimeID != "" || agent.Status == "running" || agent.Status == "starting" {
			return result, fmt.Errorf("agent %s is active; close active agents before checkpoint creation", agent.Title)
		}
		if agent.Placement.Type == "none" {
			result.UnmanagedDirectories++
		}
		if agent.SessionPath != "" {
			sessionRoot := filepath.Join(a.Config.StateDir, "agents", agent.ID, "sessions")
			if !pathInside(sessionRoot, agent.SessionPath) {
				return result, fmt.Errorf("agent %s session is outside its managed session directory", agent.Title)
			}
			if info, err := os.Stat(agent.SessionPath); err != nil || !info.Mode().IsRegular() {
				if err == nil {
					err = fmt.Errorf("not a regular file")
				}
				return result, fmt.Errorf("read session for agent %s: %w", agent.Title, err)
			}
		}
	}
	repositories := make(map[string]model.Repository, len(state.Repositories))
	for _, repository := range state.Repositories {
		if !pathInside(filepath.Join(a.Config.StateDir, "repositories"), repository.MirrorPath) {
			return result, fmt.Errorf("repository %s mirror is outside managed state", repository.Title)
		}
		pushRemote, ok := checkpointPushRemote(repository)
		if !ok {
			return result, fmt.Errorf("repository %s has no configured push remote", repository.Title)
		}
		if gitx.RemoteIsLocal(pushRemote.PushURL) && !allowLocalRemotes {
			return result, fmt.Errorf("repository %s uses local push remote %s; configure a network remote or use --allow-local-remotes", repository.Title, repository.PushRemote)
		}
		repositories[repository.ID] = repository
	}
	for _, worktree := range state.Worktrees {
		if !pathInside(filepath.Join(a.Config.StateDir, "worktrees"), worktree.Path) {
			return result, fmt.Errorf("worktree %s is outside managed state", worktree.ID)
		}
	}

	checkpointID := uuid.NewString()
	createdAt := time.Now().UTC()
	snapshots := make([]gitx.CheckpointSnapshot, 0, len(state.Worktrees))
	complete := false
	defer func() {
		if complete {
			return
		}
		for index := len(snapshots) - 1; index >= 0; index-- {
			snapshot := snapshots[index]
			for _, worktree := range state.Worktrees {
				if worktree.ID != snapshot.WorktreeID {
					continue
				}
				repository := repositories[worktree.RepositoryID]
				rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := gitx.DeleteCheckpointRef(rollbackCtx, repository, snapshot)
				cancel()
				if err != nil && a.Logger != nil {
					a.Logger.Printf("remove incomplete checkpoint ref %s: %v", snapshot.Ref, err)
				}
				break
			}
		}
	}()
	for _, worktree := range state.Worktrees {
		repository, ok := repositories[worktree.RepositoryID]
		if !ok {
			return result, fmt.Errorf("repository for worktree %s was not found", worktree.ID)
		}
		snapshot, err := gitx.PushCheckpoint(ctx, repository, worktree, checkpointID)
		if err != nil {
			return result, err
		}
		snapshots = append(snapshots, snapshot)
		result.Worktrees = append(result.Worktrees, CheckpointWorktreeResult{
			ID: snapshot.WorktreeID, Ref: snapshot.Ref, Commit: snapshot.Commit,
			Dirty: snapshot.Dirty, IgnoredFiles: snapshot.IgnoredFiles,
		})
		if snapshot.Dirty {
			result.DirtyWorktrees++
		}
		result.IgnoredFiles += snapshot.IgnoredFiles
	}
	portable, err := portableCheckpointState(a.Config.StateDir, state)
	if err != nil {
		return result, err
	}
	manifest := checkpoint.Manifest{
		FormatVersion: checkpoint.FormatVersion, ID: checkpointID, CreatedAt: createdAt,
		SourceStateDir: a.Config.StateDir, State: portable, Git: snapshots,
	}
	if err := checkpoint.Write(ctx, absolutePath, passphrase, a.Config.StateDir, manifest); err != nil {
		return result, err
	}
	complete = true
	result.ID = checkpointID
	result.Path = absolutePath
	result.CreatedAt = createdAt
	result.Resources = stateCounts(state)
	result.GitRefs = len(snapshots)
	return result, nil
}

// RestoreCheckpoint restores an encrypted checkpoint into a new Galpon state
// directory. Remote checkpoint refs remain available for later restores.
func (a *App) RestoreCheckpoint(ctx context.Context, filePath, passphrase string) (RestoreCheckpointResult, error) {
	a.agentMutationMu.Lock()
	defer a.agentMutationMu.Unlock()

	var result RestoreCheckpointResult
	empty, err := a.Store.Empty(ctx)
	if err != nil {
		return result, err
	}
	if !empty {
		return result, fmt.Errorf("checkpoint restore needs an empty Galpon state directory")
	}
	for _, name := range []string{"repositories", "worktrees", "agents"} {
		if clean, err := directoryEmpty(filepath.Join(a.Config.StateDir, name)); err != nil {
			return result, err
		} else if !clean {
			return result, fmt.Errorf("checkpoint restore needs an empty managed %s directory", name)
		}
	}
	temporary, err := os.MkdirTemp(a.Config.StateDir, "checkpoint-restore-*")
	if err != nil {
		return result, err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	manifest, err := checkpoint.Read(ctx, filePath, passphrase, temporary)
	if err != nil {
		return result, err
	}
	if err := validateCheckpointManifest(manifest); err != nil {
		return result, err
	}
	state, oldWorktreePaths, err := restoredCheckpointState(a.Config.StateDir, manifest.SourceStateDir, manifest.State)
	if err != nil {
		return result, err
	}
	if err := validateCheckpointGraph(state, manifest.Git); err != nil {
		return result, err
	}
	repositories := make(map[string]model.Repository, len(state.Repositories))
	createdRepositories := make([]model.Repository, 0, len(state.Repositories))
	createdWorktrees := make([]model.Worktree, 0, len(state.Worktrees))
	createdAgentDirs := make([]string, 0, len(state.Agents))
	createdUnmanagedDirs := make([]string, 0, len(state.Agents))
	complete := false
	defer func() {
		if complete {
			return
		}
		for index := len(createdWorktrees) - 1; index >= 0; index-- {
			worktree := createdWorktrees[index]
			if repository, ok := repositories[worktree.RepositoryID]; ok {
				_ = gitx.CleanupWorktree(context.Background(), repository, worktree.Path, worktree.Branch)
			}
		}
		for _, path := range createdAgentDirs {
			_ = os.RemoveAll(path)
		}
		sort.SliceStable(createdUnmanagedDirs, func(left, right int) bool {
			return len(createdUnmanagedDirs[left]) > len(createdUnmanagedDirs[right])
		})
		for _, path := range createdUnmanagedDirs {
			_ = os.Remove(path)
		}
		for _, repository := range createdRepositories {
			_ = os.RemoveAll(repository.MirrorPath)
		}
		for _, name := range []string{"worktrees", "repositories", "agents"} {
			_ = os.RemoveAll(filepath.Join(a.Config.StateDir, name))
		}
	}()
	for _, repository := range state.Repositories {
		if err := gitx.CreateMirror(ctx, repository.Remotes, repository.DefaultRemote, repository.PushRemote, repository.MirrorPath); err != nil {
			return result, fmt.Errorf("restore repository %s: %w", repository.Title, err)
		}
		createdRepositories = append(createdRepositories, repository)
		repositories[repository.ID] = repository
	}
	snapshotByWorktree := make(map[string]gitx.CheckpointSnapshot, len(manifest.Git))
	for _, snapshot := range manifest.Git {
		snapshotByWorktree[snapshot.WorktreeID] = snapshot
	}
	for _, worktree := range state.Worktrees {
		repository, ok := repositories[worktree.RepositoryID]
		if !ok {
			return result, fmt.Errorf("repository for worktree %s was not found", worktree.ID)
		}
		if err := gitx.RestoreCheckpoint(ctx, repository, worktree, snapshotByWorktree[worktree.ID]); err != nil {
			return result, err
		}
		createdWorktrees = append(createdWorktrees, worktree)
	}
	for _, agent := range state.Agents {
		source := filepath.Join(temporary, "agents", agent.ID)
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			if agent.SessionPath != "" {
				return result, fmt.Errorf("checkpoint session for agent %s is missing", agent.Title)
			}
			continue
		} else if err != nil {
			return result, err
		}
		target := filepath.Join(a.Config.StateDir, "agents", agent.ID)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return result, err
		}
		if err := os.Rename(source, target); err != nil {
			return result, err
		}
		createdAgentDirs = append(createdAgentDirs, target)
	}
	if err := rewriteSessionDirectories(filepath.Join(a.Config.StateDir, "agents"), manifest.SourceStateDir, a.Config.StateDir, oldWorktreePaths); err != nil {
		return result, err
	}
	for _, agent := range state.Agents {
		if agent.Placement.Type != "none" {
			continue
		}
		created, err := ensureDirectory(agent.Placement.CWD)
		createdUnmanagedDirs = append(createdUnmanagedDirs, created...)
		if err != nil {
			return result, fmt.Errorf("restore directory for agent %s: %w", agent.Title, err)
		}
		result.UnmanagedDirectories++
	}
	if err := a.Store.RestoreDurableState(ctx, state); err != nil {
		return result, err
	}
	complete = true
	result.ID = manifest.ID
	absolutePath, _ := filepath.Abs(filePath)
	result.Path = absolutePath
	result.Resources = stateCounts(state)
	result.GitRefs = len(manifest.Git)
	return result, nil
}

func portableCheckpointState(stateDir string, source model.DurableState) (model.DurableState, error) {
	state := model.DurableState{
		Repositories:           append([]model.Repository(nil), source.Repositories...),
		Workspaces:             append([]model.Workspace(nil), source.Workspaces...),
		Worktrees:              append([]model.Worktree(nil), source.Worktrees...),
		Agents:                 append([]model.Agent(nil), source.Agents...),
		Messages:               append([]model.AgentMessage(nil), source.Messages...),
		MessageIdempotencyKeys: make(map[string]string, len(source.MessageIdempotencyKeys)),
		LifecycleEvents:        append([]model.LifecycleEvent(nil), source.LifecycleEvents...),
	}
	for id, key := range source.MessageIdempotencyKeys {
		state.MessageIdempotencyKeys[id] = key
	}
	for index := range state.Repositories {
		state.Repositories[index].Remotes = append([]model.RepositoryRemote(nil), state.Repositories[index].Remotes...)
	}
	for index := range state.Agents {
		state.Agents[index].Placement.Worktrees = append([]model.AgentWorktree(nil), state.Agents[index].Placement.Worktrees...)
	}
	for index := range state.Repositories {
		state.Repositories[index].MirrorPath = ""
	}
	for index := range state.Worktrees {
		relative, err := managedRelativePath(stateDir, state.Worktrees[index].Path, "worktrees")
		if err != nil {
			return state, err
		}
		state.Worktrees[index].Path = filepath.ToSlash(relative)
	}
	for index := range state.Workspaces {
		state.Workspaces[index].Renderer = ""
		state.Workspaces[index].RendererContext = ""
		state.Workspaces[index].RendererID = ""
	}
	for index := range state.Agents {
		agent := &state.Agents[index]
		if agent.SessionPath != "" {
			relative, err := managedRelativePath(stateDir, agent.SessionPath, filepath.Join("agents", agent.ID, "sessions"))
			if err != nil {
				return state, err
			}
			agent.SessionPath = filepath.ToSlash(relative)
		}
		agent.Status = "stopped"
		agent.Renderer = ""
		agent.RendererContext = ""
		agent.RendererID = ""
		agent.RuntimeID = ""
		agent.LastError = ""
	}
	for index := range state.Messages {
		if state.Messages[index].Status == "delivered" {
			state.Messages[index].Status = "queued"
			if state.Messages[index].Kind == "result" {
				state.Messages[index].NotificationState = "pending"
			}
			state.Messages[index].ClaimKey = ""
			state.Messages[index].ClaimedAt = 0
			state.Messages[index].LeaseExpiresAt = 0
			state.Messages[index].LastError = "checkpoint restored before completion"
		}
		state.Messages[index].RuntimeID = ""
	}
	return state, nil
}

func restoredCheckpointState(stateDir, sourceStateDir string, state model.DurableState) (model.DurableState, map[string]string, error) {
	oldPaths := make(map[string]string, len(state.Worktrees))
	for index := range state.Repositories {
		state.Repositories[index].MirrorPath = filepath.Join(stateDir, "repositories", state.Repositories[index].ID+".git")
	}
	for index := range state.Worktrees {
		relative := filepath.FromSlash(state.Worktrees[index].Path)
		path, err := restoreManagedPath(stateDir, relative, "worktrees")
		if err != nil {
			return state, nil, fmt.Errorf("restore worktree %s path: %w", state.Worktrees[index].ID, err)
		}
		oldPaths[state.Worktrees[index].ID] = state.Worktrees[index].Path
		state.Worktrees[index].Path = path
	}
	for index := range state.Agents {
		agent := &state.Agents[index]
		if agent.Placement.Type == "none" {
			cwd := filepath.Clean(strings.TrimSpace(agent.Placement.CWD))
			if !filepath.IsAbs(cwd) {
				return state, nil, fmt.Errorf("checkpoint contains an invalid directory for agent %s", agent.Title)
			}
			if relative, err := filepath.Rel(sourceStateDir, cwd); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
				cwd = filepath.Join(stateDir, relative)
			}
			agent.Placement.CWD = cwd
		}
		if agent.SessionPath != "" {
			relative := filepath.FromSlash(agent.SessionPath)
			path, err := restoreManagedPath(stateDir, relative, filepath.Join("agents", agent.ID, "sessions"))
			if err != nil {
				return state, nil, fmt.Errorf("restore session path for agent %s: %w", agent.Title, err)
			}
			agent.SessionPath = path
		}
		agent.Status = "stopped"
		agent.Renderer = ""
		agent.RendererContext = ""
		agent.RendererID = ""
		agent.RuntimeID = ""
		agent.LastError = ""
	}
	return state, oldPaths, nil
}

func validateCheckpointManifest(manifest checkpoint.Manifest) error {
	if _, err := uuid.Parse(manifest.ID); err != nil {
		return fmt.Errorf("checkpoint has an invalid ID")
	}
	if !filepath.IsAbs(manifest.SourceStateDir) {
		return fmt.Errorf("checkpoint has an invalid source state directory")
	}
	prefix := "refs/heads/galpon-checkpoints/" + manifest.ID + "/"
	for _, snapshot := range manifest.Git {
		if snapshot.Ref != prefix+snapshot.WorktreeID || !validObjectID(snapshot.Commit) || !validObjectID(snapshot.Head) || !validObjectID(snapshot.IndexTree) || !validObjectID(snapshot.WorktreeTree) {
			return fmt.Errorf("checkpoint has invalid Git data for worktree %s", snapshot.WorktreeID)
		}
	}
	return nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validateCheckpointGraph(state model.DurableState, snapshots []gitx.CheckpointSnapshot) error {
	repositories := make(map[string]bool, len(state.Repositories))
	for _, repository := range state.Repositories {
		if repository.ID == "" || repositories[repository.ID] || len(repository.Remotes) == 0 {
			return fmt.Errorf("checkpoint contains an invalid repository")
		}
		repositories[repository.ID] = true
	}
	workspaces := make(map[string]bool, len(state.Workspaces))
	for _, workspace := range state.Workspaces {
		if workspace.ID == "" || workspaces[workspace.ID] {
			return fmt.Errorf("checkpoint contains an invalid workspace")
		}
		workspaces[workspace.ID] = true
	}
	worktrees := make(map[string]bool, len(state.Worktrees))
	for _, worktree := range state.Worktrees {
		if worktree.ID == "" || worktrees[worktree.ID] || !repositories[worktree.RepositoryID] || !workspaces[worktree.WorkspaceID] {
			return fmt.Errorf("checkpoint contains an invalid worktree %s", worktree.ID)
		}
		worktrees[worktree.ID] = true
	}
	snapshotIDs := make(map[string]bool, len(snapshots))
	for _, snapshot := range snapshots {
		if !worktrees[snapshot.WorktreeID] || snapshotIDs[snapshot.WorktreeID] || snapshot.Ref == "" || snapshot.Commit == "" || snapshot.Head == "" {
			return fmt.Errorf("checkpoint contains an invalid Git snapshot for worktree %s", snapshot.WorktreeID)
		}
		snapshotIDs[snapshot.WorktreeID] = true
	}
	if len(snapshotIDs) != len(worktrees) {
		return fmt.Errorf("checkpoint does not contain a Git snapshot for every worktree")
	}
	agents := make(map[string]bool, len(state.Agents))
	for _, agent := range state.Agents {
		if agent.ID == "" || agents[agent.ID] || !workspaces[agent.WorkspaceID] {
			return fmt.Errorf("checkpoint contains an invalid agent %s", agent.ID)
		}
		for _, assignment := range agent.Placement.Worktrees {
			if !worktrees[assignment.WorktreeID] {
				return fmt.Errorf("agent %s references missing worktree %s", agent.ID, assignment.WorktreeID)
			}
		}
		agents[agent.ID] = true
	}
	messages := make(map[string]bool, len(state.Messages))
	for _, message := range state.Messages {
		if message.ID == "" || messages[message.ID] || !agents[message.TargetAgentID] {
			return fmt.Errorf("checkpoint contains an invalid message %s", message.ID)
		}
		messages[message.ID] = true
	}
	for messageID, key := range state.MessageIdempotencyKeys {
		if !messages[messageID] || strings.TrimSpace(key) == "" || len(key) > 200 {
			return fmt.Errorf("checkpoint contains an invalid message idempotency key")
		}
	}
	return nil
}

func managedRelativePath(stateDir, absolutePath, requiredRoot string) (string, error) {
	relative, err := filepath.Rel(stateDir, absolutePath)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("managed path is outside the state directory: %s", absolutePath)
	}
	required := filepath.Clean(requiredRoot)
	if relative == required || !strings.HasPrefix(relative, required+string(os.PathSeparator)) {
		return "", fmt.Errorf("managed path %s is outside %s", absolutePath, requiredRoot)
	}
	return relative, nil
}

func restoreManagedPath(stateDir, relative, requiredRoot string) (string, error) {
	if filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path is not a safe relative path")
	}
	path := filepath.Join(stateDir, relative)
	root := filepath.Join(stateDir, requiredRoot)
	if !pathInside(root, path) {
		return "", fmt.Errorf("path is outside %s", requiredRoot)
	}
	return path, nil
}

func ensureDirectory(path string) ([]string, error) {
	var missing []string
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return missing, fmt.Errorf("path is not a directory: %s", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return missing, err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return missing, fmt.Errorf("no existing parent directory for %s", path)
		}
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return missing, err
	}
	return missing, nil
}

func directoryEmpty(path string) (bool, error) {
	directory, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = directory.Close() }()
	_, err = directory.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return false, err
}

func rewriteSessionDirectories(agentRoot, oldStateDir, newStateDir string, oldWorktreePaths map[string]string) error {
	if _, err := os.Stat(agentRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	oldToNew := make(map[string]string, len(oldWorktreePaths))
	for _, portablePath := range oldWorktreePaths {
		oldToNew[filepath.Join(oldStateDir, filepath.FromSlash(portablePath))] = filepath.Join(newStateDir, filepath.FromSlash(portablePath))
	}
	return filepath.WalkDir(agentRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		return rewriteSessionFile(path, oldStateDir, newStateDir, oldToNew)
	})
}

func rewriteSessionFile(path, oldStateDir, newStateDir string, oldToNew map[string]string) error {
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		_ = input.Close()
		return err
	}
	temporaryPath := temporary.Name()
	complete := false
	defer func() {
		_ = input.Close()
		_ = temporary.Close()
		if !complete {
			_ = os.Remove(temporaryPath)
		}
	}()
	reader := bufio.NewReader(input)
	writer := bufio.NewWriter(temporary)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) != 0 {
			hadNewline := line[len(line)-1] == '\n'
			content := line
			if hadNewline {
				content = line[:len(line)-1]
			}
			var record map[string]any
			if json.Unmarshal(content, &record) == nil && record["type"] == "session" {
				if cwd, ok := record["cwd"].(string); ok {
					if replacement, ok := oldToNew[filepath.Clean(cwd)]; ok {
						record["cwd"] = replacement
					} else if relative, err := filepath.Rel(oldStateDir, cwd); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
						record["cwd"] = filepath.Join(newStateDir, relative)
					}
				}
				content, err = json.Marshal(record)
				if err != nil {
					return err
				}
			}
			if _, err := writer.Write(content); err != nil {
				return err
			}
			if hadNewline {
				if err := writer.WriteByte('\n'); err != nil {
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := input.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	complete = true
	return nil
}

func checkpointPushRemote(repository model.Repository) (model.RepositoryRemote, bool) {
	for _, remote := range repository.Remotes {
		if remote.Name == repository.PushRemote {
			if remote.PushURL == "" {
				remote.PushURL = remote.FetchURL
			}
			return remote, true
		}
	}
	return model.RepositoryRemote{}, false
}

func stateCounts(state model.DurableState) model.ResourceCounts {
	return model.ResourceCounts{
		Repositories: len(state.Repositories), Workspaces: len(state.Workspaces),
		Worktrees: len(state.Worktrees), Agents: len(state.Agents),
	}
}
