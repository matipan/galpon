//go:build linux

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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
		agentID, ok := communicationRuntimeAgent(strings.Split(string(content), "\x00"), socket)
		if !ok {
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
		if !isCommunicationRuntimeCommand(args, stateDir, agentID) {
			executable, err := os.Readlink(filepath.Join(dir, "exe"))
			if err != nil || !isRetitledPiProcess(args, executable) {
				continue
			}
		}
		name := filepath.Base(args[0])
		if comm, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
			name = strings.TrimSpace(string(comm))
		}
		out = append(out, communicationAgentProcess{PID: pid, Name: name, AgentID: agentID})
	}
	return out, nil
}
