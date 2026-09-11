package main

import (
	"io"
	"log"
	"path/filepath"
	"testing"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/config"
)

func TestAutomaticUpgradeWorkerUsesInstalledGeneration(t *testing.T) {
	root := t.TempDir()
	logger := log.New(io.Discard, "", 0)
	application, prepared, err := app.OpenDaemon(t.Context(), config.Config{StateDir: root, Socket: filepath.Join(root, "galpon.sock"), PiBin: "pi"}, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	if !prepared {
		t.Fatal("fresh startup did not prepare the upgrade")
	}
	for range 2 {
		if err := runAutomaticCommunicationUpgrade(t.Context(), application, logger); err != nil {
			t.Fatal(err)
		}
		state, err := application.CommunicationProtocolState(t.Context())
		if err != nil || state.Generation != 3 || !state.Complete || state.Maintenance {
			t.Fatalf("automatic worker state = %#v, %v", state, err)
		}
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runAutomaticCommunicationUpgrade(t.Context(), application, logger); err == nil {
		t.Fatal("failed upgrade did not return an error to stop startup")
	}
}
