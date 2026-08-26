import { normalizeObservedActivity } from "./work-state.mjs";

function text(value, fallback, limit) {
  return String(value || fallback).replace(/[\p{Cc}\p{Cf}]/gu, "").slice(0, limit).trim() || fallback;
}

function normalizeItem(value, depth = 0) {
  const observation = value?.observation || {};
  const state = text(observation.state, "unknown", 40);
  const checkpoint = value?.checkpoint?.source === "reported" ? {
    phase: text(value.checkpoint.phase, "reported", 80),
    summary: text(value.checkpoint.summary, "Reported checkpoint", 240),
    blocker: value.checkpoint.blocker ? text(value.checkpoint.blocker, "Reported blocker", 240) : "",
    source: "reported",
    reportedAt: Number(value.checkpoint.reportedAt || 0),
  } : null;
  return {
    id: text(value?.id, "work", 200),
    title: text(value?.title, "Delegated work", 240),
    targetTitle: text(value?.targetTitle || value?.title, "Agent", 240),
    priority: text(value?.priority, "unclassified", 40),
    updatedAt: Number(value?.updatedAt || 0),
    observation: {
      state,
      source: "observed",
      lease: text(observation.lease, "none", 40),
      observedAt: Number(observation.observedAt || 0),
      leaseObservedAt: Number(observation.leaseObservedAt || 0),
      freshnessAt: Number(observation.freshnessAt || 0),
      attempt: Math.max(0, Math.trunc(Number(observation.attempt || 0))),
    },
    activity: normalizeObservedActivity(value?.activity),
    checkpoint,
    timeline: (Array.isArray(value?.timeline) ? value.timeline : []).slice(0, 12).map((fact) => ({
      kind: text(fact?.kind, "fact", 80),
      label: text(fact?.label, "Observed update", 240),
      source: fact?.source === "reported" ? "reported" : "observed",
      createdAt: Number(fact?.createdAt || 0),
    })),
    children: depth >= 15 ? [] : (Array.isArray(value?.children) ? value.children : []).slice(0, 128).map((child) => normalizeItem(child, depth + 1)),
  };
}

export function normalizeWorkspaceOperations(value) {
  const summary = value?.summary || {};
  return {
    version: Number(value?.version || 0),
    workspace: {
      id: text(value?.workspace?.id, "workspace", 200),
      title: text(value?.workspace?.title, "Workspace", 240),
    },
    summary: Object.fromEntries(["agents", "activeAgents", "activeWork", "queuedWork", "reportedBlockers", "staleObservations", "recentFailures", "recentCompletions"]
      .map((key) => [key, Math.max(0, Math.trunc(Number(summary[key] || 0)))])),
    agents: (Array.isArray(value?.agents) ? value.agents : []).slice(0, 128).map((agent) => ({
      id: text(agent?.id, "agent", 200),
      title: text(agent?.title, "Agent", 240),
      role: agent?.role ? text(agent.role, "", 240) : "",
      status: text(agent?.status, "stopped", 40),
      currentDelivery: agent?.currentDelivery ? {
        title: text(agent.currentDelivery.title, "Delegated work", 240),
        observation: {
          state: text(agent.currentDelivery.observation?.state, "unknown", 40),
          source: "observed",
          lease: text(agent.currentDelivery.observation?.lease, "none", 40),
          leaseObservedAt: Number(agent.currentDelivery.observation?.leaseObservedAt || 0),
          freshnessAt: Number(agent.currentDelivery.observation?.freshnessAt || 0),
        },
        activity: normalizeObservedActivity(agent.currentDelivery.activity),
        checkpoint: agent.currentDelivery.checkpoint?.source === "reported" ? {
          summary: text(agent.currentDelivery.checkpoint.summary, "Reported checkpoint", 240),
          source: "reported",
        } : null,
      } : null,
    })),
    work: (Array.isArray(value?.work) ? value.work : []).slice(0, 64).map((item) => normalizeItem(item)),
    timeline: (Array.isArray(value?.timeline) ? value.timeline : []).slice(0, 128).map((fact) => ({
      workId: text(fact?.workId, "work", 200),
      workTitle: text(fact?.workTitle, "Delegated work", 240),
      targetTitle: text(fact?.targetTitle, "Agent", 240),
      kind: text(fact?.kind, "fact", 80),
      label: text(fact?.label, "Observed update", 240),
      source: fact?.source === "reported" ? "reported" : "observed",
      createdAt: Number(fact?.createdAt || 0),
    })),
    truncation: { truncated: value?.truncation?.truncated === true },
  };
}

export function flattenOperationsWork(items, depth = 0, output = []) {
  for (const item of items || []) {
    output.push({ item, depth });
    flattenOperationsWork(item.children, depth + 1, output);
  }
  return output;
}

export function matchesOperationsResponse({ activeWorkspaceId, generation }, request, aborted = false) {
  return !aborted && request.workspaceId === activeWorkspaceId && request.generation === generation;
}
