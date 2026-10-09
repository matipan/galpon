package nativeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 4 && os.Args[1] == "runtime" && os.Args[3] == "--" {
		var err error
		switch os.Args[2] {
		case "child":
			err = SuperviseChild(context.Background(), os.Args[4:])
		case "writer":
			err = RunWriter(context.Background(), os.Args[4:])
		default:
			os.Exit(2)
		}
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testWriter(t *testing.T, script string) (*childProcess, *os.File, string, string) {
	t.Helper()
	root := t.TempDir()
	agentRoot := filepath.Join(root, "agents", "test")
	if err := os.MkdirAll(agentRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(agentRoot, "native.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", "-c", script)
	command.Dir = root
	command.Env = append(os.Environ(), "TEST_CHILD_PID="+filepath.Join(root, "child.pid"), "GALPON_RUNTIME_ID=test-writer")
	options := launchOptions{Config: config.Config{StateDir: root}, Agent: model.Agent{ID: "test"}, Executable: executable, SessionLock: lock}
	process, err := startNativeChild(t.Context(), options, command, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(process.close)
	ownerPath := writerOwnerPath(agentRoot)
	return process, lock, ownerPath, filepath.Join(root, "child.pid")
}

func savedPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 1 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native child did not start")
	return 0
}

func TestDetachedToolChildDoesNotRetainTheSessionLock(t *testing.T) {
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("setsid is not installed")
	}
	script := shellQuote(setsid) + " /bin/sh -c 'echo $$ > \"$TEST_CHILD_PID\"; exec sleep 60' & wait"
	process, lock, ownerPath, pidPath := testWriter(t, script)
	pid := savedPID(t, pidPath)
	t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
	process.close()
	_ = lock.Close()
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("detached tool did not survive the writer group: %v", err)
	}
	next, err := os.OpenFile(lock.Name(), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	if err := acquireSessionLock(t.Context(), next, ownerPath); err != nil {
		t.Fatalf("detached tool retained session ownership: %v", err)
	}
}

func TestSupervisorFailureFencesAnOrphanWriterGroup(t *testing.T) {
	process, lock, ownerPath, pidPath := testWriter(t, "sleep 60 & echo $! > \"$TEST_CHILD_PID\"; wait")
	_ = savedPID(t, pidPath)
	data, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	var owner writerOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Kill(-owner.Group, unix.SIGKILL) })
	_ = process.command.Process.Kill()
	<-process.done
	_ = lock.Close()
	next, err := os.OpenFile(lock.Name(), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := acquireSessionLock(ctx, next, ownerPath); err == nil {
		t.Fatal("replacement admitted while an orphan writer group was alive")
	}
}

func TestSessionLockWaitsForWriterHandoff(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "native.lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	if err := unix.Flock(int(first.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	second, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	result := make(chan error, 1)
	go func() { result <- acquireSessionLock(t.Context(), second, writerOwnerPath(root)) }()
	select {
	case err := <-result:
		t.Fatalf("handoff did not wait for the owner: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("handoff did not acquire the released lock")
	}
}
