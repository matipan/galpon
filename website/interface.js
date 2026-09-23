// Shared meanings for Galpon-owned surfaces. Herdr keeps its own chrome.
export const icons = Object.freeze({
  brand: "⌂", section: "▰", agent: "◈", workspace: "▦", repository: "▣",
  worktree: "⑂", delegation: "⊶", message: "✉", reasoning: "∴",
  focus: "›", collapsed: "▸", expanded: "▾", add: "+", more: "…",
  pending: "○", idle: "–", attention: "!", success: "✓", failed: "×",
  canceled: "⊘", stopped: "■", unknown: "?",
});

export const activityFrames = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];

export function visualState(status, live = true) {
  if (["running", "working", "starting", "started", "in_progress"].includes(status)) {
    return { glyph: "⠿", style: "working", live, label: status === "in_progress" ? "in progress" : status };
  }
  if (["completed", "succeeded"].includes(status)) return { glyph: icons.success, style: "completed", label: status };
  if (["failed", "expired"].includes(status)) return { glyph: icons.failed, style: "failed", label: status };
  if (["waiting", "blocked"].includes(status)) return { glyph: icons.attention, style: "waiting", label: status };
  if (["queued", "pending"].includes(status)) return { glyph: icons.pending, style: "pending", label: status };
  if (status === "stopped") return { glyph: icons.stopped, style: "idle", label: status };
  if (status === "canceled") return { glyph: icons.canceled, style: "idle", label: status };
  if (["idle", "active", "connected", "changed"].includes(status)) return { glyph: icons.idle, style: "idle", label: status === "active" ? "connected" : status };
  return { glyph: icons.unknown, style: "idle", label: "unknown" };
}

export function escapeHTML(value) {
  return String(value).replace(/[&<>'"]/g, character => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;",
  })[character]);
}

export function stateMark(status, live = true) {
  const mark = visualState(status, live);
  return `<span class="state-mark ${mark.style}"${mark.live ? ' data-live="true"' : ""} aria-hidden="true">${mark.glyph}</span>`;
}

export function sectionTitle(title) {
  return `<h3 class="console-section"><span aria-hidden="true">${icons.section}</span> ${escapeHTML(title)}</h3>`;
}

export function detailField(label, value) {
  return `<div class="detail-field"><dt>${escapeHTML(label)}</dt><dd>${escapeHTML(value)}</dd></div>`;
}

// Compute guides before clipping/scrolling, so ancestor lines remain continuous.
export function resourceDetail(item, state) {
  if (!item) return `${sectionTitle("DETAIL")}<p class="detail-empty">No resource selected.</p>`;
  const field = detailField;
  let facts = "";
  if (item.work) {
    const work = item.work;
    facts = (work.taskId ? field("Task", `#${work.taskId} ${work.taskSubject}`) : "")
      + field("Observed", work.status) + field("Lease", work.lease);
    if (work.checkpoint) facts += field("Reported", work.checkpoint.summary);
    if (work.checkpoint?.blocker) facts += field("Blocked by", work.checkpoint.blocker);
    if (work.activity) facts += field("Activity", `${work.activity.category} · ${work.activity.status}`);
    facts += field("Parent", item.agent.title);
  } else if (item.type === "agent" || item.type === "worktree") {
    const agent = item.agent || state.agents.find(agent => agent.id === item.agentId);
    const repository = state.repositories.find(repo => repo.id === agent.repositoryId);
    facts = field("Session", state.openTabs.includes(agent.id) ? "connected" : "not connected")
      + field("Role", agent.role) + field("Placement", agent.placementLabel || "Private worktree")
      + (repository ? field("Repository", repository.title) + field("Source", `origin · ${agent.ref || repository.branch}`) : "");
    if (agent.directory) facts += field("Directory", agent.directory);
    if (agent.secondary?.length) facts += field("Also includes", agent.secondary.map(id => state.repositories.find(repo => repo.id === id)?.title).filter(Boolean).join(", "));
    if (item.type === "worktree") facts += field("Files", "A terminal and its agent use the same placement.");
  } else if (item.type === "workspace") {
    facts = field("Worktrees", state.agents.filter(agent => agent.workspaceId === item.id && agent.repositoryId && !["directory", "external"].includes(agent.placement)).length)
      + field("Use", "Group related agents and files.") + field("Contents", "Tab shows agents in recent-use order.");
  } else if (item.type === "repository") {
    facts = field("Branch", item.repository.branch) + field("Remotes", item.repository.remotes)
      + field("Use", "A source for private agent worktrees.");
  }
  return `${sectionTitle("DETAIL")}<h3 class="detail-title">${escapeHTML(item.title)}</h3><dl>${facts}</dl><p class="detail-disclaimer">Sample records · browser memory only</p>`;
}

export function treeGuides(rows) {
  const nextDepth = new Map();
  const continues = rows.map(() => false);
  for (let index = rows.length - 1; index >= 0; index--) {
    const depth = rows[index].depth || 0;
    continues[index] = nextDepth.has(depth);
    for (const level of nextDepth.keys()) if (level > depth) nextDepth.delete(level);
    nextDepth.set(depth, index);
  }
  const ancestors = [];
  return rows.map((row, index) => {
    const depth = row.depth || 0;
    ancestors.length = depth;
    let guide = "";
    for (let level = 1; level < depth; level++) guide += ancestors[level] ? "│ " : "  ";
    if (depth) guide += continues[index] ? "├ " : "└ ";
    ancestors[depth] = continues[index];
    return guide;
  });
}
