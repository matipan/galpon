package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

func TestCommunicationUpgradeCommandIsTerminalOnlyAndValidatesArguments(t *testing.T) {
	cfg := config.Config{}
	for _, args := range [][]string{{}, {"status"}, {"upgrade", "extra"}, {"upgrade", "--idle-timeout", "0"}} {
		if err := communicationCommand(cfg, args); err == nil || !strings.Contains(err.Error(), "usage: galpon communication upgrade") {
			t.Fatalf("communicationCommand(%v) = %v", args, err)
		}
	}
}

func TestOperationsTextAndJSONKeepObservedAndReportedFactsSeparate(t *testing.T) {
	now := time.Now().UnixMilli()
	projection := model.WorkspaceOperations{
		Version: 1, Workspace: model.OperationsWorkspace{ID: "workspace", Title: "Work"},
		Summary: model.OperationsSummary{Agents: 2, ActiveAgents: 1, ActiveWork: 1, ReportedBlockers: 1, StaleObservations: 1},
		Agents: []model.OperationsAgent{{ID: "worker", Title: "Worker", Status: "running", CurrentDelivery: &model.OperationsDelivery{
			Observation: model.WorkObservation{State: "started", Source: "observed"},
			Activity:    &model.WorkActivity{Category: "tool: read", Status: "completed", Source: "observed", ObservedAt: now - 3_000},
		}}},
		Work: []model.WorkItem{{ID: "root", Title: "Worker", Priority: "reported_blocker",
			Observation: model.WorkObservation{State: "started", Source: "observed", Lease: "stale", LeaseObservedAt: now - 4_000},
			Activity:    &model.WorkActivity{Category: "tool: read", Status: "completed", Source: "observed", ObservedAt: now - 3_000},
			Checkpoint:  &model.WorkCheckpoint{Phase: "blocked", Summary: "Waiting for a choice", Source: "reported"}}},
		Truncation: model.OperationsTruncation{Truncated: true},
	}
	var text bytes.Buffer
	if err := printOperationsText(&text, projection); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Operations · Work · more facts omitted", "1 active agent", "Agent runtime", "Work outline", "reported blocker", "started", "lease observed", "Observed activity", "reported: Waiting for a choice", "stale observation"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("text output omitted %q:\n%s", want, text.String())
		}
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"version":1`, `"source":"observed"`, `"source":"reported"`, `"truncated":true`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("JSON output omitted %q: %s", want, encoded)
		}
	}
}

type failingOperationsWriter struct{}

func (failingOperationsWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestOperationsTextReturnsWriteError(t *testing.T) {
	err := printOperationsText(failingOperationsWriter{}, model.WorkspaceOperations{})
	if err == nil || err.Error() != "write failed" {
		t.Fatalf("write error = %v", err)
	}
}
