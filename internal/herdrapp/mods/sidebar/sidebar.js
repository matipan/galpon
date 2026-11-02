// The logic of the Galpon sidebar, without drawing. main.js gives it the live
// app data (spaces, panes, terminals, agents) and the Galpon facts from
// `galpon herdr-app sidebar`.

// Galpon puts this label on each agent terminal that it starts in the app.
export const AGENT_LABEL = "galpon.agent";

// The tab that Galpon makes for an agent is "galpon-<agent ID>".
const AGENT_TAB = /^galpon-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/;

// Agents sort by state: waiting for you first, then running, then idle.
export const RANK = { waiting: 3, running: 2, idle: 1 };

// stateOf gives the sidebar state of an agent from its app status and its
// open delegated work. An agent that waits for its delegated work runs, even
// when its own turn ended. tone names the theme color of the mark.
export function stateOf(status, delegating = 0) {
  if (status === "blocked") return { state: "waiting", mark: "!", tone: "blocked", label: "blocked" };
  if (status === "working") return { state: "running", mark: "", tone: "working", label: "working" };
  if (delegating > 0) return { state: "running", mark: "⊶", tone: "working", label: `delegating · ${delegating}` };
  if (status === "done") return { state: "waiting", mark: "✓", tone: "done", label: "done · not seen" };
  if (!status) return { state: "idle", mark: "○", tone: "idle", label: "no status" };
  return { state: "idle", mark: "–", tone: "idle", label: "idle" };
}

// galponAgentOf gives the Galpon agent ID of a tab: from the label on one of
// its terminals, else from the ID that Galpon gave the tab.
function galponAgentOf(tab, tabPanes, terminalByAddress, agentInfo) {
  for (const pane of tabPanes) {
    const id = terminalByAddress.get(pane.terminal)?.labels?.[AGENT_LABEL];
    if (id) return { id, pane };
  }
  const match = AGENT_TAB.exec(tab.id);
  if (match && agentInfo) return { id: match[1], pane: tabPanes.find((pane) => pane.pane === agentInfo.pane) };
  return null;
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
// null before the first read.
export function buildWorkspaces({ spaces, panes, terminals, agents, focused, facts }) {
  const terminalByAddress = new Map(terminals.map((terminal) => [terminal.address, terminal]));
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
    const workspaceId = space.id.startsWith("galpon-") ? space.id.slice("galpon-".length) : null;
    const rows = space.tabs.map((tab) => {
      const tabPanes = panesByTab.get(tab.id) ?? [];
      const tabAgents = agentsByTab.get(tab.id) ?? [];
      const firstPane = tabPanes.find((pane) => pane.focused) ?? tabPanes.find((pane) => pane.terminal) ?? tabPanes[0];
      const row = {
        key: tab.id, tabId: tab.id, tabIndex: tab.index, focused: focused?.tab === tab.id,
        pane: firstPane?.pane ?? null, workingSince: tab.working_since ?? null,
      };
      const galpon = galponAgentOf(tab, tabPanes, terminalByAddress, tabAgents[0]);
      if (galpon) {
        const info = tabAgents.find((agent) => agent.pane === galpon.pane?.pane) ?? tabAgents[0];
        const known = facts?.agents?.[galpon.id];
        const fallback = tab.named ? tab.name : terminalByAddress.get(galpon.pane?.terminal)?.name || info?.title || tab.name;
        return {
          ...row, kind: "agent", agentId: galpon.id, address: info?.address ?? null, status: info?.status ?? null,
          delegating: known?.delegating ?? 0,
          pane: info?.pane ?? galpon.pane?.pane ?? row.pane, app: info?.app ?? null,
          title: known?.title || fallback, ...stateOf(info?.status, known?.delegating ?? 0),
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
      return { ...row, kind: "terminal", agentId: null, address: null, status: null, title: named, state: "", mark: "$", tone: "dim", label: "" };
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

// summary counts the agents of a workspace that wait for you or run.
export function summary(rows) {
  let waiting = 0;
  let running = 0;
  for (const row of rows) {
    if (row.state === "waiting") waiting++;
    if (row.state === "running") running++;
  }
  if (waiting > 0) return { text: `${waiting} waiting`, tone: "blocked", waiting, running };
  if (running > 0) return { text: `${running} running`, tone: "working", waiting, running };
  return { text: "", tone: "dim", waiting, running };
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

// duration is a short age, such as "4m" or "2d".
export function duration(ms) {
  const minutes = Math.floor(ms / 60000);
  if (minutes < 1) return "now";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours}h` : `${Math.floor(hours / 24)}d`;
}

// signature changes when an open Galpon agent opens, closes or changes its
// app status. The sidebar then reads the Galpon facts again.
export function signature(workspaces) {
  const parts = [];
  for (const workspace of workspaces) {
    for (const row of workspace.rows) {
      if (row.agentId) parts.push(`${row.agentId}:${row.status}`);
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
export function menuItems(target) {
  switch (target?.type) {
    case "agent": {
      let off = "";
      if (target.status === "working") off = "It is working. Close it when it is idle.";
      if (target.status === "blocked") off = "It waits for you in its terminal.";
      const hint = !off && target.delegating > 0 ? "Opens again for its delegated work" : "";
      return [
        { id: "message", label: "Message…", galpon: "message" },
        { id: "operations", label: "Operations", galpon: "operations" },
        { id: "terminal", label: "Terminal", galpon: "terminal" },
        { id: "editor", label: "Editor", galpon: "editor" },
        { id: "fork", label: "Fork…", galpon: "fork-agent" },
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
      status: row.status, delegating: row.delegating ?? 0,
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
