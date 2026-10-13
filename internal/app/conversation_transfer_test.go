package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/piagent"
)

const transferSession = `{"type":"session","version":3,"id":"writer","timestamp":"2026-09-01T10:00:00.000Z","cwd":"/old/machine/project"}
{"type":"message","id":"aaaa1111","parentId":null,"timestamp":"2026-09-01T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"Remember the code word heron"}],"timestamp":1756720801000}}
{"type":"message","id":"bbbb2222","parentId":"aaaa1111","timestamp":"2026-09-01T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Noted"}],"stopReason":"stop","timestamp":1756720802000}}
`

// exportTestConversation exports an idle agent whose Pi is writing a new line.
func exportTestConversation(t *testing.T) string {
	t.Helper()
	source := communicationRuntimeTestApp(t)
	putCommunicationAgent(t, source, "writer")
	session := filepath.Join(source.Config.StateDir, "writer.jsonl")
	if err := os.WriteFile(session, []byte(transferSession+`{"type":"message","id":"partial`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := source.Store.RegisterAgentRuntime(t.Context(), "writer", "writer-runtime", "writer", session); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "writer.galpon-conversation")
	result, err := source.ExportConversation(t.Context(), "writer", path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Agent.Title != "writer" || result.Session.Bytes != int64(len(transferSession)) {
		t.Fatalf("export result = %#v", result)
	}
	before, _ := os.ReadFile(path)
	if _, err := source.ExportConversation(t.Context(), "writer", path); err == nil {
		t.Fatal("a second export replaced an existing file")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("a refused export changed the existing file")
	}
	return path
}

func transferTargetApp(t *testing.T) *App {
	t.Helper()
	target := communicationRuntimeTestApp(t)
	now := time.Now().UnixMilli()
	if err := target.Store.PutWorkspace(t.Context(), model.Workspace{ID: "workspace", Title: "Target", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return target
}

func TestConversationImportForksTheExportedPiSession(t *testing.T) {
	path := exportTestConversation(t)
	target := transferTargetApp(t)
	target.Config.DefaultHarness = "codex"
	imported, err := target.ImportConversation(t.Context(), ImportConversationRequest{Path: path, WorkspaceID: "workspace", Role: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	agent := imported.Agent
	if agent.Kind != "pi" || agent.Title != "writer" || agent.Role != "reviewer" || agent.ID == "writer" || agent.SessionPath != "" || imported.Source.Workspace != "Workspace" {
		t.Fatalf("imported agent = %#v, source = %#v", agent, imported.Source)
	}

	fork := piagent.ForkSource(target.Config.StateDir, agent, nil)
	data, err := os.ReadFile(fork)
	if err != nil {
		t.Fatalf("imported session for the first launch: %v", err)
	}
	// The complete exported lines stay exact. The unfinished line stays out.
	if !strings.HasPrefix(string(data), transferSession) || strings.Contains(string(data), "partial") {
		t.Fatalf("imported session = %s", data)
	}
	boundary := strings.TrimSuffix(strings.TrimPrefix(string(data), transferSession), "\n")
	if strings.Contains(boundary, "\n") || !strings.Contains(boundary, `"status":"conversation_imported"`) || !strings.Contains(boundary, `"parentId":"bbbb2222"`) {
		t.Fatalf("import boundary after the Pi leaf = %s", boundary)
	}
	command := piagent.Command(config.Config{PiBin: "pi", StateDir: target.Config.StateDir}, piagent.Assets{Extension: "galpon.ts"}, agent, fork)
	if index := slices.Index(command, "--fork"); index < 0 || command[index+1] != fork || !slices.Contains(command, agent.ID) {
		t.Fatalf("first launch does not fork the imported session: %v", command)
	}

	// After Pi writes the forked session, the agent uses it and the copy goes.
	forked := filepath.Join(target.Config.StateDir, "agents", agent.ID, "sessions", "forked.jsonl")
	if err := os.MkdirAll(filepath.Dir(forked), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(forked, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := target.PrepareRuntime(t.Context(), agent.ID, "runtime"); err != nil {
		t.Fatal(err)
	}
	if err := target.RegisterRuntime(t.Context(), agent.ID, "runtime", agent.SessionID, forked); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fork); !os.IsNotExist(err) {
		t.Fatalf("import copy remained after Pi registered its fork: %v", err)
	}
	registered, err := target.Store.Agent(t.Context(), agent.ID)
	if err != nil || piagent.ForkSource(target.Config.StateDir, registered, nil) != "" {
		t.Fatalf("a later launch would fork again: %#v, %v", registered, err)
	}
}

func TestConversationImportRejectsDamagedExports(t *testing.T) {
	path := exportTestConversation(t)
	damaged := filepath.Join(t.TempDir(), "damaged.galpon-conversation")
	rewriteConversationArchive(t, path, damaged, func(name string, data []byte) []byte {
		if name == conversationSessionName {
			return bytes.Replace(data, []byte("heron"), []byte("raven"), 1)
		}
		return data
	})
	notExport := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(notExport, []byte(transferSession), 0o600); err != nil {
		t.Fatal(err)
	}
	target := transferTargetApp(t)
	for file, want := range map[string]string{damaged: "checksum does not match", notExport: "is not a Galpon conversation export"} {
		_, err := target.ImportConversation(t.Context(), ImportConversationRequest{Path: file, WorkspaceID: "workspace"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("import %s error = %v, want %q", filepath.Base(file), err, want)
		}
	}
	dashboard, err := target.Store.Dashboard(t.Context())
	if err != nil || len(dashboard.Agents) != 0 {
		t.Fatalf("a refused import created agents: %#v, %v", dashboard.Agents, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(target.Config.StateDir, "agents")); len(entries) != 0 {
		t.Fatalf("a refused import left files: %v", entries)
	}
}

func rewriteConversationArchive(t *testing.T, source, target string, change func(string, []byte) []byte) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	compressed, err := gzip.NewReader(input)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writerGzip := gzip.NewWriter(&output)
	writer := tar.NewWriter(writerGzip)
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		data = change(header.Name, data)
		header.Size = int64(len(data))
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writerGzip.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
