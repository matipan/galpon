package screen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/matipan/galpon/internal/herdrapp"
	"golang.org/x/term"
)

// sshOptions make ssh stop with an error instead of asking for a password or
// a host key. herdr-core needs the same login to reach the host.
var sshOptions = []string{
	"-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
	"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4",
	// No "Connection to ... closed." after a command with a terminal.
	"-o", "LogLevel=ERROR",
}

// Codes of ssh and of the login shell of the host.
const (
	// CodeUnreachable is the exit code of ssh when it cannot log in.
	CodeUnreachable = 255
	// CodeNotFound is the exit code of the shell when the program is missing.
	CodeNotFound = 127
)

// Run runs galpon with args on the host, with the terminal of this process,
// and returns the exit code: the code of galpon, or CodeUnreachable when ssh
// cannot log in. The host gets a terminal when stdin and stdout are
// terminals. During the command, the screen names and focuses the terminals
// that the host opens, and opens its web links. build is the build of the
// galpon of the screen. When the host has another build, Run calls update
// once, at the start of the command.
func (s Setting) Run(args []string, build string, update func()) (int, error) {
	request := Request{Args: args, Env: map[string]string{}, Token: newToken(), Build: build}
	for _, name := range DisplayVariables {
		if value, ok := os.LookupEnv(name); ok {
			request.Env[name] = value
		}
	}
	tty := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	options := append([]string(nil), sshOptions...)
	if tty {
		options = append(options, "-tt")
	} else {
		options = append(options, "-T")
	}
	line := quote(s.Galpon) + " screen run " + request.Encode()
	command := exec.Command("ssh", append(options, "--", destination(s.Target), line)...)
	command.Stdin = os.Stdin
	read, write, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	// The host sends its messages on stderr. With a terminal, ssh gives
	// stderr in the output.
	output := os.Stderr
	command.Stdout, command.Stderr = os.Stdout, write
	if tty {
		output = os.Stdout
		command.Stdout, command.Stderr = write, os.Stderr
	}
	// Ctrl-C goes to ssh too, and ssh stops the command. Wait for ssh.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGQUIT)
	defer signal.Stop(interrupts)
	if err := command.Start(); err != nil {
		_ = read.Close()
		_ = write.Close()
		return 0, fmt.Errorf("run ssh: %w", err)
	}
	_ = write.Close()
	var shows sync.WaitGroup
	var updateOnce sync.Once
	_ = Filter(output, read, request.Token, func(message Message) {
		if message.Update && update != nil {
			updateOnce.Do(update)
		}
		if message.Place != nil {
			place := message.Place.On(s.Host)
			shows.Add(1)
			go func() {
				defer shows.Done()
				herdrapp.Adapter{}.Show(place)
			}()
		}
		if message.URL != "" {
			if err := openLocally(message.URL); err != nil {
				fmt.Fprintln(os.Stderr, "galpon:", err)
			}
		}
	})
	_ = read.Close()
	waitErr := command.Wait()
	shows.Wait()
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return 0, waitErr
	}
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal()), nil
	}
	return command.ProcessState.ExitCode(), nil
}

// Errors of FindHost.
var (
	// ErrNoGalpon means that the host has no galpon at the path, or on the
	// PATH of its login shell.
	ErrNoGalpon = errors.New("no galpon")
	// ErrOldGalpon means that the galpon of the host cannot work for a
	// screen.
	ErrOldGalpon = errors.New("galpon is too old for a screen")
)

// lookup finds galpon on the host as herdr-core finds hc there: on the PATH of
// the login shell, else in ~/.local/bin. It prints the path as it found it,
// then the version. The single quotes read the same in sh, zsh and fish.
const lookup = `sh -c 'p=$(command -v galpon) || p=$HOME/.local/bin/galpon; printf "%s\n" "$p"; exec "$p" version --json'`

// FindHost returns the galpon command of the host and its information. With
// a fixed path, it reads the galpon at that path. Else the login shell of the
// host finds galpon, as for a person who logs in there. The command can be a
// shim of a version manager, and Info.Executable is the program that it runs.
func FindHost(target, fixed string) (string, Info, error) {
	if fixed != "" {
		out, err := runLine(target, quote(fixed)+" version --json")
		if err != nil {
			return "", Info{}, err
		}
		info, err := readInfo(out)
		return fixed, info, err
	}
	out, err := runLine(target, lookup)
	if err != nil {
		return "", Info{}, err
	}
	path, rest, _ := strings.Cut(string(out), "\n")
	info, err := readInfo([]byte(rest))
	if err != nil {
		return "", Info{}, err
	}
	if !strings.HasPrefix(path, "/") {
		return "", Info{}, fmt.Errorf("the host found galpon at %q, which is not an absolute path", path)
	}
	return path, info, nil
}

// CopyGalpon copies the galpon executable of the host over dest, the galpon
// executable of the screen. The copy goes to a new file in the folder of dest
// first. Only a copy that runs and has the build of info replaces dest, in
// one step. A running galpon continues with the old file.
func CopyGalpon(target string, info Info, dest string) error {
	if !Quotable(info.Executable) || !strings.HasPrefix(info.Executable, "/") {
		return fmt.Errorf("cannot copy galpon from %q", info.Executable)
	}
	temporary, err := os.CreateTemp(filepath.Dir(dest), ".galpon-update-")
	if err != nil {
		return fmt.Errorf("cannot replace %s: %w", dest, err)
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	options := append(append([]string(nil), sshOptions...), "-T", "--", destination(target), "cat "+quote(info.Executable))
	command := exec.Command("ssh", options...)
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = temporary, &stderr
	runErr := command.Run()
	closeErr := temporary.Close()
	if runErr != nil {
		return fmt.Errorf("copy galpon from the host: %w: %s", runErr, lastLine(stderr.String()))
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Chmod(temporary.Name(), 0o755); err != nil {
		return err
	}
	out, err := exec.Command(temporary.Name(), "version", "--json").Output()
	if err != nil {
		return fmt.Errorf("the copy of galpon does not run: %w", err)
	}
	copied, err := readInfo(out)
	if err != nil || copied.Build != info.Build {
		return fmt.Errorf("the copy of galpon is not the build of the host")
	}
	return os.Rename(temporary.Name(), dest)
}

// ReadStatus returns the galpon of the host and the state of its daemon:
// running or stopped.
func (s Setting) ReadStatus() (Info, string, error) {
	g := quote(s.Galpon)
	out, err := runLine(s.Target, g+" version --json; "+g+" daemon status")
	if err != nil {
		return Info{}, "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	var info Info
	if decoder.Decode(&info) != nil || info.Commit == "" || info.Build == "" {
		return Info{}, "", ErrOldGalpon
	}
	rest, _ := io.ReadAll(decoder.Buffered())
	return info, strings.TrimSpace(string(rest)), nil
}

// readInfo reads `galpon version --json`. A galpon without a build cannot
// update a screen, so it is too old.
func readInfo(out []byte) (Info, error) {
	var info Info
	if json.Unmarshal(out, &info) != nil || info.Commit == "" || info.Build == "" || info.Executable == "" {
		return Info{}, ErrOldGalpon
	}
	return info, nil
}

// runLine runs a shell line on the host without a terminal, and returns its
// output.
func runLine(target, line string) ([]byte, error) {
	options := append(append([]string(nil), sshOptions...), "-T", "--", destination(target), line)
	command := exec.Command("ssh", options...)
	var stdout, stderr bytes.Buffer
	command.Stdin, command.Stdout, command.Stderr = nil, &stdout, &stderr
	err := command.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), nil
	case !errors.As(err, &exitErr):
		return nil, fmt.Errorf("run ssh: %w", err)
	case exitErr.ExitCode() == CodeUnreachable:
		return nil, fmt.Errorf("ssh %s cannot log in: %s", target, lastLine(stderr.String()))
	case exitErr.ExitCode() == CodeNotFound:
		return nil, ErrNoGalpon
	}
	detail := lastLine(stderr.String() + "\n" + stdout.String())
	if strings.Contains(detail, "unknown command") || strings.Contains(detail, "flag provided but not defined") {
		return nil, ErrOldGalpon
	}
	return nil, fmt.Errorf("galpon on the host exited with code %d: %s", exitErr.ExitCode(), detail)
}

// destination returns how ssh reaches a herdr-core machine target.
// herdr-core takes "user@host:2222", which ssh reads as a host name, so it
// becomes "ssh://user@host:2222", as herdr-core does.
func destination(target string) string {
	if strings.HasPrefix(target, "ssh://") {
		return target
	}
	host := target
	if at := strings.LastIndex(target, "@"); at >= 0 {
		host = target[at+1:]
	}
	name, port, ok := strings.Cut(host, ":")
	if !ok || name == "" || port == "" || strings.Trim(port, "0123456789") != "" {
		return target
	}
	return "ssh://" + target
}

// Quotable reports whether a path can go in a command line for the host.
func Quotable(path string) bool {
	return !strings.ContainsAny(path, "'\\\n\r")
}

// quote puts a path in single quotes. Quotable paths read the same in sh, zsh
// and fish.
func quote(path string) string { return "'" + path + "'" }

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
