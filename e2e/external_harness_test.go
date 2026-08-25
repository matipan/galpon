package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/config"
)

func TestFakeExternalHarnessesUseMCPAndDurableProtocol(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "galpon")
	buildCommand(t, "..", bin, "./cmd/galpon")
	codex := buildFakeCodex(t, root)
	claude := filepath.Join(root, "fake-claude")
	writeExecutable(t, claude, `#!/bin/sh
if [ "$1 $2" = "auth status" ]; then printf '%s\n' '{"loggedIn":true}'; exit 0; fi
cat >/dev/null
printf '%s\n' '{"type":"result","result":"fake Claude completed durable work","is_error":false}'
`)
	pi := filepath.Join(root, "fake-pi")
	writeExecutable(t, pi, "#!/bin/sh\nexit 0\n")
	stateDir := filepath.Join(root, "state")
	cfg := config.Config{StateDir: stateDir, Socket: filepath.Join(stateDir, "galpon.sock"), DefaultHarness: "pi", PiBin: pi, PiProvider: "test", CodexBin: codex, ClaudeBin: claude, HerdrBin: "herdr"}
	var logs bytes.Buffer
	application, err := app.Open(context.Background(), cfg, log.New(&logs, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	application.Executable = bin
	server := app.NewServer(application)
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(cfg.Socket) }()
	client := app.NewClient(cfg.Socket)
	waitExternalHealth(t, client)
	defer func() {
		_ = client.Shutdown(context.Background())
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
		}
		_ = application.Close()
	}()
	workspace, err := application.CreateWorkspace(context.Background(), app.CreateWorkspaceRequest{Title: "External harness E2E"})
	if err != nil {
		t.Fatal(err)
	}
	codexAgent, err := application.CreateAgent(context.Background(), app.CreateAgentRequest{Title: "Codex", Harness: "codex", WorkspaceID: workspace.ID, Presentation: "background"})
	if err != nil {
		t.Fatal(err)
	}
	claudeAgent, err := application.CreateAgent(context.Background(), app.CreateAgentRequest{Title: "Claude", Harness: "claude", WorkspaceID: workspace.ID, Presentation: "background"})
	if err != nil {
		t.Fatal(err)
	}
	piAgent, err := application.CreateAgent(context.Background(), app.CreateAgentRequest{Title: "Running Pi", Harness: "pi", WorkspaceID: workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	piCapability, err := application.PrepareRuntime(context.Background(), piAgent.ID, "pi-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if err := application.RegisterRuntime(context.Background(), piAgent.ID, "pi-runtime", piCapability, piAgent.ID, ""); err != nil {
		t.Fatal(err)
	}

	rootMessage, err := application.QueueAgentMessage(context.Background(), "", codexAgent.ID, "Use the Galpon MCP bridge to ask Running Pi for a durable reply and await it.")
	if err != nil {
		t.Fatal(err)
	}
	claimed := waitForClaim(t, client, application, codexAgent.ID, piAgent.ID, "pi-runtime", piCapability, &logs)
	if !strings.Contains(claimed.Prompt, "fake Codex MCP request") || claimed.SenderAgentID != codexAgent.ID {
		t.Fatalf("Pi received = %#v", claimed)
	}
	if err := client.CompleteMessage(context.Background(), piAgent.ID, claimed.ID, "pi-runtime", piCapability, claimed.Attempt, "Pi durable reply", ""); err != nil {
		t.Fatal(err)
	}
	waitMessageStatus(t, application, rootMessage.ID, "completed", &logs)
	completedRoot, _ := application.Store.AgentMessage(context.Background(), rootMessage.ID)
	if completedRoot.Response != "fake Codex sent and awaited Pi work" {
		t.Fatalf("Codex root response = %#v", completedRoot)
	}

	resumeMessage, err := application.QueueAgentMessage(context.Background(), "", codexAgent.ID, "Verify resumed Codex delivery")
	if err != nil {
		t.Fatal(err)
	}
	waitMessageStatus(t, application, resumeMessage.ID, "completed", &logs)
	resumed, _ := application.Store.AgentMessage(context.Background(), resumeMessage.ID)
	if resumed.Response != "fake Codex resumed" {
		t.Fatalf("Codex resumed response = %#v", resumed)
	}

	claudeMessage, err := application.QueueAgentMessage(context.Background(), "", claudeAgent.ID, "Run fake Claude work")
	if err != nil {
		t.Fatal(err)
	}
	waitMessageStatus(t, application, claudeMessage.ID, "completed", &logs)
	storedClaude, _ := application.Store.AgentMessage(context.Background(), claudeMessage.ID)
	if storedClaude.Response != "fake Claude completed durable work" {
		t.Fatalf("Claude response = %#v", storedClaude)
	}

	logPath := filepath.Join(codexAgent.Placement.CWD, "fake-codex.log")
	entries := readFakeCodexLog(t, logPath)
	if len(entries) != 2 {
		t.Fatalf("Codex invocations = %#v", entries)
	}
	firstArgs := strings.Join(entries[0].Args, " ")
	secondArgs := strings.Join(entries[1].Args, " ")
	for _, want := range []string{"mcp_servers.galpon.command=", "developer_instructions=", "exec --json", "--skip-git-repo-check"} {
		if !strings.Contains(firstArgs, want) {
			t.Errorf("fresh argv omitted %q: %s", want, firstArgs)
		}
	}
	for _, want := range []string{"exec resume --json", "11111111-1111-4111-8111-111111111111"} {
		if !strings.Contains(secondArgs, want) {
			t.Errorf("resume argv omitted %q: %s", want, secondArgs)
		}
	}
	if !entries[0].MCP || !strings.Contains(entries[0].Prompt, "galpon_delivery_data") || !strings.Contains(entries[1].Prompt, "galpon_delivery_data") {
		t.Fatalf("Codex prompt/MCP evidence = %#v", entries)
	}
}

type fakeCodexLog struct {
	Args     []string `json:"args"`
	Prompt   string   `json:"prompt"`
	MCP      bool     `json:"mcp"`
	Evidence string   `json:"evidence"`
}

func buildFakeCodex(t *testing.T, root string) string {
	t.Helper()
	source := filepath.Join(root, "fake-codex.go")
	program := `package main
import (
 "bufio"; "encoding/json"; "fmt"; "io"; "os"; "os/exec"; "strconv"; "strings"
)
type response struct { Result struct { Content []struct { Text string ` + "`json:\"text\"`" + ` } ` + "`json:\"content\"`" + ` } ` + "`json:\"result\"`" + ` }
func main() {
 if len(os.Args) >= 3 && os.Args[1] == "login" && os.Args[2] == "status" { fmt.Println("Logged in using test"); return }
 promptBytes, _ := io.ReadAll(os.Stdin); prompt := string(promptBytes)
 args := os.Args[1:]; joined := strings.Join(args, " ")
 if !strings.Contains(joined, "developer_instructions=") || !strings.Contains(joined, "mcp_servers.galpon.command=") || !strings.Contains(joined, "--json") { fmt.Fprintln(os.Stderr, "invalid Codex argv"); os.Exit(2) }
 resumed := strings.Contains(joined, "exec resume")
 mcpCommand := ""
 for _, arg := range args { if strings.HasPrefix(arg, "mcp_servers.galpon.command=") { raw := strings.TrimPrefix(arg, "mcp_servers.galpon.command="); mcpCommand, _ = strconv.Unquote(raw) } }
 usedMCP := false; evidence := ""
 if !resumed {
  cmd := exec.Command(mcpCommand, "mcp", "serve"); cmd.Env = os.Environ(); in, _ := cmd.StdinPipe(); out, _ := cmd.StdoutPipe(); cmd.Stderr = os.Stderr
  if err := cmd.Start(); err != nil { panic(err) }; enc := json.NewEncoder(in); dec := json.NewDecoder(bufio.NewReader(out))
  enc.Encode(map[string]any{"jsonrpc":"2.0","id":0,"method":"initialize"}); var init response; dec.Decode(&init)
  enc.Encode(map[string]any{"jsonrpc":"2.0","id":1,"method":"tools/call","params":map[string]any{"name":"galpon_send_agent","arguments":map[string]any{"agent":"Running Pi","prompt":"fake Codex MCP request"}}})
  var sent response; if err := dec.Decode(&sent); err != nil { panic(err) }; evidence = fmt.Sprintf("sent=%#v", sent); var message struct { ID string ` + "`json:\"id\"`" + ` }; json.Unmarshal([]byte(sent.Result.Content[0].Text), &message)
  enc.Encode(map[string]any{"jsonrpc":"2.0","id":2,"method":"tools/call","params":map[string]any{"name":"galpon_await_agent","arguments":map[string]any{"message_id":message.ID,"timeout_seconds":10}}})
  var awaited response; if err := dec.Decode(&awaited); err != nil { panic(err) }; evidence += fmt.Sprintf(" awaited=%#v", awaited); in.Close(); cmd.Wait(); usedMCP = len(awaited.Result.Content) > 0 && strings.Contains(awaited.Result.Content[0].Text, "Pi durable reply")
 }
 logFile, _ := os.OpenFile("fake-codex.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); json.NewEncoder(logFile).Encode(map[string]any{"args":args,"prompt":prompt,"mcp":usedMCP,"evidence":evidence}); logFile.Close()
 if !resumed { fmt.Println(` + "`" + `{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}` + "`" + `); fmt.Println(` + "`" + `{"type":"item.completed","item":{"type":"agent_message","text":"fake Codex sent and awaited Pi work"}}` + "`" + `) } else { fmt.Println(` + "`" + `{"type":"item.completed","item":{"type":"agent_message","text":"fake Codex resumed"}}` + "`" + `) }
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "fake-codex")
	buildCommand(t, root, bin, source)
	return bin
}

func buildCommand(t *testing.T, directory, output, packagePath string) {
	t.Helper()
	command := exec.Command("go", "build", "-o", output, packagePath)
	command.Dir = directory
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", packagePath, err, data)
	}
}

func waitForClaim(t *testing.T, client *app.Client, application *app.App, codexAgentID, agentID, runtimeID, capability string, logs *bytes.Buffer) *structClaim {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		message, err := client.ClaimMessage(context.Background(), agentID, runtimeID, capability, "pi-e2e-claim")
		if err == nil && message != nil {
			return &structClaim{ID: message.ID, SenderAgentID: message.SenderAgentID, Prompt: message.Prompt, Attempt: message.Attempt}
		}
		time.Sleep(25 * time.Millisecond)
	}
	view, _ := application.Store.AgentView(context.Background(), codexAgentID)
	evidence, _ := os.ReadFile(filepath.Join(view.Agent.Placement.CWD, "fake-codex.log"))
	t.Fatalf("Pi did not receive the fake Codex MCP request; Codex view: %#v; fake evidence: %s; logs: %s", view, evidence, logs.String())
	return nil
}

type structClaim struct {
	ID, SenderAgentID, Prompt string
	Attempt                   int
}

func readFakeCodexLog(t *testing.T, path string) []fakeCodexLog {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var out []fakeCodexLog
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var value fakeCodexLog
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		out = append(out, value)
	}
	return out
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func waitExternalHealth(t *testing.T, client *app.Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.Health(context.Background()) == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not become healthy")
}

func waitMessageStatus(t *testing.T, application *app.App, id, status string, logs *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		message, err := application.Store.AgentMessage(context.Background(), id)
		if err == nil && message.Status == status {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	message, err := application.Store.AgentMessage(context.Background(), id)
	agent, _ := application.Store.Agent(context.Background(), message.TargetAgentID)
	t.Fatalf("message %s = %#v, %v; want %s; agent: %#v; logs: %s", id, message, err, status, agent, logs.String())
}
