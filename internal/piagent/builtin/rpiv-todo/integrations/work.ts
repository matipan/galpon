import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export const GALPON_WORK_SNAPSHOT_EVENT = "galpon:work:snapshot:v1";

export type WorkState = "queued" | "started" | "completed" | "failed" | "canceled" | "expired";

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
		freshnessAt?: number;
	};
	checkpoint?: {
		phase: string;
		summary: string;
		blocker?: string;
		source: "reported";
		reportedAt: number;
	};
	children?: WorkDockItem[];
}

let snapshot: WorkDockItem[] = [];

function isItem(value: unknown): value is WorkDockItem {
	if (!value || typeof value !== "object" || Array.isArray(value)) return false;
	const item = value as Record<string, any>;
	return typeof item.id === "string" && item.id.length > 0 && item.id.length <= 200
		&& typeof item.title === "string" && item.title.length > 0 && item.title.length <= 240
		&& Number.isFinite(item.createdAt) && Number.isFinite(item.updatedAt)
		&& item.observation?.source === "observed"
		&& ["queued", "started", "completed", "failed", "canceled", "expired"].includes(item.observation?.state)
		&& ["fresh", "stale", "none"].includes(item.observation?.lease)
		&& (!item.children || (Array.isArray(item.children) && item.children.length <= 128 && item.children.every(isItem)));
}

export function getWorkSnapshot(): readonly WorkDockItem[] {
	return snapshot;
}

export function registerWorkDockIntegration(pi: ExtensionAPI, refresh: () => Promise<void>): () => void {
	const off = pi.events.on(GALPON_WORK_SNAPSHOT_EVENT, (value) => {
		const event = value as { schemaVersion?: unknown; work?: unknown };
		if (event?.schemaVersion !== 1 || !Array.isArray(event.work) || event.work.length > 128 || !event.work.every(isItem)) return;
		snapshot = event.work;
		void refresh();
	});
	return () => {
		off();
		snapshot = [];
	};
}
