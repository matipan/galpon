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
		// operator must stop agent runtimes before the upgrade.
		if a.Logger != nil {
			a.Logger.Printf("communication upgrade cannot check for running agent runtimes: %v; stop all Galpon agent runtimes before you restart the daemon", err)
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
	return fmt.Errorf("communication upgrade refused: %d agent runtime processes are still running: %s; stop these agent runtimes, then restart the daemon", len(processes), strings.Join(descriptions, ", "))
}

// A runtime must carry Galpon's socket, runtime, and agent environment.
// Launch arguments distinguish it from tool subprocesses with inherited tags.
// Linux also checks the executable when Pi's process title replaces those
// arguments. Database runtime IDs are not proof that a process is alive, and
// a real runtime may outlive its registration.

// communicationRuntimeAgent returns the agent ID when a process environment
// belongs to an agent runtime of the daemon at socket.
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

// isCommunicationRuntimeCommand recognizes a Pi launch or a native writer's
// Galpon supervisor. MCP bridges and ordinary tool processes do not match.
func isCommunicationRuntimeCommand(args []string, stateDir, agentID string) bool {
	if len(args) >= 5 && args[1] == "runtime" {
		if (args[2] == "child" || args[2] == "writer") && args[3] == "--" {
			return true
		}
		if args[2] == "run" && args[len(args)-1] == agentID {
			return true
		}
	}
	return hasProcessArgument(args, "--session-dir", filepath.Join(stateDir, "agents", agentID, "sessions")) &&
		hasProcessArgument(args, "--extension", filepath.Join(stateDir, "runtime", "pi", "galpon.ts"))
}

// npm Pi sets process.title, which replaces Linux argv with pi or pi-rpc.
// This fallback requires an exact title and a Pi executable or interpreter;
// callers must also validate all Galpon runtime environment fields.
func isRetitledPiProcess(args []string, executable string) bool {
	if len(args) == 0 || args[0] != "pi" && args[0] != "pi-rpc" {
		return false
	}
	for _, arg := range args[1:] {
		if arg != "" {
			return false
		}
	}
	switch filepath.Base(strings.TrimSuffix(executable, " (deleted)")) {
	case "node", "nodejs", "bun", "pi":
		return true
	}
	return false
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
