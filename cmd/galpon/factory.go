package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/config"
	factorysvc "github.com/matipan/galpon/internal/factory"
	"github.com/matipan/galpon/internal/tui"
)

func factoryPaths(cfg config.Config) (dir, socket, pid, lock, logPath string) {
	dir = filepath.Join(cfg.StateDir, "factory")
	return dir, filepath.Join(dir, "factory.sock"), filepath.Join(dir, "factory.pid"), filepath.Join(dir, "factory.lock"), filepath.Join(dir, "factory.log")
}

func factoryCommand(cfg config.Config, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "serve":
			return serveFactory(cfg)
		case "start":
			_, err := ensureFactory(cfg)
			if err == nil {
				fmt.Println("Factory is running")
			}
			return err
		case "stop":
			err := stopFactory(cfg)
			if err == nil {
				fmt.Println("Factory stopped")
			}
			return err
		case "restart":
			if err := stopFactory(cfg); err != nil && !strings.Contains(err.Error(), "not running") {
				return err
			}
			_, err := ensureFactory(cfg)
			if err == nil {
				fmt.Println("Factory restarted")
			}
			return err
		case "status":
			return factoryStatus(cfg)
		case "help", "--help", "-h":
			fmt.Println("Usage: galpon factory [start|stop|restart|status]")
			return nil
		default:
			return fmt.Errorf("unknown factory command %q", args[0])
		}
	}
	galpon, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	factoryClient, err := ensureFactory(cfg)
	if err != nil {
		return err
	}
	dashboard, err := galpon.Dashboard(context.Background())
	if err != nil {
		return err
	}
	return tui.RunFactory(factoryClient, galpon, dashboard, cfg.StateDir)
}
func ensureFactory(cfg config.Config) (*factorysvc.Client, error) {
	_, socket, _, lockPath, logPath := factoryPaths(cfg)
	client := factorysvc.NewClient(socket)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	serviceVersion, probeErr := client.Version(ctx)
	cancel()
	if probeErr == nil && serviceVersion == factorysvc.APIVersion {
		return client, nil
	}
	if probeErr == nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		stopErr := client.Shutdown(stopCtx)
		stopCancel()
		if stopErr != nil {
			return nil, fmt.Errorf("factory service API %d is stale and could not stop: %w", serviceVersion, stopErr)
		}
		if !waitFactoryStopped(client, lockPath, 5*time.Second) {
			return nil, fmt.Errorf("stale factory service did not stop")
		}
	}
	if _, err := ensureDaemon(cfg); err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "factory", "serve")
	cmd.Env = environmentWithout(os.Environ(), "GALPON_CHECKPOINT_PASSPHRASE")
	cmd.Stdout = file
	cmd.Stderr = file
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		_ = file.Close()
		return nil, err
	}
	_ = cmd.Process.Release()
	_ = file.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
		serviceVersion, err = client.Version(ctx)
		cancel()
		if err == nil && serviceVersion == factorysvc.APIVersion {
			return client, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("factory did not start; see %s", logPath)
}
func serveFactory(cfg config.Config) error {
	dir, socket, pidPath, lockPath, logPath := factoryPaths(cfg)
	if err := os.MkdirAll(filepath.Join(dir, "review-runs"), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("factory is already running")
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	logger := log.New(logFile, "", log.Ldate|log.Ltime|log.Lmicroseconds)
	galpon := app.NewClient(cfg.Socket)
	ctx, cancelHealth := context.WithTimeout(context.Background(), 2*time.Second)
	err = galpon.Health(ctx)
	cancelHealth()
	if err != nil {
		return fmt.Errorf("galpon daemon is not running: %w", err)
	}
	store, err := factorysvc.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	reconciler := &factorysvc.Reconciler{Store: store, Galpon: galpon, GitHub: factorysvc.GH{}}
	server := factorysvc.NewServer(store, reconciler)
	if err = os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(pidPath) }()
	defer func() { _ = os.Remove(socket) }()
	go reconciler.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Printf("Factory started")
	return server.Serve(socket)
}
func stopFactory(cfg config.Config) error {
	_, socket, _, lockPath, _ := factoryPaths(cfg)
	client := factorysvc.NewClient(socket)
	healthCtx, healthCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	running := client.Health(healthCtx) == nil
	healthCancel()
	if !running {
		return fmt.Errorf("factory is not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := client.Shutdown(ctx)
	cancel()
	if err != nil {
		return err
	}
	if waitFactoryStopped(client, lockPath, 5*time.Second) {
		return nil
	}
	return fmt.Errorf("factory did not stop")
}

func waitFactoryStopped(client *factorysvc.Client, lockPath string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := client.Health(ctx)
		cancel()
		if err != nil && factoryLockAvailable(lockPath) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func factoryLockAvailable(path string) bool {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return true
}

func factoryStatus(cfg config.Config) error {
	_, socket, _, _, _ := factoryPaths(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	version, err := factorysvc.NewClient(socket).Version(ctx)
	if err != nil {
		fmt.Println("Factory: stopped")
		return nil
	}
	if version != factorysvc.APIVersion {
		fmt.Printf("Factory: restart required (service API %d, expected %d)\n", version, factorysvc.APIVersion)
		return nil
	}
	fmt.Println("Factory: running")
	return nil
}
