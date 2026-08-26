import { normalizeDirectOperations, normalizeObservedActivity } from "./work-state.mjs";

function text(value, fallback, limit) {
  return String(value || fallback).replace(/[\p{Cc}\p{Cf}]/gu, "").slice(0, limit).trim() || fallback;
}

const coordinationKinds = new Set([
  "message", "target_operation", "source_operation", "join", "result", "result_delivery",
  "request_receipt", "result_receipt", "blocker_receipt", "control_receipt", "resume", "todo_link", "todo_settlement",
]);
const coordinationStates = new Set([
  "queued", "delivered", "completed", "failed", "canceled", "expired", "ready", "claimed", "running", "waiting", "settling", "settled",
  "open", "acknowledged", "detached", "pending", "presented", "abandoned", "applied", "legacy_suppressed_unknown",
]);

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
  const resultStages = new Set(["result_ready", "result_projected", "delivery_queued", "delivery_claimed", "delivery_completed", "delivery_failed", "result_suppressed", "receipt_claimed", "receipt_presented", "receipt_acknowledged", "result_recorded", "legacy_suppressed_unknown"]);
  const result = value?.result?.source === "observed" && resultStages.has(value?.result?.stage) ? {
    stage: value.result.stage,
    label: text(value.result.label, "Durable result fact", 240),
    source: "observed",
    observedAt: Number(value.result.observedAt || 0),
    lease: text(value.result.lease, "none", 40),
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
    checkpoint,
    result,
    coordination: value?.coordination?.version === 2 ? {
      version: 2,
      facts: (Array.isArray(value.coordination.facts) ? value.coordination.facts : []).slice(0, 24).flatMap((fact) => (
        coordinationKinds.has(fact?.kind) && coordinationStates.has(fact?.state) ? [{
          kind: fact.kind,
          state: fact.state,
          count: Math.max(1, Math.trunc(Number(fact.count || 1))),
          observedAt: Number(fact.observedAt || 0),
        }] : []
      )),
      truncated: value.coordination.truncated === true,
    } : null,
    timeline: (Array.isArray(value?.timeline) ? value.timeline : []).slice(0, 12).map((fact) => ({
      kind: text(fact?.kind, "fact", 80),
      label: text(fact?.label, "Observed update", 240),
      source: fact?.source === "reported" ? "reported" : "observed",
      createdAt: Number(fact?.createdAt || 0),
    })),
    children: depth >= 15 ? [] : (Array.isArray(value?.children) ? value.children : []).slice(0, 128).map((child) => normalizeItem(child, depth + 1)),
  };
}

function normalizeDelivery(delivery) {
  if (!delivery?.observation) return null;
  return {
    title: text(delivery.title, "Delegated work", 240),
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

export function normalizeWorkspaceOperations(value) {
  const summary = value?.summary || {};
  return {
    version: Number(value?.version || 0),
    workspace: {
      id: text(value?.workspace?.id, "workspace", 200),
      title: text(value?.workspace?.title, "Workspace", 240),
    },
    summary: {
      ...Object.fromEntries(["agents", "activeAgents", "activeWork", "waitingWork", "queuedWork", "reportedBlockers", "staleObservations", "recentFailures", "recentCompletions", "resumeQueued", "todoPending", "todoApplied", "legacySuppressedUnknown"]
        .map((key) => [key, Math.max(0, Math.trunc(Number(summary[key] || 0)))])),
      workCountsExact: summary.workCountsExact === true,
    },
    queue: Object.fromEntries(["inboundQueued", "inboundClaimed", "inboundClaimedFresh", "resultsReady", "resultDeliveries", "resultClaims", "receiptsClaimed", "receiptsPresented", "receiptsAcknowledged"]
      .map((key) => [key, Math.max(0, Math.trunc(Number(value?.queue?.[key] || 0)))])),
    agents: (Array.isArray(value?.agents) ? value.agents : []).slice(0, 128).map((agent) => ({
      id: text(agent?.id, "agent", 200),
      title: text(agent?.title, "Agent", 240),
      role: agent?.role ? text(agent.role, "", 240) : "",
      status: text(agent?.status, "stopped", 40),
      currentDelivery: normalizeDelivery(agent?.currentDelivery),
      observedDelivery: normalizeDelivery(agent?.observedDelivery),
    })),
    work: (Array.isArray(value?.work) ? value.work : []).slice(0, 64).map((item) => normalizeItem(item)),
    directOperations: normalizeDirectOperations(value?.directOperations),
    activity: value?.activity?.version === 1 ? {
      version: 1,
      facts: (Array.isArray(value.activity.facts) ? value.activity.facts : []).slice(0, 64).map((fact) => {
        const activity = normalizeObservedActivity(fact);
        return activity;
      }).filter(Boolean),
      truncation: {
        truncated: value.activity.truncation?.truncated === true,
        maxFacts: Math.max(0, Math.trunc(Number(value.activity.truncation?.maxFacts || 0))),
        factsOmitted: Math.max(0, Math.trunc(Number(value.activity.truncation?.factsOmitted || 0))),
        omissionExact: value.activity.truncation?.omissionExact === true,
      },
    } : null,
    timeline: (Array.isArray(value?.timeline) ? value.timeline : []).slice(0, 128).map((fact) => ({
      workId: text(fact?.workId, "work", 200),
      workTitle: text(fact?.workTitle, "Delegated work", 240),
      targetTitle: text(fact?.targetTitle, "Agent", 240),
      kind: text(fact?.kind, "fact", 80),
      label: text(fact?.label, "Observed update", 240),
      source: fact?.source === "reported" ? "reported" : "observed",
      createdAt: Number(fact?.createdAt || 0),
    })),
    truncation: {
      truncated: value?.truncation?.truncated === true,
      sourceTruncated: value?.truncation?.sourceTruncated === true,
      agentsOmissionExact: value?.truncation?.agentsOmissionExact === true,
      rootsOmissionExact: value?.truncation?.rootsOmissionExact === true,
      itemsOmissionExact: value?.truncation?.itemsOmissionExact === true,
      timelineOmissionExact: value?.truncation?.timelineOmissionExact === true,
    },
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
