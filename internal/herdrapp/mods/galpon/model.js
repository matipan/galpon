// The rows of the Galpon popup. These functions do not use the app or GPUI,
// so they also run outside the app.
//
// The popup has one list and no tabs. With an empty search, it shows the
// items that need the person, then the recent agents, then the ways to
// browse all pull requests and all workspaces. A search finds agents first,
// then pull requests, workspaces and commands, and last worktrees and
// repositories. Its last row starts a new agent with the search text. A
// scope shows one workspace, all pull requests or all workspaces.
//
// Rows keep their places while the popup is open: a row that changes its
// state keeps its group and its place, and only its mark changes.

export function normalize(text) {
  return String(text ?? "").toLowerCase().split(/\s+/).filter(Boolean).join(" ");
}

const wordCharacter = /[\p{L}\p{N}]/u;

// fuzzyScore ranks a title for a query. It returns -1 when the title does not
// match. An exact title is first, then a prefix, a word start, other text and
// letters in order.
export function fuzzyScore(title, query) {
  title = normalize(title);
  query = normalize(query);
  if (!query) return 0;
  const length = Array.from(title).length;
  if (title === query) return 1_000_000 - length;
  if (title.startsWith(query)) return 900_000 - length;
  let best = -1;
  for (let index = title.indexOf(query); index >= 0; index = title.indexOf(query, index + 1)) {
    const before = Array.from(title.slice(0, index));
    const tier = index === 0 || !wordCharacter.test(before[before.length - 1]) ? 800_000 : 700_000;
    best = Math.max(best, tier - before.length * 100 - length);
  }
  if (best >= 0) return best;
  const runes = Array.from(title);
  const needle = Array.from(query);
  let at = 0;
  let first = -1;
  let last = -1;
  let wordStarts = 0;
  for (let index = 0; index < runes.length && at < needle.length; index++) {
    if (runes[index] !== needle[at]) continue;
    if (first < 0) first = index;
    if (index === 0 || !wordCharacter.test(runes[index - 1])) wordStarts++;
    last = index;
    at++;
  }
  if (at !== needle.length) return -1;
  const gaps = last - first + 1 - needle.length;
  return 500_000 + wordStarts * 1_000 - gaps * 100 - first * 10 - runes.length;
}

export function itemKey(item) {
  return `${item.kind}:${item.id}`;
}

const lower = (text) => String(text ?? "").toLowerCase();

function compareText(a, b) {
  return a < b ? -1 : a > b ? 1 : 0;
}

// Most recent activity first.
export function agentOrder(a, b) {
  return (b.activityAt ?? 0) - (a.activityAt ?? 0)
    || compareText(lower(a.title), lower(b.title))
    || compareText(lower(a.workspaceTitle), lower(b.workspaceTitle))
    || compareText(a.id, b.id);
}

// Agents that another agent created show below it when it is expanded.
export function nestDelegated(data, state, rows) {
  const children = new Map();
  for (const item of data.items) {
    if (item.kind !== "agent" || !item.parentAgentId) continue;
    if (!children.has(item.parentAgentId)) children.set(item.parentAgentId, []);
    children.get(item.parentAgentId).push(item);
  }
  const nested = new Set();
  const collect = (id, path) => {
    if (!state.expanded.has(`agent:${id}`) || path.has(id)) return;
    path.add(id);
    for (const child of children.get(id) ?? []) {
      if (path.has(child.id)) continue;
      nested.add(child.id);
      collect(child.id, path);
    }
    path.delete(id);
  };
  for (const row of rows) if (row.type === "item") collect(row.item.id, new Set());
  const out = [];
  const append = (item, depth, path) => {
    out.push({ type: "item", key: itemKey(item), item, depth });
    if (!state.expanded.has(`agent:${item.id}`) || path.has(item.id)) return;
    path.add(item.id);
    for (const child of children.get(item.id) ?? []) if (!path.has(child.id)) append(child, depth + 1, path);
    path.delete(item.id);
  };
  for (const row of rows) {
    if (row.type !== "item") out.push(row);
    else if (!nested.has(row.item.id)) append(row.item, 0, new Set());
  }
  return out;
}

// Tree guides, one per level: "├" for a child with a later sibling, "└" for
// the last child, "│" while an outer sibling follows, " " for nothing.
export function withGuides(rows) {
  const next = new Map();
  for (let index = rows.length - 1; index >= 0; index--) {
    const depth = rows[index].depth ?? 0;
    for (const level of [...next.keys()]) if (level > depth) next.delete(level);
    const guide = [];
    for (let level = 1; level <= Math.min(depth, 5); level++) {
      if (level === depth) guide.push(next.get(level) ? "├" : "└");
      else guide.push(next.get(level) ? "│" : " ");
    }
    rows[index].guide = guide;
    next.set(depth, true);
  }
  return rows;
}

// pullRequestAgent is the visible agent of a pull request, or null.
export function pullRequestAgent(data, pull) {
  if (!pull?.agentId) return null;
  return data.items.find((item) => item.kind === "agent" && item.id === pull.agentId) ?? null;
}

// keep orders items by sort the first time it sees them, and keeps that
// order while the popup is open: a new item goes after the known ones.
function keep(items, order, sort) {
  for (const item of [...items].sort(sort)) {
    const key = itemKey(item);
    if (!order.has(key)) order.set(key, order.size);
  }
  return [...items].sort((a, b) => order.get(itemKey(a)) - order.get(itemKey(b)));
}

// Each search group, in the order of the list, with the number of results
// that show before a "More" row.
export const SEARCH_GROUPS = [
  { kind: "agent", title: "Agents", limit: 8 },
  { kind: "pull-request", title: "Pull requests", limit: 5 },
  { kind: "workspace", title: "Workspaces", limit: 5 },
  { kind: "command", title: "Commands", limit: 4 },
  { kind: "worktree", title: "Worktrees", limit: 3 },
  { kind: "repository", title: "Repositories", limit: 3 },
];

// Commands that a search finds by their names.
export const COMMANDS = [
  { id: "new-agent", title: "New agent", detail: "Describe a task, and an agent starts on it", keys: "ctrl-n" },
  { id: "pull-requests", title: "Pull requests", detail: "All open pull requests", keys: "alt-p" },
  { id: "workspaces", title: "Workspaces", detail: "All workspaces and their agents" },
  { id: "new-workspace", title: "New workspace", detail: "Group related agent and human work" },
  { id: "new-repository", title: "Add repository", detail: "A local path or a Git URL" },
  { id: "new-remote", title: "Add remote", detail: "A named source for a repository" },
  { id: "hidden", title: "Show hidden items", detail: "Hidden agents, workspaces, worktrees and repositories", hiddenTitle: "Hide hidden items" },
  { id: "refresh", title: "Refresh pull requests", detail: "Read the pull requests from GitHub again", keys: "ctrl-r" },
];

// A command shows only for a search that its name contains: letters in order
// would find a command for almost any search.
const COMMAND_SCORE = 700_000;

// The live state of each agent, from the app: needs, ready, running,
// delegating, background, pending or idle. fresh tells that the person did
// not look at a turn yet. An agent without an open tab has no live state.
//
// agentState gives the state of an agent for the list. Galpon knows only
// that an agent works or failed.
export function agentState(item, live) {
  if (item.state === "failed") return { state: "failed", fresh: true };
  const known = live?.get(item.id);
  if (known) return known;
  if (item.state === "working") return { state: "running", fresh: false };
  return { state: "idle", fresh: false };
}

// needsYou tells if an agent waits for the person: it asks something, it
// failed, or it finished and the person did not look at its reply yet.
export function needsYou(state) {
  return state.state === "needs" || state.state === "failed" || (state.state === "ready" && state.fresh);
}

const NEED_RANK = { needs: 0, failed: 1, ready: 2 };

function itemRow(item, extra = {}) {
  return { type: "item", key: itemKey(item), item, depth: 0, ...extra };
}

function header(key, title) {
  return { type: "header", key: `heading-${key}`, title };
}

// groupOf keeps the group of an item while the popup is open.
function groupOf(state, key, group) {
  if (!state.groups.has(key)) state.groups.set(key, group);
  return state.groups.get(key);
}

function homeRows(data, state, live) {
  const rows = [];
  const agents = data.items.filter((item) => item.kind === "agent");
  const needs = [];
  const recent = [];
  for (const item of agents) {
    const current = agentState(item, live);
    // A delegated agent shows here only when it needs the person.
    const group = groupOf(state, itemKey(item), needsYou(current) ? "needs" : "recent");
    if (group === "needs") needs.push(item);
    else if (!item.delegated) recent.push(item);
  }
  const pulls = data.items.filter((item) => item.kind === "pull-request" && (item.group ?? 0) === 0
    && groupOf(state, itemKey(item), "needs") === "needs");
  const needRank = (item) => {
    if (item.kind === "pull-request") return 3;
    return NEED_RANK[agentState(item, live).state] ?? 2;
  };
  // The agent that waited longest comes first.
  const since = (item) => live?.get(item.id)?.changedAt ?? item.activityAt ?? 0;
  const urgent = keep([...needs, ...pulls], state.order, (a, b) => needRank(a) - needRank(b) || since(a) - since(b));
  if (urgent.length) {
    rows.push(header("needs", "Needs you"));
    rows.push(...urgent.map((item) => itemRow(item, { group: "needs" })));
  }
  const cutoff = data.now - data.olderAfter;
  const fresh = [];
  const older = [];
  for (const item of keep(recent, state.order, agentOrder)) {
    const quiet = item.activityAt > 0 && item.activityAt < cutoff && agentState(item, live).state === "idle";
    (quiet ? older : fresh).push(item);
  }
  if (fresh.length || older.length) rows.push(header("recent", "Recent"));
  rows.push(...nestDelegated(data, state, fresh.map((item) => itemRow(item, { group: "recent" }))));
  if (older.length) {
    const key = "older-agent";
    const open = state.expanded.has(key);
    rows.push({ type: "more", key, kind: "agent", open, title: `Older agents (${older.length})`, detail: open ? "Tab to fold" : "Tab to show" });
    if (open) rows.push(...nestDelegated(data, state, older.map((item) => itemRow(item, { group: "recent" }))));
  }
  const pullCount = data.items.filter((item) => item.kind === "pull-request").length;
  const workspaceCount = data.items.filter((item) => item.kind === "workspace").length;
  rows.push(header("browse", "Browse"));
  const attention = data.pullRequests?.attention ?? 0;
  rows.push({
    type: "scope", key: "scope-pull-requests", scope: { kind: "pull-requests", title: "Pull requests" }, title: "Pull requests",
    detail: `${pullCount} open${attention ? ` · ${attention} need you` : ""}`, icon: "git-compare",
  });
  rows.push({ type: "scope", key: "scope-workspaces", scope: { kind: "workspaces", title: "Workspaces" }, title: "Workspaces", detail: `${workspaceCount}`, icon: "layers" });
  return rows;
}

function searchText(item) {
  if (item.kind === "pull-request") return `${item.ref ?? ""} ${item.title}`;
  return item.search || item.title;
}

// matches gives the items of a kind that match the query, the best first.
function matches(items, query, order, kind) {
  const found = [];
  for (const item of items) {
    const score = fuzzyScore(searchText(item), query);
    if (score >= (kind === "command" ? COMMAND_SCORE : 0)) found.push({ item, score });
  }
  found.sort((a, b) => b.score - a.score || (kind === "agent" ? agentOrder(a.item, b.item) : 0));
  return keep(found.map((match) => match.item), order, () => 0);
}

function commandItems(state) {
  return COMMANDS.map((command) => ({
    kind: "command", id: command.id, title: state.hidden && command.hiddenTitle ? command.hiddenTitle : command.title,
    detail: command.detail, keys: command.keys ?? "",
  }));
}

function searchRows(data, state, query) {
  const rows = [];
  for (const group of SEARCH_GROUPS) {
    const items = group.kind === "command" ? commandItems(state) : data.items.filter((item) => item.kind === group.kind);
    const found = matches(items, query, state.order, group.kind);
    if (!found.length) continue;
    rows.push(header(group.kind, group.title));
    const key = `search-${group.kind}`;
    const open = state.expanded.has(key);
    const shown = open ? found : found.slice(0, group.limit);
    rows.push(...shown.map((item) => itemRow(item)));
    if (found.length > group.limit) {
      rows.push({ type: "more", key, kind: group.kind, open, title: open ? `Fewer ${group.title.toLowerCase()}` : `${found.length - group.limit} more ${group.title.toLowerCase()}`, detail: open ? "Tab to fold" : "Tab to show" });
    }
  }
  rows.push({ type: "launch", key: "launch", text: state.query.trim() });
  return rows;
}

// A workspace shows all its agents, the most recent first.
function workspaceRows(data, state, query, scope) {
  let agents = data.items.filter((item) => item.kind === "agent" && item.workspaceId === scope.id);
  agents = query ? matches(agents, query, state.order, "agent") : keep(agents, state.order, agentOrder);
  const rows = agents.map((item) => itemRow(item));
  if (!rows.length) rows.push({ type: "empty", key: "empty", title: query ? "No agent of this workspace matches." : "This workspace has no agents." });
  rows.push({ type: "launch", key: "launch", text: state.query.trim(), workspaceId: scope.id, workspaceTitle: scope.title });
  return rows;
}

function pullRequestRows(data, state, query) {
  let pulls = data.items.filter((item) => item.kind === "pull-request");
  if (query) {
    const found = new Set(matches(pulls, query, state.order, "pull-request").map(itemKey));
    pulls = pulls.filter((item) => found.has(itemKey(item)));
  }
  const rows = [];
  let last = -1;
  for (const pull of pulls) {
    const group = pull.group ?? 0;
    if (group !== last) {
      rows.push(header(`pull-${group}`, data.pullRequests?.groups?.[group] ?? ""));
      last = group;
    }
    rows.push(itemRow(pull));
  }
  if (!rows.length) rows.push({ type: "empty", key: "empty", title: query ? "No pull request matches." : data.pullRequests?.empty || "No open pull requests." });
  return rows;
}

function workspacesRows(data, state, query) {
  let workspaces = data.items.filter((item) => item.kind === "workspace");
  if (query) workspaces = matches(workspaces, query, state.order, "workspace");
  const rows = workspaces.map((item) => itemRow(item));
  if (!rows.length) rows.push({ type: "empty", key: "empty", title: "No workspace matches." });
  return rows;
}

// buildRows gives the rows of the popup. state holds the query, the scope,
// the expanded keys, the stable order, the kept groups and whether hidden
// items show; buildRows adds new rows to the order and the groups. live maps
// an agent ID to its live state.
export function buildRows(data, state, live) {
  if (!data?.items) return [];
  const query = normalize(state.query);
  const scope = state.scope;
  let rows;
  if (scope?.kind === "workspace") rows = workspaceRows(data, state, query, scope);
  else if (scope?.kind === "pull-requests") rows = pullRequestRows(data, state, query);
  else if (scope?.kind === "workspaces") rows = workspacesRows(data, state, query);
  else if (query) rows = searchRows(data, state, query);
  else rows = homeRows(data, state, live);
  return withGuides(rows);
}

export function selectable(row) {
  return row?.type === "item" || row?.type === "more" || row?.type === "scope" || row?.type === "launch"
    || row?.type === "peek-option" || row?.type === "peek-input";
}

// The words of an agent state for a row, such as "Working · 3m".
export function stateWords(state, now = Date.now()) {
  switch (state.state) {
    case "needs": return "Needs you";
    case "failed": return "Failed";
    case "ready": return state.fresh ? "Replied" : "Ready";
    case "running": return state.workingSince ? `Working · ${duration(now - state.workingSince)}` : "Working";
    case "delegating": return "Delegating";
    case "background": return "Waiting for tasks";
    case "pending": return "Starting";
    default: return "";
  }
}

// duration gives a short time, such as "40s", "3m", "2h" or "4d".
export function duration(ms) {
  const seconds = Math.max(0, Math.round(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

// excerpt makes the first lines of a reply readable in a small pane: no
// Markdown marks, no code fences, and at most lines lines.
export function excerpt(text, lines = 10) {
  const out = [];
  let fence = false;
  for (const raw of String(text ?? "").split("\n")) {
    if (/^\s*```/.test(raw)) {
      fence = !fence;
      continue;
    }
    let line = raw.replace(/\s+$/, "");
    if (!fence) {
      line = line.replace(/^#{1,6}\s+/, "").replace(/\*\*(.+?)\*\*/g, "$1").replace(/`([^`]+)`/g, "$1")
        .replace(/^\s*[-*]\s+/, "• ").replace(/^\|?\s*-{3,}.*$/, "");
    }
    if (!line.trim() && (!out.length || !out[out.length - 1].trim())) continue;
    out.push(line);
    if (out.length >= lines) break;
  }
  while (out.length && !out[out.length - 1].trim()) out.pop();
  return out.join("\n");
}
