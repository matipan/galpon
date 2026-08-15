package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestEncryptedCheckpointRoundTrip(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	agentID := "agent-1"
	session := filepath.Join(stateDir, "agents", agentID, "sessions", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session, []byte("session data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		FormatVersion: FormatVersion, ID: "checkpoint-1", CreatedAt: time.Now().UTC(), SourceStateDir: stateDir,
		State: model.DurableState{Agents: []model.Agent{{ID: agentID, Title: "Agent"}}},
	}
	filePath := filepath.Join(root, "checkpoint.galpon")
	if err := Write(context.Background(), filePath, "correct horse battery staple", stateDir, manifest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || contains(data, []byte("session data")) || contains(data, []byte("checkpoint-1")) {
		t.Fatal("checkpoint contains readable plaintext")
	}
	if _, err := Read(context.Background(), filePath, "wrong passphrase", filepath.Join(root, "wrong")); err == nil {
		t.Fatal("checkpoint accepted a wrong passphrase")
	}
	truncatedPath := filepath.Join(root, "truncated.galpon")
	if err := os.WriteFile(truncatedPath, data[:len(data)-20], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), truncatedPath, "correct horse battery staple", filepath.Join(root, "truncated")); err == nil {
		t.Fatal("checkpoint accepted a truncated file")
	}
	destination := filepath.Join(root, "restored")
	restored, err := Read(context.Background(), filePath, "correct horse battery staple", destination)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != manifest.ID || len(restored.State.Agents) != 1 || restored.State.Agents[0].ID != agentID {
		t.Fatalf("restored manifest = %#v", restored)
	}
	restoredSession, err := os.ReadFile(filepath.Join(destination, "agents", agentID, "sessions", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredSession) != "session data\n" {
		t.Fatalf("restored session = %q", restoredSession)
	}
}

func contains(value, part []byte) bool {
	if len(part) == 0 {
		return true
	}
	for index := 0; index+len(part) <= len(value); index++ {
		match := true
		for offset := range part {
			if value[index+offset] != part[offset] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
