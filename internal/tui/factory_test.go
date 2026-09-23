package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/matipan/galpon/internal/factory"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/neovimreview"
	"github.com/muesli/termenv"
)

func factoryNewFeatureTestModel(width, height int) *FactoryModel {
	model := NewFactoryModel(nil, nil, model.Dashboard{
		Repositories: []model.Repository{
			{ID: "repo", Title: "dagger", SourcePath: "/src/dagger", DefaultBranch: "main"},
			{ID: "website", Title: "website", SourcePath: "/src/website", DefaultBranch: "trunk"},
		},
		Workspaces: []model.Workspace{{ID: "workspace", Title: "Cloud Engines", Status: "active"}},
	})
	model.width, model.height = width, height
	model.beginFactoryFeature()
	return model
}

func TestFactoryNewFeatureMakesTheMissionPrimary(t *testing.T) {
	m := factoryNewFeatureTestModel(150, 38)
	view := ansi.Strip(m.formView())
	for _, expected := range []string{
		"GALPON", "NEW FEATURE", "▰ BRIEF", "Describe the outcome, important constraints",
		"Describe the desired outcome...", "Useful details might include:", "▰ LAUNCH", "dagger",
		"Cloud Engines", "Generated from brief", "▰ ON START", "start or queue planning",
		"BRIEF → PLAN → BUILD", "tab configuration", "alt+enter start",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("New Feature surface does not contain %q:\n%s", expected, view)
		}
	}
	for _, rejected := range []string{"◀ dagger ▶", "enter launch", "tab options", "┌", "┐", "LAUNCH CONFIG", "AFTER LAUNCH"} {
		if strings.Contains(view, rejected) {
			t.Fatalf("New Feature surface retained %q:\n%s", rejected, view)
		}
	}
}

func TestFactoryNewFeatureEnterEditsAndExplicitKeyLaunches(t *testing.T) {
	m := factoryNewFeatureTestModel(120, 32)
	m.form.request.SetValue("Add organization invitations. Admins can invite people by email.")
	before := m.form.request.Value()
	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*FactoryModel)
	if command == nil || m.mode != "new" || m.form.request.Value() == before || !strings.Contains(m.form.request.Value(), "\n") {
		t.Fatalf("Enter did not remain a multiline edit: mode=%q value=%q", m.mode, m.form.request.Value())
	}
	updated, command = m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m = updated.(*FactoryModel)
	if command == nil || m.mode != "" || !strings.HasPrefix(m.launchingID, "pending:") {
		t.Fatalf("explicit launch did not return to Factory: mode=%q launching=%q", m.mode, m.launchingID)
	}
	current, ok := m.current()
	if !ok || current.Title != "Organization invitations" || current.Stage != factory.StageIntake || current.Status != "active" {
		t.Fatalf("optimistic launched feature = %#v", current)
	}
	view := ansi.Strip(m.boardView())
	if !strings.Contains(view, "▰ CURRENT WORK") || !strings.Contains(view, "Submitting") {
		t.Fatalf("launch transition is not immediate:\n%s", view)
	}
}

func TestFactoryNewFeatureStartRowLaunchesWithoutCtrlJAlias(t *testing.T) {
	m := factoryNewFeatureTestModel(130, 30)
	m.form.request.SetValue("Add organization invitations")
	if factoryLaunchKey(tea.KeyMsg{Type: tea.KeyEnter}) || factoryLaunchKey(tea.KeyMsg{Type: tea.KeyCtrlJ}) {
		t.Fatal("Enter or Ctrl+J aliases the explicit launch command")
	}
	if !factoryLaunchKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true}) {
		t.Fatal("Alt+Enter is not an explicit launch command")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = updated.(*FactoryModel)
	if m.mode != "new" || m.launchingID != "" {
		t.Fatalf("Ctrl+J launched the feature: mode=%q id=%q", m.mode, m.launchingID)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m.form.field = factoryConfigStart
	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(*FactoryModel)
	if command == nil || result.mode != "" || !strings.HasPrefix(result.launchingID, "pending:") {
		t.Fatalf("Start row did not launch: mode=%q id=%q", result.mode, result.launchingID)
	}
}

func TestFactoryNewFeatureEmptyLaunchValidatesInEditor(t *testing.T) {
	m := factoryNewFeatureTestModel(120, 30)
	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m = updated.(*FactoryModel)
	if command != nil || m.mode != "new" || m.form.focus != "brief" {
		t.Fatalf("empty launch left editor: mode=%q focus=%q", m.mode, m.form.focus)
	}
	view := ansi.Strip(m.formView())
	if !strings.Contains(view, "Tell the Factory what to build before launching.") {
		t.Fatalf("empty launch validation is not inline:\n%s", view)
	}
}

func TestFactoryNewFeatureConfigurationUsesRowsAndPickers(t *testing.T) {
	m := factoryNewFeatureTestModel(100, 28)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.form.focus != "config" {
		t.Fatalf("Tab focus = %q, want config", m.form.focus)
	}
	view := ansi.Strip(m.formView())
	if !strings.Contains(view, "› Repository") || !strings.Contains(view, "Factory default") {
		t.Fatalf("configuration focus is not clear:\n%s", view)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.form.focus != "picker" {
		t.Fatalf("repository Enter focus = %q, want picker", m.form.focus)
	}
	view = ansi.Strip(m.formView())
	if !strings.Contains(view, "SELECT REPOSITORY") || !strings.Contains(view, "›") || !strings.Contains(view, "dagger") || !strings.Contains(view, "website") {
		t.Fatalf("repository picker is incomplete:\n%s", view)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.form.repo != 1 || m.form.base.Value() != "trunk" || m.form.focus != "config" {
		t.Fatalf("repository choice did not update defaults: repo=%d base=%q focus=%q", m.form.repo, m.form.base.Value(), m.form.focus)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.form.focus != "brief" {
		t.Fatalf("Shift+Tab focus = %q, want brief", m.form.focus)
	}
}

func TestFactoryNewFeatureResponsiveSurfaceKeepsBriefLarge(t *testing.T) {
	for _, size := range [][2]int{{180, 38}, {130, 28}, {90, 28}} {
		m := factoryNewFeatureTestModel(size[0], size[1])
		view := m.formView()
		lines := strings.Split(view, "\n")
		if len(lines) != size[1] {
			t.Fatalf("%dx%d surface has %d lines", size[0], size[1], len(lines))
		}
		for lineNumber, line := range lines {
			if got := lipgloss.Width(line); got != size[0] {
				t.Fatalf("%dx%d line %d uses %d cells", size[0], size[1], lineNumber, got)
			}
		}
		plain := ansi.Strip(view)
		if !strings.Contains(plain, "▰ BRIEF") {
			t.Fatalf("%dx%d does not show the mission editor", size[0], size[1])
		}
		if size[0] < factoryMediumWidth && strings.Contains(plain, "▰ LAUNCH") {
			t.Fatalf("narrow editor shrank to preserve configuration:\n%s", plain)
		}
	}
	narrow := factoryNewFeatureTestModel(90, 28)
	_, _ = narrow.Update(tea.KeyMsg{Type: tea.KeyTab})
	plain := ansi.Strip(narrow.formView())
	if !strings.Contains(plain, "▰ LAUNCH") || strings.Contains(plain, "▰ BRIEF") {
		t.Fatalf("narrow Tab did not open separate configuration:\n%s", plain)
	}
}

func TestFactoryNewFeatureTrueColorKeepsEveryCellBackground(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(profile)
	m := factoryNewFeatureTestModel(130, 30)
	for lineNumber, line := range strings.Split(m.formView(), "\n") {
		if column, ok := firstFactoryCellWithoutBackground(line); ok {
			t.Fatalf("New Feature line %d column %d has terminal-default background: %q", lineNumber, column, line)
		}
	}
}

func TestFactoryConsolePrioritizesAttentionAndUsesThreePanes(t *testing.T) {
	now := time.Now().UnixMilli()
	orders := []factory.WorkOrder{
		{ID: "active", Title: "Billing retries", Stage: factory.StageImplementation, Status: "active", UpdatedAt: now - 2_000},
		{ID: "shipped", Title: "Audit export", Stage: factory.StageComplete, Status: "complete", UpdatedAt: now - 4_000},
		{ID: "queued", Title: "Search indexing", Request: "Index product data", Stage: factory.StageIntake, Status: "queued", UpdatedAt: now - 3_000},
		{ID: "plan", Title: "OAuth login", Request: "Add GitHub OAuth", Plan: "1. Add callback handler\n2. Persist the encrypted token\n3. Add integration tests", Stage: factory.StagePlanApproval, Status: "waiting", UpdatedAt: now},
	}
	m := &FactoryModel{width: 180, height: 38, orders: sortFactoryOrders(orders), surface: "detail"}
	view := m.boardView()
	for _, expected := range []string{"SOFTWARE FACTORY", "WORK QUEUE", "▰ NEEDS YOU  1", "▰ ACTIVE  1", "▰ QUEUED  1", "▰ RECENTLY SHIPPED  1", "▰ DECISION", "Approve the implementation plan", "LIVE AGENT"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("Factory console does not contain %q:\n%s", expected, view)
		}
	}
	if m.orders[0].ID != "plan" {
		t.Fatalf("attention item sorted to %d, want first", m.selected)
	}
}

func TestFactoryQueueUsesCompactRowsAndSharedSelection(t *testing.T) {
	order := factory.WorkOrder{ID: "build", Title: "Compact feature row", Stage: factory.StageImplementation, Status: "active", UpdatedAt: time.Now().UnixMilli()}
	m := &FactoryModel{orders: []factory.WorkOrder{order}, selected: 0}
	view := ansi.Strip(m.consoleQueueView(60, 10))
	if strings.Count(view, order.Title) != 1 {
		t.Fatalf("feature row is not compact:\n%s", view)
	}
	for _, oldPattern := range []string{"┃", "█", "░", "◆", "●"} {
		if strings.Contains(view, oldPattern) {
			t.Fatalf("queue retained old pattern %q:\n%s", oldPattern, view)
		}
	}
	if !strings.Contains(view, "›") || !strings.Contains(view, "Implementing") {
		t.Fatalf("selection or state column is missing:\n%s", view)
	}
}

func TestFactoryCanceledWorkIsNotRecentlyShipped(t *testing.T) {
	m := &FactoryModel{orders: []factory.WorkOrder{{ID: "canceled", Title: "Canceled", Stage: factory.StageImplementation, Status: "cancelled"}}}
	view := ansi.Strip(m.consoleQueueView(64, 10))
	if strings.Contains(view, "RECENTLY SHIPPED") || !strings.Contains(view, "RECENTLY STOPPED") {
		t.Fatalf("canceled work has incorrect history group:\n%s", view)
	}
}

func TestFactoryRefreshKeepsQueuePositionUntilExplicitRegroup(t *testing.T) {
	active := factory.WorkOrder{ID: "feature", Title: "Stable row", Stage: factory.StageImplementation, Status: "active"}
	m := &FactoryModel{orders: []factory.WorkOrder{active}}
	m.resetFactoryQueueClasses()
	complete := active
	complete.Stage, complete.Status = factory.StageComplete, "complete"
	m.applyFactoryOrders([]factory.WorkOrder{complete})
	if !m.queueReorderPending || m.queueClasses[complete.ID] != factoryActive {
		t.Fatalf("refresh moved the row before explicit regroup: pending=%v class=%v", m.queueReorderPending, m.queueClasses[complete.ID])
	}
	m.regroupFactoryQueue()
	if m.queueReorderPending || m.queueClasses[complete.ID] != factoryShipped {
		t.Fatalf("explicit regroup did not apply current state: pending=%v class=%v", m.queueReorderPending, m.queueClasses[complete.ID])
	}
}

func TestFactoryEnterOpensButDoesNotRetryBlockedWork(t *testing.T) {
	order := factory.WorkOrder{ID: "blocked", Title: "Blocked", Stage: factory.StageImplementation, Status: "blocked"}
	m := &FactoryModel{width: 90, height: 24, orders: []factory.WorkOrder{order}, surface: "detail"}
	_, command := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command != nil || m.submitting {
		t.Fatal("Enter retried blocked work from the selected row")
	}
	_, command = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if command == nil || !m.submitting {
		t.Fatal("explicit retry command did not submit blocked work")
	}
}

func TestFactoryConsoleActiveAgentMovesAndShowsCurrentExecution(t *testing.T) {
	now := time.Now().UnixMilli()
	order := factory.WorkOrder{ID: "build", Title: "Billing retries", Request: "Retry failed invoices", Plan: "Implement bounded retries.", Stage: factory.StageImplementation, Status: "active", UpdatedAt: now}
	checkpoint := &model.WorkCheckpoint{Summary: "Inspecting payment retry handlers"}
	m := &FactoryModel{
		width: 180, height: 34, orders: []factory.WorkOrder{order}, surface: "detail",
		detailRuns: []factory.AgentRun{{WorkOrderID: order.ID, AgentID: "agent", Kind: "developer", Status: "running", CreatedAt: now - 102_000, UpdatedAt: now}},
		operations: &model.AgentOperations{Agent: model.OperationsAgent{Title: "Billing retries · Developer", CurrentDelivery: &model.OperationsDelivery{Checkpoint: checkpoint}}},
	}
	first := m.boardView()
	m.frame++
	second := m.boardView()
	for _, expected := range []string{"▰ CURRENT WORK", "Inspecting payment retry handlers", "LIVE AGENT", "running · 01:42", "Billing retries · Developer"} {
		if !strings.Contains(first, expected) {
			t.Fatalf("active console does not contain %q:\n%s", expected, first)
		}
	}
	if first == second {
		t.Fatal("active execution did not animate")
	}
}

func TestFactoryMotionCanBeDisabled(t *testing.T) {
	t.Setenv("GALPON_UI_MOTION", "0")
	order := factory.WorkOrder{ID: "build", Title: "Stable motion", Stage: factory.StageImplementation, Status: "active"}
	m := &FactoryModel{width: 180, height: 28, orders: []factory.WorkOrder{order}, surface: "detail", detailRuns: []factory.AgentRun{{WorkOrderID: order.ID, Kind: "developer", Status: "running"}}}
	first := m.boardView()
	m.frame++
	if second := m.boardView(); first != second {
		t.Fatal("Factory animated while GALPON_UI_MOTION=0")
	}
	if command := m.scheduleFactoryAnimation(); command != nil {
		t.Fatal("Factory scheduled animation while motion was disabled")
	}
}

func TestFactoryConsoleHumanAttentionIsStaticAndActionable(t *testing.T) {
	now := time.Now().UnixMilli()
	plan := factory.WorkOrder{ID: "plan", Title: "OAuth login", Plan: "1. Add callback handler", Stage: factory.StagePlanApproval, Status: "waiting", UpdatedAt: now}
	m := &FactoryModel{width: 130, height: 30, orders: []factory.WorkOrder{plan}, surface: "detail"}
	first := m.boardView()
	m.frame++
	second := m.boardView()
	if first != second {
		t.Fatal("human attention state must not animate")
	}
	for _, expected := range []string{"▰ DECISION", "Approve the implementation plan", "e annotate", "a approve plan", "r request changes"} {
		if !strings.Contains(first, expected) {
			t.Fatalf("plan review does not contain %q:\n%s", expected, first)
		}
	}
}

func TestFactoryConsoleBlockedStateExplainsCauseAndResolution(t *testing.T) {
	order := factory.WorkOrder{ID: "blocked", Title: "Incomplete feature", Stage: factory.StageIntake, Status: "blocked", LastError: "feature request is empty"}
	m := &FactoryModel{width: 125, height: 28, orders: []factory.WorkOrder{order}, surface: "detail"}
	view := m.boardView()
	for _, expected := range []string{"▰ FAILURE", "feature request is empty", "cannot create a plan without", "e edit brief", "r retry"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("blocked console does not contain %q:\n%s", expected, view)
		}
	}
}

func TestFactoryConsoleQueuedAndShippedStatesAreDistinct(t *testing.T) {
	queued := &FactoryModel{width: 90, height: 24, orders: []factory.WorkOrder{{ID: "q", Title: "Queued", Request: "Wait for capacity", Stage: factory.StageIntake, Status: "queued"}}, surface: "detail"}
	if view := queued.boardView(); !strings.Contains(view, "Waiting for Factory capacity") || !strings.Contains(view, "capacity is available") {
		t.Fatalf("queued state is not deliberate:\n%s", view)
	}
	shipped := &FactoryModel{width: 90, height: 24, orders: []factory.WorkOrder{{ID: "s", Title: "Shipped", Stage: factory.StageComplete, Status: "complete", PRURL: "https://example.test/pull/12"}}, surface: "detail"}
	if view := shipped.boardView(); !strings.Contains(view, "✓ Feature shipped") || !strings.Contains(view, "pull/12") {
		t.Fatalf("shipped state is not deliberate:\n%s", view)
	}
}

func TestFactorySelectionChangesCommandAndAgentPanesTogether(t *testing.T) {
	orders := []factory.WorkOrder{
		{ID: "one", Title: "Billing retries", Stage: factory.StageImplementation, Status: "active"},
		{ID: "two", Title: "Search indexing", Stage: factory.StageReview, Status: "active"},
	}
	m := &FactoryModel{
		width: 180, height: 28, orders: orders, surface: "detail",
		runningRuns: []factory.AgentRun{
			{WorkOrderID: "one", AgentID: "developer", Kind: "developer", Status: "running"},
			{WorkOrderID: "two", AgentID: "reviewer", Kind: "review-security", Status: "running"},
		},
	}
	first := m.boardView()
	if !strings.Contains(first, "Billing retries") || !strings.Contains(first, "Developer agent") {
		t.Fatalf("first selection is incomplete:\n%s", first)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	second := m.boardView()
	if !strings.Contains(second, "Search indexing") || !strings.Contains(second, "Security reviewer") {
		t.Fatalf("second selection did not update both panes:\n%s", second)
	}
}

func TestFactoryConsoleResponsivePaneBehavior(t *testing.T) {
	order := factory.WorkOrder{ID: "build", Title: "Build feature", Stage: factory.StageImplementation, Status: "active"}
	medium := &FactoryModel{width: 130, height: 26, orders: []factory.WorkOrder{order}, surface: "detail", detailRuns: []factory.AgentRun{{WorkOrderID: order.ID, Kind: "developer", Status: "running"}}}
	if view := medium.boardView(); strings.Contains(view, "LIVE AGENT") || !strings.Contains(view, "v live agent") {
		t.Fatalf("medium layout did not collapse the agent pane:\n%s", view)
	}
	_, _ = medium.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if view := medium.boardView(); !strings.Contains(view, "LIVE AGENT") {
		t.Fatalf("medium layout did not expose the agent pane:\n%s", view)
	}

	narrow := &FactoryModel{width: 90, height: 24, orders: []factory.WorkOrder{order}, surface: "queue"}
	if view := narrow.boardView(); !strings.Contains(view, "WORK QUEUE") || strings.Contains(view, "CURRENT WORK") {
		t.Fatalf("narrow layout did not show only the queue:\n%s", view)
	}
	_, _ = narrow.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if view := narrow.boardView(); !strings.Contains(view, "CURRENT WORK") || strings.Contains(view, "WORK QUEUE") {
		t.Fatalf("narrow layout did not open selected work:\n%s", view)
	}
}

func TestFactoryTrueColorRenderingKeepsBackgroundAcrossStyledFragments(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(profile)

	order := factory.WorkOrder{ID: "plan", Title: "OAuth login", Plan: "1. Add callback handler", Stage: factory.StagePlanApproval, Status: "waiting"}
	m := &FactoryModel{width: 180, height: 24, orders: []factory.WorkOrder{order}, surface: "detail", events: []factory.Event{{Kind: "approval", Message: "Plan is ready for approval", CreatedAt: time.Now().UnixMilli()}}}
	for lineNumber, line := range strings.Split(m.boardView(), "\n") {
		if column, ok := firstFactoryCellWithoutBackground(line); ok {
			t.Fatalf("line %d column %d has terminal-default background: %q", lineNumber, column, line)
		}
	}
}

func firstFactoryCellWithoutBackground(line string) (int, bool) {
	background := false
	column := 0
	for index := 0; index < len(line); {
		if line[index] == '\x1b' && index+1 < len(line) && line[index+1] == '[' {
			end := strings.IndexByte(line[index+2:], 'm')
			if end < 0 {
				return column, true
			}
			parameters := line[index+2 : index+2+end]
			if parameters == "" || parameters == "0" {
				background = false
			} else {
				for _, parameter := range strings.Split(parameters, ";") {
					if parameter == "49" {
						background = false
					}
					if parameter == "48" {
						background = true
					}
				}
			}
			index += end + 3
			continue
		}
		if !background {
			return column, true
		}
		index++
		column++
	}
	return 0, false
}

func TestFactoryActionAndHelpSurfacesKeepCanvasBackground(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(profile)

	m := NewFactoryModel(nil, nil, model.Dashboard{})
	m.width, m.height = 120, 26
	m.orders = []factory.WorkOrder{{ID: "feature", Title: "Feature", Stage: factory.StageIntake, Status: "blocked"}}
	m.mode = "help"
	views := []string{m.factoryHelpView()}
	m.beginNote("update_brief")
	m.note.SetValue("A corrected brief")
	views = append(views, m.noteView())
	m.beginNote("delete_feature")
	views = append(views, m.noteView())
	for viewIndex, view := range views {
		for lineNumber, line := range strings.Split(view, "\n") {
			if column, ok := firstFactoryCellWithoutBackground(line); ok {
				t.Fatalf("surface %d line %d column %d has terminal-default background: %q", viewIndex, lineNumber, column, line)
			}
		}
	}
}

func TestFactoryConsoleFitsResponsiveTerminal(t *testing.T) {
	order := factory.WorkOrder{ID: "feature", Title: "A feature with a long descriptive title", Request: "A useful feature brief", Stage: factory.StageImplementation, Status: "active"}
	for _, width := range []int{180, 130, 90} {
		m := &FactoryModel{width: width, height: 28, orders: []factory.WorkOrder{order}, surface: "detail"}
		for lineNumber, line := range strings.Split(m.boardView(), "\n") {
			if got := lipgloss.Width(line); got != width {
				t.Fatalf("width %d line %d uses %d cells: %q", width, lineNumber, got, line)
			}
		}
	}
}

func TestFactoryPlanAnnotationsWaitForExplicitRevisionRequest(t *testing.T) {
	order := factory.WorkOrder{ID: "plan", Title: "OAuth", Plan: "Add callback", Stage: factory.StagePlanApproval, Status: "waiting"}
	m := &FactoryModel{width: 120, height: 28, orders: []factory.WorkOrder{order}, surface: "detail"}
	review := neovimreview.FactoryArtifactReview{Prepared: true, Annotations: []neovimreview.FactoryArtifactAnnotation{{Start: 0, End: 0, Quote: "Add callback", Comment: "Include state validation"}}}
	updated, command := m.Update(factoryPlanReviewDone{review: review})
	result := updated.(*FactoryModel)
	if command != nil {
		t.Fatal("prepared annotations were sent before the user requested changes")
	}
	if view := result.boardView(); !strings.Contains(view, "1 annotations added") {
		t.Fatalf("prepared annotations are not visible:\n%s", view)
	}
	_, command = result.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if command == nil {
		t.Fatal("request changes did not create an action")
	}
}

func TestFactoryMergeRequiresExplicitConfirmation(t *testing.T) {
	order := factory.WorkOrder{ID: "merge", Title: "Ready", Stage: factory.StageWaitingForApproval, Status: "waiting"}
	m := NewFactoryModel(nil, nil, model.Dashboard{})
	m.width, m.height, m.surface = 120, 24, "detail"
	m.orders = []factory.WorkOrder{order}
	_, command := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if command != nil || m.mode != "note" || m.pendingAction != "approve_merge" {
		t.Fatalf("merge did not open confirmation: mode=%q action=%q", m.mode, m.pendingAction)
	}
	if view := ansi.Strip(m.noteView()); !strings.Contains(view, "Approve this pull request merge?") || !strings.Contains(view, "enter approve merge") {
		t.Fatalf("merge confirmation is incomplete:\n%s", view)
	}
	_, command = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil || !m.submitting {
		t.Fatal("confirmed merge did not submit")
	}
}

func TestFactoryDeleteRequiresConfirmationAndRemovesSelection(t *testing.T) {
	m := NewFactoryModel(nil, nil, model.Dashboard{})
	m.width, m.height, m.surface = 100, 24, "detail"
	m.orders = []factory.WorkOrder{
		{ID: "delete", Title: "Delete me", Stage: factory.StagePlanApproval, Status: "waiting"},
		{ID: "keep", Title: "Keep me", Stage: factory.StageIntake, Status: "queued"},
	}
	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	result := updated.(*FactoryModel)
	if command != nil || result.mode != "note" || result.pendingAction != "delete_feature" {
		t.Fatalf("x did not open delete confirmation: mode=%q action=%q", result.mode, result.pendingAction)
	}
	if view := result.noteView(); !strings.Contains(view, "Delete this feature permanently?") || !strings.Contains(view, "enter delete feature") {
		t.Fatalf("delete confirmation is unclear:\n%s", view)
	}
	_, command = result.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil {
		t.Fatal("confirmed deletion did not create a command")
	}
	updated, _ = result.Update(factoryDeleted{id: "delete"})
	result = updated.(*FactoryModel)
	if len(result.orders) != 1 || result.orders[0].ID != "keep" || result.mode != "" {
		t.Fatalf("deleted selection remains: %#v", result.orders)
	}
	if view := result.factoryCommandBar(); !strings.Contains(view, "x delete") {
		t.Fatalf("delete command is not visible:\n%s", view)
	}
}

func TestFactoryActionErrorStaysVisibleAfterRefresh(t *testing.T) {
	m := &FactoryModel{
		width: 120, height: 30, err: errors.New("unknown action review_plan"), surface: "detail",
		orders: []factory.WorkOrder{{ID: "feature", Title: "Feature", Request: "Brief", Stage: factory.StagePlanApproval, Status: "waiting"}},
	}
	updated, _ := m.Update(factoryLoaded{snapshot: factory.Snapshot{WorkOrders: m.orders}})
	result := updated.(*FactoryModel)
	if result.err == nil {
		t.Fatal("successful refresh cleared the action error")
	}
	if view := result.boardView(); !strings.Contains(view, "ACTION FAILED") || !strings.Contains(view, "unknown action review_plan") {
		t.Fatalf("action error is not visible:\n%s", view)
	}
}

func TestFactorySearchMatchesTitlesOnly(t *testing.T) {
	m := &FactoryModel{orders: []factory.WorkOrder{
		{ID: "one", Title: "OAuth login", Request: "unrelated"},
		{ID: "two", Title: "Billing retries", Request: "OAuth appears only in this brief"},
	}}
	m.search.SetValue("OAuth")
	indexes := m.visibleOrderIndexes()
	if len(indexes) != 1 || indexes[0] != 0 {
		t.Fatalf("visible title matches = %#v", indexes)
	}
}

func TestFactoryActivityCollapsesNoise(t *testing.T) {
	events := []factory.Event{
		{Kind: "blocked", Message: "feature request is empty", CreatedAt: 3},
		{Kind: "action", Message: "retry", CreatedAt: 2},
		{Kind: "blocked", Message: "feature request is empty", CreatedAt: 1},
	}
	collapsed := collapseFactoryEvents(events)
	if len(collapsed) != 1 || collapsed[0].count != 2 || collapsed[0].message != "feature request is empty" {
		t.Fatalf("collapsed events = %#v", collapsed)
	}
}

func TestFactoryVisualStatesUseConciseLabels(t *testing.T) {
	tests := []struct {
		stage  factory.Stage
		status string
		label  string
		step   int
	}{
		{factory.StagePlanning, "active", "PLANNING", 1},
		{factory.StageImplementation, "active", "BUILDING", 2},
		{factory.StageHumanTest, "waiting", "READY TO TEST", 3},
		{factory.StageReview, "active", "REVIEWING", 4},
		{factory.StageWaitingForApproval, "waiting", "READY TO MERGE", 5},
		{factory.StageComplete, "complete", "SHIPPED", 6},
		{factory.StageReview, "blocked", "BLOCKED", 4},
	}
	for _, test := range tests {
		visual := factoryVisual(factory.WorkOrder{Stage: test.stage, Status: test.status})
		if visual.label != test.label || visual.step != test.step {
			t.Fatalf("visual for %s/%s = %#v", test.stage, test.status, visual)
		}
	}
}

func TestFactoryActionEventsUseHumanCopy(t *testing.T) {
	tests := map[string]string{
		"approve_plan":  "Plan approved",
		"review_plan":   "Plan annotations submitted",
		"test_pass":     "Implementation test passed",
		"test_fail":     "Implementation test failed",
		"approve_merge": "Merge approved",
		"cancel":        "Feature cancelled",
	}
	for action, expected := range tests {
		if got := factoryEventMessage(factory.Event{Kind: "action", Message: action}); got != expected {
			t.Fatalf("action %s = %q, want %q", action, got, expected)
		}
	}
}
