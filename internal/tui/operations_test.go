package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/matipan/galpon/internal/model"
)

func TestOperationsViewUsesResponsiveBoundedLayout(t *testing.T) {
	now := time.Now().UnixMilli()
	projection := model.WorkspaceOperations{
		Version:   1,
		Workspace: model.OperationsWorkspace{ID: "workspace", Title: "Operations workspace"},
		Summary:   model.OperationsSummary{Agents: 2, ActiveWork: 1, ReportedBlockers: 1, StaleObservations: 1},
		Agents: []model.OperationsAgent{
			{ID: "worker", Title: "Worker", Status: "running", CurrentDelivery: &model.OperationsDelivery{WorkID: "root", Observation: model.WorkObservation{State: "started", Source: "observed", Lease: "fresh", LeaseObservedAt: now - 2_000}}},
			{ID: "reviewer", Title: "Reviewer", Status: "idle", ObservedDelivery: &model.OperationsDelivery{Observation: model.WorkObservation{State: "started", Source: "observed", Lease: "stale", LeaseObservedAt: now - 40_000}}},
		},
		Activity: &model.OperationsActivityLane{Version: 1, Facts: []model.OperationsActivityFact{{Category: "tool: read", Status: "completed", Source: "observed", ObservedAt: now - 3_000}}},
		Work: []model.WorkItem{{
			ID: "root", Title: "Worker", TargetTitle: "Worker", Priority: "reported_blocker", Observation: model.WorkObservation{State: "started", Source: "observed", Lease: "fresh", LeaseObservedAt: now - 4_000, FreshnessAt: now + 20_000, Attempt: 2},
			Checkpoint: &model.WorkCheckpoint{Phase: "blocked", Summary: "Waiting for a choice", Blocker: "Choose the safe option", Source: "reported"},
		}},
		Truncation: model.OperationsTruncation{Truncated: true},
	}
	for _, size := range []struct{ width, height int }{{120, 28}, {52, 16}, {24, 8}, {12, 5}} {
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
		if size.width >= 52 && size.height >= 28 {
			for _, want := range []string{"lease observed", "OBSERVED ACTIVITY", "Reported"} {
				if !strings.Contains(view, want) {
					t.Fatalf("%dx%d view omitted %q:\n%s", size.width, size.height, want, view)
				}
			}
		}
		if strings.Contains(strings.ToLower(view), "stuck work") {
			t.Fatalf("view inferred stuck state:\n%s", view)
		}
	}
}

func TestSafeOperationsTitleRemovesHostileControls(t *testing.T) {
	if title := safeOperationsTitle("Bad\n\u202eTitle"); title != "BadTitle" {
		t.Fatalf("safe title = %q", title)
	}
}

func TestOperationsViewBoundsEmergencyStatesAtTwelveByFive(t *testing.T) {
	for _, value := range []Model{
		{screen: screenOperations},
		{screen: screenOperations, operationsLoaded: true, operationsErr: errors.New("private transport detail")},
	} {
		view := value.viewOperations(12, 5)
		lines := strings.Split(view, "\n")
		if len(lines) > 5 {
			t.Fatalf("emergency view has %d lines: %q", len(lines), view)
		}
		for _, line := range lines {
			if lipgloss.Width(line) > 12 {
				t.Fatalf("emergency line width = %d: %q", lipgloss.Width(line), line)
			}
		}
	}
}

func TestOperationsMessageRejectsOlderSameWorkspaceGeneration(t *testing.T) {
	value := Model{screen: screenOperations, operationsWorkspace: "current", operationsGeneration: 2, operationsInFlight: true, operationsLoaded: true, operations: model.WorkspaceOperations{Work: []model.WorkItem{{ID: "selected", Title: "Selected", Observation: model.WorkObservation{State: "started"}}}}, operationsSelectedID: "selected"}
	updated, _ := value.Update(operationsMsg{workspaceID: "current", generation: 1, value: model.WorkspaceOperations{Work: []model.WorkItem{{ID: "old", Title: "Old"}}}})
	modelValue := updated.(Model)
	if modelValue.operations.Work[0].ID != "selected" || !modelValue.operationsInFlight {
		t.Fatalf("older same-workspace response changed state: %#v", modelValue.operations)
	}
	updated, _ = modelValue.Update(operationsMsg{workspaceID: "current", generation: 2, value: model.WorkspaceOperations{Work: []model.WorkItem{{ID: "other", Title: "Other"}, {ID: "selected", Title: "Selected"}}}})
	modelValue = updated.(Model)
	if modelValue.operationsCursor != 1 || modelValue.operationsSelectedID != "selected" || modelValue.operationsInFlight {
		t.Fatalf("selection identity was not preserved: cursor %d id %q", modelValue.operationsCursor, modelValue.operationsSelectedID)
	}
}

func TestOperationsRefreshAllowsOnlyOneRequestInFlight(t *testing.T) {
	value := Model{screen: screenOperations, operationsWorkspace: "workspace", operationsGeneration: 3, operationsInFlight: true}
	if command := value.updateOperations(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}); command != nil || value.operationsGeneration != 3 {
		t.Fatalf("in-flight refresh started another request: generation %d", value.operationsGeneration)
	}
	value.operationsInFlight = false
	if command := value.updateOperations(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}); command == nil || !value.operationsInFlight || value.operationsGeneration != 4 {
		t.Fatalf("explicit refresh did not start one request: generation %d inFlight %v", value.operationsGeneration, value.operationsInFlight)
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
