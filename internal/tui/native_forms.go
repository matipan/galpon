package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/herdrapp"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/terminal"
)

// The native command center draws the forms of the terminal command center.
// A form starts from a draft that the terminal form logic makes, and a submit
// goes through the terminal create functions, so both forms have the same
// defaults, rules and messages. Native drafts name resources by ID, because
// the native client keeps a draft while the dashboard changes.

// NativeCatalog holds what the native forms choose from.
type NativeCatalog struct {
	DefaultHarness string                    `json:"defaultHarness"`
	Harnesses      []NativeOption            `json:"harnesses"`
	Workspaces     []NativeCatalogWorkspace  `json:"workspaces"`
	Repositories   []NativeCatalogRepository `json:"repositories"`
	// Agents are in dashboard order. Context agents have a saved
	// conversation.
	Agents []NativeCatalogAgent `json:"agents"`
	// Worktrees holds the worktrees that the form shows.
	Worktrees []NativeCatalogWorktree `json:"worktrees"`
}

type NativeOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type NativeCatalogWorkspace struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`
}

type NativeCatalogRepository struct {
	ID            string                `json:"id"`
	Title         string                `json:"title"`
	DefaultBranch string                `json:"defaultBranch"`
	DefaultRemote string                `json:"defaultRemote"`
	Remotes       []NativeCatalogRemote `json:"remotes"`
}

type NativeCatalogRemote struct {
	Name     string `json:"name"`
	FetchURL string `json:"fetchUrl"`
}

type NativeCatalogAgent struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	WorkspaceID string `json:"workspaceId"`
	Harness     string `json:"harness"`
	View        string `json:"view,omitempty"`
	Context     bool   `json:"context,omitempty"`
}

type NativeCatalogWorktree struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// NativeAgentDraft is a New Agent form. Placement is the placement type: 0 new
// private worktrees, 1 copy an agent placement, 2 new managed directory, 3 use
// an external directory, 4 use the selected worktree.
type NativeAgentDraft struct {
	Harness             string                `json:"harness"`
	Name                string                `json:"name"`
	Role                string                `json:"role"`
	WorkspaceID         string                `json:"workspaceId"`
	ContextAgentID      string                `json:"contextAgentId"`
	Placement           int                   `json:"placement"`
	PlacementAgentID    string                `json:"placementAgentId"`
	SuggestedWorktreeID string                `json:"suggestedWorktreeId"`
	Share               bool                  `json:"share"`
	CWD                 string                `json:"cwd"`
	Worktrees           []NativeAgentWorktree `json:"worktrees"`
}

type NativeAgentWorktree struct {
	RepositoryID string `json:"repositoryId"`
	Remote       string `json:"remote"`
	Ref          string `json:"ref"`
	FetchFirst   bool   `json:"fetchFirst"`
}

// NativeWorktreeDraft is a New Worktree form. An empty WorkspaceID creates a
// workspace with WorkspaceTitle. Editor opens the editor instead of a shell.
type NativeWorktreeDraft struct {
	RepositoryID   string `json:"repositoryId"`
	WorkspaceID    string `json:"workspaceId"`
	WorkspaceTitle string `json:"workspaceTitle"`
	Remote         string `json:"remote"`
	Ref            string `json:"ref"`
	FetchFirst     bool   `json:"fetchFirst"`
	Editor         bool   `json:"editor"`
}

// NativeRemoteDraft is an Add Remote form.
type NativeRemoteDraft struct {
	RepositoryID string `json:"repositoryId"`
	Name         string `json:"name"`
	FetchURL     string `json:"fetchUrl"`
	PushURL      string `json:"pushUrl"`
	PushDefault  bool   `json:"pushDefault"`
}

// NativeForm is a form to show, with its first draft.
type NativeForm struct {
	Form     string               `json:"form"`
	Status   string               `json:"status,omitempty"`
	Catalog  NativeCatalog        `json:"catalog"`
	Agent    *NativeAgentDraft    `json:"agent,omitempty"`
	Worktree *NativeWorktreeDraft `json:"worktree,omitempty"`
	Remote   *NativeRemoteDraft   `json:"remote,omitempty"`
}

// NativeResult is the result of a native action. Close is true when the
// action opened what it made, so that the command center closes. Field names
// the form field that an Error is about.
type NativeResult struct {
	OK      bool   `json:"ok,omitempty"`
	Close   bool   `json:"close,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
	Field   string `json:"field,omitempty"`
	// Place is where the app shows the agent or the terminal that the action
	// opened. A screen uses it to name and focus that terminal.
	Place *herdrapp.Placement `json:"place,omitempty"`
}

func nativeError(err error) NativeResult {
	if err == nil {
		return NativeResult{Error: "the action did not complete"}
	}
	return NativeResult{Error: err.Error()}
}

func nativeFormModel(client *app.Client, renderer terminal.Renderer, d model.Dashboard) Model {
	m := NewWithStartup(client, renderer, StartupRoute{})
	m.dashboard = d
	m.loaded = true
	return m
}

func nativeCatalog(d model.Dashboard, worktreeIDs ...string) NativeCatalog {
	catalog := NativeCatalog{DefaultHarness: d.DefaultHarness, Harnesses: []NativeOption{}, Workspaces: []NativeCatalogWorkspace{}, Repositories: []NativeCatalogRepository{}, Agents: []NativeCatalogAgent{}, Worktrees: []NativeCatalogWorktree{}}
	if catalog.DefaultHarness == "" {
		catalog.DefaultHarness = model.HarnessPi
	}
	for _, harness := range agentHarnesses {
		catalog.Harnesses = append(catalog.Harnesses, NativeOption{Value: harness, Label: model.HarnessChoiceLabel(harness)})
	}
	for _, workspace := range d.Workspaces {
		catalog.Workspaces = append(catalog.Workspaces, NativeCatalogWorkspace{ID: workspace.ID, Title: workspace.Title, Status: workspace.Status})
	}
	for _, repository := range d.Repositories {
		item := NativeCatalogRepository{ID: repository.ID, Title: repository.Title, DefaultBranch: repository.DefaultBranch, DefaultRemote: repository.DefaultRemote, Remotes: []NativeCatalogRemote{}}
		for _, remote := range repository.Remotes {
			item.Remotes = append(item.Remotes, NativeCatalogRemote{Name: remote.Name, FetchURL: remote.FetchURL})
		}
		catalog.Repositories = append(catalog.Repositories, item)
	}
	for _, agent := range d.Agents {
		catalog.Agents = append(catalog.Agents, NativeCatalogAgent{ID: agent.ID, Title: agent.Title, WorkspaceID: agent.WorkspaceID, Harness: agent.Harness(), View: agent.View, Context: agent.SessionPath != ""})
	}
	for _, id := range worktreeIDs {
		if worktree, ok := d.Worktree(id); ok {
			repository, _ := d.Repository(worktree.RepositoryID)
			catalog.Worktrees = append(catalog.Worktrees, NativeCatalogWorktree{ID: id, Label: repository.Title + " · " + worktree.CheckoutLabel()})
		}
	}
	return catalog
}

// nativeResult finds a command center row by kind and ID.
func nativeResult(d model.Dashboard, kind, id string) (searchResult, bool) {
	if kind == "" {
		return searchResult{}, false
	}
	for _, item := range buildResults(d, "") {
		if string(item.Kind) == kind && item.ID == id {
			return item, true
		}
	}
	return searchResult{}, false
}

// NativeAgentRequest says how a New Agent form starts. Mode "new" starts from
// the selected row like Ctrl-N, "here" like the a action (a selected worktree
// becomes a placement choice), "fork" forks the selected agent, and "focus"
// starts in WorkspaceID from AgentID, for the agent that has the keyboard.
type NativeAgentRequest struct {
	Mode        string
	Kind        string
	ID          string
	WorkspaceID string
	AgentID     string
}

// NativeAgentForm makes the first draft of a New Agent form.
func NativeAgentForm(d model.Dashboard, request NativeAgentRequest) (NativeForm, error) {
	m := nativeFormModel(nil, nil, d)
	selected, found := nativeResult(d, request.Kind, request.ID)
	if found && selected.Hidden {
		return NativeForm{}, fmt.Errorf("%s is hidden · press x to unhide it", selected.Title)
	}
	workspaceID := ""
	if found {
		if _, ok := d.Workspace(selected.WorkspaceID); ok {
			workspaceID = selected.WorkspaceID
		}
	} else if request.Kind == "" && len(d.Workspaces) > 0 {
		workspaceID = d.Workspaces[0].ID
	}
	switch request.Mode {
	case "new":
		if workspaceID == "" {
			if found {
				return NativeForm{}, fmt.Errorf("this item does not have an available workspace")
			}
			return NativeForm{}, fmt.Errorf("select an item that has an available workspace")
		}
		sourceAgentID, sourceWorktreeID := "", ""
		switch selected.Kind {
		case resultAgent:
			sourceAgentID = selected.ID
		case resultWorktree:
			sourceWorktreeID = selected.WorktreeID
		}
		m.beginAgentFormWithSource(workspaceID, "", sourceAgentID, sourceWorktreeID)
	case "here":
		if workspaceID == "" {
			return NativeForm{}, fmt.Errorf("select a workspace first")
		}
		suggested := ""
		if selected.Kind == resultWorktree {
			suggested = selected.WorktreeID
		}
		m.beginAgentForm(workspaceID, suggested)
	case "fork":
		if !found || selected.Kind != resultAgent {
			return NativeForm{}, fmt.Errorf("select an agent to fork")
		}
		m.beginAgentForkForm(selected.ID)
		if m.form != formAgent {
			return NativeForm{}, fmt.Errorf("%s", strings.ToLower(m.status))
		}
	case "focus":
		if _, ok := d.Workspace(request.WorkspaceID); !ok {
			return NativeForm{}, fmt.Errorf("the current Galpon workspace is no longer available")
		}
		m.beginAgentFormFromSource(request.WorkspaceID, "", request.AgentID)
	default:
		return NativeForm{}, fmt.Errorf("unknown New Agent mode %q", request.Mode)
	}
	draft := m.nativeAgentDraft()
	return NativeForm{Form: "agent", Status: m.status, Catalog: nativeCatalog(d, draft.SuggestedWorktreeID), Agent: &draft}, nil
}

func (m Model) nativeAgentDraft() NativeAgentDraft {
	in := m.agentDraft
	out := NativeAgentDraft{Harness: m.draftHarness(), Name: in.Name, Role: in.Role, WorkspaceID: in.WorkspaceID, Placement: in.Placement, SuggestedWorktreeID: in.SuggestedWorktreeID, Share: in.Share, CWD: in.CWD, Worktrees: []NativeAgentWorktree{}}
	if contexts := m.contextAgents(); in.Context > 0 && in.Context <= len(contexts) {
		out.ContextAgentID = contexts[in.Context-1].ID
	}
	if placements := m.placementAgents(); in.PlacementAgent >= 0 && in.PlacementAgent < len(placements) {
		out.PlacementAgentID = placements[in.PlacementAgent].ID
	}
	for _, worktree := range in.Worktrees {
		draft := NativeAgentWorktree{Ref: worktree.Ref, FetchFirst: worktree.FetchFirst}
		if worktree.Repository >= 0 && worktree.Repository < len(m.dashboard.Repositories) {
			repository := m.dashboard.Repositories[worktree.Repository]
			draft.RepositoryID = repository.ID
			if worktree.Remote >= 0 && worktree.Remote < len(repository.Remotes) {
				draft.Remote = repository.Remotes[worktree.Remote].Name
			}
		}
		out.Worktrees = append(out.Worktrees, draft)
	}
	return out
}

// loadNativeAgentDraft fills the terminal form from a native draft. A
// resource that is no longer available stays unselected, so that the create
// rules report it.
func (m *Model) loadNativeAgentDraft(in NativeAgentDraft) {
	m.screen, m.form = screenForm, formAgent
	m.formContext = in.WorkspaceID
	m.agentDraft = agentDraft{Harness: in.Harness, Name: in.Name, Role: in.Role, WorkspaceID: in.WorkspaceID, Placement: in.Placement, PlacementAgent: -1, SuggestedWorktreeID: in.SuggestedWorktreeID, Share: in.Share, CWD: in.CWD}
	if in.ContextAgentID != "" {
		for index, agent := range m.contextAgents() {
			if agent.ID == in.ContextAgentID {
				m.agentDraft.Context = index + 1
				break
			}
		}
	}
	for index, agent := range m.placementAgents() {
		if agent.ID == in.PlacementAgentID {
			m.agentDraft.PlacementAgent = index
			break
		}
	}
	for _, worktree := range in.Worktrees {
		draft := agentWorktreeDraft{Repository: -1, Ref: worktree.Ref, FetchFirst: worktree.FetchFirst}
		if index, ok := repositoryIndexByID(m.dashboard.Repositories, worktree.RepositoryID); ok {
			draft.Repository = index
			for remoteIndex, remote := range m.dashboard.Repositories[index].Remotes {
				if remote.Name == worktree.Remote {
					draft.Remote = remoteIndex
					break
				}
			}
		}
		m.agentDraft.Worktrees = append(m.agentDraft.Worktrees, draft)
	}
	// The create field holds no text, so the submit reads only the draft.
	m.agentFocus = len(m.agentFields()) - 1
	m.loadAgentInput()
}

var nativeAgentFieldNames = map[agentFieldKind]string{
	agentName: "name", agentRole: "role", agentHarness: "harness", agentWorkspace: "workspace",
	agentContext: "context", agentPlacement: "placement", agentRepository: "repository",
	agentRemote: "remote", agentRef: "ref", agentFetch: "fetch", agentAddWorktree: "add",
	agentPlacementSource: "placementSource", agentWorktreeSource: "worktreeSource",
	agentShare: "share", agentCWD: "cwd", agentCreate: "create",
}

// nativeAgentFieldKey names a New Agent field as the native form does:
// "name", or "repository:1" for a field of the second repository.
func nativeAgentFieldKey(field agentField) string {
	name := nativeAgentFieldNames[field.Kind]
	switch field.Kind {
	case agentRepository, agentRemote, agentRef, agentFetch:
		return fmt.Sprintf("%s:%d", name, field.Worktree)
	}
	return name
}

// SubmitNativeAgent creates the agent of a native New Agent form and opens it
// through the client renderer.
func SubmitNativeAgent(client *app.Client, d model.Dashboard, draft NativeAgentDraft) NativeResult {
	m := nativeFormModel(client, nil, d)
	if draft.Placement < 0 || draft.Placement > 4 {
		return NativeResult{Error: "choose a placement type", Field: "placement"}
	}
	m.loadNativeAgentDraft(draft)
	command := m.createAgent()
	if command == nil {
		result := nativeError(m.err)
		if fields := m.agentFields(); m.agentFocus >= 0 && m.agentFocus < len(fields) {
			result.Field = nativeAgentFieldKey(fields[m.agentFocus])
		}
		return result
	}
	msg, _ := command().(createMsg)
	switch {
	case msg.err != nil:
		return nativeError(msg.err)
	case msg.quit:
		return NativeResult{OK: true, Close: true}
	default:
		// The agent exists, but its terminal did not open.
		return NativeResult{OK: true, Message: msg.message}
	}
}

// NativeAgentCreateRequest makes the create request of a native New Agent
// draft, without creating the agent. A result with an Error tells what is
// missing.
func NativeAgentCreateRequest(d model.Dashboard, draft NativeAgentDraft) (app.CreateAgentRequest, NativeResult) {
	m := nativeFormModel(nil, nil, d)
	if draft.Placement < 0 || draft.Placement > 4 {
		return app.CreateAgentRequest{}, NativeResult{Error: "choose a placement type", Field: "placement"}
	}
	m.loadNativeAgentDraft(draft)
	request, ok := m.agentRequest()
	if !ok {
		return request, nativeError(m.err)
	}
	return request, NativeResult{OK: true}
}

// NativeWorktreeForm makes the first draft of a New Worktree form for a
// repository.
func NativeWorktreeForm(d model.Dashboard, repositoryID string, editor bool) (NativeForm, error) {
	m := nativeFormModel(nil, nil, d)
	m.beginWorktreeForm(repositoryID, nil)
	if m.form != formWorktree {
		return NativeForm{}, m.err
	}
	in := m.worktreeDraft
	draft := NativeWorktreeDraft{RepositoryID: in.RepositoryID, WorkspaceID: in.WorkspaceID, WorkspaceTitle: in.WorkspaceTitle, Remote: in.Remote, Ref: in.Ref, FetchFirst: in.FetchFirst, Editor: editor}
	return NativeForm{Form: "worktree", Catalog: nativeCatalog(d), Worktree: &draft}, nil
}

// SubmitNativeWorktree creates the worktree of a native New Worktree form and
// opens a terminal in it.
func SubmitNativeWorktree(client *app.Client, renderer terminal.Renderer, d model.Dashboard, draft NativeWorktreeDraft) NativeResult {
	m := nativeFormModel(client, renderer, d)
	var command []string
	if draft.Editor {
		command = EditorCommand()
	}
	m.beginWorktreeForm(draft.RepositoryID, command)
	if m.form != formWorktree {
		return nativeError(m.err)
	}
	m.worktreeDraft.WorkspaceID = draft.WorkspaceID
	m.worktreeDraft.WorkspaceTitle = draft.WorkspaceTitle
	m.worktreeDraft.Remote = draft.Remote
	m.worktreeDraft.Ref = draft.Ref
	m.worktreeDraft.FetchFirst = draft.FetchFirst
	m.worktreeFocus = len(m.worktreeFields()) - 1
	create := m.createWorktree()
	if create == nil {
		return nativeError(m.err)
	}
	msg, _ := create().(worktreeCreateMsg)
	switch {
	case msg.value.Worktree.ID == "":
		return nativeError(msg.err)
	case msg.err != nil:
		return NativeResult{OK: true, Message: "Worktree created, but the terminal did not open: " + msg.err.Error()}
	default:
		return NativeResult{OK: true, Close: true}
	}
}

// NativeRemoteForm makes the first draft of an Add Remote form. The selected
// repository, or the repository of the selected worktree, comes first.
func NativeRemoteForm(d model.Dashboard, kind, id string) (NativeForm, error) {
	if len(d.Repositories) == 0 {
		return NativeForm{}, fmt.Errorf("add a repository first")
	}
	m := nativeFormModel(nil, nil, d)
	selected, _ := nativeResult(d, kind, id)
	m.beginRemoteForm(selected)
	draft := NativeRemoteDraft{}
	if m.remoteDraft.Repository >= 0 && m.remoteDraft.Repository < len(d.Repositories) {
		draft.RepositoryID = d.Repositories[m.remoteDraft.Repository].ID
	}
	return NativeForm{Form: "remote", Catalog: nativeCatalog(d), Remote: &draft}, nil
}

// SubmitNativeRemote adds the remote of a native Add Remote form.
func SubmitNativeRemote(client *app.Client, d model.Dashboard, draft NativeRemoteDraft) NativeResult {
	m := nativeFormModel(client, nil, d)
	m.beginRemoteForm(searchResult{Kind: resultRepository, ID: draft.RepositoryID})
	if _, ok := repositoryIndexByID(d.Repositories, draft.RepositoryID); !ok {
		m.remoteDraft.Repository = -1
	}
	m.remoteDraft.Name, m.remoteDraft.FetchURL, m.remoteDraft.PushURL, m.remoteDraft.PushDefault = draft.Name, draft.FetchURL, draft.PushURL, draft.PushDefault
	m.remoteFocus = 5
	create := m.createRemote()
	if create == nil {
		return nativeError(m.err)
	}
	msg, _ := create().(createMsg)
	if msg.err != nil {
		return nativeError(msg.err)
	}
	return NativeResult{OK: true, Message: msg.message}
}

// SubmitNativeSimple adds a repository ("repository") or creates a workspace
// ("workspace").
func SubmitNativeSimple(client *app.Client, kind, value string) NativeResult {
	value = strings.TrimSpace(value)
	form := formWorkspace
	switch kind {
	case "workspace":
		if value == "" {
			return NativeResult{Error: "write a workspace title"}
		}
	case "repository":
		form = formRepository
		if value == "" {
			return NativeResult{Error: "write a local path or a Git URL"}
		}
	default:
		return NativeResult{Error: fmt.Sprintf("unknown form %q", kind)}
	}
	msg := createSimple(client, form, value)
	if msg.err != nil {
		return nativeError(msg.err)
	}
	return NativeResult{OK: true, Message: msg.message}
}

// NativeHide hides a resource, or unhides a hidden one, with its dependent
// items.
func NativeHide(ctx context.Context, client *app.Client, kind, id, title string, hidden bool) NativeResult {
	if hidden {
		value, err := client.RestoreResource(ctx, kind, id)
		if err != nil {
			return nativeError(err)
		}
		return NativeResult{OK: true, Message: restoredStatus(title, value)}
	}
	value, err := client.DeleteResource(ctx, kind, id)
	if err != nil {
		return nativeError(err)
	}
	return NativeResult{OK: true, Message: hiddenStatus(title, value)}
}

// NativeRename gives an agent a new name. An error about the name marks the
// name field.
func NativeRename(ctx context.Context, client *app.Client, agentID, title string) NativeResult {
	if strings.TrimSpace(title) == "" {
		return NativeResult{Error: "write a name", Field: "value"}
	}
	agent, err := client.RenameAgent(ctx, agentID, title)
	if err != nil {
		result := nativeError(err)
		if strings.HasPrefix(err.Error(), "agent title") {
			result.Field = "value"
		}
		return result
	}
	return NativeResult{OK: true, Message: "Renamed the agent to " + agent.Title}
}

// NativeSend sends a message from the person to an agent.
func NativeSend(ctx context.Context, client *app.Client, agentID, text string) NativeResult {
	if strings.TrimSpace(text) == "" {
		return NativeResult{Error: "write a message before sending"}
	}
	message, err := client.Send(ctx, agentID, text)
	if err != nil {
		return nativeError(messageError(err))
	}
	return NativeResult{OK: true, Message: messageStatus(message)}
}

// NativeTarget is a place where a terminal can open: an agent directory or a
// worktree.
type NativeTarget struct {
	WorkspaceID string `json:"workspaceId"`
	AgentID     string `json:"agentId,omitempty"`
	AgentTitle  string `json:"agentTitle,omitempty"`
	WorktreeID  string `json:"worktreeId,omitempty"`
	Path        string `json:"path"`
	Label       string `json:"label"`
	Detail      string `json:"detail,omitempty"`
}

// NativeTargets lists where a terminal for a row can open, as the terminal
// command center does: the directories and worktrees of an agent, of all the
// agents of a workspace and the other worktrees of that workspace, or one
// worktree.
func NativeTargets(d model.Dashboard, kind, id string) ([]NativeTarget, error) {
	m := nativeFormModel(nil, nil, d)
	selected, ok := nativeResult(d, kind, id)
	if !ok {
		return nil, fmt.Errorf("this %s is no longer available", strings.ReplaceAll(kind, "-", " "))
	}
	if selected.Hidden {
		return nil, fmt.Errorf("%s is hidden · press x to unhide it", selected.Title)
	}
	var targets []terminalTarget
	switch selected.Kind {
	case resultAgent:
		if agent, ok := d.Agent(selected.ID); ok {
			targets = m.targetsForAgent(agent)
		}
	case resultWorktree:
		worktree, ok := d.Worktree(selected.WorktreeID)
		if !ok {
			return nil, fmt.Errorf("worktree not found")
		}
		workspace, _ := d.Workspace(selected.WorkspaceID)
		repository, _ := d.Repository(worktree.RepositoryID)
		targets = append(targets, terminalTarget{WorkspaceID: workspace.ID, WorktreeID: worktree.ID, Path: worktree.Path, Label: workspace.Title + " · " + repository.Title})
	case resultWorkspace:
		targets = m.workspaceTargets(selected.WorkspaceID)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("this selection has no worktree")
	}
	out := make([]NativeTarget, 0, len(targets))
	for _, target := range targets {
		out = append(out, NativeTarget(target))
	}
	return out, nil
}

// OpenNativeTarget opens a terminal, or the editor, at a target.
func OpenNativeTarget(renderer terminal.Renderer, d model.Dashboard, target NativeTarget, editor bool) error {
	m := nativeFormModel(nil, renderer, d)
	var command []string
	if editor {
		command = EditorCommand()
	}
	msg, _ := m.openTerminalTarget(terminalTarget(target), command)().(actionMsg)
	return msg.err
}

// workspaceTargets lists the terminal places of a workspace: the places of its
// agents, then its other worktrees.
func (m *Model) workspaceTargets(workspaceID string) []terminalTarget {
	var targets []terminalTarget
	seen := map[string]bool{}
	for _, agent := range m.dashboard.Agents {
		if agent.WorkspaceID == workspaceID {
			for _, target := range m.targetsForAgent(agent) {
				targets = append(targets, target)
				seen[target.WorktreeID] = target.WorktreeID != ""
			}
		}
	}
	workspace, _ := m.dashboard.Workspace(workspaceID)
	for _, worktree := range m.dashboard.Worktrees {
		if worktree.WorkspaceID != workspaceID || seen[worktree.ID] {
			continue
		}
		repository, _ := m.dashboard.Repository(worktree.RepositoryID)
		targets = append(targets, terminalTarget{WorkspaceID: workspace.ID, AgentTitle: "Workspace worktrees", WorktreeID: worktree.ID, Path: worktree.Path, Label: workspace.Title + " · " + repository.Title, Detail: "workspace worktree · " + worktree.CheckoutLabel()})
	}
	return targets
}
