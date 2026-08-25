//go:build !linux

package app

import (
	"os"
	"os/exec"
)

func configureManagedBackgroundProcess(_ *exec.Cmd) {}

func signalManagedBackgroundProcess(command *exec.Cmd, signal os.Signal) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Signal(signal)
}

func killManagedBackgroundProcess(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
