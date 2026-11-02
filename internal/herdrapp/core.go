package herdrapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Machine is another computer that herdr-core on this computer reaches. The
// app shows its terminals with IDs that have the prefix "<Name>/".
type Machine struct {
	Name string `json:"name"`
	// Target is how ssh reaches the machine. With Via, it is a command that
	// runs a shell line there instead.
	Target string `json:"target"`
	Via    bool   `json:"via"`
	// State is connecting, connected, needs_login or unreachable.
	State string `json:"state"`
}

// Machines returns the machines of herdr-core on this computer.
func Machines() ([]Machine, error) {
	out, err := exec.Command(binary("hc"), "machine", "ls", "--json").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("hc machine ls: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("hc machine ls: %w", err)
	}
	var list struct {
		Machines []Machine `json:"machines"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("hc machine ls: %w", err)
	}
	return list.Machines, nil
}

// HasMachine reports whether herdr-core on this computer knows the machine.
// An error of hc counts as no.
func HasMachine(name string) bool {
	machines, err := Machines()
	if err != nil {
		return false
	}
	for _, machine := range machines {
		if machine.Name == name {
			return true
		}
	}
	return false
}

// ErrCore means that herdr-core could not run a program.
var ErrCore = errors.New("herdr-core could not run the program")

// RunInCore runs argv through herdr-core on this computer (hc exec), and
// waits until it ends, at most timeout. herdr-core gives the program the
// environment of the desktop session, as it gives the terminals that it
// starts, and env on top. herdr-core ends the processes that the program
// starts when it ends, except processes without ScopeVariable. When
// herdr-core cannot run the program, the error wraps ErrCore.
func RunInCore(ctx context.Context, timeout time.Duration, env []string, argv ...string) error {
	// hc exec waits at most 600 seconds.
	seconds := min(max(int(timeout/time.Second), 1), 600)
	args := []string{"exec", "--json", "--timeout", strconv.Itoa(seconds)}
	for _, value := range env {
		args = append(args, "--env", value)
	}
	args = append(args, "--")
	args = append(args, argv...)
	command := exec.CommandContext(ctx, binary("hc"), args...)
	// hc exec takes the core of HC_SESSION. The identity of a terminal
	// would make the program a child of that terminal.
	command.Env = environment("HC_TERMINAL_ID", ScopeVariable)
	out, err := command.Output()
	var result struct {
		Code   *int   `json:"code"`
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
		TimedOut bool `json:"timed_out"`
	}
	if jsonErr := json.Unmarshal(out, &result); jsonErr != nil || result.Code == nil {
		var exitErr *exec.ExitError
		switch {
		case result.Error != nil:
			return fmt.Errorf("%w: %s", ErrCore, result.Error.Message)
		case errors.As(err, &exitErr):
			return fmt.Errorf("%w: %s", ErrCore, strings.TrimSpace(string(exitErr.Stderr)))
		case err != nil:
			return fmt.Errorf("%w: %v", ErrCore, err)
		}
		return fmt.Errorf("%w: hc exec printed no result", ErrCore)
	}
	if *result.Code == 0 && !result.TimedOut {
		return nil
	}
	stderr, _ := base64.StdEncoding.DecodeString(result.Stderr)
	stdout, _ := base64.StdEncoding.DecodeString(result.Stdout)
	detail := strings.TrimSpace(string(stderr) + "\n" + string(stdout))
	if result.TimedOut {
		return fmt.Errorf("%s did not end in %s: %s", argv[0], timeout, detail)
	}
	return fmt.Errorf("%s exited with code %d: %s", argv[0], *result.Code, detail)
}
