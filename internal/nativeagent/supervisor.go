package nativeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// A native writer's supervisor retains the session lock and watches a private
// pipe. Controller death closes that pipe, so the writer stops before the lock
// can be acquired by a replacement runtime. This also applies on macOS.
func startNativeChild(ctx context.Context, options launchOptions, native *exec.Cmd, pipeInput bool) (*childProcess, error) {
	read, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	wrapper := exec.Command(options.Executable, append([]string{"runtime", "child", "--"}, native.Args...)...)
	// Keep the supervisor outside the controller's terminal process group so
	// that terminal shutdown cannot prevent it from stopping the writer.
	wrapper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	wrapper.Dir = native.Dir
	ownerPath := writerOwnerPath(filepath.Join(options.Config.StateDir, "agents", options.Agent.ID))
	wrapper.Env = append(native.Env, writerOwnerEnvironment+"="+ownerPath)
	wrapper.Stdin, wrapper.Stdout, wrapper.Stderr = native.Stdin, native.Stdout, native.Stderr
	wrapper.ExtraFiles = []*os.File{options.SessionLock, read}
	process, err := startChild(ctx, wrapper, pipeInput, write)
	_ = read.Close()
	if err != nil {
		_ = write.Close()
		return nil, err
	}
	go func() { <-process.done; _ = write.Close() }()
	return process, nil
}

func SuperviseChild(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("native child executable is required")
	}
	lock, lifeline := os.NewFile(3, "session-lock"), os.NewFile(4, "controller-lifeline")
	if lock == nil || lifeline == nil {
		return fmt.Errorf("native child requires a session lock and controller pipe")
	}
	defer func() { _ = lock.Close(); _ = lifeline.Close() }()
	unix.CloseOnExec(3)
	unix.CloseOnExec(4)
	if _, err := lock.Stat(); err != nil {
		return fmt.Errorf("native session lock is unavailable: %w", err)
	}
	info, err := lifeline.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return fmt.Errorf("native controller pipe is unavailable")
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	go func() { var data [1]byte; _, _ = lifeline.Read(data[:]); cancel() }()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	ownerPath := os.Getenv(writerOwnerEnvironment)
	if ownerPath == "" {
		return fmt.Errorf("native writer owner path is unavailable")
	}
	boot, err := writerBootID()
	if err != nil {
		return err
	}
	gate, admit, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = gate.Close(); _ = admit.Close() }()
	command := exec.Command(executable, append([]string{"runtime", "writer", "--"}, args...)...)
	command.ExtraFiles = []*os.File{gate}
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	// A CLI launcher can start the real writer as a descendant. Stop the entire
	// group, not only the launcher, before releasing the session lock.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	watchWriterParent(command.SysProcAttr)
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		if err != nil {
			return fmt.Errorf("read native terminal process group: %w", err)
		}
		signal.Ignore(syscall.SIGTTOU)
		command.SysProcAttr.Foreground, command.SysProcAttr.Ctty = true, fd
		defer func() { _ = unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, foreground) }()
	}
	process, err := startChild(ctx, command, false)
	if err != nil {
		return err
	}
	defer process.close()
	_ = gate.Close()
	if err := atomicJSON(ownerPath, writerOwner{Group: process.command.Process.Pid, RuntimeID: os.Getenv("GALPON_RUNTIME_ID"), BootID: boot}); err != nil {
		return err
	}
	if _, err := admit.Write([]byte{'G'}); err != nil {
		return err
	}
	_ = admit.Close()
	return process.wait(ctx)
}
