package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeCapabilityFileOverridesStaleInlineValueAndIsRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capability")
	if err := os.WriteFile(path, []byte("file-capability\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GALPON_RUNTIME_CAPABILITY", "stale-inline")
	t.Setenv("GALPON_RUNTIME_CAPABILITY_FILE", path)
	value, err := runtimeCapabilityFromEnvironment()
	if err != nil || value != "file-capability" {
		t.Fatalf("capability = %q, %v", value, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capability file remains: %v", err)
	}
}

func TestDaemonEnvironmentRemovesInheritedRuntimeIdentity(t *testing.T) {
	keys := []string{"GALPON_RUNTIME_ID", "GALPON_RUNTIME_CAPABILITY", "GALPON_RUNTIME_CAPABILITY_FILE", "GALPON_AGENT_ID", "GALPON_AGENT_TITLE", "GALPON_AGENT_ROLE", "GALPON_WORKSPACE_ID", "GALPON_WORKSPACE_TITLE", "GALPON_CURRENT_MESSAGE_ID", "GALPON_CURRENT_MESSAGE_ATTEMPT", "GALPON_INVOCATION_SCOPE", "GALPON_PI_EXTENSION", "GALPON_PLACEMENT"}
	environment := []string{"PATH=/bin", "GALPON_RUNTIME_ID=stale", "GALPON_AGENT_ID=stale", "GALPON_RUNTIME_CAPABILITY=secret"}
	filtered := environmentWithout(environment, keys...)
	joined := strings.Join(filtered, "\n")
	if joined != "PATH=/bin" {
		t.Fatalf("filtered environment = %q", joined)
	}
}
