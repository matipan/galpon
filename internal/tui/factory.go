package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/factory"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/neovimreview"
)

type factoryTick struct{}
type factoryFilesTick struct{}
type factoryFrame struct{}
type factoryLoaded struct {
	snapshot factory.Snapshot
	err      error
}
type factoryDetailLoaded struct {
	id         string
	snapshot   factory.Snapshot
	operations *model.AgentOperations
	agent      *model.AgentView
	err        error
}
type factoryActionDone struct {
	action string
	err    error
}
type factoryCreated struct {
	order factory.WorkOrder
	err   error
}
type factoryDeleted struct {
	id  string
	err error
}
type factoryPlanReviewDone struct {
	review neovimreview.FactoryArtifactReview
	err    error
}
type factoryFileChangesLoaded struct {
	workOrderID string
	agentID     string
	changes     []factoryFileChange
	err         error
}

type FactoryModel struct {
	client              *factory.Client
	galpon              *app.Client
	dashboard           model.Dashboard
	stateDir            string
	orders              []factory.WorkOrder
	runningRuns         []factory.AgentRun
	detailRuns          []factory.AgentRun
	events              []factory.Event
	operations          *model.AgentOperations
	agent               *model.AgentView
	fileChanges         []factoryFileChange
	filesForOrder       string
	filesForAgent       string
	filesLoading        bool
	filesErr            error
	selected            int
	width, height       int
	mode                string
	surface             string
	form                factoryForm
	note                textarea.Model
	search              textinput.Model
	pendingAction       string
	prepared            neovimreview.FactoryArtifactReview
	preparedFor         string
	frame               int
	toast               string
	toastUntil          int64
	launchingID         string
	queueClasses        map[string]factoryQueueClass
	queueReorderPending bool
	animationPending    bool
	submitting          bool
	err                 error
}
type factoryForm struct {
	title, issue, base     textinput.Model
	request                textarea.Model
	focus                  string
	field, repo, workspace int
	picker                 int
	validation             string
	inherited              bool
}

func newFactoryForm() factoryForm {
	f := factoryForm{focus: "brief"}
	f.title = textinput.New()
	f.title.Placeholder = "Optional title override"
	f.issue = textinput.New()
	f.issue.Placeholder = "Paste issue URL or number"
	f.base = textinput.New()
	f.base.Placeholder = "Branch or ref"
	f.request = textarea.New()
	f.request.Placeholder = "Describe the desired outcome...\n\nUseful details might include:\n· expected user behavior\n· important constraints\n· acceptance criteria"
	f.request.Prompt = ""
	f.request.ShowLineNumbers = false
	f.request.Focus()
	styleFactoryMissionEditor(&f)
	return f
}
func RunFactory(client *factory.Client, galpon *app.Client, dashboard model.Dashboard, stateDir string) error {
	applyPalette(configuredPalette())
	model := NewFactoryModel(client, galpon, dashboard)
	model.stateDir = stateDir
	_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}
func NewFactoryModel(client *factory.Client, galpon *app.Client, dashboard model.Dashboard) *FactoryModel {
	note := textarea.New()
	note.Placeholder = "Add feedback or test details"
	note.SetHeight(6)
	search := textinput.New()
	search.Placeholder = "Filter features"
	return &FactoryModel{client: client, galpon: galpon, dashboard: dashboard, form: newFactoryForm(), note: note, search: search, surface: "detail"}
}
func (m *FactoryModel) Init() tea.Cmd {
	return tea.Batch(m.load(), tickFactory(), tickFactoryFiles())
}
func tickFactory() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return factoryTick{} })
}
func pulseFactory() tea.Cmd {
	return tea.Tick(consoleAnimationInterval, func(time.Time) tea.Msg { return factoryFrame{} })
}
func (m *FactoryModel) scheduleFactoryAnimation() tea.Cmd {
	if m.animationPending || os.Getenv("GALPON_UI_MOTION") == "0" || !m.factoryHasVisibleActivity() {
		return nil
	}
	m.animationPending = true
	return pulseFactory()
}
func (m *FactoryModel) factoryHasVisibleActivity() bool {
	if m.mode == "new" || m.mode == "note" || m.mode == "help" {
		return m.submitting
	}
	queueVisible := m.width >= factoryMediumWidth || m.surface == "queue"
	for index, order := range m.orders {
		if !queueVisible && index != m.selected {
			continue
		}
		run := m.queueRun(order.ID)
		if factoryIsAutonomous(order) && (run != nil && run.Status == "running" || order.Stage == factory.StageIntake && order.Status == "active") {
			return true
		}
	}
	return false
}
func tickFactoryFiles() tea.Cmd {
	return tea.Tick(factoryFileRefreshInterval, func(time.Time) tea.Msg { return factoryFilesTick{} })
}
func (m *FactoryModel) load() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s, err := m.client.List(ctx)
		return factoryLoaded{s, err}
	}
}
func (m *FactoryModel) loadDetail() tea.Cmd {
	if len(m.orders) == 0 || m.client == nil {
		return nil
	}
	id := m.orders[m.selected].ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		snapshot, err := m.client.Get(ctx, id)
		if err != nil {
			return factoryDetailLoaded{id: id, err: err}
		}
		agentID := latestFactoryAgentID(snapshot.Runs)
		var operations *model.AgentOperations
		var agent *model.AgentView
		if m.galpon != nil && agentID != "" {
			if value, operationErr := m.galpon.AgentOperations(ctx, agentID); operationErr == nil {
				operations = &value
			}
			if value, agentErr := m.galpon.Agent(ctx, agentID); agentErr == nil {
				agent = &value
			}
		}
		return factoryDetailLoaded{id: id, snapshot: snapshot, operations: operations, agent: agent}
	}
}
func (m *FactoryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		wasUnset := m.width == 0
		m.width = v.Width
		m.height = v.Height
		if wasUnset && m.width < factoryMediumWidth {
			m.surface = "queue"
		}
		return m, m.scheduleFactoryAnimation()
	case factoryTick:
		return m, tea.Batch(m.load(), tickFactory())
	case factoryFilesTick:
		return m, tea.Batch(m.loadFactoryFileChanges(), tickFactoryFiles())
	case factoryFrame:
		m.animationPending = false
		m.frame++
		if m.toastUntil > 0 && time.Now().UnixMilli() >= m.toastUntil {
			m.toast = ""
			m.toastUntil = 0
		}
		return m, m.scheduleFactoryAnimation()
	case factoryLoaded:
		if v.err != nil {
			m.err = v.err
			return m, nil
		}
		selectedID := ""
		if current, ok := m.current(); ok {
			selectedID = current.ID
		}
		orders := v.snapshot.WorkOrders
		if strings.HasPrefix(m.launchingID, "pending:") {
			for _, order := range m.orders {
				if order.ID == m.launchingID {
					orders = append(orders, order)
					break
				}
			}
		}
		m.applyFactoryOrders(orders)
		m.runningRuns = v.snapshot.Runs
		m.selected = 0
		for index := range m.orders {
			if m.orders[index].ID == selectedID {
				m.selected = index
				break
			}
		}
		m.ensureVisibleSelection()
		if len(m.orders) > 0 {
			return m, tea.Batch(m.loadDetail(), m.scheduleFactoryAnimation())
		}
		return m, nil
	case factoryDetailLoaded:
		current, ok := m.current()
		if !ok || current.ID != v.id {
			return m, nil
		}
		if v.err != nil {
			m.err = v.err
			return m, nil
		}
		if len(v.snapshot.WorkOrders) == 1 {
			m.orders[m.selected] = v.snapshot.WorkOrders[0]
		}
		m.events = v.snapshot.Events
		m.detailRuns = v.snapshot.Runs
		m.operations = v.operations
		m.agent = v.agent
		if v.agent != nil && (m.filesForOrder != current.ID || m.filesForAgent != v.agent.Agent.ID) {
			return m, m.loadFactoryFileChanges()
		}
		return m, nil
	case factoryFileChangesLoaded:
		m.filesLoading = false
		current, ok := m.current()
		if !ok || current.ID != v.workOrderID || m.agent == nil || m.agent.Agent.ID != v.agentID {
			return m, nil
		}
		m.fileChanges = v.changes
		m.filesForOrder = v.workOrderID
		m.filesForAgent = v.agentID
		m.filesErr = v.err
		return m, nil
	case factoryActionDone:
		m.submitting = false
		m.err = v.err
		if v.err == nil {
			m.mode = ""
			m.toast = factoryActionToast(v.action)
			m.toastUntil = time.Now().Add(4 * time.Second).UnixMilli()
			if v.action == "review_plan" {
				m.prepared = neovimreview.FactoryArtifactReview{}
				m.preparedFor = ""
			}
		}
		return m, m.load()
	case factoryCreated:
		m.submitting = false
		m.err = v.err
		if v.err != nil {
			m.removeFactoryOrder(m.launchingID)
			m.launchingID = ""
			m.mode = "new"
			m.form.focus = "brief"
			m.form.request.Focus()
			m.form.validation = "The feature could not launch: " + v.err.Error()
			return m, nil
		}
		replaced := false
		for index := range m.orders {
			if m.orders[index].ID == m.launchingID {
				m.orders[index] = v.order
				replaced = true
				break
			}
		}
		if !replaced {
			m.orders = append(m.orders, v.order)
		}
		m.launchingID = v.order.ID
		m.form = newFactoryForm()
		m.toast = "Feature launched · Planner starting"
		m.toastUntil = time.Now().Add(4 * time.Second).UnixMilli()
		m.orders = sortFactoryOrders(m.orders)
		m.resetFactoryQueueClasses()
		for index := range m.orders {
			if m.orders[index].ID == v.order.ID {
				m.selected = index
				break
			}
		}
		return m, m.load()
	case factoryDeleted:
		m.submitting = false
		m.err = v.err
		if v.err != nil {
			return m, nil
		}
		m.mode = ""
		for index := range m.orders {
			if m.orders[index].ID == v.id {
				m.orders = append(m.orders[:index], m.orders[index+1:]...)
				break
			}
		}
		m.selected = min(m.selected, max(0, len(m.orders)-1))
		if m.queueClasses != nil {
			delete(m.queueClasses, v.id)
		}
		m.events = nil
		m.detailRuns = nil
		m.operations = nil
		m.agent = nil
		m.clearFactoryFileChanges()
		m.prepared = neovimreview.FactoryArtifactReview{}
		m.preparedFor = ""
		m.toast = "Feature deleted"
		m.toastUntil = time.Now().Add(4 * time.Second).UnixMilli()
		return m, m.load()
	case factoryPlanReviewDone:
		m.err = v.err
		if v.err != nil || !v.review.Prepared {
			return m, nil
		}
		current, ok := m.current()
		if !ok {
			return m, nil
		}
		m.prepared = v.review
		m.preparedFor = current.ID
		m.toast = fmt.Sprintf("%d annotations added · press r to request changes", len(v.review.Annotations))
		m.toastUntil = time.Now().Add(4 * time.Second).UnixMilli()
		return m, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if key.String() == "ctrl+c" || key.String() == "q" && m.mode == "" {
		return m, tea.Quit
	}
	if m.mode == "new" {
		return m.updateForm(key)
	}
	if m.mode == "note" {
		return m.updateNote(key)
	}
	if m.mode == "search" {
		return m.updateSearch(key)
	}
	if m.mode == "help" {
		if key.String() == "esc" || key.String() == "?" {
			m.mode = ""
		}
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		return m, m.moveFactorySelection(-1)
	case "down", "j":
		return m, m.moveFactorySelection(1)
	case "n":
		m.beginFactoryFeature()
	case "/":
		m.mode = "search"
		m.search.Focus()
	case "?":
		m.mode = "help"
	case "ctrl+r":
		m.regroupFactoryQueue()
	case "esc":
		if m.surface != "detail" {
			m.surface = "detail"
		} else if m.width < factoryMediumWidth {
			m.surface = "queue"
		}
	case "v":
		if m.width < factoryWideWidth {
			m.surface = "agent"
		}
	case "l":
		m.surface = "history"
	case "enter":
		if m.width < factoryMediumWidth && m.surface == "queue" {
			m.surface = "detail"
			return m, m.loadDetail()
		}
	case "o":
		if agentID := m.selectedAgentID(); agentID != "" && m.galpon != nil {
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				_, err := m.galpon.OpenAgent(ctx, agentID, true)
				return factoryActionDone{action: "open_agent", err: err}
			}
		}
	case "e":
		if w, ok := m.current(); ok && w.Stage == factory.StagePlanApproval && w.Plan != "" && m.stateDir != "" {
			cmd, directory, err := neovimreview.FactoryArtifactCommand(context.Background(), m.stateDir, filepath.Join(m.stateDir, "factory", "review-runs"), w.ID+":plan", w.Plan, factoryReviewPalette())
			if err != nil {
				m.err = err
				return m, nil
			}
			return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
				if err != nil {
					return factoryPlanReviewDone{err: err}
				}
				review, readErr := neovimreview.ReadFactoryArtifact(directory)
				return factoryPlanReviewDone{review: review, err: readErr}
			})
		} else if ok && w.Stage == factory.StageIntake && w.Status == "blocked" {
			m.beginNote("update_brief")
			m.note.SetValue(w.Request)
		}
	case "a":
		if w, ok := m.current(); ok && w.Stage == factory.StagePlanApproval {
			if m.preparedFor == w.ID && len(m.prepared.Annotations) > 0 {
				m.err = fmt.Errorf("request changes or reopen the plan before approval")
				return m, nil
			}
			return m, m.action("approve_plan", "")
		}
	case "p":
		if w, ok := m.current(); ok && w.Stage == factory.StageHumanTest {
			return m, m.action("test_pass", "")
		}
	case "f":
		if w, ok := m.current(); ok && w.Stage == factory.StageHumanTest {
			m.beginNote("test_fail")
		}
	case "m":
		if w, ok := m.current(); ok && w.Stage == factory.StageWaitingForApproval {
			m.beginNote("approve_merge")
		}
	case "r":
		if w, ok := m.current(); ok && w.Status == "blocked" {
			return m, m.action("retry", "")
		} else if ok && w.Stage == factory.StagePlanApproval {
			if m.preparedFor != w.ID || len(m.prepared.Annotations) == 0 {
				m.err = fmt.Errorf("add plan annotations with e before requesting changes")
				return m, nil
			}
			return m, m.action("review_plan", m.prepared.Prompt())
		}
	case "x":
		if _, ok := m.current(); ok {
			m.beginNote("delete_feature")
		}
	}
	return m, nil
}
func (m *FactoryModel) current() (factory.WorkOrder, bool) {
	if m.selected < 0 || m.selected >= len(m.orders) {
		return factory.WorkOrder{}, false
	}
	return m.orders[m.selected], true
}
func (m *FactoryModel) beginNote(action string) {
	m.mode = "note"
	m.pendingAction = action
	m.note.Reset()
	m.note.Focus()
}
func (m *FactoryModel) deleteFeature() tea.Cmd {
	work, _ := m.current()
	m.submitting = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := m.client.Delete(ctx, work.ID)
		return factoryDeleted{id: work.ID, err: err}
	}
}
func (m *FactoryModel) action(action, note string) tea.Cmd {
	w, _ := m.current()
	m.submitting = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := m.client.Action(ctx, w.ID, action, note)
		return factoryActionDone{action: action, err: err}
	}
}
func (m *FactoryModel) updateNote(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.mode = ""
		return m, nil
	case "alt+s":
		if m.pendingAction == "delete_feature" {
			return m, m.deleteFeature()
		}
		return m, m.action(m.pendingAction, m.note.Value())
	case "enter":
		if m.pendingAction == "delete_feature" {
			return m, m.deleteFeature()
		}
		if m.pendingAction == "stop_agent" || m.pendingAction == "approve_merge" {
			return m, m.action(m.pendingAction, "")
		}
	}
	var cmd tea.Cmd
	m.note, cmd = m.note.Update(key)
	return m, cmd
}
func (m *FactoryModel) updateForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	return m.updateFactoryFeature(key)
}
func (m *FactoryModel) updateSearch(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.search.SetValue("")
		m.search.Blur()
		m.mode = ""
		m.ensureVisibleSelection()
		return m, m.loadDetail()
	case "enter":
		m.search.Blur()
		m.mode = ""
		m.ensureVisibleSelection()
		return m, m.loadDetail()
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(key)
	m.ensureVisibleSelection()
	return m, cmd
}

func (m *FactoryModel) moveFactorySelection(delta int) tea.Cmd {
	visible := m.visibleOrderIndexes()
	if len(visible) == 0 {
		return nil
	}
	position := 0
	for index, orderIndex := range visible {
		if orderIndex == m.selected {
			position = index
			break
		}
	}
	position = max(0, min(len(visible)-1, position+delta))
	if m.selected == visible[position] {
		return nil
	}
	m.selected = visible[position]
	m.events = nil
	m.detailRuns = nil
	m.operations = nil
	m.agent = nil
	m.clearFactoryFileChanges()
	m.err = nil
	m.toast = ""
	m.toastUntil = 0
	return m.loadDetail()
}

func (m *FactoryModel) ensureVisibleSelection() {
	visible := m.visibleOrderIndexes()
	if len(visible) == 0 {
		m.selected = 0
		return
	}
	for _, index := range visible {
		if index == m.selected {
			return
		}
	}
	m.selected = visible[0]
}

func (m *FactoryModel) visibleOrderIndexes() []int {
	query := strings.ToLower(strings.TrimSpace(m.search.Value()))
	indexes := make([]int, 0, len(m.orders))
	for index, order := range m.orders {
		if query == "" || strings.Contains(strings.ToLower(order.Title), query) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func (m *FactoryModel) applyFactoryOrders(incoming []factory.WorkOrder) {
	if m.queueClasses == nil || len(m.orders) == 0 {
		m.orders = sortFactoryOrders(incoming)
		m.resetFactoryQueueClasses()
		return
	}
	byID := make(map[string]factory.WorkOrder, len(incoming))
	for _, order := range incoming {
		byID[order.ID] = order
	}
	stable := make([]factory.WorkOrder, 0, len(incoming))
	for _, old := range m.orders {
		if order, ok := byID[old.ID]; ok {
			stable = append(stable, order)
			delete(byID, old.ID)
		}
	}
	var added []factory.WorkOrder
	for _, order := range byID {
		added = append(added, order)
	}
	stable = append(stable, sortFactoryOrders(added)...)
	m.orders = stable
	present := make(map[string]bool, len(stable))
	for _, order := range stable {
		present[order.ID] = true
		class := classifyFactoryOrder(order)
		if held, ok := m.queueClasses[order.ID]; !ok {
			m.queueClasses[order.ID] = class
		} else if held != class {
			m.queueReorderPending = true
		}
	}
	for id := range m.queueClasses {
		if !present[id] {
			delete(m.queueClasses, id)
		}
	}
}

func (m *FactoryModel) resetFactoryQueueClasses() {
	m.queueClasses = make(map[string]factoryQueueClass, len(m.orders))
	for _, order := range m.orders {
		m.queueClasses[order.ID] = classifyFactoryOrder(order)
	}
	m.queueReorderPending = false
}

func (m *FactoryModel) regroupFactoryQueue() {
	selectedID := ""
	if current, ok := m.current(); ok {
		selectedID = current.ID
	}
	m.orders = sortFactoryOrders(m.orders)
	m.resetFactoryQueueClasses()
	for index, order := range m.orders {
		if order.ID == selectedID {
			m.selected = index
			break
		}
	}
}

func factoryTitleFromBrief(brief string) string {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return ""
	}
	line, _, _ := strings.Cut(brief, "\n")
	line = strings.TrimSpace(strings.TrimLeft(line, "#*- "))
	if index := strings.IndexAny(line, ".!?"); index > 0 {
		line = line[:index]
	}
	lower := strings.ToLower(line)
	for _, prefix := range []string{"add ", "build ", "create ", "implement ", "introduce "} {
		if strings.HasPrefix(lower, prefix) && len(line) > len(prefix) {
			line = strings.TrimSpace(line[len(prefix):])
			break
		}
	}
	runes := []rune(line)
	if len(runes) > 0 {
		runes[0] = unicode.ToUpper(runes[0])
	}
	return clip(string(runes), 72)
}

func (m *FactoryModel) View() string {
	if m.width == 0 {
		return ""
	}
	if m.mode == "new" {
		return m.formView()
	}
	if m.mode == "note" {
		return m.noteView()
	}
	if m.mode == "help" {
		return m.factoryHelpView()
	}
	return m.boardView()
}
func (m *FactoryModel) boardView() string { return m.factoryConsoleView() }

type factoryStageVisual struct {
	label  string
	detail string
	color  lipgloss.Color
	step   int
}

func factoryVisual(feature factory.WorkOrder) factoryStageVisual {
	if feature.Status == "blocked" || feature.Stage == factory.StageFailed {
		return factoryStageVisual{label: "BLOCKED", detail: "Factory needs attention before it can continue.", color: Tokyo.Red, step: factoryStep(feature.Stage)}
	}
	if feature.Status == "cancelled" {
		return factoryStageVisual{label: "CANCELED", detail: "Factory is no longer processing this feature.", color: Tokyo.Muted, step: factoryStep(feature.Stage)}
	}
	switch feature.Stage {
	case factory.StageIntake:
		if feature.Status == "queued" {
			return factoryStageVisual{label: "QUEUED", detail: "The feature is waiting for Factory capacity.", color: Tokyo.Muted, step: 0}
		}
		return factoryStageVisual{label: "PREPARING", detail: "Factory is preparing the feature brief.", color: Tokyo.Yellow, step: 0}
	case factory.StagePlanning:
		return factoryStageVisual{label: "PLANNING", detail: "The planner is preparing the implementation plan.", color: Tokyo.Yellow, step: 1}
	case factory.StagePlanApproval:
		return factoryStageVisual{label: "PLAN READY", detail: "The implementation plan needs your decision.", color: Tokyo.Yellow, step: 1}
	case factory.StageImplementation:
		return factoryStageVisual{label: "BUILDING", detail: "The developer is implementing the approved plan.", color: Tokyo.Yellow, step: 2}
	case factory.StageHumanTest:
		return factoryStageVisual{label: "READY TO TEST", detail: "A committed implementation is ready for your test.", color: Tokyo.Yellow, step: 3}
	case factory.StageReview:
		return factoryStageVisual{label: "REVIEWING", detail: "Independent reviewers are checking the implementation.", color: Tokyo.Yellow, step: 4}
	case factory.StageReviewFixes:
		return factoryStageVisual{label: "FIXING", detail: "The developer is addressing review findings.", color: Tokyo.Yellow, step: 4}
	case factory.StagePRCI:
		return factoryStageVisual{label: "CHECKS", detail: "The pull request and required checks are in progress.", color: Tokyo.Yellow, step: 5}
	case factory.StageWaitingForApproval:
		return factoryStageVisual{label: "READY TO MERGE", detail: "Reviews and checks passed for this commit.", color: Tokyo.Yellow, step: 5}
	case factory.StageComplete:
		return factoryStageVisual{label: "SHIPPED", detail: "The pull request was merged.", color: Tokyo.Green, step: 6}
	default:
		return factoryStageVisual{label: "UNKNOWN", detail: "Factory is reading the latest state.", color: Tokyo.Muted, step: factoryStep(feature.Stage)}
	}
}

func factoryStep(stage factory.Stage) int {
	switch stage {
	case factory.StagePlanning, factory.StagePlanApproval:
		return 1
	case factory.StageImplementation:
		return 2
	case factory.StageHumanTest:
		return 3
	case factory.StageReview, factory.StageReviewFixes:
		return 4
	case factory.StagePRCI, factory.StageWaitingForApproval:
		return 5
	case factory.StageComplete:
		return 6
	default:
		return 0
	}
}

func relativeFactoryTime(timestamp int64) string {
	if timestamp <= 0 {
		return "now"
	}
	delta := time.Since(time.UnixMilli(timestamp))
	if delta < time.Minute {
		return "now"
	}
	if delta < time.Hour {
		return fmt.Sprintf("%dm", int(delta.Minutes()))
	}
	if delta < 24*time.Hour {
		return fmt.Sprintf("%dh", int(delta.Hours()))
	}
	return fmt.Sprintf("%dd", int(delta.Hours()/24))
}

func factoryMetadata(feature factory.WorkOrder, width int) string {
	parts := make([]string, 0, 3)
	label := lipgloss.NewStyle().Foreground(Tokyo.Comment).Render
	value := lipgloss.NewStyle().Foreground(Tokyo.Foreground).Render
	if feature.Commit != "" {
		parts = append(parts, label("COMMIT ")+value(shortUI(feature.Commit)))
	}
	if feature.PRURL != "" {
		parts = append(parts, label("PR ")+value(clip(feature.PRURL, max(12, width/2))))
	}
	if feature.CIStatus != "" {
		parts = append(parts, label("CHECKS ")+value(strings.ToUpper(feature.CIStatus)))
	}
	return strings.Join(parts, "   ")
}

func factoryEventMessage(event factory.Event) string {
	if event.Kind != "action" {
		return event.Message
	}
	switch event.Message {
	case "approve_plan":
		return "Plan approved"
	case "review_plan":
		return "Plan annotations submitted"
	case "test_pass":
		return "Implementation test passed"
	case "test_fail":
		return "Implementation test failed"
	case "approve_merge":
		return "Merge approved"
	case "stop_agent":
		return "Agent stopped by you"
	case "update_brief":
		return "Feature brief updated"
	case "retry":
		return "Factory resumed"
	case "cancel":
		return "Feature cancelled"
	default:
		return strings.ReplaceAll(event.Message, "_", " ")
	}
}

func (m *FactoryModel) noteView() string {
	inner := max(1, m.width-4)
	header := titleLine(factoryActionTitle(m.pendingAction), "Factory action", inner)
	bodyHeight := max(1, m.height-lipgloss.Height(header)-3)
	var lines []string
	var hints []string
	switch m.pendingAction {
	case "delete_feature":
		lines = []string{
			consoleSection("CONFIRM ACTION"),
			lipgloss.NewStyle().Foreground(Tokyo.Red).Background(Tokyo.Background).Bold(true).Render(consoleMark(iconFailure) + " Delete this feature permanently?"),
			"",
			consoleParagraph("Factory will remove its brief, plan, runs, events, and history. Any active Factory runtime will stop. The ordinary Galpon agent and worktree remain available.", inner),
		}
		hints = []string{keyHint("esc", "cancel"), keyHint("enter", "delete feature")}
	case "stop_agent":
		lines = []string{
			consoleSection("CONFIRM ACTION"),
			lipgloss.NewStyle().Foreground(Tokyo.Red).Background(Tokyo.Background).Bold(true).Render(consoleMark(iconFailure) + " Stop the current agent?"),
			"",
			consoleParagraph("The feature will wait until you retry it.", inner),
		}
		hints = []string{keyHint("esc", "cancel"), keyHint("enter", "stop agent")}
	case "approve_merge":
		lines = []string{
			consoleSection("CONFIRM ACTION"),
			lipgloss.NewStyle().Foreground(Tokyo.Yellow).Background(Tokyo.Background).Bold(true).Render(consoleMark(iconAttention) + " Approve this pull request merge?"),
			"",
			consoleParagraph("Factory will merge the approved pull request when the service confirms this action.", inner),
		}
		hints = []string{keyHint("esc", "cancel"), keyHint("enter", "approve merge")}
	default:
		prompt := "Add feedback or test details."
		if m.pendingAction == "update_brief" {
			prompt = "Fix the feature brief, then resume Factory work."
		}
		m.note.SetWidth(inner)
		m.note.SetHeight(max(4, bodyHeight-4))
		lines = []string{consoleSection("DETAILS"), mutedStyle.Render(prompt), "", factoryPersistentBlock(m.note.View(), Tokyo.Background)}
		hints = []string{keyHint("esc", "cancel"), keyHint("alt+s", "save")}
	}
	notice := mutedStyle.Render("Review the action before you submit.")
	if m.submitting {
		notice = lipgloss.NewStyle().Foreground(Tokyo.Yellow).Background(Tokyo.Background).Render(consoleActivityMark() + " Submitting")
	} else if m.err != nil {
		notice = consoleError("! "+m.err.Error(), inner)
	}
	body := consoleRows(strings.Join(lines, "\n"), inner, bodyHeight)
	content := strings.Join([]string{header, body, notice, consoleRule(inner), footerBar(inner, hints...)}, "\n")
	content = lipgloss.NewStyle().Padding(0, 2).Render(content)
	return factoryPersistentBlock(appBackground.Render(content), Tokyo.Background)
}
func clip(s string, n int) string {
	s = consoleText(strings.TrimSpace(s))
	if lipgloss.Width(s) <= n {
		return s
	}
	return ansi.Truncate(s, max(0, n), "…")
}
func factoryActionTitle(action string) string {
	switch action {
	case "test_fail":
		return "REPORT FAILED TEST"
	case "cancel":
		return "CANCEL FEATURE"
	case "stop_agent":
		return "STOP ACTIVE AGENT"
	case "delete_feature":
		return "DELETE FEATURE"
	case "approve_merge":
		return "APPROVE MERGE"
	case "update_brief":
		return "FIX FEATURE BRIEF"
	default:
		return strings.ToUpper(strings.ReplaceAll(action, "_", " "))
	}
}

func shortUI(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}
func factoryReviewPalette() map[string]string {
	return map[string]string{
		"Background": string(Tokyo.Background), "Surface": string(Tokyo.Surface), "SurfaceRaised": string(Tokyo.SurfaceRaised),
		"Prompt": string(Tokyo.Prompt), "Selection": string(Tokyo.Selection), "Border": string(Tokyo.Border),
		"Foreground": string(Tokyo.Foreground), "Muted": string(Tokyo.Muted), "Comment": string(Tokyo.Comment),
		"Status": string(Tokyo.Status), "StatusInk": string(Tokyo.StatusInk), "Blue": string(Tokyo.Blue),
		"Cyan": string(Tokyo.Cyan), "Purple": string(Tokyo.Purple), "Green": string(Tokyo.Green),
		"Orange": string(Tokyo.Orange), "Red": string(Tokyo.Red), "Yellow": string(Tokyo.Yellow), "Teal": string(Tokyo.Teal),
	}
}
