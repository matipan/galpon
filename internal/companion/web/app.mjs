import { CompanionAPI, isDefiniteMutationRejection, mutationAttempt } from "./api.mjs";
import { MockCompanionAPI } from "./mock-api.mjs";
import { mergeOlderDetail, mergeRefreshedDetail } from "./detail-state.mjs";

const $ = (selector) => document.querySelector(selector);
const params = new URLSearchParams(location.search);
const mockMode = params.get("mock") === "1";
const api = mockMode ? new MockCompanionAPI() : new CompanionAPI();

const elements = {
  connection: $("#connection-control"),
  connectionLabel: $("#connection-label"),
  networkBanner: $("#network-banner"),
  networkTitle: $("#network-title"),
  networkCopy: $("#network-copy"),
  agentsScreen: $("#agents-screen"),
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
  timeline: $("#timeline"),
  timelineEmpty: $("#timeline-empty"),
  loadOlder: $("#load-older"),
  back: $("#back-to-agents"),
  feedbackForm: $("#feedback-form"),
  feedbackInput: $("#feedback-input"),
  sendFeedback: $("#send-feedback"),
  feedbackReceipt: $("#feedback-receipt"),
  createSheet: $("#create-sheet"),
  createForm: $("#create-form"),
  closeCreate: $("#close-create"),
  cancelCreate: $("#cancel-create"),
  sourceAgent: $("#source-agent"),
  newAgentTitle: $("#new-agent-title"),
  newAgentRole: $("#new-agent-role"),
  newAgentPrompt: $("#new-agent-prompt"),
  launchSummary: $("#launch-summary"),
  createReceipt: $("#create-receipt"),
  submitCreate: $("#submit-create"),
  statuslinePrimary: $("#statusline-primary"),
  statuslineSecondary: $("#statusline-secondary"),
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
    populateSourceAgents();
    setConnection("online");
    if (initial) startEventStream();
  } catch (error) {
    if (error?.name === "AbortError") return;
    elements.agentsLoading.hidden = true;
    if (!state.bootstrap) {
      state.bootstrap = { workspaces: [] };
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
  return {
    ...value,
    workspaces: workspaces.map((workspace) => ({
      id: String(workspace?.id || ""),
      title: String(workspace?.title || "Unknown workspace"),
      agents: (Array.isArray(workspace?.agents) ? workspace.agents : []).map((agent) => ({
        ...agent,
        id: String(agent?.id || ""),
        title: String(agent?.title || "Untitled agent"),
        role: String(agent?.role || ""),
        status: normalizeStatus(agent?.status),
        lastActivity: String(agent?.lastActivity || ""),
      })),
    })),
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
    const agents = workspace.agents.filter((agent) => {
      const titleMatch = !query
        || agent.title.toLocaleLowerCase().includes(query)
        || workspace.title.toLocaleLowerCase().includes(query);
      return titleMatch && matchesFilter(agent, state.filter);
    });
    if (!agents.length) continue;
    visibleCount += agents.length;

    const section = document.createElement("section");
    section.className = "workspace-group";
    section.dataset.workspaceId = workspace.id;
    const heading = document.createElement("h2");
    heading.className = "workspace-title";
    heading.textContent = workspace.title;
    section.append(heading);

    const list = document.createElement("ul");
    list.className = "agent-list";
    for (const agent of agents) list.append(renderAgentRow(workspace, agent));
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
      elements.agentsEmptyCopy.textContent = "Create and prepare an agent on the desktop first.";
    }
  }

  elements.statuslinePrimary.textContent = `${visibleCount} AGENT${visibleCount === 1 ? "" : "S"}`;
  elements.statuslineSecondary.textContent = mockMode ? "Mock data · isolated" : connectionStatusText();
}

function renderAgentRow(workspace, agent) {
  const item = document.createElement("li");
  const button = document.createElement("button");
  button.type = "button";
  button.className = "agent-row";
  button.dataset.agentId = agent.id;
  button.setAttribute("aria-label", `${agent.title}, ${statusLabel(agent.status)}, ${workspace.title}`);

  const mark = document.createElement("span");
  mark.className = "status-mark";
  mark.dataset.status = agent.status;
  mark.setAttribute("aria-hidden", "true");

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

  button.append(mark, copy, time);
  button.addEventListener("click", () => openAgent(agent.id));
  item.append(button);
  return item;
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
  if (updateHistory) history.pushState({ agentId: id }, "", `#agent=${encodeURIComponent(id)}`);
  elements.agentsScreen.hidden = true;
  elements.detailScreen.hidden = false;
  elements.detailLoading.hidden = false;
  elements.timelineEmpty.hidden = true;
  elements.loadOlder.hidden = true;
  elements.feedbackReceipt.textContent = "";
  elements.statuslinePrimary.textContent = "DISCUSSION";
  loadAgent(id);
  requestAnimationFrame(() => elements.back.focus());
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
  };
}

function renderDetail() {
  if (!state.selected) return;
  const { agent, timeline } = state.selected;
  const hadTimeline = elements.timeline.childElementCount > 0;
  const nearBottom = window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 140;

  elements.detailWorkspace.textContent = agent.workspaceTitle.toLocaleUpperCase();
  elements.detailTitle.textContent = agent.title;
  elements.detailRole.textContent = agent.role;
  elements.detailRole.hidden = !agent.role;
  elements.detailState.dataset.status = agent.status;
  elements.detailState.querySelector("span:last-child").textContent = statusLabel(agent.status);
  elements.detailState.setAttribute("aria-label", `Agent status: ${statusLabel(agent.status)}`);
  document.title = `${agent.title} · Galpón`;

  const reduced = reduceTimeline(timeline);
  elements.timeline.replaceChildren(...reduced.map(renderTimelineItem));
  elements.timelineEmpty.hidden = reduced.length !== 0;
  elements.loadOlder.hidden = !state.selected.hasMore;
  elements.loadOlder.disabled = false;
  elements.statuslineSecondary.textContent = `${agent.workspaceTitle} · ${statusLabel(agent.status)}`;

  if (hadTimeline && nearBottom) {
    requestAnimationFrame(() => window.scrollTo({ top: document.documentElement.scrollHeight }));
  } else if (!hadTimeline) {
    window.scrollTo({ top: 0 });
  }
}

export function reduceTimeline(source) {
  // The server merges synthetic delivery rows without changing the durable Pi
  // stream order. Preserve that order; Pi message timestamps are not monotonic
  // across live text deltas and final events.
  const events = [...(Array.isArray(source) ? source : [])]
    .filter((value) => value && typeof value === "object");
  const items = [];
  const tools = new Map();
  let assistant = null;
  let lastAssistant = null;
  let assistantSegments = [];

  for (const raw of events) {
    const event = {
      seq: Number(raw.seq || 0),
      eventId: String(raw.eventId || `event-${raw.seq || items.length}`),
      kind: String(raw.kind || "event"),
      role: String(raw.role || ""),
      content: raw.content == null ? "" : String(raw.content),
      toolName: String(raw.toolName || ""),
      toolCallId: String(raw.toolCallId || ""),
      isDelta: raw.isDelta === true,
      isError: raw.isError === true,
      state: String(raw.state || ""),
      createdAt: raw.createdAt || "",
    };
    const kind = event.kind.toLocaleLowerCase();

    if (kind.startsWith("delivery_") && event.role === "user") {
      const delivery = messageItem(event, "user");
      delivery.state = kind.slice("delivery_".length);
      items.push(delivery);
      continue;
    }

    if (kind === "user_message" || (kind === "message" && event.role === "user")) {
      items.push(messageItem(event, "user"));
      continue;
    }

    if (kind === "assistant_message_start") {
      assistant = messageItem(event, "assistant");
      assistant.state = event.state || "running";
      lastAssistant = assistant;
      assistantSegments = [assistant];
      items.push(assistant);
      continue;
    }

    if (kind.includes("text_delta") && (event.role === "assistant" || kind.startsWith("assistant"))) {
      if (!assistant) {
        assistant = messageItem(event, "assistant");
        assistant.state = event.state || "running";
        lastAssistant = assistant;
        assistantSegments.push(assistant);
        items.push(assistant);
      } else {
        applyContent(assistant, event);
        updateItem(assistant, event);
      }
      continue;
    }

    if (kind === "assistant_message_end") {
      if (!assistant && event.content) {
        assistant = messageItem(event, "assistant");
        lastAssistant = assistant;
        items.push(assistant);
      } else if (assistant) {
        applyContent(assistant, event);
        updateItem(assistant, event);
      }
      const finalState = event.isError ? "failed" : event.state || "completed";
      for (const segment of assistantSegments) segment.state = finalState;
      if (!assistantSegments.length && lastAssistant) lastAssistant.state = finalState;
      assistant = null;
      assistantSegments = [];
      continue;
    }

    if ((kind === "assistant_message" || kind === "message") && event.role === "assistant") {
      lastAssistant = messageItem(event, "assistant");
      items.push(lastAssistant);
      assistant = null;
      assistantSegments = [];
      continue;
    }

    if (kind.startsWith("tool_execution_") || kind === "tool_call" || kind === "tool_result" || event.role === "tool") {
      if (kind.endsWith("start") || kind === "tool_call") {
        if (assistant && !assistant.content) {
          items.splice(items.indexOf(assistant), 1);
          assistantSegments = assistantSegments.filter((segment) => segment !== assistant);
          if (lastAssistant === assistant) lastAssistant = null;
        }
        assistant = null;
      }
      const key = event.toolCallId || `tool-${event.eventId}`;
      let tool = tools.get(key);
      if (!tool) {
        tool = {
          id: key,
          seq: event.seq,
          kind: kind.includes("result") ? "tool_result" : "tool_call",
          role: "tool",
          toolName: event.toolName || "Tool",
          toolCallId: event.toolCallId,
          input: "",
          output: "",
          state: event.state || (kind.endsWith("start") ? "running" : ""),
          createdAt: event.createdAt,
          updatedAt: event.createdAt,
        };
        tools.set(key, tool);
        items.push(tool);
      }
      tool.toolName = event.toolName || tool.toolName;
      tool.updatedAt = event.createdAt || tool.updatedAt;
      tool.state = event.state || tool.state;
      if (kind.endsWith("start") || kind === "tool_call") {
        tool.input = event.content;
        if (!tool.state) tool.state = "running";
      } else {
        if (event.content) {
          tool.output = event.isDelta ? tool.output + event.content : event.content;
        }
        if (kind.endsWith("end") || kind === "tool_result") {
          tool.state = event.isError ? "failed" : event.state || "completed";
        }
      }
      continue;
    }

    if (event.content || event.state || kind.startsWith("agent_")) {
      items.push({
        id: event.eventId,
        seq: event.seq,
        kind: event.kind,
        role: event.role || "system",
        content: event.content || humanizeKind(event.kind),
        state: event.state,
        createdAt: event.createdAt,
        updatedAt: event.createdAt,
      });
    }
  }
  return items;
}

function messageItem(event, role) {
  return {
    id: event.eventId,
    seq: event.seq,
    kind: "message",
    role,
    content: event.content,
    state: event.state,
    createdAt: event.createdAt,
    updatedAt: event.createdAt,
  };
}

function applyContent(item, event) {
  if (!event.content) return;
  item.content = event.isDelta || !item.content ? item.content + event.content : event.content;
}

function updateItem(item, event) {
  item.updatedAt = event.createdAt || item.updatedAt;
  item.state = event.state || item.state;
}

function renderTimelineItem(item) {
  const row = document.createElement("li");
  row.className = "timeline-item";
  row.dataset.kind = item.kind;
  row.dataset.role = item.role;
  if (item.state) row.dataset.state = item.state;

  const rail = document.createElement("div");
  rail.className = "timeline-rail";
  rail.setAttribute("aria-hidden", "true");
  const node = document.createElement("span");
  node.className = "timeline-node";
  rail.append(node);

  const body = document.createElement("article");
  body.className = "timeline-content";
  const meta = document.createElement("div");
  meta.className = "timeline-meta";
  const label = document.createElement("span");
  label.textContent = timelineLabel(item);
  const time = document.createElement("time");
  const date = item.updatedAt || item.createdAt;
  time.dateTime = validDate(date) ? new Date(date).toISOString() : "";
  time.textContent = timelineTime(date);
  if (date) time.title = formatDate(date);
  meta.append(label, time);
  body.append(meta);

  if (item.role === "tool") {
    const details = document.createElement("details");
    details.className = "tool-output";
    details.open = item.state === "running" || item.state === "failed";
    const summary = document.createElement("summary");
    const toolState = item.state === "failed" ? "Failed" : item.state;
    summary.textContent = `${item.state === "failed" ? "Failure · " : ""}${item.toolName || "Tool"}${toolState ? ` · ${toolState}` : ""}`;
    const output = document.createElement("pre");
    const parts = [];
    if (item.input) parts.push(`Input\n${item.input}`);
    if (item.output) parts.push(`Output\n${item.output}`);
    output.textContent = parts.join("\n\n") || "No output was recorded.";
    details.append(summary, output);
    body.append(details);
  } else {
    const text = document.createElement("p");
    text.className = "discussion-text";
    text.textContent = item.content || (item.state === "running" ? "Agent is responding…" : "");
    body.append(text);
  }

  if (item.state && item.role !== "tool") {
    const eventState = document.createElement("span");
    eventState.className = "event-state";
    eventState.dataset.state = item.state;
    eventState.textContent = item.state;
    body.append(eventState);
  }

  row.append(rail, body);
  return row;
}

function timelineLabel(item) {
  if (item.role === "user") return "You";
  if (item.role === "assistant") return "Agent";
  if (item.role === "tool") return `Tool · ${item.toolName || "activity"}`;
  return humanizeKind(item.kind);
}

async function loadOlderDiscussion() {
  const selected = state.selected;
  if (!selected?.hasMore || elements.loadOlder.disabled) return;
  elements.loadOlder.disabled = true;
  const oldHeight = document.documentElement.scrollHeight;
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
    requestAnimationFrame(() => window.scrollBy({ top: document.documentElement.scrollHeight - oldHeight }));
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

  elements.sendFeedback.disabled = true;
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
    elements.feedbackInput.focus();
  }
}

function deliveryReceipt(value) {
  const delivery = String(value).toLocaleLowerCase();
  if (["steered", "current", "current_turn"].includes(delivery)) return "Sent to the current turn.";
  if (["delivered", "received"].includes(delivery)) return "The agent received your feedback.";
  return "Queued for the agent’s next safe point.";
}

function populateSourceAgents() {
  const previous = elements.sourceAgent.value;
  elements.sourceAgent.replaceChildren();
  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = "Choose a prepared agent";
  elements.sourceAgent.append(placeholder);

  let count = 0;
  for (const workspace of state.bootstrap?.workspaces || []) {
    const eligible = workspace.agents.filter(isEligibleSource);
    if (!eligible.length) continue;
    const group = document.createElement("optgroup");
    group.label = workspace.title;
    for (const agent of eligible) {
      const option = document.createElement("option");
      option.value = agent.id;
      option.textContent = agent.title;
      option.dataset.workspaceTitle = workspace.title;
      option.dataset.agentTitle = agent.title;
      group.append(option);
      count++;
    }
    elements.sourceAgent.append(group);
  }

  if ([...elements.sourceAgent.options].some((option) => option.value === previous)) {
    elements.sourceAgent.value = previous;
  }
  elements.sourceAgent.disabled = count === 0;
  elements.submitCreate.disabled = count === 0;
  updateLaunchSummary();
}

function isEligibleSource(agent) {
  return agent?.canCopyPlacement === true
    || agent?.placementCopyEligible === true
    || agent?.launchEligible === true;
}

function openCreateSheet() {
  populateSourceAgents();
  setReceipt(elements.createReceipt, "", "");
  if (!elements.createSheet.open) elements.createSheet.showModal();
  requestAnimationFrame(() => {
    if (elements.sourceAgent.disabled) elements.closeCreate.focus();
    else elements.sourceAgent.focus();
  });
}

function closeCreateSheet() {
  if (elements.createSheet.open) elements.createSheet.close();
}

function updateLaunchSummary() {
  const option = elements.sourceAgent.selectedOptions[0];
  elements.launchSummary.replaceChildren();
  const title = document.createElement("strong");
  const copy = document.createElement("span");
  if (!option?.value) {
    title.textContent = elements.sourceAgent.disabled ? "Desktop setup required" : "Private setup";
    copy.textContent = elements.sourceAgent.disabled
      ? "Prepare a managed-worktree agent on the desktop before you launch from this phone."
      : "Choose a source agent to prepare this launch.";
  } else {
    title.textContent = `Private copy of ${option.dataset.agentTitle || option.textContent}`;
    copy.textContent = `${option.dataset.workspaceTitle || "Workspace"} · fresh conversation · existing files are not shared`;
  }
  elements.launchSummary.append(title, copy);
}

async function createAgent(event) {
  event.preventDefault();
  if (elements.submitCreate.disabled) return;
  const input = {
    sourceAgentId: elements.sourceAgent.value,
    title: elements.newAgentTitle.value.trim(),
    role: elements.newAgentRole.value.trim(),
    prompt: elements.newAgentPrompt.value.trim(),
  };
  if (!input.sourceAgentId || !input.title || !input.prompt) {
    setReceipt(elements.createReceipt, "error", "Source agent, name, and task are required.");
    return;
  }

  setCreateDisabled(true);
  setReceipt(elements.createReceipt, "pending", "Creating a private setup and starting Pi…");
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
    populateSourceAgents();
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
  for (const control of elements.createForm.elements) control.disabled = disabled;
  elements.submitCreate.disabled = disabled || ![...elements.sourceAgent.options].some((option) => option.value);
}

function showAgents({ updateHistory = true } = {}) {
  state.detailController?.abort();
  state.selected = null;
  elements.detailScreen.hidden = true;
  elements.agentsScreen.hidden = false;
  elements.timeline.replaceChildren();
  document.title = "Galpón Companion";
  renderAgents();
  if (updateHistory) history.pushState({}, "", `${location.pathname}${location.search}`);
  requestAnimationFrame(() => elements.search.focus());
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
  elements.feedbackInput.addEventListener("input", () => resizeTextarea(elements.feedbackInput));
  elements.openCreate.addEventListener("click", openCreateSheet);
  elements.loadOlder.addEventListener("click", loadOlderDiscussion);
  elements.closeCreate.addEventListener("click", closeCreateSheet);
  elements.cancelCreate.addEventListener("click", closeCreateSheet);
  elements.sourceAgent.addEventListener("change", updateLaunchSummary);
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
  window.addEventListener("popstate", routeFromLocation);
  window.addEventListener("beforeunload", () => state.streamClose?.());
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
