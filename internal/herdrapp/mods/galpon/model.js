// The rows of the Galpon command center. These functions do not use the app
// or GPUI, so they also run outside the app. They follow the terminal command
// center: the same views, attention order, folded groups and search ranking.

export const VIEWS = ["agent", "pull-request", "workspace", "worktree", "repository"];

export const VIEW_LABELS = {
  agent: "Agents",
  "pull-request": "Pull requests",
  workspace: "Workspaces",
  worktree: "Worktrees",
  repository: "Repositories",
};

export const VIEW_ICONS = { agent: "◈", "pull-request": "⇄", workspace: "▦", worktree: "⑂", repository: "▣" };

export const GROUP_TITLES = {
  agent: "AGENTS",
  "pull-request": "PULL REQUESTS",
  workspace: "WORKSPACES",
  worktree: "WORKTREES",
  repository: "REPOSITORIES",
};

const KIND_ORDER = { agent: 0, workspace: 1, worktree: 2, repository: 3 };

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

// Attention first: failed agents, then working agents, then the others.
function attentionRank(item) {
  if (item.kind !== "agent") return 2;
  return item.state === "failed" ? 0 : item.state === "working" ? 1 : 2;
}

function itemRow(item, depth = 0) {
  return { type: "item", key: itemKey(item), item, depth };
}

// stableOrder keeps rows in place while the command center is open. The first
// order comes from sort; new rows go to the end. Ctrl-R starts a new order.
function stableOrder(items, order, sort) {
  if (order.size === 0) items.sort(sort);
  for (const item of items) {
    const key = itemKey(item);
    if (!order.has(key)) order.set(key, order.size);
  }
  return items.sort((a, b) => order.get(itemKey(a)) - order.get(itemKey(b)));
}

function viewRows(data, state) {
  const kind = state.view;
  const cutoff = data.now - data.olderAfter;
  const recent = [];
  const older = [];
  for (const item of data.items) {
    if (item.kind !== kind || (kind === "agent" && item.delegated)) continue;
    const quiet = (kind === "agent" || kind === "worktree") && item.activityAt > 0 && item.activityAt < cutoff
      && item.state !== "failed" && item.state !== "working";
    (quiet ? older : recent).push(item);
  }
  const rows = stableOrder(recent, state.order, (a, b) => attentionRank(a) - attentionRank(b)).map((item) => itemRow(item));
  if (older.length) {
    const key = `older-${kind}`;
    const open = state.expanded.has(key);
    rows.push({ type: "more", key, kind, open, title: `Older ${GROUP_TITLES[kind].toLowerCase()} (${older.length})`, detail: "tab to expand / collapse" });
    if (open) rows.push(...older.map((item) => itemRow(item)));
  }
  return kind === "agent" ? nestDelegated(data, state, rows) : rows;
}

// Agents that another agent created show below it when it is expanded.
function nestDelegated(data, state, rows) {
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
    out.push(itemRow(item, depth));
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

function searchOrder(a, b) {
  const kind = KIND_ORDER[a.item.kind] - KIND_ORDER[b.item.kind];
  if (kind) return kind;
  if (a.score !== b.score) return b.score - a.score;
  if (a.item.kind === "agent") return agentOrder(a.item, b.item);
  return compareText(lower(a.item.title), lower(b.item.title)) || compareText(a.item.id, b.item.id);
}

// Search finds titles and labels of every kind except pull requests. Each kind
// shows its best matches and a row for the rest.
function searchRows(data, state, query) {
  const matches = [];
  for (const item of data.items) {
    if (!(item.kind in KIND_ORDER)) continue;
    const score = fuzzyScore(item.search || item.title, query);
    if (score >= 0) matches.push({ item, score });
  }
  matches.sort(searchOrder);
  // A result that comes later stays in its kind.
  const ordered = stableOrder(matches.map((match) => match.item), state.order, () => 0)
    .sort((a, b) => KIND_ORDER[a.kind] - KIND_ORDER[b.kind] || state.order.get(itemKey(a)) - state.order.get(itemKey(b)));
  const rows = [];
  for (let start = 0; start < ordered.length;) {
    const kind = ordered[start].kind;
    let end = start + 1;
    while (end < ordered.length && ordered[end].kind === kind) end++;
    const group = ordered.slice(start, end);
    const limit = Math.min(data.searchLimit || 10, group.length);
    rows.push(...group.slice(0, limit).map((item) => itemRow(item)));
    if (group.length > limit) {
      const key = `search-${kind}`;
      const open = state.expanded.has(key);
      rows.push({ type: "more", key, kind, open, title: `More ${GROUP_TITLES[kind].toLowerCase()}`, detail: `${group.length - limit} more · tab to ${open ? "collapse" : "expand"}` });
      if (open) rows.push(...group.slice(limit).map((item) => itemRow(item)));
    }
    start = end;
  }
  return rows;
}

// A workspace expands into all its agents, most recent first.
function expandWorkspaces(data, state, rows) {
  if (!rows.some((row) => row.type === "item" && row.item.kind === "workspace")) return rows;
  const agents = new Map();
  for (const item of data.items) {
    if (item.kind !== "agent") continue;
    if (!agents.has(item.workspaceId)) agents.set(item.workspaceId, []);
    agents.get(item.workspaceId).push(item);
  }
  const out = [];
  for (const row of rows) {
    if (row.type !== "item" || row.item.kind !== "workspace") {
      out.push(row);
      continue;
    }
    const group = agents.get(row.item.id) ?? [];
    const open = state.expanded.has(`workspace:${row.item.id}`);
    let detail = `${group.length} ${group.length === 1 ? "agent" : "agents"} · tab to ${open ? "collapse" : "expand"}`;
    if (row.item.hidden) detail += " · hidden";
    out.push({ ...row, open, detail });
    if (!open) continue;
    for (const agent of [...group].sort(agentOrder)) {
      out.push({ type: "item", key: `${itemKey(agent)}@${row.item.id}`, item: agent, depth: 1, workspace: row.item.id });
    }
  }
  return out;
}

function pullRequestRows(data) {
  const pulls = data.items.filter((item) => item.kind === "pull-request");
  const counts = new Map();
  for (const pull of pulls) counts.set(pull.group ?? 0, (counts.get(pull.group ?? 0) ?? 0) + 1);
  const rows = [];
  let last = -1;
  for (const pull of pulls) {
    const group = pull.group ?? 0;
    if (group !== last) {
      rows.push({ type: "header", key: `group-${group}`, title: `${data.pullRequests.groups[group]} · ${counts.get(group)}` });
      last = group;
    }
    rows.push(itemRow(pull));
  }
  return rows;
}

// Kind headings separate search results. The views show state in a column, so
// they have no headings.
function searchHeadings(rows) {
  const out = [];
  let last = "";
  for (const row of rows) {
    const title = row.workspace ? GROUP_TITLES.workspace : GROUP_TITLES[row.type === "more" ? row.kind : row.item.kind];
    if (title !== last) {
      out.push({ type: "header", key: `heading-${title}`, title });
      last = title;
    }
    out.push(row);
  }
  return out;
}

// Tree guides, one per level: "├" for a child with a later sibling, "└" for
// the last child, "│" while an outer sibling follows, " " for nothing.
function withGuides(rows) {
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

// buildRows gives the rows of the view. state holds the view, the query, the
// expanded keys and the stable order; buildRows adds new rows to the order.
export function buildRows(data, state) {
  if (!data?.items) return [];
  const query = normalize(state.query);
  if (!query && state.view === "pull-request") return withGuides(pullRequestRows(data));
  let rows = query ? searchRows(data, state, query) : viewRows(data, state);
  rows = expandWorkspaces(data, state, rows);
  rows = withGuides(rows);
  return query ? searchHeadings(rows) : rows;
}

export function selectable(row) {
  return row?.type === "item" || row?.type === "more";
}

// expansionKey names what Tab expands on a row, or "" when it expands nothing.
export function expansionKey(row, query) {
  if (!row) return "";
  if (row.type === "more") return row.key;
  if (row.type !== "item" || row.workspace) return "";
  if (row.item.kind === "workspace") return `workspace:${row.item.id}`;
  if (row.item.kind === "agent" && row.item.delegatedCount > 0 && !normalize(query)) return `agent:${row.item.id}`;
  return "";
}

// pullRequestAgent is the visible agent of a pull request, or null.
export function pullRequestAgent(data, pull) {
  if (!pull?.agentId) return null;
  return data.items.find((item) => item.kind === "agent" && item.id === pull.agentId) ?? null;
}

// The workspace of a row, for a new agent.
export function rowWorkspace(row) {
  if (!row || row.type !== "item") return "";
  return row.item.kind === "workspace" ? row.item.id : row.item.workspaceId ?? "";
}
