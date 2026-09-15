package piagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/neovimreview"
	"golang.org/x/sys/unix"
)

var nativeTerminalQueries = regexp.MustCompile(`\x1b\](10|11);\?(?:\x07|\x1b\\)|\x1b\[[56]n|\x1b\[c`)
var nativeRuntimeErrors = regexp.MustCompile(`vim\.schedule callback:|Error (executing|detected)|E[0-9]{2,4}:`)

type nativeTerminalOutput struct {
	sync.Mutex
	buffer   bytes.Buffer // Do not promote ReadFrom: io.Copy must use the locked query responder.
	terminal *os.File
	pending  string
}

func (b *nativeTerminalOutput) Write(data []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	b.pending += string(data)
	for {
		location := nativeTerminalQueries.FindStringIndex(b.pending)
		if location == nil {
			break
		}
		query := b.pending[location[0]:location[1]]
		b.pending = b.pending[location[1]:]
		var reply string
		switch {
		case strings.Contains(query, "]10;"):
			reply = "\x1b]10;rgb:c8c8/d3d3/f5f5\x1b\\"
		case strings.Contains(query, "]11;"):
			reply = "\x1b]11;rgb:2222/2424/3636\x1b\\"
		case query == "\x1b[5n":
			reply = "\x1b[0n" // Neovim 0.12 waits for this status report after its color query.
		case query == "\x1b[6n":
			reply = "\x1b[1;1R"
		default:
			reply = "\x1b[?1;2c"
		}
		_, _ = io.WriteString(b.terminal, reply)
	}
	if len(b.pending) > 256 {
		b.pending = b.pending[len(b.pending)-256:]
	}
	return b.buffer.Write(data)
}

func (b *nativeTerminalOutput) position() int {
	b.Lock()
	defer b.Unlock()
	return b.buffer.Len()
}

func (b *nativeTerminalOutput) nativeFrameAfter(offset int) bool {
	b.Lock()
	defer b.Unlock()
	return bytes.Contains(b.buffer.Bytes()[offset:], []byte(" GALPON REVIEW "))
}

func (b *nativeTerminalOutput) tail() string {
	b.Lock()
	defer b.Unlock()
	data := b.buffer.Bytes()
	if len(data) > 8000 {
		data = data[len(data)-8000:]
	}
	return string(data)
}

func TestNeovimReviewTerminalQueries(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	output := &nativeTerminalOutput{terminal: writer}
	query := "frame\x1b]11;?\a\x1b[5n\x1b[6n\x1b[c"
	// A Reader without WriteTo catches an inherited ReadFrom bypassing Write.
	if _, err := io.Copy(output, io.LimitReader(strings.NewReader(query), int64(len(query)))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	want := "\x1b]11;rgb:2222/2424/3636\x1b\\\x1b[0n\x1b[1;1R\x1b[?1;2c"
	if string(reply) != want || output.tail() != query {
		t.Fatalf("terminal queries were not handled: reply %q; output %q", reply, output.tail())
	}
}

func nativePTY(t *testing.T, command *exec.Cmd) (*os.File, *nativeTerminalOutput) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "native-review-pty")
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: 36, Col: 120}); err != nil {
		_ = slave.Close()
		_ = master.Close()
		t.Fatal(err)
	}
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		_ = slave.Close()
		_ = master.Close()
		t.Fatal(err)
	}
	_ = slave.Close()
	output := &nativeTerminalOutput{terminal: master}
	readDone := make(chan struct{})
	go func() { _, _ = io.Copy(output, master); close(readDone) }()
	waitDone := make(chan struct{})
	go func() { _ = command.Wait(); close(waitDone) }()
	t.Cleanup(func() {
		// This is a new, private process group that contains only test children.
		_ = unix.Kill(-command.Process.Pid, unix.SIGKILL)
		_ = master.Close()
		select {
		case <-waitDone:
		case <-time.After(3 * time.Second):
			t.Error("isolated Pi test did not stop")
		}
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Error("isolated terminal reader did not stop")
		}
		output.Lock()
		if location := nativeRuntimeErrors.FindIndex(output.buffer.Bytes()); location != nil {
			t.Errorf("native runtime reported an error: %q", output.buffer.Bytes()[location[0]:min(location[0]+600, output.buffer.Len())])
		}
		output.Unlock()
	})
	return master, output
}

func nativeWait(t *testing.T, output *nativeTerminalOutput, label string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s\n%s", label, output.tail())
}

func nativeJSON(path string, value any) bool {
	data, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(data, value) == nil
}

func TestNeovimReviewTerminal(t *testing.T) {
	for _, name := range []string{"pi", "nvim", "cc"} {
		if _, err := exec.LookPath(name); err != nil {
			if os.Getenv("GALPON_REQUIRE_NVIM_TESTS") == "1" {
				t.Fatalf("required native Review dependency %s is missing", name)
			}
			t.Skipf("%s is not installed", name)
		}
	}
	pi, _ := exec.LookPath("pi")
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(binDir, "galpon"), "./cmd/galpon")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build isolated Galpon: %v\n%s", err, output)
	}
	setup := exec.CommandContext(ctx, filepath.Join(binDir, "galpon"), "review", "setup")
	setup.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "setup-home"), "LANG=C.UTF-8", "GALPON_STATE_DIR=" + stateDir}
	if output, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("isolated CLI setup failed: %v\n%s", err, output)
	}
	for _, name := range []string{"galpon.db", "galpon.sock", "runtime/pi"} {
		if _, err := os.Stat(filepath.Join(stateDir, name)); !os.IsNotExist(err) {
			t.Fatalf("review setup touched unrelated state %s: %v", name, err)
		}
	}
	info, err := neovimreview.Inspect(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := Materialize(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("native-keys", func(t *testing.T) {
		private := t.TempDir()
		script, err := filepath.Abs(filepath.Join("testdata", "neovim-review-test.lua"))
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, info.Neovim, "--clean", "--headless", "-u", "NONE", "-i", "NONE", "-l", script)
		command.Env = []string{
			"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TMPDIR=" + private,
			"HOME=" + filepath.Join(private, "home"), "XDG_CONFIG_HOME=" + filepath.Join(private, "config"),
			"XDG_DATA_HOME=" + filepath.Join(private, "data"), "XDG_STATE_HOME=" + filepath.Join(private, "state"),
			"XDG_CACHE_HOME=" + filepath.Join(private, "cache"), "GALPON_REVIEW_TEST_RUNTIME=" + info.Runtime,
		}
		if output, err := command.CombinedOutput(); err != nil || !bytes.Contains(output, []byte("neovim-review-test: ok")) || nativeRuntimeErrors.Match(output) {
			t.Fatalf("native key tests failed: %v\n%s", err, output)
		}
	})
	fixture, err := filepath.Abs(filepath.Join("testdata", "neovim-terminal-test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	var modelRequests atomic.Int64
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		modelRequests.Add(1)
		http.Error(w, "Review must not call the model", http.StatusBadRequest)
	}))
	defer mock.Close()

	for _, scenario := range []string{"prepare", "keep-unsent", "keep-whitespace", "recover-edit", "crash-flush", "exit-edit", "resize", "startup"} {
		t.Run(scenario, func(t *testing.T) {
			private := t.TempDir()
			// Share only this test's private Pi tool cache. Each process still
			// starts a new session in its own working directory and HOME.
			agentDir := filepath.Join(root, "pi-agent")
			if err := os.MkdirAll(agentDir, 0o700); err != nil {
				t.Fatal(err)
			}
			models, _ := json.Marshal(map[string]any{"providers": map[string]any{"native-mock": map[string]any{
				"baseUrl": mock.URL + "/v1", "api": "openai-responses", "apiKey": "test", "authHeader": true,
				"models": []any{map[string]any{"id": "mock-model", "name": "Native mock", "reasoning": false, "input": []string{"text"}, "contextWindow": 128000, "maxTokens": 4096,
					"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}},
			}}})
			if err := os.WriteFile(filepath.Join(agentDir, "models.json"), models, 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(pi, "--provider", "native-mock", "--model", "mock-model", "--no-extensions", "--extension", fixture)
			command.Dir = private
			command.Env = []string{
				"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
				"HOME=" + filepath.Join(private, "home"), "XDG_CONFIG_HOME=" + filepath.Join(private, "config"),
				"XDG_DATA_HOME=" + filepath.Join(private, "data"), "XDG_CACHE_HOME=" + filepath.Join(private, "cache"),
				"XDG_STATE_HOME=" + filepath.Join(private, "xdg-state"), "PI_CODING_AGENT_DIR=" + agentDir,
				"TERM=xterm-256color", "COLORTERM=truecolor", "LANG=C.UTF-8", "PI_TELEMETRY=0",
				"GALPON_STATE_DIR=" + stateDir, "GALPON_PI_EXTENSION=" + assets.Extension,
				"GALPON_NATIVE_TERMINAL_TEST_DIR=" + private,
			}
			if scenario == "keep-unsent" || scenario == "keep-whitespace" {
				value := "text"
				if scenario == "keep-whitespace" {
					value = "whitespace"
				}
				command.Env = append(command.Env, "GALPON_NATIVE_TERMINAL_UNSENT="+value)
			}
			terminal, output := nativePTY(t, command)
			write := func(keys string) {
				t.Helper()
				if _, err := io.WriteString(terminal, keys); err != nil {
					t.Fatal(err)
				}
			}
			nativeWait(t, output, "Pi startup", func() bool { var value any; return nativeJSON(filepath.Join(private, "ready.json"), &value) })
			type entry struct {
				Type       string `json:"type"`
				CustomType string `json:"customType"`
				Data       struct {
					RunID     string `json:"runId"`
					SessionID string `json:"sessionId"`
					Status    string `json:"status"`
					PID       int    `json:"pid"`
					Editing   *struct {
						Buffer string `json:"buffer"`
					} `json:"editing"`
				} `json:"data"`
			}
			type nativeState struct {
				Status  string           `json:"status"`
				Items   []map[string]any `json:"items"`
				Editing *struct {
					Buffer string `json:"buffer"`
				} `json:"editing"`
			}
			lastRun := ""
			startReview := func() (string, int, time.Duration) {
				t.Helper()
				started := time.Now()
				before := output.position()
				write("/native-trial\r")
				var snapshotPath string
				var pid int
				nativeWait(t, output, "native Review initial snapshot", func() bool {
					var trace struct{ Entries []entry }
					if !nativeJSON(filepath.Join(private, "trace.json"), &trace) {
						return false
					}
					for index := len(trace.Entries) - 1; index >= 0; index-- {
						item := trace.Entries[index]
						if item.CustomType != "galpon:review:nvim:v1" || item.Data.Status != "open" || item.Data.PID == 0 || item.Data.RunID == lastRun {
							continue
						}
						snapshotPath = filepath.Join(filepath.Dir(assets.Extension), "review-runs", fmt.Sprintf("%x", sha256.Sum256([]byte(item.Data.SessionID))), item.Data.RunID, "snapshot.json")
						var value nativeState
						if !nativeJSON(snapshotPath, &value) {
							return false
						}
						lastRun, pid = item.Data.RunID, item.Data.PID
						return true
					}
					return false
				})
				nativeWait(t, output, "native Review first frame", func() bool { return output.nativeFrameAfter(before) })
				return snapshotPath, pid, time.Since(started)
			}
			readResult := func(trial int) struct {
				Sent   int    `json:"sent"`
				Editor string `json:"editor"`
			} {
				t.Helper()
				var result struct {
					Sent   int    `json:"sent"`
					Editor string `json:"editor"`
				}
				nativeWait(t, output, "return to Pi", func() bool { return nativeJSON(filepath.Join(private, fmt.Sprintf("trial-%d.json", trial)), &result) })
				if result.Sent != 0 {
					t.Fatal("Review sent a prompt or message")
				}
				return result
			}
			if scenario == "startup" {
				var timings []time.Duration
				for trial := 1; trial <= 5; trial++ {
					_, _, duration := startReview()
					timings = append(timings, duration)
					write("q")
					readResult(trial)
				}
				sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
				t.Logf("Neovim %s; five /review dispatch-to-first-native-frame PTY samples: median %s, range %s–%s (includes config inspection; not a real terminal-emulator paint benchmark)", info.Version, timings[2], timings[0], timings[4])
				write("\x11")
				return
			}
			path, pid, _ := startReview()
			if strings.Contains(output.tail(), "BACKGROUND-RENDER-MARKER:1") {
				probe, _ := os.ReadFile(filepath.Join(private, "tui-api.json"))
				t.Fatalf("Pi rendered while Neovim owned the terminal\n%s\n%s", probe, output.tail())
			}
			if scenario == "resize" {
				write("Vj")
				if err := unix.IoctlSetWinsize(int(terminal.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(100 * time.Millisecond)
				write("c")
			} else {
				write("Vc")
			}
			nativeWait(t, output, "native comment editor", func() bool { var value nativeState; return nativeJSON(path, &value) && value.Editing != nil })
			comment := "Keep this heading."
			if scenario == "prepare" {
				comment += "\nKeep this second line too."
			}
			write(strings.ReplaceAll(comment, "\n", "\r"))
			if scenario == "prepare" {
				nativeWait(t, output, "Insert Enter kept the multiline comment open", func() bool {
					var value nativeState
					return nativeJSON(path, &value) && value.Editing != nil && value.Editing.Buffer == comment && len(value.Items) == 0
				})
			}
			if scenario == "resize" {
				write("\x1b")
				if err := unix.IoctlSetWinsize(int(terminal.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 6, Col: 20}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(100 * time.Millisecond)
				write(":lua local s=GalponReview._state; vim.fn.writefile({vim.json.encode({lines=vim.o.lines,columns=vim.o.columns,uis=vim.api.nvim_list_uis(),windows=#vim.api.nvim_tabpage_list_wins(0),editing=vim.api.nvim_get_current_buf()==s.comment.buffer})},vim.env.GALPON_REVIEW_NVIM_OUTPUT..'.layout')\r")
				var layout struct {
					Lines, Columns, Windows int
					Editing                 bool
				}
				nativeWait(t, output, "real tiny terminal layout", func() bool { return nativeJSON(path+".layout", &layout) })
				if layout.Lines != 6 || layout.Columns != 20 || layout.Windows != 1 || !layout.Editing {
					data, _ := os.ReadFile(path + ".layout")
					t.Fatalf("tiny native layout lost the comment editor: %s", data)
				}
			}
			if scenario == "crash-flush" {
				// Confirm the core accepted the text, then kill the TUI before the
				// debounce timer can save it. The core's exit hook must flush it.
				write("\x1b:lua vim.fn.writefile({'ready'},vim.env.GALPON_REVIEW_NVIM_OUTPUT..'.typed')\r")
				nativeWait(t, output, "accepted native edit", func() bool { _, err := os.Stat(path + ".typed"); return err == nil })
			}
			if scenario == "exit-edit" {
				write("\x1b:qa!\r")
				if result := readResult(1); result.Editor != "" {
					t.Fatalf("ordinary native exit changed the Pi editor: %q", result.Editor)
				}
			}
			if scenario == "recover-edit" || scenario == "exit-edit" {
				nativeWait(t, output, "mirrored unfinished comment", func() bool {
					var trace struct{ Entries []entry }
					if !nativeJSON(filepath.Join(private, "trace.json"), &trace) {
						return false
					}
					for index := len(trace.Entries) - 1; index >= 0; index-- {
						item := trace.Entries[index]
						if item.CustomType == "galpon:review:draft:v1" && item.Data.Editing != nil && item.Data.Editing.Buffer == comment {
							return true
						}
					}
					return false
				})
			}
			if scenario == "recover-edit" || scenario == "crash-flush" {
				if err := unix.Kill(pid, unix.SIGKILL); err != nil {
					t.Fatal(err)
				}
				if result := readResult(1); result.Editor != "" {
					t.Fatalf("interrupted review changed the Pi editor: %q", result.Editor)
				}
			}
			if scenario == "recover-edit" || scenario == "crash-flush" || scenario == "exit-edit" {
				path, _, _ = startReview()
				nativeWait(t, output, "restored unfinished native editor", func() bool {
					var value nativeState
					return nativeJSON(path, &value) && value.Editing != nil && value.Editing.Buffer == comment
				})
			}
			write("\x1b\r")
			nativeWait(t, output, "saved annotation", func() bool {
				var value nativeState
				return nativeJSON(path, &value) && value.Editing == nil && len(value.Items) == 1 && value.Items[0]["quote"] == func() string {
					if scenario == "resize" {
						return "# Native review\n"
					}
					return "# Native review"
				}() && value.Items[0]["comment"] == comment
			})
			if scenario != "recover-edit" && scenario != "crash-flush" && scenario != "exit-edit" && strings.Contains(output.tail(), "BACKGROUND-RENDER-MARKER:1") {
				t.Fatal("a Pi background status update wrote into the native review")
			}
			write("s")
			if scenario == "keep-unsent" || scenario == "keep-whitespace" {
				nativeWait(t, output, "unsent editor confirmation", func() bool {
					var value any
					return nativeJSON(filepath.Join(private, "confirm.json"), &value) && strings.Contains(output.tail(), "Replace unsent editor text?")
				})
				write("\x1b")
			}
			trial := 1
			if scenario == "recover-edit" || scenario == "crash-flush" || scenario == "exit-edit" {
				trial = 2
			}
			result := readResult(trial)
			if scenario == "keep-unsent" || scenario == "keep-whitespace" {
				expected := "Keep my unsent editor text."
				if scenario == "keep-whitespace" {
					expected = " \n "
				}
				if result.Editor != expected {
					t.Fatalf("unsent draft was replaced: %q", result.Editor)
				}
			} else if scenario == "resize" {
				if !strings.Contains(result.Editor, "> # Native review\n>\n\n"+comment) {
					t.Fatalf("resize changed the visual selection: %q", result.Editor)
				}
			} else if !strings.Contains(result.Editor, "> # Native review\n\n"+comment) {
				t.Fatalf("prepared feedback changed the exact source quote: %q", result.Editor)
			}
			write("\x11")
		})
	}
	if requests := modelRequests.Load(); requests != 0 {
		t.Fatalf("Review caused %d model requests", requests)
	}
}
