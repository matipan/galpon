package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/herdrapp"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/screen"
)

// exitError ends galpon with code, after its message, if it has one.
type exitError struct {
	code    int
	message string
}

func (e exitError) Error() string { return e.message }

// screenRoute runs the command on the host when this computer is a screen,
// and returns true. It returns false for a command that runs on this
// computer.
func screenRoute(args []string) (bool, error) {
	// The host runs the commands of a screen here.
	if screen.Active() || runsOnScreen(args) {
		return false, nil
	}
	setting, ok, err := screen.Load()
	if err != nil || !ok {
		return err != nil, err
	}
	if len(args) > 0 {
		switch args[0] {
		case "serve", "pi", "runtime", "screen":
			return true, fmt.Errorf("this computer is a screen of %s, and Galpon and its agents run there; run galpon disconnect to run Galpon on this computer", setting.Host)
		}
	}
	code, err := setting.Run(args)
	switch {
	case err != nil:
		return true, err
	case code == 0:
		return true, nil
	case code == screen.CodeUnreachable:
		return true, exitError{code, fmt.Sprintf("cannot reach %s with ssh %s; run galpon status", setting.Host, setting.Target)}
	case code == screen.CodeNotFound:
		return true, exitError{code, fmt.Sprintf("galpon is not at %s on %s any more; run galpon connect %s again", setting.Galpon, setting.Host, setting.Host)}
	}
	return true, exitError{code: code}
}

// runsOnScreen reports whether a screen runs the command itself.
func runsOnScreen(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "connect", "status", "disconnect", "version", "--version", "-v", "help", "--help", "-h":
		return true
	case "herdr-app":
		return len(args) > 1 && args[1] == "install"
	}
	return false
}

// screenCommand runs a command of a screen on this computer, the host:
//
//	screen run <request>
//
// The screen runs it over SSH, and the request holds the command.
func screenCommand(args []string) error {
	if len(args) != 2 || args[0] != "run" {
		return fmt.Errorf("usage: galpon screen run <request>; a screen runs it on its host")
	}
	if _, ok, _ := screen.Load(); ok {
		return fmt.Errorf("this computer is a screen of another computer, so it does not run the commands of a screen")
	}
	request, err := screen.DecodeRequest(args[1])
	if err != nil {
		return err
	}
	if len(request.Args) > 0 && request.Args[0] == "screen" {
		return fmt.Errorf("a screen cannot run galpon screen")
	}
	if err := os.Setenv(screen.Variable, request.Token); err != nil {
		return err
	}
	for _, name := range screen.DisplayVariables {
		if value, ok := request.Env[name]; ok {
			_ = os.Setenv(name, value)
		}
	}
	herdrAppScreen = true
	return run(request.Args)
}

// screenClient makes the agents that client opens show on the screen that
// runs this command. Without a screen, it changes nothing.
func screenClient(client *app.Client) *app.Client {
	if !screen.Active() {
		return client
	}
	client.Screen = true
	client.Opened = func(agent model.Agent) {
		dashboard, err := herdrAppDashboard(client)
		if err != nil {
			return
		}
		if place, ok := herdrAppAgentPlacement(dashboard, agent); ok {
			_ = screen.Send(screen.Message{Place: &place})
		}
	}
	return client
}

// screenAdapter opens project terminals for the screen that runs this
// command.
func screenAdapter() herdrapp.Adapter {
	return herdrapp.Adapter{Sync: true, Screen: true, Placed: func(place herdrapp.Placement) {
		_ = screen.Send(screen.Message{Place: &place})
	}}
}

// startDaemonInCore starts the daemon through herdr-core on this computer. A
// screen runs its commands over SSH, and an SSH login does not have all of the
// desktop session, for example the SSH agent. herdr-core gives the daemon the
// environment that it gives agent terminals. The error wraps
// herdrapp.ErrCore when herdr-core cannot run galpon.
func startDaemonInCore() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	var settings []string
	for _, value := range daemonEnvironment(os.Environ()) {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "GALPON_") || strings.HasPrefix(key, "PI_") || key == "XDG_CONFIG_HOME" || key == "HC_SESSION" {
			settings = append(settings, value)
		}
	}
	return herdrapp.RunInCore(context.Background(), 10*time.Minute, settings, executable, "daemon", "start")
}

// connectCommand makes this computer a screen of a host:
//
//	connect <machine> [--host-galpon <path>]
//
// <machine> is the herdr-core machine name of the host. Without
// --host-galpon, the login shell of the host finds galpon.
func connectCommand(cfg config.Config, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: galpon connect <machine> [--host-galpon <path>]")
	}
	name := args[0]
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	hostGalpon := fs.String("host-galpon", "", "absolute path of galpon on the host")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: galpon connect <machine> [--host-galpon <path>]")
	}
	if *hostGalpon != "" && (!filepath.IsAbs(*hostGalpon) || !screen.Quotable(*hostGalpon)) {
		return fmt.Errorf("--host-galpon needs an absolute path without quotes or backslashes")
	}
	// A computer that runs Galpon cannot also be a screen: its agents run
	// galpon here.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	running := app.NewClient(cfg.Socket).Health(ctx) == nil
	cancel()
	if running {
		return fmt.Errorf("Galpon runs on this computer, so it cannot be a screen of %s", name)
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, "galpon.db")); err == nil {
		return fmt.Errorf("this computer has Galpon data in %s, so it cannot be a screen of %s; remove that folder if you do not need it", cfg.StateDir, name)
	}
	machines, err := herdrapp.Machines()
	if err != nil {
		return fmt.Errorf("read the machines of herdr-core: %w", err)
	}
	var machine *herdrapp.Machine
	for index := range machines {
		if machines[index].Name == name {
			machine = &machines[index]
		}
	}
	switch {
	case machine == nil:
		return fmt.Errorf("herdr-core does not know %s. Add it in the Herdr app with Settings > Machines, or run: hc machine add <ssh target> --name %s", name, name)
	case machine.Via:
		return fmt.Errorf("herdr-core reaches %s with a command, not with ssh; galpon connect needs a machine that ssh reaches", name)
	}
	info, err := screen.ReadHost(machine.Target, *hostGalpon)
	switch {
	case errors.Is(err, screen.ErrNoGalpon) && *hostGalpon == "":
		return fmt.Errorf("cannot find galpon on %s; give its path with --host-galpon", name)
	case errors.Is(err, screen.ErrNoGalpon):
		return fmt.Errorf("%s has no galpon at %s", name, *hostGalpon)
	case errors.Is(err, screen.ErrOldGalpon):
		return fmt.Errorf("galpon on %s is too old for a screen; install this galpon version on %s first", name, name)
	case err != nil:
		return err
	}
	if !filepath.IsAbs(info.Executable) || !screen.Quotable(info.Executable) {
		return fmt.Errorf("galpon on %s is at %q; give a path without quotes or backslashes with --host-galpon", name, info.Executable)
	}
	setting := screen.Setting{Host: name, Target: machine.Target, Galpon: info.Executable, OS: info.OS, Arch: info.Arch}
	if err := screen.Save(setting); err != nil {
		return err
	}
	fmt.Printf("This computer is now a screen of %s. galpon commands run on %s with %s.\n", name, name, info.Executable)
	if info.Commit != buildCommit() {
		fmt.Printf("galpon on %s has commit %s, and this galpon has commit %s. Use the same galpon on the two computers.\n", name, shortCommit(info.Commit), shortCommit(buildCommit()))
	}
	if info.OS != runtime.GOOS || info.Arch != runtime.GOARCH {
		fmt.Printf("%s runs %s/%s, and this computer %s/%s, so a copy of galpon from %s cannot run here.\n", name, info.OS, info.Arch, runtime.GOOS, runtime.GOARCH, name)
	}
	home, err := herdrapp.Home()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(home, "keys.json")); err != nil {
		fmt.Println("Open the Herdr app once, then run galpon herdr-app install to add the Galpon mods.")
		return nil
	}
	return herdrAppInstall(nil)
}

// statusCommand tells where Galpon runs. On a screen, it reads the host.
func statusCommand(cfg config.Config, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: galpon status")
	}
	setting, ok, err := screen.Load()
	if err != nil {
		return err
	}
	if !ok {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		state := "stopped"
		if app.NewClient(cfg.Socket).Health(ctx) == nil {
			state = "running"
		}
		fmt.Printf("Galpon runs on this computer. Its daemon is %s.\n", state)
		return nil
	}
	fmt.Printf("This computer is a screen of %s. galpon commands run there with ssh %s.\n", setting.Host, setting.Target)
	info, state, err := setting.ReadStatus()
	switch {
	case errors.Is(err, screen.ErrNoGalpon):
		return fmt.Errorf("galpon is not at %s on %s any more; run galpon connect %s again", setting.Galpon, setting.Host, setting.Host)
	case errors.Is(err, screen.ErrOldGalpon):
		return fmt.Errorf("galpon on %s is too old for a screen; install this galpon version on %s", setting.Host, setting.Host)
	case err != nil:
		return err
	}
	fmt.Printf("%s: galpon %s, commit %s, at %s. Its daemon is %s.\n", setting.Host, info.Version, shortCommit(info.Commit), setting.Galpon, state)
	fmt.Printf("This computer: galpon %s, commit %s.\n", version, shortCommit(buildCommit()))
	if info.Commit != buildCommit() {
		fmt.Printf("The two versions are different. Copy galpon from %s to this computer again.\n", setting.Host)
	}
	return nil
}

// disconnectCommand makes this computer stop being a screen.
func disconnectCommand(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: galpon disconnect")
	}
	setting, _, _ := screen.Load()
	removed, err := screen.Remove()
	if err != nil {
		return err
	}
	if !removed {
		fmt.Println("This computer is not a screen.")
		return nil
	}
	host := setting.Host
	if host == "" {
		host = "its host"
	}
	fmt.Printf("This computer is not a screen of %s now. galpon commands run on this computer.\n", host)
	fmt.Printf("The Galpon mods in the Herdr app still use %s. Run galpon herdr-app install to make them use Galpon on this computer.\n", host)
	return nil
}

// versionCommand prints the version. --json also prints the system, the CPU
// and the executable, for a screen.
func versionCommand(args []string) error {
	switch {
	case len(args) == 0:
		fmt.Printf("galpon %s (commit %s)\n", version, buildCommit())
		return nil
	case len(args) == 1 && args[0] == "--json":
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			executable = resolved
		}
		printJSON(screen.Info{Version: version, Commit: buildCommit(), OS: runtime.GOOS, Arch: runtime.GOARCH, Executable: executable})
		return nil
	}
	return fmt.Errorf("usage: galpon version [--json]")
}

func shortCommit(commit string) string {
	if len(commit) > 12 && !strings.HasSuffix(commit, "-dirty") {
		return commit[:12]
	}
	if base, ok := strings.CutSuffix(commit, "-dirty"); ok && len(base) > 12 {
		return base[:12] + "-dirty"
	}
	return commit
}
