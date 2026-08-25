const workStates = new Set(["queued", "started", "completed", "failed", "canceled", "expired"]);
const leaseStates = new Set(["fresh", "stale", "none"]);

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
      state: String(milestone?.state || "pending"),
    })),
    counts: (Array.isArray(item.checkpoint.counts) ? item.checkpoint.counts : []).slice(0, 8).map((count) => ({
      label: String(count?.label || "count").slice(0, 40),
      completed: Number(count?.completed || 0),
      total: Number(count?.total || 0),
    })),
  } : null;
  return {
    id: String(item?.id || "").slice(0, 200),
    title: String(item?.title || "Delegated work").slice(0, 240),
    createdAt: Number(item?.createdAt || 0),
    updatedAt: Number(item?.updatedAt || 0),
    observation: { state, lease, source: "observed" },
    checkpoint,
    children: depth >= 15 ? [] : (Array.isArray(item?.children) ? item.children : []).slice(0, 128).map((child) => normalizeWorkItem(child, depth + 1)),
  };
}

export function normalizeWorkItems(items) {
  return (Array.isArray(items) ? items : []).slice(0, 128).map((item) => normalizeWorkItem(item));
}

export function countWork(items) {
  return (items || []).reduce((count, item) => count + 1 + countWork(item.children), 0);
}
