package nativeagent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// appBridge is the part of the app view that does not depend on the harness.
// The app host of the Herdr app reads the events of Pi RPC on the stdout of
// the native runtime, one JSON value per line: emit writes them. The tab also
// gets the Galpon facts of the agent and its delegated work through bridge
// widgets, as galpon-app-bridge.ts gives them for Pi.
type appBridge struct {
	ctx     context.Context
	options launchOptions
	harness string
	outMu   sync.Mutex
	workMu  sync.Mutex
	work    string
}

// emit sends one event to the app host.
func (b *appBridge) emit(value map[string]any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	b.outMu.Lock()
	defer b.outMu.Unlock()
	_, _ = b.options.Output.Write(append(data, '\n'))
}

// respond answers a command of the tab.
func (b *appBridge) respond(id, command string, data any, err error) {
	if id == "" {
		return
	}
	response := map[string]any{"type": "response", "id": id, "command": command, "success": err == nil}
	if err != nil {
		response["error"] = err.Error()
	} else if data != nil {
		response["data"] = data
	}
	b.emit(response)
}

// notify shows a notice in the tab: info, warning or error.
func (b *appBridge) notify(text, level string) {
	text = strings.TrimSpace(stripANSI(text))
	if text == "" {
		return
	}
	b.emit(map[string]any{"type": "extension_ui_request", "id": uuid.NewString(), "method": "notify", "message": text, "notifyType": level})
}

// publish gives the tab a fact of the bridge of the app host.
func (b *appBridge) publish(key string, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	b.emit(map[string]any{"type": "extension_ui_request", "id": uuid.NewString(), "method": "setWidget", "widgetKey": "galpon-app:" + key, "widgetLines": []string{string(data)}})
}

// identity gives the tab the Galpon facts of the agent, with its current
// title.
func (b *appBridge) identity() {
	agent := b.options.Agent
	title := agent.Title
	if b.options.Client != nil {
		ctx, cancel := context.WithTimeout(b.ctx, 2*time.Second)
		if current, err := b.options.Client.AgentTitle(ctx, agent.ID); err == nil && current != "" {
			title = current
		}
		cancel()
	}
	b.publish("galpon", map[string]any{"agentId": agent.ID, "title": title, "role": agent.Role, "workspaceId": agent.WorkspaceID,
		"workspace": b.options.WorkspaceTitle, "placement": agent.Placement.Type, "cwd": b.options.Directory, "harness": b.harness})
}

func (b *appBridge) workLoop() {
	for {
		b.refreshWork()
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// refreshWork gives the tab the delegated work of the agent, as the Work Dock
// of Pi shows it.
func (b *appBridge) refreshWork() {
	if b.options.Client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()
	work, err := b.options.Client.AgentWork(ctx, b.options.Agent.ID, false)
	if err != nil {
		return
	}
	data, err := json.Marshal(work)
	if err != nil {
		return
	}
	b.workMu.Lock()
	changed := string(data) != b.work
	b.work = string(data)
	b.workMu.Unlock()
	if changed {
		b.publish("work", work)
	}
}
