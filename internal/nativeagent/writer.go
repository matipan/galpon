package nativeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const writerOwnerEnvironment = "GALPON_NATIVE_WRITER_FILE"
const writerHandoffWait = 8 * time.Second

type writerOwner struct {
	Group     int    `json:"group"`
	RuntimeID string `json:"runtimeId"`
	BootID    string `json:"bootId,omitempty"`
}

func writerOwnerPath(root string) string { return filepath.Join(root, "native-writer.json") }

func acquireSessionLock(ctx context.Context, lock *os.File, ownerPath string) error {
	deadline := time.Now().Add(writerHandoffWait)
	for {
		err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("another native runtime owns this agent's session; inspect %s: %w", ownerPath, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return awaitWriterExit(ctx, ownerPath, deadline)
}

// A supervisor can die without running its cleanup. Never start a replacement
// while its recorded process group exists. Do not signal a possibly reused ID.
func awaitWriterExit(ctx context.Context, path string, deadline time.Time) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var owner writerOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return fmt.Errorf("read native writer owner: %w", err)
	}
	boot, err := writerBootID()
	if err != nil {
		return err
	}
	if boot != "" && owner.BootID != "" && boot != owner.BootID {
		return os.Remove(path)
	}
	if owner.Group <= 1 {
		return fmt.Errorf("invalid native writer process group")
	}
	for {
		err := unix.Kill(-owner.Group, 0)
		if errors.Is(err, unix.ESRCH) {
			return os.Remove(path)
		}
		if err != nil && !errors.Is(err, unix.EPERM) {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("native writer process group %d from runtime %s still exists; inspect the group and its owner record before reopening this agent", owner.Group, owner.RuntimeID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// RunWriter cannot exec the harness until its supervisor has synced the owner
// record. It receives only a one-use start pipe, never the session lock.
func RunWriter(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("native writer executable is required")
	}
	gate := os.NewFile(3, "native-start-gate")
	if gate == nil {
		return fmt.Errorf("native writer start pipe is unavailable")
	}
	var permit [1]byte
	_, err := io.ReadFull(gate, permit[:])
	_ = gate.Close()
	if err != nil || permit[0] != 'G' {
		return fmt.Errorf("native writer was not admitted")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return unix.Exec(args[0], args, os.Environ())
}
