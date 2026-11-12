package nativeagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/model"
)

// The app view of a Claude Code agent.
//
// The app host of the Herdr app (internal/apphost) runs one child process for
// an agent in the app view and speaks the RPC protocol of Pi with it: commands
// on the stdin of the child, and responses and events on its stdout, one JSON
// value per line. For a Claude Code agent the child is the native runtime,
// which runs Claude Code in stream-json mode. claudeApp translates between the
// two: the stream of Claude Code becomes the events of Pi that the agent tab
// draws, and the commands of the tab become the user messages and the control
// requests of Claude Code. Permission prompts and questions of Claude Code
// become dialogs of the tab.

const claudeProvider = "anthropic"

// claudeHistoryLimit is how many of the last messages a snapshot holds.
const claudeHistoryLimit = 500

// claudeBlockedCommands are the slash commands that would end or replace the
// conversation of the agent, which belongs to Galpon.
var claudeBlockedCommands = map[string]bool{
	"clear": true, "new": true, "resume": true, "exit": true, "quit": true, "login": true, "logout": true,
	"branch": true, "teleport": true, "remote-control": true,
}

// claudeHiddenCommands are slash commands that need the terminal interface of
// Claude Code. The tab does not offer them.
var claudeHiddenCommands = map[string]bool{
	"ide": true, "terminal-setup": true, "vim": true, "theme": true, "statusline": true, "keybindings": true,
	"rewind": true, "upgrade": true, "permissions": true, "agents": true, "hooks": true, "memory": true,
	"plugin": true, "plugins": true, "install-github-app": true, "migrate-installer": true, "release-notes": true,
	"mobile": true, "desktop": true, "chrome": true, "feedback": true, "bug": true, "export": true, "privacy-settings": true,
	"__remote-workflow": true, "workflow-launch-exec": true, "heapdump": true,
	// These do nothing in a tab, or the tab has its own: /model, /compact and
	// /name.
	"color": true, "focus": true, "fast": true, "auto-mode-setup": true, "rename": true, "model": true, "compact": true,
}

// claudeAppSettings are the choices of the person in the tab that Claude Code
// keeps only for its process: the model, the effort and the permission mode.
// Galpon saves them, and the next start of the agent uses them again.
type claudeAppSettings struct {
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
}

// claudeModel is a model that Claude Code offers.
type claudeModel struct {
	Value, Resolved, Name, Description string
	Efforts                            []string
}

type claudePrompt struct {
	Sent     int64
	Text     string
	Display  string
	Delivery bool
	Priority string
	Started  bool
	Queued   bool
	// Content is the message as Claude Code got it, text or blocks with
	// images. Shown tells that the tab has the message already.
	Content any
	Shown   bool
}

// claudeStream is the assistant message that streams now.
type claudeStream struct {
	id, model string
	blocks    map[int]*claudeBlock
	order     []int
	usage     map[string]any
	stop      string
	at        int64
}

type claudeBlock struct {
	kind     string
	text     strings.Builder
	id, name string
}

type claudeCall struct {
	name  string
	args  any
	at    time.Time
	ended bool
}

// claudeDialog is a permission request of Claude Code that the person answers
// in the tab, in one or more dialogs.
type claudeDialog struct {
	requestID string
	request   map[string]any
	kind      string
	steps     int
	actions   map[string]string
	questions []any
	index     int
	answers   map[string]any
	// waiting is the action that waits for the text of an input dialog.
	waiting string
}

type claudeApp struct {
	appBridge
	stop   context.CancelFunc
	driver *claudeDriver
	dir    string
	ready  chan struct{}

	mu            sync.Mutex
	readyDone     bool
	next          int
	pending       map[string]chan map[string]any
	models        []claudeModel
	windows       map[string]float64
	commands      []any
	model         string
	effort        string
	mode          string
	contextUsage  map[string]any
	cost          float64
	streaming     bool
	turn          []string
	announced     map[string]bool
	wake          string
	wakes         int
	prompts       map[string]*claudePrompt
	message       *claudeStream
	streamed      map[string]bool
	calls         map[string]*claudeCall
	dialogs       map[string]*claudeDialog
	tasks         map[string]map[string]any
	taskNotices   map[string]bool
	retrying      bool
	compacting    bool
	compacted     map[string]any
	lastAssistant string
	interrupting  bool
	abortShown    bool
	costs         map[string]claudeTurnCost
	mcpWarned     bool
	queueKey      string
}

func newClaudeApp(ctx context.Context, options launchOptions, driver *claudeDriver) *claudeApp {
	dir := filepath.Join(options.Config.StateDir, "agents", options.Agent.ID, "app")
	a := &claudeApp{appBridge: appBridge{ctx: ctx, options: options, harness: model.HarnessClaude}, stop: options.Stop, driver: driver, dir: dir, ready: make(chan struct{}),
		pending: map[string]chan map[string]any{}, windows: map[string]float64{}, announced: map[string]bool{}, prompts: map[string]*claudePrompt{},
		streamed: map[string]bool{}, calls: map[string]*claudeCall{}, dialogs: map[string]*claudeDialog{}, tasks: map[string]map[string]any{},
		costs: map[string]claudeTurnCost{}}
	if data, err := os.ReadFile(a.costsPath()); err == nil {
		_ = json.Unmarshal(data, &a.costs)
	}
	a.cost = claudeSavedCost(driver.path)
	if a.cost == 0 && options.Agent.SessionPath == "" && !options.ConvertedContext && options.Source.SessionPath != "" {
		// A new fork: Claude Code counts the cost of the source session in
		// the totals of the fork, so the turns of the fork start from it.
		a.cost = claudeSavedCost(options.Source.SessionPath)
	}
	return a
}

func (a *claudeApp) settingsPath() string { return filepath.Join(a.dir, "claude.json") }
func (a *claudeApp) costsPath() string    { return filepath.Join(a.dir, "claude-turns.json") }

// claudeAppArgs gives the flags of the saved choices of the person.
func claudeAppArgs(stateDir, agentID string) []string {
	var settings claudeAppSettings
	data, err := os.ReadFile(filepath.Join(stateDir, "agents", agentID, "app", "claude.json"))
	if err != nil || json.Unmarshal(data, &settings) != nil {
		return nil
	}
	var args []string
	if settings.Model != "" {
		args = append(args, "--model", settings.Model)
	}
	if settings.Effort != "" {
		args = append(args, "--effort", settings.Effort)
	}
	if settings.PermissionMode != "" {
		args = append(args, "--permission-mode", settings.PermissionMode)
	}
	return args
}

func (a *claudeApp) saveSettings() {
	a.mu.Lock()
	settings := claudeAppSettings{Model: a.model, Effort: a.effort, PermissionMode: a.mode}
	a.mu.Unlock()
	if settings.PermissionMode == "bypassPermissions" {
		// Bypass mode is a choice for one run, never the start of the next one.
		settings.PermissionMode = ""
	}
	_ = atomicJSON(a.settingsPath(), settings)
}

// start sends the initialize request, then reads the commands of the tab.
func (a *claudeApp) start() {
	go func() {
		response, err := a.control(map[string]any{"subtype": "initialize"}, 30*time.Second)
		if err == nil {
			a.initialized(response)
		}
		if settings, err := a.control(map[string]any{"subtype": "get_settings"}, 10*time.Second); err == nil {
			applied := objectValue(settings["applied"])
			a.mu.Lock()
			if model := stringValue(applied["model"]); model != "" {
				a.model = model
			}
			if effort := stringValue(applied["effort"]); effort != "" {
				a.effort = effort
			}
			a.mu.Unlock()
		}
		a.markReady()
		a.refreshContext()
		a.identity()
		a.publishTasks()
	}()
	go a.commandsLoop()
	go a.workLoop()
}

func (a *claudeApp) markReady() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.readyDone {
		a.readyDone = true
		close(a.ready)
	}
}

func (a *claudeApp) waitReady() {
	select {
	case <-a.ready:
	case <-a.ctx.Done():
	case <-time.After(30 * time.Second):
	}
}

func (a *claudeApp) initialized(response map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.models = a.models[:0]
	for _, raw := range anyList(response["models"]) {
		item := objectValue(raw)
		value := claudeModel{Value: stringValue(item["value"]), Resolved: stringValue(item["resolvedModel"]), Description: stringValue(item["description"])}
		for _, level := range anyList(item["supportedEffortLevels"]) {
			value.Efforts = append(value.Efforts, stringValue(level))
		}
		value.Name = claudeModelName(value.Resolved, stringValue(item["displayName"]), value.Description)
		if value.Resolved == "" {
			value.Resolved = value.Value
		}
		a.models = append(a.models, value)
	}
	a.commands = a.commands[:0]
	for _, raw := range anyList(response["commands"]) {
		item := objectValue(raw)
		name := stringValue(item["name"])
		if name == "" || claudeHiddenCommands[name] || claudeBlockedCommands[name] {
			continue
		}
		description := stringValue(item["description"])
		if hint := stringValue(item["argumentHint"]); hint != "" {
			description = strings.TrimSpace(description + " · " + hint)
		}
		a.commands = append(a.commands, map[string]any{"name": name, "description": description, "source": "claude"})
	}
	if mode := stringValue(response["current_permission_mode"]); mode != "" {
		a.mode = mode
	}
}

var claudeVersion = regexp.MustCompile(`^claude-([a-z]+)-(\d+)(?:-(\d+))?`)

// claudeModelName names a model for people, such as "Opus 5.5".
func claudeModelName(resolved, display, description string) string {
	if name, _, ok := strings.Cut(description, " · "); ok && name != "" && !strings.HasPrefix(name, "Use the default") {
		return name
	}
	if match := claudeVersion.FindStringSubmatch(resolved); match != nil {
		name := strings.ToUpper(match[1][:1]) + match[1][1:] + " " + match[2]
		if match[3] != "" && len(match[3]) < 3 {
			name += "." + match[3]
		}
		return name
	}
	if display != "" {
		return display
	}
	return resolved
}

// control sends a control request to Claude Code and waits for its answer.
func (a *claudeApp) control(request map[string]any, timeout time.Duration) (map[string]any, error) {
	a.mu.Lock()
	a.next++
	id := fmt.Sprintf("galpon-%d", a.next)
	reply := make(chan map[string]any, 1)
	a.pending[id] = reply
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()
	if err := a.driver.send(map[string]any{"type": "control_request", "request_id": id, "request": request}); err != nil {
		return nil, err
	}
	select {
	case response := <-reply:
		if response["subtype"] == "error" {
			return nil, errors.New(firstText(stringValue(response["error"]), "Claude Code refused the request"))
		}
		data := objectValue(response["response"])
		if data == nil {
			data = map[string]any{}
		}
		return data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("Claude Code did not answer %s in time", stringValue(request["subtype"])) //nolint:staticcheck // Claude Code is a proper name
	case <-a.ctx.Done():
		return nil, a.ctx.Err()
	}
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// --- the stream of Claude Code

// frame handles one line that Claude Code writes. Lines come in order, from
// one goroutine.
func (a *claudeApp) frame(data []byte) {
	var event map[string]any
	if json.Unmarshal(data, &event) != nil {
		return
	}
	switch event["type"] {
	case "control_response":
		response := objectValue(event["response"])
		a.mu.Lock()
		reply := a.pending[stringValue(response["request_id"])]
		a.mu.Unlock()
		if reply != nil {
			reply <- response
		}
	case "control_request":
		a.controlRequest(event)
	case "control_cancel_request":
		a.cancelDialogs(stringValue(event["request_id"]))
	case "system":
		a.system(event)
	case "stream_event":
		if event["parent_tool_use_id"] == nil {
			a.stream(objectValue(event["event"]))
		}
	case "assistant":
		if event["parent_tool_use_id"] == nil {
			a.assistant(event)
		}
	case "user":
		if event["parent_tool_use_id"] == nil {
			a.user(event)
		}
	case "result":
		a.result(event)
	case "command_lifecycle":
		a.lifecycle(stringValue(event["command_uuid"]), stringValue(event["state"]))
	}
}

func (a *claudeApp) system(event map[string]any) {
	switch event["subtype"] {
	case "init":
		a.mu.Lock()
		if model := stringValue(event["model"]); model != "" {
			a.model = model
		}
		changed := false
		if mode := stringValue(event["permissionMode"]); mode != "" && mode != a.mode {
			a.mode, changed = mode, true
		}
		warn := !a.mcpWarned
		a.mcpWarned = true
		a.mu.Unlock()
		if changed {
			a.emit(map[string]any{"type": "permission_mode_changed", "mode": stringValue(event["permissionMode"])})
		}
		if warn {
			a.warnServers(anyList(event["mcp_servers"]))
		}
	case "status":
		if mode := stringValue(event["permissionMode"]); mode != "" {
			a.mu.Lock()
			changed := mode != a.mode
			a.mode = mode
			a.mu.Unlock()
			if changed {
				a.emit(map[string]any{"type": "permission_mode_changed", "mode": mode})
				a.saveSettings()
			}
		}
		switch stringValue(event["status"]) {
		case "requesting":
			var ids []string
			for _, id := range anyList(event["user_message_uuids"]) {
				ids = append(ids, stringValue(id))
			}
			a.turnStarted(ids)
		case "compacting":
			a.mu.Lock()
			start := !a.compacting
			a.compacting = true
			a.mu.Unlock()
			if start {
				a.emit(map[string]any{"type": "compaction_start", "reason": "manual"})
			}
		}
	case "compact_boundary":
		metadata := objectValue(event["compact_metadata"])
		a.mu.Lock()
		a.compacting = true
		a.compacted = map[string]any{"tokensBefore": metadata["pre_tokens"], "estimatedTokensAfter": metadata["post_tokens"], "trigger": metadata["trigger"]}
		a.mu.Unlock()
	case "api_retry":
		a.mu.Lock()
		a.retrying = true
		a.mu.Unlock()
		problem := firstText(stringValue(event["error"]), "error")
		if status, ok := event["error_status"].(float64); ok {
			problem = fmt.Sprintf("%s (%d)", problem, int(status))
		}
		a.emit(map[string]any{"type": "auto_retry_start", "attempt": event["attempt"], "maxAttempts": event["max_retries"], "delayMs": event["retry_delay_ms"], "errorMessage": problem})
	case "task_started", "task_progress", "task_updated", "task_notification", "background_tasks_changed":
		a.task(event)
	case "notification":
		level := "info"
		if priority := stringValue(event["priority"]); priority == "high" || priority == "immediate" {
			level = "warning"
		}
		a.notify(stringValue(event["text"]), level)
	case "model_fallback", "model_refusal_fallback", "model_refusal_no_fallback", "model_consent_fallback":
		a.notify(firstText(stringValue(event["message"]), stringValue(event["text"]), "Claude Code changed the model of this turn."), "warning")
	}
}

// warnServers tells the person about MCP servers that need attention, as the
// terminal of Claude Code does at its start.
func (a *claudeApp) warnServers(servers []any) {
	var lines []string
	for _, raw := range servers {
		server := objectValue(raw)
		name, status := stringValue(server["name"]), stringValue(server["status"])
		if name == "galpon" || status == "connected" || status == "pending" || status == "" || status == "disabled" {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s: %s", name, strings.ReplaceAll(status, "-", " ")))
	}
	if len(lines) > 0 {
		a.notify("MCP servers need attention:\n"+strings.Join(lines, "\n")+"\nRun /mcp in a terminal of this agent to fix them.", "warning")
	}
}

// turnStarted runs when Claude Code sends a model request. A turn starts at
// its first request; a later request can take in more messages.
func (a *claudeApp) turnStarted(ids []string) {
	a.mu.Lock()
	start := !a.streaming
	a.streaming = true
	var inputs []nativeEvent
	var shown []map[string]any
	if start {
		a.turn, a.lastAssistant = nil, ""
		if len(ids) == 0 {
			// A turn without a message, for example after a background task
			// ended. The runtime keeps it as its active turn.
			a.wakes++
			a.wake = fmt.Sprintf("claude-wake-%d", a.wakes)
			inputs = append(inputs, nativeEvent{Kind: "start", ID: a.wake})
		}
	}
	for _, id := range ids {
		if id == "" || a.announced[id] {
			continue
		}
		a.announced[id] = true
		a.turn = append(a.turn, id)
		prompt := a.prompts[id]
		text := ""
		if prompt != nil {
			text = prompt.Text
			prompt.Started = true
			prompt.Queued = false
			if message := shownPrompt(prompt); message != nil {
				prompt.Shown = true
				shown = append(shown, message)
			}
		}
		inputs = append(inputs, nativeEvent{Kind: "input", ID: id, Input: text})
	}
	a.mu.Unlock()
	if start {
		a.emit(map[string]any{"type": "agent_start"})
		go a.identity()
	}
	// Claude Code gives the message back only with the first token of the
	// answer, which can come seconds later. The tab shows the message when
	// the model request takes it, as Pi does.
	for _, message := range shown {
		a.emit(map[string]any{"type": "message_start", "message": message})
		a.emit(map[string]any{"type": "message_end", "message": message})
	}
	for _, event := range inputs {
		emit(a.ctx, a.options.Events, event)
	}
	a.emitQueue()
}

// shownPrompt gives the message that the tab shows for a prompt of the
// person or a delivery, or nil for a prompt that the tab does not show as
// a message, such as a slash command.
func shownPrompt(prompt *claudePrompt) map[string]any {
	at := time.Now().UnixMilli()
	if prompt.Delivery {
		return map[string]any{"role": "custom", "customType": "galpon-operation", "content": prompt.Display, "display": true, "timestamp": at}
	}
	if prompt.Content == nil || strings.HasPrefix(strings.TrimSpace(prompt.Text), "/") {
		return nil
	}
	return map[string]any{"role": "user", "content": claudeContent(prompt.Content), "timestamp": at}
}

func (a *claudeApp) stream(event map[string]any) {
	switch event["type"] {
	case "message_start":
		message := objectValue(event["message"])
		a.turnStarted(nil)
		a.mu.Lock()
		a.message = &claudeStream{id: stringValue(message["id"]), model: stringValue(message["model"]), blocks: map[int]*claudeBlock{},
			usage: objectValue(message["usage"]), at: time.Now().UnixMilli()}
		a.streamed[a.message.id] = true
		retried := a.retrying
		a.retrying = false
		a.mu.Unlock()
		if retried {
			a.emit(map[string]any{"type": "auto_retry_end", "success": true})
		}
		a.emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}, "timestamp": time.Now().UnixMilli()}})
	case "content_block_start":
		index := intValue(event["index"])
		block := objectValue(event["content_block"])
		a.mu.Lock()
		current := a.message
		if current == nil {
			a.mu.Unlock()
			return
		}
		value := &claudeBlock{kind: stringValue(block["type"]), id: stringValue(block["id"]), name: claudeToolName(stringValue(block["name"]))}
		current.blocks[index] = value
		current.order = append(current.order, index)
		a.mu.Unlock()
		switch value.kind {
		case "text":
			a.update(map[string]any{"type": "text_start", "contentIndex": index})
		case "thinking", "redacted_thinking":
			a.update(map[string]any{"type": "thinking_start", "contentIndex": index})
		case "tool_use", "server_tool_use":
			a.update(map[string]any{"type": "toolcall_start", "contentIndex": index, "id": value.id, "toolName": value.name})
		}
	case "content_block_delta":
		index := intValue(event["index"])
		delta := objectValue(event["delta"])
		a.mu.Lock()
		var block *claudeBlock
		if a.message != nil {
			block = a.message.blocks[index]
		}
		a.mu.Unlock()
		if block == nil {
			return
		}
		switch delta["type"] {
		case "text_delta":
			text := stringValue(delta["text"])
			block.text.WriteString(text)
			a.update(map[string]any{"type": "text_delta", "contentIndex": index, "delta": text})
		case "thinking_delta":
			text := stringValue(delta["thinking"])
			block.text.WriteString(text)
			a.update(map[string]any{"type": "thinking_delta", "contentIndex": index, "delta": text})
		case "input_json_delta":
			text := stringValue(delta["partial_json"])
			block.text.WriteString(text)
			a.update(map[string]any{"type": "toolcall_delta", "contentIndex": index, "delta": text})
		}
	case "content_block_stop":
		index := intValue(event["index"])
		a.mu.Lock()
		var block *claudeBlock
		if a.message != nil {
			block = a.message.blocks[index]
		}
		a.mu.Unlock()
		if block == nil {
			return
		}
		switch block.kind {
		case "text":
			a.update(map[string]any{"type": "text_end", "contentIndex": index, "content": block.text.String()})
		case "thinking", "redacted_thinking":
			a.update(map[string]any{"type": "thinking_end", "contentIndex": index, "content": block.text.String()})
		case "tool_use", "server_tool_use":
			a.update(map[string]any{"type": "toolcall_end", "contentIndex": index, "toolCall": map[string]any{"id": block.id, "name": block.name, "arguments": blockArguments(block)}})
		}
	case "message_delta":
		delta := objectValue(event["delta"])
		usage := objectValue(event["usage"])
		a.mu.Lock()
		if a.message != nil {
			if reason := stringValue(delta["stop_reason"]); reason != "" {
				a.message.stop = reason
			}
			if a.message.usage == nil {
				a.message.usage = map[string]any{}
			}
			for key, value := range usage {
				a.message.usage[key] = value
			}
		}
		a.mu.Unlock()
	case "message_stop":
		a.finishMessage()
	}
}

func intValue(value any) int { number, _ := value.(float64); return int(number) }

func blockArguments(block *claudeBlock) any {
	var value any
	if json.Unmarshal([]byte(block.text.String()), &value) != nil || value == nil {
		return map[string]any{}
	}
	return value
}

func (a *claudeApp) update(update map[string]any) {
	a.emit(map[string]any{"type": "message_update", "assistantMessageEvent": update})
}

// finishMessage sends the end of the streamed assistant message, then the
// start of each tool that it calls.
func (a *claudeApp) finishMessage() {
	a.mu.Lock()
	current := a.message
	a.message = nil
	if current == nil {
		a.mu.Unlock()
		return
	}
	sort.Ints(current.order)
	var content []any
	var tools []map[string]any
	for _, index := range current.order {
		block := current.blocks[index]
		switch block.kind {
		case "text":
			content = append(content, map[string]any{"type": "text", "text": block.text.String()})
		case "thinking":
			content = append(content, map[string]any{"type": "thinking", "thinking": block.text.String()})
		case "redacted_thinking":
			content = append(content, map[string]any{"type": "thinking", "thinking": "", "redacted": true})
		case "tool_use", "server_tool_use":
			arguments := blockArguments(block)
			content = append(content, map[string]any{"type": "toolCall", "id": block.id, "name": block.name, "arguments": arguments})
			tools = append(tools, map[string]any{"type": "tool_execution_start", "toolCallId": block.id, "toolName": block.name, "args": arguments})
			if a.calls[block.id] == nil {
				a.calls[block.id] = &claudeCall{name: block.name, args: arguments, at: time.Now()}
			}
		}
	}
	if content == nil {
		content = []any{}
	}
	stop := claudeStopReason(current.stop, len(tools) > 0)
	if current.stop == "" && a.interrupting {
		// The person stopped the run while the message streamed, so its
		// tool calls do not run.
		stop, tools = "aborted", nil
		a.abortShown = true
	}
	message := map[string]any{"role": "assistant", "content": content, "usage": claudeUsage(current.usage, 0),
		"stopReason": stop, "model": current.model, "provider": claudeProvider,
		"thinkingLevel": a.effort, "timestamp": current.at}
	a.mu.Unlock()
	a.emit(map[string]any{"type": "message_end", "message": message})
	for _, tool := range tools {
		a.emit(tool)
	}
}

// assistant handles a complete assistant record. A streamed message is drawn
// already; a message that did not stream, such as an error of the API, is
// drawn here.
func (a *claudeApp) assistant(event map[string]any) {
	message := objectValue(event["message"])
	// The output of a local command, such as /context, comes as an answer
	// that no model wrote.
	if run := objectValue(event["local_command_run"]); run != nil || event["local_command_source"] != nil {
		output, ok := claudeLocalOutput(stringValue(event["local_command_source"]))
		if !ok {
			output = contentText(message["content"])
		}
		shown := claudeCommandMessage(stringValue(run["command"]), stringValue(run["args"]), output, time.Now().UnixMilli())
		a.emit(map[string]any{"type": "message_start", "message": shown})
		a.emit(map[string]any{"type": "message_end", "message": shown})
		return
	}
	id := stringValue(message["id"])
	a.mu.Lock()
	a.lastAssistant = stringValue(event["uuid"])
	streamed := a.streamed[id]
	if streamed {
		// Claude Code asks for the permission of a call before the stream
		// of its block ends, so the call is known from this record.
		for _, raw := range anyList(message["content"]) {
			block := objectValue(raw)
			if kind := block["type"]; (kind == "tool_use" || kind == "server_tool_use") && a.calls[stringValue(block["id"])] == nil {
				a.calls[stringValue(block["id"])] = &claudeCall{name: claudeToolName(stringValue(block["name"])), args: block["input"], at: time.Now()}
			}
		}
	}
	a.mu.Unlock()
	if streamed {
		return
	}
	a.turnStarted(nil)
	failed := message["model"] == "<synthetic>" || event["error"] != nil
	content := []any{}
	calls := false
	for _, raw := range anyList(message["content"]) {
		if block := claudeAssistantBlock(objectValue(raw)); block != nil {
			content = append(content, block)
			calls = calls || block["type"] == "toolCall"
		}
	}
	out := map[string]any{"role": "assistant", "content": content, "usage": claudeUsage(objectValue(message["usage"]), 0),
		"stopReason": claudeStopReason(stringValue(message["stop_reason"]), calls), "model": stringValue(message["model"]),
		"provider": claudeProvider, "timestamp": time.Now().UnixMilli()}
	if failed {
		out["stopReason"], out["errorMessage"], out["content"] = "error", contentText(message["content"]), []any{}
	}
	a.emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}, "timestamp": out["timestamp"]}})
	a.emit(map[string]any{"type": "message_end", "message": out})
}

func (a *claudeApp) user(event map[string]any) {
	message := objectValue(event["message"])
	content := message["content"]
	var results []map[string]any
	for _, raw := range anyList(content) {
		if block := objectValue(raw); block["type"] == "tool_result" {
			results = append(results, block)
		}
	}
	if len(results) > 0 {
		details := claudeToolDetails(event["tool_use_result"])
		for _, block := range results {
			id := stringValue(block["tool_use_id"])
			a.mu.Lock()
			call := a.calls[id]
			name, duration := "", int64(0)
			if call != nil {
				name, duration, call.ended = call.name, time.Since(call.at).Milliseconds(), true
			}
			a.mu.Unlock()
			result := map[string]any{"content": claudeContent(block["content"])}
			if details != nil {
				result["details"] = details
			}
			a.emit(map[string]any{"type": "tool_execution_end", "toolCallId": id, "toolName": name, "result": result, "isError": block["is_error"] == true, "durationMs": duration})
		}
		return
	}
	text := contentText(content)
	if output, ok := claudeLocalOutput(text); ok {
		if output != "" && !strings.HasPrefix(output, "Set model to") && output != "Compacted" {
			a.notify(output, "info")
		}
		return
	}
	if event["isCompactSummary"] == true || strings.HasPrefix(text, "This session is being continued from a previous conversation") {
		a.endCompaction(text)
		return
	}
	if event["isReplay"] != true || strings.HasPrefix(strings.TrimSpace(text), "<command-name>") || strings.HasPrefix(strings.TrimSpace(text), "<local-command-caveat>") {
		return
	}
	id := stringValue(event["uuid"])
	a.mu.Lock()
	prompt := a.prompts[id]
	early := false
	if prompt != nil {
		prompt.Queued = false
		early = prompt.Shown
	}
	a.mu.Unlock()
	if early {
		a.emitQueue()
		return
	}
	at := time.Now().UnixMilli()
	// Prompts submitted by the person keep their user role, even when they
	// contain a task-notification example. Internal replays have no prompt.
	if prompt == nil {
		if id, notice := claudeTaskEnd(text, at); notice != nil {
			a.emitTaskNotice(id, notice)
			return
		}
	}
	var shown map[string]any
	if prompt != nil && prompt.Delivery {
		shown = map[string]any{"role": "custom", "customType": "galpon-operation", "content": prompt.Display, "display": true, "timestamp": at}
	} else if display, ok := nativeDeliveryDisplay(text); ok {
		shown = map[string]any{"role": "custom", "customType": "galpon-operation", "content": display, "display": true, "timestamp": at}
	} else {
		shown = map[string]any{"role": "user", "content": claudeContent(content), "timestamp": at}
	}
	a.emit(map[string]any{"type": "message_start", "message": shown})
	a.emit(map[string]any{"type": "message_end", "message": shown})
	a.emitQueue()
}

func (a *claudeApp) endCompaction(summary string) {
	a.mu.Lock()
	compacted := a.compacted
	pending := a.compacting
	a.compacting, a.compacted = false, nil
	a.mu.Unlock()
	if !pending {
		return
	}
	result := map[string]any{"summary": claudeSummary(summary)}
	for key, value := range compacted {
		result[key] = value
	}
	a.emit(map[string]any{"type": "compaction_end", "result": result})
	go a.refreshContext()
}

func (a *claudeApp) result(event map[string]any) {
	a.finishMessage()
	a.endCompaction("")
	a.mu.Lock()
	streaming := a.streaming
	retried := a.retrying
	interrupted := a.interrupting
	a.streaming, a.retrying, a.interrupting = false, false, false
	cost, _ := event["total_cost_usd"].(float64)
	delta := cost - a.cost
	if delta < 0 {
		delta = cost
	}
	if cost > 0 {
		a.cost = cost
	}
	for model, raw := range objectValue(event["modelUsage"]) {
		if window, ok := objectValue(raw)["contextWindow"].(float64); ok && window > 0 {
			a.windows[model] = window
		}
	}
	ids := append([]string(nil), a.turn...)
	for _, raw := range anyList(event["user_message_uuids"]) {
		if id := stringValue(raw); a.announced[id] && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	wake := a.wake
	last := a.lastAssistant
	a.turn, a.wake, a.lastAssistant = nil, "", ""
	duration, _ := event["duration_ms"].(float64)
	if last != "" && delta > 0 {
		a.costs[last] = claudeTurnCost{Cost: delta, DurationMS: int64(duration)}
	}
	costs := a.costs
	a.mu.Unlock()
	if last != "" && delta > 0 {
		a.mu.Lock()
		data, err := json.Marshal(costs)
		a.mu.Unlock()
		if err == nil {
			_ = atomicFile(a.costsPath(), append(data, '\n'))
		}
	}
	failure := ""
	subtype := stringValue(event["subtype"])
	if event["is_error"] == true || subtype != "" && subtype != "success" {
		failure = "Claude Code failed: " + firstText(subtype, "error")
		if interrupted {
			failure = "Claude Code was interrupted"
		}
	}
	if retried {
		a.emit(map[string]any{"type": "auto_retry_end", "success": failure == "", "attempt": 0, "finalError": failure})
	}
	if streaming {
		switch {
		case interrupted:
			// A message that streamed when the person stopped the run ends as
			// stopped already. Otherwise an empty message shows the stop.
			a.mu.Lock()
			shown := a.abortShown
			a.abortShown = false
			a.mu.Unlock()
			if !shown {
				stopped := map[string]any{"role": "assistant", "content": []any{}, "stopReason": "aborted", "usage": claudeUsage(nil, 0), "provider": claudeProvider, "timestamp": time.Now().UnixMilli()}
				a.emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}}})
				a.emit(map[string]any{"type": "message_end", "message": stopped})
			}
		case failure != "":
			text := firstText(claudeErrors(event), stringValue(event["result"]), failure)
			a.notify(text, "error")
		}
		a.emit(map[string]any{"type": "turn_cost", "cost": delta})
		a.emit(map[string]any{"type": "agent_end", "messages": []any{}})
		a.emit(map[string]any{"type": "agent_settled"})
	}
	final := stringValue(event["result"])
	for _, id := range ids {
		emit(a.ctx, a.options.Events, nativeEvent{Kind: "finish", ID: id, Final: final, Failure: failure})
	}
	if wake != "" {
		emit(a.ctx, a.options.Events, nativeEvent{Kind: "finish", ID: wake})
	}
	go a.refreshContext()
	go a.refreshWork()
}

func claudeErrors(event map[string]any) string {
	var lines []string
	for _, raw := range anyList(event["errors"]) {
		if text := stringValue(raw); text != "" {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, "\n")
}

func (a *claudeApp) lifecycle(id, state string) {
	a.mu.Lock()
	prompt := a.prompts[id]
	changed := false
	if prompt != nil {
		switch state {
		case "queued":
			if a.streaming && !prompt.Started {
				prompt.Queued, changed = true, true
			}
		case "started", "completed", "cancelled":
			changed = prompt.Queued
			prompt.Queued = false
			if state == "started" {
				prompt.Started = true
			}
		}
	}
	a.mu.Unlock()
	if changed {
		a.emitQueue()
	}
}

// emitQueue sends the messages of the person that wait for the run, when
// they changed.
func (a *claudeApp) emitQueue() {
	a.mu.Lock()
	steering, followUp := []string{}, []string{}
	for _, prompt := range a.queuedLocked() {
		if prompt.Priority == "next" {
			steering = append(steering, prompt.Text)
		} else {
			followUp = append(followUp, prompt.Text)
		}
	}
	key := strings.Join(steering, "\x00") + "\x01" + strings.Join(followUp, "\x00")
	changed := key != a.queueKey
	a.queueKey = key
	a.mu.Unlock()
	if changed {
		a.emit(map[string]any{"type": "queue_update", "steering": steering, "followUp": followUp})
	}
}

func (a *claudeApp) queuedLocked() []*claudePrompt {
	var out []*claudePrompt
	for _, prompt := range a.prompts {
		if prompt.Queued && !prompt.Delivery {
			out = append(out, prompt)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sent < out[j].Sent })
	return out
}

// --- background tasks

func (a *claudeApp) task(event map[string]any) {
	now := time.Now().UnixMilli()
	a.mu.Lock()
	switch event["subtype"] {
	case "background_tasks_changed":
		live := map[string]bool{}
		for _, raw := range anyList(event["tasks"]) {
			item := objectValue(raw)
			id := stringValue(item["task_id"])
			live[id] = true
			row := a.taskRowLocked(id, now)
			row["kind"] = claudeTaskKind(stringValue(item["task_type"]))
			if description := stringValue(item["description"]); description != "" {
				row["description"] = description
			}
		}
		for id, row := range a.tasks {
			if !live[id] && row["status"] == "running" {
				row["status"], row["endedAt"] = "completed", now
			}
		}
	default:
		id := stringValue(event["task_id"])
		if id == "" {
			a.mu.Unlock()
			return
		}
		row := a.taskRowLocked(id, now)
		if kind := stringValue(event["task_type"]); kind != "" {
			row["kind"] = claudeTaskKind(kind)
		}
		if description := stringValue(event["description"]); description != "" {
			row["description"] = description
		}
		if prompt := firstText(stringValue(event["command"]), stringValue(event["prompt"])); prompt != "" {
			row["command"] = firstLine(prompt)
		}
		if summary := firstText(stringValue(event["summary"]), stringValue(event["last_tool_name"])); summary != "" {
			row["tail"] = []string{firstLine(summary)}
		}
		if path := stringValue(event["output_file"]); path != "" {
			row["outputPath"] = path
		}
		status := stringValue(event["status"])
		if patch := objectValue(event["patch"]); patch != nil {
			status = firstText(stringValue(patch["status"]), status)
			if end, ok := patch["end_time"].(float64); ok {
				row["endedAt"] = int64(end)
			}
		}
		switch status {
		case "":
		case "running", "pending":
			row["status"] = "running"
		case "completed", "done":
			row["status"] = "completed"
		case "killed", "stopped", "cancelled":
			row["status"] = "stopped"
		default:
			row["status"] = "failed"
		}
		if row["status"] != "running" && row["endedAt"] == nil {
			row["endedAt"] = now
		}
		if event["subtype"] == "task_notification" {
			// The end of a task is a notice in the conversation, as Claude
			// Code tells it to the model.
			kind := stringValue(row["kind"])
			if kind != "agent" {
				kind = "command"
			}
			notice := claudeTaskMessage(id, kind, firstText(stringValue(row["description"]), stringValue(row["command"])), status, stringValue(event["summary"]), now)
			a.mu.Unlock()
			a.emitTaskNotice(id, notice)
			a.publishTasks()
			return
		}
	}
	a.mu.Unlock()
	a.publishTasks()
}

// Claude can report the same completion as both a system event and a
// replayed user event. The conversation gets one entry for that task.
func (a *claudeApp) emitTaskNotice(id string, notice map[string]any) {
	a.mu.Lock()
	if a.taskNotices == nil {
		a.taskNotices = map[string]bool{}
	}
	seen := a.taskNotices[id]
	a.taskNotices[id] = true
	a.mu.Unlock()
	if seen {
		return
	}
	a.emit(map[string]any{"type": "message_start", "message": notice})
	a.emit(map[string]any{"type": "message_end", "message": notice})
}

func (a *claudeApp) taskRowLocked(id string, now int64) map[string]any {
	row := a.tasks[id]
	if row == nil {
		row = map[string]any{"id": id, "kind": "command", "description": "", "command": "", "status": "running", "startedAt": now, "tail": []string{}, "outputPath": ""}
		a.tasks[id] = row
	}
	return row
}

func claudeTaskKind(kind string) string {
	switch {
	case strings.Contains(kind, "agent"):
		return "agent"
	case strings.Contains(kind, "monitor"):
		return "monitor"
	}
	return "command"
}

func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > 160 {
				line = line[:160]
			}
			return line
		}
	}
	return ""
}

func (a *claudeApp) publishTasks() {
	a.mu.Lock()
	rows := make([]map[string]any, 0, len(a.tasks))
	for _, row := range a.tasks {
		copied := map[string]any{}
		for key, value := range row {
			copied[key] = value
		}
		rows = append(rows, copied)
	}
	a.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i]["startedAt"]) < fmt.Sprint(rows[j]["startedAt"]) })
	a.publish("tasks", map[string]any{"tasks": rows})
}

// --- Galpon facts

func (a *claudeApp) refreshContext() {
	usage, err := a.control(map[string]any{"subtype": "get_context_usage"}, 20*time.Second)
	if err != nil {
		return
	}
	total, _ := usage["totalTokens"].(float64)
	window, _ := usage["maxTokens"].(float64)
	percent := 0.0
	if window > 0 {
		percent = total * 100 / window
	}
	a.mu.Lock()
	a.contextUsage = map[string]any{"tokens": total, "contextWindow": window, "percent": percent}
	if model := stringValue(usage["model"]); model != "" && window > 0 {
		a.windows[model] = window
	}
	a.mu.Unlock()
	a.emit(map[string]any{"type": "context_usage", "contextUsage": a.contextUsage})
}

// claudeSavedCost reads the cost that the saved conversation reports, from
// the last cost record of its end.
func claudeSavedCost(path string) float64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return 0
	}
	const tail = 512 << 10
	offset := max(0, info.Size()-tail)
	data := make([]byte, info.Size()-offset)
	if _, err := file.ReadAt(data, offset); err != nil {
		return 0
	}
	cost := 0.0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.Contains(line, []byte(`"cost-state"`)) {
			continue
		}
		var record map[string]any
		if json.Unmarshal(line, &record) == nil {
			if value, ok := record["totalCostUSD"].(float64); ok {
				cost = value
			}
		}
	}
	return cost
}

// --- the commands of the tab

func (a *claudeApp) commandsLoop() {
	reader := bufio.NewReaderSize(a.options.Input, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			a.command(line)
		}
		if err != nil {
			// The app host closed the pipe: it ends the agent.
			if a.stop != nil {
				a.stop()
			}
			return
		}
	}
}

func (a *claudeApp) command(line []byte) {
	var command map[string]any
	if json.Unmarshal(line, &command) != nil {
		return
	}
	id, kind := stringValue(command["id"]), stringValue(command["type"])
	switch kind {
	case "extension_ui_response":
		a.answer(command)
		return
	case "prompt", "steer", "follow_up":
		// Prompts go to Claude Code in their order.
		a.respond(id, kind, nil, a.prompt(command, kind))
		return
	}
	go func() {
		data, err := a.call(kind, command)
		a.respond(id, kind, data, err)
	}()
}

func (a *claudeApp) prompt(command map[string]any, kind string) error {
	text := stringValue(command["message"])
	if name, args, ok := slashCommand(text); ok {
		if claudeBlockedCommands[name] {
			return fmt.Errorf("/%s would end the conversation of this Galpon agent; create another agent for a new conversation", name)
		}
		if name == "galpon-app-stop" {
			_, err := a.control(map[string]any{"subtype": "stop_task", "task_id": strings.TrimSpace(args)}, 10*time.Second)
			return err
		}
		// The effort of the tab is the effort of the next runs, so /effort
		// with a level changes it as the effort control does.
		if level := strings.TrimSpace(args); name == "effort" && level != "" && !strings.Contains(level, " ") {
			a.mu.Lock()
			known := slices.Contains(a.levelsLocked(), level)
			a.mu.Unlock()
			if known {
				_, err := a.call("set_thinking_level", map[string]any{"level": level})
				if err == nil {
					a.notify("Effort: "+level, "info")
				}
				return err
			}
		}
	}
	a.mu.Lock()
	working := a.streaming
	a.mu.Unlock()
	// While Claude Code works, "next" gives the message to the model at the
	// next step of the run, as Pi steers, and "later" keeps it for after the
	// run. "now" would stop the run, with the permission prompts it waits on.
	priority := ""
	if working {
		priority = "later"
		if kind == "steer" || kind == "prompt" && stringValue(command["streamingBehavior"]) != "followUp" {
			priority = "next"
		}
	}
	var content any = text
	if images := anyList(command["images"]); len(images) > 0 {
		blocks := []any{map[string]any{"type": "text", "text": text}}
		for _, raw := range images {
			image := objectValue(raw)
			if data := stringValue(image["data"]); data != "" {
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": firstText(stringValue(image["mimeType"]), "image/png"), "data": data}})
			}
		}
		content = blocks
	}
	id := uuid.NewString()
	message := map[string]any{"type": "user", "uuid": id, "session_id": a.driver.id, "parent_tool_use_id": nil,
		"message": map[string]any{"role": "user", "content": content}, "origin": map[string]any{"kind": "human"}}
	if priority != "" {
		message["priority"] = priority
	}
	a.mu.Lock()
	a.prompts[id] = &claudePrompt{Sent: time.Now().UnixNano(), Text: text, Priority: priority, Content: content}
	a.mu.Unlock()
	return a.driver.send(message)
}

// slashCommand reads "/name args".
func slashCommand(text string) (name, args string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || strings.Contains(strings.SplitN(text, "\n", 2)[0], "/") && strings.Count(strings.Fields(text)[0], "/") > 1 {
		return "", "", false
	}
	name, args, _ = strings.Cut(strings.TrimPrefix(text, "/"), " ")
	return strings.ToLower(name), args, name != ""
}

// deliver sends a Galpon delivery of the runtime to Claude Code.
func (a *claudeApp) deliver(input nativeInput, token string) error {
	display, ok := nativeDeliveryDisplay(input.Text)
	if !ok {
		display = input.Text
	}
	var content any = input.Text
	if len(input.Images) > 0 {
		blocks := []any{map[string]any{"type": "text", "text": input.Text}}
		for _, image := range input.Images {
			if image.Data == "" {
				return fmt.Errorf("%w: image %s has no saved data", errSubmissionRejected, image.ID)
			}
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": image.MimeType, "data": image.Data}})
		}
		content = blocks
	}
	a.mu.Lock()
	a.prompts[token] = &claudePrompt{Text: input.Text, Display: display, Delivery: true, Content: content}
	a.mu.Unlock()
	return a.driver.send(map[string]any{"type": "user", "uuid": token, "session_id": a.driver.id, "parent_tool_use_id": nil, "message": map[string]any{"role": "user", "content": content}})
}

func (a *claudeApp) call(kind string, command map[string]any) (any, error) {
	switch kind {
	case "abort":
		a.mu.Lock()
		working := a.streaming
		if working {
			a.interrupting = true
		}
		a.mu.Unlock()
		if !working {
			return nil, nil
		}
		_, err := a.control(map[string]any{"subtype": "interrupt"}, 20*time.Second)
		return nil, err
	case "clear_queue":
		a.mu.Lock()
		queued := map[string]*claudePrompt{}
		for id, prompt := range a.prompts {
			if prompt.Queued && !prompt.Delivery {
				queued[id] = prompt
			}
		}
		a.mu.Unlock()
		steering, followUp := []string{}, []string{}
		for id, prompt := range queued {
			response, err := a.control(map[string]any{"subtype": "cancel_async_message", "message_uuid": id}, 10*time.Second)
			if err != nil || response["cancelled"] != true {
				continue
			}
			a.mu.Lock()
			prompt.Queued = false
			a.mu.Unlock()
			if prompt.Priority == "next" {
				steering = append(steering, prompt.Text)
			} else {
				followUp = append(followUp, prompt.Text)
			}
		}
		a.emitQueue()
		return map[string]any{"steering": steering, "followUp": followUp}, nil
	case "get_state":
		a.waitReady()
		return a.state(), nil
	case "get_messages":
		a.mu.Lock()
		costs := map[string]claudeTurnCost{}
		for key, value := range a.costs {
			costs[key] = value
		}
		a.mu.Unlock()
		path := a.driver.path
		if found, err := a.driver.findTranscript(); err == nil && found != "" {
			path = found
		}
		messages, _, err := claudeHistory(path, claudeHistoryLimit, costs)
		if err != nil {
			return nil, err
		}
		return map[string]any{"messages": messages}, nil
	case "get_session_stats":
		a.mu.Lock()
		defer a.mu.Unlock()
		return map[string]any{"sessionId": a.driver.id, "sessionFile": a.driver.path, "cost": a.cost, "contextUsage": a.contextUsage}, nil
	case "get_commands":
		a.waitReady()
		a.mu.Lock()
		defer a.mu.Unlock()
		return map[string]any{"commands": append([]any(nil), a.commands...)}, nil
	case "get_available_models":
		a.waitReady()
		a.mu.Lock()
		defer a.mu.Unlock()
		models := []any{}
		seen := map[string]bool{}
		for _, item := range a.models {
			if item.Value == "default" || seen[item.Resolved] {
				continue
			}
			seen[item.Resolved] = true
			models = append(models, a.modelInfoLocked(item))
		}
		return map[string]any{"models": models}, nil
	case "set_model", "cycle_model":
		target := stringValue(command["modelId"])
		if kind == "cycle_model" {
			target = a.nextModel()
		}
		if target == "" {
			return nil, errors.New("choose a model")
		}
		response, err := a.control(map[string]any{"subtype": "set_model", "model": target}, 20*time.Second)
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.model = firstText(stringValue(response["model"]), target)
		info := a.currentModelLocked()
		levels := a.levelsLocked()
		if len(levels) > 0 && !slices.Contains(levels, a.effort) {
			a.effort = levels[min(1, len(levels)-1)]
		}
		a.mu.Unlock()
		a.saveSettings()
		go a.refreshContext()
		if kind == "cycle_model" {
			return map[string]any{"model": info, "thinkingLevel": a.effort}, nil
		}
		return info, nil
	case "get_available_thinking_levels":
		a.waitReady()
		a.mu.Lock()
		defer a.mu.Unlock()
		return map[string]any{"levels": a.levelsLocked()}, nil
	case "set_thinking_level", "cycle_thinking_level":
		level := stringValue(command["level"])
		a.mu.Lock()
		levels := a.levelsLocked()
		if kind == "cycle_thinking_level" && len(levels) > 0 {
			index := slices.Index(levels, a.effort)
			level = levels[(index+1)%len(levels)]
		}
		a.mu.Unlock()
		if level == "" || len(levels) > 0 && !slices.Contains(levels, level) {
			return nil, fmt.Errorf("this model has no %q effort", level)
		}
		if _, err := a.control(map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"effortLevel": level}}, 10*time.Second); err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.effort = level
		a.mu.Unlock()
		a.saveSettings()
		a.emit(map[string]any{"type": "thinking_level_changed", "level": level})
		if kind == "cycle_thinking_level" {
			return map[string]any{"level": level}, nil
		}
		return nil, nil
	case "set_permission_mode":
		mode := stringValue(command["mode"])
		response, err := a.control(map[string]any{"subtype": "set_permission_mode", "mode": mode}, 10*time.Second)
		if err != nil {
			return nil, err
		}
		mode = firstText(stringValue(response["mode"]), mode)
		a.mu.Lock()
		a.mode = mode
		a.mu.Unlock()
		a.saveSettings()
		a.emit(map[string]any{"type": "permission_mode_changed", "mode": mode})
		return map[string]any{"mode": mode}, nil
	case "compact":
		text := strings.TrimSpace("/compact " + stringValue(command["customInstructions"]))
		id := uuid.NewString()
		a.mu.Lock()
		a.prompts[id] = &claudePrompt{Text: text}
		a.mu.Unlock()
		return nil, a.driver.send(map[string]any{"type": "user", "uuid": id, "session_id": a.driver.id, "parent_tool_use_id": nil,
			"message": map[string]any{"role": "user", "content": text}, "origin": map[string]any{"kind": "human"}})
	case "set_session_name":
		return nil, nil
	case "new_session", "switch_session", "fork", "clone":
		return nil, errors.New("this Claude Code conversation belongs to its Galpon agent; create another agent for a new conversation")
	case "bash":
		return nil, errors.New("run shell commands in a terminal of this agent")
	}
	return nil, fmt.Errorf("unknown command %q", kind)
}

func (a *claudeApp) state() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending := 0
	for _, prompt := range a.prompts {
		if prompt.Queued {
			pending++
		}
	}
	return map[string]any{"model": a.currentModelLocked(), "thinkingLevel": a.effort, "isStreaming": a.streaming, "isCompacting": a.compacting,
		"sessionId": a.driver.id, "sessionFile": a.driver.path, "sessionName": "", "autoCompactionEnabled": true,
		"pendingMessageCount": pending, "permissionMode": a.mode, "harness": model.HarnessClaude}
}

func (a *claudeApp) modelInfoLocked(item claudeModel) map[string]any {
	window := a.windows[item.Resolved]
	if window == 0 {
		window = 200000
	}
	detail := item.Description
	if _, rest, ok := strings.Cut(detail, " · "); ok {
		detail = rest
	}
	info := map[string]any{"id": item.Resolved, "name": item.Name, "provider": claudeProvider, "alias": item.Value, "description": detail,
		"reasoning": len(item.Efforts) > 0, "input": []string{"text", "image"}, "contextWindow": window}
	// Claude Code gives the price in the description: "$4/$20 per Mtok".
	if match := modelPrice.FindStringSubmatch(item.Description); match != nil {
		input, inputErr := strconv.ParseFloat(match[1], 64)
		output, outputErr := strconv.ParseFloat(match[2], 64)
		if inputErr == nil && outputErr == nil {
			info["cost"] = map[string]any{"input": input, "output": output}
		}
	}
	return info
}

var modelPrice = regexp.MustCompile(`\$([0-9.]+)/\$([0-9.]+) per Mtok`)

func (a *claudeApp) currentModelLocked() map[string]any {
	if a.model == "" {
		return nil
	}
	for _, item := range a.models {
		if item.Value != "default" && (item.Resolved == a.model || item.Value == a.model) {
			return a.modelInfoLocked(item)
		}
	}
	return a.modelInfoLocked(claudeModel{Value: a.model, Resolved: a.model, Name: claudeModelName(a.model, "", "")})
}

func (a *claudeApp) levelsLocked() []string {
	for _, item := range a.models {
		if item.Resolved == a.model || item.Value == a.model {
			return item.Efforts
		}
	}
	return []string{"low", "medium", "high", "xhigh", "max"}
}

func (a *claudeApp) nextModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var choices []string
	for _, item := range a.models {
		if item.Value != "default" && !slices.Contains(choices, item.Resolved) {
			choices = append(choices, item.Resolved)
		}
	}
	if len(choices) == 0 {
		return ""
	}
	return choices[(slices.Index(choices, a.model)+1)%len(choices)]
}
