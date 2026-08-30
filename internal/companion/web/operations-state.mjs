import { normalizeDirectOperations, normalizeObservedActivity } from "./work-state.mjs";

function text(value, fallback, limit) {
  return String(value || fallback).replace(/[\p{Cc}\p{Cf}]/gu, "").slice(0, limit).trim() || fallback;
}

const resultStages = new Set([
  "result_ready", "result_projected", "delivery_queued", "delivery_claimed", "delivery_completed",
  "delivery_failed", "result_suppressed", "receipt_claimed", "receipt_presented", "receipt_acknowledged",
  "result_recorded", "legacy_suppressed_unknown",
]);

function normalizeItem(value) {
  const observation = value?.observation || {};
  const checkpoint = value?.checkpoint?.source === "reported" ? {
    phase: text(value.checkpoint.phase, "reported", 80),
    summary: text(value.checkpoint.summary, "Reported checkpoint", 240),
    blocker: value.checkpoint.blocker ? text(value.checkpoint.blocker, "Reported blocker", 240) : "",
    source: "reported",
    reportedAt: Number(value.checkpoint.reportedAt || 0),
  } : null;
  const result = value?.result?.source === "observed" && resultStages.has(value.result.stage) ? {
    stage: value.result.stage,
    label: text(value.result.label, "Durable result fact", 240),
    source: "observed",
    observedAt: Number(value.result.observedAt || 0),
    lease: text(value.result.lease, "none", 40),
  } : null;
  return {
    id: text(value?.id, "work", 200),
    title: text(value?.title, "Work", 240),
    targetTitle: text(value?.targetTitle || value?.title, "Agent", 240),
    delegatorTitle: text(value?.delegatorTitle, "Agent", 240),
    direction: value?.direction === "delegated" ? "delegated" : "received",
    priority: text(value?.priority, "unclassified", 40),
    updatedAt: Number(value?.updatedAt || 0),
    observation: {
      state: text(observation.state, "unknown", 40),
      source: "observed",
      lease: text(observation.lease, "none", 40),
      observedAt: Number(observation.observedAt || 0),
      leaseObservedAt: Number(observation.leaseObservedAt || 0),
      freshnessAt: Number(observation.freshnessAt || 0),
      attempt: Math.max(0, Math.trunc(Number(observation.attempt || 0))),
    },
    checkpoint,
    result,
    timeline: (Array.isArray(value?.timeline) ? value.timeline : []).slice(0, 12).map((fact) => ({
      kind: text(fact?.kind, "fact", 80),
      label: text(fact?.label, "Observed update", 240),
      source: fact?.source === "reported" ? "reported" : "observed",
      createdAt: Number(fact?.createdAt || 0),
    })),
  };
}

function normalizeDelivery(delivery) {
  if (!delivery?.observation) return null;
  return {
    title: text(delivery.title, "Work", 240),
    observation: {
      state: text(delivery.observation.state, "unknown", 40),
      source: "observed",
      lease: text(delivery.observation.lease, "none", 40),
      observedAt: Number(delivery.observation.observedAt || 0),
      leaseObservedAt: Number(delivery.observation.leaseObservedAt || 0),
      freshnessAt: Number(delivery.observation.freshnessAt || 0),
    },
    checkpoint: delivery.checkpoint?.source === "reported" ? {
      summary: text(delivery.checkpoint.summary, "Reported checkpoint", 240),
      source: "reported",
    } : null,
  };
}

function normalizeItems(value, limit) {
  return (Array.isArray(value) ? value : []).slice(0, limit).map(normalizeItem);
}

export function normalizeAgentOperations(value) {
  const summary = value?.summary || {};
  return {
    version: Number(value?.version || 0),
    agent: {
      id: text(value?.agent?.id, "agent", 200),
      title: text(value?.agent?.title, "Agent", 240),
      role: value?.agent?.role ? text(value.agent.role, "", 240) : "",
      status: text(value?.agent?.status, "stopped", 40),
      currentDelivery: normalizeDelivery(value?.agent?.currentDelivery),
      observedDelivery: normalizeDelivery(value?.agent?.observedDelivery),
    },
    workspace: {
      id: text(value?.workspace?.id, "workspace", 200),
      title: text(value?.workspace?.title, "Workspace", 240),
    },
    summary: Object.fromEntries(["received", "delegated", "current", "needsAttention", "results", "failures"]
      .map((key) => [key, Math.max(0, Math.trunc(Number(summary[key] || 0)))])),
    queue: Object.fromEntries(["inboundQueued", "inboundClaimed", "inboundClaimedFresh", "resultsReady", "resultDeliveries", "resultClaims", "receiptsClaimed", "receiptsPresented", "receiptsAcknowledged"]
      .map((key) => [key, Math.max(0, Math.trunc(Number(value?.queue?.[key] || 0)))])),
    current: normalizeItems(value?.current, 64),
    attention: normalizeItems(value?.attention, 32),
    recentResults: normalizeItems(value?.recentResults, 32),
    directOperations: normalizeDirectOperations(value?.directOperations),
    activity: value?.activity?.version === 1 ? {
      version: 1,
      facts: (Array.isArray(value.activity.facts) ? value.activity.facts : []).slice(0, 64).map(normalizeObservedActivity).filter(Boolean),
      truncation: {
        truncated: value.activity.truncation?.truncated === true,
        factsOmitted: Math.max(0, Math.trunc(Number(value.activity.truncation?.factsOmitted || 0))),
      },
    } : null,
    recentCoordination: (Array.isArray(value?.recentCoordination) ? value.recentCoordination : []).slice(0, 64).map((fact) => ({
      workId: text(fact?.workId, "work", 200),
      workTitle: text(fact?.workTitle, "Work", 240),
      targetTitle: text(fact?.targetTitle, "Agent", 240),
      kind: text(fact?.kind, "fact", 80),
      label: text(fact?.label, "Observed update", 240),
      source: fact?.source === "reported" ? "reported" : "observed",
      createdAt: Number(fact?.createdAt || 0),
    })),
    truncation: {
      truncated: value?.truncation?.truncated === true,
      sourceTruncated: value?.truncation?.sourceTruncated === true,
      currentOmitted: Math.max(0, Math.trunc(Number(value?.truncation?.currentOmitted || 0))),
      attentionOmitted: Math.max(0, Math.trunc(Number(value?.truncation?.attentionOmitted || 0))),
      recentResultsOmitted: Math.max(0, Math.trunc(Number(value?.truncation?.recentResultsOmitted || 0))),
      recentCoordinationOmitted: Math.max(0, Math.trunc(Number(value?.truncation?.recentCoordinationOmitted || 0))),
    },
  };
}

export function flattenOperationsWork(value) {
  const output = [];
  const seen = new Set();
  for (const [section, items] of [["Current", value?.current], ["Attention", value?.attention], ["Recent results", value?.recentResults]]) {
    for (const item of items || []) {
      if (seen.has(item.id)) continue;
      seen.add(item.id);
      output.push({ item, section });
    }
  }
  return output;
}

export function matchesOperationsResponse({ activeAgentId, generation }, request, aborted = false) {
  return !aborted && request.agentId === activeAgentId && request.generation === generation;
}
