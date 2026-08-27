import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export const GALPON_TODO_OPERATION_SNAPSHOT_EVENT = "galpon:todo:operation-snapshot:v1";
export const RPIV_TODO_MUTATION_EVENT = "rpiv-todo:mutation:v1";

export type TodoMutationEvent = {
	action: "create" | "update" | "delete" | "clear";
	taskId?: number;
	finalStatus?: "pending" | "in_progress" | "completed" | "deleted";
	effect: "changed" | "no_change" | "rejected";
};

const MAX_ACTIVE_OPERATION_TASKS = 256;
let activeTaskIds = new Set<number>();
let ownershipKnowledge: "exact" | "unknown" = "unknown";

/**
 * Return the Pi-local TODO IDs associated with the operation that is currently
 * running in this Pi session. No operation identifier crosses this local event
 * boundary.
 */
export function getActivePiOperationTaskIds(): ReadonlySet<number> {
	return activeTaskIds;
}

export function isPiOperationOwnershipExact(): boolean {
	return ownershipKnowledge === "exact";
}

export function registerActivePiOperationIntegration(pi: ExtensionAPI, refresh: () => Promise<void>): () => void {
	const off = pi.events.on(GALPON_TODO_OPERATION_SNAPSHOT_EVENT, (value) => {
		const event = value as { schemaVersion?: unknown; activeTaskIds?: unknown; ownershipKnowledge?: unknown };
		if (event?.schemaVersion !== 1 || !Array.isArray(event.activeTaskIds)) return;
		const next = new Set<number>();
		for (const value of event.activeTaskIds.slice(0, MAX_ACTIVE_OPERATION_TASKS)) {
			if (Number.isSafeInteger(value) && value > 0) next.add(Number(value));
		}
		activeTaskIds = next;
		ownershipKnowledge = event.ownershipKnowledge === "exact" ? "exact" : "unknown";
		void refresh();
	});
	return () => {
		off();
		activeTaskIds = new Set<number>();
		ownershipKnowledge = "unknown";
	};
}
