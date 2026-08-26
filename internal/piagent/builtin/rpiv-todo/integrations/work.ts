import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export const GALPON_WORK_SNAPSHOT_EVENT = "galpon:work:snapshot:v1";

export type WorkState = "queued" | "started" | "waiting" | "completed" | "failed" | "canceled" | "expired";
export type WorkActivityCategory = "tool: read" | "tool: write" | "tool: edit" | "tool: bash" | "tool: todo" | "tool: web_search" | "tool: source_check" | "tool: fetch_content" | "tool: mcp" | "tool: mcpScript" | "tool activity" | "responding" | "compacting";

export interface WorkDockItem {
	id: string;
	title: string;
	createdAt: number;
	updatedAt: number;
	completedAt?: number;
	observation: {
		state: WorkState;
		source: "observed";
		lease: "fresh" | "stale" | "none";
		leaseObservedAt?: number;
		freshnessAt?: number;
	};
	activity?: {
		category: WorkActivityCategory;
		status: "started" | "completed" | "failed";
		source: "observed";
		observedAt: number;
	};
	historicalReport?: {
		summary: string;
		source: "reported";
		reportedAt: number;
		current: false;
	};
	checkpoint?: {
		phase: string;
		summary: string;
		blocker?: string;
		source: "reported";
		reportedAt: number;
	};
	coordination?: {
		version: 2;
		facts: Array<{ kind: string; state: string; count: number; observedAt: number }>;
		truncated: boolean;
	};
	children?: WorkDockItem[];
}

let snapshot: WorkDockItem[] = [];
let snapshotTruncated = false;

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

function safeTitle(value: unknown): string {
	const clean = Array.from(String(value ?? "").replace(/[\p{Cc}\p{Cf}]/gu, "")).slice(0, 96).join("").trim();
	return clean || "Delegated work";
}

function normalizeItem(value: unknown, depth: number, budget: { remaining: number; truncated: boolean }): WorkDockItem | undefined {
	if (budget.remaining <= 0) {
		budget.truncated = true;
		return undefined;
	}
	if (depth > 15 || !value || typeof value !== "object" || Array.isArray(value)) return undefined;
	const item = value as Record<string, any>;
	if (typeof item.id !== "string" || item.id.length === 0 || item.id.length > 200
		|| !Number.isFinite(item.createdAt) || !Number.isFinite(item.updatedAt)
		|| item.observation?.source !== "observed"
		|| !["queued", "started", "waiting", "completed", "failed", "canceled", "expired"].includes(item.observation?.state)
		|| !["fresh", "stale", "none"].includes(item.observation?.lease)) return undefined;
	budget.remaining--;
	const checkpointValue = item.checkpoint && typeof item.checkpoint === "object" && !Array.isArray(item.checkpoint) && item.checkpoint.source === "reported"
		? {
			phase: String(item.checkpoint.phase ?? "reported").slice(0, 40),
			summary: String(item.checkpoint.summary ?? "Reported checkpoint").replace(/[\p{Cc}\p{Cf}]/gu, "").slice(0, 240),
			blocker: item.checkpoint.blocker === undefined ? undefined : String(item.checkpoint.blocker).replace(/[\p{Cc}\p{Cf}]/gu, "").slice(0, 240),
			source: "reported" as const,
			reportedAt: Number.isFinite(item.checkpoint.reportedAt) ? Number(item.checkpoint.reportedAt) : Number(item.updatedAt),
		} : undefined;
	const historicalValue = checkpointValue === undefined
		? (Array.isArray(item.timeline) ? item.timeline : []).slice(-12).reverse().find((event: any) => event?.source === "reported" && event?.kind === "checkpoint")
		: undefined;
	const historicalReport = historicalValue ? {
		summary: String(historicalValue.label ?? "Historical report").replace(/[\p{Cc}\p{Cf}]/gu, "").slice(0, 240),
		source: "reported" as const,
		reportedAt: Number.isFinite(historicalValue.createdAt) ? Number(historicalValue.createdAt) : Number(item.updatedAt),
		current: false as const,
	} : undefined;
	const activityValue = item.activity && typeof item.activity === "object" && !Array.isArray(item.activity)
		&& item.activity.source === "observed" && safeActivityCategories.has(item.activity.category)
		&& safeActivityStatuses.has(item.activity.status) && Number.isFinite(item.activity.observedAt)
		? {
			category: item.activity.category as WorkActivityCategory,
			status: item.activity.status as "started" | "completed" | "failed",
			source: "observed" as const,
			observedAt: Number(item.activity.observedAt),
		} : undefined;
	const coordination = item.coordination?.version === 2 && Array.isArray(item.coordination.facts) ? {
		version: 2 as const,
		facts: item.coordination.facts.slice(0, 24).flatMap((fact: any) => (
			safeCoordinationKinds.has(fact?.kind) && safeCoordinationStates.has(fact?.state)
				? [{ kind: String(fact.kind), state: String(fact.state), count: Math.max(1, Math.trunc(Number(fact.count || 1))), observedAt: Number(fact.observedAt || 0) }]
				: []
		)),
		truncated: item.coordination.truncated === true,
	} : undefined;
	const children = (Array.isArray(item.children) ? item.children : [])
		.slice(0, 128)
		.map((child) => normalizeItem(child, depth + 1, budget))
		.filter((child): child is WorkDockItem => child !== undefined);
	return {
		id: item.id,
		title: safeTitle(item.title),
		createdAt: Number(item.createdAt),
		updatedAt: Number(item.updatedAt),
		completedAt: Number.isFinite(item.completedAt) ? Number(item.completedAt) : undefined,
		observation: {
			state: item.observation.state,
			source: "observed",
			lease: item.observation.lease,
			leaseObservedAt: Number.isFinite(item.observation.leaseObservedAt) ? Number(item.observation.leaseObservedAt) : undefined,
			freshnessAt: Number.isFinite(item.observation.freshnessAt) ? Number(item.observation.freshnessAt) : undefined,
		},
		activity: activityValue,
		historicalReport,
		checkpoint: checkpointValue,
		coordination,
		children,
	};
}

export function getWorkSnapshot(): readonly WorkDockItem[] {
	return snapshot;
}

export function isWorkSnapshotTruncated(): boolean {
	return snapshotTruncated;
}

export function registerWorkDockIntegration(pi: ExtensionAPI, refresh: () => Promise<void>): () => void {
	const off = pi.events.on(GALPON_WORK_SNAPSHOT_EVENT, (value) => {
		const event = value as { schemaVersion?: unknown; work?: unknown; truncated?: unknown };
		if (event?.schemaVersion !== 1 || !Array.isArray(event.work)) return;
		const budget = { remaining: 256, truncated: false };
		snapshot = event.work.slice(0, 128)
			.map((item) => normalizeItem(item, 0, budget))
			.filter((item): item is WorkDockItem => item !== undefined);
		snapshotTruncated = event.truncated === true || budget.truncated;
		void refresh();
	});
	return () => {
		off();
		snapshot = [];
		snapshotTruncated = false;
	};
}
