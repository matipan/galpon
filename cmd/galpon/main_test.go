package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

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
	printOperationsText(&text, projection)
	for _, want := range []string{"Operations · Work · more facts omitted", "1 active agents", "Agent runtime", "Work outline", "reported blocker", "started", "lease observed", "observed activity: tool: read · completed", "reported: Waiting for a choice", "stale observation"} {
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
