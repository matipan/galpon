package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/config"
)

func TestReviewConfigDoesNotCreateStateOrStartServices(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native Review requires Linux")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "nvim"), []byte("#!/bin/sh\nprintf 'NVIM v0.11.5\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	state := t.TempDir()
	cfg := config.Config{StateDir: state, Socket: filepath.Join(state, "must-not-start.sock")}
	if err := reviewCommand(cfg, []string{"config"}); err == nil || !strings.Contains(err.Error(), "galpon review setup") {
		t.Fatalf("missing runtime error = %v", err)
	}
	if err := reviewCommand(cfg, []string{"setup", "extra"}); err == nil {
		t.Fatal("extra setup arguments were accepted")
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("configuration inspection changed Galpon state: %v", entries)
	}
}
