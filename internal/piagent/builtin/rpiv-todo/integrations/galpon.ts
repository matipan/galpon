import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { applyTaskMutation } from "../state/state-reducer.js";
import { RPIV_TODO_MUTATION_EVENT, type TodoMutationEvent } from "./operations.js";
import type { TaskState } from "../state/state.js";
import type { Task, TaskDetails } from "../tool/types.js";

export const GALPON_LINK_EVENT = "galpon:todo:link:v1";
export const GALPON_SETTLE_EVENT = "galpon:todo:settle:v1";
export const GALPON_ACK_EVENT = "rpiv-todo:galpon:ack:v1";
export const GALPON_STATE_ENTRY = "rpiv-todo:galpon-state:v1";
export const GALPON_METADATA_KEY = "galponDelegations";

export type GalponCompletionPolicy = "annotate" | "complete_on_success";
export type GalponOutcome = "succeeded" | "failed";

export interface GalponLinkEvent {
	schemaVersion: 1;
	sessionId: string;
	messageId: string;
	todoId: number;
	operationId: string;
	policy?: GalponCompletionPolicy;
	agentId?: string;
	agentTitle?: string;
}

export interface GalponSettleEvent {
	schemaVersion: 1;
	sessionId: string;
	messageId: string;
	operationId: string;
	outcome: GalponOutcome;
	resultMessageId?: string;
	summary?: string;
}

export interface GalponAckEvent {
	schemaVersion: 1;
	operationId: string;
	sessionId: string;
	phase: "link" | "settle";
	status: "applied" | "duplicate" | "rejected";
	todoId?: number;
	error?: string;
}

interface GalponDelegation {
	messageId: string;
	policy: GalponCompletionPolicy;
	agentId?: string;
	agentTitle?: string;
	outcome?: GalponOutcome;
	resultMessageId?: string;
	summary?: string;
}

interface GalponEntryData extends TaskDetails {
	integration: "galpon";
	operationId: string;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
	return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function delegationMap(task: Task): Record<string, GalponDelegation> {
	const raw = objectValue(task.metadata?.[GALPON_METADATA_KEY]);
	if (!raw) return {};
	const result: Record<string, GalponDelegation> = {};
	for (const [messageId, value] of Object.entries(raw)) {
		const item = objectValue(value);
		if (!item || item.messageId !== messageId) continue;
		const policy = item.policy === "annotate" ? "annotate" : "complete_on_success";
		result[messageId] = {
			messageId,
			policy,
			...(typeof item.agentId === "string" ? { agentId: item.agentId } : {}),
			...(typeof item.agentTitle === "string" ? { agentTitle: item.agentTitle } : {}),
			...(item.outcome === "succeeded" || item.outcome === "failed" ? { outcome: item.outcome } : {}),
			...(typeof item.resultMessageId === "string" ? { resultMessageId: item.resultMessageId } : {}),
			...(typeof item.summary === "string" ? { summary: item.summary } : {}),
		};
	}
	return result;
}

// This selector stays inside Pi. It reads only the Pi-local TODO snapshot and
// does not send subjects, snapshots, or derived readiness to Galpón.
export function hasUnsettledGalponDelegation(task: Task): boolean {
	return Object.values(delegationMap(task)).some((delegation) => delegation.outcome === undefined);
}

function validBase(value: unknown): value is {
	schemaVersion: 1;
	sessionId: string;
	messageId: string;
	operationId: string;
} {
	const item = objectValue(value);
	return (
		item?.schemaVersion === 1 &&
		typeof item.sessionId === "string" &&
		item.sessionId.length > 0 &&
		typeof item.messageId === "string" &&
		item.messageId.length > 0 &&
		typeof item.operationId === "string" &&
		item.operationId.length > 0
	);
}

export function applyGalponLink(state: TaskState, event: GalponLinkEvent): { state: TaskState; status: "applied" | "duplicate" | "rejected"; error?: string } {
	if (!validBase(event) || !Number.isInteger(event.todoId) || event.todoId <= 0) {
		return { state, status: "rejected", error: "invalid link payload" };
	}
	const target = state.tasks.find((task) => task.id === event.todoId);
	if (!target || target.status === "deleted") return { state, status: "rejected", error: `#${event.todoId} not found` };
	for (const task of state.tasks) {
		const existing = delegationMap(task)[event.messageId];
		if (!existing) continue;
		if (task.id === event.todoId) return { state, status: "duplicate" };
		return { state, status: "rejected", error: "delegation is linked to another task" };
	}
	const delegations = delegationMap(target);
	delegations[event.messageId] = {
		messageId: event.messageId,
		policy: event.policy === "annotate" ? "annotate" : "complete_on_success",
		...(event.agentId ? { agentId: event.agentId } : {}),
		...(event.agentTitle ? { agentTitle: event.agentTitle } : {}),
	};
	const applied = applyTaskMutation(state, "update", {
		id: target.id,
		metadata: { [GALPON_METADATA_KEY]: delegations },
	});
	if (applied.op.kind === "error") return { state, status: "rejected", error: applied.op.message };
	return { state: applied.state, status: "applied" };
}

export function applyGalponSettle(state: TaskState, event: GalponSettleEvent): { state: TaskState; status: "applied" | "duplicate" | "rejected"; todoId?: number; error?: string } {
	if (!validBase(event) || (event.outcome !== "succeeded" && event.outcome !== "failed")) {
		return { state, status: "rejected", error: "invalid settle payload" };
	}
	for (const task of state.tasks) {
		const delegations = delegationMap(task);
		const existing = delegations[event.messageId];
		if (!existing) continue;
		if (task.status === "deleted") return { state, status: "rejected", todoId: task.id, error: `#${task.id} is deleted` };
		if (existing.outcome) {
			if (existing.outcome === event.outcome) return { state, status: "duplicate", todoId: task.id };
			return { state, status: "rejected", todoId: task.id, error: "delegation already settled with another outcome" };
		}
		delegations[event.messageId] = {
			...existing,
			outcome: event.outcome,
			...(event.resultMessageId ? { resultMessageId: event.resultMessageId } : {}),
			...(event.summary ? { summary: event.summary.slice(0, 1000) } : {}),
		};
		const shouldComplete = event.outcome === "succeeded" && existing.policy === "complete_on_success" && (task.status === "pending" || task.status === "in_progress");
		const applied = applyTaskMutation(state, "update", {
			id: task.id,
			...(shouldComplete ? { status: "completed" as const } : {}),
			metadata: { [GALPON_METADATA_KEY]: delegations },
		});
		if (applied.op.kind === "error") return { state, status: "rejected", todoId: task.id, error: applied.op.message };
		return { state: applied.state, status: "applied", todoId: task.id };
	}
	return { state, status: "rejected", error: "delegation is not linked to a task" };
}

export function registerGalponIntegration(
	pi: ExtensionAPI,
	options: {
		currentSessionId: () => string;
		getState: (sessionId: string) => TaskState;
		commitState: (sessionId: string, state: TaskState) => void;
		refresh: () => Promise<void>;
	},
): () => void {
	const persist = (sessionId: string, operationId: string, state: TaskState, mutation: TodoMutationEvent) => {
		const data: GalponEntryData = {
			action: "update",
			params: {},
			tasks: state.tasks,
			nextId: state.nextId,
			integration: "galpon",
			operationId,
		};
		pi.appendEntry(GALPON_STATE_ENTRY, data);
		options.commitState(sessionId, state);
		pi.events.emit(RPIV_TODO_MUTATION_EVENT, mutation);
		void options.refresh();
	};
	const acknowledge = (ack: GalponAckEvent) => pi.events.emit(GALPON_ACK_EVENT, ack);
	const offLink = pi.events.on(GALPON_LINK_EVENT, (value) => {
		const event = value as GalponLinkEvent;
		const sessionId = options.currentSessionId();
		if (!validBase(event) || event.sessionId !== sessionId) return;
		const result = applyGalponLink(options.getState(sessionId), event);
		if (result.status === "applied") {
			const task = result.state.tasks.find((item) => item.id === event.todoId);
			persist(sessionId, event.operationId, result.state, { action: "update", taskId: event.todoId, finalStatus: task?.status ?? "pending", effect: "changed" });
		}
		acknowledge({
			schemaVersion: 1,
			operationId: event.operationId,
			sessionId,
			phase: "link",
			status: result.status,
			...(result.status !== "rejected" ? { todoId: event.todoId } : {}),
			...(result.error ? { error: result.error } : {}),
		});
	});
	const offSettle = pi.events.on(GALPON_SETTLE_EVENT, (value) => {
		const event = value as GalponSettleEvent;
		const sessionId = options.currentSessionId();
		if (!validBase(event) || event.sessionId !== sessionId) return;
		const result = applyGalponSettle(options.getState(sessionId), event);
		if (result.status === "applied") {
			const task = result.state.tasks.find((item) => item.id === result.todoId);
			persist(sessionId, event.operationId, result.state, { action: "update", taskId: result.todoId, finalStatus: task?.status ?? "pending", effect: "changed" });
		}
		acknowledge({
			schemaVersion: 1,
			operationId: event.operationId,
			sessionId,
			phase: "settle",
			status: result.status,
			...(result.todoId ? { todoId: result.todoId } : {}),
			...(result.error ? { error: result.error } : {}),
		});
	});
	return () => {
		offLink();
		offSettle();
	};
}
