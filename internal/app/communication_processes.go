package app

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type communicationAgentProcess struct {
	PID     int
	Name    string
	AgentID string
}

// errProcessInspectionUnsupported means this operating system has no process
// inspection support in Galpon. Linux and macOS are supported.
var errProcessInspectionUnsupported = errors.New("process inspection is not supported on this operating system")

func (a *App) requireStoppedCommunicationProcesses() error {
	processes, err := communicationAgentProcesses(a.Config.StateDir, a.Config.Socket)
	if errors.Is(err, errProcessInspectionUnsupported) {
		// Do not block startup on a system that Galpon cannot inspect. The
		// operator must stop old Pi runtimes before the upgrade.
		if a.Logger != nil {
			a.Logger.Printf("communication upgrade cannot check for running agent runtimes: %v; stop all Galpon Pi runtimes before you restart the daemon", err)
		}
		return nil
	}
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

// A runtime is identified by its environment AND Galpon's Pi launch
// arguments. Tool subprocesses inherit the environment, but not the
// session-directory and extension arguments. Executable names alone cannot
// identify npm Pi, native Pi, and user-configured launch wrappers. Do not use
// database runtime IDs: stopped rows and old launch records are not proof that
// a process is alive, and a real Pi may outlive its registration.

// communicationRuntimeAgent returns the agent ID when a process environment
// belongs to a Pi runtime of the daemon at socket.
func communicationRuntimeAgent(environment []string, socket string) (string, bool) {
	matchedSocket, runtimeID, agentID := false, "", ""
	for _, field := range environment {
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
		return "", false
	}
	return agentID, true
}

// isCommunicationRuntimeCommand reports whether args are Galpon's Pi launch
// for the agent.
func isCommunicationRuntimeCommand(args []string, stateDir, agentID string) bool {
	return hasProcessArgument(args, "--session-dir", filepath.Join(stateDir, "agents", agentID, "sessions")) &&
		hasProcessArgument(args, "--extension", filepath.Join(stateDir, "runtime", "pi", "galpon.ts"))
}

func hasProcessArgument(args []string, flag, value string) bool {
	for index := 1; index < len(args); index++ {
		if args[index] == flag+"="+value || args[index] == flag && index+1 < len(args) && args[index+1] == value {
			return true
		}
	}
	return false
}

// parseProcessArguments decodes macOS KERN_PROCARGS2 data: an int32 argument
// count, the executable path, NUL padding, the arguments, and the environment
// up to an empty string. Strings after that empty string belong to the
// kernel, not to the environment. macOS runs only on little-endian CPUs.
func parseProcessArguments(data []byte) (args, environment []string, ok bool) {
	if len(data) < 4 {
		return nil, nil, false
	}
	count := int32(binary.LittleEndian.Uint32(data))
	rest := data[4:]
	end := bytes.IndexByte(rest, 0)
	if count < 0 || int(count) > len(rest) || end < 0 {
		return nil, nil, false
	}
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	next := func() (string, bool) {
		if len(rest) == 0 {
			return "", false
		}
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			value := string(rest)
			rest = nil
			return value, true
		}
		value := string(rest[:end])
		rest = rest[end+1:]
		return value, true
	}
	for range count {
		value, ok := next()
		if !ok {
			return nil, nil, false
		}
		args = append(args, value)
	}
	for {
		value, ok := next()
		if !ok || value == "" {
			return args, environment, true
		}
		environment = append(environment, value)
	}
}
