package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/apphost"
	"github.com/matipan/galpon/internal/model"
)

// A Pi agent in the app view runs in its app host: the agent tab of the
// Herdr app sends it prompts over HTTP, reads its events, and herdr-core
// gets its state. Galpon messages reach the same Pi.
func TestAppHostRunsAPiAgentForTheAgentTab(t *testing.T) {
	piBin, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi is not installed")
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = r.Body.Close() }()
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		prompt, _ := responseInput(request)
		if strings.Contains(prompt, "Message from Galpon") {
			writeTextResponse(w, "Reply to the Galpon message")
			return
		}
		writeTextResponse(w, "Reply from the app view")
	}))
	defer mock.Close()

	root := t.TempDir()
	piHome := filepath.Join(root, "pi")
	writePiConfig(t, piHome, mock.URL)
	stateDir := filepath.Join(root, "state")
	appHome := filepath.Join(root, "herdr-app")
	env := []string{}
	for _, value := range os.Environ() {
		// The host must not report to the terminal of this test.
		if strings.HasPrefix(value, "HC_") || strings.HasPrefix(value, "HERDR_") || strings.HasPrefix(value, "GALPON_") {
			continue
		}
		env = append(env, value)
	}
	env = append(env,
		"XDG_CONFIG_HOME="+testConfigDir(t),
		"GALPON_STATE_DIR="+stateDir,
		"GALPON_PI_BIN="+piBin,
		"GALPON_PI_PROVIDER=galpon-mock",
		"GALPON_PI_MODEL=mock-model",
		"GALPON_HERDR_BIN=herdr-not-used",
		"HERDR_APP_HOME="+appHome,
		"PI_CODING_AGENT_DIR="+piHome,
		"PI_OFFLINE=1",
		"GALPON_TEST_SKIP_PI_PACKAGE_SETUP=1",
		"NO_COLOR=",
	)
	bin := filepath.Join(root, "galpon")
	runRaw(t, "..", nil, "go", "build", "-o", bin, "./cmd/galpon")
	defer func() { _ = runCommand("", env, bin, "daemon", "stop") }()

	var workspace model.Workspace
	decodeCommand(t, &workspace, runRaw(t, "", env, bin, "workspace", "create", "Tabs"))
	agent, err := app.NewClient(filepath.Join(stateDir, "galpon.sock")).CreateAgent(t.Context(), app.CreateAgentRequest{
		Title: "Tab agent", WorkspaceID: workspace.ID, Harness: model.HarnessPiApp,
		Placement: app.AgentPlacementRequest{Type: "none", CWD: root},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !agent.IsApp() {
		t.Fatalf("pi-app agent = %#v", agent)
	}

	host := startAppHost(t, bin, env, stateDir, agent.ID)
	waitFor(t, 60*time.Second, "Pi in the host", host.alive)

	if code, out := host.call(http.MethodPost, "/rpc", map[string]any{"type": "prompt", "message": "Hello from the tab"}); code >= 300 {
		t.Fatalf("prompt: %d %v", code, out)
	}
	waitFor(t, 60*time.Second, "the reply in the events", func() bool {
		return strings.Contains(strings.Join(host.replies(), "\n"), "Reply from the app view")
	})
	if bridge := host.bridge(); bridge["agentId"] != agent.ID {
		t.Fatalf("the bridge did not name the agent: %v", bridge)
	}
	host.checkReports()

	// A Galpon message reaches the Pi of the host, and its reply goes back.
	message := sendMessage(t, bin, env, agent.ID, "Message from Galpon")
	waitForMessage(t, bin, env, agent.ID, message.ID, "Reply to the Galpon message")
	waitFor(t, 30*time.Second, "the delivered turn in the events", func() bool {
		return strings.Contains(strings.Join(host.replies(), "\n"), "Reply to the Galpon message")
	})
	host.shutdown()
}

// A Claude Code agent in the app view runs in its app host as a Pi agent
// does: the tab speaks the same protocol to it, the Galpon tools work in its
// runs, Galpon messages reach the same conversation, and a reload of the tab
// reads the saved session.
func TestAppHostRunsAClaudeAgentForTheAgentTab(t *testing.T) {
	mock := httptest.NewServer(&nativeModel{})
	t.Cleanup(mock.Close)
	instance := newNativeInstance(t, nativeBinaries(t), mock.URL, "", false)
	agent, err := instance.client.CreateAgent(t.Context(), app.CreateAgentRequest{
		Title: "Claude tab", WorkspaceID: instance.workspace.ID, Harness: model.HarnessClaudeApp,
		Placement: app.AgentPlacementRequest{Type: "none", CWD: instance.root},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !agent.IsApp() || agent.Harness() != model.HarnessClaude {
		t.Fatalf("claude-app agent = %#v", agent)
	}
	env := append(append([]string(nil), instance.env...), "HERDR_APP_HOME="+filepath.Join(instance.root, "herdr-app"))
	host := startAppHost(t, instance.binary, env, instance.state, agent.ID)
	waitFor(t, 60*time.Second, "Claude Code in the host", host.alive)
	_, state := host.call(http.MethodPost, "/rpc", map[string]any{"type": "get_state"})
	if data, _ := state["data"].(map[string]any); data["harness"] != model.HarnessClaude {
		t.Fatalf("the state does not name Claude Code: %v", state)
	}

	ask := func(prompt, want string) {
		t.Helper()
		if code, out := host.call(http.MethodPost, "/rpc", map[string]any{"type": "prompt", "message": prompt}); code >= 300 {
			t.Fatalf("prompt %q: %d %v", prompt, code, out)
		}
		waitFor(t, 60*time.Second, want+" in the events", func() bool { return strings.Contains(strings.Join(host.replies(), "\n"), want) })
	}
	ask("NATIVE_REMEMBER kiwi", "remembered:kiwi")
	// A Galpon tool works in a run that the tab started.
	ask("NATIVE_LIST tab", "listed:tab")
	ran := false
	for _, event := range host.events() {
		ran = ran || event["type"] == "tool_execution_end" && event["toolName"] == "galpon_list_agents" && event["isError"] != true
	}
	if !ran {
		t.Fatal("the events have no galpon_list_agents step")
	}
	host.checkReports()

	// A Galpon message reaches the same conversation, as a delivery.
	message := instance.send(t, agent.ID, "NATIVE_RECALL kiwi")
	waitNativeMessage(t, instance.client, agent.ID, message.ID, "recalled:kiwi")
	waitFor(t, 30*time.Second, "the delivered turn in the events", func() bool {
		return strings.Contains(strings.Join(host.replies(), "\n"), "recalled:kiwi")
	})

	// A reload of the tab reads the saved session as Pi messages.
	_, snapshot := host.call(http.MethodGet, "/snapshot", nil)
	var shown []string
	for _, raw := range nativeArray(snapshot["messages"]) {
		message := nativeObject(raw)
		switch message["role"] {
		case "user", "assistant":
			shown = append(shown, nativeString(message["role"])+": "+nativeText(message["content"]))
		case "toolResult":
			shown = append(shown, "tool: "+nativeString(message["toolName"]))
		case "custom":
			shown = append(shown, nativeString(message["customType"])+": "+nativeText(message["content"]))
		}
	}
	all := strings.Join(shown, "\n")
	for _, want := range []string{"user: NATIVE_REMEMBER kiwi", "assistant: remembered:kiwi", "tool: galpon_list_agents", "assistant: listed:tab", "galpon-operation: Work request", "NATIVE_RECALL kiwi", "assistant: recalled:kiwi"} {
		if !strings.Contains(all, want) {
			t.Fatalf("the snapshot has no %q:\n%s", want, all)
		}
	}
	host.shutdown()
}

// appHost is an app host that a test runs, with the requests of the tab.
type appHost struct {
	t      *testing.T
	dir    string
	info   apphost.Info
	status *lockedBuffer
	done   chan error
	exited bool
}

func startAppHost(t *testing.T, bin string, env []string, stateDir, agentID string) *appHost {
	t.Helper()
	h := &appHost{t: t, dir: apphost.Dir(stateDir, agentID), status: &lockedBuffer{}, done: make(chan error, 1)}
	command := exec.Command(bin, "herdr-app", "host", agentID)
	command.Env = env
	command.Stdout = h.status
	command.Stderr = h.status
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { h.done <- command.Wait() }()
	t.Cleanup(func() {
		if !h.exited {
			_ = command.Process.Kill()
			if err := <-h.done; err != nil {
				t.Logf("the killed host ended with %v", err)
			}
		}
	})
	waitFor(t, 30*time.Second, "the host file", func() bool {
		var ok bool
		h.info, ok = apphost.ReadInfo(h.dir)
		return ok
	})
	return h
}

func (h *appHost) call(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	request, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", h.info.Port, path), reader)
	request.Header.Set(apphost.TokenHeader, h.info.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(response.Body).Decode(&out)
	return response.StatusCode, out
}

func (h *appHost) alive() bool {
	_, state := h.call(http.MethodGet, "/state", nil)
	return state["alive"] == true
}

func (h *appHost) events() []map[string]any {
	_, out := h.call(http.MethodGet, "/events?since=0", nil)
	var events []map[string]any
	for _, item := range nativeArray(out["events"]) {
		events = append(events, nativeObject(item))
	}
	return events
}

// replies gives the texts of the assistant messages so far.
func (h *appHost) replies() []string {
	var replies []string
	for _, event := range h.events() {
		message := nativeObject(event["message"])
		if event["type"] != "message_end" || message["role"] != "assistant" {
			continue
		}
		for _, block := range nativeArray(message["content"]) {
			if text := nativeString(nativeObject(block)["text"]); text != "" {
				replies = append(replies, text)
			}
		}
	}
	return replies
}

// bridge gives the Galpon facts of the agent that the host published.
func (h *appHost) bridge() map[string]any {
	var bridge map[string]any
	for _, event := range h.events() {
		if event["type"] == "host_bridge" && event["key"] == "galpon" {
			bridge = nativeObject(event["value"])
		}
	}
	return bridge
}

// checkReports checks that herdr-core gets the state of the agent from the
// host terminal: working during a run, done after it, and idle when the
// tab saw it.
func (h *appHost) checkReports() {
	h.t.Helper()
	waitFor(h.t, 10*time.Second, "the done report", func() bool { return strings.Contains(h.status.String(), "state=done") })
	h.call(http.MethodPost, "/seen", nil)
	waitFor(h.t, 10*time.Second, "the idle report", func() bool {
		reports := h.status.String()
		return strings.LastIndex(reports, "state=idle") > strings.LastIndex(reports, "state=done")
	})
	if !strings.Contains(h.status.String(), "state=working") {
		h.t.Fatalf("the host did not report the run: %q", h.status.String())
	}
}

func (h *appHost) shutdown() {
	h.t.Helper()
	if code, _ := h.call(http.MethodPost, "/shutdown", nil); code != http.StatusOK {
		h.t.Fatalf("shutdown: %d", code)
	}
	select {
	case err := <-h.done:
		h.exited = true
		if err != nil {
			h.t.Fatalf("the host ended with %v", err)
		}
	case <-time.After(20 * time.Second):
		h.t.Fatal("the host did not stop")
	}
	if _, ok := apphost.ReadInfo(h.dir); ok {
		h.t.Fatal("the host file stayed after the host stopped")
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
