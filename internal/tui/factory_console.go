package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/matipan/galpon/internal/factory"
)

const (
	factoryWideWidth   = 168
	factoryMediumWidth = 108
)

type factoryQueueClass int

const (
	factoryNeedsUser factoryQueueClass = iota
	factoryActive
	factoryQueued
	factoryShipped
	factoryInactive
)

type factoryQueueGroup struct {
	class  factoryQueueClass
	label  string
	orders []int
}

type factorySemanticEvent struct {
	message   string
	kind      string
	createdAt int64
	count     int
}

func sortFactoryOrders(orders []factory.WorkOrder) []factory.WorkOrder {
	result := append([]factory.WorkOrder(nil), orders...)
	sort.SliceStable(result, func(left, right int) bool {
		leftClass := classifyFactoryOrder(result[left])
		rightClass := classifyFactoryOrder(result[right])
		if leftClass != rightClass {
			return leftClass < rightClass
		}
		return result[left].UpdatedAt > result[right].UpdatedAt
	})
	return result
}

func classifyFactoryOrder(order factory.WorkOrder) factoryQueueClass {
	if factoryNeedsAttention(order) {
		return factoryNeedsUser
	}
	if order.Status == "cancelled" {
		return factoryInactive
	}
	if order.Stage == factory.StageComplete || order.Status == "complete" {
		return factoryShipped
	}
	if order.Status == "queued" {
		return factoryQueued
	}
	return factoryActive
}

func factoryNeedsAttention(order factory.WorkOrder) bool {
	if order.Status == "blocked" || order.Stage == factory.StageFailed {
		return true
	}
	if order.Status != "waiting" {
		return false
	}
	return order.Stage == factory.StagePlanApproval || order.Stage == factory.StageHumanTest || order.Stage == factory.StageWaitingForApproval
}

func factoryIsAutonomous(order factory.WorkOrder) bool {
	if order.Status != "active" {
		return false
	}
	switch order.Stage {
	case factory.StageIntake, factory.StagePlanning, factory.StageImplementation, factory.StageHumanTest, factory.StageReview, factory.StageReviewFixes, factory.StagePRCI:
		return true
	default:
		return false
	}
}

func latestFactoryAgentID(runs []factory.AgentRun) string {
	for index := len(runs) - 1; index >= 0; index-- {
		if runs[index].Status == "running" && runs[index].AgentID != "" {
			return runs[index].AgentID
		}
	}
	for index := len(runs) - 1; index >= 0; index-- {
		if runs[index].AgentID != "" {
			return runs[index].AgentID
		}
	}
	return ""
}

func (m *FactoryModel) selectedAgentID() string {
	if id := latestFactoryAgentID(m.detailRuns); id != "" {
		return id
	}
	current, ok := m.current()
	if !ok {
		return ""
	}
	for index := len(m.runningRuns) - 1; index >= 0; index-- {
		if m.runningRuns[index].WorkOrderID == current.ID {
			return m.runningRuns[index].AgentID
		}
	}
	return current.DeveloperID
}

func (m *FactoryModel) selectedRun() (factory.AgentRun, bool) {
	for index := len(m.detailRuns) - 1; index >= 0; index-- {
		if m.detailRuns[index].Status == "running" {
			return m.detailRuns[index], true
		}
	}
	if len(m.detailRuns) > 0 {
		return m.detailRuns[len(m.detailRuns)-1], true
	}
	current, ok := m.current()
	if !ok {
		return factory.AgentRun{}, false
	}
	for index := len(m.runningRuns) - 1; index >= 0; index-- {
		if m.runningRuns[index].WorkOrderID == current.ID {
			return m.runningRuns[index], true
		}
	}
	return factory.AgentRun{}, false
}

func (m *FactoryModel) factoryConsoleView() string {
	inner := max(1, m.width-4)
	workspace, repository := m.factoryLocationNames()
	context := strings.Trim(strings.TrimSpace(workspace)+" / "+strings.TrimSpace(repository), " /")
	header := titleLine("Software Factory", context, inner)
	footer := m.factoryCommandBarForWidth(inner)
	notice := m.factoryNotice(inner)
	bodyHeight := max(1, m.height-lipgloss.Height(header)-lipgloss.Height(notice)-1-lipgloss.Height(footer))

	var body string
	switch {
	case m.width >= factoryWideWidth:
		available := max(3, inner-6)
		leftWidth := available / 3
		centerWidth := (available - leftWidth) / 2
		rightWidth := available - leftWidth - centerWidth
		center := m.consoleCenterView(centerWidth, bodyHeight)
		if m.surface == "history" {
			center = m.consoleHistoryView(centerWidth, bodyHeight)
		}
		right := m.consoleAgentView(rightWidth, bodyHeight)
		body = consoleColumns(m.consoleQueueView(leftWidth, bodyHeight), consoleColumns(center, right, centerWidth, rightWidth, bodyHeight), leftWidth, centerWidth+rightWidth+3, bodyHeight)
	case m.width >= factoryMediumWidth:
		leftWidth, rightWidth := consoleSplit(inner)
		var detail string
		switch m.surface {
		case "agent":
			detail = m.consoleAgentView(rightWidth, bodyHeight)
		case "history":
			detail = m.consoleHistoryView(rightWidth, bodyHeight)
		default:
			detail = m.consoleCenterView(rightWidth, bodyHeight)
		}
		body = consoleColumns(m.consoleQueueView(leftWidth, bodyHeight), detail, leftWidth, rightWidth, bodyHeight)
	default:
		switch m.surface {
		case "detail":
			body = m.consoleCenterView(inner, bodyHeight)
		case "agent":
			body = m.consoleAgentView(inner, bodyHeight)
		case "history":
			body = m.consoleHistoryView(inner, bodyHeight)
		default:
			body = m.consoleQueueView(inner, bodyHeight)
		}
	}

	content := strings.Join([]string{header, body, notice, consoleRule(inner), footer}, "\n")
	content = lipgloss.NewStyle().Padding(0, 2).Render(content)
	return factoryPersistentBlock(appBackground.Render(content), Tokyo.Background)
}

func (m *FactoryModel) factoryNotice(width int) string {
	text := "Attention first · selection stays stable during refresh"
	color := Tokyo.Muted
	if m.mode == "search" {
		text = "/ " + m.search.View()
		color = Tokyo.Status
	} else if m.err != nil {
		text = "! " + m.err.Error()
		color = Tokyo.Red
	} else if m.submitting {
		text = consoleActivityMark() + " Submitting"
		color = Tokyo.Yellow
	} else if current, ok := m.current(); ok && current.ID == m.launchingID && strings.HasPrefix(current.ID, "pending:") {
		text = consoleActivityMark() + " Starting feature and planner"
		color = Tokyo.Yellow
	} else if m.toast != "" {
		text = consoleMark(iconSuccess) + " " + m.toast
		color = Tokyo.Green
	} else if m.queueReorderPending {
		text = "Queue state changed · ctrl+r reorders the groups"
	}
	return lipgloss.NewStyle().Foreground(color).Background(Tokyo.Background).Render(ansi.Truncate(text, width, "…"))
}

func (m *FactoryModel) factoryLocationNames() (string, string) {
	workspace, repository := "", ""
	current, hasCurrent := m.current()
	for _, value := range m.dashboard.Workspaces {
		if !hasCurrent || value.ID == current.WorkspaceID {
			workspace = consoleText(value.Title)
			if hasCurrent {
				break
			}
		}
	}
	for _, value := range m.dashboard.Repositories {
		if !hasCurrent || value.ID == current.RepositoryID {
			repository = consoleText(value.Title)
			if hasCurrent {
				break
			}
		}
	}
	if workspace == "" && repository == "" {
		return "Factory queue", ""
	}
	return workspace, repository
}

func (m *FactoryModel) consoleQueueView(width, height int) string {
	lines := []string{consoleSection("WORK QUEUE")}
	selectedLine := 0
	for _, group := range m.factoryQueueGroups() {
		if len(group.orders) == 0 {
			continue
		}
		if len(lines) > 1 {
			lines = append(lines, "")
		}
		lines = append(lines, consoleSection(fmt.Sprintf("%s  %d", group.label, len(group.orders))))
		for _, orderIndex := range group.orders {
			if orderIndex == m.selected {
				selectedLine = len(lines)
			}
			lines = append(lines, m.factoryQueueRow(m.orders[orderIndex], width, orderIndex == m.selected))
		}
	}
	if len(lines) == 1 {
		message := "No matching feature titles."
		if len(m.orders) == 0 {
			message = "No features yet. Press n to start one."
		}
		lines = append(lines, "", consoleParagraph(message, width))
	}
	if len(lines) > height {
		total := len(lines)
		start := max(0, selectedLine-height/2)
		start = min(start, total-height)
		lines = lines[start : start+height]
		if start > 0 {
			lines[0] = mutedStyle.Render("↑ More work")
		}
		if start+height < total {
			lines[len(lines)-1] = mutedStyle.Render("↓ More work")
		}
	}
	return factoryPaneRows(strings.Join(lines, "\n"), width, height)
}

func (m *FactoryModel) factoryQueueGroups() []factoryQueueGroup {
	groups := []factoryQueueGroup{
		{class: factoryNeedsUser, label: "NEEDS YOU"},
		{class: factoryActive, label: "ACTIVE"},
		{class: factoryQueued, label: "QUEUED"},
		{class: factoryShipped, label: "RECENTLY SHIPPED"},
		{class: factoryInactive, label: "RECENTLY STOPPED"},
	}
	for _, orderIndex := range m.visibleOrderIndexes() {
		class := classifyFactoryOrder(m.orders[orderIndex])
		if held, ok := m.queueClasses[m.orders[orderIndex].ID]; ok {
			class = held
		}
		if (class == factoryShipped || class == factoryInactive) && len(groups[class].orders) == 3 {
			continue
		}
		groups[class].orders = append(groups[class].orders, orderIndex)
	}
	return groups
}

func (m *FactoryModel) factoryQueueRow(order factory.WorkOrder, width int, selected bool) string {
	style := rowStyle
	prefix := "  "
	if selected {
		style = selectedStyle.Bold(true)
		prefix = consoleMark(iconFocus) + " "
	}
	mark, color, _ := m.factoryOrderState(order, m.queueRun(order.ID))
	status := m.factoryQueueStatus(order, m.queueRun(order.ID))
	age := relativeFactoryTime(order.UpdatedAt)
	ageWidth := 0
	if width >= 42 {
		ageWidth = min(5, max(3, lipgloss.Width(age)))
	}
	statusWidth := min(18, max(10, width/4))
	fixedWidth := 4 + 1 + statusWidth
	if ageWidth > 0 {
		fixedWidth += 1 + ageWidth
	}
	titleWidth := max(5, width-fixedWidth)
	paint := func(text string, foreground lipgloss.Color) string {
		return style.Foreground(foreground).Render(text)
	}
	row := style.Render(prefix) + paint(mark, color) + style.Render(" "+consoleCell(consoleText(order.Title), titleWidth))
	row += style.Render(" ") + paint(consoleCell(status, statusWidth), Tokyo.Muted)
	if ageWidth > 0 {
		row += style.Render(" ") + paint(consoleCell(age, ageWidth), Tokyo.Comment)
	}
	return consoleFillRow(row, width, style)
}

func (m *FactoryModel) queueRun(workOrderID string) *factory.AgentRun {
	for index := len(m.runningRuns) - 1; index >= 0; index-- {
		if m.runningRuns[index].WorkOrderID == workOrderID {
			return &m.runningRuns[index]
		}
	}
	if current, ok := m.current(); ok && current.ID == workOrderID {
		for index := len(m.detailRuns) - 1; index >= 0; index-- {
			if m.detailRuns[index].WorkOrderID == workOrderID {
				return &m.detailRuns[index]
			}
		}
	}
	return nil
}

func (m *FactoryModel) factoryOrderState(order factory.WorkOrder, run *factory.AgentRun) (string, lipgloss.Color, string) {
	switch {
	case order.Status == "blocked" || order.Stage == factory.StageFailed:
		return consoleMark(iconFailure), Tokyo.Red, "blocked"
	case order.Status == "cancelled":
		return consoleMark(iconCanceled), Tokyo.Muted, "canceled"
	case factoryNeedsAttention(order):
		return consoleMark(iconAttention), Tokyo.Yellow, "waiting"
	case order.Stage == factory.StageComplete || order.Status == "complete":
		return consoleMark(iconSuccess), Tokyo.Green, "completed"
	case order.Status == "queued":
		return consoleMark(iconPending), Tokyo.Muted, "queued"
	case factoryIsAutonomous(order):
		if run != nil && run.Status == "running" || order.Stage == factory.StageIntake && order.Status == "active" {
			return consolePulse(m.frame), Tokyo.Yellow, "running"
		}
		return consoleGlyph("◐", "*"), Tokyo.Yellow, "starting"
	default:
		return consoleMark(iconIdle), Tokyo.Muted, "idle"
	}
}

func (m *FactoryModel) factoryQueueStatus(order factory.WorkOrder, run *factory.AgentRun) string {
	if order.Status == "blocked" {
		return "Fix required"
	}
	if order.Status == "cancelled" {
		return "Canceled"
	}
	switch order.Stage {
	case factory.StageIntake:
		if order.Status == "queued" {
			return "Queued"
		}
		return "Preparing brief"
	case factory.StagePlanning:
		return "Planning"
	case factory.StagePlanApproval:
		return "Approve plan"
	case factory.StageImplementation:
		return "Implementing"
	case factory.StageHumanTest:
		if order.Status == "active" {
			return "Preparing test environment"
		}
		return "Record test"
	case factory.StageReview:
		return "Reviewing"
	case factory.StageReviewFixes:
		return "Fixing findings"
	case factory.StagePRCI:
		return "Checking CI"
	case factory.StageWaitingForApproval:
		return "Approve merge"
	case factory.StageComplete:
		return "Shipped"
	}
	if run != nil {
		return "Agent " + factoryRuntime(run.CreatedAt)
	}
	return "Unknown"
}

func factoryRuntime(startedAt int64) string {
	return factoryDuration(startedAt, time.Now().UnixMilli())
}

func factoryRunRuntime(run factory.AgentRun, active bool) string {
	endedAt := run.UpdatedAt
	if active || endedAt <= 0 {
		endedAt = time.Now().UnixMilli()
	}
	return factoryDuration(run.CreatedAt, endedAt)
}

func factoryDuration(startedAt, endedAt int64) string {
	if startedAt <= 0 || endedAt < startedAt {
		return "00:00"
	}
	duration := time.Duration(endedAt-startedAt) * time.Millisecond
	if duration >= time.Hour {
		return fmt.Sprintf("%dh%02d", int(duration.Hours()), int(duration.Minutes())%60)
	}
	return fmt.Sprintf("%02d:%02d", int(duration.Minutes()), int(duration.Seconds())%60)
}

func factoryLifecycleName(stage factory.Stage) string {
	return []string{"BRIEF", "PLAN", "BUILD", "TEST", "REVIEW", "SHIP"}[factoryLifecycleIndex(stage)]
}

func factoryLifecycleIndex(stage factory.Stage) int {
	switch stage {
	case factory.StagePlanning, factory.StagePlanApproval:
		return 1
	case factory.StageImplementation:
		return 2
	case factory.StageHumanTest:
		return 3
	case factory.StageReview, factory.StageReviewFixes:
		return 4
	case factory.StagePRCI, factory.StageWaitingForApproval, factory.StageComplete:
		return 5
	default:
		return 0
	}
}

func (m *FactoryModel) consoleCenterView(width, height int) string {
	order, ok := m.current()
	if !ok {
		return factoryPaneRows(consoleSection("FEATURE")+"\n\n"+brandStyle.Render("Software Factory")+"\n\n"+mutedStyle.Render("Press n to start the first feature."), width, height)
	}
	mark, color, state := m.factoryOrderState(order, m.queueRun(order.ID))
	lines := []string{
		consoleSection("FEATURE"),
		brandStyle.Render(ansi.Truncate(consoleText(order.Title), width, "…")),
		mutedStyle.Render("#"+shortUI(order.ID)) + "  " + lipgloss.NewStyle().Foreground(color).Background(Tokyo.Background).Render(mark+" "+state),
		"",
	}
	lines = append(lines, m.factoryLifecycle(order, width)...)
	if m.err != nil {
		lines = append(lines, "", consoleError("! ACTION FAILED · "+m.err.Error(), width))
	}
	lines = append(lines, "")
	lines = append(lines, m.factoryStageContent(order, width, max(1, height-len(lines)))...)
	return factoryPaneRows(strings.Join(lines, "\n"), width, height)
}

func (m *FactoryModel) factoryLifecycle(order factory.WorkOrder, width int) []string {
	current := factoryLifecycleIndex(order.Stage)
	if width < 48 {
		_, color, state := m.factoryOrderState(order, m.queueRun(order.ID))
		return []string{consoleSection("STAGE") + "  " + lipgloss.NewStyle().Foreground(color).Background(Tokyo.Background).Bold(true).Render(factoryLifecycleName(order.Stage)+" · "+state)}
	}
	names := []string{"BRIEF", "PLAN", "BUILD", "TEST", "REVIEW", "SHIP"}
	parts := make([]string, 0, len(names))
	for index, name := range names {
		style := lipgloss.NewStyle().Foreground(Tokyo.Comment).Background(Tokyo.Background)
		if index == current {
			style = style.Foreground(Tokyo.Status).Bold(true).Underline(true)
		}
		parts = append(parts, style.Render(name))
	}
	separator := lipgloss.NewStyle().Foreground(Tokyo.Border).Background(Tokyo.Background).Render("  " + consoleGlyph("›", ">") + "  ")
	return []string{consoleSection("STAGE"), ansi.Truncate(strings.Join(parts, separator), width, "…")}
}

func (m *FactoryModel) factoryStageContent(order factory.WorkOrder, width, available int) []string {
	switch {
	case order.Status == "blocked" || order.Stage == factory.StageFailed:
		return m.factoryBlockedContent(order, width, available)
	case order.Stage == factory.StagePlanApproval:
		return m.factoryPlanContent(order, width, available)
	case order.Stage == factory.StageHumanTest && order.Status == "waiting":
		return m.factoryTestContent(order, width, available)
	case order.Stage == factory.StageWaitingForApproval:
		return m.factoryMergeContent(order, width)
	case order.Stage == factory.StageComplete:
		return m.factoryShippedContent(order, width)
	case order.Status == "cancelled":
		return []string{consoleSection("RESULT"), mutedStyle.Render(consoleMark(iconCanceled) + " Feature canceled"), "", consoleParagraph("Factory is no longer processing this feature.", width)}
	case order.Stage == factory.StageIntake && order.Status == "queued":
		return m.factoryQueuedContent(order, width, available)
	default:
		return m.factoryActiveContent(order, width, available)
	}
}

func (m *FactoryModel) factoryPlanContent(order factory.WorkOrder, width, available int) []string {
	lines := []string{
		consoleSection("DECISION"),
		lipgloss.NewStyle().Foreground(Tokyo.Yellow).Background(Tokyo.Background).Bold(true).Render(consoleMark(iconAttention) + " Approve the implementation plan"),
		"",
	}
	planLimit := max(1, available-9)
	lines = append(lines, wrapFactoryPreservingLines(order.Plan, width, planLimit)...)
	if m.preparedFor == order.ID && len(m.prepared.Annotations) > 0 {
		lines = append(lines, "", lipgloss.NewStyle().Foreground(Tokyo.Yellow).Background(Tokyo.Background).Render(fmt.Sprintf("%s %d unsent annotations", consoleMark(iconAttention), len(m.prepared.Annotations))))
	}
	lines = append(lines, "", consoleSection("ON APPROVE"), consoleParagraph("Start implementation in the feature worktree.", width))
	return lines
}

func (m *FactoryModel) factoryBlockedContent(order factory.WorkOrder, width, _ int) []string {
	reason := strings.TrimSpace(order.LastError)
	if reason == "" {
		reason = "The last Factory operation failed."
	}
	lines := []string{
		consoleSection("FAILURE"),
		lipgloss.NewStyle().Foreground(Tokyo.Red).Background(Tokyo.Background).Bold(true).Render(consoleMark(iconFailure) + " " + ansi.Truncate(consoleText(reason), max(1, width-2), "…")),
		"",
		consoleParagraph(factoryBlockExplanation(order), width),
		"",
		consoleSection("NEXT ACTION"),
	}
	if order.Stage == factory.StageIntake {
		lines = append(lines, consoleParagraph("Edit the feature brief, then retry when it is ready.", width))
	} else {
		lines = append(lines, consoleParagraph("Resolve the cause, inspect the agent or history, then retry.", width))
	}
	return lines
}

func factoryBlockExplanation(order factory.WorkOrder) string {
	message := strings.ToLower(order.LastError)
	switch {
	case strings.Contains(message, "request is empty") || strings.Contains(message, "brief"):
		return "The Factory cannot create a plan without a clear feature brief."
	case strings.Contains(message, "agent stopped"):
		return "The active agent was stopped. Retry when you want the same agent to continue."
	case strings.Contains(message, "check"):
		return "A required check did not complete successfully. Inspect the evidence before you retry."
	default:
		return "The Factory stopped to protect the work. Inspect the cause before you retry."
	}
}

func (m *FactoryModel) factoryActiveContent(order factory.WorkOrder, width, available int) []string {
	mark, color, state := m.factoryOrderState(order, m.queueRun(order.ID))
	lines := []string{
		consoleSection("CURRENT WORK"),
		lipgloss.NewStyle().Foreground(color).Background(Tokyo.Background).Bold(true).Render(mark + " " + factoryVisual(order).label + " · " + state),
	}
	if operation, ok := m.factoryReportedOperation(); ok {
		lines = append(lines, "", factoryQuietLabel("REPORTED"), consoleParagraph(operation, width))
	}
	if order.Stage == factory.StageReview || order.Stage == factory.StageReviewFixes {
		lines = append(lines, "")
		lines = append(lines, m.factoryReviewLines(width)...)
	}
	if len(lines) < available-4 {
		label, text := "BRIEF", order.Request
		if order.Plan != "" {
			label, text = "APPROVED PLAN", order.Plan
		}
		lines = append(lines, "", consoleSection(label))
		lines = append(lines, wrapFactoryPreservingLines(text, width, max(1, available-len(lines)-2))...)
	}
	if metadata := factoryMetadata(order, width); metadata != "" && len(lines) < available {
		lines = append(lines, "", metadata)
	}
	return lines
}

func (m *FactoryModel) factoryReportedOperation() (string, bool) {
	if m.operations == nil {
		return "", false
	}
	if delivery := m.operations.Agent.CurrentDelivery; delivery != nil && delivery.Checkpoint != nil && strings.TrimSpace(delivery.Checkpoint.Summary) != "" {
		return consoleText(delivery.Checkpoint.Summary), true
	}
	for _, item := range m.operations.Current {
		if item.Checkpoint != nil && strings.TrimSpace(item.Checkpoint.Summary) != "" {
			return consoleText(item.Checkpoint.Summary), true
		}
	}
	return "", false
}

func (m *FactoryModel) factoryReviewLines(width int) []string {
	lines := []string{consoleSection("INDEPENDENT REVIEWS")}
	found := false
	for _, kind := range []string{"review-general", "review-security", "review-simplicity"} {
		var selected *factory.AgentRun
		for index := range m.detailRuns {
			if m.detailRuns[index].Kind == kind {
				selected = &m.detailRuns[index]
			}
		}
		if selected == nil {
			continue
		}
		found = true
		mark, color := consoleStateMark(selected.Status, selected.Status == "running")
		label := strings.TrimSuffix(factoryRunTitle(kind), " reviewer") + " review"
		state := selected.Status
		row := lipgloss.NewStyle().Foreground(color).Background(Tokyo.Background).Render(mark) + " " + consoleCell(label, max(8, width-14)) + mutedStyle.Render(state)
		lines = append(lines, ansi.Truncate(row, width, "…"))
	}
	if !found {
		lines = append(lines, mutedStyle.Render("Waiting for reviewer results."))
	}
	return lines
}

func (m *FactoryModel) factoryTestContent(order factory.WorkOrder, width, available int) []string {
	lines := []string{
		consoleSection("DECISION"),
		lipgloss.NewStyle().Foreground(Tokyo.Yellow).Background(Tokyo.Background).Bold(true).Render(consoleMark(iconAttention) + " Use the prepared test target, then record the result"),
		"",
		consoleParagraph("The developer prepared the build, setup, and services. Perform only the feature checks below.", width),
	}
	if order.Commit != "" {
		lines = append(lines, "", factoryField("COMMIT", shortUI(order.Commit), width))
	}
	lines = append(lines, "", consoleSection("READY TEST HANDOFF · FROM DEVELOPER"))
	guide := m.factoryTestGuide(order)
	if guide == "" {
		lines = append(lines, mutedStyle.Render("Loading the prepared test handoff…"))
	} else {
		guideLimit := max(1, available-len(lines)-4)
		lines = append(lines, wrapFactoryPreservingLines(guide, width, guideLimit)...)
	}
	lines = append(lines, "", consoleSection("ON PASS"), consoleParagraph("Start independent general, security, and simplicity reviews for this commit.", width))
	return lines
}

func (m *FactoryModel) factoryTestGuide(order factory.WorkOrder) string {
	for index := len(m.detailRuns) - 1; index >= 0; index-- {
		run := m.detailRuns[index]
		if run.Kind == "test-guide" && run.Commit == order.Commit && run.Status == "completed" {
			return strings.TrimSpace(run.Result)
		}
	}
	return ""
}

func (m *FactoryModel) factoryMergeContent(order factory.WorkOrder, width int) []string {
	lines := []string{
		consoleSection("DECISION"),
		lipgloss.NewStyle().Foreground(Tokyo.Yellow).Background(Tokyo.Background).Bold(true).Render(consoleMark(iconAttention) + " Approve the pull request merge"),
		"",
		consoleParagraph("Reviews and required checks passed. Confirm that this pull request can merge.", width),
	}
	if order.PRURL != "" {
		lines = append(lines, "", factoryField("PULL REQUEST", order.PRURL, width))
	}
	if order.Commit != "" {
		lines = append(lines, factoryField("COMMIT", shortUI(order.Commit), width))
	}
	lines = append(lines, "", consoleSection("ON APPROVE"), consoleParagraph("Merge the pull request and mark Factory work complete.", width))
	return lines
}

func (m *FactoryModel) factoryShippedContent(order factory.WorkOrder, width int) []string {
	lines := []string{
		consoleSection("RESULT"),
		lipgloss.NewStyle().Foreground(Tokyo.Green).Background(Tokyo.Background).Bold(true).Render(consoleMark(iconSuccess) + " Feature shipped"),
		"",
		consoleParagraph("The pull request was merged and Factory work is complete.", width),
	}
	if order.PRURL != "" {
		lines = append(lines, "", factoryField("PULL REQUEST", order.PRURL, width))
	}
	return lines
}

func (m *FactoryModel) factoryQueuedContent(order factory.WorkOrder, width, available int) []string {
	lines := []string{
		consoleSection("QUEUE"),
		mutedStyle.Render(consoleMark(iconPending) + " Waiting for Factory capacity"),
		"",
		consoleParagraph("Planning starts when agent capacity is available.", width),
		"",
		consoleSection("BRIEF"),
	}
	lines = append(lines, wrapFactoryPreservingLines(order.Request, width, max(1, available-len(lines)))...)
	return lines
}

func factoryQuietLabel(label string) string {
	return lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.Background).Bold(true).Render(label)
}

func factoryField(label, value string, width int) string {
	labelWidth := min(14, max(8, width/3))
	return mutedStyle.Render(consoleCell(label, labelWidth)) + " " + ansi.Truncate(consoleParagraph(value, max(1, width-labelWidth-1)), max(1, width-labelWidth-1), "…")
}

func wrapFactoryPreservingLines(text string, width, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{mutedStyle.Render("No content was provided.")}
	}
	width = max(8, width)
	limit = max(1, limit)
	var result []string
	truncated := false
	for _, rawParagraph := range strings.Split(text, "\n") {
		paragraph := consoleText(rawParagraph)
		if len(result) >= limit {
			truncated = true
			break
		}
		if strings.TrimSpace(paragraph) == "" {
			result = append(result, "")
			continue
		}
		wrapped := strings.Split(ansi.Wrap(paragraph, width, ""), "\n")
		for _, line := range wrapped {
			if len(result) == limit {
				truncated = true
				break
			}
			result = append(result, rowStyle.Render(line))
		}
		if truncated {
			break
		}
	}
	if truncated && len(result) > 0 {
		plain := ansi.Strip(result[len(result)-1])
		result[len(result)-1] = rowStyle.Render(ansi.Truncate(plain, max(1, width-1), "") + "…")
	}
	return result
}

func (m *FactoryModel) consoleAgentView(width, height int) string {
	lines := []string{consoleSection("LIVE AGENT")}
	order, hasOrder := m.current()
	run, hasRun := m.selectedRun()
	if !hasOrder || !hasRun {
		lines = append(lines, "", mutedStyle.Render(consoleMark(iconPending)+" No agent assigned"))
		if hasOrder && order.Stage == factory.StageIntake && order.Status == "active" {
			lines[len(lines)-1] = lipgloss.NewStyle().Foreground(Tokyo.Yellow).Background(Tokyo.Background).Render(consolePulse(m.frame) + " Planner starting")
		}
		if hasOrder && factoryNeedsAttention(order) {
			lines = append(lines, consoleParagraph("Waiting for your decision.", width))
		}
		return factoryPaneRows(strings.Join(lines, "\n"), width, height)
	}

	agentTitle := factoryRunTitle(run.Kind)
	if m.operations != nil && m.operations.Agent.Title != "" {
		agentTitle = consoleText(m.operations.Agent.Title)
	} else if m.agent != nil && m.agent.Agent.Title != "" {
		agentTitle = consoleText(m.agent.Agent.Title)
	}
	active := run.Status == "running" && factoryIsAutonomous(order)
	mark, color := consoleStateMark(run.Status, active)
	state := run.Status
	if state == "" {
		state = "unknown"
	}
	lines = append(lines, "", brandStyle.Render(consoleMark(iconAgent)+" "+ansi.Truncate(agentTitle, max(1, width-2), "…")))
	observed := "unknown"
	if run.UpdatedAt > 0 {
		observed = relativeFactoryTime(run.UpdatedAt)
		if observed != "now" {
			observed += " ago"
		}
	}
	lines = append(lines, lipgloss.NewStyle().Foreground(color).Background(Tokyo.Background).Render(mark+" "+state)+mutedStyle.Render(" · "+factoryRunRuntime(run, active)+" · observed "+observed))

	if operation, ok := m.factoryReportedOperation(); ok {
		lines = append(lines, "", consoleSection("REPORTED"), consoleParagraph(operation, width))
	}
	if reviews := m.factoryReviewAgentLines(width); len(reviews) > 0 {
		lines = append(lines, "")
		lines = append(lines, reviews...)
	}
	if changedFiles := m.factoryFileChangeLines(order, width); len(changedFiles) > 0 {
		lines = append(lines, "")
		lines = append(lines, changedFiles...)
	}
	if contextLines := m.factoryAgentContext(order, width); len(contextLines) > 0 {
		lines = append(lines, "", consoleSection("CONTEXT"))
		lines = append(lines, contextLines...)
	}
	if events := collapseFactoryEvents(m.events); len(events) > 0 {
		lines = append(lines, "", consoleSection("RECENT EVENTS"))
		for index, event := range events {
			if index == 3 {
				break
			}
			timestamp := time.UnixMilli(event.createdAt).Format("15:04")
			message := event.message
			if event.count > 1 {
				message += fmt.Sprintf(" ×%d", event.count)
			}
			lines = append(lines, mutedStyle.Render(timestamp+"  ")+ansi.Truncate(rowStyle.Render(message), max(1, width-7), "…"))
		}
	}
	return factoryPaneRows(strings.Join(lines, "\n"), width, height)
}

func (m *FactoryModel) factoryReviewAgentLines(width int) []string {
	count := 0
	for _, run := range m.detailRuns {
		if strings.HasPrefix(run.Kind, "review-") {
			count++
		}
	}
	if count < 2 {
		return nil
	}
	lines := []string{consoleSection(fmt.Sprintf("REVIEW AGENTS  %d", count))}
	for _, run := range m.detailRuns {
		if !strings.HasPrefix(run.Kind, "review-") {
			continue
		}
		mark, color := consoleStateMark(run.Status, run.Status == "running")
		line := lipgloss.NewStyle().Foreground(color).Background(Tokyo.Background).Render(mark) + " " + rowStyle.Render(factoryRunTitle(run.Kind))
		lines = append(lines, ansi.Truncate(line, width, "…"))
	}
	return lines
}

func (m *FactoryModel) factoryFileChangeLines(order factory.WorkOrder, width int) []string {
	if m.agent == nil || len(m.agent.Worktrees) == 0 {
		return nil
	}
	label := consoleSection("CHANGED FILES")
	scope := "against " + strings.TrimSpace(order.BaseRef) + "; includes uncommitted changes"
	if strings.TrimSpace(order.BaseRef) == "" {
		scope = "against the feature base; includes uncommitted changes"
	}
	lines := []string{label, mutedStyle.Render(ansi.Truncate(scope, width, "…"))}
	if m.filesForOrder != order.ID || m.filesForAgent != m.agent.Agent.ID {
		return append(lines, lipgloss.NewStyle().Foreground(Tokyo.Yellow).Background(Tokyo.Background).Render(consoleActivityMark()+" Reading worktree changes"))
	}
	if m.filesErr != nil && len(m.fileChanges) == 0 {
		return append(lines, mutedStyle.Render("Change summary unavailable"))
	}
	if len(m.fileChanges) == 0 {
		return append(lines, mutedStyle.Render("No file changes yet"))
	}
	totalAdded, totalRemoved := 0, 0
	for _, change := range m.fileChanges {
		totalAdded += change.Added
		totalRemoved += change.Removed
	}
	lines[0] = consolePair(label, factoryFileChangeStats(totalAdded, totalRemoved, false), width)
	limit := min(6, len(m.fileChanges))
	for index := 0; index < limit; index++ {
		change := m.fileChanges[index]
		stats := factoryFileChangeStats(change.Added, change.Removed, change.Binary)
		pathWidth := max(6, width-lipgloss.Width(stats)-3)
		path := rowStyle.Render(factoryCompactFilePath(consoleText(change.Path), pathWidth))
		lines = append(lines, consolePair(path, stats, width))
	}
	if len(m.fileChanges) > limit {
		lines = append(lines, mutedStyle.Render(fmt.Sprintf("%s %d more files", consoleMark(iconMore), len(m.fileChanges)-limit)))
	}
	return lines
}

func factoryCompactFilePath(path string, width int) string {
	if lipgloss.Width(path) <= width {
		return path
	}
	base := filepath.Base(filepath.FromSlash(path))
	if lipgloss.Width(base)+2 <= width {
		return "…/" + base
	}
	return ansi.Truncate(base, width, "…")
}

func factoryFileChangeStats(added, removed int, binary bool) string {
	if binary {
		return mutedStyle.Render("binary")
	}
	return lipgloss.NewStyle().Foreground(Tokyo.Green).Background(Tokyo.Background).Render(fmt.Sprintf("+%d", added)) + " " + lipgloss.NewStyle().Foreground(Tokyo.Red).Background(Tokyo.Background).Render(fmt.Sprintf("-%d", removed))
}

func factoryRunTitle(kind string) string {
	switch kind {
	case "planner", "planner-feedback":
		return "Planning agent"
	case "developer":
		return "Developer agent"
	case "fixer":
		return "Fix agent"
	case "test-guide":
		return "Testing guide"
	case "review-general":
		return "General reviewer"
	case "review-simplicity":
		return "Simplicity reviewer"
	case "review-security":
		return "Security reviewer"
	default:
		title := strings.ReplaceAll(kind, "-", " ")
		if title == "" {
			return "Agent"
		}
		return strings.ToUpper(title[:1]) + title[1:]
	}
}

func (m *FactoryModel) factoryAgentContext(order factory.WorkOrder, width int) []string {
	var lines []string
	if m.agent != nil && len(m.agent.Worktrees) > 0 && strings.TrimSpace(m.agent.Worktrees[0].Branch) != "" {
		lines = append(lines, factoryField("BRANCH", m.agent.Worktrees[0].Branch, width))
	}
	if order.Commit != "" {
		lines = append(lines, factoryField("COMMIT", shortUI(order.Commit), width))
	}
	if order.CIStatus != "" {
		lines = append(lines, factoryField("CHECKS", order.CIStatus, width))
	}
	return lines
}

func collapseFactoryEvents(events []factory.Event) []factorySemanticEvent {
	result := make([]factorySemanticEvent, 0, len(events))
	indexByKey := map[string]int{}
	for _, event := range events {
		message, keep := semanticFactoryEvent(event)
		if !keep {
			continue
		}
		key := event.Kind + "\x00" + message
		if index, exists := indexByKey[key]; exists {
			result[index].count++
			continue
		}
		indexByKey[key] = len(result)
		result = append(result, factorySemanticEvent{message: message, kind: event.Kind, createdAt: event.CreatedAt, count: 1})
	}
	return result
}

func semanticFactoryEvent(event factory.Event) (string, bool) {
	if event.Kind == "action" && event.Message == "retry" {
		return "", false
	}
	message := consoleText(factoryEventMessage(event))
	switch event.Kind {
	case "created":
		return "Feature created", true
	case "stage", "agent", "approval", "test", "review", "pr", "merge", "complete", "blocked", "action":
		return message, true
	default:
		return message, message != ""
	}
}

func (m *FactoryModel) consoleHistoryView(width, height int) string {
	order, ok := m.current()
	if !ok {
		return factoryPaneRows(consoleSection("HISTORY")+"\n\n"+mutedStyle.Render("No feature selected."), width, height)
	}
	lines := []string{
		consoleSection("HISTORY"),
		brandStyle.Render(ansi.Truncate(consoleText(order.Title), width, "…")),
		mutedStyle.Render("Semantic events · repeated entries are collapsed"),
		"",
	}
	events := collapseFactoryEvents(m.events)
	if len(events) == 0 {
		lines = append(lines, mutedStyle.Render("No activity has been recorded."))
	}
	for _, event := range events {
		timestamp := time.UnixMilli(event.createdAt).Format("15:04")
		message := event.message
		if event.count > 1 {
			message += fmt.Sprintf(" ×%d", event.count)
		}
		mark, color := factoryEventMark(event.kind)
		line := mutedStyle.Render(timestamp+"  ") + lipgloss.NewStyle().Foreground(color).Background(Tokyo.Background).Render(mark) + " " + rowStyle.Render(message)
		lines = append(lines, ansi.Truncate(line, width, "…"))
		if event.kind == "blocked" && order.LastError != "" {
			lines = append(lines, mutedStyle.Render("        "+ansi.Truncate(consoleText(order.LastError), max(1, width-8), "…")))
		}
	}
	return factoryPaneRows(strings.Join(lines, "\n"), width, height)
}

func factoryEventMark(kind string) (string, lipgloss.Color) {
	switch kind {
	case "merge", "complete":
		return consoleMark(iconSuccess), Tokyo.Green
	case "blocked":
		return consoleMark(iconFailure), Tokyo.Red
	case "approval", "test":
		return consoleMark(iconAttention), Tokyo.Yellow
	case "stage", "agent", "review", "pr":
		return consoleGlyph("◐", "*"), Tokyo.Yellow
	default:
		return consoleMark(iconIdle), Tokyo.Muted
	}
}

func (m *FactoryModel) factoryCommandBar() string {
	return m.factoryCommandBarForWidth(max(1, m.width))
}

func (m *FactoryModel) factoryCommandBarForWidth(width int) string {
	commands := [][2]string{{"enter", "open"}, {"n", "new feature"}, {"/", "search"}, {"ctrl+r", "reorder"}, {"?", "actions"}, {"q", "quit"}}
	if m.mode == "search" {
		commands = [][2]string{{"enter", "apply"}, {"esc", "clear"}}
	} else if m.surface == "history" {
		commands = [][2]string{{"esc", "back"}, {"x", "delete"}, {"?", "actions"}}
	} else if m.surface == "agent" {
		commands = [][2]string{{"esc", "back"}, {"o", "open agent"}, {"l", "history"}, {"x", "delete"}, {"?", "actions"}}
	} else if order, ok := m.current(); ok && (m.width >= factoryMediumWidth || m.surface != "queue") {
		commands = nil
		if m.width < factoryMediumWidth {
			commands = append(commands, [2]string{"esc", "back"})
		}
		switch {
		case order.Status == "blocked":
			if order.Stage == factory.StageIntake {
				commands = append(commands, [2]string{"e", "edit brief"})
			}
			commands = append(commands, [2]string{"r", "retry"}, [2]string{"l", "history"})
		case order.Stage == factory.StagePlanApproval:
			commands = append(commands, [2]string{"e", "annotate"}, [2]string{"a", "approve plan"}, [2]string{"r", "request changes"})
		case order.Stage == factory.StageHumanTest && order.Status == "waiting":
			commands = append(commands, [2]string{"p", "test passed"}, [2]string{"f", "test failed"}, [2]string{"o", "open agent"})
		case order.Stage == factory.StageWaitingForApproval:
			commands = append(commands, [2]string{"m", "approve merge"}, [2]string{"o", "open agent"})
		default:
			if m.selectedAgentID() != "" {
				commands = append(commands, [2]string{"o", "open agent"})
			}
			commands = append(commands, [2]string{"l", "history"})
		}
		if m.width < factoryWideWidth && m.surface == "detail" {
			commands = append(commands, [2]string{"v", "live agent"})
		}
		commands = append(commands, [2]string{"x", "delete"}, [2]string{"?", "actions"})
	}
	parts := make([]string, 0, len(commands))
	for _, command := range commands {
		parts = append(parts, keyHint(command[0], command[1]))
	}
	return footerBar(width, parts...)
}

func (m *FactoryModel) factoryHelpView() string {
	inner := max(1, m.width-4)
	header := titleLine("Factory actions", "keyboard reference", inner)
	lines := []string{
		consoleSection("NAVIGATION"),
		"j / k, arrows   Move through the work queue",
		"enter           Open the selected feature",
		"v               Show observed agent work",
		"l               Show semantic history",
		"esc             Move one surface outward",
		"",
		consoleSection("FEATURE ACTIONS"),
		"n               Start a new feature",
		"e               Edit the artifact that needs input",
		"a               Approve a generated plan",
		"r               Request plan changes or retry blocked work",
		"p / f           Record the implementation test result",
		"m               Approve a pull request merge",
		"o               Open the selected agent",
		"x               Delete the selected feature",
		"",
		consoleSection("QUEUE"),
		"/               Filter feature titles",
		"ctrl+r          Apply current operational grouping",
		"q               Quit",
	}
	bodyHeight := max(1, m.height-lipgloss.Height(header)-2)
	body := consoleRows(strings.Join(lines, "\n"), inner, bodyHeight)
	footer := footerBar(inner, keyHint("? / esc", "back"))
	content := strings.Join([]string{header, body, consoleRule(inner), footer}, "\n")
	content = lipgloss.NewStyle().Padding(0, 2).Render(content)
	return factoryPersistentBlock(appBackground.Render(content), Tokyo.Background)
}

func factoryPaneRows(body string, width, height int) string {
	return factoryPersistentBlock(rowStyle.Render(consoleRows(body, width, height)), Tokyo.Background)
}

func factoryPersistentBlock(content string, background lipgloss.Color) string {
	lines := strings.Split(content, "\n")
	for index := range lines {
		lines[index] = factoryPersistentBackground(lines[index], background)
	}
	return strings.Join(lines, "\n")
}

// factoryPersistentBackground reapplies the canvas after nested ANSI resets.
// Without it, a terminal can expose its default background between fragments.
func factoryPersistentBackground(content string, background lipgloss.Color) string {
	color := lipgloss.ColorProfile().Color(string(background))
	sequence := color.Sequence(true)
	if sequence == "" {
		return content
	}
	setBackground := "\x1b[" + sequence + "m"
	content = strings.ReplaceAll(content, "\x1b[0m", "\x1b[0m"+setBackground)
	content = strings.ReplaceAll(content, "\x1b[m", "\x1b[m"+setBackground)
	content = strings.ReplaceAll(content, "\x1b[49m", "\x1b[49m"+setBackground)
	return setBackground + content + "\x1b[0m"
}

func factoryActionToast(action string) string {
	switch action {
	case "approve_plan":
		return "Plan approved · implementation will start"
	case "review_plan":
		return "Annotations sent · planner is revising the plan"
	case "test_pass":
		return "Test accepted · independent review will start"
	case "test_fail":
		return "Test feedback sent"
	case "approve_merge":
		return "Merge approved"
	case "retry":
		return "Factory resumed"
	case "update_brief":
		return "Brief updated · Factory resumed"
	case "stop_agent":
		return "Agent stopped"
	default:
		return ""
	}
}
