// The logic of the Galpon sidebar, without drawing. The sidebar gives it the
// live app data (spaces, panes, terminals, agents) and the Galpon facts from
// `galpon herdr-app sidebar`. `galpon herdr-app install` copies this file into
// each Galpon mod, so the popup shows the agent states of the sidebar.

// Galpon puts this label on each agent terminal that it starts in the app.
export const AGENT_LABEL = "galpon.agent";

// The app host of an agent in the app view has this label too: "app". Its
// terminal has no pane; the tab of the agent shows the agent mod.
export const VIEW_LABEL = "galpon.view";
export const AGENT_MOD = "galpon-agent";

// The tab that Galpon makes for an agent is "galpon-<agent ID>".
const AGENT_TAB = /^galpon-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/;

// MARKS is the one set of marks of the sidebar. A state has the same mark
// and the same tone on agent rows, on pull request rows and in counts, and
// the terminal command center uses the same marks. needs and ready are the
// turn of the person. Only the mark of a running agent moves: in the app it
// is a purple dot that fades out and in. A turn that the person did not
// look at is new: its row gets a band, a bold title and a filled mark.
// After the person looks, the turn is seen: an outline mark stays until the
// state changes. tone names a color of tones() in main.js, for the barn.
// words go into the tooltip.
export const MARKS = {
  needs: { mark: "!", tone: "attention", words: "Needs you", turn: true },
  ready: { mark: "✓", tone: "ready", words: "Ready for you", turn: true },
  running: { mark: "◐", icon: "mark-running", tone: "working", words: "Running" },
  delegating: { mark: "⊶", icon: "mark-delegating", tone: "working", words: "Delegating" },
  background: { mark: "◷", icon: "mark-background", tone: "working", words: "Waiting for background tasks" },
  agent: { mark: "×", tone: "error", words: "Agent's turn" },
  pending: { mark: "○", icon: "mark-pending", tone: "quiet", words: "Pending" },
  idle: { mark: "–", tone: "quiet", words: "Idle" },
  terminal: { mark: "$", tone: "quiet", words: "Terminal" },
};

// Agents sort by state: needs you first, then ready for you, then running,
// then idle.
export const RANK = { needs: 4, ready: 3, running: 2, delegating: 2, background: 2, pending: 1, idle: 1 };

// taskCount counts the background commands and monitors of a Pi agent.
export const taskCount = (tasks) => (tasks?.commands ?? 0) + (tasks?.monitors ?? 0);

// backgroundWords says what a Pi agent waits for, such as "Waiting for 1
// background command and 2 monitors".
export function backgroundWords(tasks) {
  const parts = [];
  const commands = tasks?.commands ?? 0;
  const monitors = tasks?.monitors ?? 0;
  if (commands > 0) parts.push(`${commands} background ${commands === 1 ? "command" : "commands"}`);
  if (monitors > 0) parts.push(`${monitors} ${monitors === 1 ? "monitor" : "monitors"}`);
  return parts.length ? `Waiting for ${parts.join(" and ")}` : MARKS.background.words;
}

// stateOf gives the sidebar state of an agent from its app status, its open
// delegated work and its background tasks. An agent whose turn ended waits,
// and is not the turn of the person, while its delegated work is open or its
// background commands and monitors run: they send it a notice later.
export function stateOf(status, delegating = 0, background = 0) {
  let state = "idle";
  if (status === "blocked") state = "needs";
  else if (status === "working") state = "running";
  else if (delegating > 0) state = "delegating";
  else if (background > 0) state = "background";
  else if (status === "done") state = "ready";
  else if (!status) state = "pending";
  return { state, ...MARKS[state] };
}

// lookAt keeps in looked the turn state that the person looked at, for each
// row key. A row with the focus is looked at. A row that leaves its turn, or
// that is gone, is forgotten, so that its next turn is new again. An empty
// list forgets nothing, because the app can show no rows for a moment while
// it starts. lookAt gives the keys of the turns that are new.
export function lookAt(rows, looked) {
  const unseen = new Set();
  const present = new Set();
  for (const row of rows) {
    present.add(row.key);
    if (!MARKS[row.state]?.turn) {
      looked.delete(row.key);
      continue;
    }
    if (row.focused) looked.set(row.key, row.state);
    if (looked.get(row.key) !== row.state) unseen.add(row.key);
  }
  if (rows.length > 0) {
    for (const key of [...looked.keys()]) {
      if (!present.has(key)) looked.delete(key);
    }
  }
  return unseen;
}

// freshOf tells, for each turn state, whether a row in that state is new.
export function freshOf(rows, unseen) {
  const fresh = { needs: false, ready: false };
  for (const row of rows) {
    if (unseen.has(row.key) && row.state in fresh) fresh[row.state] = true;
  }
  return fresh;
}

// turnOrder gives the agent turns in the order that the jump goes through
// them: the agents that need you, then the agents that are ready for you,
// and in each state the one that waited longest first. changedAt is null
// for a state from before the app started, which is the oldest.
export function turnOrder(rows) {
  return rows.filter((row) => row.kind === "agent" && MARKS[row.state]?.turn && row.pane)
    .sort((a, b) => RANK[b.state] - RANK[a.state] || (a.changedAt ?? 0) - (b.changedAt ?? 0) || a.tabIndex - b.tabIndex);
}

// nextTurn gives the turn to go to: the turn after the one that has the
// focus, else the first turn. It is null when there is no turn.
export function nextTurn(turns, focusedKey) {
  if (turns.length === 0) return null;
  const at = turns.findIndex((turn) => turn.key === focusedKey);
  return turns[(at + 1) % turns.length];
}

// commandName gives the name of a key command for this mod: "next-turn"
// from "next-turn", "galpon-sidebar.next-turn" or "galpon-sidebar:next-turn".
export function commandName(raw) {
  return String(raw ?? "").split(/[.:]/).pop();
}

// hostId gives the ID that Galpon uses for an app ID of a space or a tab. The
// app shows the IDs of another computer as "<machine>/<id>". host is the
// machine that runs Galpon, or null for this computer. The result is null for
// an ID that is not on the computer that runs Galpon.
export function hostId(id, host = null) {
  if (!id) return null;
  if (!host) return id.includes("/") ? null : id;
  return id.startsWith(`${host}/`) ? id.slice(host.length + 1) : null;
}

// galponAgentOf gives the Galpon agent ID of a tab: from the label on one of
// its terminals, else from the ID that Galpon gave the tab. The tab of an
// agent in the app view holds the agent mod, and its hidden app host gives
// the state.
function galponAgentOf(tab, tabPanes, terminalByAddress, agentInfo, host, hosts) {
  for (const pane of tabPanes) {
    const id = terminalByAddress.get(pane.terminal)?.labels?.[AGENT_LABEL];
    if (id && terminalByAddress.get(pane.terminal)?.labels?.[VIEW_LABEL] !== "app") return { id, pane };
  }
  const match = AGENT_TAB.exec(hostId(tab.id, host) ?? "");
  if (!match) return null;
  const view = tabPanes.find((pane) => pane.mod === AGENT_MOD);
  if (view) return { id: match[1], pane: view, app: true, host: hosts.get(match[1]) ?? null };
  if (agentInfo) return { id: match[1], pane: tabPanes.find((pane) => pane.pane === agentInfo.pane) };
  return null;
}

// appHosts maps each agent in the app view to the live terminal of its app
// host. On a screen, only the hosts of the Galpon host count.
export function appHosts(terminals, host = null) {
  const out = new Map();
  for (const terminal of terminals) {
    const labels = terminal.labels ?? {};
    if (labels[VIEW_LABEL] !== "app" || !labels[AGENT_LABEL] || terminal.state === "finished") continue;
    if (hostId(terminal.address, host) === null) continue;
    out.set(labels[AGENT_LABEL], terminal);
  }
  return out;
}

// statusWord gives the app status of a terminal of terminals(): "working",
// "blocked", "done", "idle", or null.
export function statusWord(terminal) {
  const status = terminal?.status;
  if (typeof status === "string") return status;
  return status?.state ?? null;
}

// strayHosts gives the app hosts that the window shows as a terminal pane.
// Galpon starts a host with the labels of its tab when no window is open,
// so that the window shows the tab when it opens; the sidebar then puts the
// agent mod in the tab and parks the host. pane is the pane of the host,
// view the pane of the agent mod in that tab, if there is one.
export function strayHosts(panes, terminals, host = null) {
  const byAddress = new Map(terminals.map((terminal) => [terminal.address, terminal]));
  const out = [];
  for (const pane of panes) {
    const terminal = byAddress.get(pane.terminal);
    const labels = terminal?.labels ?? {};
    if (labels[VIEW_LABEL] !== "app" || !labels[AGENT_LABEL] || hostId(pane.terminal, host) === null) continue;
    const view = panes.find((other) => other.tab === pane.tab && other.mod === AGENT_MOD);
    out.push({ address: pane.terminal, agentId: labels[AGENT_LABEL], pane: pane.pane, tab: pane.tab, view: view?.pane ?? null });
  }
  return out;
}

// openAgentPanes maps each Galpon agent that has a terminal in the app to the
// pane that shows it.
export function openAgentPanes(panes, terminals) {
  const terminalByAddress = new Map(terminals.map((terminal) => [terminal.address, terminal]));
  const out = new Map();
  for (const pane of panes) {
    const id = terminalByAddress.get(pane.terminal)?.labels?.[AGENT_LABEL];
    if (id && !out.has(id)) out.set(id, pane.pane);
  }
  return out;
}

// buildWorkspaces makes one entry for each app space, in the app order, with
// a row for each of its tabs, in tab order. facts are the Galpon facts, or
// null before the first read. host is the machine that runs Galpon, or null
// for this computer.
export function buildWorkspaces({ spaces, panes, terminals, agents, focused, facts, host = null }) {
  const terminalByAddress = new Map(terminals.map((terminal) => [terminal.address, terminal]));
  const hosts = appHosts(terminals, host);
  const panesByTab = new Map();
  for (const pane of panes) {
    if (!panesByTab.has(pane.tab)) panesByTab.set(pane.tab, []);
    panesByTab.get(pane.tab).push(pane);
  }
  const agentsByTab = new Map();
  for (const agent of agents) {
    if (!agentsByTab.has(agent.tab)) agentsByTab.set(agent.tab, []);
    agentsByTab.get(agent.tab).push(agent);
  }
  return spaces.map((space) => {
    const spaceId = hostId(space.id, host);
    const workspaceId = spaceId?.startsWith("galpon-") ? spaceId.slice("galpon-".length) : null;
    const rows = space.tabs.map((tab) => {
      const tabPanes = panesByTab.get(tab.id) ?? [];
      const tabAgents = agentsByTab.get(tab.id) ?? [];
      const firstPane = tabPanes.find((pane) => pane.focused) ?? tabPanes.find((pane) => pane.terminal) ?? tabPanes[0];
      const row = {
        key: tab.id, tabId: tab.id, tabName: tab.name, tabIndex: tab.index, focused: focused?.tab === tab.id,
        pane: firstPane?.pane ?? null, workingSince: tab.working_since ?? null, changedAt: tab.changed_at ?? null,
      };
      const galpon = galponAgentOf(tab, tabPanes, terminalByAddress, tabAgents[0], host, hosts);
      if (galpon?.app) {
        // An agent in the app view: the state comes from its app host.
        const known = facts?.agents?.[galpon.id];
        const status = galpon.host ? statusWord(galpon.host) : null;
        // herdr-core tells since when the host reports its state.
        const since = Number(galpon.host?.status_ms) || null;
        return {
          ...row, workingSince: status === "working" ? since : null, changedAt: since ?? row.changedAt,
          kind: "agent", agentId: galpon.id, address: null, host: galpon.host?.address ?? null, status, app: "pi", appView: true,
          // herdr-core counts the times that the state left working.
          turn: Number(galpon.host?.turn) || 0,
          delegating: known?.delegating ?? 0, background: known?.background ?? null, cost: known?.cost ?? null,
          harness: known?.harness ?? "pi", view: "app", pane: galpon.pane.pane,
          title: known?.title || (tab.named ? tab.name : "Pi"),
          ...(galpon.host ? stateOf(status, known?.delegating ?? 0, taskCount(known?.background)) : { state: "idle", ...MARKS.idle }),
        };
      }
      if (galpon) {
        const info = tabAgents.find((agent) => agent.pane === galpon.pane?.pane) ?? tabAgents[0];
        const known = facts?.agents?.[galpon.id];
        const fallback = tab.named ? tab.name : terminalByAddress.get(galpon.pane?.terminal)?.name || info?.title || tab.name;
        return {
          ...row, kind: "agent", agentId: galpon.id, address: info?.address ?? null, status: info?.status ?? null,
          delegating: known?.delegating ?? 0, background: known?.background ?? null, cost: known?.cost ?? null,
          harness: known?.harness ?? null, view: known?.view ?? "",
          pane: info?.pane ?? galpon.pane?.pane ?? row.pane, app: info?.app ?? null,
          title: known?.title || fallback, ...stateOf(info?.status, known?.delegating ?? 0, taskCount(known?.background)),
        };
      }
      if (tabAgents.length > 0) {
        const info = tabAgents[0];
        return {
          ...row, kind: "agent", agentId: null, address: info.address, status: info.status, pane: info.pane, app: info.app,
          title: tab.named ? tab.name : info.title || info.program?.name || tab.name, ...stateOf(info.status),
        };
      }
      const named = tab.named ? tab.name : firstPane?.title || firstPane?.program?.name || `Tab ${tab.index}`;
      return { ...row, kind: "terminal", agentId: null, address: null, status: null, title: named, state: "terminal", ...MARKS.terminal };
    });
    const title = (workspaceId && facts?.workspaces?.[workspaceId]) || space.name;
    return { id: space.id, workspaceId, title, active: space.active, rows };
  });
}

// orderRows puts the agents first, the most urgent state first and in tab
// order inside a state, and then the other tabs in tab order. rankOf gives the
// rank of an agent row.
export function orderRows(rows, rankOf = (row) => RANK[row.state] ?? 0) {
  const agents = rows.filter((row) => row.kind === "agent")
    .sort((a, b) => rankOf(b) - rankOf(a) || a.tabIndex - b.tabIndex);
  return [...agents, ...rows.filter((row) => row.kind !== "agent")];
}

// keepOrder keeps the rows in a previous order of their keys. New rows go at
// the end, and rows that are gone drop out.
export function keepOrder(rows, keys) {
  if (!keys) return rows;
  const position = new Map(keys.map((key, index) => [key, index]));
  const known = rows.filter((row) => position.has(row.key)).sort((a, b) => position.get(a.key) - position.get(b.key));
  return [...known, ...rows.filter((row) => !position.has(row.key))];
}

// summary counts rows for a header: needs you, ready for you, and running.
// An agent that delegates or waits for background tasks runs too. Pull
// requests count the same way.
export function summary(rows) {
  const counts = { needs: 0, ready: 0, running: 0 };
  for (const row of rows) {
    if (row.state === "needs" || row.state === "ready") counts[row.state]++;
    if (row.state === "running" || row.state === "delegating" || row.state === "background") counts.running++;
  }
  return counts;
}

// pullRequestSections groups the pull requests by their command center group,
// in the order that Galpon gives them.
export function pullRequestSections(facts) {
  const titles = facts?.pullRequestGroups ?? [];
  const sections = [];
  for (const item of facts?.pullRequests ?? []) {
    const group = item.group ?? 0;
    let section = sections.at(-1);
    if (!section || section.group !== group) {
      section = { group, title: titles[group] ?? "", items: [] };
      sections.push(section);
    }
    section.items.push(item);
  }
  return sections;
}

// rowWords says what a row of an agent or a tab shows, for its tooltip.
export function rowWords(row, now) {
  if (row.kind === "terminal") return row.title;
  if (row.state === "running" && row.workingSince) return `${row.title} · Running · ${duration(now - row.workingSince)}`;
  if (row.state === "delegating") return `${row.title} · ${row.delegating} delegated ${row.delegating === 1 ? "agent works" : "agents work"}`;
  if (row.state === "background") return `${row.title} · ${backgroundWords(row.background)}`;
  if (row.state === "pending") return `${row.title} · No status yet`;
  return `${row.title} · ${row.words}`;
}

// duration is a short age, such as "4m" or "2d".
export function duration(ms) {
  const minutes = Math.floor(ms / 60000);
  if (minutes < 1) return "now";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours}h` : `${Math.floor(hours / 24)}d`;
}

// signature changes when an open Galpon agent opens, closes, changes its
// app status or gets a new tab name, for example after a rename. The sidebar
// then reads the Galpon facts again.
export function signature(workspaces) {
  const parts = [];
  for (const workspace of workspaces) {
    for (const row of workspace.rows) {
      if (row.agentId) parts.push(`${row.agentId}:${row.status}:${row.tabName ?? ""}`);
    }
  }
  return parts.sort().join(",");
}

// menuItems gives the right-click menu of a sidebar row. Each item runs one
// of these:
//   galpon   an action of the command center, which opens on that action
//   app      an app command with its arguments
//   request  work for the sidebar block that owns the row
// A disabled item says why. Close is off while an agent works or waits for
// you in its terminal, because Galpon would open it again to finish its turn.
// Close stops the background tasks of a Pi agent, so its hint says so.
export function menuItems(target) {
  switch (target?.type) {
    case "agent": {
      let off = "";
      if (target.status === "working") off = "It is working. Close it when it is idle.";
      if (target.status === "blocked") off = target.view === "app" ? "It waits for you in its tab." : "It waits for you in its terminal.";
      let hint = "";
      if (!off && target.delegating > 0) hint = "Opens again for its delegated work";
      if (!off && target.background > 0) hint = "This stops its background tasks";
      // A Pi or Claude Code agent moves between a tab of the app and a
      // terminal. Its process restarts, so it moves only while it is idle.
      const harness = target.harness ?? "pi";
      const pi = harness === "pi" || harness === "claude";
      const name = harness === "claude" ? "Claude Code" : "Pi";
      const view = target.view === "app"
        ? { id: "view", label: "Open in a terminal", galpon: "switch-view", off, hint: off ? "" : `${name} restarts in a terminal with the same conversation` }
        : { id: "view", label: "Open in a tab", galpon: "switch-view", off, hint: off ? "" : `${name} restarts in a tab of the app with the same conversation` };
      return [
        { id: "message", label: "Message…", galpon: "message" },
        { id: "terminal", label: "Terminal", galpon: "terminal" },
        { id: "editor", label: "Editor", galpon: "editor" },
        { id: "fork", label: "Fork…", galpon: "fork-agent" },
        { id: "rename", label: "Rename…", galpon: "rename" },
        ...(pi ? [view] : []),
        { separator: true },
        { id: "close", label: "Close", request: "close", off, hint },
        { id: "hide", label: "Hide…", galpon: "hide" },
      ];
    }
    case "tab":
      return [
        { id: "rename", label: "Rename", app: "tab.rename", args: { id: target.tabId } },
        { id: "close", label: "Close", app: "tab.close", args: { id: target.tabId } },
      ];
    case "workspace":
      return [
        { id: "new-agent", label: "New agent here…", galpon: "new-agent-here" },
        { id: "terminal", label: "New terminal", galpon: "terminal" },
      ];
    case "space":
      return [
        { id: "rename", label: "Rename", app: "space.rename", args: { id: target.spaceId } },
        { id: "close", label: "Close", app: "space.close", args: { id: target.spaceId } },
      ];
    case "pull-request":
      return [
        ...(target.agentId ? [{ id: "open", label: `Open ${target.agentTitle || "the agent"}`, request: "open" }] : []),
        { id: "github", label: "Open on GitHub", request: "browse" },
        { separator: true },
        { id: "refresh", label: "Refresh pull requests", request: "refresh" },
      ];
  }
  return [];
}

// menuTarget describes a sidebar row for its menu. It holds only what the
// menu needs, because it goes through the popup props.
export function menuTarget(row) {
  if (row.kind === "agent" && row.agentId) {
    return {
      type: "agent", kind: "agent", id: row.agentId, key: row.key, title: row.title,
      status: row.status, delegating: row.delegating ?? 0, background: taskCount(row.background),
      harness: row.harness ?? null, view: row.view ?? "",
    };
  }
  return { type: "tab", key: row.key, tabId: row.tabId, title: row.title };
}

// The first item that the keyboard can select from index, in direction step.
export function nextItem(items, index, step) {
  for (let count = 0, next = index; count < items.length; count++) {
    next = (next + step + items.length) % items.length;
    if (!items[next].separator && !items[next].off) return next;
  }
  return index;
}
