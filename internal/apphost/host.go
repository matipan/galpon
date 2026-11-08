// Package apphost runs the Pi session of an agent in the app view. The agent
// tab of the Herdr app (the galpon-agent mod) draws the conversation; this
// host runs `galpon pi run --app <agent>`, which is Pi in RPC mode with the
// Galpon extension and the app bridge, in a hidden herdr-core terminal.
//
// The mod reads the session events with HTTP long polls on 127.0.0.1 and
// sends commands with HTTP posts. Each request carries the token of
// host.json, a file that only this user reads. On a screen (another computer
// that shows this one), the mod sends the requests with curl on this computer
// through herdr-core, and curl reads the token from the headers file.
//
// The host also tells herdr-core the state of the agent with OSC 7501
// reports on its own terminal: working, blocked, done or idle. The Galpon
// sidebar shows the agent tab with that state.
package apphost

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Version is the version of the HTTP API of the host.
const Version = 3

// TokenHeader carries the token of host.json.
const TokenHeader = "X-Galpon-App-Token"

// BridgePrefix starts the widget keys of the app bridge
// (internal/piagent/galpon-app-bridge.ts).
const BridgePrefix = "galpon-app:"

const (
	maxEvents     = 20000
	maxEventBytes = 48 << 20
	maxPollBytes  = 2 << 20
	maxPollWait   = 20 * time.Second
	maxBody       = 64 << 20
	// snapshotMessages is how many of the last messages a snapshot holds.
	snapshotMessages = 400
	// promptWait is how long a prompt waits for the answer of Pi. Pi answers
	// a prompt of an extension command only when the command ends, for
	// example after the person answers its dialogs. A later answer goes out
	// as an rpc_response event.
	promptWait = 3 * time.Second
)

// slowCommands can take minutes. The host answers at once and sends the Pi
// response later as an rpc_response event.
var slowCommands = map[string]bool{
	"abort": true, "compact": true, "new_session": true, "switch_session": true, "fork": true,
	"clone": true, "export_html": true, "bash": true,
}

var promptCommands = map[string]bool{"prompt": true, "steer": true, "follow_up": true}

var dialogMethods = map[string]bool{"select": true, "confirm": true, "input": true, "editor": true}

var imageExtensions = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp"}

// Options configure a host.
type Options struct {
	// AgentID is the Galpon agent.
	AgentID string
	// Dir holds host.json, the headers file and host.log.
	Dir string
	// AppHome is the Herdr app home of this computer. Images of the session
	// go under it, because the app draws images by their path in its home.
	AppHome string
	// Command starts Pi in RPC mode, such as galpon pi run --app <agent>.
	Command []string
	// Env is the environment of Command. Nil uses the environment of the
	// host without the herdr-core and Herdr variables.
	Env []string
	// CWD is the folder that the host reports for the agent.
	CWD string
	// IdleAfter ends a host that had no client for that long while nothing
	// runs. Zero keeps the host.
	IdleAfter time.Duration
	// Status gets the OSC 7501 reports, normally the host terminal. Nil
	// sends none.
	Status io.Writer
	// Rename gives the agent a new title in Galpon and returns the title
	// that Galpon saved.
	Rename func(ctx context.Context, title string) (string, error)
	// Clipboard returns the image of the clipboard and its MIME type. Nil
	// uses wl-paste.
	Clipboard func(ctx context.Context) ([]byte, string, error)
}

// Info is the content of host.json.
type Info struct {
	Version   int    `json:"version"`
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	Token     string `json:"token"`
	Headers   string `json:"headers"`
	AgentID   string `json:"agentId"`
	CWD       string `json:"cwd"`
	StartedAt int64  `json:"startedAt"`
}

// Dir is the folder of the host files of an agent.
func Dir(stateDir, agentID string) string {
	return filepath.Join(stateDir, "agents", agentID, "app")
}

// ReadInfo reads host.json of the folder. It returns false when there is no
// host file or its process does not run.
func ReadInfo(dir string) (Info, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "host.json"))
	if err != nil {
		return Info{}, false
	}
	var info Info
	if json.Unmarshal(data, &info) != nil || info.PID <= 0 || info.Port <= 0 {
		return Info{}, false
	}
	if err := syscall.Kill(info.PID, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return Info{}, false
	}
	return info, true
}

type event struct {
	seq  int64
	json []byte
}

type pendingCall struct {
	done chan rpcResult
}

type rpcResult struct {
	record map[string]any
	seq    int64
	err    error
}

type dialog struct {
	record map[string]any
	at     time.Time
}

type upload struct {
	parts [][]byte
	got   int
	at    time.Time
}

// Host runs one Pi session.
type Host struct {
	opts      Options
	token     string
	startedAt time.Time

	mu         sync.Mutex
	seq        int64
	events     []event
	eventBytes int
	wake       chan struct{}

	streaming bool
	statuses  map[string]string
	widgets   map[string]map[string]any
	bridge    map[string]any
	title     string
	dialogs   map[string]dialog
	// unseen is true after a run ended and before a client said that the
	// person looked at the tab.
	unseen   bool
	reported string

	pending map[string]*pendingCall
	nextID  int64

	stdin     io.WriteCloser
	writeMu   sync.Mutex
	cmd       *exec.Cmd
	alive     bool
	exited    *processExit
	lastError string
	stderr    []string

	lastClient time.Time
	waiters    int
	uploads    map[string]*upload

	server   *http.Server
	port     int
	stopOnce sync.Once
	stopped  chan struct{}
	ready    chan struct{}
}

type processExit struct {
	Code   *int   `json:"code"`
	Signal string `json:"signal"`
}

// New makes a host.
func New(opts Options) (*Host, error) {
	if opts.AgentID == "" || opts.Dir == "" || len(opts.Command) == 0 {
		return nil, errors.New("the app host needs an agent, a folder and a Pi command")
	}
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	return &Host{
		opts: opts, token: hex.EncodeToString(token), startedAt: time.Now(),
		wake: make(chan struct{}), statuses: map[string]string{}, widgets: map[string]map[string]any{},
		bridge: map[string]any{}, dialogs: map[string]dialog{}, pending: map[string]*pendingCall{},
		uploads: map[string]*upload{}, lastClient: time.Now(), stopped: make(chan struct{}), ready: make(chan struct{}),
	}, nil
}

// Run serves the session until Pi exits, the context ends, a client asks the
// host to stop, or the host is idle for Options.IdleAfter.
func (h *Host) Run(ctx context.Context) error {
	if err := os.MkdirAll(h.opts.Dir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(h.imagesDir(), 0o700); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	h.port = listener.Addr().(*net.TCPAddr).Port
	h.server = &http.Server{Handler: http.HandlerFunc(h.serve), ReadHeaderTimeout: 10 * time.Second}
	// curl reads the token from this file, so that it is not in the process
	// list of the computer.
	if err := os.WriteFile(h.headersPath(), []byte(TokenHeader+": "+h.token+"\n"), 0o600); err != nil {
		_ = listener.Close()
		return err
	}
	if err := h.writeInfo(); err != nil {
		_ = listener.Close()
		return err
	}
	go func() { _ = h.server.Serve(listener) }()
	h.logf("listening on 127.0.0.1:%d", h.port)
	h.report()
	if err := h.startPi(); err != nil {
		h.mu.Lock()
		h.lastError = err.Error()
		h.mu.Unlock()
		h.logf("start failed: %v", err)
		h.piExited(nil, "")
	}
	close(h.ready)
	idle := time.NewTicker(30 * time.Second)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			h.Stop()
		case <-h.stopped:
			return h.shutdown()
		case <-idle.C:
			if h.idle() {
				h.logf("no client for %s and nothing runs", h.opts.IdleAfter)
				h.Stop()
			}
		}
	}
}

// Stop asks the host to end.
func (h *Host) Stop() {
	h.stopOnce.Do(func() { close(h.stopped) })
}

// Port is the TCP port of the HTTP API, after Run started it.
func (h *Host) Port() int { return h.port }

// Token is the token that each request carries.
func (h *Host) Token() string { return h.token }

func (h *Host) headersPath() string { return filepath.Join(h.opts.Dir, "headers") }
func (h *Host) infoPath() string    { return filepath.Join(h.opts.Dir, "host.json") }

// imagesRel is the folder of the images of the session, relative to the app
// home.
func (h *Host) imagesRel() string {
	return filepath.Join("data", "galpon-agent", h.opts.AgentID, "images")
}

// appHome is the folder that the image paths start from.
func (h *Host) appHome() string {
	if h.opts.AppHome != "" {
		return h.opts.AppHome
	}
	return h.opts.Dir
}

func (h *Host) imagesDir() string { return filepath.Join(h.appHome(), h.imagesRel()) }

func (h *Host) writeInfo() error {
	data, err := json.MarshalIndent(Info{
		Version: Version, PID: os.Getpid(), Port: h.port, Token: h.token, Headers: h.headersPath(),
		AgentID: h.opts.AgentID, CWD: h.opts.CWD, StartedAt: h.startedAt.UnixMilli(),
	}, "", "  ")
	if err != nil {
		return err
	}
	temp := fmt.Sprintf("%s.%d.tmp", h.infoPath(), os.Getpid())
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, h.infoPath())
}

func (h *Host) logf(format string, args ...any) {
	file, err := os.OpenFile(filepath.Join(h.opts.Dir, "host.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = fmt.Fprintf(file, "%s %s\n", time.Now().Format(time.RFC3339Nano), fmt.Sprintf(format, args...))
}

// --- the event log

func (h *Host) pushLocked(value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	h.pushRawLocked(data)
}

// pushRaw adds an event that is already JSON.
func (h *Host) pushRawLocked(data []byte) {
	h.seq++
	h.events = append(h.events, event{seq: h.seq, json: data})
	h.eventBytes += len(data)
	for len(h.events) > maxEvents || h.eventBytes > maxEventBytes {
		h.eventBytes -= len(h.events[0].json)
		h.events = h.events[1:]
	}
	close(h.wake)
	h.wake = make(chan struct{})
}

func (h *Host) push(value any) {
	h.mu.Lock()
	h.pushLocked(value)
	h.mu.Unlock()
}

type pollResult struct {
	reset bool
	seq   int64
	more  bool
	raw   [][]byte
}

func (h *Host) eventsAfterLocked(since int64) pollResult {
	first := h.seq + 1
	if len(h.events) > 0 {
		first = h.events[0].seq
	}
	if since >= 0 && since < first-1 {
		return pollResult{reset: true, seq: h.seq}
	}
	from := int(max(0, since-first+1))
	var out [][]byte
	bytes := 0
	index := from
	for ; index < len(h.events); index++ {
		bytes += len(h.events[index].json)
		if len(out) > 0 && bytes > maxPollBytes {
			break
		}
		out = append(out, h.events[index].json)
	}
	last := max(since, 0)
	if index > from {
		last = h.events[index-1].seq
	}
	return pollResult{seq: last, more: index < len(h.events), raw: out}
}

// --- Pi

func (h *Host) startPi() error {
	cmd := exec.Command(h.opts.Command[0], h.opts.Command[1:]...)
	cmd.Dir = h.opts.CWD
	if cmd.Dir == "" {
		cmd.Dir, _ = os.UserHomeDir()
	}
	if info, err := os.Stat(cmd.Dir); err != nil || !info.IsDir() {
		cmd.Dir, _ = os.UserHomeDir()
	}
	cmd.Env = h.opts.Env
	if cmd.Env == nil {
		cmd.Env = PiEnvironment(os.Environ())
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	h.logf("start %s in %s", strings.Join(h.opts.Command, " "), cmd.Dir)
	if err := cmd.Start(); err != nil {
		return err
	}
	h.mu.Lock()
	h.cmd, h.stdin, h.alive = cmd, stdin, true
	h.mu.Unlock()
	var reads sync.WaitGroup
	reads.Add(2)
	go func() { defer reads.Done(); h.readStdout(stdout) }()
	go func() { defer reads.Done(); h.readStderr(stderr) }()
	go func() {
		reads.Wait()
		err := cmd.Wait()
		var code *int
		signal := ""
		if state := cmd.ProcessState; state != nil {
			if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				signal = status.Signal().String()
			} else {
				exitCode := state.ExitCode()
				code = &exitCode
			}
		}
		if err != nil {
			h.mu.Lock()
			if h.lastError == "" {
				h.lastError = err.Error()
			}
			h.mu.Unlock()
		}
		h.piExited(code, signal)
	}()
	return nil
}

// PiEnvironment is the environment of Pi without the identity of the host
// terminal: Pi must not report to a terminal that it does not own. Pi keeps
// HC_SCOPE, so its commands end with the host terminal, and HC_SESSION.
func PiEnvironment(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, value := range environ {
		key, _, _ := strings.Cut(value, "=")
		if key == "HC_TERMINAL_ID" || strings.HasPrefix(key, "HERDR_") {
			continue
		}
		out = append(out, value)
	}
	return out
}

func (h *Host) piExited(code *int, signal string) {
	h.mu.Lock()
	if h.exited != nil {
		h.mu.Unlock()
		return
	}
	h.alive = false
	h.exited = &processExit{Code: code, Signal: signal}
	h.streaming = false
	pending := h.pending
	h.pending = map[string]*pendingCall{}
	tail := append([]string(nil), h.stderr[max(0, len(h.stderr)-12):]...)
	h.pushLocked(map[string]any{"type": "host_pi_exit", "code": code, "signal": signal, "error": h.lastError, "stderr": tail})
	h.mu.Unlock()
	if code != nil {
		h.logf("pi exited with code %d", *code)
	} else {
		h.logf("pi exited by signal %s", signal)
	}
	for _, call := range pending {
		call.done <- rpcResult{err: errors.New("Pi exited")} //nolint:staticcheck // Pi is a proper name
	}
	h.report()
	// The mods read the exit event, then the host ends.
	go func() {
		time.Sleep(4 * time.Second)
		h.Stop()
	}()
}

func (h *Host) readStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		h.mu.Lock()
		h.stderr = append(h.stderr, line)
		if len(h.stderr) > 50 {
			h.stderr = h.stderr[1:]
		}
		h.mu.Unlock()
		h.logf("pi: %s", line)
	}
}

// readStdout reads one JSON record per line. It splits on LF only: JSON
// strings can hold U+2028 and U+2029.
func (h *Host) readStdout(stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		if len(line) > 0 {
			h.onLine(line)
		}
		if err != nil {
			return
		}
	}
}

// passThrough are the events that go to the mods as Pi wrote them: the
// host neither tracks nor changes them. message_update comes with each
// streamed delta, so it skips a decode and an encode.
var passThrough = map[string]bool{
	"message_update": true, "tool_execution_start": true, "turn_start": true, "auto_compaction_start": true,
	"auto_retry_start": true, "auto_retry_end": true, "queue_update": true,
}

func (h *Host) onLine(line []byte) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		h.logf("unreadable line from Pi: %.200s", line)
		return
	}
	if passThrough[head.Type] {
		h.mu.Lock()
		h.pushRawLocked(append([]byte(nil), line...))
		h.mu.Unlock()
		return
	}
	var record map[string]any
	if err := json.Unmarshal(line, &record); err != nil {
		h.logf("unreadable line from Pi: %.200s", line)
		return
	}
	kind := head.Type
	if kind == "response" {
		id, _ := record["id"].(string)
		h.mu.Lock()
		call := h.pending[id]
		delete(h.pending, id)
		seq := h.seq
		if call == nil && record["success"] == false {
			h.pushLocked(map[string]any{"type": "rpc_error", "command": record["command"], "error": record["error"]})
		}
		h.mu.Unlock()
		if call != nil {
			// Every event before this response has a sequence number at most seq.
			call.done <- rpcResult{record: record, seq: seq}
		} else if record["success"] == false {
			h.logf("Pi refused %v: %v", record["command"], record["error"])
		}
		return
	}
	h.mu.Lock()
	if h.bridgeLocked(record) {
		h.mu.Unlock()
		return
	}
	h.trackLocked(record)
	sanitized := h.sanitize(record)
	h.pushLocked(sanitized)
	h.mu.Unlock()
	h.report()
}

// bridgeLocked keeps the value of a bridge widget, and sends the mods a
// host_bridge event in place of the widget.
func (h *Host) bridgeLocked(record map[string]any) bool {
	if record["type"] != "extension_ui_request" || record["method"] != "setWidget" {
		return false
	}
	key, _ := record["widgetKey"].(string)
	name, ok := strings.CutPrefix(key, BridgePrefix)
	if !ok {
		return false
	}
	lines, _ := record["widgetLines"].([]any)
	if lines == nil {
		h.bridge[name] = nil
	} else {
		parts := make([]string, 0, len(lines))
		for _, line := range lines {
			text, _ := line.(string)
			parts = append(parts, text)
		}
		var value any
		if json.Unmarshal([]byte(strings.Join(parts, "\n")), &value) != nil {
			return true
		}
		h.bridge[name] = value
	}
	h.pushLocked(map[string]any{"type": "host_bridge", "key": name, "value": h.bridge[name]})
	return true
}

func (h *Host) trackLocked(record map[string]any) {
	switch record["type"] {
	case "agent_start":
		h.streaming = true
	case "agent_settled":
		if h.streaming {
			h.unseen = true
		}
		h.streaming = false
	case "dialog_closed":
		// The harness withdrew a dialog, for example a permission prompt of
		// Claude Code after an interrupt.
		id, _ := record["id"].(string)
		delete(h.dialogs, id)
	case "extension_ui_request":
		method, _ := record["method"].(string)
		switch method {
		case "setStatus":
			key, _ := record["statusKey"].(string)
			if text, _ := record["statusText"].(string); text != "" {
				h.statuses[key] = text
			} else {
				delete(h.statuses, key)
			}
		case "setWidget":
			key, _ := record["widgetKey"].(string)
			if lines, ok := record["widgetLines"].([]any); ok {
				placement, _ := record["widgetPlacement"].(string)
				if placement == "" {
					placement = "aboveEditor"
				}
				h.widgets[key] = map[string]any{"lines": lines, "placement": placement}
			} else {
				delete(h.widgets, key)
			}
		case "setTitle":
			h.title, _ = record["title"].(string)
		default:
			if dialogMethods[method] {
				id, _ := record["id"].(string)
				h.dialogs[id] = dialog{record: record, at: time.Now()}
			}
		}
	}
}

// openDialogsLocked forgets dialogs that timed out in Pi and returns the
// others, oldest first.
func (h *Host) openDialogsLocked() []map[string]any {
	now := time.Now()
	out := make([]map[string]any, 0, len(h.dialogs))
	for id, item := range h.dialogs {
		if timeout, ok := item.record["timeout"].(float64); ok && timeout > 0 && now.Sub(item.at) > time.Duration(timeout)*time.Millisecond {
			delete(h.dialogs, id)
			continue
		}
		record := make(map[string]any, len(item.record)+1)
		for key, value := range item.record {
			record[key] = value
		}
		record["at"] = item.at.UnixMilli()
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["at"].(int64) < out[j]["at"].(int64) })
	return out
}

func (h *Host) send(record map[string]any) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	h.mu.Lock()
	stdin, alive := h.stdin, h.alive
	h.mu.Unlock()
	if !alive || stdin == nil {
		return errors.New("Pi is not running") //nolint:staticcheck // Pi is a proper name
	}
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	_, err = stdin.Write(append(data, '\n'))
	return err
}

// rpc sends a command to Pi and waits for its response.
func (h *Host) rpc(ctx context.Context, command map[string]any, timeout time.Duration) (rpcResult, error) {
	<-h.ready
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return rpcResult{}, errors.New("Pi is not running") //nolint:staticcheck // Pi is a proper name
	}
	h.nextID++
	id := fmt.Sprintf("h%d", h.nextID)
	call := &pendingCall{done: make(chan rpcResult, 1)}
	h.pending[id] = call
	h.mu.Unlock()
	out := make(map[string]any, len(command)+1)
	for key, value := range command {
		out[key] = value
	}
	out["id"] = id
	if err := h.send(out); err != nil {
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
		return rpcResult{}, err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-call.done:
		return result, result.err
	case <-timer.C:
	case <-ctx.Done():
	}
	h.mu.Lock()
	delete(h.pending, id)
	h.mu.Unlock()
	kind, _ := command["type"].(string)
	return rpcResult{}, fmt.Errorf("Pi did not answer %s in time", kind) //nolint:staticcheck // Pi is a proper name
}

// --- images: Pi sends base64 data, the mods draw files under the app home

// imageSize reads the pixel size from a PNG, GIF or JPEG header, so that
// the mod can draw the image with its aspect ratio.
func imageSize(data []byte) (int, int, bool) {
	switch {
	case len(data) > 24 && binary.BigEndian.Uint32(data) == 0x89504e47:
		return int(binary.BigEndian.Uint32(data[16:])), int(binary.BigEndian.Uint32(data[20:])), true
	case len(data) > 10 && string(data[:3]) == "GIF":
		return int(binary.LittleEndian.Uint16(data[6:])), int(binary.LittleEndian.Uint16(data[8:])), true
	case len(data) > 4 && data[0] == 0xff && data[1] == 0xd8:
		at := 2
		for at+9 < len(data) {
			if data[at] != 0xff {
				return 0, 0, false
			}
			marker := data[at+1]
			length := int(binary.BigEndian.Uint16(data[at+2:]))
			if marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc {
				return int(binary.BigEndian.Uint16(data[at+7:])), int(binary.BigEndian.Uint16(data[at+5:])), true
			}
			at += 2 + length
		}
	}
	return 0, 0, false
}

// saveImage writes an image under the app home and returns its image block.
func (h *Host) saveImage(data []byte, mimeType string) (map[string]any, error) {
	ext := imageExtensions[mimeType]
	if ext == "" {
		ext = "bin"
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])[:24] + "." + ext
	file := filepath.Join(h.imagesDir(), name)
	if _, err := os.Stat(file); err != nil {
		if err := os.WriteFile(file, data, 0o600); err != nil {
			return nil, err
		}
	}
	block := map[string]any{"type": "image", "mimeType": mimeType, "path": filepath.Join(h.imagesRel(), name), "bytes": len(data)}
	if width, height, ok := imageSize(data); ok {
		block["width"], block["height"] = width, height
	}
	return block, nil
}

func (h *Host) externalize(content any) any {
	blocks, ok := content.([]any)
	if !ok {
		return content
	}
	out := make([]any, len(blocks))
	for index, item := range blocks {
		out[index] = item
		block, ok := item.(map[string]any)
		if !ok || block["type"] != "image" {
			continue
		}
		data, ok := block["data"].(string)
		if !ok {
			continue
		}
		mimeType, _ := block["mimeType"].(string)
		bytes, err := base64.StdEncoding.DecodeString(data)
		if err == nil {
			var saved map[string]any
			if saved, err = h.saveImage(bytes, mimeType); err == nil {
				out[index] = saved
				continue
			}
		}
		out[index] = map[string]any{"type": "image", "mimeType": mimeType, "error": err.Error()}
	}
	return out
}

func (h *Host) sanitizeMessage(value any) any {
	message, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if _, ok := message["content"].([]any); !ok {
		return message
	}
	out := make(map[string]any, len(message))
	for key, item := range message {
		out[key] = item
	}
	out["content"] = h.externalize(message["content"])
	return out
}

func (h *Host) sanitizeResult(value any) any {
	result, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if _, ok := result["content"].([]any); !ok {
		return result
	}
	out := make(map[string]any, len(result))
	for key, item := range result {
		out[key] = item
	}
	out["content"] = h.externalize(result["content"])
	return out
}

func (h *Host) sanitize(record map[string]any) map[string]any {
	switch record["type"] {
	case "message_start", "message_end":
		out := shallow(record)
		out["message"] = h.sanitizeMessage(record["message"])
		return out
	case "tool_execution_update":
		out := shallow(record)
		out["partialResult"] = h.sanitizeResult(record["partialResult"])
		return out
	case "tool_execution_end":
		out := shallow(record)
		out["result"] = h.sanitizeResult(record["result"])
		return out
	case "turn_end":
		// message_end events already carry its messages.
		return map[string]any{"type": "turn_end"}
	case "agent_end":
		messages, _ := record["messages"].([]any)
		return map[string]any{"type": "agent_end", "willRetry": record["willRetry"], "count": len(messages)}
	}
	return record
}

func shallow(record map[string]any) map[string]any {
	out := make(map[string]any, len(record))
	for key, value := range record {
		out[key] = value
	}
	return out
}

// --- the state for herdr-core

// state is the OSC 7501 state of the agent: blocked while a dialog waits
// for the person, working during a run, done after a run until a client
// says that the person looked, else idle.
func (h *Host) state() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.exited != nil:
		return "idle"
	case len(h.dialogs) > 0:
		return "blocked"
	case h.streaming:
		return "working"
	case h.unseen:
		return "done"
	}
	return "idle"
}

// report writes an OSC 7501 report when the state changed.
func (h *Host) report() {
	if h.opts.Status == nil {
		return
	}
	state := h.state()
	h.mu.Lock()
	if state == h.reported {
		h.mu.Unlock()
		return
	}
	h.reported = state
	h.mu.Unlock()
	fields := "state=" + state + ":app=pi"
	if state == "blocked" {
		fields += ":kind=question"
	}
	_, _ = fmt.Fprintf(h.opts.Status, "\x1b]7501;%s\x1b\\", fields)
}

// --- clipboard images

func clipboardImage(ctx context.Context) ([]byte, string, error) {
	types, err := exec.CommandContext(ctx, "wl-paste", "--list-types").Output()
	if err != nil {
		return nil, "", errors.New("the clipboard has no image")
	}
	for _, line := range strings.Split(string(types), "\n") {
		mimeType := strings.TrimSpace(line)
		if imageExtensions[mimeType] == "" {
			continue
		}
		data, err := exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", mimeType).Output()
		if err != nil || len(data) == 0 {
			break
		}
		return data, mimeType, nil
	}
	return nil, "", errors.New("the clipboard has no image")
}

// --- HTTP

func (h *Host) authorized(r *http.Request) bool {
	given := []byte(r.Header.Get(TokenHeader))
	return len(given) == len(h.token) && subtle.ConstantTimeCompare(given, []byte(h.token)) == 1
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func readBody(r *http.Request) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBody {
		return nil, errors.New("the request is too large")
	}
	return data, nil
}

func (h *Host) serve(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	h.mu.Lock()
	h.lastClient = time.Now()
	h.mu.Unlock()
	defer func() {
		if recovered := recover(); recovered != nil {
			reply(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprint(recovered)})
		}
	}()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/events":
		h.serveEvents(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/snapshot":
		snapshot, err := h.snapshot(r.Context())
		if err != nil {
			reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, snapshot)
	case r.Method == http.MethodGet && r.URL.Path == "/state":
		reply(w, http.StatusOK, h.stateBody())
	case r.Method == http.MethodGet && r.URL.Path == "/image":
		h.serveImage(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/chunk":
		h.serveChunk(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/ui":
		h.serveDialogAnswer(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/attach":
		read := h.opts.Clipboard
		if read == nil {
			read = clipboardImage
		}
		data, mimeType, err := read(r.Context())
		if err != nil {
			reply(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		block, err := h.saveImage(data, mimeType)
		if err != nil {
			reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, block)
	case r.Method == http.MethodPost && r.URL.Path == "/galpon/rename":
		h.serveRename(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/seen":
		h.mu.Lock()
		h.unseen = false
		h.mu.Unlock()
		h.report()
		reply(w, http.StatusOK, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/shutdown":
		reply(w, http.StatusOK, map[string]bool{"ok": true})
		h.Stop()
	case r.Method == http.MethodPost:
		body, err := readBody(r)
		if err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		h.route(w, r, r.URL.Path, body)
	default:
		reply(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (h *Host) serveEvents(w http.ResponseWriter, r *http.Request) {
	since, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if err != nil {
		since = -1
	}
	waitMS, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	wait := min(max(time.Duration(waitMS)*time.Millisecond, 0), maxPollWait)
	h.mu.Lock()
	result := h.eventsAfterLocked(since)
	if !result.reset && len(result.raw) == 0 && wait > 0 && h.exited == nil {
		wake := h.wake
		h.waiters++
		h.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-wake:
		case <-timer.C:
		case <-r.Context().Done():
		case <-h.stopped:
		}
		timer.Stop()
		h.mu.Lock()
		h.waiters--
		result = h.eventsAfterLocked(since)
	}
	h.lastClient = time.Now()
	alive, streaming := h.alive, h.streaming
	h.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if result.reset {
		_ = json.NewEncoder(w).Encode(map[string]any{"reset": true, "seq": result.seq, "alive": alive})
		return
	}
	// Events are kept as JSON text, so the body is built by hand.
	var body bytes.Buffer
	fmt.Fprintf(&body, `{"seq":%d,"more":%t,"alive":%t,"streaming":%t,"events":[`, result.seq, result.more, alive, streaming)
	for index, raw := range result.raw {
		if index > 0 {
			body.WriteByte(',')
		}
		body.Write(raw)
	}
	body.WriteString("]}")
	_, _ = w.Write(body.Bytes())
}

func (h *Host) stateBody() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	piPID := 0
	if h.cmd != nil && h.cmd.Process != nil {
		piPID = h.cmd.Process.Pid
	}
	return map[string]any{
		"version": Version, "agentId": h.opts.AgentID, "cwd": h.opts.CWD, "pid": os.Getpid(), "piPid": piPID,
		"alive": h.alive, "exit": h.exited, "streaming": h.streaming, "startedAt": h.startedAt.UnixMilli(),
		"lastError": h.lastError, "stderr": append([]string(nil), h.stderr[max(0, len(h.stderr)-12):]...), "background": true,
		"galpon": h.galponFactsLocked(),
	}
}

func (h *Host) galponFactsLocked() map[string]any {
	return map[string]any{"enabled": true, "agentId": h.opts.AgentID, "error": ""}
}

// latestTodos is the todo list of the session: the tasks of the last result
// of the todo tool.
func latestTodos(messages []any) any {
	for index := len(messages) - 1; index >= 0; index-- {
		message, _ := messages[index].(map[string]any)
		if message["role"] != "toolResult" || message["toolName"] != "todo" {
			continue
		}
		details, _ := message["details"].(map[string]any)
		if tasks, ok := details["tasks"].([]any); ok {
			return tasks
		}
	}
	return nil
}

func (h *Host) snapshot(ctx context.Context) (map[string]any, error) {
	messages, err := h.rpc(ctx, map[string]any{"type": "get_messages"}, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	data, _ := messages.record["data"].(map[string]any)
	all, _ := data["messages"].([]any)
	recent := all[max(0, len(all)-snapshotMessages):]
	sanitized := make([]any, len(recent))
	for index, message := range recent {
		sanitized[index] = h.sanitizeMessage(message)
	}
	var calls sync.WaitGroup
	results := make([]map[string]any, 4)
	for index, kind := range []string{"get_state", "get_session_stats", "get_commands", "get_available_thinking_levels"} {
		calls.Add(1)
		go func() {
			defer calls.Done()
			if result, err := h.rpc(ctx, map[string]any{"type": kind}, time.Minute); err == nil {
				results[index], _ = result.record["data"].(map[string]any)
			}
		}()
	}
	calls.Wait()
	var commands, levels any = []any{}, nil
	if results[2] != nil && results[2]["commands"] != nil {
		commands = results[2]["commands"]
	}
	if results[3] != nil {
		levels = results[3]["levels"]
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	statuses := make(map[string]string, len(h.statuses))
	for key, value := range h.statuses {
		statuses[key] = value
	}
	widgets := make(map[string]any, len(h.widgets))
	for key, value := range h.widgets {
		widgets[key] = value
	}
	bridge := make(map[string]any, len(h.bridge))
	for key, value := range h.bridge {
		bridge[key] = value
	}
	return map[string]any{
		"seq": messages.seq, "total": len(all), "messages": sanitized, "todos": latestTodos(all),
		"state": nilMap(results[0]), "stats": nilMap(results[1]), "commands": commands, "thinkingLevels": levels,
		"statuses": statuses, "widgets": widgets, "bridge": bridge, "galpon": h.galponFactsLocked(),
		"dialogs": h.openDialogsLocked(), "title": h.title, "streaming": h.streaming, "alive": h.alive,
		"cwd": h.opts.CWD, "agentId": h.opts.AgentID, "background": true, "unseen": h.unseen,
	}, nil
}

func nilMap(value map[string]any) any {
	if value == nil {
		return nil
	}
	return value
}

var safeImagePath = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

func (h *Host) serveImage(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(r.URL.Query().Get("path"))
	root := h.imagesRel()
	if !safeImagePath.MatchString(path) || !strings.HasPrefix(path, root+string(filepath.Separator)) {
		reply(w, http.StatusNotFound, map[string]string{"error": "no such image"})
		return
	}
	data, err := os.ReadFile(filepath.Join(h.appHome(), path))
	if err != nil {
		reply(w, http.StatusNotFound, map[string]string{"error": "no such image"})
		return
	}
	mimeType := "application/octet-stream"
	for kind, ext := range imageExtensions {
		if strings.HasSuffix(path, "."+ext) {
			mimeType = kind
		}
	}
	reply(w, http.StatusOK, map[string]string{"mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(data)})
}

var chunkID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// serveChunk takes one part of a big request. A screen sends requests with
// curl on this computer, and one command line has a size limit.
func (h *Host) serveChunk(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	id := query.Get("id")
	index, indexErr := strconv.Atoi(query.Get("index"))
	count, countErr := strconv.Atoi(query.Get("count"))
	to := query.Get("to")
	if !chunkID.MatchString(id) || indexErr != nil || countErr != nil || index < 0 || count <= 0 || count > 4096 || index >= count || (to != "/rpc" && to != "/attach-data") {
		reply(w, http.StatusBadRequest, map[string]string{"error": "bad chunk"})
		return
	}
	part, err := readBody(r)
	if err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	h.mu.Lock()
	for key, item := range h.uploads {
		if time.Since(item.at) > 10*time.Minute {
			delete(h.uploads, key)
		}
	}
	item := h.uploads[id]
	if item == nil || len(item.parts) != count {
		item = &upload{parts: make([][]byte, count), at: time.Now()}
		h.uploads[id] = item
	}
	if item.parts[index] == nil {
		item.got++
	}
	item.parts[index] = part
	complete := item.got == count
	if complete {
		delete(h.uploads, id)
	}
	h.mu.Unlock()
	if !complete {
		reply(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	h.route(w, r, to, bytes.Join(item.parts, nil))
}

func (h *Host) serveDialogAnswer(w http.ResponseWriter, r *http.Request) {
	data, err := readBody(r)
	var body struct {
		ID        string `json:"id"`
		Cancelled bool   `json:"cancelled"`
		Confirmed *bool  `json:"confirmed"`
		Value     any    `json:"value"`
	}
	if err == nil {
		err = json.Unmarshal(data, &body)
	}
	if err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "the answer is not JSON"})
		return
	}
	h.mu.Lock()
	_, open := h.dialogs[body.ID]
	delete(h.dialogs, body.ID)
	h.mu.Unlock()
	if !open {
		reply(w, http.StatusNotFound, map[string]string{"error": "no such dialog"})
		return
	}
	answer := map[string]any{"type": "extension_ui_response", "id": body.ID}
	switch {
	case body.Cancelled:
		answer["cancelled"] = true
	case body.Confirmed != nil:
		answer["confirmed"] = *body.Confirmed
	default:
		answer["value"] = fmt.Sprint(valueOr(body.Value, ""))
	}
	if err := h.send(answer); err != nil {
		reply(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	// Other copies of the mod close the same dialog.
	h.push(map[string]any{"type": "dialog_closed", "id": body.ID})
	h.report()
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

func valueOr(value, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

func (h *Host) serveRename(w http.ResponseWriter, r *http.Request) {
	data, err := readBody(r)
	var body struct {
		Title string `json:"title"`
	}
	if err == nil {
		err = json.Unmarshal(data, &body)
	}
	if err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "the name is not JSON"})
		return
	}
	if h.opts.Rename == nil {
		reply(w, http.StatusConflict, map[string]string{"error": "this host cannot rename its agent"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	title, err := h.opts.Rename(ctx, body.Title)
	if err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	h.mu.Lock()
	galpon, _ := h.bridge["galpon"].(map[string]any)
	next := map[string]any{}
	for key, value := range galpon {
		next[key] = value
	}
	next["title"] = title
	h.bridge["galpon"] = next
	h.pushLocked(map[string]any{"type": "host_bridge", "key": "galpon", "value": next})
	h.mu.Unlock()
	reply(w, http.StatusOK, map[string]string{"title": title})
}

// route runs a posted command: /rpc or /attach-data.
func (h *Host) route(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	switch path {
	case "/rpc":
		var command map[string]any
		if err := json.Unmarshal(body, &command); err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": "the command is not JSON"})
			return
		}
		kind, _ := command["type"].(string)
		if kind == "" {
			reply(w, http.StatusBadRequest, map[string]string{"error": "the command needs a type"})
			return
		}
		command, err := h.inlineImages(command)
		if err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		later := func(result <-chan rpcCall) {
			call := <-result
			event := map[string]any{"type": "rpc_response", "command": kind}
			if call.err != nil {
				event["success"], event["error"] = false, call.err.Error()
			} else {
				event["success"], event["error"], event["data"] = call.result.record["success"], call.result.record["error"], call.result.record["data"]
			}
			h.push(event)
		}
		start := func(timeout time.Duration) <-chan rpcCall {
			out := make(chan rpcCall, 1)
			go func() {
				result, err := h.rpc(context.Background(), command, timeout)
				out <- rpcCall{result: result, err: err}
			}()
			return out
		}
		switch {
		case slowCommands[kind]:
			go later(start(30 * time.Minute))
			reply(w, http.StatusAccepted, map[string]bool{"accepted": true})
		case promptCommands[kind]:
			result := start(30 * time.Minute)
			timer := time.NewTimer(promptWait)
			defer timer.Stop()
			select {
			case call := <-result:
				if call.err != nil {
					reply(w, http.StatusInternalServerError, map[string]string{"error": call.err.Error()})
					return
				}
				reply(w, http.StatusOK, call.result.record)
			case <-timer.C:
				go later(result)
				reply(w, http.StatusAccepted, map[string]bool{"accepted": true})
			}
		default:
			result, err := h.rpc(r.Context(), command, time.Minute)
			if err != nil {
				reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if kind == "get_messages" {
				if data, ok := result.record["data"].(map[string]any); ok {
					if messages, ok := data["messages"].([]any); ok {
						for index, message := range messages {
							messages[index] = h.sanitizeMessage(message)
						}
					}
				}
			}
			reply(w, http.StatusOK, result.record)
		}
	case "/attach-data":
		var image struct {
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
		}
		if json.Unmarshal(body, &image) != nil || imageExtensions[image.MimeType] == "" || image.Data == "" {
			reply(w, http.StatusBadRequest, map[string]string{"error": "an image needs mimeType and data"})
			return
		}
		data, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": "the image data is not base64"})
			return
		}
		block, err := h.saveImage(data, image.MimeType)
		if err != nil {
			reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, block)
	default:
		reply(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

type rpcCall struct {
	result rpcResult
	err    error
}

// inlineImages gives Pi the data of the images that a prompt names by path
// under the app home.
func (h *Host) inlineImages(command map[string]any) (map[string]any, error) {
	images, ok := command["images"].([]any)
	if !ok {
		return command, nil
	}
	out := make([]any, len(images))
	for index, item := range images {
		image, _ := item.(map[string]any)
		if _, ok := image["data"].(string); ok {
			out[index] = image
			continue
		}
		path, _ := image["path"].(string)
		file := path
		if !filepath.IsAbs(file) {
			file = filepath.Join(h.appHome(), path)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read image %s: %w", path, err)
		}
		mimeType, _ := image["mimeType"].(string)
		if mimeType == "" {
			mimeType = "image/png"
		}
		out[index] = map[string]any{"type": "image", "mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(data)}
	}
	next := shallow(command)
	next["images"] = out
	return next, nil
}

// --- the end

// idle reports a host that had no client for IdleAfter while nothing runs:
// no run, no dialog, no background command or monitor and no delegated work
// that comes back to Pi.
func (h *Host) idle() bool {
	if h.opts.IdleAfter <= 0 {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.lastClient) < h.opts.IdleAfter || h.waiters > 0 || h.streaming || len(h.dialogs) > 0 || h.exited != nil {
		return false
	}
	if h.statuses["galpon-background"] != "" || runningTasks(h.bridge["tasks"]) || liveWork(h.bridge["work"]) {
		return false
	}
	return true
}

func runningTasks(value any) bool {
	facts, _ := value.(map[string]any)
	tasks, _ := facts["tasks"].([]any)
	for _, item := range tasks {
		task, _ := item.(map[string]any)
		if task["status"] == "running" {
			return true
		}
	}
	return false
}

// liveWork reports delegated work that runs: its result comes back to Pi.
func liveWork(value any) bool {
	facts, _ := value.(map[string]any)
	var live func(items []any) bool
	live = func(items []any) bool {
		for _, entry := range items {
			item, _ := entry.(map[string]any)
			observation, _ := item["observation"].(map[string]any)
			switch observation["state"] {
			case "queued", "started", "waiting":
				return true
			}
			children, _ := item["children"].([]any)
			if live(children) {
				return true
			}
		}
		return false
	}
	work, _ := facts["work"].([]any)
	return live(work)
}

func (h *Host) shutdown() error {
	h.logf("shutdown")
	if data, err := os.ReadFile(h.infoPath()); err == nil {
		var info Info
		if json.Unmarshal(data, &info) == nil && info.PID == os.Getpid() {
			_ = os.Remove(h.infoPath())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = h.server.Shutdown(ctx)
	cancel()
	h.mu.Lock()
	cmd, stdin, alive := h.cmd, h.stdin, h.alive
	h.mu.Unlock()
	if alive && cmd != nil {
		// Closing stdin asks Pi for an orderly shutdown.
		h.writeMu.Lock()
		_ = stdin.Close()
		h.writeMu.Unlock()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			h.mu.Lock()
			done := h.exited != nil
			h.mu.Unlock()
			if done {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		h.mu.Lock()
		done := h.exited != nil
		h.mu.Unlock()
		if !done && cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	if h.opts.Status != nil {
		_, _ = fmt.Fprint(h.opts.Status, "\x1b]7501;state=clear\x1b\\")
	}
	return nil
}
