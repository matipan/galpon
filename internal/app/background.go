package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/piagent"
)

type backgroundProcess struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	runtimeID string
	done      chan struct{}
	writeMu   sync.Mutex
	stopping  bool
}

// StartAgent starts a foreground agent in the renderer and a background agent
// in a pane-free Pi RPC process.
func (a *App) StartAgent(ctx context.Context, id string) (model.Agent, error) {
	agent, err := a.Store.Agent(ctx, id)
	if err != nil {
		return model.Agent{}, err
	}
	if !agent.IsBackground() {
		return a.OpenAgent(ctx, id, false)
	}
	return a.StartBackgroundAgent(ctx, id)
}

func (a *App) StartBackgroundAgent(ctx context.Context, id string) (model.Agent, error) {
	unlock := a.lockAgentLifecycle(id)
	defer unlock()
	return a.startBackgroundAgentLocked(ctx, id)
}

// dispatchQueuedAgents recovers durable queued work after daemon, renderer, or
// background process failures. Active agents are inexpensive no-op starts.
func (a *App) dispatchQueuedAgents() {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-a.backgroundContext.Done():
			return
		case <-timer.C:
		}
		generation, complete, maintenance, stateErr := a.Store.CommunicationProtocolState(a.backgroundContext)
		_ = generation
		if stateErr != nil {
			if a.Logger != nil && !errors.Is(stateErr, context.Canceled) {
				a.Logger.Printf("read communication protocol state: %v", stateErr)
			}
			timer.Reset(15 * time.Second)
			continue
		}
		if maintenance || a.communicationDraining.Load() {
			timer.Reset(time.Second)
			continue
		}
		if complete {
			if err := a.Store.SweepCoordinationDeadlines(a.backgroundContext); err != nil && a.Logger != nil && !errors.Is(err, context.Canceled) {
				a.Logger.Printf("sweep coordination deadlines: %v", err)
			}
		} else if err := a.Store.SweepExpiredAgentMessages(a.backgroundContext); err != nil {
			if a.Logger != nil && !errors.Is(err, context.Canceled) {
				a.Logger.Printf("sweep expired agent messages: %v", err)
			}
		} else {
			// The sweep can settle work in the durable store. Wake waiters so they
			// can read the committed state without a short database poll loop.
			a.notifyAllMessageWaiters()
		}
		if err := a.Store.DispatchLifecycleEvents(a.backgroundContext, 100); err != nil && a.Logger != nil && !errors.Is(err, context.Canceled) {
			a.Logger.Printf("dispatch lifecycle events: %v", err)
		}
		if _, err := a.Store.PruneAgentMessageHistory(a.backgroundContext); err != nil && a.Logger != nil && !errors.Is(err, context.Canceled) {
			a.Logger.Printf("prune agent message history: %v", err)
		}
		var ids []string
		var err error
		if complete {
			ids, err = a.Store.CoordinationReadyAgentIDs(a.backgroundContext)
		} else {
			ids, err = a.Store.QueuedAgentIDs(a.backgroundContext)
		}
		if err != nil {
			if a.Logger != nil && !errors.Is(err, context.Canceled) {
				a.Logger.Printf("scan queued agents: %v", err)
			}
		} else {
			for _, id := range ids {
				if _, err := a.StartAgent(a.backgroundContext, id); err != nil && a.Logger != nil {
					a.Logger.Printf("start queued agent %s: %v", id, err)
				}
			}
		}
		timer.Reset(15 * time.Second)
	}
}

// scheduleAgentStartRetry gives a durable queued message more chances to start
// its target after a temporary renderer or process error. One loop owns each
// target, so concurrent sends do not create a retry storm.
func (a *App) scheduleAgentStartRetry(agentID, messageID string) {
	if a.backgroundContext == nil {
		return
	}
	a.startRetryMu.Lock()
	if a.startRetries == nil {
		a.startRetries = make(map[string]bool)
	}
	if a.startRetries[agentID] {
		a.startRetryMu.Unlock()
		return
	}
	a.startRetries[agentID] = true
	a.startRetryMu.Unlock()
	go func() {
		defer func() {
			a.startRetryMu.Lock()
			delete(a.startRetries, agentID)
			a.startRetryMu.Unlock()
		}()
		for _, delay := range []time.Duration{250 * time.Millisecond, time.Second, 4 * time.Second, 15 * time.Second} {
			timer := time.NewTimer(delay)
			select {
			case <-a.backgroundContext.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			message, err := a.Store.AgentMessage(a.backgroundContext, messageID)
			if err != nil || message.Status != "queued" {
				return
			}
			if _, err := a.StartAgent(a.backgroundContext, agentID); err == nil {
				return
			} else if a.Logger != nil {
				a.Logger.Printf("retry start Pi agent %s for message %s: %v", agentID, messageID, err)
			}
		}
	}()
}

func (a *App) startBackgroundAgentLocked(ctx context.Context, id string) (model.Agent, error) {
	agent, err := a.Store.Agent(ctx, id)
	if err != nil {
		return model.Agent{}, err
	}
	if !agent.IsBackground() {
		return model.Agent{}, fmt.Errorf("agent %s is not a background agent", agent.Title)
	}

	a.backgroundMu.Lock()
	_, running := a.backgroundProcesses[id]
	a.backgroundMu.Unlock()
	if running {
		return agent, nil
	}

	// A background child cannot survive the daemon that owned its stdin pipe.
	// Clear a stale runtime before a replacement child starts.
	if agent.RuntimeID != "" {
		_ = a.Store.StopAgentRuntime(ctx, agent.ID, agent.RuntimeID, "background runtime owner stopped")
		agent, err = a.Store.Agent(ctx, id)
		if err != nil {
			return model.Agent{}, err
		}
	}

	if a.backgroundStart != nil {
		if err := a.backgroundStart(ctx, agent); err != nil {
			_ = a.Store.SetAgentStatus(ctx, agent.ID, "failed", err.Error())
			return model.Agent{}, err
		}
		_ = a.Store.SetAgentStatus(ctx, agent.ID, "starting", "")
		return a.Store.Agent(ctx, agent.ID)
	}

	dashboard, err := a.Store.Dashboard(ctx)
	if err != nil {
		return model.Agent{}, err
	}
	workspace, ok := dashboard.Workspace(agent.WorkspaceID)
	if !ok {
		return model.Agent{}, fmt.Errorf("workspace not found")
	}
	worktree, ok := dashboard.PrimaryWorktree(agent)
	if !ok {
		if agent.Placement.Type != "none" || agent.Placement.CWD == "" {
			return model.Agent{}, fmt.Errorf("agent primary worktree not found")
		}
		worktree = model.Worktree{Path: agent.Placement.CWD}
	}
	contextSessionPath := ""
	if agent.ContextAgentID != "" && agent.SessionPath == "" {
		if source, ok := dashboard.Agent(agent.ContextAgentID); ok {
			contextSessionPath = source.SessionPath
		}
	}
	commandLine := piagent.BackgroundCommand(a.Config, a.PiAssets, agent, contextSessionPath)
	command := exec.CommandContext(a.backgroundContext, commandLine[0], commandLine[1:]...)
	command.Dir = worktree.Path
	runtimeID := uuid.NewString()
	if err := a.PrepareRuntime(ctx, agent.ID, runtimeID); err != nil {
		return model.Agent{}, err
	}
	protocol, err := a.CommunicationProtocolState(ctx)
	if err != nil {
		return model.Agent{}, err
	}
	command.Env = append(os.Environ(),
		"GALPON_SOCKET="+a.Config.Socket,
		fmt.Sprintf("GALPON_PROTOCOL_GENERATION=%d", protocol.Generation),
		"GALPON_AGENT_ID="+agent.ID,
		"GALPON_AGENT_TITLE="+agent.Title,
		"GALPON_AGENT_ROLE="+agent.Role,
		"GALPON_WORKSPACE_ID="+workspace.ID,
		"GALPON_WORKSPACE_TITLE="+workspace.Title,
		"GALPON_RUNTIME_ID="+runtimeID,
		"GALPON_PI_EXTENSION="+a.PiAssets.Extension,
		"GALPON_PLACEMENT="+backgroundPlacementDescription(dashboard, agent),
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		return model.Agent{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return model.Agent{}, err
	}
	if a.Logger != nil {
		command.Stderr = a.Logger.Writer()
	} else {
		command.Stderr = io.Discard
	}
	if err := a.Store.SetAgentStatus(ctx, agent.ID, "starting", ""); err != nil {
		_ = stdin.Close()
		return model.Agent{}, err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = a.Store.SetAgentStatus(ctx, agent.ID, "failed", err.Error())
		return model.Agent{}, err
	}
	process := &backgroundProcess{cmd: command, stdin: stdin, runtimeID: runtimeID, done: make(chan struct{})}
	a.backgroundMu.Lock()
	a.backgroundProcesses[agent.ID] = process
	a.backgroundMu.Unlock()
	go a.readBackgroundRPC(agent.ID, process, stdout)
	go a.waitBackgroundProcess(agent.ID, process)
	return a.Store.Agent(ctx, agent.ID)
}

func (a *App) readBackgroundRPC(agentID string, process *backgroundProcess, stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if len(line) != 0 {
			var event struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")), &event) == nil && event.Type == "extension_ui_request" && event.ID != "" {
				switch event.Method {
				case "select", "confirm", "input", "editor":
					response, _ := json.Marshal(map[string]any{"type": "extension_ui_response", "id": event.ID, "cancelled": true})
					process.writeMu.Lock()
					_, writeErr := process.stdin.Write(append(response, '\n'))
					process.writeMu.Unlock()
					if writeErr != nil && a.Logger != nil {
						a.Logger.Printf("cancel background Pi dialog for %s: %v", agentID, writeErr)
					}
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && a.Logger != nil {
				a.Logger.Printf("read background Pi output for %s: %v", agentID, err)
			}
			return
		}
	}
}

func (a *App) waitBackgroundProcess(agentID string, process *backgroundProcess) {
	err := process.cmd.Wait()
	a.backgroundMu.Lock()
	if a.backgroundProcesses[agentID] == process {
		delete(a.backgroundProcesses, agentID)
	}
	stopping := process.stopping
	a.backgroundMu.Unlock()
	lastError := ""
	if err != nil && !stopping && !errors.Is(a.backgroundContext.Err(), context.Canceled) {
		lastError = err.Error()
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !stopping {
		unlock := a.lockAgentLifecycle(agentID)
		defer unlock()
	}
	if err := a.Store.StopAgentRuntime(stopCtx, agentID, process.runtimeID, lastError); err != nil {
		if IsNotFound(err) {
			if agent, readErr := a.Store.Agent(stopCtx, agentID); readErr == nil && agent.RuntimeID == "" && agent.Status == "starting" {
				status := "stopped"
				if lastError != "" {
					status = "failed"
				}
				_ = a.Store.SetAgentStatus(stopCtx, agentID, status, lastError)
			}
		} else if a.Logger != nil {
			a.Logger.Printf("stop background Pi runtime for %s: %v", agentID, err)
		}
	}
	close(process.done)
}

func (a *App) stopBackgroundProcess(ctx context.Context, agentID string) error {
	a.backgroundMu.Lock()
	process := a.backgroundProcesses[agentID]
	if process != nil {
		process.stopping = true
	}
	a.backgroundMu.Unlock()
	if process == nil {
		return nil
	}
	process.writeMu.Lock()
	err := process.stdin.Close()
	process.writeMu.Unlock()
	if err != nil && !errors.Is(err, os.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	select {
	case <-process.done:
		return nil
	case <-ctx.Done():
		_ = process.cmd.Process.Kill()
		select {
		case <-process.done:
		case <-time.After(2 * time.Second):
		}
		return ctx.Err()
	case <-time.After(5 * time.Second):
		_ = process.cmd.Process.Kill()
		select {
		case <-process.done:
			return nil
		case <-time.After(2 * time.Second):
			return fmt.Errorf("background Pi process for agent %s did not stop", agentID)
		}
	}
}

func (a *App) stopAllBackgroundProcesses() {
	a.backgroundMu.Lock()
	ids := make([]string, 0, len(a.backgroundProcesses))
	for id := range a.backgroundProcesses {
		ids = append(ids, id)
	}
	a.backgroundMu.Unlock()
	var waits sync.WaitGroup
	waits.Add(len(ids))
	for _, id := range ids {
		go func(agentID string) {
			defer waits.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			_ = a.stopBackgroundProcess(ctx, agentID)
		}(id)
	}
	waits.Wait()
}

func backgroundPlacementDescription(dashboard model.Dashboard, agent model.Agent) string {
	if agent.Placement.Type == "none" {
		return "a directory at " + agent.Placement.CWD
	}
	parts := make([]string, 0, len(agent.Placement.Worktrees))
	for _, assignment := range agent.Placement.Worktrees {
		worktree, ok := dashboard.Worktree(assignment.WorktreeID)
		if !ok {
			continue
		}
		repository, _ := dashboard.Repository(worktree.RepositoryID)
		parts = append(parts, fmt.Sprintf("%s (%s, %s) at %s", repository.Title, assignment.Mode, worktree.Branch, worktree.Path))
	}
	return strings.Join(parts, "; ")
}
