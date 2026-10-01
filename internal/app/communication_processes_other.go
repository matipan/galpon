//go:build !linux && !darwin

package app

func communicationAgentProcesses(string, string) ([]communicationAgentProcess, error) {
	return nil, errProcessInspectionUnsupported
}
