package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/piagent"
	"github.com/matipan/galpon/internal/sessionfile"
)

// A conversation export moves one native session to another Galpon instance.
// It holds no Galpon database rows, worktree files, or harness credentials.
const (
	conversationFormat        = "galpon-conversation"
	conversationFormatVersion = 1
	conversationManifestName  = "manifest.json"
	conversationSessionName   = "session.jsonl"
	conversationManifestLimit = 1 << 20
	conversationSessionLimit  = 4 << 30
	conversationHeaderLimit   = 1 << 20
	// The Galpon Pi extension discards recovery state recorded before this
	// entry. That state names operations of the source instance.
	conversationImportedStatus = "conversation_imported"
)

type ConversationAgent struct {
	Harness string `json:"harness,omitempty"`
	Title   string `json:"title"`
	Role    string `json:"role,omitempty"`
}

type ConversationSource struct {
	Workspace string `json:"workspace,omitempty"`
	Placement string `json:"placement,omitempty"`
}

type ConversationSession struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type ConversationManifest struct {
	Format     string              `json:"format"`
	Version    int                 `json:"version"`
	ExportedAt int64               `json:"exportedAt"`
	Agent      ConversationAgent   `json:"agent"`
	Source     ConversationSource  `json:"source"`
	Session    ConversationSession `json:"session"`
}

type ExportConversationResult struct {
	Path    string              `json:"path"`
	Agent   ConversationAgent   `json:"agent"`
	Session ConversationSession `json:"session"`
}

type ImportConversationRequest struct {
	Path        string                `json:"path"`
	Title       string                `json:"title,omitempty"`
	Role        string                `json:"role,omitempty"`
	WorkspaceID string                `json:"workspaceId"`
	Placement   AgentPlacementRequest `json:"placement"`
}

type ImportConversationResult struct {
	Agent      model.Agent         `json:"agent"`
	Source     ConversationSource  `json:"source"`
	ExportedAt int64               `json:"exportedAt"`
	Session    ConversationSession `json:"session"`
}

// ExportConversation writes the agent's native session to a new file. It
// uses the same rule as a context fork: the agent must not be working.
func (a *App) ExportConversation(ctx context.Context, agentID, path string) (ExportConversationResult, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return ExportConversationResult{}, fmt.Errorf("conversation export needs an absolute file path")
	}
	dashboard, err := a.Store.Dashboard(ctx)
	if err != nil {
		return ExportConversationResult{}, err
	}
	agent, ok := dashboard.Agent(strings.TrimSpace(agentID))
	if !ok {
		return ExportConversationResult{}, fmt.Errorf("agent not found")
	}
	if agent.SessionPath == "" {
		return ExportConversationResult{}, fmt.Errorf("agent has no saved session to export")
	}
	if agent.Status == "running" || agent.Status == "starting" {
		return ExportConversationResult{}, fmt.Errorf("agent must be idle or stopped before its conversation is exported")
	}
	if _, err := os.Lstat(path); err == nil {
		return ExportConversationResult{}, fmt.Errorf("export file already exists: %s", path)
	}
	source, err := os.Open(agent.SessionPath)
	if err != nil {
		return ExportConversationResult{}, fmt.Errorf("open native session: %w", err)
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil {
		return ExportConversationResult{}, err
	}
	// Export only complete records from the current session snapshot.
	size, err := completeLinesSize(source, info.Size())
	if err != nil {
		return ExportConversationResult{}, err
	}
	if size == 0 {
		return ExportConversationResult{}, fmt.Errorf("pi session is empty")
	}
	if size > conversationSessionLimit {
		return ExportConversationResult{}, fmt.Errorf("session is larger than %d bytes", int64(conversationSessionLimit))
	}
	if err := validateHarnessSessionHeader(io.NewSectionReader(source, 0, size), agent.Harness()); err != nil {
		return ExportConversationResult{}, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(source, 0, size)); err != nil {
		return ExportConversationResult{}, err
	}
	workspace, _ := dashboard.Workspace(agent.WorkspaceID)
	manifest := ConversationManifest{
		Format: conversationFormat, Version: conversationFormatVersion, ExportedAt: time.Now().UnixMilli(),
		Agent:   ConversationAgent{Harness: agent.Harness(), Title: agent.Title, Role: agent.Role},
		Source:  ConversationSource{Workspace: workspace.Title, Placement: backgroundPlacementDescription(dashboard, agent)},
		Session: ConversationSession{Bytes: size, SHA256: hex.EncodeToString(hash.Sum(nil))},
	}
	if err := writeConversationArchive(path, manifest, io.NewSectionReader(source, 0, size)); err != nil {
		return ExportConversationResult{}, err
	}
	return ExportConversationResult{Path: path, Agent: manifest.Agent, Session: manifest.Session}, nil
}

// ImportConversation creates an agent with the exported harness. Its first
// launch forks the imported session into the new placement.
func (a *App) ImportConversation(ctx context.Context, request ImportConversationRequest) (ImportConversationResult, error) {
	path := filepath.Clean(strings.TrimSpace(request.Path))
	if !filepath.IsAbs(path) {
		return ImportConversationResult{}, fmt.Errorf("conversation import needs an absolute file path")
	}
	agentsRoot := filepath.Join(a.Config.StateDir, "agents")
	if err := os.MkdirAll(agentsRoot, 0o700); err != nil {
		return ImportConversationResult{}, err
	}
	// Stage below the state directory so CreateAgent can rename the session.
	staging, err := os.MkdirTemp(agentsRoot, ".import-*")
	if err != nil {
		return ImportConversationResult{}, err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	manifest, sessionPath, err := readConversationArchive(path, staging)
	if err != nil {
		return ImportConversationResult{}, err
	}
	if manifest.Agent.Harness == model.HarnessPi {
		if err := appendImportBoundary(sessionPath); err != nil {
			return ImportConversationResult{}, err
		}
	}
	title := strings.TrimSpace(request.Title)
	if title == "" {
		title = manifest.Agent.Title
	}
	role := strings.TrimSpace(request.Role)
	if role == "" {
		role = manifest.Agent.Role
	}
	agent, err := a.CreateAgent(ctx, CreateAgentRequest{
		Title: title, Role: role, WorkspaceID: request.WorkspaceID, Placement: request.Placement,
		Harness: manifest.Agent.Harness, importedSession: sessionPath,
	})
	if err != nil {
		return ImportConversationResult{}, err
	}
	return ImportConversationResult{Agent: agent, Source: manifest.Source, ExportedAt: manifest.ExportedAt, Session: manifest.Session}, nil
}

// releaseImportedSession removes the import copy after the harness writes the
// forked session that the agent now uses.
func (a *App) releaseImportedSession(agentID, sessionPath string) {
	imported := piagent.ImportedSessionPath(a.Config.StateDir, agentID)
	if imported == "" || sessionPath == "" || filepath.Clean(sessionPath) == imported {
		return
	}
	if info, err := os.Stat(sessionPath); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return
	}
	_ = os.RemoveAll(filepath.Dir(imported))
}

func completeLinesSize(file *os.File, size int64) (int64, error) {
	buffer := make([]byte, 64<<10)
	for end := size; end > 0; {
		start := max(0, end-int64(len(buffer)))
		chunk := buffer[:end-start]
		if _, err := file.ReadAt(chunk, start); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if index := bytes.LastIndexByte(chunk, '\n'); index >= 0 {
			return start + int64(index) + 1, nil
		}
		end = start
	}
	return 0, nil
}

func validateHarnessSessionHeader(reader io.Reader, kind string) error {
	if kind == model.HarnessPi {
		return validateSessionHeader(reader)
	}
	_, err := sessionfile.NativeID(kind, reader)
	return err
}

func validateSessionHeader(reader io.Reader) error {
	line, err := readFirstLine(reader, conversationHeaderLimit)
	if err != nil {
		return err
	}
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &header) != nil || header.Type != "session" {
		return fmt.Errorf("the file does not start with a Pi session header")
	}
	return nil
}

func readFirstLine(reader io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)))
	if err != nil {
		return nil, err
	}
	index := bytes.IndexByte(data, '\n')
	if index < 0 {
		return nil, fmt.Errorf("pi session header is incomplete")
	}
	return data[:index], nil
}

func writeConversationArchive(path string, manifest ConversationManifest, session io.Reader) error {
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".galpon-conversation-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	compressed := gzip.NewWriter(temporary)
	archive := tar.NewWriter(compressed)
	modified := time.UnixMilli(manifest.ExportedAt)
	if err := archive.WriteHeader(&tar.Header{Name: conversationManifestName, Mode: 0o600, Size: int64(len(manifestData)), ModTime: modified, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := archive.Write(manifestData); err != nil {
		return err
	}
	if err := archive.WriteHeader(&tar.Header{Name: conversationSessionName, Mode: 0o600, Size: manifest.Session.Bytes, ModTime: modified, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	// Hash again while writing. A Pi rewrite during export must not produce an
	// archive whose checksum does not match its session.
	hash := sha256.New()
	if written, err := io.Copy(archive, io.TeeReader(session, hash)); err != nil {
		return err
	} else if written != manifest.Session.Bytes {
		return fmt.Errorf("pi session changed during export")
	}
	if hex.EncodeToString(hash.Sum(nil)) != manifest.Session.SHA256 {
		return fmt.Errorf("pi session changed during export")
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// A hard link never replaces an existing file.
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("export file already exists: %s", path)
		}
		if _, statErr := os.Lstat(path); statErr == nil {
			return fmt.Errorf("export file already exists: %s", path)
		}
		return os.Rename(temporaryPath, path)
	}
	return nil
}

func readConversationArchive(path, directory string) (ConversationManifest, string, error) {
	invalid := fmt.Errorf("%s is not a Galpon conversation export", path)
	file, err := os.Open(path)
	if err != nil {
		return ConversationManifest{}, "", err
	}
	defer func() { _ = file.Close() }()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return ConversationManifest{}, "", invalid
	}
	defer func() { _ = compressed.Close() }()
	archive := tar.NewReader(compressed)
	header, err := archive.Next()
	if err != nil || header.Name != conversationManifestName || header.Typeflag != tar.TypeReg || header.Size > conversationManifestLimit {
		return ConversationManifest{}, "", invalid
	}
	var manifest ConversationManifest
	if err := json.NewDecoder(io.LimitReader(archive, conversationManifestLimit)).Decode(&manifest); err != nil || manifest.Format != conversationFormat {
		return ConversationManifest{}, "", invalid
	}
	if manifest.Version != conversationFormatVersion {
		return ConversationManifest{}, "", fmt.Errorf("conversation export version %d is not supported; this Galpon reads version %d", manifest.Version, conversationFormatVersion)
	}
	manifest.Agent.Harness, err = model.ParseHarness(manifest.Agent.Harness)
	if err != nil {
		return ConversationManifest{}, "", err
	}
	header, err = archive.Next()
	if err != nil || header.Name != conversationSessionName || header.Typeflag != tar.TypeReg ||
		header.Size <= 0 || header.Size > conversationSessionLimit || header.Size != manifest.Session.Bytes {
		return ConversationManifest{}, "", invalid
	}
	sessionPath := filepath.Join(directory, conversationSessionName)
	output, err := os.OpenFile(sessionPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ConversationManifest{}, "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), archive)
	closeErr := output.Close()
	if copyErr != nil {
		return ConversationManifest{}, "", fmt.Errorf("read conversation export: %w", copyErr)
	}
	if closeErr != nil {
		return ConversationManifest{}, "", closeErr
	}
	if written != manifest.Session.Bytes || hex.EncodeToString(hash.Sum(nil)) != manifest.Session.SHA256 {
		return ConversationManifest{}, "", fmt.Errorf("conversation export is damaged: the session checksum does not match")
	}
	if _, err := archive.Next(); !errors.Is(err, io.EOF) {
		return ConversationManifest{}, "", invalid
	}
	session, err := os.Open(sessionPath)
	if err != nil {
		return ConversationManifest{}, "", err
	}
	defer func() { _ = session.Close() }()
	if err := validateHarnessSessionHeader(session, manifest.Agent.Harness); err != nil {
		return ConversationManifest{}, "", err
	}
	return manifest, sessionPath, nil
}

// appendImportBoundary adds a Galpon entry after Pi's current leaf. Pi resumes
// from the last entry, so the fork continues after this boundary.
func appendImportBoundary(sessionPath string) error {
	file, err := os.OpenFile(sessionPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	last, err := lastLine(file, info.Size())
	if err != nil {
		return err
	}
	var leaf struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(last, &leaf); err != nil {
		return fmt.Errorf("the last Pi session entry is not valid JSON")
	}
	var parent any
	if leaf.Type != "session" && leaf.ID != "" {
		parent = leaf.ID
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	entry, err := json.Marshal(map[string]any{
		"type": "custom", "customType": "galpon-operation",
		"data":      map[string]any{"status": conversationImportedStatus},
		"id":        "import-" + hex.EncodeToString(suffix),
		"parentId":  parent,
		"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	if err != nil {
		return err
	}
	if _, err := file.WriteAt(append(entry, '\n'), info.Size()); err != nil {
		return err
	}
	return file.Sync()
}

func lastLine(file *os.File, size int64) ([]byte, error) {
	end, err := completeLinesSize(file, size)
	if err != nil {
		return nil, err
	}
	if end != size || end == 0 {
		return nil, fmt.Errorf("pi session does not end with a complete entry")
	}
	start, err := completeLinesSize(file, end-1)
	if err != nil {
		return nil, err
	}
	if end-1-start > conversationManifestLimit*64 {
		return nil, fmt.Errorf("the last Pi session entry is too large")
	}
	line := make([]byte, end-1-start)
	if _, err := file.ReadAt(line, start); err != nil {
		return nil, err
	}
	return line, nil
}
