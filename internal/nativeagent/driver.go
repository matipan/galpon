package nativeagent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

type nativeEvent struct {
	Kind, ID, Input, Final, Failure string
}

type launchOptions struct {
	Config                                 config.Config
	Agent                                  model.Agent
	Source                                 model.Agent
	Worktrees                              []model.Worktree
	Directory, Socket, TempDir, Executable string
	Background                             bool
	SessionLock                            *os.File
	Environment                            []string
	Events                                 chan<- nativeEvent
	Channel                                chan map[string]any
	Input                                  io.Reader
	Output, Errors                         io.Writer
}

type nativeInput struct {
	Text   string
	Images []model.ImageAttachment
}

type driver interface {
	Session() (id, path string)
	Submit(context.Context, nativeInput, string) (string, error)
	// Withdraw returns true only if the submission cannot start later.
	Withdraw(context.Context, string) (bool, error)
	Terminal(context.Context) error
	Close()
}

type childProcess struct {
	command  *exec.Cmd
	input    io.WriteCloser
	lifeline io.Closer
	done     chan struct{}
	mu       sync.Mutex
	err      error
}

func startChild(ctx context.Context, command *exec.Cmd, pipeInput bool, lifeline ...io.Closer) (*childProcess, error) {
	child := &childProcess{command: command, done: make(chan struct{})}
	if len(lifeline) > 0 {
		child.lifeline = lifeline[0]
	}
	var err error
	if pipeInput {
		child.input, err = command.StdinPipe()
		if err != nil {
			return nil, err
		}
	}
	if err := command.Start(); err != nil {
		if child.input != nil {
			_ = child.input.Close()
		}
		return nil, err
	}
	go func() {
		err := command.Wait()
		if command.SysProcAttr != nil && command.SysProcAttr.Setpgid {
			_ = unix.Kill(-command.Process.Pid, unix.SIGKILL)
		}
		child.mu.Lock()
		child.err = err
		child.mu.Unlock()
		close(child.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			child.close()
		case <-child.done:
		}
	}()
	return child, nil
}

func (p *childProcess) close() {
	if p.lifeline != nil {
		_ = p.lifeline.Close()
	}
	p.mu.Lock()
	if p.input != nil {
		_ = p.input.Close()
	}
	p.mu.Unlock()
	select {
	case <-p.done:
		return
	default:
	}
	stop := func(signal syscall.Signal) {
		if p.command.SysProcAttr != nil && p.command.SysProcAttr.Setpgid {
			_ = unix.Kill(-p.command.Process.Pid, signal)
		} else {
			_ = p.command.Process.Signal(signal)
		}
	}
	stop(syscall.SIGINT)
	grace := 3 * time.Second
	if p.lifeline != nil {
		grace = 6 * time.Second
	}
	select {
	case <-p.done:
	case <-time.After(grace):
		stop(syscall.SIGKILL)
		<-p.done
	}
}

func (p *childProcess) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	}
}

func shellQuote(value string) string {
	var result = "'"
	for _, char := range value {
		if char == '\'' {
			result += "'\"'\"'"
		} else {
			result += string(char)
		}
	}
	return result + "'"
}

func emit(ctx context.Context, events chan<- nativeEvent, event nativeEvent) {
	select {
	case events <- event:
	case <-ctx.Done():
	}
}

func executable(binary, fallback string) (string, error) {
	if binary == "" {
		binary = fallback
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("%s harness executable is unavailable: %w", fallback, err)
	}
	return path, nil
}
