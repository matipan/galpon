const workStates = new Set(["queued", "started", "completed", "failed", "canceled", "expired"]);
const leaseStates = new Set(["fresh", "stale", "none"]);
const milestoneStates = new Set(["pending", "active", "completed", "blocked"]);
const activeStates = new Set(["queued", "started"]);
const attentionStates = new Set(["failed", "canceled", "expired"]);
const safeActivityCategories = new Set([
  "tool: read", "tool: write", "tool: edit", "tool: bash", "tool: todo", "tool: web_search",
  "tool: source_check", "tool: fetch_content", "tool: mcp", "tool: mcpScript", "tool activity", "responding", "compacting",
]);
const safeActivityStatuses = new Set(["started", "completed", "failed"]);

function boundedNumber(value, minimum = 0) {
  const number = Number(value);
  return Number.isFinite(number) ? Math.max(minimum, Math.trunc(number)) : minimum;
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
    phase: String(item.checkpoint.phase || "working").slice(0, 80),
    summary: String(item.checkpoint.summary || "").slice(0, 240),
    blocker: String(item.checkpoint.blocker || "").slice(0, 240),
    source: "reported",
    reportedAt: Number(item.checkpoint.reportedAt || 0),
    milestones: (Array.isArray(item.checkpoint.milestones) ? item.checkpoint.milestones : []).slice(0, 8).map((milestone) => ({
      label: String(milestone?.label || "Milestone").slice(0, 80),
      state: milestoneStates.has(milestone?.state) ? milestone.state : "pending",
    })),
    counts: (Array.isArray(item.checkpoint.counts) ? item.checkpoint.counts : []).slice(0, 8).map((count) => {
      const total = boundedNumber(count?.total);
      return {
        label: String(count?.label || "count").slice(0, 40),
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
    id: String(item?.id || "").slice(0, 200),
    title: String(item?.title || "Delegated work").slice(0, 240),
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
    children: depth >= 15 ? [] : (Array.isArray(item?.children) ? item.children : []).slice(0, 128).map((child) => normalizeWorkItem(child, depth + 1)),
  };
}

export function normalizeWorkItems(items) {
  return (Array.isArray(items) ? items : []).slice(0, 128).map((item) => normalizeWorkItem(item));
}

export function countWork(items) {
  return (items || []).reduce((count, item) => count + 1 + countWork(item.children), 0);
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
