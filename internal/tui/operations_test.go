package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/matipan/galpon/internal/model"
)

func TestOperationsViewUsesResponsiveBoundedLayout(t *testing.T) {
	projection := model.WorkspaceOperations{
		Version:   1,
		Workspace: model.OperationsWorkspace{ID: "workspace", Title: "Operations workspace"},
		Summary:   model.OperationsSummary{Agents: 2, ActiveWork: 1, ReportedBlockers: 1, StaleObservations: 1},
		Agents: []model.OperationsAgent{
			{ID: "worker", Title: "Worker", Status: "running", CurrentDelivery: &model.OperationsDelivery{WorkID: "root", Observation: model.WorkObservation{State: "started", Source: "observed"}}},
			{ID: "reviewer", Title: "Reviewer", Status: "idle"},
		},
		Work: []model.WorkItem{{
			ID: "root", Title: "Worker", Priority: "reported_blocker", Observation: model.WorkObservation{State: "started", Source: "observed", Lease: "stale", Attempt: 2},
			Checkpoint: &model.WorkCheckpoint{Phase: "blocked", Summary: "Waiting for a choice", Blocker: "Choose the safe option", Source: "reported"},
		}},
		Truncation: model.OperationsTruncation{Truncated: true},
	}
	for _, size := range []struct{ width, height int }{{120, 28}, {52, 16}, {24, 8}} {
		view := (Model{operations: projection, operationsLoaded: true}).viewOperations(size.width, size.height)
		lines := strings.Split(view, "\n")
		if len(lines) > size.height {
			t.Fatalf("%dx%d view has %d lines:\n%s", size.width, size.height, len(lines), view)
		}
		for _, line := range lines {
			if lipgloss.Width(line) > size.width {
				t.Fatalf("%dx%d line width = %d: %q", size.width, size.height, lipgloss.Width(line), line)
			}
		}
		for _, want := range []string{"Operations", "WORK OUTLINE", "SELECTED DETAIL", "AGENT RUNTIME"} {
			if size.height >= 16 && !strings.Contains(view, want) {
				t.Fatalf("%dx%d view omitted %q:\n%s", size.width, size.height, want, view)
			}
		}
		if strings.Contains(strings.ToLower(view), "stuck work") {
			t.Fatalf("view inferred stuck state:\n%s", view)
		}
	}
}

func TestOperationsMessageIgnoresStaleWorkspaceResponse(t *testing.T) {
	value := Model{screen: screenOperations, operationsWorkspace: "current"}
	updated, _ := value.Update(operationsMsg{workspaceID: "old", value: model.WorkspaceOperations{Workspace: model.OperationsWorkspace{ID: "old"}}})
	modelValue := updated.(Model)
	if modelValue.operationsLoaded || modelValue.operations.Workspace.ID != "" {
		t.Fatalf("stale response changed operations state: %#v", modelValue.operations)
	}
}
