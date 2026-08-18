import { CompanionAPI, isDefiniteMutationRejection, mutationAttempt, newIdempotencyKey } from "./api.mjs";
import { MockCompanionAPI } from "./mock-api.mjs";
import { mergeOlderDetail, mergeRefreshedDetail } from "./detail-state.mjs";
import { applyMobileViewportCompensation } from "./mobile-viewport.mjs";
import { reduceTimeline } from "./timeline-state.mjs";

applyMobileViewportCompensation();
window.addEventListener("resize", () => applyMobileViewportCompensation());

const $ = (selector) => document.querySelector(selector);
const params = new URLSearchParams(location.search);
const mockMode = params.get("mock") === "1";
const api = mockMode ? new MockCompanionAPI() : new CompanionAPI();
const audioLanguageChoices = new Map();

const elements = {
  connection: $("#connection-control"),
  connectionLabel: $("#connection-label"),
  networkBanner: $("#network-banner"),
  networkTitle: $("#network-title"),
  networkCopy: $("#network-copy"),
  agentsScreen: $("#agents-screen"),
  agentsHeading: $("#agents-heading"),
  detailScreen: $("#detail-screen"),
  search: $("#agent-search"),
  filters: [...document.querySelectorAll(".filter-button")],
  agentsLoading: $("#agents-loading"),
  agentsEmpty: $("#agents-empty"),
  agentsEmptyTitle: $("#agents-empty-title"),
  agentsEmptyCopy: $("#agents-empty-copy"),
  workspaceList: $("#workspace-list"),
  openCreate: $("#open-create"),
  detailWorkspace: $("#detail-workspace"),
  detailTitle: $("#detail-title"),
  detailRole: $("#detail-role"),
  detailState: $("#detail-state"),
  detailLoading: $("#detail-loading"),
  timelineScroll: $("#timeline-scroll"),
  timeline: $("#timeline"),
  timelineEmpty: $("#timeline-empty"),
  loadOlder: $("#load-older"),
  jumpLatest: $("#jump-latest"),
  back: $("#back-to-agents"),
  feedbackForm: $("#feedback-form"),
  feedbackInput: $("#feedback-input"),
  audioLanguage: $("#audio-language"),
  recordAudio: $("#record-audio"),
  sendFeedback: $("#send-feedback"),
  feedbackReceipt: $("#feedback-receipt"),
  createSheet: $("#create-sheet"),
  createForm: $("#create-form"),
  closeCreate: $("#close-create"),
  cancelCreate: $("#cancel-create"),
  newAgentWorkspace: $("#new-agent-workspace"),
  newAgentRepository: $("#new-agent-repository"),
  repositoryStartFields: $("#repository-start-fields"),
  agentStartFields: $("#agent-start-fields"),
  repositoryOptions: $("#repository-options"),
  startModes: [...document.querySelectorAll('input[name="startMode"]')],
  sourceAgent: $("#source-agent"),
  newAgentTitle: $("#new-agent-title"),
  newAgentRole: $("#new-agent-role"),
  newAgentPrompt: $("#new-agent-prompt"),
  launchSummary: $("#launch-summary"),
  createReceipt: $("#create-receipt"),
  submitCreate: $("#submit-create"),
  statuslinePrimary: $("#statusline-primary"),
  statuslineSecondary: $("#statusline-secondary"),
  statuslineDelegated: $("#statusline-delegated"),
  statuslineDelegatedCount: $("#statusline-delegated-count"),
  toastRegion: $("#toast-region"),
};

const state = {
  bootstrap: null,
  selected: null,
  filter: "all",
  query: "",
  cursor: 0,
  connection: "connecting",
  streamClose: null,
  bootstrapController: null,
  detailController: null,
  refreshTimer: null,
  refreshInFlight: false,
  refreshDirty: false,
  feedbackAttempt: null,
  createAttempt: null,
  createBusy: false,
  audioRecorder: null,
  audioStream: null,
  audioTimer: null,
  audioDiscard: false,
  audioBusy: false,
  followConversation: true,
  firstLoad: true,
};

function setConnection(value, detail = "") {
  state.connection = value;
  elements.connection.dataset.state = value;
  const labels = {
    connecting: "Connecting",
    online: mockMode ? "Mock host" : "Host online",
    reconnecting: "Reconnecting",
    offline: "Host offline",
    error: "Connection failed",
  };
  elements.connectionLabel.textContent = labels[value] || "Connection unknown";
  elements.statuslineSecondary.textContent = mockMode ? "Isolated preview" : connectionStatusText();
  if (!elements.detailScreen.hidden) {
    elements.statuslinePrimary.textContent = connectionModeText(value);
  }

  const showBanner = value === "reconnecting" || value === "offline" || value === "error";
  elements.networkBanner.hidden = !showBanner;
  if (value === "offline") {
    elements.networkTitle.textContent = "Host offline";
    elements.networkCopy.textContent = detail || "The last synchronized view remains available.";
  } else if (value === "error") {
    elements.networkTitle.textContent = "Could not reach Galpón";
    elements.networkCopy.textContent = detail || "Check the host address and try again.";
  } else if (value === "reconnecting") {
    elements.networkTitle.textContent = "Reconnecting";
    elements.networkCopy.textContent = "New activity will appear after the connection returns.";
  }
}

async function loadBootstrap({ initial = false } = {}) {
  state.bootstrapController?.abort();
  const controller = new AbortController();
  state.bootstrapController = controller;
  if (initial) elements.agentsLoading.hidden = false;

  try {
    const value = await api.bootstrap({ signal: controller.signal });
    if (controller.signal.aborted) return;
    state.bootstrap = normalizeBootstrap(value);
    state.cursor = Math.max(state.cursor, Number(value.cursor || 0));
    elements.agentsLoading.hidden = true;
    renderAgents();
    populateLaunchOptions();
    syncAudioControl();
    setConnection("online");
    if (initial) startEventStream();
  } catch (error) {
    if (error?.name === "AbortError") return;
    elements.agentsLoading.hidden = true;
    if (!state.bootstrap) {
      state.bootstrap = { repositories: [], workspaces: [] };
      renderAgents({ loadError: true });
    }
    setConnection(navigator.onLine ? "error" : "offline", error.message);
  } finally {
    if (state.bootstrapController === controller) state.bootstrapController = null;
    state.firstLoad = false;
  }
}

function normalizeBootstrap(value) {
  const workspaces = Array.isArray(value?.workspaces) ? value.workspaces : [];
  const repositories = Array.isArray(value?.repositories) ? value.repositories : [];
  return {
    ...value,
    repositories: repositories.map((repository) => ({
      id: String(repository?.id || ""),
      title: String(repository?.title || "Unknown repository"),
    })),
    workspaces: workspaces.map((workspace) => ({
      id: String(workspace?.id || ""),
      title: String(workspace?.title || "Unknown workspace"),
      agents: (Array.isArray(workspace?.agents) ? workspace.agents : []).map(normalizeAgentSummary),
    })),
  };
}

function normalizeAgentSummary(agent) {
  return {
    ...agent,
    id: String(agent?.id || ""),
    title: String(agent?.title || "Untitled agent"),
    role: String(agent?.role || ""),
    status: normalizeStatus(agent?.status),
    lastActivity: String(agent?.lastActivity || ""),
    delegatedAgents: (Array.isArray(agent?.delegatedAgents) ? agent.delegatedAgents : []).map(normalizeAgentSummary),
  };
}

function startEventStream() {
  state.streamClose?.();
  state.streamClose = api.subscribe(state.cursor, {
    onState(value) {
      if (value === "online") setConnection("online");
      if (value === "reconnecting") setConnection(navigator.onLine ? "reconnecting" : "offline");
    },
    onEvent(value) {
      state.cursor = Math.max(state.cursor, Number(value?.seq || 0));
      scheduleInvalidation();
    },
  });
}

function scheduleInvalidation() {
  state.refreshDirty = true;
  if (state.refreshTimer || state.refreshInFlight) return;
  state.refreshTimer = setTimeout(runInvalidationRefresh, 300);
}

async function runInvalidationRefresh() {
  state.refreshTimer = null;
  if (state.refreshInFlight) return;
  state.refreshInFlight = true;
  try {
    state.refreshDirty = false;
    await loadBootstrap();
    const agentId = state.selected?.agent?.id;
    if (agentId) await loadAgent(agentId, { preserve: true });
  } finally {
    state.refreshInFlight = false;
    if (state.refreshDirty) scheduleInvalidation();
  }
}

function renderAgents({ loadError = false } = {}) {
  elements.workspaceList.replaceChildren();
  const workspaces = state.bootstrap?.workspaces || [];
  const query = state.query.trim().toLocaleLowerCase();
  let visibleCount = 0;

  for (const workspace of workspaces) {
    const agents = workspace.agents
      .map((agent) => filterAgentTree(agent, workspace.title, query, state.filter))
      .filter(Boolean);
    if (!agents.length) continue;
    visibleCount += agents.reduce((count, agent) => count + countAgentTree(agent), 0);

    const section = document.createElement("section");
    section.className = "workspace-group";
    section.dataset.workspaceId = workspace.id;
    const heading = document.createElement("h2");
    heading.className = "workspace-title";
    heading.textContent = workspace.title;
    section.append(heading);

    const list = document.createElement("ul");
    list.className = "agent-list";
    const expandDelegated = Boolean(query) || state.filter !== "all";
    for (const agent of agents) list.append(renderAgentRow(workspace, agent, expandDelegated));
    section.append(list);
    elements.workspaceList.append(section);
  }

  elements.agentsEmpty.hidden = visibleCount !== 0;
  if (visibleCount === 0) {
    if (loadError) {
      elements.agentsEmptyTitle.textContent = "Galpón is unavailable";
      elements.agentsEmptyCopy.textContent = "This browser has no synchronized agent list yet.";
    } else if (query) {
      elements.agentsEmptyTitle.textContent = "No title matches";
      elements.agentsEmptyCopy.textContent = `No agent or workspace title matches “${state.query.trim()}”.`;
    } else if (state.filter === "active") {
      elements.agentsEmptyTitle.textContent = "No active agents";
      elements.agentsEmptyCopy.textContent = "No agent is starting or working now.";
    } else if (state.filter === "attention") {
      elements.agentsEmptyTitle.textContent = "Nothing needs you";
      elements.agentsEmptyCopy.textContent = "No agent is blocked or failed.";
    } else {
      elements.agentsEmptyTitle.textContent = "Your galpón is quiet";
      elements.agentsEmptyCopy.textContent = "Start an agent from an existing workspace and repository.";
    }
  }

  if (!elements.agentsScreen.hidden) {
    elements.statuslinePrimary.textContent = `${visibleCount} AGENT${visibleCount === 1 ? "" : "S"}`;
    elements.statuslineSecondary.textContent = mockMode ? "Mock data · isolated" : connectionStatusText();
  }
}

function renderAgentRow(workspace, agent, expandDelegated = false) {
  const item = document.createElement("li");
  const button = document.createElement("button");
  button.type = "button";
  button.className = "agent-row";
  button.dataset.agentId = agent.id;
  button.setAttribute("aria-label", `${agent.title}, ${statusLabel(agent.status)}, ${workspace.title}`);

  const avatar = conversationIdentity({ role: "assistant" });
  avatar.classList.add("agent-row-mark");
  avatar.removeAttribute("title");
  const mark = document.createElement("span");
  mark.className = "status-mark";
  mark.dataset.status = agent.status;
  mark.setAttribute("aria-hidden", "true");
  avatar.append(mark);

  const copy = document.createElement("span");
  copy.className = "agent-row-copy";
  const title = document.createElement("span");
  title.className = "agent-row-title";
  title.textContent = agent.title;
  const detail = document.createElement("span");
  detail.className = "agent-row-detail";
  detail.textContent = [agent.role, statusLabel(agent.status), agent.lastActivity].filter(Boolean).join(" · ");
  copy.append(title, detail);

  const time = document.createElement("span");
  time.className = "agent-row-time";
  time.textContent = relativeTime(agent.updatedAt);
  if (agent.updatedAt) time.title = formatDate(agent.updatedAt);

  button.append(avatar, copy, time);
  button.addEventListener("click", () => openAgent(agent.id));
  item.append(button);
  if (agent.delegatedAgents?.length) {
    const disclosure = document.createElement("details");
    disclosure.className = "delegated-disclosure";
    disclosure.open = expandDelegated;
    const summary = document.createElement("summary");
    summary.textContent = `${agent.delegatedAgents.length} delegated ${agent.delegatedAgents.length === 1 ? "agent" : "agents"}`;
    const delegated = document.createElement("ul");
    delegated.className = "delegated-agent-list";
    delegated.setAttribute("aria-label", `Agents delegated by ${agent.title}`);
    for (const child of agent.delegatedAgents) {
      const childWorkspace = { id: child.workspaceId, title: child.workspaceTitle || workspace.title };
      delegated.append(renderAgentRow(childWorkspace, child, expandDelegated));
    }
    disclosure.append(summary, delegated);
    item.append(disclosure);
  }
  return item;
}

function filterAgentTree(agent, workspaceTitle, query, filter) {
  const delegatedAgents = (agent.delegatedAgents || [])
    .map((child) => filterAgentTree(child, child.workspaceTitle || workspaceTitle, query, filter))
    .filter(Boolean);
  const titleMatch = !query
    || agent.title.toLocaleLowerCase().includes(query)
    || workspaceTitle.toLocaleLowerCase().includes(query);
  if (!(titleMatch && matchesFilter(agent, filter)) && delegatedAgents.length === 0) return null;
  return { ...agent, delegatedAgents };
}

function countAgentTree(agent) {
  return 1 + (agent.delegatedAgents || []).reduce((count, child) => count + countAgentTree(child), 0);
}

function flattenAgentTree(agents) {
  const output = [];
  const visit = (agent) => {
    output.push(agent);
    for (const child of agent.delegatedAgents || []) visit(child);
  };
  for (const agent of agents) visit(agent);
  return output;
}

function matchesFilter(agent, filter) {
  if (filter === "active") return ["running", "starting"].includes(agent.status);
  if (filter === "attention") {
    return ["failed", "attention", "blocked", "needs_input"].includes(agent.status)
      || agent.needsAttention === true;
  }
  return true;
}

function openAgent(id, { updateHistory = true } = {}) {
  if (state.selected?.agent?.id && state.selected.agent.id !== id) {
    stopAudioRecording({ discard: true });
    state.selected = null;
  }
  if (updateHistory) history.pushState({ agentId: id }, "", `#agent=${encodeURIComponent(id)}`);
  elements.agentsScreen.hidden = true;
  elements.detailScreen.hidden = false;
  document.body.dataset.view = "detail";
  elements.detailLoading.hidden = false;
  elements.timelineEmpty.hidden = true;
  elements.loadOlder.hidden = true;
  elements.feedbackReceipt.textContent = "";
  elements.jumpLatest.hidden = true;
  elements.statuslinePrimary.textContent = connectionModeText(state.connection);
  elements.statuslineSecondary.textContent = mockMode ? "Isolated preview" : connectionStatusText();
  elements.statuslineDelegated.hidden = true;
  state.followConversation = true;
  loadAgent(id);
  requestAnimationFrame(() => elements.detailTitle.focus());
}

async function loadAgent(id, { preserve = false } = {}) {
  state.detailController?.abort();
  const controller = new AbortController();
  state.detailController = controller;
  if (!preserve) elements.detailLoading.hidden = false;

  try {
    const value = await api.agent(id, { signal: controller.signal });
    if (controller.signal.aborted) return;
    const fresh = normalizeAgentDetail(value);
    state.selected = preserve ? mergeRefreshedDetail(state.selected, fresh) : fresh;
    state.cursor = Math.max(state.cursor, Number(value.cursor || 0));
    elements.detailLoading.hidden = true;
    renderDetail();
  } catch (error) {
    if (error?.name === "AbortError") return;
    elements.detailLoading.hidden = true;
    setReceipt(elements.feedbackReceipt, "error", error.message || "The discussion could not be loaded.");
    if (!state.selected) {
      elements.timeline.replaceChildren();
      elements.timelineEmpty.hidden = false;
      elements.timelineEmpty.querySelector("strong").textContent = "Discussion unavailable";
      elements.timelineEmpty.querySelector("p").textContent = "Return to the agent list and try again.";
    }
  } finally {
    if (state.detailController === controller) state.detailController = null;
  }
}

function normalizeAgentDetail(value) {
  const agent = value?.agent || {};
  return {
    cursor: Number(value?.cursor || 0),
    agent: {
      id: String(agent.id || ""),
      title: String(agent.title || "Untitled agent"),
      role: String(agent.role || ""),
      status: normalizeStatus(agent.status),
      workspaceId: String(agent.workspaceId || ""),
      workspaceTitle: String(agent.workspaceTitle || "Unknown workspace"),
    },
    timeline: Array.isArray(value?.timeline) ? value.timeline : [],
    hasMore: value?.hasMore === true,
    conversationHasMore: value?.conversationHasMore === true,
    messageHasMore: value?.messageHasMore === true,
    before: Number(value?.before || 0),
    messageBefore: String(value?.messageBefore || ""),
    mirroredDeliveryResponses: Array.isArray(value?.mirroredDeliveryResponses)
      ? value.mirroredDeliveryResponses.map(String)
      : [],
    messagePageIds: Array.isArray(value?.messagePageIds) ? value.messagePageIds.map(String) : [],
    delegatedAgents: (Array.isArray(value?.delegatedAgents) ? value.delegatedAgents : []).map(normalizeAgentSummary),
  };
}

function renderDetail() {
  if (!state.selected) return;
  const { agent, timeline } = state.selected;
  const hadTimeline = elements.timeline.childElementCount > 0;
  const nearBottom = isNearConversationEnd();
  const shouldFollow = !hadTimeline || state.followConversation || nearBottom;
  const disclosureState = new Map([...elements.timeline.querySelectorAll("details[data-disclosure-id]")]
    .map((details) => [details.dataset.disclosureId, details.open]));
  const focusedItemId = document.activeElement?.closest?.(".timeline-item")?.dataset.itemId || "";
  const focusedDisclosureId = document.activeElement?.closest?.("details")?.dataset.disclosureId || "";

  elements.detailWorkspace.textContent = [agent.workspaceTitle, agent.role].filter(Boolean).join(" · ");
  elements.detailTitle.textContent = agent.title;
  elements.detailRole.textContent = "";
  elements.detailRole.hidden = true;
  elements.detailState.dataset.status = agent.status;
  elements.detailState.querySelector("span:last-child").textContent = statusLabel(agent.status);
  elements.detailState.setAttribute("aria-label", `Agent status: ${statusLabel(agent.status)}`);
  document.title = `${agent.title} · Galpón`;
  syncAudioControl();

  const delegatedCount = (state.selected.delegatedAgents || [])
    .reduce((count, child) => count + countAgentTree(child), 0);
  elements.statuslineDelegatedCount.textContent = String(delegatedCount);
  elements.statuslineDelegated.hidden = delegatedCount === 0;

  const reduced = reduceTimeline(timeline);
  elements.timeline.replaceChildren(...reduced.map(renderTimelineItem));
  for (const details of elements.timeline.querySelectorAll("details[data-disclosure-id]")) {
    if (disclosureState.has(details.dataset.disclosureId)) details.open = disclosureState.get(details.dataset.disclosureId);
  }
  elements.timelineEmpty.hidden = reduced.length !== 0;
  elements.loadOlder.hidden = !state.selected.hasMore;
  elements.loadOlder.disabled = false;

  elements.jumpLatest.hidden = shouldFollow || reduced.length === 0;
  if (shouldFollow) {
    state.followConversation = true;
    requestAnimationFrame(scrollToConversationEnd);
  }
  if (focusedItemId) {
    requestAnimationFrame(() => {
      const row = [...elements.timeline.querySelectorAll(".timeline-item")]
        .find((item) => item.dataset.itemId === focusedItemId);
      const summary = focusedDisclosureId
        ? [...(row?.querySelectorAll("details") || [])].find((details) => details.dataset.disclosureId === focusedDisclosureId)?.querySelector("summary")
        : row?.querySelector("summary");
      summary?.focus({ preventScroll: true });
    });
  }
}

function isNearConversationEnd() {
  return elements.timelineScroll.scrollTop + elements.timelineScroll.clientHeight >= elements.timelineScroll.scrollHeight - 120;
}

function scrollToConversationEnd() {
  elements.timelineScroll.scrollTo({ top: elements.timelineScroll.scrollHeight, behavior: "auto" });
  elements.jumpLatest.hidden = true;
}

function renderTimelineItem(item) {
  const row = document.createElement("li");
  row.className = "timeline-item";
  row.dataset.kind = item.kind;
  row.dataset.role = item.role;
  row.dataset.itemId = item.id;
  if (item.state) row.dataset.state = item.state;

  const identity = conversationIdentity(item);
  const body = document.createElement("article");
  body.className = "timeline-content";
  const meta = document.createElement("div");
  meta.className = "timeline-meta";
  const label = document.createElement("span");
  const labelText = timelineLabel(item);
  label.className = item.role === "user" || item.role === "assistant" ? "sr-only" : "timeline-context";
  label.textContent = labelText;
  const time = document.createElement("time");
  const date = item.updatedAt || item.createdAt;
  time.dateTime = validDate(date) ? new Date(date).toISOString() : "";
  time.textContent = timelineTime(date);
  if (date) time.title = formatDate(date);
  meta.append(label, time);
  body.append(meta);

  if (item.role === "tools") {
    body.append(renderToolGroup(item));
  } else {
    const text = document.createElement("p");
    text.className = "discussion-text";
    text.textContent = String(item.content || "").trim() || (item.state === "running" ? "Agent is responding…" : "");
    body.append(text);
  }

  const showsState = item.state === "failed"
    || item.role === "user" && ["queued", "delivered"].includes(item.state);
  if (showsState && item.role !== "tools") {
    const eventState = document.createElement("span");
    eventState.className = "event-state";
    eventState.dataset.state = item.state;
    eventState.textContent = item.state;
    body.append(eventState);
  }

  row.append(identity, body);
  return row;
}

function conversationIdentity(item) {
  const identity = document.createElement("span");
  const role = item.role === "assistant" ? "assistant" : item.role === "user" ? "user" : item.role === "tools" ? "tools" : "system";
  identity.className = `conversation-mark conversation-mark-${role}`;
  identity.setAttribute("aria-hidden", "true");
  identity.title = timelineLabel(item);
  const icons = {
    assistant: '<svg viewBox="0 0 24 24"><path d="M12 2l2.15 7.85L22 12l-7.85 2.15L12 22l-2.15-7.85L2 12l7.85-2.15L12 2Z"/><circle cx="18.5" cy="5.5" r="1.5"/></svg>',
    user: '<svg viewBox="0 0 24 24"><circle cx="12" cy="8" r="3.2"/><path d="M5.5 20c.65-4.15 2.8-6.2 6.5-6.2s5.85 2.05 6.5 6.2"/></svg>',
    tools: '<svg viewBox="0 0 24 24"><path d="M8 7 3 12l5 5M16 7l5 5-5 5M14 4l-4 16"/></svg>',
    system: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M12 2v4M12 18v4M2 12h4M18 12h4"/></svg>',
  };
  identity.innerHTML = icons[role];
  return identity;
}

function renderToolGroup(item) {
  const group = document.createElement("div");
  group.className = "tool-stack";
  group.setAttribute("role", "group");
  group.setAttribute("aria-label", `${item.tools.length} tool ${item.tools.length === 1 ? "action" : "actions"}`);
  for (const tool of item.tools) {
    const details = document.createElement("details");
    details.className = "tool-line";
    details.dataset.disclosureId = `tool:${tool.id}`;
    details.dataset.state = tool.state || "running";

    const summary = document.createElement("summary");
    const emoji = document.createElement("span");
    emoji.className = "tool-emoji";
    emoji.setAttribute("aria-hidden", "true");
    emoji.textContent = toolEmoji(tool);
    const description = document.createElement("span");
    description.className = "tool-description";
    description.textContent = toolDescription(tool);
    const status = document.createElement("span");
    status.className = "tool-line-status";
    status.dataset.state = tool.state || "running";
    status.setAttribute("aria-label", toolStateLabel(tool.state));
    status.title = toolStateLabel(tool.state);
    summary.append(emoji, description, status);

    const output = document.createElement("pre");
    const parts = [];
    if (tool.input) parts.push(`Input\n${tool.input}`);
    if (tool.output) parts.push(`Output\n${tool.output}`);
    output.textContent = parts.join("\n\n") || "No detail was recorded.";
    details.append(summary, output);
    group.append(details);
  }
  return group;
}

function toolEmoji(tool) {
  const name = String(tool.toolName || "").toLocaleLowerCase();
  const input = String(tool.input || "").toLocaleLowerCase();
  if (name.includes("parallel") || name.includes("multi_tool")) return "⚙️";
  if (name.includes("bash") && /(go test|node --test|npm test|pnpm test|pytest|cargo test)/.test(input)) return "🧪";
  if (name.includes("bash") || name.includes("shell") || name.includes("exec")) return "⚡";
  if (name.includes("read")) return "📖";
  if (name.includes("edit")) return "✏️";
  if (name.includes("write")) return "📝";
  if (name.includes("search") || name.includes("grep")) return "🔎";
  if (name.includes("find") || name.includes("list")) return "🧭";
  if (name.includes("agent") || name.includes("message")) return "🤝";
  if (name.includes("web") || name.includes("http") || name.includes("fetch")) return "🌐";
  return "🔧";
}

function toolDescription(tool) {
  const name = String(tool.toolName || "Tool");
  const normalized = name.toLocaleLowerCase();
  const args = parseToolInput(tool.input);
  const path = conciseValue(args.path || args.file || args.filename);
  const command = conciseValue(args.command || args.cmd, 96);
  const query = conciseValue(args.query || args.pattern || args.prompt, 84);
  const target = conciseValue(args.agent || args.title || args.repository || args.workspace, 64);

  if (normalized.includes("parallel") || normalized.includes("multi_tool")) {
    const count = Array.isArray(args.tool_uses) ? args.tool_uses.length : 0;
    return count ? `Run ${count} actions in parallel` : "Run actions in parallel";
  }
  if (normalized.includes("read")) return path ? `Read ${path}` : "Read project context";
  if (normalized.includes("edit")) return path ? `Edit ${path}` : "Edit project files";
  if (normalized.includes("write")) return path ? `Write ${path}` : "Write project file";
  if (normalized.includes("bash") || normalized.includes("shell") || normalized.includes("exec")) {
    const raw = command || conciseValue(tool.input, 96);
    return raw ? `Run ${raw}` : "Run project command";
  }
  if (normalized.includes("search") || normalized.includes("grep")) return query ? `Search for ${query}` : "Search the project";
  if (normalized.includes("find") || normalized.includes("list")) return query || path ? `Find ${query || path}` : "Inspect project structure";
  if (normalized.includes("agent") || normalized.includes("message")) return target ? `Coordinate with ${target}` : "Coordinate agent work";
  if (normalized.includes("web") || normalized.includes("http") || normalized.includes("fetch")) return query ? `Check ${query}` : "Check a web resource";
  return `${humanizeKind(name)}${path || query || target ? ` · ${path || query || target}` : ""}`;
}

function parseToolInput(value) {
  if (!value) return {};
  try {
    const parsed = JSON.parse(value);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

function conciseValue(value, maximum = 72) {
  const text = String(value || "").replace(/\s+/g, " ").trim();
  if (!text) return "";
  return text.length <= maximum ? text : `${text.slice(0, maximum - 1).trimEnd()}…`;
}

function toolStateLabel(value) {
  if (value === "failed") return "Failed";
  if (value === "completed") return "Completed";
  return "Running";
}

function timelineLabel(item) {
  if (item.role === "user") return "Your message";
  if (item.role === "assistant") return "Agent message";
  if (item.role === "tools") return `${item.tools.length} ${item.tools.length === 1 ? "action" : "actions"}`;
  return humanizeKind(item.kind);
}

async function loadOlderDiscussion() {
  const selected = state.selected;
  if (!selected?.hasMore || elements.loadOlder.disabled) return;
  state.followConversation = false;
  elements.loadOlder.disabled = true;
  const oldHeight = elements.timelineScroll.scrollHeight;
  try {
    const value = await api.agent(selected.agent.id, {
      before: selected.conversationHasMore ? selected.before : 0,
      messageBefore: selected.messageHasMore ? selected.messageBefore : "",
    });
    const older = normalizeAgentDetail(value);
    const current = state.selected;
    if (!current || current.agent.id !== selected.agent.id) return;
    state.selected = mergeOlderDetail(current, older);
    state.cursor = Math.max(state.cursor, Number(value.cursor || 0));
    renderDetail();
    requestAnimationFrame(() => {
      elements.timelineScroll.scrollTop += elements.timelineScroll.scrollHeight - oldHeight;
    });
  } catch (error) {
    elements.loadOlder.disabled = false;
    showToast(error.message || "Older discussion could not be loaded.", "error");
  }
}

async function sendFeedback(event) {
  event.preventDefault();
  const agentId = state.selected?.agent?.id;
  const prompt = elements.feedbackInput.value.trim();
  if (!agentId || !prompt) return;

  state.followConversation = true;
  scrollToConversationEnd();
  elements.feedbackInput.blur();
  elements.sendFeedback.disabled = true;
  elements.recordAudio.disabled = true;
  elements.audioLanguage.disabled = true;
  elements.feedbackInput.disabled = true;
  setReceipt(elements.feedbackReceipt, "pending", "Sending feedback…");
  const payload = { agentId, prompt };
  const attempt = mutationAttempt(state.feedbackAttempt, payload);
  state.feedbackAttempt = attempt;
  try {
    const value = await api.sendMessage(agentId, prompt, attempt.key);
    state.feedbackAttempt = null;
    elements.feedbackInput.value = "";
    resizeTextarea(elements.feedbackInput);
    const delivery = value?.delivery || value?.message?.status || "queued";
    setReceipt(elements.feedbackReceipt, "success", deliveryReceipt(delivery));
    scheduleInvalidation();
  } catch (error) {
    if (isDefiniteMutationRejection(error)) state.feedbackAttempt = null;
    setReceipt(elements.feedbackReceipt, "error", error.message || "Feedback was not sent.");
  } finally {
    elements.sendFeedback.disabled = false;
    elements.feedbackInput.disabled = false;
    syncAudioControl();
  }
}

function audioMessagesSupported() {
  return state.bootstrap?.audioMessages === true
    && Boolean(navigator.mediaDevices?.getUserMedia)
    && typeof MediaRecorder !== "undefined";
}

function audioLanguage(agentId = state.selected?.agent?.id) {
  if (!agentId) return "en";
  if (audioLanguageChoices.has(agentId)) return audioLanguageChoices.get(agentId);
  let language = "en";
  try {
    language = localStorage.getItem(`galpon.audio-language.${agentId}`) === "es" ? "es" : "en";
  } catch {
    // Keep the default when browser storage is unavailable.
  }
  audioLanguageChoices.set(agentId, language);
  return language;
}

function syncAudioControl() {
  const supported = audioMessagesSupported();
  const language = audioLanguage();
  const recording = state.audioRecorder?.state === "recording";
  elements.audioLanguage.hidden = !supported;
  elements.audioLanguage.disabled = !supported || state.audioBusy || recording;
  elements.audioLanguage.textContent = language.toLocaleUpperCase();
  elements.audioLanguage.dataset.language = language;
  const current = language === "es" ? "Spanish" : "English";
  const next = language === "es" ? "English" : "Spanish";
  elements.audioLanguage.setAttribute("aria-label", `Voice transcription language: ${current}. Change to ${next}`);
  elements.audioLanguage.title = `Voice transcription: ${current}`;
  elements.recordAudio.hidden = !supported;
  elements.recordAudio.disabled = !supported || state.audioBusy;
}

function toggleAudioLanguage() {
  const agentId = state.selected?.agent?.id;
  if (!agentId || state.audioBusy || state.audioRecorder?.state === "recording") return;
  const language = audioLanguage(agentId) === "es" ? "en" : "es";
  audioLanguageChoices.set(agentId, language);
  try {
    localStorage.setItem(`galpon.audio-language.${agentId}`, language);
  } catch {
    // The current choice still applies when browser storage is unavailable.
  }
  syncAudioControl();
  setReceipt(elements.feedbackReceipt, "success", `Voice transcription set to ${language === "es" ? "Spanish" : "English"}.`);
}

async function toggleAudioRecording() {
  if (state.audioRecorder?.state === "recording") {
    stopAudioRecording();
    return;
  }
  if (!audioMessagesSupported() || state.audioBusy || !state.selected?.agent?.id) return;

  state.audioBusy = true;
  syncAudioControl();
  setReceipt(elements.feedbackReceipt, "pending", "Requesting microphone access…");
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true },
    });
    const agentId = state.selected?.agent?.id;
    if (!agentId || elements.detailScreen.hidden) {
      for (const track of stream.getTracks()) track.stop();
      state.audioBusy = false;
      syncAudioControl();
      return;
    }
    const language = audioLanguage(agentId);
    const mimeType = preferredAudioType();
    const recorder = new MediaRecorder(stream, mimeType ? { mimeType } : undefined);
    const chunks = [];
    state.audioRecorder = recorder;
    state.audioStream = stream;
    state.audioDiscard = false;
    recorder.addEventListener("dataavailable", (event) => {
      if (event.data?.size) chunks.push(event.data);
    });
    recorder.addEventListener("error", () => {
      state.audioDiscard = true;
      stopAudioRecording({ discard: true });
      setReceipt(elements.feedbackReceipt, "error", "The recording failed. Try again.");
    });
    recorder.addEventListener("stop", () => finishAudioRecording(recorder, chunks, agentId, language));
    recorder.start(1_000);
    state.audioBusy = false;
    elements.recordAudio.dataset.state = "recording";
    elements.recordAudio.setAttribute("aria-label", "Stop and send voice message");
    elements.recordAudio.disabled = false;
    elements.audioLanguage.disabled = true;
    elements.feedbackInput.disabled = true;
    elements.sendFeedback.disabled = true;
    setReceipt(elements.feedbackReceipt, "pending", `Recording in ${language === "es" ? "Spanish" : "English"}… Tap the microphone to stop and send.`);
    state.audioTimer = setTimeout(() => stopAudioRecording(), 120_000);
  } catch (error) {
    if (stream) for (const track of stream.getTracks()) track.stop();
    state.audioBusy = false;
    syncAudioControl();
    const denied = error?.name === "NotAllowedError" || error?.name === "SecurityError";
    setReceipt(elements.feedbackReceipt, "error", denied
      ? "Microphone access is required for a voice message."
      : "The microphone could not start.");
  }
}

function preferredAudioType() {
  const types = ["audio/webm;codecs=opus", "audio/mp4", "audio/ogg;codecs=opus", "audio/webm"];
  return types.find((type) => MediaRecorder.isTypeSupported?.(type)) || "";
}

function stopAudioRecording({ discard = false } = {}) {
  const recorder = state.audioRecorder;
  if (!recorder) return;
  state.audioDiscard = state.audioDiscard || discard;
  clearTimeout(state.audioTimer);
  state.audioTimer = null;
  if (recorder.state !== "inactive") recorder.stop();
  for (const track of state.audioStream?.getTracks?.() || []) track.stop();
}

async function finishAudioRecording(recorder, chunks, agentId, language) {
  if (state.audioRecorder !== recorder) return;
  const discard = state.audioDiscard;
  state.audioRecorder = null;
  state.audioStream = null;
  state.audioDiscard = false;
  clearTimeout(state.audioTimer);
  state.audioTimer = null;
  delete elements.recordAudio.dataset.state;
  elements.recordAudio.setAttribute("aria-label", "Record a voice message");
  if (discard || !chunks.length || state.selected?.agent?.id !== agentId) {
    state.audioBusy = false;
    elements.feedbackInput.disabled = false;
    elements.sendFeedback.disabled = false;
    syncAudioControl();
    return;
  }

  const audio = new Blob(chunks, { type: recorder.mimeType || chunks[0]?.type || "application/octet-stream" });
  if (!audio.size) {
    state.audioBusy = false;
    elements.feedbackInput.disabled = false;
    elements.sendFeedback.disabled = false;
    syncAudioControl();
    setReceipt(elements.feedbackReceipt, "error", "The recording was empty. Try again.");
    return;
  }

  state.audioBusy = true;
  state.followConversation = true;
  scrollToConversationEnd();
  elements.recordAudio.disabled = true;
  setReceipt(elements.feedbackReceipt, "pending", `Transcribing ${language === "es" ? "Spanish" : "English"} voice message…`);
  try {
    const value = await api.sendAudioMessage(agentId, audio, language, newIdempotencyKey());
    const delivery = value?.message?.status || value?.delivery || "queued";
    setReceipt(elements.feedbackReceipt, "success", `Voice message transcribed. ${deliveryReceipt(delivery)}`);
    scheduleInvalidation();
  } catch (error) {
    setReceipt(elements.feedbackReceipt, "error", error.message || "The voice message was not sent.");
  } finally {
    state.audioBusy = false;
    elements.feedbackInput.disabled = false;
    elements.sendFeedback.disabled = false;
    syncAudioControl();
  }
}

function deliveryReceipt(value) {
  const delivery = String(value).toLocaleLowerCase();
  if (["steered", "current", "current_turn"].includes(delivery)) return "Sent to the current turn.";
  if (["delivered", "received"].includes(delivery)) return "The agent received your feedback.";
  return "Queued for the agent’s next safe point.";
}

function populateLaunchOptions() {
  const previousWorkspace = elements.newAgentWorkspace.value;
  const previousRepository = elements.newAgentRepository.value;
  const previousSource = elements.sourceAgent.value;

  elements.newAgentWorkspace.replaceChildren(optionElement("", "Choose a workspace"));
  for (const workspace of state.bootstrap?.workspaces || []) {
    elements.newAgentWorkspace.append(optionElement(workspace.id, workspace.title));
  }
  restoreSelect(elements.newAgentWorkspace, previousWorkspace);

  elements.newAgentRepository.replaceChildren(optionElement("", "Choose a repository"));
  for (const repository of state.bootstrap?.repositories || []) {
    elements.newAgentRepository.append(optionElement(repository.id, repository.title));
  }
  restoreSelect(elements.newAgentRepository, previousRepository);

  elements.sourceAgent.replaceChildren(optionElement("", "Choose an agent"));
  for (const workspace of state.bootstrap?.workspaces || []) {
    const eligible = flattenAgentTree(workspace.agents).filter(isEligibleSource);
    if (!eligible.length) continue;
    const group = document.createElement("optgroup");
    group.label = workspace.title;
    for (const agent of eligible) {
      const option = optionElement(agent.id, agent.title);
      option.dataset.workspaceTitle = workspace.title;
      option.dataset.agentTitle = agent.title;
      group.append(option);
    }
    elements.sourceAgent.append(group);
  }
  restoreSelect(elements.sourceAgent, previousSource);

  const repositoryMode = elements.startModes.find((input) => input.value === "repository");
  const agentMode = elements.startModes.find((input) => input.value === "agent");
  repositoryMode.disabled = (state.bootstrap?.repositories || []).length === 0;
  agentMode.disabled = ![...elements.sourceAgent.options].some((option) => option.value);
  if (repositoryMode.disabled && !agentMode.disabled) agentMode.checked = true;
  if (agentMode.disabled && !repositoryMode.disabled) repositoryMode.checked = true;
  renderAdditionalRepositories();
  syncLaunchMode();
}

function optionElement(value, label) {
  const option = document.createElement("option");
  option.value = value;
  option.textContent = label;
  return option;
}

function restoreSelect(select, value) {
  if (value && [...select.options].some((option) => option.value === value)) {
    select.value = value;
  } else if (select.options.length > 1) {
    select.selectedIndex = 1;
  }
}

function renderAdditionalRepositories() {
  const selected = new Set([...elements.repositoryOptions.querySelectorAll("input:checked")].map((input) => input.value));
  elements.repositoryOptions.replaceChildren();
  for (const repository of state.bootstrap?.repositories || []) {
    if (repository.id === elements.newAgentRepository.value) continue;
    const label = document.createElement("label");
    const input = document.createElement("input");
    input.type = "checkbox";
    input.name = "additionalRepository";
    input.value = repository.id;
    input.checked = selected.has(repository.id);
    input.addEventListener("change", updateAdditionalRepositoryLimit);
    const text = document.createElement("span");
    text.textContent = repository.title;
    label.append(input, text);
    elements.repositoryOptions.append(label);
  }
  updateAdditionalRepositoryLimit();
}

function updateAdditionalRepositoryLimit() {
  const inputs = [...elements.repositoryOptions.querySelectorAll("input")];
  const atLimit = inputs.filter((input) => input.checked).length >= 7;
  for (const input of inputs) input.disabled = state.createBusy || (atLimit && !input.checked);
}

function isEligibleSource(agent) {
  return agent?.canCopyPlacement === true
    || agent?.placementCopyEligible === true
    || agent?.launchEligible === true;
}

function selectedStartMode() {
  return elements.startModes.find((input) => input.checked)?.value || "repository";
}

function syncLaunchMode() {
  const mode = selectedStartMode();
  const repositoryMode = mode === "repository";
  elements.repositoryStartFields.hidden = !repositoryMode;
  elements.agentStartFields.hidden = repositoryMode;
  elements.newAgentRepository.required = repositoryMode;
  elements.sourceAgent.required = !repositoryMode;
  updateLaunchSummary();
  updateCreateAvailability();
}

function openCreateSheet() {
  populateLaunchOptions();
  setReceipt(elements.createReceipt, "", "");
  if (!elements.createSheet.open) elements.createSheet.showModal();
  requestAnimationFrame(() => elements.closeCreate.focus());
}

function closeCreateSheet() {
  if (elements.createSheet.open) elements.createSheet.close();
}

function updateLaunchSummary() {
  const workspace = elements.newAgentWorkspace.selectedOptions[0]?.textContent || "the selected workspace";
  if (selectedStartMode() === "agent") {
    const source = elements.sourceAgent.selectedOptions[0]?.textContent;
    elements.launchSummary.textContent = source
      ? `Copies ${source} into private worktrees in ${workspace}. The first task is queued before start.`
      : "Choose an existing agent to copy into private worktrees.";
  } else {
    const repository = elements.newAgentRepository.selectedOptions[0]?.textContent;
    elements.launchSummary.textContent = repository
      ? `Creates a private ${repository} worktree in ${workspace}. The first task is queued before start.`
      : "Choose a repository. Galpon will create a private worktree and queue the first task before start.";
  }
}

async function createAgent(event) {
  event.preventDefault();
  if (elements.submitCreate.disabled) return;
  const input = {
    workspaceId: elements.newAgentWorkspace.value,
    title: elements.newAgentTitle.value.trim(),
    role: elements.newAgentRole.value.trim(),
    prompt: elements.newAgentPrompt.value.trim(),
  };
  if (selectedStartMode() === "agent") {
    input.sourceAgentId = elements.sourceAgent.value;
  } else {
    input.repositoryIds = [
      elements.newAgentRepository.value,
      ...elements.repositoryOptions.querySelectorAll("input:checked"),
    ].map((value) => typeof value === "string" ? value : value.value).filter(Boolean);
  }
  if (!input.workspaceId || !input.title || !input.prompt || (!input.sourceAgentId && !input.repositoryIds?.length)) {
    setReceipt(elements.createReceipt, "error", "Workspace, starting point, name, and first task are required.");
    return;
  }

  setCreateDisabled(true);
  setReceipt(elements.createReceipt, "pending", "Creating private worktrees and starting the agent…");
  const attempt = mutationAttempt(state.createAttempt, input);
  state.createAttempt = attempt;
  try {
    const value = await api.createAgent(input, attempt.key);
    state.createAttempt = null;
    const createdId = value?.agent?.id || value?.id;
    const startPending = value?.startPending === true;
    setReceipt(
      elements.createReceipt,
      startPending ? "error" : "success",
      startPending
        ? "Task saved. Pi could not start; open Galpon on the desktop or retry later."
        : "Agent created. Its first task is queued.",
    );
    scheduleInvalidation();
    showToast(
      startPending
        ? "Task saved. Pi could not start; open Galpon on the desktop or retry later."
        : `${input.title} is starting.`,
      startPending ? "warning" : "success",
    );
    elements.createForm.reset();
    populateLaunchOptions();
    closeCreateSheet();
    if (createdId) openAgent(createdId);
  } catch (error) {
    if (isDefiniteMutationRejection(error)) state.createAttempt = null;
    setReceipt(elements.createReceipt, "error", error.message || "The agent could not be created.");
  } finally {
    setCreateDisabled(false);
  }
}

function setCreateDisabled(disabled) {
  state.createBusy = disabled;
  for (const control of elements.createForm.elements) control.disabled = disabled;
  if (!disabled) populateLaunchOptions();
  updateCreateAvailability();
}

function updateCreateAvailability() {
  if (state.createBusy) {
    elements.submitCreate.disabled = true;
    return;
  }
  const hasWorkspace = (state.bootstrap?.workspaces || []).length > 0;
  const modeAvailable = selectedStartMode() === "agent"
    ? [...elements.sourceAgent.options].some((option) => option.value)
    : (state.bootstrap?.repositories || []).length > 0;
  elements.submitCreate.disabled = !hasWorkspace || !modeAvailable;
}

function showAgents({ updateHistory = true } = {}) {
  stopAudioRecording({ discard: true });
  state.detailController?.abort();
  state.selected = null;
  elements.detailScreen.hidden = true;
  elements.agentsScreen.hidden = false;
  document.body.dataset.view = "agents";
  elements.timeline.replaceChildren();
  elements.statuslineDelegated.hidden = true;
  document.title = "Galpón Companion";
  renderAgents();
  if (updateHistory) history.pushState({}, "", `${location.pathname}${location.search}`);
  requestAnimationFrame(() => elements.agentsHeading.focus());
}

function setReceipt(element, receiptState, text) {
  element.dataset.state = receiptState;
  element.textContent = text;
}

function showToast(text, toastState = "success") {
  const toast = document.createElement("div");
  toast.className = "toast";
  toast.dataset.state = toastState;
  toast.textContent = text;
  elements.toastRegion.replaceChildren(toast);
  setTimeout(() => {
    if (toast.isConnected) toast.remove();
  }, 4_000);
}

function normalizeStatus(value) {
  const status = String(value || "stopped").toLocaleLowerCase();
  if (status === "working") return "running";
  if (status === "ready") return "idle";
  return status;
}

function statusLabel(status) {
  const labels = {
    idle: "Ready",
    running: "Working",
    starting: "Starting",
    stopped: "Stopped",
    failed: "Failed",
    attention: "Needs you",
    blocked: "Blocked",
    needs_input: "Needs you",
  };
  return labels[status] || humanizeKind(status);
}

function connectionModeText(value) {
  if (value === "online") return "LIVE";
  if (value === "offline" || value === "error") return "OFFLINE";
  return "SYNCING";
}

function connectionStatusText() {
  return {
    online: "Host online",
    connecting: "Connecting",
    reconnecting: "Reconnecting",
    offline: "Host offline",
    error: "Connection failed",
  }[state.connection] || "Connection unknown";
}

function humanizeKind(value) {
  const text = String(value || "Activity").replaceAll("_", " ").trim();
  return text ? text[0].toLocaleUpperCase() + text.slice(1) : "Activity";
}

function relativeTime(value) {
  if (!validDate(value)) return "";
  const elapsed = Date.now() - new Date(value).getTime();
  if (elapsed < 45_000) return "now";
  if (elapsed < 60 * 60_000) return `${Math.max(1, Math.round(elapsed / 60_000))}m`;
  if (elapsed < 24 * 60 * 60_000) return `${Math.round(elapsed / (60 * 60_000))}h`;
  return `${Math.round(elapsed / (24 * 60 * 60_000))}d`;
}

function timelineTime(value) {
  if (!validDate(value)) return "";
  return new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" }).format(new Date(value));
}

function formatDate(value) {
  if (!validDate(value)) return "";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function validDate(value) {
  return value != null && !Number.isNaN(new Date(value).getTime());
}

function resizeTextarea(element) {
  element.style.height = "auto";
  element.style.height = `${Math.min(element.scrollHeight, 160)}px`;
}

function bindEvents() {
  elements.search.addEventListener("input", () => {
    state.query = elements.search.value;
    renderAgents();
  });
  for (const button of elements.filters) {
    button.addEventListener("click", () => {
      state.filter = button.dataset.filter;
      for (const item of elements.filters) {
        const active = item === button;
        item.classList.toggle("is-active", active);
        item.setAttribute("aria-pressed", String(active));
      }
      renderAgents();
    });
  }
  elements.back.addEventListener("click", () => history.back());
  elements.feedbackForm.addEventListener("submit", sendFeedback);
  elements.audioLanguage.addEventListener("click", toggleAudioLanguage);
  elements.recordAudio.addEventListener("click", toggleAudioRecording);
  elements.feedbackInput.addEventListener("input", () => resizeTextarea(elements.feedbackInput));
  elements.openCreate.addEventListener("click", openCreateSheet);
  elements.loadOlder.addEventListener("click", loadOlderDiscussion);
  elements.jumpLatest.addEventListener("click", () => {
    state.followConversation = true;
    scrollToConversationEnd();
  });
  elements.closeCreate.addEventListener("click", closeCreateSheet);
  elements.cancelCreate.addEventListener("click", closeCreateSheet);
  elements.newAgentWorkspace.addEventListener("change", updateLaunchSummary);
  elements.newAgentRepository.addEventListener("change", () => {
    renderAdditionalRepositories();
    updateLaunchSummary();
  });
  elements.sourceAgent.addEventListener("change", updateLaunchSummary);
  for (const input of elements.startModes) input.addEventListener("change", syncLaunchMode);
  elements.createForm.addEventListener("submit", createAgent);
  elements.connection.addEventListener("click", () => {
    showToast(mockMode ? "This preview uses isolated mock data." : connectionStatusText(), state.connection === "error" ? "error" : "success");
  });

  window.addEventListener("online", () => {
    setConnection("connecting");
    scheduleInvalidation();
    startEventStream();
  });
  window.addEventListener("offline", () => setConnection("offline", "Your device has no network connection."));
  elements.timelineScroll.addEventListener("scroll", () => {
    state.followConversation = isNearConversationEnd();
    if (state.followConversation) elements.jumpLatest.hidden = true;
  }, { passive: true });
  window.addEventListener("popstate", routeFromLocation);
  window.addEventListener("beforeunload", () => {
    stopAudioRecording({ discard: true });
    state.streamClose?.();
  });
}

function routeFromLocation() {
  const hash = new URLSearchParams(location.hash.replace(/^#/, ""));
  const id = hash.get("agent");
  if (id) openAgent(id, { updateHistory: false });
  else showAgents({ updateHistory: false });
}

async function init() {
  bindEvents();
  if (!navigator.onLine) setConnection("offline", "Your device has no network connection.");
  await loadBootstrap({ initial: true });
  const hash = new URLSearchParams(location.hash.replace(/^#/, ""));
  const id = hash.get("agent");
  if (id) openAgent(id, { updateHistory: false });
}

init();
