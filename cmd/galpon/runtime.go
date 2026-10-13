package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/nativeagent"
)

func runtimeCommand(cfg config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: galpon runtime run <agent-id>")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	switch args[0] {
	case "child":
		if len(args) < 3 || args[1] != "--" {
			return fmt.Errorf("invalid native child invocation")
		}
		return nativeagent.SuperviseChild(ctx, args[2:])
	case "writer":
		if len(args) < 3 || args[1] != "--" {
			return fmt.Errorf("invalid native writer invocation")
		}
		return nativeagent.RunWriter(ctx, args[2:])
	case "mcp":
		if len(args) < 2 || len(args) > 3 || len(args) == 3 && args[2] != "channel" {
			return fmt.Errorf("invalid native MCP invocation")
		}
		return nativeagent.RunMCP(ctx, args[1], len(args) == 3, os.Stdin, os.Stdout)
	case "hook":
		if len(args) != 2 {
			return fmt.Errorf("invalid native hook invocation")
		}
		return nativeagent.RunHook(ctx, args[1], os.Stdin, os.Stdout)
	case "run":
		flags := flag.NewFlagSet("runtime run", flag.ContinueOnError)
		background := flags.Bool("background", false, "run without a terminal")
		runtimeID := flags.String("runtime-id", "", "prepared runtime ID supplied by the daemon")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return fmt.Errorf("usage: galpon runtime run [--background] <agent-id>")
		}
		return nativeagent.Run(ctx, cfg, flags.Arg(0), *runtimeID, *background, os.Stdin, os.Stdout, os.Stderr)
	default:
		return fmt.Errorf("unknown runtime command %q", args[0])
	}
}
