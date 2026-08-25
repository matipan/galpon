//go:build linux

package app

import (
	"os"
	"os/exec"
	"syscall"
)

func configureManagedBackgroundProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

func signalManagedBackgroundProcess(command *exec.Cmd, signal os.Signal) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return syscall.Kill(-command.Process.Pid, signal.(syscall.Signal))
}

func killManagedBackgroundProcess(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}
