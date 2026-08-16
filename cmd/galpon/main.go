package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/herdr"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/piagent"
	"github.com/matipan/galpon/internal/store"
	"github.com/matipan/galpon/internal/tui"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "galpon:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return runTUI(cfg)
	}
	switch args[0] {
	case "serve":
		return serve(cfg)
	case "companion":
		return companionCommand(cfg, args[1:])
	case "daemon":
		return daemonCommand(cfg, args[1:])
	case "repo":
		return repoCommand(cfg, args[1:])
	case "workspace":
		return workspaceCommand(cfg, args[1:])
	case "worktree":
		return worktreeCommand(cfg, args[1:])
	case "agent":
		return agentCommand(cfg, args[1:])
	case "cleanup":
		return cleanupCommand(cfg, args[1:])
	case "checkpoint":
		return checkpointCommand(cfg, args[1:])
	case "pi":
		return piCommand(cfg, args[1:])
	case "herdr":
		return herdrCommand(cfg, args[1:])
	case "snapshot":
		return snapshotCommand(cfg)
	case "version", "--version", "-v":
		fmt.Println("galpon", version)
		return nil
	case "help", "--help", "-h":
		usage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Galpon — a terminal-first workstation for durable coding agents

Usage:
  galpon                         Open the command center
  galpon daemon start|stop|status
  galpon companion [--listen 127.0.0.1:8420] [--origin URL]
  galpon repo add <path-or-url> [--title title] [--remote name=url] [--push-remote name]
  galpon repo remote add <repository> <name> <url> [--push-url url] [--push-default]
  galpon repo remote list <repository>
  galpon workspace create <title>
  galpon worktree create --repo <id> (--workspace <id> | --workspace-title <title>) [--remote name] [--ref ref]
  galpon worktree open <id>
  galpon agent create <title> --workspace <id> --repo <id> [--role role] [--context-agent id]
  galpon agent create <title> --workspace <id> --placement-agent <id> [--share]
  galpon agent create <title> --workspace <id> --cwd <absolute-path>
  galpon agent open <id>
  galpon agent send <id> <message>
  galpon agent show <id>
  galpon cleanup                     Permanently remove soft-deleted state and files
  galpon checkpoint create [--passphrase-file path] [--allow-local-remotes] <file>
  galpon checkpoint restore [--passphrase-file path] <file>
  galpon herdr install           Install the Ctrl-K popup binding
  galpon herdr config            Print the Herdr binding
`)
}

func runTUI(cfg config.Config) error {
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	return tui.Run(client, herdr.Adapter{Bin: cfg.HerdrBin})
}

func serve(cfg config.Config) error {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(cfg.StateDir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("galpon is already running")
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	logFile, err := os.OpenFile(filepath.Join(cfg.StateDir, "galpon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	logger := log.New(logFile, "", log.Ldate|log.Ltime|log.Lmicroseconds)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	application, err := app.Open(ctx, cfg, logger, herdr.Adapter{Bin: cfg.HerdrBin})
	if err != nil {
		return err
	}
	defer func() { _ = application.Close() }()
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "galpon.pid"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(filepath.Join(cfg.StateDir, "galpon.pid")) }()
	defer func() { _ = os.Remove(cfg.Socket) }()
	server := app.NewServer(application)
	go func() {
		<-ctx.Done()
		shutdownClient := app.NewClient(cfg.Socket)
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = shutdownClient.Shutdown(stopCtx)
	}()
	logger.Printf("Galpon %s listening on %s", version, cfg.Socket)
	return server.Serve(cfg.Socket)
}

func companionCommand(cfg config.Config, args []string) error {
	flags := flag.NewFlagSet("companion", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listen := flags.String("listen", "127.0.0.1:8420", "loopback listen address")
	origin := flags.String("origin", "", "exact allowed browser origin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("usage: galpon companion [--listen 127.0.0.1:8420] [--origin URL]")
	}
	listenAddress := strings.TrimSpace(*listen)
	host, _, err := net.SplitHostPort(listenAddress)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("companion --listen must use 127.0.0.1:PORT")
	}
	allowedOrigin := strings.TrimSpace(*origin)
	if allowedOrigin == "" {
		allowedOrigin = "http://" + listenAddress
	}
	originURL, err := url.ParseRequestURI(allowedOrigin)
	if err != nil || (originURL.Scheme != "http" && originURL.Scheme != "https") || originURL.Host == "" || originURL.Path != "" || originURL.RawQuery != "" || originURL.Fragment != "" || originURL.User != nil {
		return fmt.Errorf("companion --origin must be an exact HTTP or HTTPS origin")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	companionStore, err := store.Open(cfg.StateDir)
	if err != nil {
		return err
	}
	defer func() { _ = companionStore.Close() }()
	companionLog, err := os.OpenFile(filepath.Join(cfg.StateDir, "companion.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = companionLog.Close() }()
	server := app.NewCompanionServer(companionStore, client, allowedOrigin)
	server.Logger = log.New(companionLog, "", log.Ldate|log.Ltime|log.Lmicroseconds)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	localURL := "http://" + listenAddress
	fmt.Printf("Galpon companion listening at %s\n", localURL)
	if allowedOrigin != localURL {
		fmt.Printf("Allowed browser origin: %s\n", allowedOrigin)
	}
	return server.Serve(listenAddress)
}

func ensureDaemon(cfg config.Config) (*app.Client, error) {
	client := app.NewClient(cfg.Socket)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	err := client.Health(ctx)
	cancel()
	if err == nil {
		return client, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(cfg.StateDir, "galpon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	command := exec.Command(executable, "serve")
	command.Env = environmentWithout(os.Environ(), "GALPON_CHECKPOINT_PASSPHRASE")
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	_ = command.Process.Release()
	_ = logFile.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err = client.Health(ctx)
		cancel()
		if err == nil {
			return client, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("daemon did not start; see %s", filepath.Join(cfg.StateDir, "galpon.log"))
}

func environmentWithout(environment []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(environment))
	for _, value := range environment {
		if !strings.HasPrefix(value, prefix) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func daemonCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("daemon needs start, stop, or status")
	}
	client := app.NewClient(cfg.Socket)
	switch args[0] {
	case "start":
		_, err := ensureDaemon(cfg)
		if err == nil {
			fmt.Println("Galpon is running")
		}
		return err
	case "stop":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := client.Shutdown(ctx); err != nil {
			return err
		}
		fmt.Println("Galpon stopped")
		return nil
	case "status":
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := client.Health(ctx); err != nil {
			fmt.Println("stopped")
			return nil
		}
		fmt.Println("running")
		return nil
	default:
		return fmt.Errorf("unknown daemon command %q", args[0])
	}
}

func repoCommand(cfg config.Config, args []string) error {
	if len(args) >= 2 && args[0] == "add" {
		fs := flag.NewFlagSet("repo add", flag.ContinueOnError)
		title := fs.String("title", "", "display title")
		pushRemote := fs.String("push-remote", "", "default push remote")
		var values repeatedFlag
		fs.Var(&values, "remote", "additional remote in name=url form; repeatable")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		remotes := make([]model.RepositoryRemote, 0, len(values))
		for _, value := range values {
			name, remoteURL, ok := strings.Cut(value, "=")
			if !ok {
				return fmt.Errorf("--remote must use name=url")
			}
			remotes = append(remotes, model.RepositoryRemote{Name: name, FetchURL: remoteURL, PushURL: remoteURL})
		}
		client, err := ensureDaemon(cfg)
		if err != nil {
			return err
		}
		value, err := client.AddRepository(context.Background(), app.AddRepositoryRequest{Path: args[1], Title: *title, Remotes: remotes, PushRemote: *pushRemote})
		if err == nil {
			printJSON(value)
		}
		return err
	}
	if len(args) >= 3 && args[0] == "remote" && args[1] == "list" {
		client, err := ensureDaemon(cfg)
		if err != nil {
			return err
		}
		dashboard, err := client.Dashboard(context.Background())
		if err != nil {
			return err
		}
		repo := findRepository(dashboard.Repositories, args[2])
		if repo.ID == "" {
			return fmt.Errorf("repository not found: %s", args[2])
		}
		printJSON(map[string]any{"defaultRemote": repo.DefaultRemote, "pushRemote": repo.PushRemote, "remotes": repo.Remotes})
		return nil
	}
	if len(args) >= 5 && args[0] == "remote" && args[1] == "add" {
		fs := flag.NewFlagSet("repo remote add", flag.ContinueOnError)
		pushURL := fs.String("push-url", "", "separate push URL")
		pushDefault := fs.Bool("push-default", false, "use this remote for plain git push")
		if err := fs.Parse(args[5:]); err != nil {
			return err
		}
		client, err := ensureDaemon(cfg)
		if err != nil {
			return err
		}
		dashboard, err := client.Dashboard(context.Background())
		if err != nil {
			return err
		}
		repo := findRepository(dashboard.Repositories, args[2])
		if repo.ID == "" {
			return fmt.Errorf("repository not found: %s", args[2])
		}
		value, err := client.AddRepositoryRemote(context.Background(), repo.ID, args[3], args[4], *pushURL, *pushDefault)
		if err == nil {
			printJSON(value)
		}
		return err
	}
	return fmt.Errorf("usage: galpon repo add <path-or-url> [--title title] [--remote name=url] [--push-remote name]\n       galpon repo remote add <repository> <name> <url> [--push-url url] [--push-default]\n       galpon repo remote list <repository>")
}

type repeatedFlag []string

func (f *repeatedFlag) String() string { return strings.Join(*f, ",") }
func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func findRepository(items []model.Repository, query string) model.Repository {
	for _, repo := range items {
		if repo.ID == query || strings.EqualFold(repo.Title, query) {
			return repo
		}
	}
	return model.Repository{}
}

func findWorkspace(items []model.Workspace, query string) model.Workspace {
	for _, workspace := range items {
		if workspace.ID == query || strings.EqualFold(workspace.Title, query) {
			return workspace
		}
	}
	return model.Workspace{}
}

func findAgent(items []model.Agent, query string) model.Agent {
	for _, agent := range items {
		if agent.ID == query || strings.EqualFold(agent.Title, query) {
			return agent
		}
	}
	return model.Agent{}
}

func placementDescription(dashboard model.Dashboard, agent model.Agent) string {
	if agent.Placement.Type == "none" {
		return "an unmanaged directory at " + agent.Placement.CWD
	}
	parts := make([]string, 0, len(agent.Placement.Worktrees))
	for _, assignment := range agent.Placement.Worktrees {
		worktree, ok := dashboard.Worktree(assignment.WorktreeID)
		if !ok {
			continue
		}
		repository, _ := dashboard.Repository(worktree.RepositoryID)
		kind := "secondary"
		if assignment.Position == 0 {
			kind = "primary"
		}
		parts = append(parts, fmt.Sprintf("%s %s worktree at %s on branch %s (%s)", kind, repository.Title, worktree.Path, worktree.Branch, assignment.Mode))
	}
	return strings.Join(parts, "; ")
}

func workspaceCommand(cfg config.Config, args []string) error {
	if len(args) < 2 || args[0] != "create" {
		return fmt.Errorf("usage: galpon workspace create <title>")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	ws, err := client.CreateWorkspace(context.Background(), app.CreateWorkspaceRequest{Title: args[1]})
	if err == nil {
		printJSON(ws)
	}
	return err
}

func worktreeCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("worktree needs create or open")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("worktree create", flag.ContinueOnError)
		repositoryQuery := fs.String("repo", "", "repository ID or title")
		workspaceQuery := fs.String("workspace", "", "existing workspace ID or title")
		workspaceTitle := fs.String("workspace-title", "", "title for a new workspace")
		remote := fs.String("remote", "", "source remote")
		ref := fs.String("ref", "", "source reference")
		fetch := fs.Bool("fetch", true, "fetch the source remote first")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("usage: galpon worktree create --repo <id> (--workspace <id> | --workspace-title <title>)")
		}
		dashboard, err := client.Dashboard(context.Background())
		if err != nil {
			return err
		}
		repository := findRepository(dashboard.Repositories, *repositoryQuery)
		if repository.ID == "" {
			return fmt.Errorf("repository not found: %s", *repositoryQuery)
		}
		request := app.CreateWorktreeRequest{RepositoryID: repository.ID, Remote: *remote, Ref: *ref, FetchFirst: *fetch}
		switch {
		case strings.TrimSpace(*workspaceQuery) != "" && strings.TrimSpace(*workspaceTitle) != "":
			return fmt.Errorf("select an existing workspace or provide --workspace-title, not both")
		case strings.TrimSpace(*workspaceQuery) != "":
			workspace := findWorkspace(dashboard.Workspaces, *workspaceQuery)
			if workspace.ID == "" {
				return fmt.Errorf("workspace not found: %s", *workspaceQuery)
			}
			request.WorkspaceID = workspace.ID
		case strings.TrimSpace(*workspaceTitle) != "":
			request.WorkspaceTitle = *workspaceTitle
		default:
			return fmt.Errorf("--workspace or --workspace-title is required")
		}
		value, err := client.CreateWorktree(context.Background(), request)
		if err == nil {
			printJSON(value)
		}
		return err
	case "open":
		if len(args) != 2 {
			return fmt.Errorf("usage: galpon worktree open <id>")
		}
		dashboard, err := client.Dashboard(context.Background())
		if err != nil {
			return err
		}
		worktree, ok := dashboard.Worktree(args[1])
		if !ok {
			return fmt.Errorf("worktree not found: %s", args[1])
		}
		workspace, ok := dashboard.Workspace(worktree.WorkspaceID)
		if !ok {
			return fmt.Errorf("workspace not found")
		}
		repository, ok := dashboard.Repository(worktree.RepositoryID)
		if !ok {
			return fmt.Errorf("repository not found")
		}
		renderer := herdr.Adapter{Bin: cfg.HerdrBin}
		rendererID, err := renderer.OpenTerminal(context.Background(), workspace, worktree, workspace.Title+" · "+repository.Title, nil)
		if err != nil {
			return err
		}
		if err := client.SetRenderer(context.Background(), workspace.ID, renderer.Name(), renderer.Context(), rendererID); err != nil {
			return err
		}
		printJSON(worktree)
		return nil
	default:
		return fmt.Errorf("unknown worktree command %q", args[0])
	}
}

func agentCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("agent needs create, open, send, or show")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		if len(args) < 2 {
			return fmt.Errorf("usage: galpon agent create <title> --workspace <id> --repo <id> [placement options]")
		}
		fs := flag.NewFlagSet("agent create", flag.ContinueOnError)
		ws := fs.String("workspace", "", "workspace ID")
		role := fs.String("role", "", "optional agent role")
		contextAgent := fs.String("context-agent", "", "agent context source")
		repository := fs.String("repo", "", "primary repository")
		remote := fs.String("remote", "", "primary source remote")
		ref := fs.String("ref", "", "primary source reference")
		placementAgent := fs.String("placement-agent", "", "agent placement source")
		share := fs.Bool("share", false, "share the source agent worktrees exactly")
		cwd := fs.String("cwd", "", "absolute directory for no managed worktree")
		var secondary repeatedFlag
		fs.Var(&secondary, "secondary", "secondary repository as repo[,remote[,ref]]; repeatable")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		dashboard, err := client.Dashboard(context.Background())
		if err != nil {
			return err
		}
		workspace := findWorkspace(dashboard.Workspaces, *ws)
		if workspace.ID == "" {
			return fmt.Errorf("workspace not found: %s", *ws)
		}
		contextID := ""
		if *contextAgent != "" {
			source := findAgent(dashboard.Agents, *contextAgent)
			if source.ID == "" {
				return fmt.Errorf("context agent not found: %s", *contextAgent)
			}
			contextID = source.ID
		}
		placement := app.AgentPlacementRequest{}
		switch {
		case *cwd != "":
			placement = app.AgentPlacementRequest{Type: "none", CWD: *cwd}
		case *placementAgent != "":
			source := findAgent(dashboard.Agents, *placementAgent)
			if source.ID == "" {
				return fmt.Errorf("placement agent not found: %s", *placementAgent)
			}
			placement = app.AgentPlacementRequest{Type: "agent", SourceAgentID: source.ID, Share: *share}
		default:
			repo := findRepository(dashboard.Repositories, *repository)
			if repo.ID == "" {
				return fmt.Errorf("--repo is required and must match a repository")
			}
			placement.Type = "worktrees"
			placement.Worktrees = append(placement.Worktrees, app.AgentPlacementWorktreeRequest{RepositoryID: repo.ID, Remote: *remote, Ref: *ref, FetchFirst: true})
			for _, raw := range secondary {
				parts := strings.SplitN(raw, ",", 3)
				repo := findRepository(dashboard.Repositories, parts[0])
				if repo.ID == "" {
					return fmt.Errorf("secondary repository not found: %s", parts[0])
				}
				entry := app.AgentPlacementWorktreeRequest{RepositoryID: repo.ID, FetchFirst: true}
				if len(parts) > 1 {
					entry.Remote = parts[1]
				}
				if len(parts) > 2 {
					entry.Ref = parts[2]
				}
				placement.Worktrees = append(placement.Worktrees, entry)
			}
		}
		value, err := client.CreateAgent(context.Background(), app.CreateAgentRequest{Title: args[1], Role: *role, WorkspaceID: workspace.ID, ContextAgentID: contextID, Placement: placement})
		if err == nil {
			value, err = client.OpenAgent(context.Background(), value.ID, true)
		}
		if err == nil {
			printJSON(value)
		}
		return err
	case "send":
		if len(args) < 3 {
			return fmt.Errorf("usage: galpon agent send <id> <message>")
		}
		message, err := client.Send(context.Background(), args[1], strings.Join(args[2:], " "))
		if err == nil {
			printJSON(message)
		}
		return err
	case "open":
		if len(args) != 2 {
			return fmt.Errorf("usage: galpon agent open <id>")
		}
		value, err := client.OpenAgent(context.Background(), args[1], true)
		if err == nil {
			printJSON(value)
		}
		return err
	case "show":
		if len(args) != 2 {
			return fmt.Errorf("usage: galpon agent show <id>")
		}
		value, err := client.Agent(context.Background(), args[1])
		if err == nil {
			printJSON(value)
		}
		return err
	default:
		return fmt.Errorf("unknown agent command %q", args[0])
	}
}

func cleanupCommand(cfg config.Config, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: galpon cleanup")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	result, err := client.Cleanup(context.Background())
	if err != nil {
		return err
	}
	printJSON(result)
	return nil
}

func checkpointCommand(cfg config.Config, args []string) error {
	if len(args) == 0 || (args[0] != "create" && args[0] != "restore") {
		return fmt.Errorf("checkpoint needs create or restore")
	}
	action := args[0]
	fs := flag.NewFlagSet("checkpoint "+action, flag.ContinueOnError)
	passphraseFile := fs.String("passphrase-file", "", "read the checkpoint passphrase from a file")
	allowLocalRemotes := fs.Bool("allow-local-remotes", false, "allow checkpoint refs on local filesystem remotes")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: galpon checkpoint %s [--passphrase-file path] <file>", action)
	}
	passphrase, err := readCheckpointPassphrase(*passphraseFile, action == "create")
	if err != nil {
		return err
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	if action == "create" {
		result, err := client.CreateCheckpoint(context.Background(), fs.Arg(0), passphrase, *allowLocalRemotes)
		if err == nil {
			printJSON(result)
		}
		return err
	}
	result, err := client.RestoreCheckpoint(context.Background(), fs.Arg(0), passphrase)
	if err == nil {
		printJSON(result)
	}
	return err
}

func readCheckpointPassphrase(filePath string, confirm bool) (string, error) {
	if strings.TrimSpace(filePath) != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read checkpoint passphrase: %w", err)
		}
		value := strings.TrimRight(string(data), "\r\n")
		if value == "" {
			return "", fmt.Errorf("checkpoint passphrase is empty")
		}
		return value, nil
	}
	if value, ok := os.LookupEnv("GALPON_CHECKPOINT_PASSPHRASE"); ok {
		if value == "" {
			return "", fmt.Errorf("GALPON_CHECKPOINT_PASSPHRASE is empty")
		}
		return value, nil
	}
	input := int(os.Stdin.Fd())
	if !term.IsTerminal(input) {
		return "", fmt.Errorf("set GALPON_CHECKPOINT_PASSPHRASE or use --passphrase-file")
	}
	_, _ = fmt.Fprint(os.Stderr, "Checkpoint passphrase: ")
	secret, err := term.ReadPassword(input)
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(secret) == 0 {
		return "", fmt.Errorf("checkpoint passphrase is empty")
	}
	if confirm {
		_, _ = fmt.Fprint(os.Stderr, "Confirm checkpoint passphrase: ")
		repeated, err := term.ReadPassword(input)
		_, _ = fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(secret) != string(repeated) {
			return "", fmt.Errorf("checkpoint passphrases do not match")
		}
	}
	return string(secret), nil
}

func piCommand(cfg config.Config, args []string) error {
	if len(args) != 2 || args[0] != "run" {
		return fmt.Errorf("usage: galpon pi run <agent-id>")
	}
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	view, err := client.Agent(context.Background(), args[1])
	if err != nil {
		return err
	}
	dashboard, err := client.Dashboard(context.Background())
	if err != nil {
		return err
	}
	workspace, ok := dashboard.Workspace(view.Agent.WorkspaceID)
	if !ok {
		return fmt.Errorf("workspace not found")
	}
	worktree, ok := dashboard.PrimaryWorktree(view.Agent)
	if !ok {
		if view.Agent.Placement.Type != "none" || view.Agent.Placement.CWD == "" {
			return fmt.Errorf("agent primary worktree not found")
		}
		worktree = model.Worktree{Path: view.Agent.Placement.CWD}
	}
	assets, err := piagent.Materialize(cfg.StateDir)
	if err != nil {
		return err
	}
	contextSessionPath := ""
	if view.Agent.ContextAgentID != "" && view.Agent.SessionPath == "" {
		if source, ok := dashboard.Agent(view.Agent.ContextAgentID); ok {
			contextSessionPath = source.SessionPath
		}
	}
	commandLine := piagent.Command(cfg, assets, view.Agent, contextSessionPath)
	command := exec.Command(commandLine[0], commandLine[1:]...)
	command.Dir = worktree.Path
	runtimeID := uuid.NewString()
	command.Env = append(os.Environ(),
		"GALPON_SOCKET="+cfg.Socket,
		"GALPON_AGENT_ID="+view.Agent.ID,
		"GALPON_AGENT_TITLE="+view.Agent.Title,
		"GALPON_AGENT_ROLE="+view.Agent.Role,
		"GALPON_WORKSPACE_ID="+workspace.ID,
		"GALPON_WORKSPACE_TITLE="+workspace.Title,
		"GALPON_RUNTIME_ID="+runtimeID,
		"GALPON_PLACEMENT="+placementDescription(dashboard, view.Agent),
	)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	err = command.Run()
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = client.StopRuntime(stopCtx, view.Agent.ID, runtimeID, errorText(err))
	return err
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func herdrCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("herdr needs install or config")
	}
	binary, err := os.Executable()
	if err != nil {
		binary = "galpon"
	}
	switch args[0] {
	case "config":
		fmt.Print(herdr.PopupConfig(binary))
		return nil
	case "install":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path := os.Getenv("HERDR_CONFIG_PATH")
		if path == "" {
			path = filepath.Join(home, ".config", "herdr", "config.toml")
		}
		if err := herdr.InstallPopup(path, binary); err != nil {
			return err
		}
		fmt.Println("Installed Ctrl-K Galpon popup in", path)
		fmt.Println("Run: herdr server reload-config")
		return nil
	default:
		return fmt.Errorf("unknown herdr command %q", args[0])
	}
}

func snapshotCommand(cfg config.Config) error {
	client, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	dashboard, err := client.Dashboard(context.Background())
	if err != nil {
		return err
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	fmt.Print(tui.Snapshot(dashboard, 100, 32))
	return nil
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}
