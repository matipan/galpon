package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/herdrapp"
	"github.com/matipan/galpon/internal/screen"
)

// A screen uses the same galpon build as its host. When the host has another
// build, the screen copies the galpon of the host over its own galpon, and
// installs the Galpon mods of that build. The app reloads them at once.
//
// galpon screen update starts an update in its own process, and the
// forwarded commands, the mods and galpon status start it. galpon connect
// updates in the foreground.

// updateRetry is the least time between an update that failed and the next
// update that starts by itself. An update that cannot work, for example for
// another CPU, then does not start again and again.
const updateRetry = time.Minute

// updateState is the result of the last update, in screen-update.json.
type updateState struct {
	// At is when the last update started, in Unix seconds.
	At int64 `json:"at"`
	// Build is the build of the host after the last update. It is empty
	// while an update runs, and after an update that could not read it.
	Build string `json:"build,omitempty"`
	Error string `json:"error,omitempty"`
}

// waits reports whether a new update must wait: an update started less than
// updateRetry ago, and it runs or it failed.
func (s updateState) waits() bool {
	return time.Since(time.Unix(s.At, 0)) < updateRetry && (s.Build == "" || s.Error != "")
}

func updatePath(cfg config.Config, extension string) string {
	return filepath.Join(cfg.StateDir, "screen-update."+extension)
}

func readUpdateState(cfg config.Config) updateState {
	var state updateState
	data, err := os.ReadFile(updatePath(cfg, "json"))
	if err == nil {
		_ = json.Unmarshal(data, &state)
	}
	return state
}

func writeUpdateState(cfg config.Config, state updateState) {
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	temporary := updatePath(cfg, "json.new")
	if os.WriteFile(temporary, data, 0o600) == nil {
		_ = os.Rename(temporary, updatePath(cfg, "json"))
	}
}

// lockUpdate takes the update lock. It returns false when another update
// holds it.
func lockUpdate(cfg config.Config) (*os.File, bool, error) {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, false, err
	}
	file, err := os.OpenFile(updatePath(cfg, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, false, nil
	}
	return file, true, nil
}

func unlockUpdate(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

// startScreenUpdate starts an update in its own process, which continues
// after this command and the app task end. It returns false when an update
// runs, or when the last update failed less than updateRetry ago.
func startScreenUpdate(cfg config.Config) (bool, error) {
	lock, free, err := lockUpdate(cfg)
	if err != nil || !free {
		return false, err
	}
	unlockUpdate(lock)
	if readUpdateState(cfg).waits() {
		return false, nil
	}
	// The update runs from now, so that another command does not start one
	// too.
	writeUpdateState(cfg, updateState{At: time.Now().Unix()})
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	// Two commands can start an update at the same time. The update that
	// gets the lock empties the log.
	logFile, err := os.OpenFile(updatePath(cfg, "log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	defer func() { _ = logFile.Close() }()
	command := exec.Command(executable, "screen", "update", "--wait")
	command.Env = herdrapp.WithoutScope(os.Environ())
	command.Stdout, command.Stderr = logFile, logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return false, err
	}
	_ = command.Process.Release()
	return true, nil
}

// runScreenUpdate updates this screen now, and records the result. It does
// nothing when another update runs. An update that started by itself empties
// its log first.
func runScreenUpdate(cfg config.Config, out io.Writer, installMods, started bool) error {
	lock, free, err := lockUpdate(cfg)
	if err != nil {
		return err
	}
	if !free {
		_, _ = fmt.Fprintln(out, "Another galpon updates this computer now.")
		return nil
	}
	defer unlockUpdate(lock)
	if started {
		_ = os.Truncate(updatePath(cfg, "log"), 0)
		_, _ = fmt.Fprintf(out, "Update at %s\n", time.Now().Format(time.RFC3339))
	}
	state := updateState{At: time.Now().Unix()}
	writeUpdateState(cfg, state)
	state.Build, err = updateScreen(out, installMods)
	if err != nil {
		state.Error = err.Error()
	}
	writeUpdateState(cfg, state)
	return err
}

// updateScreen makes this screen use the galpon build of its host, and
// returns that build. It finds galpon on the host again, copies it when its
// build is another one, and then installs the mods of the new galpon. With
// installMods, it installs the mods also when nothing changed.
func updateScreen(out io.Writer, installMods bool) (string, error) {
	setting, ok, err := screen.Load()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("this computer is not a screen")
	}
	fixed := ""
	if setting.Fixed {
		fixed = setting.Galpon
	}
	path, info, err := screen.FindHost(setting.Target, fixed)
	switch {
	case errors.Is(err, screen.ErrNoGalpon):
		return "", fmt.Errorf("cannot find galpon on %s; run galpon connect %s --host-galpon <path>", setting.Host, setting.Host)
	case errors.Is(err, screen.ErrOldGalpon):
		return "", fmt.Errorf("galpon on %s is too old for a screen", setting.Host)
	case err != nil:
		return "", err
	}
	if !screen.Quotable(path) {
		return "", fmt.Errorf("galpon on %s is at %q; give a path without quotes or backslashes with galpon connect --host-galpon", setting.Host, path)
	}
	moved := path != setting.Galpon || info.OS != setting.OS || info.Arch != setting.Arch
	if moved {
		setting.Galpon, setting.OS, setting.Arch = path, info.OS, info.Arch
		if err := screen.Save(setting); err != nil {
			return "", err
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	if info.Build != buildIdentity() {
		if info.OS != runtime.GOOS || info.Arch != runtime.GOARCH {
			byHand := fmt.Errorf("galpon on %s is for %s/%s, and this computer is %s/%s; install galpon %s (commit %s) on this computer by hand",
				setting.Host, info.OS, info.Arch, runtime.GOOS, runtime.GOARCH, info.Version, shortCommit(info.Commit))
			// galpon connect installs the mods of this galpon also then.
			if installMods {
				if err := installScreenMods(out, executable); err != nil {
					return info.Build, err
				}
			}
			return info.Build, byHand
		}
		_, _ = fmt.Fprintf(out, "Copying galpon from %s to %s…\n", setting.Host, executable)
		if err := screen.CopyGalpon(setting.Target, info, executable); err != nil {
			return info.Build, err
		}
		_, _ = fmt.Fprintf(out, "This computer has the galpon of %s now: %s, commit %s.\n", setting.Host, info.Version, shortCommit(info.Commit))
	}
	home, err := herdrapp.Home()
	if err != nil {
		return info.Build, err
	}
	// After a copy, the mods have the old build.
	if !installMods && !moved && herdrapp.InstalledBuild(home) == info.Build {
		return info.Build, nil
	}
	return info.Build, installScreenMods(out, executable)
}

// installScreenMods installs the Galpon mods of the galpon at executable. A
// new process installs them, so that they come from a galpon that an update
// copied. Without the app, it only tells how to add them later.
func installScreenMods(out io.Writer, executable string) error {
	home, err := herdrapp.Home()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(home, "keys.json")); err != nil {
		_, _ = fmt.Fprintln(out, "Open the Herdr app once, then run galpon herdr-app install to add the Galpon mods.")
		return nil
	}
	install := exec.Command(executable, "herdr-app", "install")
	install.Stdout, install.Stderr = out, out
	if err := install.Run(); err != nil {
		return fmt.Errorf("install the Galpon mods: %w", err)
	}
	return nil
}

// buildIdentity identifies this galpon program: its Go build ID, or else its
// commit.
func buildIdentity() string {
	if executable, err := os.Executable(); err == nil {
		if id, err := screen.BuildID(executable); err == nil {
			return id
		}
	}
	return buildCommit()
}
