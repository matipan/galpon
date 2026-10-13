package nativeagent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestClaudeSessionLookupPreservesOwnedHistory(t *testing.T) {
	root, id := t.TempDir(), uuid.NewString()
	preferred := filepath.Join(root, "projects", "galpon-owner", id+".jsonl")
	native := filepath.Join(root, "projects", "worktree-name", id+".jsonl")
	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{\"sessionId\":\""+id+"\",\"type\":\"user\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(native)
	path, err := findClaudeSession(root, id, preferred)
	if err != nil || path != native {
		t.Fatalf("native-only history: path=%q err=%v", path, err)
	}
	write(preferred)
	path, err = findClaudeSession(root, id, preferred)
	if err != nil || path != preferred {
		t.Fatalf("agent-owned history: path=%q err=%v", path, err)
	}
	if err := os.Remove(preferred); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "projects", "another-copy", id+".jsonl"))
	if _, err := findClaudeSession(root, id, preferred); err == nil {
		t.Fatal("selected an ambiguous history")
	}
}
