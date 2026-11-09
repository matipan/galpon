package nativeagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/model"
)

// The app view of a Codex agent.
//
// The app host of the Herdr app speaks the RPC protocol of Pi with the native
// runtime (see claudeApp). For a Codex agent the runtime runs the Codex
// app-server, and codexApp translates: the notifications of the thread become
// the events of Pi that the agent tab draws, and the commands of the tab
// become turns, steers, interrupts and thread settings. Approvals and
// questions of Codex become dialogs of the tab.

// codexModel is a model that Codex offers.
type codexModel struct {
	ID, Name, Description, DefaultEffort string
	Efforts, Inputs                      []string
}

// codexAppSettings are the choices of the person in the tab: the model, the
// effort and the mode. Galpon saves them, and the next start of the agent
// uses them again.
type codexAppSettings struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Mode   string `json:"mode,omitempty"`
}

// codexModes are the modes of a Codex agent in the tab: its sandbox, its
// approvals and its plan mode. Shift+Tab goes through the modes that are not
// menuOnly. The tone is the color of the mode chip.
var codexModes = []map[string]any{
	{"mode": "workspace", "label": "Workspace", "icon": "square-pen", "tone": "", "detail": "Works in its folders. Asks for more"},
	{"mode": "plan", "label": "Plan", "icon": "list-todo", "tone": "blue", "detail": "Plans and asks. Changes nothing"},
	{"mode": "read-only", "label": "Read only", "icon": "shield", "tone": "", "detail": "Reads. Asks before any change"},
	{"mode": "full-access", "label": "Full access", "icon": "alert", "tone": "red", "detail": "Never asks. Use it with care", "menuOnly": true},
}

// codexOpen is the assistant message that streams now: a message or the
// reasoning of Codex.
type codexOpen struct {
	itemID, kind string
	text         strings.Builder
	at           int64
}

type codexApp struct {
	appBridge
	stop   context.CancelFunc
	driver *codexDriver
	dir    string
	ready  chan struct{}

	mu            sync.Mutex
	readyDone     bool
	models        []codexModel
	model         string
	effort        string
	approval      any
	sandbox       map[string]any
	workspace     map[string]any
	collaboration string
	contextUsage  map[string]any
	window        float64
	streaming     bool
	turnID        string
	open          *codexOpen
	items         map[string]map[string]any
	started       map[string]time.Time
	outputs       map[string]*strings.Builder
	dialogs       map[string]*codexDialog
	nextDialog    int
	retrying      bool
	retries       int
	queueKey      string
	compacting    bool
	compactedFrom float64
	warned        map[string]bool
	// anchors counts the anchors of each turn; see codexAnchors.
	anchors map[string]int
	// usage is the tokens of the turn that runs now.
	usage map[string]float64
	// starts keeps the place of a tool item among the anchors of its turn
	// when it starts; see journal.
	starts map[string]int
	// tasks are the commands that run on after their turn, by item ID,
	// and background their items.
	tasks      map[string]map[string]any
	background map[string]map[string]any
}

func newCodexApp(ctx context.Context, options launchOptions, driver *codexDriver) *codexApp {
	return &codexApp{appBridge: appBridge{ctx: ctx, options: options, harness: model.HarnessCodex}, stop: options.Stop, driver: driver,
		dir: filepath.Join(options.Config.StateDir, "agents", options.Agent.ID, "app"), ready: make(chan struct{}),
		items: map[string]map[string]any{}, started: map[string]time.Time{}, outputs: map[string]*strings.Builder{},
		dialogs: map[string]*codexDialog{}, warned: map[string]bool{}, anchors: map[string]int{}, starts: map[string]int{},
		tasks: map[string]map[string]any{}, background: map[string]map[string]any{}, collaboration: "default"}
}

func (a *codexApp) settingsPath() string { return filepath.Join(a.dir, "codex.json") }
func (a *codexApp) stepsPath() string    { return filepath.Join(a.dir, "codex-steps.jsonl") }

// threadReady reads the settings of the thread from the answer of
// thread/start or thread/resume.
func (a *codexApp) threadReady(result json.RawMessage) {
	var response map[string]any
	if json.Unmarshal(result, &response) != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = stringValue(response["model"])
	a.effort = stringValue(response["reasoningEffort"])
	a.approval = response["approvalPolicy"]
	a.setSandboxLocked(objectValue(response["sandbox"]))
	if mode := stringValue(objectValue(response["collaborationMode"])["mode"]); mode != "" {
		a.collaboration = mode
	}
}

func (a *codexApp) setSandboxLocked(sandbox map[string]any) {
	if sandbox == nil {
		return
	}
	a.sandbox = sandbox
	if sandbox["type"] == "workspaceWrite" {
		// The workspace mode gives back these folders, with the Galpon folder.
		a.workspace = sandbox
	}
}

// start loads the models, gives the thread the saved choices of the person,
// then reads the commands of the tab.
func (a *codexApp) start() {
	go func() {
		a.loadModels()
		a.restoreSettings()
		a.markReady()
		a.identity()
		a.publish("tasks", map[string]any{"tasks": []any{}})
	}()
	go a.commandsLoop()
	go a.workLoop()
}

func (a *codexApp) markReady() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.readyDone {
		a.readyDone = true
		close(a.ready)
	}
}

func (a *codexApp) waitReady() {
	select {
	case <-a.ready:
	case <-a.ctx.Done():
	case <-time.After(30 * time.Second):
	}
}

func (a *codexApp) call(method string, params any) (json.RawMessage, error) {
	return a.driver.call(a.ctx, method, params)
}

func (a *codexApp) loadModels() {
	result, err := a.call("model/list", map[string]any{"includeHidden": false})
	if err != nil {
		return
	}
	var response struct {
		Data []struct {
			ID                        string `json:"id"`
			Model                     string `json:"model"`
			DisplayName               string `json:"displayName"`
			Description               string `json:"description"`
			Hidden                    bool   `json:"hidden"`
			DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
			InputModalities           []string
			SupportedReasoningEfforts []struct {
				ReasoningEffort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
	}
	if json.Unmarshal(result, &response) != nil {
		return
	}
	var models []codexModel
	for _, item := range response.Data {
		if item.Hidden {
			continue
		}
		value := codexModel{ID: firstText(item.Model, item.ID), Name: firstText(item.DisplayName, item.ID), Description: item.Description,
			DefaultEffort: item.DefaultReasoningEffort, Inputs: item.InputModalities}
		for _, effort := range item.SupportedReasoningEfforts {
			value.Efforts = append(value.Efforts, effort.ReasoningEffort)
		}
		if len(value.Inputs) == 0 {
			value.Inputs = []string{"text", "image"}
		}
		models = append(models, value)
	}
	a.mu.Lock()
	a.models = models
	a.mu.Unlock()
}

func (a *codexApp) restoreSettings() {
	var settings codexAppSettings
	data, err := os.ReadFile(a.settingsPath())
	if err != nil || json.Unmarshal(data, &settings) != nil {
		return
	}
	params := map[string]any{}
	a.mu.Lock()
	if settings.Model != "" && settings.Model != a.model {
		params["model"] = settings.Model
	}
	if settings.Effort != "" && settings.Effort != a.effort {
		params["effort"] = settings.Effort
	}
	a.mu.Unlock()
	if settings.Mode != "" && settings.Mode != a.mode() {
		for key, value := range a.modeParams(settings.Mode) {
			params[key] = value
		}
	}
	if len(params) == 0 {
		return
	}
	if err := a.updateSettings(params); err != nil {
		a.notify("Galpon could not give Codex the model, the effort or the mode of the last run: "+err.Error(), "warning")
	}
}

func (a *codexApp) saveSettings() {
	mode := a.mode()
	a.mu.Lock()
	settings := codexAppSettings{Model: a.model, Effort: a.effort, Mode: mode}
	a.mu.Unlock()
	if settings.Mode == "full-access" {
		// Full access is a choice for one run, never the start of the next.
		settings.Mode = ""
	}
	_ = atomicJSON(a.settingsPath(), settings)
}

// updateSettings changes the settings of the thread for the next turns, and
// keeps them.
func (a *codexApp) updateSettings(params map[string]any) error {
	params["threadId"] = a.driver.id
	if _, err := a.call("thread/settings/update", params); err != nil {
		return err
	}
	a.mu.Lock()
	if value, ok := params["model"].(string); ok {
		a.model = value
	}
	if value, ok := params["effort"].(string); ok {
		a.effort = value
	}
	if value, ok := params["approvalPolicy"]; ok {
		a.approval = value
	}
	if value := objectValue(params["sandboxPolicy"]); value != nil {
		a.setSandboxLocked(value)
	}
	if value := objectValue(params["collaborationMode"]); value != nil {
		a.collaboration = firstText(stringValue(value["mode"]), "default")
	}
	a.mu.Unlock()
	return nil
}

// mode gives the mode of the tab for the settings of the thread.
func (a *codexApp) mode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collaboration == "plan" {
		return "plan"
	}
	switch stringValue(a.sandbox["type"]) {
	case "dangerFullAccess":
		return "full-access"
	case "readOnly":
		return "read-only"
	}
	return "workspace"
}

// modeParams gives the thread settings of a mode.
func (a *codexApp) modeParams(mode string) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	collaboration := func(kind string) map[string]any {
		return map[string]any{"mode": kind, "settings": map[string]any{"model": a.model, "reasoning_effort": nil, "developer_instructions": nil}}
	}
	approval := a.approval
	if approval == "never" || approval == nil {
		approval = "on-request"
	}
	switch mode {
	case "plan":
		return map[string]any{"collaborationMode": collaboration("plan")}
	case "read-only":
		return map[string]any{"collaborationMode": collaboration("default"), "approvalPolicy": approval,
			"sandboxPolicy": map[string]any{"type": "readOnly", "networkAccess": false}}
	case "full-access":
		return map[string]any{"collaborationMode": collaboration("default"), "approvalPolicy": "never", "sandboxPolicy": map[string]any{"type": "dangerFullAccess"}}
	}
	workspace := a.workspace
	if workspace == nil {
		roots := []any{}
		if a.options.Folder != "" {
			roots = append(roots, a.options.Folder)
		}
		workspace = map[string]any{"type": "workspaceWrite", "writableRoots": roots, "networkAccess": false, "excludeTmpdirEnvVar": false, "excludeSlashTmp": false}
	}
	return map[string]any{"collaborationMode": collaboration("default"), "approvalPolicy": approval, "sandboxPolicy": workspace}
}

// --- the notifications of Codex

// notification handles one notification of the thread. Notifications come in
// order, from the reader of the connection, so it must not wait for Codex.
func (a *codexApp) notification(method string, params map[string]any) {
	switch method {
	case "turn/started":
		a.turnStarted(stringValue(objectValue(params["turn"])["id"]))
	case "item/started":
		item := objectValue(params["item"])
		if codexJournaled(stringValue(item["type"])) {
			a.mu.Lock()
			a.starts[stringValue(item["id"])] = a.anchors[stringValue(params["turnId"])]
			a.mu.Unlock()
		}
		a.itemStarted(item)
	case "item/agentMessage/delta", "item/plan/delta":
		a.delta(stringValue(params["itemId"]), "text", stringValue(params["delta"]))
	case "item/reasoning/summaryTextDelta":
		a.delta(stringValue(params["itemId"]), "thinking", stringValue(params["delta"]))
	case "item/reasoning/summaryPartAdded":
		// A new part of the summary is a new paragraph.
		a.mu.Lock()
		open := a.open
		apart := open != nil && open.itemID == stringValue(params["itemId"]) && open.text.Len() > 0
		a.mu.Unlock()
		if apart {
			a.delta(open.itemID, "thinking", "\n\n")
		}
	case "item/commandExecution/outputDelta":
		a.output(stringValue(params["itemId"]), stringValue(params["delta"]))
	case "item/completed":
		item := objectValue(params["item"])
		a.journal(stringValue(params["turnId"]), item)
		a.itemCompleted(item)
	case "turn/completed":
		a.turnCompleted(objectValue(params["turn"]))
	case "turn/plan/updated":
		a.plan(params)
	case "thread/tokenUsage/updated":
		a.tokenUsage(stringValue(params["turnId"]), objectValue(params["tokenUsage"]))
	case "thread/settings/updated":
		a.settingsUpdated(objectValue(params["threadSettings"]))
	case "thread/queue/changed":
		go a.emitQueue()
	case "serverRequest/resolved":
		a.resolved(params["requestId"])
	case "error":
		if params["willRetry"] == true {
			a.mu.Lock()
			a.retrying = true
			a.retries++
			attempt := a.retries
			a.mu.Unlock()
			a.emit(map[string]any{"type": "auto_retry_start", "attempt": attempt, "errorMessage": stringValue(objectValue(params["error"])["message"])})
		}
	case "warning":
		a.notify(stringValue(params["message"]), "warning")
	case "configWarning":
		a.notify(strings.TrimSpace(stringValue(params["summary"])+"\n"+stringValue(params["details"])), "warning")
	case "model/rerouted":
		a.notify(fmt.Sprintf("Codex used %s in place of %s for this turn.", stringValue(params["toModel"]), stringValue(params["fromModel"])), "warning")
	case "mcpServer/startupStatus/updated":
		a.serverStatus(params)
	}
}

func (a *codexApp) turnStarted(id string) {
	a.mu.Lock()
	start := !a.streaming
	a.streaming, a.turnID = true, id
	a.mu.Unlock()
	if start {
		a.emit(map[string]any{"type": "agent_start"})
		go a.identity()
	}
}

func (a *codexApp) update(update map[string]any) {
	a.emit(map[string]any{"type": "message_update", "assistantMessageEvent": update})
}

// openMessage starts the assistant message of an item, and ends the message
// before it.
func (a *codexApp) openMessage(itemID, kind string) {
	a.closeOpen("stop")
	a.mu.Lock()
	a.open = &codexOpen{itemID: itemID, kind: kind, at: time.Now().UnixMilli()}
	a.mu.Unlock()
	a.emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}, "timestamp": time.Now().UnixMilli()}})
	a.update(map[string]any{"type": kind + "_start", "contentIndex": 0})
}

// closeOpen ends the message that streams, with the text that it has. A
// message ends without its completion when Codex stops it, for example for a
// steer or a stop of the person.
func (a *codexApp) closeOpen(stop string) {
	a.mu.Lock()
	open := a.open
	a.open = nil
	modelName, effort := a.model, a.effort
	a.mu.Unlock()
	if open == nil {
		return
	}
	text := open.text.String()
	a.update(map[string]any{"type": open.kind + "_end", "contentIndex": 0, "content": text})
	var content []any
	switch {
	case open.kind == "thinking" && text != "":
		content = []any{map[string]any{"type": "thinking", "thinking": text}}
	case open.kind == "text" && text != "":
		content = []any{map[string]any{"type": "text", "text": text}}
	}
	message := codexAssistant(content, stop, modelName, open.at)
	message["thinkingLevel"] = effort
	a.emit(map[string]any{"type": "message_end", "message": message})
}

func (a *codexApp) delta(itemID, kind, delta string) {
	a.mu.Lock()
	open := a.open
	same := open != nil && open.itemID == itemID
	a.mu.Unlock()
	if !same {
		a.openMessage(itemID, kind)
		a.mu.Lock()
		open = a.open
		a.mu.Unlock()
	}
	a.endRetry(true)
	open.text.WriteString(delta)
	a.update(map[string]any{"type": kind + "_delta", "contentIndex": 0, "delta": delta})
}

// endRetry ends a retry of the model, when the turn goes on or ends.
func (a *codexApp) endRetry(success bool) {
	a.mu.Lock()
	retried := a.retrying
	a.retrying = false
	a.mu.Unlock()
	if retried {
		a.emit(map[string]any{"type": "auto_retry_end", "success": success})
	}
}

func (a *codexApp) itemStarted(item map[string]any) {
	id, kind := stringValue(item["id"]), stringValue(item["type"])
	switch {
	case kind == "agentMessage" || kind == "plan" || kind == "reasoning":
		a.endRetry(true)
		a.openMessage(id, messageKind(kind))
	case kind == "contextCompaction":
		a.closeOpen("stop")
		a.mu.Lock()
		a.compacting = true
		a.compactedFrom = 0
		if a.contextUsage != nil {
			a.compactedFrom, _ = a.contextUsage["tokens"].(float64)
		}
		a.mu.Unlock()
		a.emit(map[string]any{"type": "compaction_start", "reason": "manual"})
	case codexToolItems[kind]:
		a.endRetry(true)
		a.closeOpen("toolUse")
		a.mu.Lock()
		a.items[id] = item
		a.started[id] = time.Now()
		a.mu.Unlock()
		for _, call := range codexCalls(item, a.options.Directory) {
			a.emit(map[string]any{"type": "tool_execution_start", "toolCallId": call.ID, "toolName": call.Name, "args": call.Args})
		}
	case kind == "enteredReviewMode":
		a.notify("Codex started a review: "+stringValue(item["review"]), "info")
	}
}

func (a *codexApp) output(itemID, delta string) {
	a.mu.Lock()
	if task := a.tasks[itemID]; task != nil {
		task["tail"] = tailLines(stringValue(task["output"])+delta, 12)
		task["output"] = lastBytes(stringValue(task["output"])+delta, 16<<10)
		a.mu.Unlock()
		a.publishTasks()
		return
	}
	buffer := a.outputs[itemID]
	if buffer == nil {
		buffer = &strings.Builder{}
		a.outputs[itemID] = buffer
	}
	buffer.WriteString(delta)
	text := buffer.String()
	item := a.items[itemID]
	a.mu.Unlock()
	if item == nil {
		return
	}
	if len(text) > 64<<10 {
		text = text[len(text)-64<<10:]
	}
	for _, call := range codexCalls(item, a.options.Directory) {
		a.emit(map[string]any{"type": "tool_execution_update", "toolCallId": call.ID, "toolName": call.Name,
			"partialResult": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
	}
}

// journal keeps a step that the saved history of the thread drops, with
// its place among the anchors of its turn.
func (a *codexApp) journal(turn string, item map[string]any) {
	kind := stringValue(item["type"])
	if turn == "" || !codexAnchors[kind] && !codexJournaled(kind) {
		return
	}
	a.mu.Lock()
	after := a.anchors[turn]
	if start, ok := a.starts[stringValue(item["id"])]; ok {
		// A step keeps the place where it started, also when it ends
		// after other items, or after its turn.
		after = start
		delete(a.starts, stringValue(item["id"]))
	}
	if codexAnchors[kind] {
		a.anchors[turn]++
	}
	a.mu.Unlock()
	if !codexJournaled(kind) {
		return
	}
	kept := make(map[string]any, len(item))
	for key, value := range item {
		kept[key] = value
	}
	if output := stringValue(kept["aggregatedOutput"]); len(output) > 16<<10 {
		kept["aggregatedOutput"] = output[:8<<10] + "\n…\n" + output[len(output)-8<<10:]
	}
	a.writeStep(codexStep{Turn: turn, After: after, Item: kept})
}

// writeStep adds an entry to the journal of steps.
func (a *codexApp) writeStep(step codexStep) {
	data, err := json.Marshal(step)
	if err != nil {
		return
	}
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(a.stepsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = file.Write(append(data, '\n'))
	_ = file.Close()
}

func (a *codexApp) itemCompleted(item map[string]any) {
	id, kind := stringValue(item["id"]), stringValue(item["type"])
	at := itemTime(item, time.Now().UnixMilli())
	if a.taskEnded(item) {
		return
	}
	switch {
	case kind == "userMessage":
		message := codexUserMessage(item, at)
		a.emit(map[string]any{"type": "message_start", "message": message})
		a.emit(map[string]any{"type": "message_end", "message": message})
	case kind == "agentMessage" || kind == "plan" || kind == "reasoning":
		a.mu.Lock()
		open := a.open
		if open == nil || open.itemID != id {
			a.mu.Unlock()
			// A message that did not stream.
			a.openMessage(id, messageKind(kind))
			a.mu.Lock()
			open = a.open
		}
		text := stringValue(item["text"])
		if kind == "reasoning" {
			text = codexReasoning(item)
		}
		if text != "" {
			open.text.Reset()
			open.text.WriteString(text)
		}
		a.mu.Unlock()
		a.closeOpen("stop")
	case kind == "contextCompaction":
		a.mu.Lock()
		a.compacting = false
		before := a.compactedFrom
		a.mu.Unlock()
		a.emit(map[string]any{"type": "compaction_end", "result": map[string]any{"summary": "", "tokensBefore": before}})
	case codexToolItems[kind]:
		a.mu.Lock()
		started, known := a.started[id]
		delete(a.started, id)
		delete(a.items, id)
		delete(a.outputs, id)
		a.mu.Unlock()
		calls := codexCalls(item, a.options.Directory)
		duration := int64(0)
		if value, ok := item["durationMs"].(float64); ok {
			duration = int64(value)
		} else if known {
			duration = time.Since(started).Milliseconds()
		}
		for index, call := range calls {
			if !known {
				a.emit(map[string]any{"type": "tool_execution_start", "toolCallId": call.ID, "toolName": call.Name, "args": call.Args})
			}
			content, details, failed := codexResult(item, index)
			result := map[string]any{"content": content}
			if details != nil {
				result["details"] = details
			}
			a.emit(map[string]any{"type": "tool_execution_end", "toolCallId": call.ID, "toolName": call.Name, "result": result, "isError": failed, "durationMs": duration})
		}
	case kind == "exitedReviewMode":
		a.notify("Codex ended the review.", "info")
	}
}

// messageKind gives the kind of content of a message item.
func messageKind(itemType string) string {
	if itemType == "reasoning" {
		return "thinking"
	}
	return "text"
}

func (a *codexApp) turnCompleted(turn map[string]any) {
	status := stringValue(turn["status"])
	switch status {
	case "interrupted":
		a.mu.Lock()
		open := a.open != nil
		a.mu.Unlock()
		if open {
			a.closeOpen("aborted")
		} else {
			stopped := codexAssistant(nil, "aborted", a.currentModel(), time.Now().UnixMilli())
			a.emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}}})
			a.emit(map[string]any{"type": "message_end", "message": stopped})
		}
	case "failed":
		a.closeOpen("stop")
		failure := codexAssistant(nil, "error", a.currentModel(), time.Now().UnixMilli())
		failure["errorMessage"] = firstText(stringValue(objectValue(turn["error"])["message"]), "The turn failed.")
		a.emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}}})
		a.emit(map[string]any{"type": "message_end", "message": failure})
	default:
		a.closeOpen("stop")
	}
	a.endRetry(status != "failed")
	a.mu.Lock()
	compacting := a.compacting
	a.compacting = false
	usage, turnID := a.usage, a.turnID
	a.usage = nil
	a.streaming, a.turnID, a.retries = false, "", 0
	// A command that runs on after its turn becomes a background task.
	// Other tools that never reported an end belong to the turn that ended.
	var moved []map[string]any
	for id, item := range a.items {
		if item["type"] != "commandExecution" || status != "completed" {
			continue
		}
		output := ""
		if buffer := a.outputs[id]; buffer != nil {
			output = lastBytes(buffer.String(), 16<<10)
		}
		started := a.started[id]
		a.tasks[id] = map[string]any{"id": id, "kind": "command", "description": "", "command": codexCommand(stringValue(item["command"])),
			"status": "running", "startedAt": started.UnixMilli(), "tail": tailLines(output, 12), "output": output, "outputPath": "",
			"pid": stringValue(item["processId"])}
		a.background[id] = item
		moved = append(moved, item)
	}
	a.items = map[string]map[string]any{}
	a.started = map[string]time.Time{}
	a.outputs = map[string]*strings.Builder{}
	a.mu.Unlock()
	for _, item := range moved {
		for _, call := range codexCalls(item, a.options.Directory) {
			a.emit(map[string]any{"type": "tool_execution_end", "toolCallId": call.ID, "toolName": call.Name, "isError": false,
				"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "This command runs on in the background. The Tasks panel shows it."}}}})
		}
	}
	if len(moved) > 0 {
		a.publishTasks()
	}
	if len(usage) > 0 && turnID != "" {
		a.writeStep(codexStep{Turn: turnID, Usage: usage})
	}
	if compacting {
		a.emit(map[string]any{"type": "compaction_end", "aborted": true})
	}
	a.emit(map[string]any{"type": "agent_end", "messages": []any{}})
	a.emit(map[string]any{"type": "agent_settled"})
	go a.refreshWork()
}

func (a *codexApp) currentModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// plan shows the plan of the turn as the todo list of the tab.
func (a *codexApp) plan(params map[string]any) {
	var tasks []any
	for index, raw := range anyList(params["plan"]) {
		step := objectValue(raw)
		status := stringValue(step["status"])
		switch status {
		case "inProgress", "in_progress":
			status = "in_progress"
		case "completed":
		default:
			status = "pending"
		}
		tasks = append(tasks, map[string]any{"id": index + 1, "subject": stringValue(step["step"]), "status": status})
	}
	id := "codex-plan-" + uuid.NewString()
	args := map[string]any{"action": "update", "subject": stringValue(params["explanation"])}
	a.emit(map[string]any{"type": "tool_execution_start", "toolCallId": id, "toolName": "todo", "args": args})
	a.emit(map[string]any{"type": "tool_execution_end", "toolCallId": id, "toolName": "todo", "isError": false,
		"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Updated the plan"}}, "details": map[string]any{"tasks": tasks}}})
}

// tokenUsage gives the tab the context use, and the tokens of the model
// request that ended for the turn.
func (a *codexApp) tokenUsage(turn string, usage map[string]any) {
	last := objectValue(usage["last"])
	number := func(key string) float64 { value, _ := last[key].(float64); return value }
	request := map[string]float64{"input": max(0, number("inputTokens")-number("cachedInputTokens")), "output": number("outputTokens"), "cacheRead": number("cachedInputTokens")}
	a.mu.Lock()
	streaming := a.streaming && a.turnID == turn
	if streaming {
		if a.usage == nil {
			a.usage = map[string]float64{}
		}
		for key, value := range request {
			a.usage[key] += value
		}
	}
	a.mu.Unlock()
	if streaming {
		a.emit(map[string]any{"type": "turn_usage", "input": request["input"], "output": request["output"], "cacheRead": request["cacheRead"]})
	}
	tokens := number("totalTokens")
	window, _ := usage["modelContextWindow"].(float64)
	a.mu.Lock()
	if window > 0 {
		a.window = window
	}
	window = a.window
	percent := 0.0
	if window > 0 {
		percent = tokens * 100 / window
	}
	a.contextUsage = map[string]any{"tokens": tokens, "contextWindow": window, "percent": percent}
	usageCopy := a.contextUsage
	a.mu.Unlock()
	a.emit(map[string]any{"type": "context_usage", "contextUsage": usageCopy})
}

func (a *codexApp) settingsUpdated(settings map[string]any) {
	if settings == nil {
		return
	}
	before := a.mode()
	a.mu.Lock()
	effortBefore := a.effort
	if value := stringValue(settings["model"]); value != "" {
		a.model = value
	}
	if _, ok := settings["effort"]; ok {
		a.effort = stringValue(settings["effort"])
	}
	if value, ok := settings["approvalPolicy"]; ok {
		a.approval = value
	}
	a.setSandboxLocked(objectValue(settings["sandboxPolicy"]))
	if mode := stringValue(objectValue(settings["collaborationMode"])["mode"]); mode != "" {
		a.collaboration = mode
	}
	effort := a.effort
	a.mu.Unlock()
	if mode := a.mode(); mode != before {
		a.emit(map[string]any{"type": "permission_mode_changed", "mode": mode})
	}
	if effort != effortBefore && effort != "" {
		a.emit(map[string]any{"type": "thinking_level_changed", "level": effort})
	}
}

// serverStatus tells the person about an MCP server that did not start, as
// the terminal of Codex does.
func (a *codexApp) serverStatus(params map[string]any) {
	name := stringValue(params["name"])
	status := stringValue(params["status"])
	if name == "" || name == "galpon" || status != "failed" {
		return
	}
	a.mu.Lock()
	seen := a.warned[name]
	a.warned[name] = true
	a.mu.Unlock()
	if !seen {
		a.notify(fmt.Sprintf("The MCP server %s did not start: %s", name, firstText(stringValue(params["error"]), "unknown error")), "warning")
	}
}

// --- background tasks

// taskEnded ends the background task of a command that ended after its turn,
// and tells it in the conversation. It reports false for another item.
func (a *codexApp) taskEnded(item map[string]any) bool {
	id := stringValue(item["id"])
	a.mu.Lock()
	task := a.tasks[id]
	if task == nil {
		a.mu.Unlock()
		return false
	}
	delete(a.background, id)
	status := "completed"
	if code, ok := item["exitCode"].(float64); ok && code != 0 || item["status"] == "failed" {
		status = "failed"
	}
	if task["status"] == "stopped" {
		status = "stopped"
	}
	output := firstText(stringValue(item["aggregatedOutput"]), stringValue(task["output"]))
	task["status"], task["endedAt"], task["tail"] = status, time.Now().UnixMilli(), tailLines(output, 12)
	if code, ok := item["exitCode"].(float64); ok {
		task["exitCode"] = int(code)
	}
	command := stringValue(task["command"])
	a.mu.Unlock()
	// The end of the command is a notice in the conversation, as for the
	// background tasks of the other agents.
	notice := claudeTaskMessage(id, "command", command, status, lastBytes(output, 4000), time.Now().UnixMilli())
	a.emit(map[string]any{"type": "message_start", "message": notice})
	a.emit(map[string]any{"type": "message_end", "message": notice})
	a.publishTasks()
	return true
}

func (a *codexApp) publishTasks() {
	a.mu.Lock()
	rows := make([]map[string]any, 0, len(a.tasks))
	for _, task := range a.tasks {
		row := map[string]any{}
		for key, value := range task {
			if key != "output" && key != "pid" {
				row[key] = value
			}
		}
		rows = append(rows, row)
	}
	a.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i]["startedAt"]) < fmt.Sprint(rows[j]["startedAt"]) })
	a.publish("tasks", map[string]any{"tasks": rows})
}

// stopTask stops a command that runs in the background: the process that
// Codex started for it, when it still runs that command.
func (a *codexApp) stopTask(id string) error {
	a.mu.Lock()
	task := a.tasks[strings.TrimSpace(id)]
	var pid int
	var command string
	if task != nil && task["status"] == "running" {
		pid, _ = strconv.Atoi(stringValue(task["pid"]))
		command = stringValue(task["command"])
	}
	a.mu.Unlock()
	if task == nil {
		return fmt.Errorf("no background command %q", id)
	}
	if found := a.commandProcess(command); found > 0 {
		pid = found
	} else if out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output(); pid <= 0 || err != nil || !strings.Contains(string(out), firstLine(command)) {
		// The process ID can belong to another process after the command ends.
		return errors.New("this command already ended")
	}
	// The command and its children have their own process group, apart
	// from the runtime.
	if group, err := syscall.Getpgid(pid); err == nil && group != syscall.Getpgrp() {
		pid = -group
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	a.mu.Lock()
	task["status"] = "stopped"
	a.mu.Unlock()
	return nil
}

// commandProcess finds the process of a command that Codex runs: Codex can
// run it in a sandbox with its own process IDs, so the ID that it gives does
// not name the process. The process is a descendant of the app-server of
// this agent, with the command in its arguments. It gives 0 when /proc has
// no such process.
func (a *codexApp) commandProcess(command string) int {
	if a.driver.process == nil || a.driver.process.command == nil || a.driver.process.command.Process == nil {
		return 0
	}
	root := a.driver.process.command.Process.Pid
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	parent := func(pid int) int {
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			return 0
		}
		// The name in parentheses can hold spaces: the fields come after it.
		fields := strings.Fields(string(data[bytes.LastIndexByte(data, ')')+1:]))
		if len(fields) < 2 {
			return 0
		}
		value, _ := strconv.Atoi(fields[1])
		return value
	}
	best := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || !strings.Contains(strings.ReplaceAll(string(data), "\x00", " "), command) {
			continue
		}
		for ancestor, steps := parent(pid), 0; ancestor > 1 && steps < 64; ancestor, steps = parent(ancestor), steps+1 {
			if ancestor == root {
				// The outermost match is the shell that runs the command.
				if best == 0 || pid < best {
					best = pid
				}
				break
			}
		}
	}
	return best
}

func tailLines(text string, count int) []string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return []string{}
	}
	return lines
}

func lastBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[len(text)-limit:]
}

// emitQueue sends the messages of the person that wait for the run.
func (a *codexApp) emitQueue() {
	result, err := a.call("thread/queue/list", map[string]any{"threadId": a.driver.id})
	if err != nil {
		return
	}
	followUp := []string{}
	for _, submission := range codexQueue(result) {
		followUp = append(followUp, submission.text)
	}
	key := strings.Join(followUp, "\x00")
	a.mu.Lock()
	changed := key != a.queueKey
	a.queueKey = key
	a.mu.Unlock()
	if changed {
		a.emit(map[string]any{"type": "queue_update", "steering": []string{}, "followUp": followUp})
	}
}

type codexSubmission struct{ id, text string }

// codexQueue reads the queued messages of the person. Galpon deliveries wait
// in the same queue, but they are not messages of the person.
func codexQueue(result json.RawMessage) []codexSubmission {
	var response struct {
		Data []struct {
			ID                  string `json:"id"`
			Input               []any  `json:"input"`
			ClientUserMessageID string `json:"clientUserMessageId"`
		} `json:"data"`
	}
	if json.Unmarshal(result, &response) != nil {
		return nil
	}
	var out []codexSubmission
	for _, item := range response.Data {
		if strings.HasPrefix(item.ClientUserMessageID, "galpon:") {
			continue
		}
		out = append(out, codexSubmission{item.ID, contentText(codexUserContent(item.Input))})
	}
	return out
}

// --- the commands of the tab

func (a *codexApp) commandsLoop() {
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

func (a *codexApp) command(line []byte) {
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
		// Prompts go to Codex in their order.
		a.waitReady()
		a.respond(id, kind, nil, a.prompt(command, kind))
		return
	}
	go func() {
		data, err := a.run(kind, command)
		a.respond(id, kind, data, err)
	}()
}

// codexInput gives the input of a message of the tab: its text and images.
func codexInput(text string, images []any) []any {
	input := []any{map[string]any{"type": "text", "text": text, "text_elements": []any{}}}
	for _, raw := range images {
		image := objectValue(raw)
		if data := stringValue(image["data"]); data != "" {
			input = append(input, map[string]any{"type": "image", "url": "data:" + firstText(stringValue(image["mimeType"]), "image/png") + ";base64," + data})
		}
	}
	return input
}

func (a *codexApp) prompt(command map[string]any, kind string) error {
	text := stringValue(command["message"])
	if name, args, ok := slashCommand(text); ok {
		if claudeBlockedCommands[name] {
			return fmt.Errorf("/%s would end the conversation of this Galpon agent; create another agent for a new conversation", name)
		}
		if name == "galpon-app-stop" {
			return a.stopTask(args)
		}
	}
	input := codexInput(text, anyList(command["images"]))
	a.mu.Lock()
	working, turn := a.streaming, a.turnID
	a.mu.Unlock()
	if working {
		// While Codex works, a steer goes to the model at its next step, as
		// Pi steers, and a follow-up waits in the queue of the thread.
		if kind == "follow_up" || kind == "prompt" && stringValue(command["streamingBehavior"]) == "followUp" {
			_, err := a.call("thread/queue/add", map[string]any{"threadId": a.driver.id, "input": input, "clientUserMessageId": "tab:" + uuid.NewString()})
			return err
		}
		if _, err := a.call("turn/steer", map[string]any{"threadId": a.driver.id, "input": input, "expectedTurnId": turn}); err == nil {
			return nil
		}
		// The turn ended before the steer: the message starts the next turn.
	}
	_, err := a.call("turn/start", map[string]any{"threadId": a.driver.id, "input": input})
	return err
}

func (a *codexApp) run(kind string, command map[string]any) (any, error) {
	switch kind {
	case "abort":
		a.mu.Lock()
		working, turn := a.streaming, a.turnID
		a.mu.Unlock()
		if !working {
			return nil, nil
		}
		_, err := a.call("turn/interrupt", map[string]any{"threadId": a.driver.id, "turnId": turn})
		return nil, err
	case "clear_queue":
		result, err := a.call("thread/queue/list", map[string]any{"threadId": a.driver.id})
		if err != nil {
			return nil, err
		}
		followUp := []string{}
		for _, submission := range codexQueue(result) {
			if _, err := a.call("thread/queue/delete", map[string]any{"threadId": a.driver.id, "queuedSubmissionId": submission.id}); err == nil {
				followUp = append(followUp, submission.text)
			}
		}
		a.emitQueue()
		return map[string]any{"steering": []string{}, "followUp": followUp}, nil
	case "get_state":
		a.waitReady()
		return a.state(), nil
	case "get_messages":
		a.waitReady()
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
		defer cancel()
		result, err := a.driver.call(ctx, "thread/read", map[string]any{"threadId": a.driver.id, "includeTurns": true})
		if err != nil {
			return nil, err
		}
		var response struct {
			Thread struct {
				Turns []any `json:"turns"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(result, &response); err != nil {
			return nil, err
		}
		steps := readCodexSteps(a.stepsPath())
		return map[string]any{"messages": codexHistory(response.Thread.Turns, steps, a.options.Directory, a.currentModel(), claudeHistoryLimit)}, nil
	case "get_session_stats":
		id, path := a.driver.Session()
		a.mu.Lock()
		defer a.mu.Unlock()
		return map[string]any{"sessionId": id, "sessionFile": path, "contextUsage": a.contextUsage}, nil
	case "get_commands":
		return map[string]any{"commands": []any{}}, nil
	case "get_available_models":
		a.waitReady()
		a.mu.Lock()
		defer a.mu.Unlock()
		models := []any{}
		for _, item := range a.models {
			models = append(models, a.modelInfoLocked(item))
		}
		return map[string]any{"models": models}, nil
	case "set_model", "cycle_model":
		a.waitReady()
		target := stringValue(command["modelId"])
		if kind == "cycle_model" {
			target = a.nextModel()
		}
		if target == "" {
			return nil, errors.New("choose a model")
		}
		params := map[string]any{"model": target}
		a.mu.Lock()
		levels := a.levelsForLocked(target)
		effort := a.effort
		if len(levels) > 0 && !slices.Contains(levels, effort) {
			params["effort"] = a.defaultEffortLocked(target, levels)
		}
		a.mu.Unlock()
		if err := a.updateSettings(params); err != nil {
			return nil, err
		}
		a.saveSettings()
		a.mu.Lock()
		info := a.currentModelLocked()
		effort = a.effortLocked()
		a.mu.Unlock()
		a.emit(map[string]any{"type": "thinking_level_changed", "level": effort})
		if kind == "cycle_model" {
			return map[string]any{"model": info, "thinkingLevel": effort}, nil
		}
		return info, nil
	case "get_available_thinking_levels":
		a.waitReady()
		a.mu.Lock()
		defer a.mu.Unlock()
		return map[string]any{"levels": a.levelsForLocked(a.model)}, nil
	case "set_thinking_level", "cycle_thinking_level":
		a.waitReady()
		level := stringValue(command["level"])
		a.mu.Lock()
		levels := a.levelsForLocked(a.model)
		if kind == "cycle_thinking_level" && len(levels) > 0 {
			level = levels[(slices.Index(levels, a.effortLocked())+1)%len(levels)]
		}
		a.mu.Unlock()
		if level == "" || len(levels) > 0 && !slices.Contains(levels, level) {
			return nil, fmt.Errorf("this model has no %q effort", level)
		}
		if err := a.updateSettings(map[string]any{"effort": level}); err != nil {
			return nil, err
		}
		a.saveSettings()
		a.emit(map[string]any{"type": "thinking_level_changed", "level": level})
		if kind == "cycle_thinking_level" {
			return map[string]any{"level": level}, nil
		}
		return nil, nil
	case "set_permission_mode":
		a.waitReady()
		mode := stringValue(command["mode"])
		known := false
		for _, item := range codexModes {
			known = known || item["mode"] == mode
		}
		if !known {
			return nil, fmt.Errorf("unknown mode %q", mode)
		}
		if err := a.updateSettings(a.modeParams(mode)); err != nil {
			return nil, err
		}
		a.saveSettings()
		mode = a.mode()
		a.emit(map[string]any{"type": "permission_mode_changed", "mode": mode})
		return map[string]any{"mode": mode}, nil
	case "compact":
		_, err := a.call("thread/compact/start", map[string]any{"threadId": a.driver.id})
		return nil, err
	case "set_session_name":
		return nil, nil
	case "new_session", "switch_session", "fork", "clone":
		return nil, errors.New("this Codex conversation belongs to its Galpon agent; create another agent for a new conversation")
	case "bash":
		return nil, errors.New("run shell commands in a terminal of this agent")
	}
	return nil, fmt.Errorf("unknown command %q", kind)
}

func (a *codexApp) state() map[string]any {
	id, path := a.driver.Session()
	mode := a.mode()
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{"model": a.currentModelLocked(), "thinkingLevel": a.effortLocked(), "isStreaming": a.streaming, "isCompacting": a.compacting,
		"sessionId": id, "sessionFile": path, "sessionName": "", "autoCompactionEnabled": true, "pendingMessageCount": 0,
		"permissionMode": mode, "permissionModes": codexModes, "harness": model.HarnessCodex}
}

func (a *codexApp) findModelLocked(id string) (codexModel, bool) {
	for _, item := range a.models {
		if item.ID == id {
			return item, true
		}
	}
	return codexModel{}, false
}

func (a *codexApp) modelInfoLocked(item codexModel) map[string]any {
	info := map[string]any{"id": item.ID, "name": item.Name, "provider": codexProvider, "description": item.Description,
		"reasoning": len(item.Efforts) > 0, "input": item.Inputs}
	if item.ID == a.model && a.window > 0 {
		info["contextWindow"] = a.window
	}
	return info
}

func (a *codexApp) currentModelLocked() map[string]any {
	if a.model == "" {
		return nil
	}
	item, ok := a.findModelLocked(a.model)
	if !ok {
		item = codexModel{ID: a.model, Name: a.model, Inputs: []string{"text", "image"}}
	}
	return a.modelInfoLocked(item)
}

// effortLocked gives the effort of the next turns: the chosen effort, or the
// effort that the model uses by default.
func (a *codexApp) effortLocked() string {
	if a.effort != "" {
		return a.effort
	}
	if item, ok := a.findModelLocked(a.model); ok {
		return item.DefaultEffort
	}
	return ""
}

func (a *codexApp) levelsForLocked(id string) []string {
	if item, ok := a.findModelLocked(id); ok {
		return item.Efforts
	}
	return []string{"low", "medium", "high", "xhigh"}
}

func (a *codexApp) defaultEffortLocked(id string, levels []string) string {
	if item, ok := a.findModelLocked(id); ok && item.DefaultEffort != "" {
		return item.DefaultEffort
	}
	return levels[min(1, len(levels)-1)]
}

func (a *codexApp) nextModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.models) == 0 {
		return ""
	}
	index := slices.IndexFunc(a.models, func(item codexModel) bool { return item.ID == a.model })
	return a.models[(index+1)%len(a.models)].ID
}
