package herdrapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

// AgentMod is the mod that draws an agent in the app view: the agent tab.
const AgentMod = "galpon-agent"

// viewLabel marks the hidden terminal of the app host of an agent in the
// app view: "galpon.view=app".
const viewLabel = "galpon.view"

// An agent in the app view has two parts. Its app host (`galpon herdr-app
// host <agent>`) runs Pi in a hidden herdr-core terminal with the labels
// galpon.agent=<agent> and galpon.view=app. Its tab, "galpon-<agent>" in the
// space of its workspace, shows the agent mod, which talks to the host.
//
// The app has no command that makes a tab with a chosen ID, but it puts a
// terminal with herdr.space and herdr.tab labels in that space and tab. So
// the adapter gives the host terminal those labels, waits until the window
// shows it, splits the agent mod beside it, and parks the host terminal:
// its pane goes, its labels go, and it keeps running. Without an open
// window, the host keeps the labels; the window shows it when it opens, and
// the Galpon sidebar does the rest.

// openApp starts the app host of an agent when it does not run, and shows
// its tab.
func (a Adapter) openApp(ctx context.Context, ws model.Workspace, wt model.Worktree, agent model.Agent, command []string, focus bool) (string, string, bool, error) {
	current, err := a.agentTerminal(ctx, agent)
	if err != nil {
		return "", "", false, err
	}
	if current != nil && current.Labels[viewLabel] != "app" {
		return "", "", false, fmt.Errorf("agent %s still runs in a terminal; close it, then open it again", agent.Title)
	}
	place := placement{Placement: AgentPlacement(ws, agent), focus: focus}
	place.App = true
	started := false
	if current == nil {
		labels := []string{agentLabel + "=" + agent.ID, viewLabel + "=app"}
		if !a.windowOpen(ctx) {
			labels = append(labels, "herdr.space="+place.Space, "herdr.tab="+place.Tab)
		}
		if place.Terminal, err = a.start(ctx, wt.Path, terminalName(agent), labels, shellCommand(command, false)); err != nil {
			return "", "", false, err
		}
		started = true
	} else {
		place.Terminal = current.Address
	}
	a.arrange(place)
	return "", place.Terminal, started, nil
}

// windowOpen reports whether the app window on this computer runs.
func (a Adapter) windowOpen(ctx context.Context) bool {
	bin := a.App
	if bin == "" {
		bin = binary("herdr-app")
	}
	command := exec.CommandContext(ctx, bin, "status")
	command.Env = environment("HC_TERMINAL_ID")
	out, err := command.CombinedOutput()
	return err == nil && !strings.Contains(string(out), "no Herdr window")
}

// arrangeAppNow puts the agent mod in the tab of the agent, names the tab and
// the space, and focuses the tab.
func (a Adapter) arrangeAppNow(place placement) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	agentID := strings.TrimPrefix(place.Tab, "galpon-")
	doc, err := a.layoutDoc(ctx)
	if err != nil {
		a.logf("read the Herdr app layout for agent %s: %v", agentID, err)
		return
	}
	pane := doc.agentPane(place.Tab, agentID)
	// A terminal of the agent that just ended can still show in the layout,
	// with its tab: the window removes its pane a moment later. So a failed
	// split is tried again with the next layout.
	for attempt := 0; pane == ""; attempt++ {
		if pane, err = a.addAgentPane(ctx, doc, place, agentID); err == nil {
			break
		}
		if errors.Is(err, errNoWindow) {
			return
		}
		if attempt == 2 {
			a.logf("show agent %s in the Herdr app: %v", agentID, err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
		if doc, err = a.layoutDoc(ctx); err != nil {
			a.logf("read the Herdr app layout for agent %s: %v", agentID, err)
			return
		}
		pane = doc.agentPane(place.Tab, agentID)
	}
	if err := a.window(ctx, "tab.rename", map[string]any{"id": place.Tab, "name": place.TabName}); err != nil {
		a.logf("name tab %s in the Herdr app: %v", place.Tab, err)
	}
	if err := a.window(ctx, "space.rename", map[string]any{"id": place.Space, "name": place.SpaceName}); err != nil {
		a.logf("name space %s in the Herdr app: %v", place.Space, err)
	}
	if place.focus {
		if err := a.window(ctx, "tab.select", map[string]any{"id": place.Tab}); err != nil {
			a.logf("show tab %s in the Herdr app: %v", place.Tab, err)
		}
		if err := a.window(ctx, "pane.focus", map[string]any{"pane": pane}); err != nil {
			a.logf("focus the agent pane %s in the Herdr app: %v", pane, err)
		}
	}
}

// addAgentPane puts the agent mod in the tab of the agent and returns its
// pane. A tab that is gone comes back with the host terminal.
func (a Adapter) addAgentPane(ctx context.Context, doc layoutDoc, place placement, agentID string) (string, error) {
	props := map[string]any{"agent": agentID}
	if place.Harness != "" {
		props["harness"] = place.Harness
	}
	if tab := doc.tab(place.Tab); tab != nil {
		// The person closed the agent pane and kept other panes.
		if first := tab.Root.firstBlock(); first != "" {
			out, err := a.windowOutput(ctx, "pane.split", map[string]any{"pane": first, "direction": "right", "mod": AgentMod, "props": props})
			return out.Pane, err
		}
	}
	if _, err := a.hc(ctx, "label", place.Terminal, "herdr.space="+place.Space, "herdr.tab="+place.Tab); err != nil {
		return "", err
	}
	// The window puts the terminal in the tab after its next core listing.
	host := ""
	for host == "" {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("the window did not show the host terminal of agent %s", agentID)
		case <-time.After(100 * time.Millisecond):
		}
		next, err := a.layoutDoc(ctx)
		if err != nil {
			return "", err
		}
		if tab := next.tab(place.Tab); tab != nil {
			host = tab.Root.terminalBlock(place.Terminal)
			if pane := next.agentPane(place.Tab, agentID); pane != "" {
				// Another window put the agent pane there first.
				_ = a.window(ctx, "terminal.park", map[string]any{"terminal": place.Terminal})
				return pane, nil
			}
		}
	}
	out, err := a.windowOutput(ctx, "pane.split", map[string]any{"pane": host, "direction": "right", "mod": AgentMod, "props": props})
	if err != nil {
		return "", err
	}
	if err := a.window(ctx, "terminal.park", map[string]any{"terminal": place.Terminal}); err != nil {
		return out.Pane, err
	}
	return out.Pane, nil
}

// closeApp ends the app host of the agent and closes its agent pane. A tab
// with only that pane closes with it.
func (a Adapter) closeApp(ctx context.Context, agent model.Agent) {
	doc, err := a.layoutDoc(ctx)
	if err != nil {
		return
	}
	if pane := doc.agentPane(agentTabID(agent.ID), agent.ID); pane != "" {
		if err := a.window(ctx, "pane.close", map[string]any{"pane": pane}); err != nil && !errors.Is(err, errNoWindow) {
			a.logf("close the agent pane of %s in the Herdr app: %v", agent.ID, err)
		}
	}
}

type windowResult struct {
	Pane     string `json:"pane"`
	Tab      string `json:"tab"`
	Space    string `json:"space"`
	Terminal string `json:"terminal"`
}

// windowOutput runs one app window command and returns what it made.
func (a Adapter) windowOutput(ctx context.Context, name string, args map[string]any) (windowResult, error) {
	bin := a.App
	if bin == "" {
		bin = binary("herdr-app")
	}
	data, err := json.Marshal(args)
	if err != nil {
		return windowResult{}, err
	}
	command := exec.CommandContext(ctx, bin, "do", name, string(data))
	command.Env = environment("HC_TERMINAL_ID")
	out, err := command.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "no Herdr window") {
			return windowResult{}, errNoWindow
		}
		return windowResult{}, fmt.Errorf("herdr-app do %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	var result windowResult
	if err := json.Unmarshal(out, &result); err != nil {
		return windowResult{}, fmt.Errorf("herdr-app do %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return result, nil
}

// layoutDoc is the layout document of the app on this computer: its spaces,
// their tabs, and the panes of each tab. herdr-core keeps it, so it can be
// read without a window.
type layoutDoc struct {
	Spaces []struct {
		ID   string      `json:"id"`
		Tabs []layoutTab `json:"tabs"`
	} `json:"spaces"`
}

type layoutTab struct {
	ID   string     `json:"id"`
	Root layoutNode `json:"root"`
}

type layoutNode struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Children []layoutNode `json:"children"`
	Content  *struct {
		Terminal string         `json:"terminal"`
		Mod      string         `json:"mod"`
		Props    map[string]any `json:"props"`
	} `json:"content"`
}

func (d layoutDoc) tab(id string) *layoutTab {
	for _, space := range d.Spaces {
		for index := range space.Tabs {
			if space.Tabs[index].ID == id {
				return &space.Tabs[index]
			}
		}
	}
	return nil
}

// agentPane returns the pane of the agent mod of an agent in a tab, or "".
func (d layoutDoc) agentPane(tabID, agentID string) string {
	tab := d.tab(tabID)
	if tab == nil {
		return ""
	}
	return tab.Root.find(func(node layoutNode) bool {
		return node.Content != nil && node.Content.Mod == AgentMod && fmt.Sprint(node.Content.Props["agent"]) == agentID
	})
}

func (n layoutNode) find(match func(layoutNode) bool) string {
	if n.Type == "block" && match(n) {
		return n.ID
	}
	for _, child := range n.Children {
		if found := child.find(match); found != "" {
			return found
		}
	}
	return ""
}

func (n layoutNode) firstBlock() string {
	return n.find(func(node layoutNode) bool { return node.Content != nil })
}

func (n layoutNode) terminalBlock(address string) string {
	return n.find(func(node layoutNode) bool { return node.Content != nil && node.Content.Terminal == address })
}

// layoutDoc reads the layout document through the herdr-core API.
func (a Adapter) layoutDoc(ctx context.Context) (layoutDoc, error) {
	bin := a.HC
	if bin == "" {
		bin = binary("hc")
	}
	command := exec.CommandContext(ctx, bin, "connect")
	command.Env = environment("HC_TERMINAL_ID")
	stdin, err := command.StdinPipe()
	if err != nil {
		return layoutDoc{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return layoutDoc{}, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return layoutDoc{}, err
	}
	defer func() {
		_ = stdin.Close()
		_ = command.Wait()
	}()
	if _, err := io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"doc.get","params":{"name":"herdr-app"}}`+"\n"); err != nil {
		return layoutDoc{}, err
	}
	reader := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var response struct {
				ID     int `json:"id"`
				Result *struct {
					Value layoutDoc `json:"value"`
				} `json:"result"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(line, &response) == nil && response.ID == 1 {
				if response.Error != nil {
					return layoutDoc{}, fmt.Errorf("hc doc.get: %s", response.Error.Message)
				}
				if response.Result == nil {
					return layoutDoc{}, nil
				}
				return response.Result.Value, nil
			}
		}
		if err != nil {
			return layoutDoc{}, fmt.Errorf("hc connect: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}
}
