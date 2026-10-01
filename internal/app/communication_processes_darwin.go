//go:build darwin

package app

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// macOS has no /proc. The kernel returns the exact arguments and environment
// of processes that belong to the same user through KERN_PROCARGS2.
func communicationAgentProcesses(stateDir, socket string) ([]communicationAgentProcess, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	self := os.Getpid()
	var out []communicationAgentProcess
	for _, process := range processes {
		pid := int(process.Proc.P_pid)
		if pid <= 0 || pid == self {
			continue
		}
		data, err := unix.SysctlRaw("kern.procargs2", pid)
		if err != nil {
			// The process exited, or it belongs to another user. Neither can
			// be a runtime of this user's daemon.
			continue
		}
		args, environment, ok := parseProcessArguments(data)
		if !ok {
			continue
		}
		agentID, ok := communicationRuntimeAgent(environment, socket)
		if !ok || !isCommunicationRuntimeCommand(args, stateDir, agentID) {
			continue
		}
		name := unix.ByteSliceToString(process.Proc.P_comm[:])
		if name == "" {
			name = filepath.Base(args[0])
		}
		out = append(out, communicationAgentProcess{PID: pid, Name: name, AgentID: agentID})
	}
	return out, nil
}
