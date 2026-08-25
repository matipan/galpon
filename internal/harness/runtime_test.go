package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/config"
)

func TestCommandUsesStructuredProtocolsAndMCPBridge(t *testing.T) {
	cfg := config.Config{CodexBin: "/bin/codex", CodexModel: "test-codex", ClaudeBin: "/bin/claude", ClaudeModel: "test-claude"}
	codex, err := Command(cfg, "/bin/galpon", Codex, "/work", "thread-id", "system", true, []string{"/secondary"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(codex, " ")
	for _, want := range []string{"/bin/codex", "--sandbox workspace-write", "--ask-for-approval never", "mcp_servers.galpon.command=", "developer_instructions=", "exec resume --json", "thread-id", "-"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Codex argv omitted %q: %#v", want, codex)
		}
	}
	claude, err := Command(cfg, "/bin/galpon", Claude, "/work", "session-id", "system", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(claude, " ")
	for _, want := range []string{"/bin/claude", "--input-format text", "--output-format stream-json", "--append-system-prompt system", "--strict-mcp-config", `"command":"/bin/galpon"`, "--session-id session-id"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Claude argv omitted %q: %#v", want, claude)
		}
	}
}

func TestParseStructuredHarnessOutput(t *testing.T) {
	codex, err := ParseStructured(Codex, strings.NewReader("{\"type\":\"thread.started\",\"thread_id\":\"thread\"}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"done\"}}\n"))
	if err != nil || codex.SessionID != "thread" || codex.Response != "done" {
		t.Fatalf("Codex result = %#v, %v", codex, err)
	}
	claude, err := ParseStructured(Claude, strings.NewReader("{\"type\":\"system\",\"session_id\":\"session\"}\n{\"type\":\"result\",\"session_id\":\"session\",\"result\":\"complete\",\"is_error\":false}\n"))
	if err != nil || claude.SessionID != "session" || claude.Response != "complete" {
		t.Fatalf("Claude result = %#v, %v", claude, err)
	}
	if _, err := ParseStructured(Claude, strings.NewReader("{\"type\":\"result\",\"result\":\"Not logged in. Run claude auth login.\",\"is_error\":true}\n")); err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("Claude authentication error = %v", err)
	}
}

func TestBoundedAuthenticationStatusProbesDoNotCallModels(t *testing.T) {
	root := t.TempDir()
	codex := filepath.Join(root, "codex")
	claude := filepath.Join(root, "claude")
	if err := os.WriteFile(codex, []byte("#!/bin/sh\n[ \"$1 $2\" = \"login status\" ] || exit 99\necho 'Logged in using test'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claude, []byte("#!/bin/sh\n[ \"$1 $2\" = \"auth status\" ] || exit 99\necho '{\"loggedIn\":false}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := AuthenticationStatus(Codex, codex); got != "authenticated" {
		t.Fatalf("Codex auth = %q", got)
	}
	if got := AuthenticationStatus(Claude, claude); got != "unauthenticated" {
		t.Fatalf("Claude auth = %q", got)
	}
	cfg := config.Config{ClaudeBin: claude}
	if _, err := RequireAvailable(cfg, Claude); err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("Claude readiness = %v", err)
	}
}

func TestInstalledCLIStatusCommandsAreBoundedAndModelFree(t *testing.T) {
	for _, id := range []string{Codex, Claude} {
		path, err := exec.LookPath(id)
		if err != nil {
			continue
		}
		status := AuthenticationStatus(id, path)
		if status != "authenticated" && status != "unauthenticated" && status != "unknown" {
			t.Fatalf("%s status = %q", id, status)
		}
	}
}

func TestCloudAliasMeansClaude(t *testing.T) {
	if got, err := Normalize("Cloud"); err != nil || got != Claude {
		t.Fatalf("Normalize(Cloud) = %q, %v", got, err)
	}
}
