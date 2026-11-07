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

	status := &lockedBuffer{}
	host := exec.Command(bin, "herdr-app", "host", agent.ID)
	host.Env = env
	host.Stdout = status
	host.Stderr = status
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Wait() }()
	exited := false
	defer func() {
		if !exited {
			_ = host.Process.Kill()
			if err := <-done; err != nil {
				t.Logf("the killed host ended with %v", err)
			}
		}
	}()

	dir := apphost.Dir(stateDir, agent.ID)
	var info apphost.Info
	waitFor(t, 30*time.Second, "the host file", func() bool {
		var ok bool
		info, ok = apphost.ReadInfo(dir)
		return ok
	})
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			reader = bytes.NewReader(data)
		}
		request, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", info.Port, path), reader)
		request.Header.Set(apphost.TokenHeader, info.Token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(response.Body).Decode(&out)
		return response.StatusCode, out
	}
	waitFor(t, 60*time.Second, "Pi in the host", func() bool {
		_, state := call(http.MethodGet, "/state", nil)
		return state["alive"] == true
	})

	if code, out := call(http.MethodPost, "/rpc", map[string]any{"type": "prompt", "message": "Hello from the tab"}); code >= 300 {
		t.Fatalf("prompt: %d %v", code, out)
	}
	// events gives the texts of the assistant messages and the bridge facts
	// of the agent so far.
	events := func() (replies []string, bridge map[string]any) {
		_, out := call(http.MethodGet, "/events?since=0", nil)
		list, _ := out["events"].([]any)
		for _, item := range list {
			event, _ := item.(map[string]any)
			if event["type"] == "host_bridge" && event["key"] == "galpon" {
				bridge, _ = event["value"].(map[string]any)
			}
			message, _ := event["message"].(map[string]any)
			if event["type"] != "message_end" || message["role"] != "assistant" {
				continue
			}
			content, _ := message["content"].([]any)
			for _, block := range content {
				part, _ := block.(map[string]any)
				if text, _ := part["text"].(string); text != "" {
					replies = append(replies, text)
				}
			}
		}
		return replies, bridge
	}
	waitFor(t, 60*time.Second, "the reply in the events", func() bool {
		replies, _ := events()
		return strings.Contains(strings.Join(replies, "\n"), "Reply from the app view")
	})
	if _, bridge := events(); bridge["agentId"] != agent.ID {
		t.Fatalf("the bridge did not name the agent: %v", bridge)
	}
	// herdr-core gets the state of the agent from the host terminal.
	waitFor(t, 10*time.Second, "the done report", func() bool { return strings.Contains(status.String(), "state=done") })
	call(http.MethodPost, "/seen", nil)
	waitFor(t, 10*time.Second, "the idle report", func() bool {
		reports := status.String()
		return strings.LastIndex(reports, "state=idle") > strings.LastIndex(reports, "state=done")
	})
	if !strings.Contains(status.String(), "state=working") {
		t.Fatalf("the host did not report the run: %q", status.String())
	}

	// A Galpon message reaches the Pi of the host, and its reply goes back.
	message := sendMessage(t, bin, env, agent.ID, "Message from Galpon")
	waitForMessage(t, bin, env, agent.ID, message.ID, "Reply to the Galpon message")
	waitFor(t, 30*time.Second, "the delivered turn in the events", func() bool {
		replies, _ := events()
		return strings.Contains(strings.Join(replies, "\n"), "Reply to the Galpon message")
	})

	if code, _ := call(http.MethodPost, "/shutdown", nil); code != http.StatusOK {
		t.Fatalf("shutdown: %d", code)
	}
	select {
	case err := <-done:
		exited = true
		if err != nil {
			t.Fatalf("the host ended with %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the host did not stop")
	}
	if _, ok := apphost.ReadInfo(dir); ok {
		t.Fatal("the host file stayed after the host stopped")
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
