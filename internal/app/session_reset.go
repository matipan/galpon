package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/matipan/galpon/internal/store"
)

// CheckRuntimeSessionReset reports whether the live runtime of an agent can
// replace its current Pi session with a new one. Pi calls this before it
// tears down the current session for /new.
func (a *App) CheckRuntimeSessionReset(ctx context.Context, agentID, runtimeID, sessionID string) error {
	runtimeID = strings.TrimSpace(runtimeID)
	sessionID = strings.TrimSpace(sessionID)
	if runtimeID == "" || sessionID == "" {
		return invalidRequestf("runtime ID and session ID are required")
	}
	agent, err := a.Store.Agent(ctx, agentID)
	if err != nil {
		return err
	}
	if agent.RuntimeID != runtimeID {
		return invalidRequestf("this Pi runtime does not own agent %s", agent.Title)
	}
	if agent.SessionID != sessionID {
		return invalidRequestf("Pi session %s is not the current session of agent %s", sessionID, agent.Title)
	}
	blocker, err := a.Store.AgentSessionResetBlocker(ctx, agentID)
	if err != nil {
		return err
	}
	if blocker != "" {
		return invalidRequestf("agent %s has %s; wait for it to settle before starting a new session", agent.Title, blocker)
	}
	return nil
}

// ResetRuntimeSession binds a new Pi session to an agent after Pi replaced
// the previous session in the same runtime. The agent keeps its identity,
// placement, inbox, and delegated agents. Only its Pi conversation changes.
// A repeated call for a session that is already bound registers the runtime
// again so that a lost response can be retried.
func (a *App) ResetRuntimeSession(ctx context.Context, agentID, runtimeID, previousSessionPath, sessionID, sessionPath string, generation int) (CommunicationProtocolState, error) {
	unlock := a.lockAgentLifecycle(agentID)
	defer unlock()
	runtimeID = strings.TrimSpace(runtimeID)
	sessionID = strings.TrimSpace(sessionID)
	if runtimeID == "" || sessionID == "" || strings.TrimSpace(sessionPath) == "" || strings.TrimSpace(previousSessionPath) == "" {
		return CommunicationProtocolState{}, invalidRequestf("runtime ID, session ID, session path, and previous session path are required")
	}
	agent, err := a.Store.Agent(ctx, agentID)
	if err != nil {
		return CommunicationProtocolState{}, err
	}
	sessionRoot := filepath.Join(a.Config.StateDir, "agents", agent.ID, "sessions")
	if !pathInside(sessionRoot, sessionPath) {
		return CommunicationProtocolState{}, invalidRequestf("the new Pi session of agent %s is outside its managed session directory", agent.Title)
	}
	if agent.SessionID != sessionID {
		if err := a.Store.ResetAgentSession(ctx, agentID, runtimeID, previousSessionPath, sessionID, sessionPath, generation); err != nil {
			switch {
			case errors.Is(err, store.ErrAgentSessionResetBlocked):
				return CommunicationProtocolState{}, invalidRequestf("agent %s: %v", agent.Title, err)
			case errors.Is(err, store.ErrAgentSessionResetConflict):
				return CommunicationProtocolState{}, invalidRequestf("agent %s: %v", agent.Title, err)
			}
			state, _ := a.CommunicationProtocolState(ctx)
			return state, err
		}
	}
	return a.registerRuntimeV2Locked(ctx, agentID, runtimeID, sessionID, sessionPath, generation)
}
