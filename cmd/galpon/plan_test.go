package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/app"
)

func TestPlanHandoffFile(t *testing.T) {
	request := app.PlanHandoff{SourceAgentID: uuid.NewString(), RevisionID: uuid.NewString(), Plan: "# Exact plan\n\nKeep whitespace.  \n"}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "request.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := readPlanHandoff(path)
	if err != nil || result != request {
		t.Fatalf("handoff changed: %#v; %v", result, err)
	}
	for name, data := range map[string]string{
		"unknown-field": strings.TrimSuffix(string(encoded), "}") + `,"extra":true}`,
		"trailing":      string(encoded) + `{}`,
		"oversized":     strings.Repeat(" ", app.MaxPlanBytes*6+4097),
		"bad-identity":  `{"sourceAgentId":"bad","revisionId":"bad","plan":"valid"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readPlanHandoff(path); err == nil {
				t.Fatal("accepted an invalid handoff")
			}
		})
	}
	link := filepath.Join(root, "symlink")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, link, fifo} {
		if _, err := readPlanHandoff(path); err == nil {
			t.Fatalf("accepted non-regular handoff %s", path)
		}
	}
}
