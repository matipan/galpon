package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/neovimreview"
	"github.com/matipan/galpon/internal/tui"
)

// Review setup and inspection do not connect to the daemon or install Pi assets.
func reviewCommand(cfg config.Config, args []string) error {
	if len(args) != 1 || (args[0] != "setup" && args[0] != "config") {
		return fmt.Errorf("review needs setup or config")
	}
	if args[0] == "setup" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		info, err := neovimreview.Setup(ctx, cfg.StateDir)
		if err != nil {
			return err
		}
		fmt.Printf("Neovim Review dependencies are ready at %s.\nUse /review in an interactive Galpon agent.\n", info.Runtime)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	info, err := neovimreview.Inspect(ctx, cfg.StateDir)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		neovimreview.Info
		Palette tui.Palette `json:"palette"`
	}{Info: info, Palette: tui.ReviewPalette()})
}
