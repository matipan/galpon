package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type communicationAgentProcess struct {
	PID     int
	Name    string
	AgentID string
}

func (a *App) requireStoppedCommunicationProcesses() error {
	processes, err := communicationAgentProcesses(a.Config.StateDir, a.Config.Socket)
	if err != nil {
		return fmt.Errorf("inspect agent processes before communication upgrade: %w", err)
	}
	if len(processes) == 0 {
		return nil
	}
	const limit = 3
	var descriptions []string
	for _, process := range processes[:min(len(processes), limit)] {
		descriptions = append(descriptions, fmt.Sprintf("PID %d (%s; agent %s)", process.PID,
			boundedCommunicationAgentTitle(process.Name), boundedCommunicationAgentTitle(process.AgentID)))
	}
	if len(processes) > limit {
		descriptions = append(descriptions, fmt.Sprintf("and %d more", len(processes)-limit))
	}
	return fmt.Errorf("communication upgrade refused: %d agent runtime processes are still running: %s; stop these Pi runtimes, then restart the daemon", len(processes), strings.Join(descriptions, ", "))
}

// communicationAgentProcesses identifies runtimes by their environment AND
// Galpon's Pi launch arguments. Tool subprocesses inherit the environment, but
// not the session-directory and extension arguments. Executable names alone
// cannot identify npm Pi, native Pi, and user-configured launch wrappers.
// Do not use database runtime IDs: stopped rows and old launch records are not
// proof that a process is alive, and a real Pi may outlive its registration.
func communicationAgentProcesses(stateDir, socket string) ([]communicationAgentProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []communicationAgentProcess
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		dir := filepath.Join("/proc", entry.Name())
		content, err := os.ReadFile(filepath.Join(dir, "environ"))
		if err != nil {
			// A process may exit or deny inspection between these reads.
			continue
		}
		matchedSocket, runtimeID, agentID := false, "", ""
		for _, field := range strings.Split(string(content), "\x00") {
			key, value, _ := strings.Cut(field, "=")
			switch key {
			case "GALPON_SOCKET":
				matchedSocket = value == socket
			case "GALPON_RUNTIME_ID":
				runtimeID = value
			case "GALPON_AGENT_ID":
				agentID = value
			}
		}
		if !matchedSocket || runtimeID == "" || agentID == "" || agentID == "." || agentID == ".." || filepath.Base(agentID) != agentID {
			continue
		}
		command, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read command for PID %d: %w", pid, err)
		}
		args := strings.Split(strings.TrimSuffix(string(command), "\x00"), "\x00")
		if !hasProcessArgument(args, "--session-dir", filepath.Join(stateDir, "agents", agentID, "sessions")) ||
			!hasProcessArgument(args, "--extension", filepath.Join(stateDir, "runtime", "pi", "galpon.ts")) {
			continue
		}
		name := filepath.Base(args[0])
		if comm, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
			name = strings.TrimSpace(string(comm))
		}
		out = append(out, communicationAgentProcess{PID: pid, Name: name, AgentID: agentID})
	}
	return out, nil
}

func hasProcessArgument(args []string, flag, value string) bool {
	for index := 1; index < len(args); index++ {
		if args[index] == flag+"="+value || args[index] == flag && index+1 < len(args) && args[index+1] == value {
			return true
		}
	}
	return false
}
