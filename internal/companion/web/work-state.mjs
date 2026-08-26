const workStates = new Set(["queued", "started", "waiting", "completed", "failed", "canceled", "expired"]);
const leaseStates = new Set(["fresh", "stale", "none"]);
const milestoneStates = new Set(["pending", "active", "completed", "blocked"]);
const activeStates = new Set(["queued", "started", "waiting"]);
const attentionStates = new Set(["failed", "canceled", "expired"]);
const directOperationStates = new Set(["ready", "started", "waiting", "completed", "failed", "canceled", "expired"]);
const safeActivityCategories = new Set([
  "tool: read", "tool: write", "tool: edit", "tool: bash", "tool: todo", "tool: web_search",
  "tool: source_check", "tool: fetch_content", "tool: mcp", "tool: mcpScript", "tool activity", "responding", "compacting",
]);
const safeActivityStatuses = new Set(["started", "completed", "failed"]);
const safeCoordinationKinds = new Set([
  "message", "target_operation", "source_operation", "join", "result", "result_delivery",
  "request_receipt", "result_receipt", "blocker_receipt", "control_receipt", "resume", "todo_link", "todo_settlement",
]);
const safeCoordinationStates = new Set([
  "queued", "delivered", "completed", "failed", "canceled", "expired", "ready", "claimed", "running", "waiting", "settling", "settled",
  "open", "acknowledged", "detached", "pending", "presented", "abandoned", "applied", "legacy_suppressed_unknown",
]);

function boundedNumber(value, minimum = 0) {
  const number = Number(value);
  return Number.isFinite(number) ? Math.max(minimum, Math.trunc(number)) : minimum;
}

function safeText(value, fallback, maximum) {
  return String(value || fallback).replace(/[\p{Cc}\p{Cf}]/gu, "").slice(0, maximum);
}

export function normalizeObservedActivity(activity) {
  if (activity?.source !== "observed" || !safeActivityCategories.has(activity?.category)
      || !safeActivityStatuses.has(activity?.status) || !Number.isFinite(Number(activity?.observedAt))) return null;
  return {
    category: activity.category,
    status: activity.status,
    source: "observed",
    observedAt: Number(activity.observedAt),
  };
}

export function normalizeWorkItem(item, depth = 0) {
  const state = workStates.has(item?.observation?.state) ? item.observation.state : "failed";
  const lease = leaseStates.has(item?.observation?.lease) ? item.observation.lease : "none";
  const checkpoint = item?.checkpoint?.source === "reported" ? {
    phase: safeText(item.checkpoint.phase, "working", 80),
    summary: safeText(item.checkpoint.summary, "", 240),
    blocker: safeText(item.checkpoint.blocker, "", 240),
    source: "reported",
    reportedAt: Number(item.checkpoint.reportedAt || 0),
    milestones: (Array.isArray(item.checkpoint.milestones) ? item.checkpoint.milestones : []).slice(0, 8).map((milestone) => ({
      label: safeText(milestone?.label, "Milestone", 80),
      state: milestoneStates.has(milestone?.state) ? milestone.state : "pending",
    })),
    counts: (Array.isArray(item.checkpoint.counts) ? item.checkpoint.counts : []).slice(0, 8).map((count) => {
      const total = boundedNumber(count?.total);
      return {
        label: safeText(count?.label, "count", 40),
        completed: Math.min(boundedNumber(count?.completed), total),
        total,
      };
    }),
  } : null;
  const historicalValue = !checkpoint
    ? (Array.isArray(item?.timeline) ? item.timeline : []).slice(-12).reverse().find((event) => event?.source === "reported" && event?.kind === "checkpoint")
    : null;
  const historicalReport = historicalValue ? {
    summary: String(historicalValue.label || "Historical report").replace(/[\p{Cc}\p{Cf}]/gu, "").slice(0, 240),
    source: "reported",
    reportedAt: Number(historicalValue.createdAt || 0),
    current: false,
  } : null;
  return {
    id: safeText(item?.id, "", 200),
    title: safeText(item?.title, "Delegated work", 240),
    createdAt: Number(item?.createdAt || 0),
    updatedAt: Number(item?.updatedAt || 0),
    observation: {
      state,
      lease,
      source: "observed",
      observedAt: Number(item?.observation?.observedAt || 0),
      leaseObservedAt: Number(item?.observation?.leaseObservedAt || 0),
      freshnessAt: Number(item?.observation?.freshnessAt || 0),
    },
    activity: normalizeObservedActivity(item?.activity),
    checkpoint,
    historicalReport,
    coordination: item?.coordination?.version === 2 ? {
      version: 2,
      facts: (Array.isArray(item.coordination.facts) ? item.coordination.facts : []).slice(0, 24).flatMap((fact) => (
        safeCoordinationKinds.has(fact?.kind) && safeCoordinationStates.has(fact?.state) ? [{
          kind: fact.kind, state: fact.state,
          count: Math.max(1, Math.trunc(Number(fact.count || 1))), observedAt: Number(fact.observedAt || 0),
        }] : []
      )),
      truncated: item.coordination.truncated === true,
    } : null,
    children: depth >= 15 ? [] : (Array.isArray(item?.children) ? item.children : []).slice(0, 128).map((child) => normalizeWorkItem(child, depth + 1)),
  };
}

export function normalizeWorkItems(items) {
  return (Array.isArray(items) ? items : []).slice(0, 128).map((item) => normalizeWorkItem(item));
}

export function normalizeDirectOperations(items) {
  return (Array.isArray(items) ? items : []).slice(0, 16).flatMap((item) => {
    const state = directOperationStates.has(item?.state) ? item.state : "";
    const lease = leaseStates.has(item?.lease) ? item.lease : "none";
    const count = Math.max(0, Math.trunc(Number(item?.count || 0)));
    if (!state || count === 0 || item?.source !== "observed") return [];
    return [{
      title: safeText(item?.title, "Direct Pi work", 96),
      state,
      source: "observed",
      lease,
      count,
      observedAt: Number(item?.observedAt || 0),
    }];
  });
}

export function countWork(items) {
  return (items || []).reduce((count, item) => count + 1 + countWork(item.children), 0);
}

export function selectPrimaryWork(items) {
  const candidates = [];
  const visit = (values, depth = 0) => {
    for (const item of values || []) {
      const hasBlocker = Boolean(item.checkpoint?.blocker);
      const state = item.observation.state;
      const score = hasBlocker ? 500
        : state === "started" ? 400
          : state === "waiting" ? 350
            : state === "queued" ? 300
            : attentionStates.has(state) ? 200
              : state === "completed" ? 100 : 0;
      candidates.push({ item, depth, score, updatedAt: Number(item.updatedAt || item.createdAt || 0) });
      visit(item.children, depth + 1);
    }
  };
  visit(items);
  candidates.sort((left, right) => right.score - left.score || right.updatedAt - left.updatedAt || left.depth - right.depth);
  return candidates[0]?.item || null;
}

export function summarizeWork(items) {
  const summary = { total: 0, active: 0, attention: 0, completed: 0, stale: 0 };
  const visit = (values) => {
    for (const item of values || []) {
      summary.total += 1;
      if (activeStates.has(item.observation.state)) summary.active += 1;
      if (attentionStates.has(item.observation.state) || item.checkpoint?.blocker) summary.attention += 1;
      if (item.observation.state === "completed") summary.completed += 1;
      if (item.observation.lease === "stale") summary.stale += 1;
      visit(item.children);
    }
  };
  visit(items);
  return summary;
}
