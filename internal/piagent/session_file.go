package piagent

import (
	"os"
	"path/filepath"
	"time"

	"github.com/matipan/galpon/internal/model"
)

// SessionFile gives the session file that Pi continues for agent: the newest
// file of the session ID of the agent in its session folder, as the launch
// command selects it. It gives "" when the agent has no Pi session yet.
func SessionFile(stateDir string, agent model.Agent) string {
	sessionID := agent.SessionID
	if sessionID == "" {
		sessionID = agent.ID
	}
	matches, _ := filepath.Glob(filepath.Join(stateDir, "agents", agent.ID, "sessions", "*_"+sessionID+".jsonl"))
	newest, newestAt := "", time.Time{}
	for _, path := range matches {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.ModTime().After(newestAt) {
			newest, newestAt = path, info.ModTime()
		}
	}
	return newest
}
