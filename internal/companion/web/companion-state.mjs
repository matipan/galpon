const draftPrefix = "galpon.feedback-draft.";

export function draftStorageKey(agentId) {
  return `${draftPrefix}${String(agentId || "")}`;
}

export function readAgentDraft(storage, agentId) {
  if (!storage || !agentId) return "";
  try {
    return String(storage.getItem(draftStorageKey(agentId)) || "");
  } catch {
    return "";
  }
}

export function writeAgentDraft(storage, agentId, value) {
  if (!storage || !agentId) return;
  try {
    const draft = String(value || "");
    if (draft) storage.setItem(draftStorageKey(agentId), draft);
    else storage.removeItem(draftStorageKey(agentId));
  } catch {
    // A draft can stay in the current control when storage is unavailable.
  }
}

export function invalidationPlan(events, selectedAgent) {
  const values = Array.isArray(events) ? events : [];
  const selectedId = String(selectedAgent?.id || "");
  const selectedWorkspaceId = String(selectedAgent?.workspaceId || "");
  let bootstrap = false;
  let detail = false;

  for (const event of values) {
    const retryScope = String(event?.retryScope || "");
    const agentId = String(event?.agentId || "");
    const workspaceId = String(event?.workspaceId || "");
    const global = !agentId && !workspaceId;
    if (retryScope !== "detail") bootstrap = true;
    if (retryScope !== "bootstrap" && selectedId && (global || agentId === selectedId || (!agentId && workspaceId === selectedWorkspaceId))) {
      detail = true;
    }
  }

  return { bootstrap, detail };
}

export function matchesDetailPage(current, request, currentGeneration) {
  if (!current || !request) return false;
  const before = current.conversationHasMore ? current.before : 0;
  const messageBefore = current.messageHasMore ? current.messageBefore : "";
  return Number(currentGeneration) === Number(request.generation)
    && String(current.agent?.id || "") === String(request.agentId || "")
    && Number(before) === Number(request.before)
    && String(messageBefore) === String(request.messageBefore || "");
}

export function optimisticMessage(prompt, key, createdAt = Date.now()) {
  return {
    seq: 0,
    eventId: `optimistic:${key}`,
    clientRequestId: key,
    kind: "delivery_sending",
    role: "user",
    content: String(prompt || ""),
    state: "sending",
    localOnly: true,
    createdAt,
  };
}

export function settleOptimisticMessage(timeline, key, response) {
  const values = Array.isArray(timeline) ? timeline : [];
  const optimisticId = `optimistic:${key}`;
  const status = String(response?.status || response?.delivery || response?.message?.status || "queued");
  const message = response?.message && typeof response.message === "object" ? response.message : response;
  const messageId = String(message?.id || "");
  return values.map((event) => {
    if (event?.eventId !== optimisticId) return event;
    return {
      ...event,
      eventId: messageId ? `delivery:${messageId}:prompt` : optimisticId,
      kind: `delivery_${status}`,
      state: status,
      createdAt: message?.createdAt || event.createdAt,
    };
  });
}

export function reconcileOptimisticMessages(overlay, timeline) {
  const reconciled = new Map(overlay instanceof Map ? overlay : []);
  const durableIDs = new Set((timeline || []).map((item) => String(item?.eventId || "")));
  const durableRequestIDs = new Set((timeline || []).map((item) => String(item?.clientRequestId || "")).filter(Boolean));
  for (const [key, item] of reconciled) {
    if (durableIDs.has(String(item?.eventId || "")) || durableRequestIDs.has(key)) reconciled.delete(key);
  }
  return reconciled;
}

export function removeOptimisticMessage(timeline, key) {
  const optimisticId = `optimistic:${key}`;
  return (Array.isArray(timeline) ? timeline : []).filter((event) => event?.eventId !== optimisticId);
}
