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

func TestFakeExternalHarnessesUseRestartSafeMCPAndDurableProtocol(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "galpon")
	buildCommand(t, "..", bin, "./cmd/galpon")
	fakeHarness := buildFakeExternalHarness(t, root)
	codex := filepath.Join(root, "fake-codex")
	claude := filepath.Join(root, "fake-claude")
	if err := os.Link(fakeHarness, codex); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(fakeHarness, claude); err != nil {
		t.Fatal(err)
	}
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

	codexRoot, err := application.QueueAgentMessage(context.Background(), "", codexAgent.ID, "Use restart-safe MCP and await Pi.")
	if err != nil {
		t.Fatal(err)
	}
	codexChild := waitForClaim(t, client, application, codexAgent.ID, piAgent.ID, "pi-runtime", piCapability, "fake Codex MCP request", &logs)
	if codexChild.SenderAgentID != codexAgent.ID {
		t.Fatalf("Pi received = %#v", codexChild)
	}
	if err := client.CompleteMessage(context.Background(), piAgent.ID, codexChild.ID, "pi-runtime", piCapability, codexChild.Attempt, "Pi durable reply to Codex", ""); err != nil {
		t.Fatal(err)
	}
	waitMessageStatus(t, application, codexRoot.ID, "completed", &logs)
	completedCodex, _ := application.Store.AgentMessage(context.Background(), codexRoot.ID)
	if completedCodex.Response != "fake Codex sent and awaited Pi work" {
		t.Fatalf("Codex root = %#v", completedCodex)
	}
	codexProgress, err := application.Store.WorkProgressEvents(context.Background(), codexRoot.ID)
	if err != nil || len(codexProgress) != 1 || codexProgress[0].Summary != "Fake Codex is coordinating work" || codexProgress[0].Attempt != completedCodex.Attempt {
		t.Fatalf("Codex progress = %#v, %v", codexProgress, err)
	}
	codexChildren := 0
	piView, _ := application.Store.AgentView(context.Background(), piAgent.ID)
	for _, message := range piView.Messages {
		if message.SenderAgentID == codexAgent.ID && message.TargetAgentID == piAgent.ID && message.Prompt == "fake Codex MCP request" {
			codexChildren++
		}
	}
	if codexChildren != 1 {
		t.Fatalf("MCP restart duplicated Codex mutation: %d messages", codexChildren)
	}

	codexResume, err := application.QueueAgentMessage(context.Background(), "", codexAgent.ID, "Resume Codex")
	if err != nil {
		t.Fatal(err)
	}
	waitMessageStatus(t, application, codexResume.ID, "completed", &logs)
	storedCodexResume, _ := application.Store.AgentMessage(context.Background(), codexResume.ID)
	if storedCodexResume.Response != "fake Codex resumed" {
		t.Fatalf("Codex resume = %#v", storedCodexResume)
	}

	claudeRoot, err := application.QueueAgentMessage(context.Background(), piAgent.ID, claudeAgent.ID, "Pi asks Claude to coordinate and reply.")
	if err != nil {
		t.Fatal(err)
	}
	claudeChild := waitForClaim(t, client, application, claudeAgent.ID, piAgent.ID, "pi-runtime", piCapability, "fake Claude MCP request", &logs)
	if claudeChild.SenderAgentID != claudeAgent.ID {
		t.Fatalf("Pi received Claude child = %#v", claudeChild)
	}
	if err := client.CompleteMessage(context.Background(), piAgent.ID, claudeChild.ID, "pi-runtime", piCapability, claudeChild.Attempt, "Pi durable reply to Claude", ""); err != nil {
		t.Fatal(err)
	}
	waitMessageStatus(t, application, claudeRoot.ID, "completed", &logs)
	completedClaude, _ := application.Store.AgentMessage(context.Background(), claudeRoot.ID)
	if completedClaude.Response != "fake Claude sent and awaited Pi work" {
		t.Fatalf("Claude root = %#v", completedClaude)
	}
	claudeProgress, err := application.Store.WorkProgressEvents(context.Background(), claudeRoot.ID)
	if err != nil || len(claudeProgress) != 1 || claudeProgress[0].Summary != "Fake Claude is coordinating work" || claudeProgress[0].Attempt != completedClaude.Attempt {
		t.Fatalf("Claude progress = %#v, %v", claudeProgress, err)
	}
	piResult := waitForClaim(t, client, application, claudeAgent.ID, piAgent.ID, "pi-runtime", piCapability, "fake Claude sent and awaited Pi work", &logs)
	if piResult.SenderAgentID != claudeAgent.ID {
		t.Fatalf("Pi-originated result = %#v", piResult)
	}
	if err := client.CompleteMessage(context.Background(), piAgent.ID, piResult.ID, "pi-runtime", piCapability, piResult.Attempt, "Pi accepted Claude result", ""); err != nil {
		t.Fatal(err)
	}

	claudeResume, err := application.QueueAgentMessage(context.Background(), "", claudeAgent.ID, "Resume Claude")
	if err != nil {
		t.Fatal(err)
	}
	waitMessageStatus(t, application, claudeResume.ID, "completed", &logs)
	storedClaudeResume, _ := application.Store.AgentMessage(context.Background(), claudeResume.ID)
	if storedClaudeResume.Response != "fake Claude resumed" {
		t.Fatalf("Claude resume = %#v", storedClaudeResume)
	}

	assertFakeHarnessLog(t, filepath.Join(codexAgent.Placement.CWD, "fake-codex.log"), "codex", "11111111-1111-4111-8111-111111111111")
	assertFakeHarnessLog(t, filepath.Join(claudeAgent.Placement.CWD, "fake-claude.log"), "claude", claudeAgent.ID)
}

type fakeHarnessLog struct {
	Args      []string `json:"args"`
	Prompt    string   `json:"prompt"`
	MCP       bool     `json:"mcp"`
	SessionID string   `json:"sessionId"`
}

func buildFakeExternalHarness(t *testing.T, root string) string {
	t.Helper()
	source := filepath.Join(root, "fake-external.go")
	program := `package main
import (
 "bufio"; "encoding/json"; "fmt"; "io"; "os"; "os/exec"; "path/filepath"; "strconv"; "strings"; "time"
)
type response struct { Result struct { Content []struct { Text string ` + "`json:\"text\"`" + ` } ` + "`json:\"content\"`" + `; IsError bool ` + "`json:\"isError\"`" + ` } ` + "`json:\"result\"`" + ` }
type mcp struct { cmd *exec.Cmd; in io.WriteCloser; enc *json.Encoder; dec *json.Decoder }
func start(command string) *mcp { cmd:=exec.Command(command,"mcp","serve"); cmd.Env=os.Environ(); in,_:=cmd.StdinPipe(); out,_:=cmd.StdoutPipe(); cmd.Stderr=os.Stderr; if err:=cmd.Start();err!=nil{panic(err)}; value:=&mcp{cmd:cmd,in:in,enc:json.NewEncoder(in),dec:json.NewDecoder(bufio.NewReader(out))}; value.enc.Encode(map[string]any{"jsonrpc":"2.0","id":0,"method":"initialize"}); var init response; if err:=value.dec.Decode(&init);err!=nil{panic(err)}; return value }
func (m *mcp) close() { m.in.Close(); m.cmd.Wait() }
func sendAndAwait(command, target, prompt, reply string, restart bool) bool {
 if restart { first:=start(command); first.enc.Encode(map[string]any{"jsonrpc":"2.0","id":1,"method":"tools/call","params":map[string]any{"name":"galpon_send_agent","arguments":map[string]any{"agent":target,"prompt":prompt}}}); time.Sleep(500*time.Millisecond); first.cmd.Process.Kill(); first.cmd.Wait() }
 current:=start(command); current.enc.Encode(map[string]any{"jsonrpc":"2.0","id":3,"method":"tools/call","params":map[string]any{"name":"galpon_report_progress","arguments":map[string]any{"version":1,"event_id":"fake-progress","phase":"working","summary":"Fake "+strings.Fields(prompt)[1]+" is coordinating work"}}}); var progress response; if err:=current.dec.Decode(&progress);err!=nil{panic(err)}
 current.enc.Encode(map[string]any{"jsonrpc":"2.0","id":1,"method":"tools/call","params":map[string]any{"name":"galpon_send_agent","arguments":map[string]any{"agent":target,"prompt":prompt}}}); var sent response; if err:=current.dec.Decode(&sent);err!=nil{panic(err)}; var message struct{ID string ` + "`json:\"id\"`" + `}; json.Unmarshal([]byte(sent.Result.Content[0].Text),&message)
 current.enc.Encode(map[string]any{"jsonrpc":"2.0","id":2,"method":"tools/call","params":map[string]any{"name":"galpon_await_agent","arguments":map[string]any{"message_id":message.ID,"timeout_seconds":10}}}); var awaited response; if err:=current.dec.Decode(&awaited);err!=nil{panic(err)}; current.close(); return !progress.Result.IsError && len(awaited.Result.Content)>0 && strings.Contains(awaited.Result.Content[0].Text,reply)
}
func main(){
 kind:="codex"; if strings.Contains(filepath.Base(os.Args[0]),"claude"){kind="claude"}
 if kind=="codex" && len(os.Args)>=3 && os.Args[1]=="login" && os.Args[2]=="status"{fmt.Println("Logged in using test");return}; if kind=="claude" && len(os.Args)>=3 && os.Args[1]=="auth" && os.Args[2]=="status"{fmt.Println(` + "`" + `{"loggedIn":true}` + "`" + `);return}
 promptBytes,_:=io.ReadAll(os.Stdin); promptText:=string(promptBytes); args:=os.Args[1:]; joined:=strings.Join(args," "); resumed:=strings.Contains(joined,"exec resume")||strings.Contains(joined,"--resume")
 command:=""; if kind=="codex"{for _,arg:=range args{if strings.HasPrefix(arg,"mcp_servers.galpon.command="){command,_=strconv.Unquote(strings.TrimPrefix(arg,"mcp_servers.galpon.command="))}}}else{index:=strings.Index(joined,` + "`" + `"command":` + "`" + `); if index>=0{part:=joined[index+10:]; json.Unmarshal([]byte(part[:strings.Index(part,",")]),&command)}}
 session:="11111111-1111-4111-8111-111111111111"; if kind=="claude"{session=os.Getenv("GALPON_AGENT_ID")}; used:=false
 if !resumed{used=sendAndAwait(command,"Running Pi","fake "+strings.ToUpper(kind[:1])+kind[1:]+" MCP request","Pi durable reply to "+strings.ToUpper(kind[:1])+kind[1:],kind=="codex")}
 file,_:=os.OpenFile("fake-"+kind+".log",os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); json.NewEncoder(file).Encode(map[string]any{"args":args,"prompt":promptText,"mcp":used,"sessionId":session}); file.Close()
 if kind=="codex"{data,_:=json.Marshal(map[string]any{"type":"thread.started","thread_id":session});fmt.Println(string(data)); text:="fake Codex sent and awaited Pi work";if resumed{text="fake Codex resumed"}; data,_=json.Marshal(map[string]any{"type":"item.completed","item":map[string]any{"type":"agent_message","text":text}});fmt.Println(string(data))}else{data,_:=json.Marshal(map[string]any{"type":"system","session_id":session});fmt.Println(string(data));text:="fake Claude sent and awaited Pi work";if resumed{text="fake Claude resumed"};data,_=json.Marshal(map[string]any{"type":"result","session_id":session,"result":text,"is_error":false});fmt.Println(string(data))}
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "fake-external")
	buildCommand(t, root, binary, source)
	return binary
}

func assertFakeHarnessLog(t *testing.T, path, kind, sessionID string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var entries []fakeHarnessLog
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var value fakeHarnessLog
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, value)
	}
	if len(entries) != 2 {
		t.Fatalf("%s invocations = %#v", kind, entries)
	}
	if !entries[0].MCP || entries[1].MCP || entries[0].SessionID != sessionID || entries[1].SessionID != sessionID {
		t.Fatalf("%s MCP/session evidence = %#v", kind, entries)
	}
	if !strings.Contains(entries[0].Prompt, "galpon_delivery_data") || !strings.Contains(entries[1].Prompt, "galpon_delivery_data") {
		t.Fatalf("%s prompts = %#v", kind, entries)
	}
	fresh, resume := strings.Join(entries[0].Args, " "), strings.Join(entries[1].Args, " ")
	if kind == "codex" {
		for _, want := range []string{"developer_instructions=", "mcp_servers.galpon.command=", "exec --json"} {
			if !strings.Contains(fresh, want) {
				t.Errorf("Codex fresh omitted %q: %s", want, fresh)
			}
		}
		for _, want := range []string{"exec resume --json", sessionID} {
			if !strings.Contains(resume, want) {
				t.Errorf("Codex resume omitted %q: %s", want, resume)
			}
		}
	} else {
		for _, want := range []string{"--append-system-prompt", "--session-id " + sessionID} {
			if !strings.Contains(fresh, want) {
				t.Errorf("Claude fresh omitted %q: %s", want, fresh)
			}
		}
		if !strings.Contains(resume, "--resume "+sessionID) {
			t.Errorf("Claude resume omitted session: %s", resume)
		}
	}
}

func buildCommand(t *testing.T, directory, output, packagePath string) {
	t.Helper()
	command := exec.Command("go", "build", "-o", output, packagePath)
	command.Dir = directory
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", packagePath, err, data)
	}
}

func waitForClaim(t *testing.T, client *app.Client, application *app.App, sourceAgentID, agentID, runtimeID, capability, prompt string, logs *bytes.Buffer) *structClaim {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		message, err := client.ClaimMessage(context.Background(), agentID, runtimeID, capability, "pi-e2e-"+prompt)
		if err == nil && message != nil {
			if strings.Contains(message.Prompt, prompt) {
				return &structClaim{ID: message.ID, SenderAgentID: message.SenderAgentID, Prompt: message.Prompt, Attempt: message.Attempt}
			}
			t.Fatalf("unexpected Pi delivery while waiting for %q: %#v", prompt, message)
		}
		time.Sleep(25 * time.Millisecond)
	}
	view, _ := application.Store.AgentView(context.Background(), sourceAgentID)
	t.Fatalf("Pi did not receive %q; source: %#v; logs: %s", prompt, view, logs.String())
	return nil
}

type structClaim struct {
	ID, SenderAgentID, Prompt string
	Attempt                   int
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
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		message, err := application.Store.AgentMessage(context.Background(), id)
		if err == nil && message.Status == status {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	message, err := application.Store.AgentMessage(context.Background(), id)
	t.Fatalf("message %s = %#v, %v; want %s; logs: %s", id, message, err, status, logs.String())
}
