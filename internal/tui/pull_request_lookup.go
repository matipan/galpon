package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/model"
)

type pullRequestLookup struct {
	key        string
	generation uint64
	loading    bool
	loaded     bool
	at         time.Time
	creators   []model.PullRequestCreator
	err        error
}

type pullRequestLookupTick uint64

type pullRequestLookupMsg struct {
	generation uint64
	creators   []model.PullRequestCreator
	err        error
}

func (m Model) pullRequestReference() (model.PullRequestReference, bool) {
	if m.scope != nil {
		return model.PullRequestReference{}, false
	}
	return model.ParsePullRequestReference(m.query.Value())
}

func (m *Model) lookupPullRequestCreators() tea.Cmd {
	ref, ok := m.pullRequestReference()
	if !ok {
		m.prLookup = nil
		return nil
	}
	if m.client == nil {
		return nil
	}
	key := ref.String()
	known := m.prLookup
	if known != nil && known.key == key && (known.loading || time.Since(known.at) < pullRequestPoll) {
		return nil
	}
	m.prLookupGeneration++
	lookup := &pullRequestLookup{key: key, generation: m.prLookupGeneration, loading: true}
	if known != nil && known.key == key {
		lookup.loaded, lookup.creators = known.loaded, known.creators
	}
	m.prLookup = lookup
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return pullRequestLookupTick(lookup.generation) })
}

func (m *Model) readPullRequestCreators(generation uint64) tea.Cmd {
	lookup := m.prLookup
	ref, ok := m.pullRequestReference()
	if !ok || lookup == nil || lookup.generation != generation || lookup.key != ref.String() {
		return nil
	}
	client, key := m.client, lookup.key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		creators, err := client.LookupPullRequestCreators(ctx, key)
		return pullRequestLookupMsg{generation: generation, creators: creators, err: err}
	}
}

func (m *Model) applyPullRequestLookup(value pullRequestLookupMsg) {
	lookup := m.prLookup
	ref, ok := m.pullRequestReference()
	if !ok || lookup == nil || lookup.generation != value.generation || lookup.key != ref.String() {
		return
	}
	lookup.creators, lookup.err = value.creators, value.err
	lookup.loading, lookup.loaded, lookup.at = false, true, time.Now()
	m.refreshResults()
}

func (m Model) pullRequestLookupResults() []searchResult {
	ref, ok := m.pullRequestReference()
	if !ok || m.prLookup == nil || m.prLookup.key != ref.String() || m.prLookup.err != nil {
		return nil
	}
	return pullRequestCreatorResults(m.prLookup.creators)
}

func (m Model) pullRequestLookupNotice() string {
	ref, ok := m.pullRequestReference()
	if !ok {
		return ""
	}
	lookup := m.prLookup
	if lookup == nil || lookup.key != ref.String() || !lookup.loaded {
		return "Looking up PR creators…"
	}
	if lookup.err != nil {
		return "Cannot look up PR creators: " + lookup.err.Error()
	}
	return "No recorded agent."
}

// creatorDashboard supplies the agent facts needed to render lookup results,
// including creators that do not appear in the visible dashboard.
func creatorDashboard(creators []model.PullRequestCreator) model.Dashboard {
	var d model.Dashboard
	agents, workspaces := map[string]bool{}, map[string]bool{}
	for _, creator := range creators {
		agent := creator.Agent
		if !agents[agent.ID] {
			d.Agents = append(d.Agents, agent)
			agents[agent.ID] = true
		}
		if !workspaces[agent.WorkspaceID] {
			d.Workspaces = append(d.Workspaces, model.Workspace{ID: agent.WorkspaceID, Title: creator.WorkspaceTitle})
			workspaces[agent.WorkspaceID] = true
		}
	}
	return d
}

// One agent can create the same PR number in several repositories. Keep one
// selectable row for it and show every matching repository in its label.
func pullRequestCreatorResults(creators []model.PullRequestCreator) []searchResult {
	items := map[string]searchResult{}
	for _, item := range buildAgentResults(creatorDashboard(creators), "") {
		items[item.ID] = item
	}
	refs := map[string][]string{}
	for _, creator := range creators {
		refs[creator.Agent.ID] = append(refs[creator.Agent.ID], creator.Ref())
	}
	out := []searchResult{}
	for _, creator := range creators {
		item, ok := items[creator.Agent.ID]
		if !ok {
			continue
		}
		delete(items, item.ID)
		item.CreatorRef = strings.Join(refs[item.ID], " · ")
		item.Group = "PR CREATORS"
		out = append(out, item)
	}
	return out
}

// BuildNativePullRequestCreators returns agent rows for a structured PR lookup.
func BuildNativePullRequestCreators(creators []model.PullRequestCreator) []NativeItem {
	m := Model{dashboard: creatorDashboard(creators)}
	out := []NativeItem{}
	for _, result := range pullRequestCreatorResults(creators) {
		item := m.nativeItem(result)
		item.Ref = result.CreatorRef
		out = append(out, item)
	}
	return out
}
