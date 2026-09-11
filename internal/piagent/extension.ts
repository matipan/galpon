import { createHash, randomUUID } from "node:crypto";
import { request as httpRequest } from "node:http";
import { unwatchFile, watchFile } from "node:fs";
import { dirname, join } from "node:path";
import { StringEnum, Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Key, matchesKey, truncateToWidth, visibleWidth } from "@earendil-works/pi-tui";
import {
	ReviewMode,
	compileReview,
	isReviewColumnBoundary,
	maxReviewBlocks,
	maxReviewDraftBytes,
	maxReviewItems,
	maxReviewSelectionBytes,
	maxReviewSourceBytes,
	legacyReviewOffset,
	legacyReviewSelection,
	parseReviewBlocks,
	parseReviewBuffer,
	reviewDraftEvent,
	reviewParserVersion,
	reviewSelection,
	sanitizeReviewText,
	type ReviewAction,
	type ReviewEditingDraft,
	type ReviewItem,
	type ReviewViewState,
} from "./galpon-review.ts";

type JSONValue = Record<string, any> | any[] | string | number | boolean | null;

type ConversationEventKind =
	| "user_message"
	| "assistant_message_start"
	| "assistant_text_delta"
	| "assistant_message_end"
	| "tool_execution_start"
	| "tool_execution_update"
	| "tool_execution_end"
	| "compaction_start"
	| "compaction_end";

type PendingConversationEvent = {
	eventId: string;
	kind: ConversationEventKind;
	piEntryId?: string;
	role?: "user" | "assistant";
	content?: string;
	toolName?: string;
	toolCallId?: string;
	isDelta?: boolean;
	isError?: boolean;
	images?: Array<{ mimeType: string; data: string; name?: string }>;
	createdAt: number;
};

type ConversationEvent = PendingConversationEvent & { runtimeSeq: number };

const maxPendingConversationEvents = 512;
const maxConversationBatchEvents = 50;
const maxConversationBatchBytes = 30 * 1024 * 1024;
const maxConversationContentBytes = 64 * 1024;
// One request per Pi turn keeps each durable response correlated to one request.
const maxDeliveryBatchMessages = 1;
const maxDeliveryResponseBytes = 512 * 1024;
const delegatedStatusPollMs = 3_000;
const todoLinkEvent = "galpon:todo:link:v1";
const todoSettleEvent = "galpon:todo:settle:v1";
const todoAckEvent = "rpiv-todo:galpon:ack:v1";
const todoOperationSnapshotEvent = "galpon:todo:operation-snapshot:v1";
const todoMutationEvent = "rpiv-todo:mutation:v1";
const workSnapshotEvent = "galpon:work:snapshot:v1";
const maxTodoOperationAssociations = 256;
type TodoMutationEvent = {
	action: "create" | "update" | "delete" | "clear";
	taskId?: number;
	finalStatus?: "pending" | "in_progress" | "completed" | "deleted";
	effect: "changed" | "no_change" | "rejected";
};

const socketPath = process.env.GALPON_SOCKET ?? "";
const agentId = process.env.GALPON_AGENT_ID ?? "";
const agentTitle = process.env.GALPON_AGENT_TITLE ?? "Agent";
const agentRole = process.env.GALPON_AGENT_ROLE ?? "";
const workspaceTitle = process.env.GALPON_WORKSPACE_TITLE ?? "Workspace";
const workspaceId = process.env.GALPON_WORKSPACE_ID ?? "";
const placement = process.env.GALPON_PLACEMENT ?? "";
const runtimeId = process.env.GALPON_RUNTIME_ID ?? "";
const extensionPath = process.env.GALPON_PI_EXTENSION ?? "";
const reviewExtensionPath = extensionPath ? join(dirname(extensionPath), "galpon-review.ts") : "";
const configuredProtocolGeneration = Math.max(1, Number.parseInt(process.env.GALPON_PROTOCOL_GENERATION ?? "1", 10) || 1);

type ActiveCoordinationOperation = {
	id: string;
	attempt: number;
	kind: string;
	parentMessageId: string;
	userEntryId: string;
	message?: any;
	claimId: string;
	started: boolean;
};

type CoordinationReceiptBatch = { receipts?: any[]; results?: any[] };

type PendingResultObservation = {
	operationId: string;
	operationAttempt: number;
	toolCallId: string;
	messageIds: string[];
	presented?: boolean;
};

function api(method: string, path: string, body?: JSONValue, signal?: AbortSignal): Promise<any> {
	return new Promise((resolve, reject) => {
		let settled = false;
		const succeed = (value: any) => {
			if (settled) return;
			settled = true;
			resolve(value);
		};
		const fail = (error: Error) => {
			if (settled) return;
			settled = true;
			reject(error);
		};
		const data = body === undefined ? undefined : Buffer.from(JSON.stringify(body));
		const request = httpRequest({
			method,
			path,
			socketPath,
			signal,
			headers: data ? { "content-type": "application/json", "content-length": data.length } : undefined,
		}, response => {
			const chunks: Buffer[] = [];
			response.on("data", chunk => chunks.push(Buffer.from(chunk)));
			response.on("aborted", () => fail(new Error("Galpón response was aborted")));
			response.on("error", fail);
			response.on("close", () => {
				if (!response.complete) fail(new Error("Galpón response closed before it completed"));
			});
			response.on("end", () => {
				const text = Buffer.concat(chunks).toString("utf8");
				let value: any = {};
				if (text.trim()) {
					try { value = JSON.parse(text); } catch { value = { error: text.trim() }; }
				}
				if ((response.statusCode ?? 500) < 200 || (response.statusCode ?? 500) >= 300) {
					const error = new Error(value.error ?? `Galpón returned HTTP ${response.statusCode}`);
					(error as any).statusCode = response.statusCode ?? 500;
					fail(error);
					return;
				}
				succeed(value);
			});
		});
		request.on("error", fail);
		if (data) request.write(data);
		request.end();
	});
}

function postConversationEvents(events: ConversationEvent[], signal?: AbortSignal) {
	return api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/conversation-events`, { runtimeId, events }, signal);
}

function boundedConversationContent(value: string): string {
	if (Buffer.byteLength(value) <= maxConversationContentBytes) return value;
	const kept = Buffer.from(value).subarray(0, maxConversationContentBytes - 80).toString("utf8");
	return `${kept}\n\n[Companion output truncated to ${maxConversationContentBytes} bytes]`;
}

function boundedDeliveryResponse(value: string): string {
	if (Buffer.byteLength(value) <= maxDeliveryResponseBytes) return value;
	const suffix = `\n\n[Galpón delivery response truncated to ${maxDeliveryResponseBytes} bytes]`;
	const kept = Buffer.from(value).subarray(0, maxDeliveryResponseBytes - Buffer.byteLength(suffix) - 4).toString("utf8");
	return kept + suffix;
}

function conversationEvent(kind: ConversationEventKind, fields: Omit<Partial<PendingConversationEvent>, "kind"> = {}): PendingConversationEvent {
	return {
		eventId: fields.eventId ?? randomUUID(),
		kind,
		createdAt: fields.createdAt ?? Date.now(),
		...(fields.piEntryId ? { piEntryId: fields.piEntryId } : {}),
		...(fields.role ? { role: fields.role } : {}),
		...(fields.content !== undefined ? { content: boundedConversationContent(fields.content) } : {}),
		...(fields.toolName ? { toolName: fields.toolName } : {}),
		...(fields.toolCallId ? { toolCallId: fields.toolCallId } : {}),
		...(fields.isDelta !== undefined ? { isDelta: fields.isDelta } : {}),
		...(fields.isError !== undefined ? { isError: fields.isError } : {}),
		...(fields.images?.length ? { images: fields.images } : {}),
	};
}

function readableJSON(value: any): string {
	const seen = new WeakSet<object>();
	try {
		const text = JSON.stringify(value, (key, current) => {
			if (/(?:token|password|secret|api[_-]?key|authorization|cookie)/i.test(key)) return "[redacted]";
			if (typeof current === "bigint") return current.toString();
			if (!current || typeof current !== "object") return current;
			if (seen.has(current)) return "[circular]";
			seen.add(current);
			if (current.type === "image") {
				const source = current.source && typeof current.source === "object"
					? { ...current.source, data: typeof current.source.data === "string" ? "[binary image omitted]" : current.source.data }
					: current.source;
				return { ...current, data: typeof current.data === "string" ? "[binary image omitted]" : current.data, source };
			}
			return current;
		}, 2);
		return text === undefined ? String(value ?? "") : text;
	} catch {
		return String(value ?? "");
	}
}

function normalContent(content: any): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	return content.flatMap((part: any) => {
		if (part?.type === "text" || part?.type === "output_text") return [String(part.text ?? "")];
		if (part?.type === "image") return [`[image: ${String(part.source?.mediaType ?? part.mimeType ?? "unknown type")}]`];
		return [];
	}).join("\n");
}

function normalImages(content: any): Array<{ mimeType: string; data: string; name?: string }> {
	if (!Array.isArray(content)) return [];
	return content.flatMap((part: any) => {
		if (part?.type !== "image") return [];
		const mimeType = String(part.source?.mediaType ?? part.mimeType ?? "");
		const data = String(part.source?.data ?? part.data ?? "");
		if (!mimeType.startsWith("image/") || !data) return [];
		return [{ mimeType, data, ...(part.name ? { name: String(part.name) } : {}) }];
	});
}

function conversationImages(content: any): Array<{ mimeType: string; data: string; name?: string }> {
	// A Galpón delivery already owns durable image blobs. The Companion replaces
	// its mirrored prompt with that delivery, so do not store the same bytes twice.
	if (/\[delivery [A-Za-z0-9:_-]{1,128}\]/.test(normalContent(content))) return [];
	return normalImages(content);
}

const supportedImageMimeTypes = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);

function validImageData(data: unknown): data is string {
	return typeof data === "string" && data.length > 0 && data.length % 4 === 0 && /^[A-Za-z0-9+/]+={0,2}$/.test(data);
}

// Older Galpón deliveries stored the extension input shape in the Pi session.
// Normalize it before every provider call so one bad image cannot poison later turns.
function canonicalMessageImages(message: any): any {
	if (!message || !Array.isArray(message.content)) return message;
	let changed = false;
	const content = message.content.map((part: any) => {
		if (part?.type !== "image") return part;
		const mimeType = String(part.mimeType ?? part.source?.mediaType ?? "");
		const data = part.data ?? part.source?.data;
		if (!supportedImageMimeTypes.has(mimeType) || !validImageData(data)) {
			changed = true;
			return { type: "text" as const, text: "[invalid image omitted]" };
		}
		if (part.mimeType === mimeType && part.data === data) return part;
		changed = true;
		return { type: "image" as const, mimeType, data };
	});
	return changed ? { ...message, content } : message;
}

function toolOutput(value: any): string {
	if (value && typeof value === "object" && "content" in value) return normalContent(value.content);
	return readableJSON(value);
}

function stablePiEventId(sessionId: string, entryId: string, suffix: string): string {
	return `pi:${sessionId}:${entryId}:${suffix}`;
}

function messageCreatedAt(message: any): number {
	const timestamp = Number(message?.timestamp);
	return Number.isFinite(timestamp) ? timestamp : Date.now();
}

function entryCreatedAt(entry: any): number {
	const timestamp = Date.parse(String(entry?.timestamp ?? ""));
	return Number.isFinite(timestamp) ? timestamp : Date.now();
}

function toolCallEntry(sessionManager: any, toolCallId: string): any {
	const leaf = sessionManager.getLeafEntry?.();
	if (leaf?.type === "message" && leaf.message?.role === "assistant" && Array.isArray(leaf.message.content)
		&& leaf.message.content.some((part: any) => part?.type === "toolCall" && part.id === toolCallId)) {
		return leaf;
	}
	const entries = sessionManager.getBranch();
	for (let index = entries.length - 1; index >= 0; index--) {
		const entry = entries[index];
		if (entry?.type !== "message" || entry.message?.role !== "assistant" || !Array.isArray(entry.message.content)) continue;
		if (entry.message.content.some((part: any) => part?.type === "toolCall" && part.id === toolCallId)) return entry;
	}
	return undefined;
}

function* conversationBackfill(sessionId: string, entries: any[]): Generator<PendingConversationEvent> {
	for (const entry of entries) {
		if (entry?.type === "custom_message" && entry.customType === "galpon-operation") {
			yield conversationEvent("user_message", {
				eventId: stablePiEventId(sessionId, entry.id, "galpon-operation"),
				piEntryId: entry.id,
				role: "user",
				content: normalContent(entry.content),
				images: conversationImages(entry.content),
				createdAt: entryCreatedAt(entry),
			});
			continue;
		}
		if (entry?.type === "message" && entry.message?.role === "user") {
			yield conversationEvent("user_message", {
				eventId: stablePiEventId(sessionId, entry.id, "user"),
				piEntryId: entry.id,
				role: "user",
				content: normalContent(entry.message.content),
				images: conversationImages(entry.message.content),
				createdAt: entryCreatedAt(entry),
			});
			continue;
		}
		if (entry?.type === "message" && entry.message?.role === "assistant") {
			const createdAt = entryCreatedAt(entry);
			yield conversationEvent("assistant_message_end", {
				eventId: stablePiEventId(sessionId, entry.id, "assistant"),
				piEntryId: entry.id,
				role: "assistant",
				content: normalContent(entry.message.content),
				images: normalImages(entry.message.content),
				isDelta: false,
				createdAt,
			});
			for (const [index, part] of (Array.isArray(entry.message.content) ? entry.message.content : []).entries()) {
				if (part?.type !== "toolCall") continue;
				yield conversationEvent("tool_execution_start", {
					eventId: stablePiEventId(sessionId, entry.id, `tool-start-${part.id ?? index}`),
					piEntryId: entry.id,
					content: readableJSON(part.arguments ?? {}),
					toolName: String(part.name ?? "tool"),
					toolCallId: String(part.id ?? `${entry.id}-${index}`),
					isDelta: false,
					createdAt,
				});
			}
			continue;
		}
		if (entry?.type === "message" && entry.message?.role === "toolResult") {
			yield conversationEvent("tool_execution_end", {
				eventId: stablePiEventId(sessionId, entry.id, "tool-end"),
				piEntryId: entry.id,
				content: normalContent(entry.message.content),
				images: normalImages(entry.message.content),
				toolName: String(entry.message.toolName ?? "tool"),
				toolCallId: String(entry.message.toolCallId ?? entry.id),
				isDelta: false,
				isError: Boolean(entry.message.isError),
				createdAt: entryCreatedAt(entry),
			});
			continue;
		}
		if (entry?.type === "compaction") {
			yield conversationEvent("compaction_end", {
				eventId: stablePiEventId(sessionId, entry.id, "compaction"),
				piEntryId: entry.id,
				content: String(entry.summary ?? ""),
				isDelta: false,
				createdAt: entryCreatedAt(entry),
			});
		}
	}
}

class ConversationMirror {
	private pending: PendingConversationEvent[] = [];
	private finalMessages = new WeakMap<PendingConversationEvent, { message: any; sessionManager: any; sessionId: string; suffix: string }>();
	private recoveryBackfills = new Map<any, string>();
	private backfill: Iterator<PendingConversationEvent> | undefined;
	private deferred: PendingConversationEvent | undefined;
	private retryBatches: ConversationEvent[][] = [];
	private runtimeSeq = 0;
	private sending = false;
	private stopped = false;
	private controller: AbortController | undefined;
	private timer: NodeJS.Timeout | undefined;
	private retryDelay = 250;

	startBackfill(events: Iterable<PendingConversationEvent>) {
		this.backfill = events[Symbol.iterator]();
		this.schedule(0);
	}

	enqueue(event: PendingConversationEvent) {
		if (this.stopped) return;
		const tail = this.pending[this.pending.length - 1];
		if (tail?.kind === "assistant_text_delta" && event.kind === "assistant_text_delta") {
			const combined = (tail.content ?? "") + (event.content ?? "");
			if (Buffer.byteLength(combined) <= maxConversationContentBytes) {
				tail.content = combined;
				tail.createdAt = event.createdAt;
				return;
			}
		}
		if (tail?.kind === "tool_execution_update" && event.kind === "tool_execution_update" && tail.toolCallId === event.toolCallId) {
			tail.content = event.content;
			tail.createdAt = event.createdAt;
			return;
		}
		this.pending.push(event);
		this.boundPending();
		this.schedule(40);
	}

	enqueueFinalMessage(event: PendingConversationEvent, message: any, sessionManager: any, sessionId: string, suffix: string) {
		this.finalMessages.set(event, { message, sessionManager, sessionId, suffix });
		this.enqueue(event);
	}

	private resolveFinalMessage(event: PendingConversationEvent, branchCache: Map<any, any[]>): boolean {
		const pending = this.finalMessages.get(event);
		if (!pending) return true;
		const expected = pending.message;
		const expectedTimestamp = Number(expected?.timestamp);
		let entries = branchCache.get(pending.sessionManager);
		if (entries === undefined) {
			const branch: any[] = pending.sessionManager.getBranch();
			branchCache.set(pending.sessionManager, branch);
			entries = branch;
		}
		for (let index = entries.length - 1; index >= 0; index--) {
			const entry = entries[index];
			const candidate = entry?.type === "message"
				? entry.message
				: entry?.type === "custom_message"
					? { role: "custom", customType: entry.customType, content: entry.content, details: entry.details }
					: undefined;
			if (!candidate) continue;
			const sameReference = candidate === expected;
			const sameCustomMessage = candidate.role === "custom" && expected?.role === "custom"
				&& candidate.customType === expected.customType
				&& normalContent(candidate.content) === normalContent(expected.content)
				&& candidate.details?.operationId === expected.details?.operationId
				&& candidate.details?.operationAttempt === expected.details?.operationAttempt;
			const sameValue = sameCustomMessage || candidate?.role === expected?.role
				&& Number.isFinite(expectedTimestamp)
				&& Number(candidate?.timestamp) === expectedTimestamp
				&& normalContent(candidate?.content) === normalContent(expected?.content)
				&& (expected?.role !== "toolResult" || candidate?.toolCallId === expected?.toolCallId);
			if (!sameReference && !sameValue) continue;
			event.piEntryId = entry.id;
			event.eventId = stablePiEventId(pending.sessionId, entry.id, pending.suffix);
			this.finalMessages.delete(event);
			return true;
		}
		// Pi persists final messages shortly after message_end. Do not send a
		// random event ID while that write is in progress. A later flush gets the
		// durable entry ID, and a process restart can recover it from backfill.
		return false;
	}

	stop() {
		this.stopped = true;
		if (this.timer) clearTimeout(this.timer);
		this.timer = undefined;
		this.controller?.abort();
		this.controller = undefined;
		this.pending = [];
		this.recoveryBackfills.clear();
		this.backfill = undefined;
		this.deferred = undefined;
		this.retryBatches = [];
	}

	private boundPending() {
		while (this.pending.length > maxPendingConversationEvents) {
			let index = this.pending.findIndex(event => event.kind === "assistant_text_delta" || event.kind === "tool_execution_update");
			if (index < 0) {
				index = this.pending.findIndex(event => event.kind.endsWith("_start"));
			}
			if (index < 0) index = 0;
			const dropped = this.pending[index];
			const final = this.finalMessages.get(dropped);
			if (final) this.recoveryBackfills.set(final.sessionManager, final.sessionId);
			this.pending.splice(index, 1);
		}
	}

	private schedule(delay: number) {
		if (this.stopped || this.timer || this.sending) return;
		this.timer = setTimeout(() => {
			this.timer = undefined;
			void this.flush();
		}, delay);
		this.timer.unref?.();
	}

	private nextPending(): PendingConversationEvent | undefined {
		if (this.deferred) {
			const event = this.deferred;
			this.deferred = undefined;
			return event;
		}
		if (this.backfill) {
			const next = this.backfill.next();
			if (!next.done) return next.value;
			this.backfill = undefined;
		}
		const recovery = this.recoveryBackfills.entries().next();
		if (!recovery.done) {
			const [sessionManager, sessionId] = recovery.value;
			this.recoveryBackfills.delete(sessionManager);
			this.backfill = conversationBackfill(sessionId, sessionManager.getBranch());
			return this.nextPending();
		}
		return this.pending.shift();
	}

	private takeBatch(): ConversationEvent[] {
		const batch: ConversationEvent[] = [];
		const branchCache = new Map<any, any[]>();
		let bytes = 0;
		while (batch.length < maxConversationBatchEvents) {
			const input = this.nextPending();
			if (!input) break;
			if (!this.resolveFinalMessage(input, branchCache)) {
				this.deferred = input;
				break;
			}
			const event = { ...input, runtimeSeq: this.runtimeSeq + 1 };
			const size = Buffer.byteLength(JSON.stringify(event));
			if (batch.length > 0 && bytes + size > maxConversationBatchBytes) {
				this.deferred = input;
				break;
			}
			this.runtimeSeq++;
			batch.push(event);
			bytes += size;
		}
		return batch;
	}

	private hasWork() {
		return Boolean(this.retryBatches.length > 0 || this.deferred || this.backfill || this.recoveryBackfills.size > 0 || this.pending.length > 0);
	}

	private async flush() {
		if (this.sending) return;
		const batch = this.retryBatches[0] ?? this.takeBatch();
		if (batch.length === 0) {
			if (this.hasWork()) this.schedule(100);
			return;
		}
		this.sending = true;
		const controller = new AbortController();
		this.controller = controller;
		const timeout = setTimeout(() => controller.abort(), 10_000);
		timeout.unref?.();
		try {
			await postConversationEvents(batch, controller.signal);
			if (this.retryBatches[0] === batch) this.retryBatches.shift();
			this.retryDelay = 250;
		} catch (error) {
			const status = Number((error as any)?.statusCode ?? 0);
			if (status === 400 || status === 413 || status === 422) {
				if (batch.length > 1) {
					// Keep the exact event objects and sequence numbers while a bad or
					// oversized batch is split. One invalid event must not discard the
					// other recoverable conversation events.
					const middle = Math.ceil(batch.length / 2);
					this.retryBatches.splice(0, this.retryBatches[0] === batch ? 1 : 0, batch.slice(0, middle), batch.slice(middle));
				} else if (this.retryBatches[0] === batch) {
					// A permanently invalid batch must not block later session events.
					this.retryBatches.shift();
				}
				this.retryDelay = 250;
			} else {
				if (this.retryBatches[0] !== batch) this.retryBatches.unshift(batch);
				this.retryDelay = Math.min(this.retryDelay * 2, 5000);
			}
		} finally {
			clearTimeout(timeout);
			if (this.controller === controller) this.controller = undefined;
			this.sending = false;
			if (!this.stopped && this.hasWork()) this.schedule(this.retryBatches.length > 0 ? this.retryDelay : 0);
		}
	}
}

function toolResult(value: any) {
	return {
		content: [{ type: "text" as const, text: JSON.stringify(value, null, 2) }],
		details: value,
	};
}

function isUnavailableProgressError(error: unknown): boolean {
	const status = Number((error as any)?.statusCode ?? 0);
	if (status !== 400 && status !== 422) return false;
	const message = error instanceof Error ? error.message : String(error);
	return message === "current delivery is not active for this runtime"
		|| message === "report_progress requires an active delivery"
		|| message === "report_progress requires an active delegated request delivery"
		|| message === "report_progress requires an active delegated request operation";
}

function unavailableProgressResult() {
	return {
		accepted: false,
		recorded: false,
		reason: "no_active_delegated_request",
		message: "Progress was not recorded because this turn is not an active delegated request delivery.",
	};
}

function assistantText(message: any): string {
	if (message?.role !== "assistant") return "";
	return normalContent(message.content).trim();
}

type AssistantReviewSource = {
	entryId: string;
	text: string;
	timestamp: number;
	hash: string;
};

type PersistedReviewItem = ReviewItem & { quoteHash?: string };

type PersistedReviewEditing = ReviewEditingDraft & { quoteHash: string };

type ReviewDraftSnapshotV1 = {
	version: 1;
	sourceEntryId: string;
	sourceHash: string;
	status: "open" | "prepared";
	items: ReviewItem[];
	updatedAt: number;
};

type ReviewDraftSnapshotV2 = {
	version: 2;
	parserVersion: number;
	sourceEntryId: string;
	sourceHash: string;
	sourceBytes: number;
	status: "open" | "prepared";
	items: PersistedReviewItem[];
	editing?: PersistedReviewEditing;
	updatedAt: number;
};

type ReviewDraftSnapshot = ReviewDraftSnapshotV1 | ReviewDraftSnapshotV2;

function assistantReviewSources(branch: any[]): AssistantReviewSource[] {
	const output: AssistantReviewSource[] = [];
	for (const entry of branch) {
		if (entry?.type !== "message" || entry?.message?.role !== "assistant") continue;
		if (["aborted", "error", "pending"].includes(String(entry.message.stopReason ?? ""))) continue;
		const text = assistantText(entry.message);
		if (!text) continue;
		output.push({
			entryId: String(entry.id ?? ""),
			text,
			timestamp: Number(entry.message.timestamp ?? new Date(entry.timestamp ?? 0).getTime() ?? 0),
			hash: createHash("sha256").update(text).digest("hex"),
		});
	}
	return output.filter(source => source.entryId);
}

function reviewSourceLabel(source: AssistantReviewSource, position: number): string {
	const preview = plainLabel(source.text.split("\n").find(line => line.trim()) ?? "Assistant response", "Assistant response", 72);
	const when = Number.isFinite(source.timestamp) && source.timestamp > 0
		? new Date(source.timestamp).toLocaleString()
		: "unknown time";
	return `${position + 1}. ${position === 0 ? "Latest" : "Earlier"} · ${when} · ${preview}`;
}

type RestoredReviewDraft = { items: ReviewItem[]; editing?: ReviewEditingDraft };

function reviewTextHash(value: string): string {
	return createHash("sha256").update(value).digest("hex");
}

function reviewRangeAtOffsets(lines: ReturnType<typeof parseReviewBuffer>, startOffset: number, endOffset: number) {
	const point = (offset: number) => {
		for (const line of lines) {
			if (offset <= (line.endOffset ?? 0)) return { line: line.index, column: Math.max(0, Math.min(offset - (line.startOffset ?? 0), line.text.length)) };
		}
		const last = lines[lines.length - 1];
		return { line: last?.index ?? 0, column: last?.text.length ?? 0 };
	};
	const start = point(startOffset);
	const end = point(endOffset);
	return { start: start.line, end: end.line, startColumn: start.column, endColumn: end.column };
}

function currentReviewRange(lines: ReturnType<typeof parseReviewBuffer>, raw: any) {
	const start = Number(raw?.start);
	const end = Number(raw?.end);
	const startColumn = Number(raw?.startColumn);
	const endColumn = Number(raw?.endColumn);
	if (!Number.isInteger(start) || !Number.isInteger(end) || !Number.isInteger(startColumn) || !Number.isInteger(endColumn)
		|| start < 0 || end < start || end >= lines.length || startColumn < 0 || endColumn < 0
		|| startColumn > lines[start].text.length || endColumn > lines[end].text.length
		|| !isReviewColumnBoundary(lines[start].text, startColumn) || !isReviewColumnBoundary(lines[end].text, endColumn)
		|| (start === end && startColumn >= endColumn)) return undefined;
	return { start, end, startColumn, endColumn };
}

function restoredReviewDraft(branch: any[], source: AssistantReviewSource, lines: ReturnType<typeof parseReviewBuffer>): RestoredReviewDraft {
	const legacyBlocks = parseReviewBlocks(source.text);
	for (let index = branch.length - 1; index >= 0; index--) {
		const entry = branch[index];
		if (entry?.type !== "custom" || entry?.customType !== reviewDraftEvent) continue;
		const data = entry.data as (Partial<ReviewDraftSnapshot> & Record<string, any>) | undefined;
		const version = Number(data?.version);
		if ((version !== 1 && version !== 2) || data?.sourceEntryId !== source.entryId || data.sourceHash !== source.hash) continue;
		if (version === 2 && (data.sourceBytes !== Buffer.byteLength(source.text) || ![2, reviewParserVersion].includes(Number(data.parserVersion)))) continue;
		if (!(["open", "prepared"] as string[]).includes(String(data.status ?? "")) || !Array.isArray(data.items) || data.items.length > maxReviewItems) continue;
		const legacy = version === 1 || Number(data.parserVersion) === 2;
		const restored: ReviewItem[] = [];
		const ids = new Set<string>();
		let valid = true;
		for (const item of data.items) {
			const id = String(item?.id ?? "");
			const comment = sanitizeReviewText(String(item?.comment ?? "")).trim();
			if (!id || id.length > 128 || ids.has(id) || !comment) {
				valid = false;
				break;
			}
			let range = currentReviewRange(lines, item);
			let quote = range ? reviewSelection(lines, range.start, range.end, range.startColumn, range.endColumn) : "";
			if (legacy) {
				const start = Number(item?.start);
				const end = Number(item?.end);
				if (!Number.isInteger(start) || !Number.isInteger(end) || start < 0 || end < start || end >= legacyBlocks.length) {
					valid = false;
					break;
				}
				const legacyQuote = legacyReviewSelection(legacyBlocks, start, end);
				const quoteMatches = version === 2 ? String(item?.quoteHash ?? "") === reviewTextHash(legacyQuote) : sanitizeReviewText(String(item?.quote ?? "")).trim() === legacyQuote;
				if (!quoteMatches) {
					valid = false;
					break;
				}
				range = reviewRangeAtOffsets(
					lines,
					legacyReviewOffset(source.text, legacyBlocks[start].startOffset ?? 0),
					legacyReviewOffset(source.text, legacyBlocks[end].endOffset ?? 0),
				);
				quote = reviewSelection(lines, range.start, range.end, range.startColumn, range.endColumn);
			} else if (!range || String(item?.quoteHash ?? "") !== reviewTextHash(quote)) {
				valid = false;
				break;
			}
			if (!range || !quote || Buffer.byteLength(quote) > maxReviewSelectionBytes) {
				valid = false;
				break;
			}
			ids.add(id);
			restored.push({ id, ...range, quote, comment });
		}
		if (!valid || reviewDraftBytes(restored) > maxReviewDraftBytes) continue;
		let editing: ReviewEditingDraft | undefined;
		if (version === 2 && data.editing !== undefined) {
			const raw = data.editing as Partial<PersistedReviewEditing>;
			const kind = String(raw.kind ?? "");
			const itemId = String(raw.itemId ?? "");
			const buffer = sanitizeReviewText(String(raw.buffer ?? ""));
			let range = currentReviewRange(lines, raw);
			let quote = range ? reviewSelection(lines, range.start, range.end, range.startColumn, range.endColumn) : "";
			if (legacy) {
				const start = Number(raw.start);
				const end = Number(raw.end);
				if (!Number.isInteger(start) || !Number.isInteger(end) || start < 0 || end < start || end >= legacyBlocks.length) continue;
				const legacyQuote = legacyReviewSelection(legacyBlocks, start, end);
				if (raw.quoteHash !== reviewTextHash(legacyQuote)) continue;
				range = reviewRangeAtOffsets(
					lines,
					legacyReviewOffset(source.text, legacyBlocks[start].startOffset ?? 0),
					legacyReviewOffset(source.text, legacyBlocks[end].endOffset ?? 0),
				);
				quote = reviewSelection(lines, range.start, range.end, range.startColumn, range.endColumn);
			}
			if ((kind !== "new" && kind !== "edit") || !range || !quote || (!legacy && raw.quoteHash !== reviewTextHash(quote)) || Buffer.byteLength(buffer) + reviewDraftBytes(restored) > maxReviewDraftBytes) continue;
			if (kind === "edit") {
				const item = restored.find(candidate => candidate.id === itemId);
				if (!item || item.start !== range.start || item.end !== range.end || item.startColumn !== range.startColumn || item.endColumn !== range.endColumn) continue;
				editing = { kind: "edit", itemId, ...range, buffer };
			} else editing = { kind: "new", ...range, visualMode: raw.visualMode === "character" ? "character" : "line", buffer };
		}
		return { items: restored, editing };
	}
	return { items: [] };
}

function reviewDraftBytes(items: ReviewItem[]): number {
	return Buffer.byteLength(compileReview(items));
}

type OperationsRow = { item: any; section: string };

function operationsRows(value: any): OperationsRow[] {
	const output: OperationsRow[] = [];
	const seen = new Set<string>();
	for (const [section, items] of [["CURRENT", value?.current], ["ATTENTION", value?.attention], ["RECENT RESULTS", value?.recentResults]] as const) {
		for (const item of Array.isArray(items) ? items : []) {
			const id = String(item?.id ?? "");
			if (seen.has(id)) continue;
			seen.add(id);
			output.push({ item, section });
		}
	}
	return output;
}

function operationMark(state: string): string {
	if (state === "started" || state === "running") return "◐";
	if (state === "waiting") return "◇";
	if (state === "ready" || state === "queued" || state === "starting") return "○";
	if (state === "completed" || state === "idle") return "✓";
	if (["failed", "canceled", "expired"].includes(state)) return "×";
	return "·";
}

function plainLabel(value: unknown, fallback: string, limit = 240): string {
	const text = Array.from(String(value ?? "").replace(/[\p{Cc}\p{Cf}]/gu, "")).slice(0, limit).join("").trim();
	return text || fallback;
}

function observedAge(timestamp: unknown): string {
	const elapsed = Math.max(0, Date.now() - Number(timestamp ?? 0));
	if (elapsed < 1_000) return "now";
	if (elapsed < 60_000) return `${Math.floor(elapsed / 1_000)}s ago`;
	if (elapsed < 3_600_000) return `${Math.floor(elapsed / 60_000)}m ago`;
	if (elapsed < 86_400_000) return `${Math.floor(elapsed / 3_600_000)}h ago`;
	return `${Math.floor(elapsed / 86_400_000)}d ago`;
}

function fitLine(value: string, width: number): string {
	return truncateToWidth(value, Math.max(1, width), "…");
}

function padLine(value: string, width: number): string {
	const fitted = fitLine(value, width);
	return fitted + " ".repeat(Math.max(0, width - visibleWidth(fitted)));
}

function joinOperationColumns(left: string[], right: string[], leftWidth: number, rightWidth: number): string[] {
	const lines: string[] = [];
	for (let index = 0; index < Math.max(left.length, right.length); index++) {
		lines.push(padLine(left[index] ?? "", leftWidth) + fitLine(right[index] ?? "", rightWidth));
	}
	return lines;
}

export function renderOperationsCockpit(value: any, width: number, selected: number, theme: any): string[] {
	width = Math.max(1, width);
	const rows = operationsRows(value);
	const summary = value?.summary ?? {};
	const truncated = value?.truncation?.truncated === true ? " · more facts omitted" : "";
	const header = theme.fg("accent", theme.bold(`GALPÓN  Operations · ${plainLabel(value?.agent?.title, agentTitle, 96)}`));
	const summaryLine = `${Number(summary.current ?? 0)} current · ${Number(summary.received ?? 0)} received · ${Number(summary.delegated ?? 0)} delegated · ${Number(summary.needsAttention ?? 0)} need attention · ${Number(summary.results ?? 0)} results · ${Number(summary.failures ?? 0)} failures${truncated}`;
	const outline = [theme.fg("muted", theme.bold("AGENT WORK"))];
	if (rows.length === 0) outline.push(theme.fg("dim", "No current work, attention, or recent results"));
	const visibleStart = selected >= 8 ? selected - 7 : 0;
	for (let index = visibleStart; index < rows.length && outline.length < 10; index++) {
		const row = rows[index];
		const state = String(row.item?.observation?.state ?? "unknown");
		const prefix = index === selected ? "❯ " : "  ";
		const markColor = state === "completed" ? "success" : ["failed", "canceled", "expired"].includes(state) ? "error" : ["started", "waiting"].includes(state) ? "warning" : "dim";
		const mark = theme.fg(markColor, operationMark(state));
		const label = `${prefix}${mark} ${plainLabel(row.item?.title, "Work", 96)} · ${row.section} · ${plainLabel(row.item?.direction, "work", 40)}`;
		outline.push(index === selected ? theme.fg("accent", label) : theme.fg("text", label));
	}
	const detail = [theme.fg("muted", theme.bold("SELECTED DETAIL"))];
	const item = rows[Math.min(Math.max(0, selected), Math.max(0, rows.length - 1))]?.item;
	if (!item) {
		detail.push(theme.fg("dim", "No work item is selected."));
	} else {
		const observation = item.observation ?? {};
		detail.push(theme.fg("text", theme.bold(plainLabel(item.title, "Delegated work", 96))));
		const leaseAge = observation.state === "started" && Number(observation.leaseObservedAt ?? 0) > 0 ? ` · lease observed ${observedAge(observation.leaseObservedAt)}` : "";
		detail.push(theme.fg("accent", `Observed · ${plainLabel(observation.state, "unknown", 40)} · attempt ${Number(observation.attempt ?? 0)} · lease ${plainLabel(observation.lease, "none", 40)}${leaseAge}`));
		if (item.result?.source === "observed") detail.push(theme.fg("muted", `Observed result · ${plainLabel(item.result.label, "Durable result fact")}`));
		if (item.checkpoint?.source === "reported") {
			detail.push(theme.fg("warning", `Reported · ${plainLabel(item.checkpoint.phase, "reported", 40)} · ${plainLabel(item.checkpoint.summary, "Reported checkpoint")}`));
			if (item.checkpoint.blocker) detail.push(theme.fg("error", `Reported blocker · ${plainLabel(item.checkpoint.blocker, "Reported blocker")}`));
		} else {
			detail.push(theme.fg("dim", "Reported · No current checkpoint"));
		}
		if (observation.lease === "stale") detail.push(theme.fg("warning", "A stale observation does not mean that work is stuck."));
	}
	const agents = [theme.fg("muted", theme.bold("SELECTED AGENT"))];
	for (const fact of (Array.isArray(value?.directOperations) ? value.directOperations : []).slice(0, 4)) {
		agents.push(theme.fg("text", `${operationMark(String(fact?.state ?? ""))} ${plainLabel(fact?.title, "Direct Pi work", 96)} · ${Number(fact?.count ?? 0)} direct Pi ${Number(fact?.count ?? 0) === 1 ? "operation" : "operations"} · ${plainLabel(fact?.state, "observed", 40)} · ${plainLabel(fact?.lease, "none", 40)} lease · observed ${observedAge(fact?.observedAt)}`));
	}
	for (const agent of value?.agent ? [value.agent] : []) {
		const delivery = agent?.currentDelivery ?? agent?.observedDelivery;
		const observation = delivery?.observation;
		const current = observation?.state
			? ` · ${agent?.currentDelivery ? "current" : "latest observed"} ${plainLabel(observation.state, "unknown", 40)} delivery · ${plainLabel(observation.lease, "none", 40)} lease${Number(observation.leaseObservedAt ?? 0) > 0 ? ` observed ${observedAge(observation.leaseObservedAt)}` : ""}${delivery?.checkpoint?.source === "reported" ? ` · reported: ${plainLabel(delivery.checkpoint.summary, "checkpoint")}` : ""}`
			: " · no observed delivery · no lease";
		agents.push(theme.fg("text", `${operationMark(String(agent?.status ?? ""))} ${plainLabel(agent?.title, "Agent", 96)} · ${plainLabel(agent?.status, "stopped", 40)}${current}`));
	}
	const activities = Array.isArray(value?.activity?.facts) ? value.activity.facts : [];
	if (activities.length > 0) {
		agents.push("", theme.fg("muted", theme.bold("OBSERVED ACTIVITY")));
		for (const activity of activities.slice(0, 3)) {
			const prefix = Date.now() - Number(activity?.observedAt ?? 0) > 30_000 ? "last" : "observed";
			agents.push(theme.fg("text", `${plainLabel(activity?.category, "activity", 40)} · ${plainLabel(activity?.status, "observed", 40)} · ${prefix} ${observedAge(activity?.observedAt)}`));
		}
	}
	const lines = [fitLine(header, width), fitLine(theme.fg("dim", summaryLine), width), ""];
	if (width >= 100) {
		const leftWidth = Math.floor(width * 0.46);
		lines.push(...joinOperationColumns(outline, detail, leftWidth, width - leftWidth));
	} else {
		lines.push(...outline, "", ...detail);
	}
	lines.push("", ...agents, "", theme.fg("dim", "TODOs stay in the Work Dock · Delegations stay in this read-only view · ↑↓ select · q close"));
	return lines.slice(0, 24).map(line => fitLine(line, width));
}

export function renderOperationsEmergency(kind: "loading" | "error", width: number, theme: any): string[] {
	const lines = kind === "loading"
		? [theme.fg("accent", theme.bold("GALPÓN  Operations")), theme.fg("muted", "Loading selected agent facts…"), theme.fg("dim", "q close")]
		: [theme.fg("error", theme.bold("Operations unavailable")), theme.fg("muted", "Galpón could not load this agent. Close this view and open it again."), theme.fg("dim", "q close")];
	return lines.map(line => fitLine(line, Math.max(1, width)));
}

export class OperationsCockpit {
	private value: any;
	private selected = 0;
	private loading = true;
	private failed = false;
	private request = 0;
	private controller: AbortController | undefined;

	constructor(
		private theme: any,
		private onRender: () => void,
		private onClose: () => void,
		private loader: (signal: AbortSignal) => Promise<any>,
	) {
		void this.refresh();
	}

	private async refresh() {
		this.controller?.abort();
		const controller = new AbortController();
		const request = ++this.request;
		this.controller = controller;
		this.loading = true;
		this.failed = false;
		this.onRender();
		try {
			const value = await this.loader(controller.signal);
			if (controller.signal.aborted || request !== this.request) return;
			if (![1, 2].includes(Number(value?.version)) || String(value?.agent?.id ?? "") !== agentId) throw new Error("invalid operations projection");
			this.value = value;
			this.selected = Math.min(this.selected, Math.max(0, operationsRows(value).length - 1));
		} catch {
			if (!controller.signal.aborted && request === this.request) this.failed = true;
		} finally {
			if (!controller.signal.aborted && request === this.request) {
				this.loading = false;
				this.onRender();
			}
		}
	}

	handleInput(data: string) {
		if (matchesKey(data, Key.escape) || data === "q") {
			this.onClose();
			return;
		}
		const rows = operationsRows(this.value);
		if ((matchesKey(data, Key.up) || matchesKey(data, Key.ctrl("p"))) && this.selected > 0) this.selected--;
		if ((matchesKey(data, Key.down) || matchesKey(data, Key.ctrl("n"))) && this.selected < rows.length - 1) this.selected++;
		this.onRender();
	}

	render(width: number): string[] {
		if (this.loading) return renderOperationsEmergency("loading", width, this.theme);
		if (this.failed) return renderOperationsEmergency("error", width, this.theme);
		return renderOperationsCockpit(this.value, width, this.selected, this.theme);
	}

	invalidate() {}
	dispose() { this.controller?.abort(); }
}

export default function galpon(pi: ExtensionAPI) {
	let timer: NodeJS.Timeout | undefined;
	let delegatedStatusTimer: NodeJS.Timeout | undefined;
	let delegatedStatusRefreshing = false;
	let delegatedStatusRefreshDone: Promise<void> | undefined;
	let finishDelegatedStatusRefresh: (() => void) | undefined;
	let piLifecycleActive = false;
	let stopped = false;
	let polling = false;
	let registered = false;
	let registrationPromise: Promise<boolean> | undefined;
	let registrationDelay = 250;
	let protocolGeneration = configuredProtocolGeneration;
	let protocolV2 = configuredProtocolGeneration > 1;
	let protocolMaintenance = false;
	let protocolObservedAt = 0;
	let activeOperation: ActiveCoordinationOperation | undefined;
	let modelOperationAttempt = "";
	let operationClaimSequence = 0;
	let pendingOperationClaimId = "";
	let pendingTodoSettlementClaimId = "";
	let operationSettling = false;
	let directInputPending = false;
	let reviewUiActive = false;
	let pendingDirectUserEntryId = "";
	const operationCompletions = new Map<string, { response: string; error: string; attempt: number }>();
	const persistedOperationReceipts = new Map<string, { operationId: string; operationAttempt: number }>();
	const injectedOperationAttempts = new Set<string>();
	const pendingReceiptPresentations = new Map<string, { operationId: string; operationAttempt: number; toolRequestId: string; toolCallId?: string }>();
	let mirrorStarted = false;
	let extensionWatcherStarted = false;
	let extensionReloadNeeded = false;
	let extensionReloading = false;
	let activeMessageIds: string[] = [];
	const activeMessages = new Map<string, any>();
	let activeBatchId = "";
	let nextClaimIndex = 0;
	let completionPending = false;
	let injectionPending = false;
	let deliveryRunActive = false;
	let deliveryRunBatchId = "";
	let lastLeaseRenewal = 0;
	let finishing = false;
	let lastAssistant = "";
	let lastAssistantBatchId = "";
	let activeContext: any;
	let delegatedStatusText = "";
	let workSnapshotFingerprint = "";
	let registration: { sessionId: string; sessionPath: string; branch: any[] } | undefined;
	const recoverableCompletions = new Map<string, { response: string; error: string }>();
	const awaitInterrupts = new Set<AbortController>();
	const awaitedMessageCounts = new Map<string, number>();
	const beginAwaitingMessages = (messageIds: string[]) => {
		for (const messageId of messageIds) awaitedMessageCounts.set(messageId, (awaitedMessageCounts.get(messageId) ?? 0) + 1);
	};
	const finishAwaitingMessages = (messageIds: string[]) => {
		for (const messageId of messageIds) {
			const remaining = (awaitedMessageCounts.get(messageId) ?? 0) - 1;
			if (remaining > 0) awaitedMessageCounts.set(messageId, remaining);
			else awaitedMessageCounts.delete(messageId);
		}
	};
	const raceAgentWait = <T>(request: Promise<T>, interrupt: AbortController): Promise<{ interrupted: true } | { interrupted: false; value: T }> => {
		const interrupted = new Promise<{ interrupted: true }>(resolve => {
			if (interrupt.signal.aborted) {
				resolve({ interrupted: true });
				return;
			}
			interrupt.signal.addEventListener("abort", () => resolve({ interrupted: true }), { once: true });
		});
		return Promise.race([
			request.then(value => ({ interrupted: false as const, value })),
			interrupted,
		]);
	};
	const conversationMirror = new ConversationMirror();
	const pendingToolEnds = new Map<string, { isError: boolean }>();
	const pendingResultObservations = new Map<string, PendingResultObservation>();
	const todoOperationTaskIds = new Map<string, Set<number>>();
	let todoOwnershipKnowledge: "exact" | "unknown" = "unknown";
	let todoOwnershipReconciling = false;
	const todoAcknowledgements = new Map<string, any>();
	pi.events.on(todoAckEvent, value => {
		const acknowledgement = value as any;
		if (acknowledgement?.schemaVersion === 1 && typeof acknowledgement.operationId === "string") {
			todoAcknowledgements.set(acknowledgement.operationId, acknowledgement);
		}
	});

	const todoAssociationCount = () => {
		let count = 0;
		for (const ids of todoOperationTaskIds.values()) count += ids.size;
		return count;
	};
	const emitActiveTodoOperationSnapshot = () => {
		const ids = new Set<number>();
		for (const taskIds of todoOperationTaskIds.values()) {
			for (const taskId of taskIds) {
				if (ids.size < maxTodoOperationAssociations) ids.add(taskId);
			}
		}
		const exact = todoOwnershipKnowledge === "exact" && todoAssociationCount() <= maxTodoOperationAssociations;
		pi.events.emit(todoOperationSnapshotEvent, {
			schemaVersion: 1,
			activeTaskIds: [...ids],
			ownershipKnowledge: exact ? "exact" : "unknown",
		});
	};
	const markTodoOwnershipUnknown = () => {
		todoOwnershipKnowledge = "unknown";
		emitActiveTodoOperationSnapshot();
	};
	const clearTodoOperationAssociations = (operationId: string, operationAttempt = 0) => {
		if (!todoOperationTaskIds.has(operationId)) return;
		todoOperationTaskIds.delete(operationId);
		pi.appendEntry("galpon-operation", { operationId, operationAttempt, status: "todo_associations_cleared" });
	};
	const associateTodoWithOperation = (operationId: string, operationAttempt: number, todoId: number) => {
		const ids = new Set(todoOperationTaskIds.get(operationId) ?? []);
		if (ids.has(todoId)) return;
		ids.add(todoId);
		todoOperationTaskIds.set(operationId, ids);
		pi.appendEntry("galpon-operation", { operationId, operationAttempt, status: "todo_associated", todoId });
		emitActiveTodoOperationSnapshot();
	};
	const globallyDissociateTodo = (todoId: number) => {
		for (const [operationId, ids] of todoOperationTaskIds) {
			ids.delete(todoId);
			if (ids.size === 0) todoOperationTaskIds.delete(operationId);
		}
		pi.appendEntry("galpon-operation", { status: "todo_globally_dissociated", todoId });
		emitActiveTodoOperationSnapshot();
	};
	const globallyClearTodoAssociations = () => {
		todoOperationTaskIds.clear();
		pi.appendEntry("galpon-operation", { status: "todo_associations_globally_cleared" });
		emitActiveTodoOperationSnapshot();
	};
	const reconcileTodoOperationOwnership = async (): Promise<boolean> => {
		if (!protocolV2 || protocolMaintenance || !registered || todoOwnershipReconciling) return false;
		const operationIds = [...todoOperationTaskIds.keys()];
		if (operationIds.length > maxTodoOperationAssociations) {
			markTodoOwnershipUnknown();
			return false;
		}
		todoOwnershipReconciling = true;
		try {
			const value = await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/reconcile-ownership`, {
				runtimeId,
				protocolGeneration,
				operationIds,
			});
			if (!Array.isArray(value?.ownedOperationIds)
				|| value.ownedOperationIds.some((id: unknown) => typeof id !== "string" || !operationIds.includes(id))) {
				throw new Error("Galpón returned an invalid operation ownership reconciliation");
			}
			const owned = new Set<string>(value.ownedOperationIds);
			for (const operationId of operationIds) {
				if (!owned.has(operationId)) clearTodoOperationAssociations(operationId);
			}
			todoOwnershipKnowledge = todoAssociationCount() <= maxTodoOperationAssociations ? "exact" : "unknown";
			emitActiveTodoOperationSnapshot();
			return todoOwnershipKnowledge === "exact";
		} catch (error) {
			invalidateRegistration(error);
			markTodoOwnershipUnknown();
			return false;
		} finally {
			todoOwnershipReconciling = false;
		}
	};
	pi.events.on(todoMutationEvent, value => {
		const mutation = value as TodoMutationEvent;
		if (!mutation || !["create", "update", "delete", "clear"].includes(mutation.action)
			|| !["changed", "no_change", "rejected"].includes(mutation.effect)
			|| mutation.effect === "rejected") return;
		if ((mutation.action === "delete" || mutation.action === "update" && (mutation.finalStatus === "completed" || mutation.finalStatus === "deleted"))
			&& Number.isSafeInteger(mutation.taskId) && Number(mutation.taskId) > 0) {
			globallyDissociateTodo(Number(mutation.taskId));
			return;
		}
		if (mutation.action === "clear") {
			globallyClearTodoAssociations();
			return;
		}
		if (mutation.action === "update" && mutation.effect === "changed"
			&& (mutation.finalStatus === "pending" || mutation.finalStatus === "in_progress")
			&& Number.isSafeInteger(mutation.taskId) && Number(mutation.taskId) > 0 && activeOperation) {
			associateTodoWithOperation(activeOperation.id, activeOperation.attempt, Number(mutation.taskId));
		}
	});

	const setDelegatedStatus = (count?: number) => {
		const value = count === undefined ? "…" : String(count);
		const text = `🛖  ${workspaceTitle}  ·  🤖 ${value}`;
		if (text === delegatedStatusText) return;
		delegatedStatusText = text;
		activeContext?.ui.setStatus("galpon", text);
	};
	const publishWorkSnapshot = (work: any[], truncated: boolean) => {
		const snapshot = { schemaVersion: 1, work, truncated };
		const fingerprint = JSON.stringify(snapshot);
		if (fingerprint === workSnapshotFingerprint) return;
		workSnapshotFingerprint = fingerprint;
		pi.events.emit(workSnapshotEvent, snapshot);
	};
	const scheduleDelegatedStatus = (delay = delegatedStatusPollMs) => {
		if (stopped) return;
		if (delegatedStatusTimer) clearTimeout(delegatedStatusTimer);
		delegatedStatusTimer = setTimeout(refreshDelegatedStatus, delay);
	};
	const refreshDelegatedStatus = async () => {
		delegatedStatusTimer = undefined;
		if (stopped || delegatedStatusRefreshing) return;
		delegatedStatusRefreshing = true;
		delegatedStatusRefreshDone = new Promise<void>(resolve => { finishDelegatedStatusRefresh = resolve; });
		try {
			if (!await ensureRegistered()) return;
			await reconcileTodoOperationOwnership();
			if (!registered) return;
			const projectContextualActivity = !piLifecycleActive && activeContext?.isIdle() === true;
			const [status, work] = await Promise.all([
				api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/delegated-status`, { runtimeId, projectContextualActivity }),
				api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/work`, { runtimeId }),
			]);
			const count = Number(status?.activeDelegatedAgents);
			if (Number.isSafeInteger(count) && count >= 0) setDelegatedStatus(count);
			publishWorkSnapshot(Array.isArray(work?.work) ? work.work : [], work?.truncated === true);
		} catch {
			// Keep the last known count and work snapshot while the daemon reconnects.
		} finally {
			delegatedStatusRefreshing = false;
			finishDelegatedStatusRefresh?.();
			finishDelegatedStatusRefresh = undefined;
			delegatedStatusRefreshDone = undefined;
			scheduleDelegatedStatus();
		}
	};

	const currentSessionId = () => String(activeContext?.sessionManager?.getSessionId?.() ?? "");
	const emitTodoOperation = (eventName: string, payload: Record<string, any>) => {
		todoAcknowledgements.delete(payload.operationId);
		pi.events.emit(eventName, payload);
		const acknowledgement = todoAcknowledgements.get(payload.operationId);
		todoAcknowledgements.delete(payload.operationId);
		return acknowledgement;
	};
	const linkTodo = (todoId: number | undefined, policy: string | undefined, message: any, target: any) => {
		if (todoId === undefined) return undefined;
		if (!Number.isSafeInteger(todoId) || todoId <= 0) return { status: "rejected", error: "todo_id must be a positive integer" };
		const messageId = String(message?.id ?? "");
		if (!messageId) return { status: "rejected", error: "Galpón did not return a message ID for todo correlation" };
		const operationId = `link:${messageId}:${todoId}`;
		const acknowledgement = emitTodoOperation(todoLinkEvent, {
			schemaVersion: 1,
			sessionId: currentSessionId(),
			messageId,
			todoId,
			operationId,
			policy: policy === "annotate" ? "annotate" : "complete_on_success",
			agentId: String(target?.id ?? message?.targetAgentId ?? ""),
			agentTitle: String(target?.title ?? ""),
		});
		return acknowledgement ?? { status: "rejected", todoId, error: "the bundled todo extension did not acknowledge the delegation link" };
	};
	const settleTodoMessage = (messageId: string, operationId: string, outcome: "succeeded" | "failed", resultMessageId: string, summary: string) =>
		emitTodoOperation(todoSettleEvent, {
			schemaVersion: 1,
			sessionId: currentSessionId(),
			messageId,
			operationId,
			outcome,
			resultMessageId,
			summary: summary.slice(0, 1000),
		});
	const settleTodoResults = (messages: any[]) => {
		for (const message of messages) {
			if (message.kind !== "result" || !message.replyTo || String(message.id ?? "").startsWith("event:work-blocker:")) continue;
			const acknowledgement = settleTodoMessage(
				message.replyTo,
				`settle:${message.id}`,
				message.error ? "failed" : "succeeded",
				message.id,
				String(message.prompt ?? message.response ?? message.error ?? ""),
			);
			if (acknowledgement && acknowledgement.status !== "rejected") message.todoSettlement = acknowledgement;
		}
	};
	const settleAwaitOutcome = (outcome: any) => {
		if (!outcome?.id || (outcome.waitStatus !== "completed" && outcome.messageStatus !== "completed" && outcome.messageStatus !== "failed")) return;
		settleTodoMessage(
			String(outcome.id),
			`settle:await:${outcome.id}`,
			outcome.error || outcome.messageStatus === "failed" ? "failed" : "succeeded",
			`await:${outcome.id}`,
			String(outcome.response ?? outcome.error ?? ""),
		);
	};

	const invalidateRegistration = (error: unknown) => {
		const status = Number((error as any)?.statusCode ?? 0);
		const message = error instanceof Error ? error.message : String(error);
		if (status === 0 || status === 401 || status === 409 || status === 503 || /runtime|register|generation|maintenance/i.test(message)) {
			registered = false;
			markTodoOwnershipUnknown();
		}
	};

	const refreshProtocol = async (force = false) => {
		if (!force && Date.now() - protocolObservedAt < 500) return;
		const state = await api("GET", "/v1/communication/protocol");
		protocolObservedAt = Date.now();
		const nextMaintenance = state?.maintenance === true;
		if (protocolMaintenance && !nextMaintenance) registered = false;
		protocolMaintenance = nextMaintenance;
		if (nextMaintenance) markTodoOwnershipUnknown();
		if (state?.complete === true && Number.isSafeInteger(Number(state.generation)) && Number(state.generation) > 1) {
			const nextGeneration = Number(state.generation);
			if (nextGeneration !== protocolGeneration) {
				registered = false;
				markTodoOwnershipUnknown();
			}
			protocolGeneration = nextGeneration;
			protocolV2 = true;
		} else if (!state?.complete && configuredProtocolGeneration <= 1) {
			protocolGeneration = 1;
			protocolV2 = false;
		}
	};

	const operationBody = (operation: ActiveCoordinationOperation, requestId: string, extra: Record<string, any> = {}) => ({
		runtimeId,
		operationId: operation.id,
		operationAttempt: operation.attempt,
		attempt: operation.attempt,
		protocolGeneration,
		requestId,
		...extra,
	});

	const terminalObservationMessageIds = (name: string, args: Record<string, any>, value: any): string[] => {
		if (name === "read_message") {
			return value?.status === "completed" || value?.status === "failed" ? [String(args.message_id ?? "")] : [];
		}
		if (name === "await_agent") {
			return value?.waitStatus === "completed" || value?.waitStatus === "failed" ? [String(args.message_id ?? "")] : [];
		}
		if (name === "await_agents") {
			return (Array.isArray(value?.outcomes) ? value.outcomes : []).flatMap((outcome: any, index: number) =>
				outcome?.waitStatus === "completed" || outcome?.waitStatus === "failed"
					? [String(outcome.messageId ?? args.message_ids?.[index] ?? "")]
					: [],
			);
		}
		return [];
	};

	const queueResultObservation = (name: string, args: Record<string, any>, value: any, toolCallId: string) => {
		if (!activeOperation) return;
		const messageIds = [...new Set(terminalObservationMessageIds(name, args, value).filter(Boolean))];
		if (messageIds.length === 0) return;
		const observation: PendingResultObservation = {
			operationId: activeOperation.id,
			operationAttempt: activeOperation.attempt,
			toolCallId,
			messageIds,
		};
		pendingResultObservations.set(toolCallId, observation);
		pi.appendEntry("galpon-operation", { ...observation, status: "result_observation_pending" });
	};

	const flushPendingResultObservations = async (): Promise<boolean> => {
		const branch: any[] = activeContext?.sessionManager?.getBranch?.() ?? [];
		let flushed = false;
		for (const observation of pendingResultObservations.values()) {
			if (!activeOperation || observation.operationId !== activeOperation.id) continue;
			if (observation.presented && observation.operationAttempt === activeOperation.attempt) continue;
			if (observation.operationAttempt !== activeOperation.attempt) {
				observation.operationAttempt = activeOperation.attempt;
				observation.presented = false;
				pi.appendEntry("galpon-operation", { ...observation, status: "result_observation_pending" });
			}
			const persisted = branch.some((entry: any) => entry?.type === "message"
				&& entry.message?.role === "toolResult"
				&& entry.message.toolCallId === observation.toolCallId
				&& entry.message.isError !== true);
			if (!persisted) continue;
			const operation: ActiveCoordinationOperation = {
				id: observation.operationId,
				attempt: observation.operationAttempt,
				kind: "observation",
				parentMessageId: "",
				userEntryId: "",
				claimId: "",
				started: true,
			};
			await api(
				"POST",
				`/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/${encodeURIComponent(operation.id)}/observe-results`,
				operationBody(operation, `observe-results:${observation.toolCallId}`, {
					messageIds: observation.messageIds,
					toolCallId: observation.toolCallId,
				}),
			);
			observation.presented = true;
			pi.appendEntry("galpon-operation", { ...observation, status: "result_observation_presented" });
			flushed = true;
		}
		return flushed;
	};

	const stableDirectInputID = (event: any, ctx: any): string => {
		const imageDigests = (Array.isArray(event.images) ? event.images : []).map((image: any) =>
			createHash("sha256")
				.update(String(image?.mimeType ?? image?.mediaType ?? ""))
				.update("\0")
				.update(String(image?.data ?? ""))
				.digest("hex"),
		);
		return `pi-input:${createHash("sha256")
			.update(currentSessionId())
			.update("\0")
			.update(String(ctx.sessionManager.getLeafId?.() ?? ""))
			.update("\0")
			.update(String(event.text ?? ""))
			.update("\0")
			.update(imageDigests.join(","))
			.digest("hex")}`;
	};

	const latestTodoSnapshot = (operationId?: string): string => {
		const branch: any[] = activeContext?.sessionManager?.getBranch?.() ?? [];
		for (let index = branch.length - 1; index >= 0; index--) {
			const entry = branch[index];
			if (entry?.type === "custom" && entry.customType === "rpiv-todo:galpon-state:v1"
				&& (!operationId || entry.data?.operationId === operationId)) {
				return JSON.stringify(entry.data);
			}
		}
		return "";
	};

	const presentReceipt = async (operation: ActiveCoordinationOperation, receiptId: string, toolRequestId: string, payload?: any) => {
		const existing = pendingReceiptPresentations.get(receiptId);
		pendingReceiptPresentations.set(receiptId, { operationId: operation.id, operationAttempt: operation.attempt, toolRequestId, ...(existing?.toolCallId ? { toolCallId: existing.toolCallId } : {}) });
		persistedOperationReceipts.set(receiptId, { operationId: operation.id, operationAttempt: operation.attempt });
		pi.appendEntry("galpon-operation", {
			operationId: operation.id,
			operationAttempt: operation.attempt,
			status: "receipt_persisted",
			receiptId,
			toolRequestId,
			...(payload === undefined ? {} : { payload }),
		});
		await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/${encodeURIComponent(operation.id)}/receipts/${encodeURIComponent(receiptId)}/present`, operationBody(operation, `present:${receiptId}:${toolRequestId}`, { toolRequestId }));
		pi.appendEntry("galpon-operation", {
			operationId: operation.id,
			operationAttempt: operation.attempt,
			status: "receipt_presented",
			receiptId,
			toolRequestId,
		});
		pendingReceiptPresentations.delete(receiptId);
	};

	const processTodoLinkReceipt = async (operation: ActiveCoordinationOperation, receipt: any): Promise<boolean> => {
		const prefix = "todo-link-receipt:";
		if (receipt?.kind !== "control" || !String(receipt.id ?? "").startsWith(prefix)) return false;
		const intentId = String(receipt.id).slice(prefix.length);
		const claimId = `todo-link:${intentId}:${operation.attempt}`;
		const intent = await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/todos/links/${encodeURIComponent(intentId)}/claim`, operationBody(operation, claimId, { claimId }));
		const operationId = `daemon-link:${intent.id}`;
		const acknowledgement = emitTodoOperation(todoLinkEvent, {
			schemaVersion: 1,
			sessionId: currentSessionId(),
			messageId: String(intent.messageId ?? ""),
			todoId: Number(intent.todoId),
			operationId,
			policy: intent.policy === "annotate" ? "annotate" : "complete_on_success",
		});
		if (!acknowledgement || acknowledgement.status === "rejected") {
			await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/todos/links/${encodeURIComponent(intentId)}/fail`, operationBody(operation, `todo-link-fail:${intentId}`, { failure: String(acknowledgement?.error ?? "Pi-local TODO link persistence failed") }));
			throw new Error(String(acknowledgement?.error ?? "Pi-local TODO link persistence failed"));
		}
		await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/todos/links/${encodeURIComponent(intentId)}/apply`, operationBody(operation, `todo-link-apply:${intentId}`));
		return true;
	};

	const processTodoSettlement = async (): Promise<boolean> => {
		if (!protocolV2 || protocolMaintenance || !activeContext?.isIdle() || activeOperation) return false;
		const claimId = pendingTodoSettlementClaimId || `todo-settlement:${currentSessionId()}:${operationClaimSequence++}`;
		pendingTodoSettlementClaimId = claimId;
		let event: any;
		try {
			event = await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/todos/settlements/claim`, {
				runtimeId, claimId, operationAttempt: 0, protocolGeneration,
			});
		} catch (error) {
			if (Number((error as any)?.statusCode ?? 0) === 404) {
				pendingTodoSettlementClaimId = "";
				return false;
			}
			throw error;
		}
		if (!event?.id || !event?.operationId || !event?.operationAttempt) return false;
		const operation: ActiveCoordinationOperation = {
			id: String(event.operationId), attempt: Number(event.operationAttempt), kind: "todo",
			parentMessageId: "", userEntryId: "", claimId, started: false,
		};
		activeOperation = operation;
		emitActiveTodoOperationSnapshot();
		pi.appendEntry("galpon-operation", { operationId: operation.id, operationAttempt: operation.attempt, status: "todo_settlement_claimed", claimId, eventId: String(event.id) });
		try {
			await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/${encodeURIComponent(operation.id)}/start`, operationBody(operation, `todo-settlement-start:${event.id}`));
			operation.started = true;
			const localOperationId = `daemon-settle:${event.id}`;
			let snapshot = latestTodoSnapshot(localOperationId);
			if (!snapshot) {
				const messageId = String(event.resultId ?? "").replace(/^result:/, "");
				const message = await callTool("read_message", { message_id: messageId }, undefined, `todo-settlement-read:${event.id}`);
				const acknowledgement = settleTodoMessage(
					messageId,
					localOperationId,
					message?.status === "failed" || message?.error ? "failed" : "succeeded",
					String(event.resultId ?? ""),
					String(message?.response ?? message?.error ?? ""),
				);
				if (!acknowledgement || acknowledgement.status === "rejected") {
					await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/todos/settlements/${encodeURIComponent(event.id)}/fail`, operationBody(operation, `todo-settlement-fail:${event.id}`, { failure: String(acknowledgement?.error ?? "Pi-local TODO settlement persistence failed") }));
					throw new Error(String(acknowledgement?.error ?? "Pi-local TODO settlement persistence failed"));
				}
				snapshot = latestTodoSnapshot(localOperationId);
				if (!snapshot && acknowledgement.status === "duplicate") {
					// Migration can create a daemon event for a result that the old Pi
					// integration already applied. Reuse the latest durable local state.
					snapshot = latestTodoSnapshot();
				}
			}
			if (!snapshot) throw new Error("Pi-local TODO settlement snapshot was not persisted");
			if (event.state === "pending") {
				await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/todos/settlements/${encodeURIComponent(event.id)}/apply`, operationBody(operation, `todo-settlement-apply:${event.id}`, { snapshot }));
			}
			await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/todos/settlements/${encodeURIComponent(event.id)}/ack`, operationBody(operation, `todo-settlement-ack:${event.id}`));
			pi.appendEntry("galpon-operation", { operationId: operation.id, operationAttempt: operation.attempt, status: "todo_settlement_acknowledged", claimId, eventId: String(event.id) });
			pendingTodoSettlementClaimId = "";
			return true;
		} finally {
			activeOperation = undefined;
			emitActiveTodoOperationSnapshot();
		}
	};

	const callTool = async (name: string, args: Record<string, any>, signal: AbortSignal | undefined, toolCallId: string) => {
		let lastError: unknown;
		const readOnly = name === "list_repositories" || name === "list_workspaces" || name === "list_agents"
			|| name === "read_message" || name === "await_agent" || name === "await_agents";
		const retryable = readOnly || name === "send_agent" || name === "update_agent";
		for (let attempt = 0; attempt < (retryable ? 3 : 1); attempt++) {
			if (!await ensureRegistered()) throw new Error("Galpón runtime registration is not available");
			if (protocolV2 && !activeOperation && !readOnly) throw new Error("This Galpón tool call requires an active operation");
			try {
				return await api("POST", `/v1/runtime/tools/${name}`, {
					agentId,
					runtimeId,
					requestId: toolCallId,
					toolCallId,
					...(protocolV2 ? activeOperation ? {
						operationId: activeOperation.id,
						operationAttempt: activeOperation.attempt,
						protocolGeneration,
						currentMessageId: activeOperation.parentMessageId,
						currentAttempt: activeOperation.attempt,
					} : { protocolGeneration } : {
						currentMessageId: activeMessageIds[0] ?? "",
						currentAttempt: Number(activeMessages.get(activeMessageIds[0] ?? "")?.attempt ?? 0),
					}),
					args,
				}, signal);
			} catch (error) {
				lastError = error;
				invalidateRegistration(error);
				if (signal?.aborted) throw error;
				const status = Number((error as any)?.statusCode ?? 0);
				if (status > 0 && status < 500 && status !== 409) throw error;
				await new Promise(resolve => setTimeout(resolve, 100 * (attempt + 1)));
			}
		}
		throw lastError;
	};

	pi.registerCommand("review", {
		description: "Review Markdown in a modal text buffer and prepare quoted feedback",
		handler: async (args, ctx) => {
			if (ctx.mode !== "tui") {
				ctx.ui.notify("Review mode requires an interactive terminal.", "error");
				return;
			}
			await ctx.waitForIdle();
			if (reviewUiActive) {
				ctx.ui.notify("Review Mode is already open.", "warning");
				return;
			}
			if (activeOperation || activeMessageIds.length > 0 || completionPending || directInputPending || deliveryRunActive) {
				ctx.ui.notify("Finish the current Galpón work before you open Review Mode.", "warning");
				return;
			}
			reviewUiActive = true;
			try {
			// A timer poll can already be past its opening guard. Do not show the
			// modal until that poll has completed all claims and Pi injections.
			const pollDrainDeadline = Date.now() + 5_000;
			while (polling && Date.now() < pollDrainDeadline) await new Promise(resolve => setTimeout(resolve, 10));
			if (polling) {
				ctx.ui.notify("Review Mode could not open because Galpón work polling is still active. Try again.", "warning");
				return;
			}
			if (activeOperation || activeMessageIds.length > 0 || completionPending || directInputPending || deliveryRunActive) {
				ctx.ui.notify("Finish the current Galpón work before you open Review Mode.", "warning");
				return;
			}
			const branch = ctx.sessionManager.getBranch();
			const sources = assistantReviewSources(branch);
			if (sources.length === 0) {
				ctx.ui.notify("No completed assistant response is available to review.", "error");
				return;
			}
			let source = sources[sources.length - 1];
			const argument = args.trim().toLocaleLowerCase();
			if (argument && argument !== "pick") {
				ctx.ui.notify("Use /review for the latest response or /review pick to choose an earlier response.", "error");
				return;
			}
			if (argument === "pick") {
				const choices = sources.slice(-20).reverse().map((candidate, index) => ({
					candidate,
					label: reviewSourceLabel(candidate, index),
				}));
				const selected = await ctx.ui.select("Select an assistant response", choices.map(choice => choice.label));
				if (!selected) return;
				source = choices.find(choice => choice.label === selected)?.candidate ?? source;
			}
			if (Buffer.byteLength(source.text) > maxReviewSourceBytes) {
				ctx.ui.notify("The selected response is too large for Review Mode.", "error");
				return;
			}
			const blocks = parseReviewBuffer(source.text);
			if (blocks.length > maxReviewBlocks) {
				ctx.ui.notify("The selected response has too many lines for Review Mode.", "error");
				return;
			}
			if (blocks.length === 0) {
				ctx.ui.notify("The selected response has no reviewable text.", "error");
				return;
			}
			const restored = restoredReviewDraft(branch, source, blocks);
			let items = restored.items;
			let editing = restored.editing;
			const state: ReviewViewState = { focus: "source", cursor: 0, itemCursor: 0, query: "" };
			const save = (status: "open" | "prepared", activeEditing?: ReviewEditingDraft) => {
				const persistedItems: PersistedReviewItem[] = items.map(item => ({ ...item, quoteHash: reviewTextHash(item.quote) }));
				let persistedEditing: PersistedReviewEditing | undefined;
				if (activeEditing) {
					const range = currentReviewRange(blocks, activeEditing);
					if (!range) return;
					if (activeEditing.kind === "edit") {
						const item = items.find(candidate => candidate.id === activeEditing.itemId);
						if (!item || item.start !== range.start || item.end !== range.end || item.startColumn !== range.startColumn || item.endColumn !== range.endColumn) return;
					}
					const quote = reviewSelection(blocks, range.start, range.end, range.startColumn, range.endColumn);
					const buffer = sanitizeReviewText(activeEditing.buffer);
					if (!quote || Buffer.byteLength(buffer) + reviewDraftBytes(items) > maxReviewDraftBytes) return;
					persistedEditing = { ...activeEditing, ...range, buffer, quoteHash: reviewTextHash(quote) };
				}
				const snapshot: ReviewDraftSnapshotV2 = {
					version: 2,
					parserVersion: reviewParserVersion,
					sourceEntryId: source.entryId,
					sourceHash: source.hash,
					sourceBytes: Buffer.byteLength(source.text),
					status,
					items: persistedItems,
					...(persistedEditing ? { editing: persistedEditing } : {}),
					updatedAt: Date.now(),
				};
				pi.appendEntry(reviewDraftEvent, snapshot);
			};

			for (;;) {
				const action = await ctx.ui.custom<ReviewAction | undefined>((tui, theme, _keybindings, done) => new ReviewMode(
					blocks,
					items,
					state,
					theme,
					() => tui.requestRender(),
					done,
					() => Math.max(1, Number(tui.terminal.rows ?? 28)),
					{
						tui,
						editing,
						makeID: randomUUID,
						confirmFinish: Boolean(ctx.ui.getEditorText().trim()),
						onItemsChanged: next => {
							items = next;
							editing = undefined;
							save("open");
						},
						onEditingChanged: next => {
							editing = next;
							save("open", next);
						},
					},
				), {
					overlay: true,
					overlayOptions: { row: 0, col: 0, width: "100%", maxHeight: "100%" },
				});
				if (!action || action.kind === "cancel") {
					if (items.length > 0 || editing) ctx.ui.notify("Review draft saved. Run /review to continue.", "info");
					return;
				}
				if (action.kind === "finish") {
					if (items.length === 0) {
						ctx.ui.notify("Add feedback before you prepare the review.", "warning");
						continue;
					}
					if (reviewDraftBytes(items) > maxReviewDraftBytes) {
						ctx.ui.notify("The review draft is too large to prepare.", "warning");
						continue;
					}
					ctx.ui.setEditorText(compileReview(items));
					save("prepared");
					ctx.ui.notify("Review prepared. Edit and submit it when ready.", "info");
					return;
				}
			}
			} finally {
				reviewUiActive = false;
				schedule(0);
			}
		},
	});

	pi.registerCommand("finish", {
		description: "Finish and hide this Galpón agent",
		handler: async (_args, ctx) => {
			const confirmed = await ctx.ui.confirm(
				`Finish ${agentTitle}?`,
				"This closes any terminal view and hides this agent and its unshared private worktrees. Files and the Pi session remain until galpon cleanup.",
			);
			if (!confirmed) {
				ctx.ui.notify("Finish cancelled", "info");
				return;
			}
			try {
				if (!await ensureRegistered()) throw new Error("Galpón runtime registration is not available");
				await api("POST", `/v1/runtime/agents/${agentId}/finish`, { runtimeId });
			} catch (error) {
				ctx.ui.notify(`Could not finish agent: ${error instanceof Error ? error.message : String(error)}`, "error");
				return;
			}
			ctx.ui.notify(`Finishing ${agentTitle}…`, "info");
			ctx.shutdown();
		},
	});

	pi.registerTool({
		name: "galpon_list_repositories",
		label: "Galpón repositories",
		description: "List the repositories that Galpón manages.",
		parameters: Type.Object({}),
		async execute(id, _params, signal) { return toolResult(await callTool("list_repositories", {}, signal, id)); },
	});
	pi.registerTool({
		name: "galpon_list_workspaces",
		label: "Galpón workspaces",
		description: "List active Galpón workspaces.",
		parameters: Type.Object({}),
		async execute(id, _params, signal) { return toolResult(await callTool("list_workspaces", {}, signal, id)); },
	});
	pi.registerTool({
		name: "galpon_list_agents",
		label: "Galpón agents",
		description: "List durable Galpón agents and their current state.",
		parameters: Type.Object({}),
		async execute(id, _params, signal) { return toolResult(await callTool("list_agents", {}, signal, id)); },
	});
	pi.registerTool({
		name: "galpon_create_agent",
		label: "Create agent",
		description: "Create and start a durable background Pi agent with an independent context source and file placement. If no repository, placement agent, or cwd is set, Galpón creates a private managed directory for the agent. It runs without a Herdr tab until the user promotes it. If prompt is set, Galpón queues it before Pi starts so the agent begins work as soon as its runtime is ready. The result then includes initialMessage, whose ID can be used with galpon_read_message or galpon_await_agent.",
		parameters: Type.Object({
			title: Type.String({ description: "Agent title" }),
			workspace: Type.Optional(Type.String({ description: "Omit to use your current workspace. If set, it must be your current workspace ID or exact title." })),
			role: Type.Optional(Type.String({ description: "Optional role, such as implementer, reviewer, or coordinator" })),
			prompt: Type.Optional(Type.String({ description: "Initial work request to queue before the new agent starts" })),
			todo_id: Type.Optional(Type.Integer({ minimum: 1, description: "Parent todo ID that this delegated request owns." })),
			todo_policy: Type.Optional(Type.Union([Type.Literal("complete_on_success"), Type.Literal("annotate")], { description: "How the linked todo changes when the result returns. Defaults to complete_on_success." })),
			context_agent: Type.Optional(Type.String({ description: "Existing agent ID or exact title whose Pi conversation must be forked" })),
			repository: Type.Optional(Type.String({ description: "Primary repository ID or exact title for a new private placement" })),
			remote: Type.Optional(Type.String({ description: "Primary source remote" })),
			ref: Type.Optional(Type.String({ description: "Primary source reference" })),
			secondary: Type.Optional(Type.Array(Type.Object({
				repository: Type.String({ description: "Secondary repository ID or exact title" }),
				remote: Type.Optional(Type.String({ description: "Secondary source remote" })),
				ref: Type.Optional(Type.String({ description: "Secondary source reference" })),
			}))),
			placement_agent: Type.Optional(Type.String({ description: "Existing agent whose complete placement must be copied" })),
			share: Type.Optional(Type.Boolean({ description: "Share the placement agent's exact worktrees instead of creating private forks" })),
			cwd: Type.Optional(Type.String({ description: "Existing absolute directory outside Galpón management" })),
		}),
		async execute(id, params, signal) {
			const { todo_id, todo_policy, ...request } = params;
			const value = await callTool("create_agent", protocolV2 ? { ...request, todo_id, todo_policy } : request, signal, id);
			if (!protocolV2 && todo_id !== undefined) {
				value.todoLink = value.initialMessage
					? linkTodo(todo_id, todo_policy, value.initialMessage, value)
					: { status: "rejected", todoId: todo_id, error: "todo_id requires an initial prompt that creates a reply-bearing message" };
			}
			return toolResult(value);
		},
	});
	pi.registerTool({
		name: "galpon_cleanup_agents",
		label: "Clean up agents",
		description: "Permanently remove the specified agents created directly or indirectly by this agent. Use galpon_list_agents to inspect IDs and creator relationships, then pass only the requested agent IDs. A selected agent cannot be removed while one of its descendants is not selected. This closes managed Herdr views and removes private worktrees, Pi sessions, and related messages. It never removes the calling agent. Use only after an explicit cleanup request and after delegated results are no longer needed.",
		parameters: Type.Object({
			agent_ids: Type.Array(Type.String({ description: "Exact Galpón agent ID" }), { minItems: 1, uniqueItems: true, description: "Agent IDs to remove permanently" }),
		}),
		async execute(id, params, signal) { return toolResult(await callTool("cleanup_agents", params, signal, id)); },
	});
	pi.registerTool({
		name: "galpon_send_agent",
		label: "Send agent message",
		description: "Queue a typed durable message for another Galpón agent and start that agent if necessary. request and query require a reply. inform is one-way coordination and does not send the target's final reply back. Returns a message ID immediately. Do not use this tool to return the result of a delivery that you are processing; put that complete result in your final assistant response.",
		parameters: Type.Object({
			agent: Type.String({ description: "Target agent ID or exact title" }),
			prompt: Type.String({ description: "Message text" }),
			act: Type.Optional(Type.Union([Type.Literal("request"), Type.Literal("query"), Type.Literal("inform")], { description: "Message intent. Defaults to request." })),
			todo_id: Type.Optional(Type.Integer({ minimum: 1, description: "Parent todo ID that this request owns." })),
			todo_policy: Type.Optional(Type.Union([Type.Literal("complete_on_success"), Type.Literal("annotate")], { description: "How the linked todo changes when the result returns. Defaults to complete_on_success." })),
		}),
		async execute(id, params, signal) {
			if (params.todo_id !== undefined && params.act === "inform") throw new Error("todo_id requires request or query intent");
			const { todo_id, todo_policy, ...request } = params;
			const value = await callTool("send_agent", protocolV2 ? { ...request, todo_id, todo_policy } : request, signal, id);
			if (!protocolV2 && todo_id !== undefined) value.todoLink = linkTodo(todo_id, todo_policy, value, value);
			return toolResult(value);
		},
	});
	pi.registerTool({
		name: "galpon_update_agent",
		label: "Update agent message",
		description: "Update one queued, unclaimed agent assignment. Running and completed assignments are not changed. The response status is updated, already_started, or already_completed.",
		parameters: Type.Object({
			message_id: Type.String({ description: "Message ID from galpon_send_agent or galpon_create_agent" }),
			prompt: Type.String({ description: "Additional instructions to append to the queued assignment" }),
		}),
		async execute(id, params, signal) { return toolResult(await callTool("update_agent", params, signal, id)); },
	});
	pi.registerTool({
		name: "galpon_report_progress",
		label: "Report delegated work progress",
		description: "Report one safe, factual checkpoint only while processing an active inbound delegated request. Direct user turns and completed-result notifications are not eligible. An unavailable report returns recorded=false. The update is attempt-fenced and does not wake the parent model. Do not include reasoning, prompts, tool data, secrets, paths, percentages, estimates, or ETA values.",
		promptSnippet: "Report a safe checkpoint only for an active inbound delegated request",
		promptGuidelines: ["Use galpon_report_progress only for meaningful phase, milestone, blocker, or factual-count changes while processing an active inbound delegated request. Do not use it for direct user turns or completed-result notifications."],
		parameters: Type.Object({
			version: Type.Optional(Type.Literal(1, { description: "Schema version. Defaults to 1." })),
			event_id: Type.Optional(Type.String({ minLength: 1, maxLength: 100, description: "Stable unique ID for this report. Defaults to the runtime tool ID." })),
			phase: StringEnum(["planning", "working", "verifying", "waiting", "blocked", "finishing"] as const),
			summary: Type.String({ minLength: 1, maxLength: 240, description: "One-line safe factual checkpoint" }),
			milestones: Type.Optional(Type.Array(Type.Object({
				label: Type.String({ minLength: 1, maxLength: 80 }),
				state: StringEnum(["pending", "active", "completed", "blocked"] as const),
			}), { maxItems: 8 })),
			blocker: Type.Optional(Type.String({ maxLength: 240 })),
			counts: Type.Optional(Type.Array(Type.Object({
				label: Type.String({ minLength: 1, maxLength: 40 }),
				completed: Type.Integer({ minimum: 0, maximum: 1_000_000_000 }),
				total: Type.Integer({ minimum: 0, maximum: 1_000_000_000 }),
			}), { maxItems: 8 })),
		}),
		async execute(id, params, signal) {
			if (!activeOperation || activeOperation.kind !== "inbound" || !activeOperation.parentMessageId) {
				return toolResult(unavailableProgressResult());
			}
			const report = { ...params, version: params.version ?? 1, event_id: params.event_id ?? id };
			try {
				return toolResult(await callTool("report_progress", report, signal, id));
			} catch (error) {
				if (signal?.aborted) throw error;
				if (isUnavailableProgressError(error)) return toolResult(unavailableProgressResult());
				const status = Number((error as any)?.statusCode ?? 0);
				if (status > 0 && status < 500) throw error;
				try {
					return toolResult(await callTool("report_progress", report, signal, id));
				} catch (retryError) {
					if (signal?.aborted) throw retryError;
					if (isUnavailableProgressError(retryError)) return toolResult(unavailableProgressResult());
					throw retryError;
				}
			}
		},
	});
	pi.registerTool({
		name: "galpon_read_message",
		label: "Read agent message",
		description: "Read the current state and result of a Galpón agent message.",
		parameters: Type.Object({ message_id: Type.String({ description: "Message ID from galpon_send_agent" }) }),
		async execute(id, params, signal) {
			const value = await callTool("read_message", params, signal, id);
			queueResultObservation("read_message", params, value, id);
			return toolResult(value);
		},
	});
	pi.registerTool({
		name: "galpon_await_agents",
		label: "Wait for agents",
		description: "Wait for 1 to 16 Galpón agent messages with one global timeout. Return when any message settles or when all messages settle. Outcomes keep input order. A timeout does not cancel agent work. Galpón rejects duplicate IDs and circular waits.",
		parameters: Type.Object({
			message_ids: Type.Array(Type.String({ description: "Message ID from galpon_send_agent" }), { minItems: 1, maxItems: 16, uniqueItems: true }),
			return_when: Type.Union([Type.Literal("any"), Type.Literal("all")], { description: "Return after any message settles or after all messages settle" }),
			timeout_seconds: Type.Optional(Type.Integer({ minimum: 1, maximum: 300, description: "One maximum wait for the full call, from 1 to 300 seconds (default 60)" })),
		}),
		async execute(id, params, signal) {
			if (protocolV2) {
				const value = await callTool("await_agents", params, signal, id);
				queueResultObservation("await_agents", params, value, id);
				return toolResult(value);
			}
			if (activeMessageIds.length !== 0 && !deliveryRunActive) {
				return toolResult({
					status: "interrupted", returnWhen: params.return_when, completed: 0, total: params.message_ids.length,
					outcomes: params.message_ids.map(messageId => ({ messageId, waitStatus: "interrupted", messageStatus: "unknown", targetRuntimeStatus: "unknown", attempt: 0, waitError: { kind: "inbound_work", message: "Inbound work is already queued for this agent." } })),
				});
			}
			const interrupt = new AbortController();
			awaitInterrupts.add(interrupt);
			beginAwaitingMessages(params.message_ids);
			const waitSignal = signal
				? (AbortSignal as any).any([signal, interrupt.signal]) as AbortSignal
				: interrupt.signal;
			try {
				const outcome = await raceAgentWait(callTool("await_agents", params, waitSignal, id), interrupt);
				if (outcome.interrupted) {
					return toolResult({
						status: "interrupted", returnWhen: params.return_when, completed: 0, total: params.message_ids.length,
						outcomes: params.message_ids.map(messageId => ({ messageId, waitStatus: "interrupted", messageStatus: "unknown", targetRuntimeStatus: "unknown", attempt: 0, waitError: { kind: "inbound_work", message: "The wait stopped because this agent received inbound work." } })),
					});
				}
				for (const value of outcome.value?.outcomes ?? []) settleAwaitOutcome(value);
				return toolResult(outcome.value);
			} catch (error) {
				if (interrupt.signal.aborted && !signal?.aborted) {
					return toolResult({
						status: "interrupted", returnWhen: params.return_when, completed: 0, total: params.message_ids.length,
						outcomes: params.message_ids.map(messageId => ({ messageId, waitStatus: "interrupted", messageStatus: "unknown", targetRuntimeStatus: "unknown", attempt: 0, waitError: { kind: "inbound_work", message: "The wait stopped because this agent received inbound work." } })),
					});
				}
				throw error;
			} finally {
				awaitInterrupts.delete(interrupt);
				finishAwaitingMessages(params.message_ids);
				schedule(0);
			}
		},
	});
	pi.registerTool({
		name: "galpon_await_agent",
		label: "Wait for agent",
		description: "Wait for up to 60 seconds by default for one Galpón agent message. The typed result includes waitStatus, messageStatus, targetRuntimeStatus, attempt, and a structured waitError when applicable. Galpón rejects circular waits. A timeout does not cancel agent work.",
		parameters: Type.Object({
			message_id: Type.String({ description: "Message ID from galpon_send_agent" }),
			timeout_seconds: Type.Optional(Type.Integer({ minimum: 1, maximum: 300, description: "Maximum wait for this call, from 1 to 300 seconds (default 60)" })),
		}),
		async execute(id, params, signal, onUpdate) {
			if (protocolV2) {
				const value = await callTool("await_agent", params, signal, id);
				queueResultObservation("await_agent", params, value, id);
				return toolResult(value);
			}
			if (activeMessageIds.length !== 0 && !deliveryRunActive) {
				return toolResult({
					messageId: params.message_id,
					status: "interrupted",
					waitStatus: "interrupted",
					messageStatus: "unknown",
					targetRuntimeStatus: "unknown",
					attempt: 0,
					waitError: { kind: "inbound_work", message: "Inbound work is already queued for this agent. Finish the current turn before you wait again." },
				});
			}
			const interrupt = new AbortController();
			awaitInterrupts.add(interrupt);
			beginAwaitingMessages([params.message_id]);
			const waitSignal = signal
				? (AbortSignal as any).any([signal, interrupt.signal]) as AbortSignal
				: interrupt.signal;
			const started = Date.now();
			let progressReads = 0;
			let updating = false;
			const progress = setInterval(async () => {
				if (updating || waitSignal.aborted) return;
				updating = true;
				try {
					const value = await callTool("read_message", { message_id: params.message_id }, waitSignal, `${id}:progress:${progressReads++}`);
					onUpdate?.({
						content: [{ type: "text", text: `Waiting for agent ${value.targetAgentId}: ${value.status} (${Math.round((Date.now() - started) / 1000)}s)` }],
						details: value,
					});
				} catch {
					// The main wait reports connection and user cancellation errors.
				} finally {
					updating = false;
				}
			}, 5000);
			progress.unref?.();
			try {
				const outcome = await raceAgentWait(callTool("await_agent", params, waitSignal, id), interrupt);
				if (outcome.interrupted) {
					return toolResult({
						messageId: params.message_id,
						status: "interrupted",
						waitStatus: "interrupted",
						messageStatus: "unknown",
						targetRuntimeStatus: "unknown",
						attempt: 0,
						waitError: { kind: "inbound_work", message: "The wait stopped because this agent received inbound work. Address that work before you wait again." },
					});
				}
				settleAwaitOutcome(outcome.value);
				return toolResult(outcome.value);
			} catch (error) {
				if (interrupt.signal.aborted && !signal?.aborted) {
					return toolResult({
						messageId: params.message_id,
						status: "interrupted",
						waitStatus: "interrupted",
						messageStatus: "unknown",
						targetRuntimeStatus: "unknown",
						attempt: 0,
						waitError: { kind: "inbound_work", message: "The wait stopped because this agent received inbound work. Address that work before you wait again." },
					});
				}
				throw error;
			} finally {
				clearInterval(progress);
				awaitInterrupts.delete(interrupt);
				finishAwaitingMessages([params.message_id]);
				schedule(0);
			}
		},
	});

	const schedule = (delay = 350) => {
		if (stopped) return;
		if (timer) clearTimeout(timer);
		timer = setTimeout(() => void poll(), delay);
		timer.unref?.();
	};

	pi.registerCommand("galpon-reload-extension", {
		description: "Reload the installed Galpón runtime extension",
		handler: async (_args, ctx) => {
			await ctx.reload();
			return;
		},
	});

	const ensureRegistered = async (): Promise<boolean> => {
		if (registered) return true;
		if (registrationPromise) return registrationPromise;
		if (!registration || stopped) return false;
		registrationPromise = (async () => {
			try {
				await refreshProtocol(true);
				await api("POST", `/v1/runtime/agents/${agentId}/register`, {
					runtimeId,
					sessionId: registration.sessionId,
					sessionPath: registration.sessionPath,
					protocolGeneration,
				});
				registered = true;
				registrationDelay = 250;
				if (!mirrorStarted) {
					mirrorStarted = true;
					conversationMirror.startBackfill(conversationBackfill(registration.sessionId, registration.branch));
				}
				return true;
			} catch (error) {
				invalidateRegistration(error);
				registrationDelay = Math.min(registrationDelay * 2, 5000);
				return false;
			}
		})();
		try {
			return await registrationPromise;
		} finally {
			registrationPromise = undefined;
		}
	};

	pi.registerCommand("operations", {
		description: "Open read-only Operations for this Galpón agent",
		handler: async (_args, ctx) => {
			if (ctx.mode !== "tui") {
				ctx.ui.notify("The Operations cockpit is available in the interactive terminal.", "info");
				return;
			}
			if (!agentId || !await ensureRegistered()) {
				ctx.ui.notify("Galpón could not load this agent.", "error");
				return;
			}
			await ctx.ui.custom<void>((tui, theme, _keybindings, done) => new OperationsCockpit(
				theme,
				() => tui.requestRender(),
				() => done(undefined),
				(signal) => api("GET", `/v1/agents/${encodeURIComponent(agentId)}/operations`, undefined, signal),
			));
		},
	});

	const markDeliveryComplete = (messageId: string, failure: string) => {
		pi.appendEntry("galpon-delivery", { messageId, status: failure ? "failed" : "completed" });
		activeMessageIds = activeMessageIds.filter(id => id !== messageId);
		activeMessages.delete(messageId);
		recoverableCompletions.delete(messageId);
	};

	const releaseStaleDeliveryAttempt = (messageId: string, attempt: number) => {
		pi.appendEntry("galpon-delivery", { messageId, status: "stale_attempt", attempt });
		activeMessageIds = activeMessageIds.filter(id => id !== messageId);
		activeMessages.delete(messageId);
		// Keep recoverableCompletions. The next claim gets a fenced attempt and
		// submits the already persisted final response without running Pi again.
	};

	const completeDelivery = async (message: any, response: string, failure: string): Promise<boolean> => {
		const saved = recoverableCompletions.get(message.id);
		if (!saved || saved.response !== response || saved.error !== failure) {
			recoverableCompletions.set(message.id, { response, error: failure });
			pi.appendEntry("galpon-delivery", {
				messageId: message.id,
				status: "completion_pending",
				attempt: message.attempt,
				response,
				error: failure,
			});
		}
		try {
			await api("POST", `/v1/runtime/agents/${agentId}/messages/${message.id}/complete`, {
				runtimeId,
				attempt: message.attempt,
				response,
				error: failure,
			});
			markDeliveryComplete(message.id, failure);
			return true;
		} catch {
			try {
				const observed = await api("POST", "/v1/runtime/tools/read_message", {
					agentId,
					runtimeId,
					requestId: `delivery-reconcile:${message.id}:${message.attempt ?? 0}:${randomUUID()}`,
					args: { message_id: message.id },
				});
				if (observed.status === "completed" || observed.status === "failed") {
					// Exact agreement is the common lost-response case. A different
					// terminal value means a deadline or another fenced owner won.
					const exact = String(observed.response ?? "") === response
						&& String(observed.error ?? "") === failure;
					markDeliveryComplete(message.id, exact ? failure : String(observed.error ?? "durable delivery was already settled"));
					return true;
				}
				const staleLease = observed.status === "delivered"
					&& ((Number(observed.attempt) !== Number(message.attempt))
						|| (Number(observed.leaseExpiresAt) > 0 && Number(observed.leaseExpiresAt) <= Date.now())
						|| (Number(observed.processingDeadlineAt) > 0 && Number(observed.processingDeadlineAt) <= Date.now()));
				if (observed.status === "queued" || staleLease) {
					releaseStaleDeliveryAttempt(message.id, Number(message.attempt ?? 0));
					return true;
				}
			} catch {
				// Keep the durable completion intent. A later claim or poll retries it.
			}
			return false;
		}
	};

	const renewActiveLeases = async () => {
		if (Date.now() - lastLeaseRenewal < 30_000) return;
		for (const messageId of activeMessageIds) {
			const message = activeMessages.get(messageId);
			if (!message) continue;
			await api("POST", `/v1/runtime/agents/${agentId}/messages/${message.id}/renew`, {
				runtimeId,
				attempt: message.attempt,
			});
		}
		lastLeaseRenewal = Date.now();
	};

	const finishActive = async () => {
		if (activeMessageIds.length === 0) return true;
		if (finishing) return false;
		finishing = true;
		const rawResponse = lastAssistantBatchId === deliveryRunBatchId ? lastAssistant : "";
		const correlatedResponse = boundedDeliveryResponse(rawResponse);
		const failure = correlatedResponse ? "" : "Pi agent settled without a final text response for this delivery batch";
		try {
			for (const messageId of [...activeMessageIds]) {
				const message = activeMessages.get(messageId);
				if (message) await completeDelivery(message, correlatedResponse, failure);
			}
			if (activeMessageIds.length === 0) {
				completionPending = false;
				lastAssistant = "";
				lastAssistantBatchId = "";
				activeBatchId = "";
				nextClaimIndex = 0;
				injectionPending = false;
				lastLeaseRenewal = 0;
			}
			return activeMessageIds.length === 0;
		} finally {
			finishing = false;
		}
	};

	const claimMessages = async (limit: number) => {
		const messages: any[] = [];
		if (!activeBatchId) {
			activeBatchId = randomUUID();
			nextClaimIndex = 0;
		}
		for (let count = 0; count < limit; count++) {
			const claimKey = `${activeBatchId}:${nextClaimIndex}`;
			const value = await api("POST", `/v1/runtime/agents/${agentId}/claim`, { runtimeId, claimId: claimKey });
			if (!value.message) {
				nextClaimIndex++;
				break;
			}
			messages.push(value.message);
			nextClaimIndex++;
		}
		return messages;
	};

	const deliveryImages = (message: any) => {
		const values = Array.isArray(message.images) ? message.images : Array.isArray(message.attachments) ? message.attachments : [];
		return values.flatMap((image: any) => {
			const mimeType = String(image.mimeType ?? image.mediaType ?? image.source?.mediaType ?? "");
			const data = image.data ?? image.source?.data;
			if (!supportedImageMimeTypes.has(mimeType) || !validImageData(data)) return [];
			// Pi session messages and provider adapters use top-level image fields.
			return [{ type: "image" as const, mimeType, data }];
		});
	};

	const formatMessages = (messages: any[]) => {
		const body = messages.map((message, index) => {
			const senderLabel = message.senderTitle || message.senderAgentId;
			const sender = senderLabel ? ` from Galpón agent ${senderLabel}` : "";
			if (message.kind === "result" && String(message.id ?? "").startsWith("event:work-blocker:")) {
				return `Blocked work notification${sender} [delivery ${message.id}]:\n\nThis is an agent-reported blocker. It is not a completed result. Review it and request user input when necessary.\n\n${String(message.prompt ?? "A delegated work item is blocked.")}`;
			}
			if (message.kind === "result") {
				const reply = message.replyTo ? ` for message ${message.replyTo}` : "";
				const result = String(message.prompt ?? message.response ?? message.error ?? "No result text was provided.");
				const todo = message.todoSettlement?.todoId
					? `\n\nLinked todo #${message.todoSettlement.todoId} was reconciled automatically. Review the result before you close any dependent todo.`
					: "";
				return `${messages.length > 1 ? `Message ${index + 1} of ${messages.length}` : "Message"}${sender} [delivery ${message.id}]:\n\nCompleted correlated result${reply}. This is a result notification, not a new work request.${todo}\n\n${result}`;
			}
			const intent = message.act === "inform" ? "One-way information" : message.act === "query" ? "Question" : "Work request";
			return `${messages.length > 1 ? `Message ${index + 1} of ${messages.length}` : intent}${sender} [delivery ${message.id}]:\n\n${message.prompt}`;
		}).join("\n\n---\n\n");
		const oneWay = messages.every(message => message.kind === "request" && message.act === "inform");
		const instructions = oneWay
			? "Delivery instructions: This is one-way information. Use it if it is relevant. Address it in this turn, but do not send a reply to the sender. Your final assistant text is stored only as the durable local completion record."
			: "Delivery instructions: Address every delivery in this batch. Your final assistant text is the durable result for this batch. State what you completed, the main result, and any error or remaining work. Do not use galpon_send_agent to return a result for a current delivery. Galpón sends your final text to the requester when this turn settles.";
		const text = `${body}\n\n---\n\n${instructions}`;
		return [{ type: "text" as const, text }, ...messages.flatMap(deliveryImages)];
	};

	const reloadInstalledExtension = () => {
		if (!extensionReloadNeeded || extensionReloading || stopped || directInputPending || activeMessageIds.length !== 0 || activeOperation || !activeContext?.isIdle()) return false;
		extensionReloading = true;
		try {
			pi.sendUserMessage("/galpon-reload-extension", { expandPromptTemplates: true });
			return true;
		} catch {
			extensionReloading = false;
			return false;
		}
	};

	const isStaleCoordinationAttempt = (error: unknown) => Number((error as any)?.statusCode ?? 0) === 404;

	const releaseStaleCoordinationAttempt = (operation: ActiveCoordinationOperation, phase: "renew" | "settle") => {
		if (activeOperation !== operation) return;
		for (const [receiptId, presentation] of pendingReceiptPresentations) {
			if (presentation.operationId === operation.id && presentation.operationAttempt === operation.attempt) pendingReceiptPresentations.delete(receiptId);
		}
		activeOperation = undefined;
		pendingOperationClaimId = "";
		lastLeaseRenewal = 0;
		pi.appendEntry("galpon-operation", {
			operationId: operation.id,
			operationAttempt: operation.attempt,
			status: "stale_attempt",
			phase,
		});
		emitActiveTodoOperationSnapshot();
		// Keep operationCompletions. A new fenced attempt can submit the saved
		// response without another model turn after a daemon restart or lease loss.
	};

	const settleCoordinationOperation = async (response: string, failure: string): Promise<boolean> => {
		const operation = activeOperation;
		if (!operation || operationSettling) return false;
		operationSettling = true;
		const boundedResponse = boundedDeliveryResponse(response);
		const saved = operationCompletions.get(operation.id);
		if (!saved || saved.response !== boundedResponse || saved.error !== failure || saved.attempt !== operation.attempt) {
			operationCompletions.set(operation.id, { response: boundedResponse, error: failure, attempt: operation.attempt });
			pi.appendEntry("galpon-operation", {
				operationId: operation.id,
				operationAttempt: operation.attempt,
				status: "completion_pending",
				response: boundedResponse,
				error: failure,
			});
		}
		try {
			await flushPendingResultObservations();
			if ([...pendingResultObservations.values()].some(observation => observation.operationId === operation.id && observation.operationAttempt === operation.attempt && !observation.presented)) return false;
			for (const [receiptId, presentation] of pendingReceiptPresentations) {
				if (presentation.operationId !== operation.id || presentation.operationAttempt !== operation.attempt) continue;
				if (presentation.toolCallId) {
					const persisted = (activeContext?.sessionManager?.getBranch?.() ?? []).some((entry: any) => entry?.type === "message" && entry.message?.role === "toolResult" && entry.message.toolCallId === presentation.toolCallId);
					if (!persisted) return false;
				}
				await presentReceipt(operation, receiptId, presentation.toolRequestId);
			}
			const value = await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/${encodeURIComponent(operation.id)}/settle`, operationBody(operation, `settle:${operation.id}:${operation.attempt}`, { response: boundedResponse, error: failure }));
			pi.appendEntry("galpon-operation", {
				operationId: operation.id,
				operationAttempt: operation.attempt,
				status: value?.parked ? "parked" : failure ? "failed" : "settled",
				operationState: String(value?.operation?.state ?? ""),
			});
			for (const [id, observation] of pendingResultObservations) if (observation.operationId === operation.id) pendingResultObservations.delete(id);
			// Keep the completion only when control work or a result already made
			// the parked operation ready. The next attempt takes those receipts
			// before it decides whether the saved completion is still final.
			if (!value?.parked || value?.operation?.state === "waiting") operationCompletions.delete(operation.id);
			if (!value?.parked) {
				clearTodoOperationAssociations(operation.id, operation.attempt);
				for (const [receiptId, persisted] of persistedOperationReceipts) if (persisted.operationId === operation.id) persistedOperationReceipts.delete(receiptId);
			}
			activeOperation = undefined;
			emitActiveTodoOperationSnapshot();
			await reconcileTodoOperationOwnership();
			return true;
		} catch (error) {
			if (isStaleCoordinationAttempt(error)) releaseStaleCoordinationAttempt(operation, "settle");
			else invalidateRegistration(error);
			return false;
		} finally {
			operationSettling = false;
		}
	};

	const receiptPrompt = (operation: ActiveCoordinationOperation, receipts: any[], results: any[]) => {
		const byID = new Map(results.map(result => [String(result.id ?? ""), result]));
		const sections = receipts.filter(receipt => receipt.kind === "result" || receipt.kind === "blocker").map(receipt => {
			const result: any = byID.get(String(receipt.resultId ?? ""));
			const body = String(result?.response ?? result?.error ?? "No durable result text was provided.");
			const label = receipt.kind === "blocker" || result?.status === "failed" ? "Durable blocker" : "Durable result";
			return `${label} for assignment ${String(receipt.messageId ?? "unknown")}:\n\n${body}`;
		});
		if (sections.length === 0) return "";
		const independent = !operation.parentMessageId && !operation.userEntryId;
		const instruction = independent
			? "This is a result from an earlier assignment, not a new assignment. Do not treat it as a reply to an unrelated user request. Do not repeat a completion report that was already given."
			: "Continue the original task from the saved conversation. Use these results, then give the final result for that task.";
		return `${sections.join("\n\n---\n\n")}\n\n---\n\n${instruction}`;
	};

	const recoverDirectOperation = async (): Promise<boolean> => {
		if (!protocolV2 || protocolMaintenance || !pendingDirectUserEntryId || activeOperation || !activeContext?.isIdle()) return false;
		const userEntryId = pendingDirectUserEntryId;
		const source = await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/direct`, {
			runtimeId,
			userEntryId,
			requestId: `direct:${userEntryId}`,
			protocolGeneration,
		});
		const operation: ActiveCoordinationOperation = {
			id: String(source.id), attempt: Number(source.attempt), kind: String(source.kind ?? "direct"),
			parentMessageId: String(source.parentMessageId ?? ""), userEntryId,
			claimId: `direct:${userEntryId}`, started: source.state === "running",
		};
		activeOperation = operation;
		emitActiveTodoOperationSnapshot();
		pi.appendEntry("galpon-operation", { operationId: operation.id, operationAttempt: operation.attempt, status: "claimed", userEntryId });
		const recovered = operationCompletions.get(operation.id);
		if (recovered) {
			pendingDirectUserEntryId = "";
			await settleCoordinationOperation(recovered.response, recovered.error);
			return true;
		}
		if (injectedOperationAttempts.has(`${operation.id}:${operation.attempt}`) && !operationCompletions.has(operation.id)) {
			activeOperation = undefined;
			emitActiveTodoOperationSnapshot();
			pi.appendEntry("galpon-operation", { operationId: operation.id, operationAttempt: operation.attempt, status: "direct_registration_registered", userEntryId });
			pendingDirectUserEntryId = "";
			return true;
		}
		try {
			pi.sendMessage({
				customType: "galpon-operation",
				content: "Resume the direct user objective from its durable Pi session entry. This is the same causal operation after registration recovery.",
				display: true,
				details: { operationId: operation.id, operationAttempt: operation.attempt, claimId: operation.claimId },
			}, { deliverAs: "followUp", triggerTurn: true });
			modelOperationAttempt = `${operation.id}:${operation.attempt}`;
			injectedOperationAttempts.add(`${operation.id}:${operation.attempt}`);
			pi.appendEntry("galpon-operation", { operationId: operation.id, operationAttempt: operation.attempt, status: "direct_registration_registered", userEntryId });
			pendingDirectUserEntryId = "";
			return true;
		} catch (error) {
			activeOperation = undefined;
			emitActiveTodoOperationSnapshot();
			throw error;
		}
	};

	const claimCoordinationOperation = async (): Promise<boolean> => {
		if (!protocolV2 || protocolMaintenance || activeOperation || !activeContext?.isIdle()) return false;
		if (!pendingOperationClaimId) pendingOperationClaimId = `operation:${runtimeId}:${operationClaimSequence++}`;
		let value: any;
		try {
			value = await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/claim`, {
				runtimeId,
				claimId: pendingOperationClaimId,
				requestId: pendingOperationClaimId,
				protocolGeneration,
			});
		} catch (error) {
			if (Number((error as any)?.statusCode ?? 0) === 404) {
				pendingOperationClaimId = "";
				return false;
			}
			throw error;
		}
		const delivery = value?.delivery;
		if (!delivery?.operation) {
			pendingOperationClaimId = "";
			return false;
		}
		const source = delivery.operation;
		const operation: ActiveCoordinationOperation = {
			id: String(source.id),
			attempt: Number(source.attempt),
			kind: String(source.kind ?? "direct"),
			parentMessageId: String(source.parentMessageId ?? ""),
			userEntryId: String(source.userEntryId ?? ""),
			message: delivery.message,
			claimId: pendingOperationClaimId,
			started: source.state === "running",
		};
		pendingOperationClaimId = "";
		activeOperation = operation;
		emitActiveTodoOperationSnapshot();
		pi.appendEntry("galpon-operation", { operationId: operation.id, operationAttempt: operation.attempt, status: "claimed", claimId: operation.claimId });
		if (injectedOperationAttempts.has(`${operation.id}:${operation.attempt}`) && !operationCompletions.has(operation.id)) {
			// The exact attempt already entered the Pi session before extension reload.
			// Do not duplicate steering. Let its lease recover to a new attempt.
			activeOperation = undefined;
			emitActiveTodoOperationSnapshot();
			pendingOperationClaimId = "";
			return false;
		}
		try {
		if (!operation.started) {
			await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/${encodeURIComponent(operation.id)}/start`, operationBody(operation, `start:${operation.id}:${operation.attempt}`));
			operation.started = true;
		}
		await flushPendingResultObservations();
		const recovered = operationCompletions.get(operation.id);
		const toolRequestId = `receipts:${operation.id}:${operation.attempt}`;
		const batch = await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/${encodeURIComponent(operation.id)}/receipts/take`, operationBody(operation, toolRequestId, { toolRequestId })) as CoordinationReceiptBatch;
		const receipts = Array.isArray(batch.receipts) ? batch.receipts : [];
		const results = Array.isArray(batch.results) ? batch.results : [];
		const alreadyAppliedReceiptIDs = new Set(recovered ? receipts.filter(receipt => {
			const persisted = persistedOperationReceipts.get(String(receipt.id ?? ""));
			return (receipt.kind === "result" || receipt.kind === "blocker") && persisted?.operationId === operation.id && persisted.operationAttempt === recovered.attempt;
		}).map(receipt => String(receipt.id ?? "")) : []);
		const resultsByID = new Map(results.map(result => [String(result.id ?? ""), result]));
		for (const receipt of receipts) {
			if (await processTodoLinkReceipt(operation, receipt)) continue;
			if (receipt.kind === "result" || receipt.kind === "blocker") {
				await presentReceipt(operation, String(receipt.id), toolRequestId, { receipt, result: resultsByID.get(String(receipt.resultId ?? "")) });
			}
		}
		const modelReceipts = receipts.filter(receipt => (receipt.kind === "result" || receipt.kind === "blocker") && !alreadyAppliedReceiptIDs.has(String(receipt.id ?? "")));
		if (modelReceipts.length > 0) {
			// A new result advances the objective. Its next model response replaces
			// the response that parked the prior attempt.
			operationCompletions.delete(operation.id);
		} else if (recovered) {
			// Control-only work does not change the model result. Re-submit the
			// saved completion after its durable local side effect is applied.
			await settleCoordinationOperation(recovered.response, recovered.error);
			return true;
		}
		let content: any = "";
		if (modelReceipts.length > 0) {
			content = receiptPrompt(operation, modelReceipts, results);
		} else if (operation.message && operation.attempt <= 1) {
			content = formatMessages([operation.message]);
		} else if (operation.userEntryId) {
			content = "Resume the direct user objective from its durable Pi session entry. This is the same causal operation after recovery.";
		}
		if (!content) {
			await settleCoordinationOperation("", "");
			return true;
		}
		lastAssistant = "";
		lastAssistantBatchId = operation.id;
		if (operation.message && deliveryImages(operation.message).length > 0) {
			// Use a real user message when an inbound delivery has images. Pi provider
			// adapters include image parts from user messages, but custom messages are
			// text-only. The durable galpon-operation entry above keeps the fenced
			// operation identity and attempt in the Pi session.
			pi.sendUserMessage(content, { deliverAs: "followUp" });
		} else {
			pi.sendMessage({
				customType: "galpon-operation",
				content,
				display: true,
				details: { operationId: operation.id, operationAttempt: operation.attempt, claimId: operation.claimId },
			}, { deliverAs: "followUp", triggerTurn: true });
		}
		modelOperationAttempt = `${operation.id}:${operation.attempt}`;
		injectedOperationAttempts.add(`${operation.id}:${operation.attempt}`);
		return true;
		} catch (error) {
			if (activeOperation === operation) activeOperation = undefined;
			emitActiveTodoOperationSnapshot();
			pendingOperationClaimId = operation.claimId;
			throw error;
		}
	};

	const poll = async () => {
		if (stopped || polling || !activeContext) return;
		polling = true;
		try {
			if (reviewUiActive) return;
			if (extensionReloadNeeded && reloadInstalledExtension()) return;
			if (!await ensureRegistered()) return;
			if (protocolV2 && await flushPendingResultObservations()) return;
			if (protocolV2) {
				await refreshProtocol(true);
				if (!registered || protocolMaintenance || directInputPending) return;
				if (activeOperation) {
					const pendingCompletion = operationCompletions.get(activeOperation.id);
					if (pendingCompletion) {
						await settleCoordinationOperation(pendingCompletion.response, pendingCompletion.error);
						return;
					}
					if (Date.now() - lastLeaseRenewal >= 30_000) {
						const operation = activeOperation;
						try {
							await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/${encodeURIComponent(operation.id)}/renew`, operationBody(operation, `renew:${operation.id}:${operation.attempt}`));
							lastLeaseRenewal = Date.now();
						} catch (error) {
							if (isStaleCoordinationAttempt(error)) {
								if (modelOperationAttempt === `${operation.id}:${operation.attempt}`) lastLeaseRenewal = Date.now();
								else releaseStaleCoordinationAttempt(operation, "renew");
							} else throw error;
						}
					}
					return;
				}
				if (!activeContext.isIdle()) return;
				if (await recoverDirectOperation()) return;
				if (await processTodoSettlement()) return;
				await claimCoordinationOperation();
				return;
			}
			if (completionPending) {
				await finishActive();
				return;
			}
			if (activeMessageIds.length !== 0 && deliveryRunActive) {
				await renewActiveLeases();
				return;
			}
			if (activeMessageIds.length !== 0) {
				await renewActiveLeases();
				if (injectionPending && activeContext.isIdle()) {
					const pending = activeMessageIds.map(id => activeMessages.get(id)).filter(Boolean);
					try {
						settleTodoResults(pending);
						pi.sendUserMessage(formatMessages(pending), { deliverAs: "followUp" });
						injectionPending = false;
					} catch {
						// Retry the in-process Pi injection without changing the durable claim.
					}
				}
				return;
			}
			const wasBusy = !activeContext.isIdle();
			const capacity = maxDeliveryBatchMessages - activeMessageIds.length;
			if (capacity <= 0) return;
			const messages = await claimMessages(capacity);
			if (messages.length === 0) {
				if (activeMessageIds.length === 0) {
					activeBatchId = "";
					nextClaimIndex = 0;
				}
				return;
			}

			const inbound: any[] = [];
			for (const message of messages) {
				if (message.kind === "result" && String(message.id ?? "").startsWith("result:") && awaitedMessageCounts.has(message.replyTo)) {
					// The active await returns this result through its original request.
					// The server consumes this delivered notification atomically.
					continue;
				}
				if (activeMessages.has(message.id)) {
					// A lease renewal or an idempotent claim can return an active
					// delivery again. Keep one batch member and use its latest attempt.
					activeMessages.set(message.id, message);
					continue;
				}
				const recovered = recoverableCompletions.get(message.id);
				if (recovered) {
					await completeDelivery(message, recovered.response, recovered.error);
					continue;
				}
				activeMessageIds.push(message.id);
				activeMessages.set(message.id, message);
				inbound.push(message);
				pi.appendEntry("galpon-delivery", {
					messageId: message.id,
					status: "delivered",
					batchId: activeBatchId,
					attempt: message.attempt,
					kind: message.kind,
					replyTo: message.replyTo,
				});
			}
			if (inbound.length === 0) return;
			const interruptingAwait = awaitInterrupts.size !== 0;
			for (const interrupt of awaitInterrupts) interrupt.abort();
			injectionPending = true;
			// Pi steering is delivered only after the active tool call finishes. Let
			// the local wait tool return first, then inject this claimed delivery as
			// a follow-up from the idle poll. This avoids an await/steer live-lock.
			if (interruptingAwait) return;
			const steering = deliveryRunActive || (wasBusy && inbound.every(message => message.kind === "result"));
			lastAssistant = "";
			lastAssistantBatchId = "";
			if (steering && !deliveryRunActive) {
				deliveryRunActive = true;
				deliveryRunBatchId = activeBatchId;
				lastLeaseRenewal = 0;
			}
			try {
				settleTodoResults(inbound);
				pi.sendUserMessage(formatMessages(inbound), { deliverAs: steering ? "steer" : "followUp" });
				injectionPending = false;
			} catch {
				// The durable claim remains active. A later idle poll retries injection.
				return;
			}
		} catch (error) {
			invalidateRegistration(error);
			// The daemon can restart while Pi stays open. Stable claim keys and
			// completion attempts reconcile requests with unknown HTTP outcomes.
		} finally {
			polling = false;
			schedule(registered ? (activeMessageIds.length !== 0 || activeOperation ? 700 : 350) : registrationDelay);
		}
	};

	pi.on("session_start", (_event, ctx) => {
		activeContext = ctx;
		stopped = false;
		registered = false;
		piLifecycleActive = ctx?.isIdle?.() === false;
		if (!extensionWatcherStarted && extensionPath) {
			extensionWatcherStarted = true;
			const watchExtensionFile = (path: string) => watchFile(path, { interval: 1000, persistent: false }, (current, previous) => {
				if (current.mtimeMs === previous.mtimeMs && current.size === previous.size) return;
				extensionReloadNeeded = true;
				schedule(0);
			});
			watchExtensionFile(extensionPath);
			watchExtensionFile(reviewExtensionPath);
		}
		ctx.ui.setTitle(`${agentTitle} · ${workspaceTitle}`);
		setDelegatedStatus();
		pi.setSessionName(agentTitle);
		const sessionId = ctx.sessionManager.getSessionId();
		const branch = ctx.sessionManager.getBranch();
		todoOperationTaskIds.clear();
		todoOwnershipKnowledge = "unknown";
		registration = { sessionId, sessionPath: ctx.sessionManager.getSessionFile() ?? "", branch };
		for (const entry of branch) {
			if (entry?.type === "custom_message" && entry.customType === "galpon-operation" && typeof entry.details?.operationId === "string") {
				injectedOperationAttempts.add(`${entry.details.operationId}:${Number(entry.details.operationAttempt)}`);
			}
			if (entry?.type !== "custom") continue;
			const data = entry.data ?? {};
			if (entry.customType === "galpon-operation" && data.status === "direct_registration_pending" && typeof data.userEntryId === "string") pendingDirectUserEntryId = data.userEntryId;
			if (entry.customType === "galpon-operation" && data.status === "direct_registration_registered" && data.userEntryId === pendingDirectUserEntryId) pendingDirectUserEntryId = "";
			if (entry.customType === "galpon-operation" && data.status === "result_observation_pending"
				&& typeof data.operationId === "string" && typeof data.toolCallId === "string" && Array.isArray(data.messageIds)) {
				pendingResultObservations.set(data.toolCallId, {
					operationId: data.operationId,
					operationAttempt: Number(data.operationAttempt),
					toolCallId: data.toolCallId,
					messageIds: data.messageIds.filter((id: unknown) => typeof id === "string" && id.length > 0),
				});
			} else if (entry.customType === "galpon-operation" && data.status === "result_observation_presented" && typeof data.toolCallId === "string") {
				const observation = pendingResultObservations.get(data.toolCallId);
				if (observation) observation.presented = true;
			} else if (entry.customType === "galpon-operation" && data.status === "result_observation_discarded" && typeof data.toolCallId === "string") {
				pendingResultObservations.delete(data.toolCallId);
			}
			if (entry.customType === "galpon-operation" && ["settled", "failed", "parked"].includes(String(data.status))) {
				for (const [id, observation] of pendingResultObservations) if (observation.operationId === data.operationId) pendingResultObservations.delete(id);
			}
			if (entry.customType === "galpon-delivery") {
				if (typeof data.messageId !== "string") continue;
				if (data.status === "completion_pending") {
					recoverableCompletions.set(data.messageId, { response: String(data.response ?? ""), error: String(data.error ?? "") });
				} else if (data.status === "completed" || data.status === "failed") {
					recoverableCompletions.delete(data.messageId);
				}
			}
			if (entry.customType === "galpon-operation" && data.status === "todo_globally_dissociated" && Number.isSafeInteger(data.todoId) && data.todoId > 0) {
				for (const [operationId, ids] of todoOperationTaskIds) {
					ids.delete(Number(data.todoId));
					if (ids.size === 0) todoOperationTaskIds.delete(operationId);
				}
			} else if (entry.customType === "galpon-operation" && data.status === "todo_associations_globally_cleared") {
				todoOperationTaskIds.clear();
			}
			if (entry.customType === "galpon-operation" && typeof data.operationId === "string") {
				if (data.status === "todo_associated" && Number.isSafeInteger(data.todoId) && data.todoId > 0) {
					const ids = todoOperationTaskIds.get(data.operationId) ?? new Set<number>();
					ids.add(Number(data.todoId));
					todoOperationTaskIds.set(data.operationId, ids);
				} else if (data.status === "todo_dissociated" && Number.isSafeInteger(data.todoId) && data.todoId > 0) {
					const ids = todoOperationTaskIds.get(data.operationId);
					ids?.delete(Number(data.todoId));
					if (ids?.size === 0) todoOperationTaskIds.delete(data.operationId);
				} else if (data.status === "todo_associations_cleared") {
					todoOperationTaskIds.delete(data.operationId);
				}
				if (data.status === "claimed" && typeof data.claimId === "string") pendingOperationClaimId = data.claimId;
				if (["settled", "failed", "parked"].includes(String(data.status))) pendingOperationClaimId = "";
				if (data.status === "todo_settlement_claimed" && typeof data.claimId === "string") pendingTodoSettlementClaimId = data.claimId;
				if (data.status === "todo_settlement_acknowledged") pendingTodoSettlementClaimId = "";
				if (data.status === "receipt_persisted" && typeof data.receiptId === "string") {
					pendingReceiptPresentations.set(data.receiptId, { operationId: data.operationId, operationAttempt: Number(data.operationAttempt), toolRequestId: String(data.toolRequestId ?? "") });
					persistedOperationReceipts.set(data.receiptId, { operationId: data.operationId, operationAttempt: Number(data.operationAttempt) });
				} else if (data.status === "receipt_presented" && typeof data.receiptId === "string") {
					pendingReceiptPresentations.delete(data.receiptId);
				}
				if (data.status === "completion_pending") {
					operationCompletions.set(data.operationId, { response: String(data.response ?? ""), error: String(data.error ?? ""), attempt: Number(data.operationAttempt) });
				} else if (data.status === "settled" || data.status === "failed" || data.status === "parked" && data.operationState === "waiting") {
					operationCompletions.delete(data.operationId);
					if (data.status === "settled" || data.status === "failed") {
						for (const [receiptId, persisted] of persistedOperationReceipts) if (persisted.operationId === data.operationId) persistedOperationReceipts.delete(receiptId);
					}
				}
			}
		}
		// An interrupted tool call with no saved result is not an observation.
		for (const [id, observation] of pendingResultObservations) {
			if (!branch.some((entry: any) => entry?.type === "message" && entry.message?.role === "toolResult" && entry.message.toolCallId === id && entry.message.isError !== true)) {
				pi.appendEntry("galpon-operation", { ...observation, status: "result_observation_discarded" });
				pendingResultObservations.delete(id);
			}
		}
		schedule(0);
		scheduleDelegatedStatus(0);
		const associationRefresh = setTimeout(emitActiveTodoOperationSnapshot, 0);
		associationRefresh.unref?.();
	});

	pi.on("input", async (event, ctx) => {
		if (event.source === "extension") return { action: "continue" as const };
		if (protocolV2 && activeOperation) {
			if (event.streamingBehavior === "steer" || event.streamingBehavior === "followUp" || !ctx.isIdle()) {
				return { action: "continue" as const };
			}
			ctx.ui.notify("Finish the active Galpón operation before you start another user objective.", "warning");
			ctx.ui.setEditorText(event.text);
			return { action: "handled" as const };
		}
		directInputPending = true;
		try {
			await refreshProtocol(true);
			if (protocolV2 && protocolMaintenance) {
				ctx.ui.notify("Galpón communication maintenance is active. The model did not start.", "warning");
				ctx.ui.setEditorText(event.text);
				return { action: "handled" as const };
			}
			if (protocolV2) {
				if (!await ensureRegistered()) throw new Error("runtime registration is not available");
				const userEntryId = stableDirectInputID(event, ctx);
				const source = await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/direct`, {
					runtimeId,
					userEntryId,
					requestId: `direct:${userEntryId}`,
					protocolGeneration,
				});
				activeOperation = {
					id: String(source.id), attempt: Number(source.attempt), kind: String(source.kind ?? "direct"),
					parentMessageId: String(source.parentMessageId ?? ""), userEntryId,
					claimId: `direct:${userEntryId}`, started: true,
				};
				emitActiveTodoOperationSnapshot();
				pi.appendEntry("galpon-operation", { operationId: activeOperation.id, operationAttempt: activeOperation.attempt, status: "claimed", userEntryId });
				pi.appendEntry("galpon-operation", { operationId: activeOperation.id, operationAttempt: activeOperation.attempt, status: "direct_registration_registered", userEntryId });
				modelOperationAttempt = `${activeOperation.id}:${activeOperation.attempt}`;
				pendingDirectUserEntryId = "";
			}
			return { action: "continue" as const };
		} catch (error) {
			invalidateRegistration(error);
			ctx.ui.notify(`Galpón did not start the model: ${error instanceof Error ? error.message : String(error)}`, "error");
			ctx.ui.setEditorText(event.text);
			return { action: "handled" as const };
		} finally {
			directInputPending = false;
		}
	});

	pi.on("before_agent_start", async (event) => {
		return {
			systemPrompt: event.systemPrompt + `\n\nYou are the durable Galpón agent ${agentTitle} in workspace ${workspaceTitle}.${agentRole ? ` Your role is ${agentRole}.` : ""}${placement ? ` Your placement is ${placement}.` : ""} Galpón provides optional tools for repository, workspace inspection, agent, and cross-agent operations. Agent roles and names do not have special built-in behavior. Use these tools only when the user requests coordination or when the current task clearly requires it. Workspaces are user-managed. Do not create or request a new workspace. Create background delegated agents only in your current workspace. Use the inform act for one-way coordination that does not need an agent reply. Galpón attaches new reply-bearing work to the current objective automatically. When a delegated request owns one of your todos, pass its id as todo_id so Galpón can reconcile it when the result settles, and keep separate todos for review or integration work. Use galpon_update_agent only to append instructions to a queued, unclaimed assignment; a running or completed assignment is not changed. Progress reports are only for active inbound delegated requests, not direct user turns or completed-result notifications. Galpón delivers one queued cross-agent message per Pi turn so each response stays correlated to its request. Address every delivered message. A delivery with a completed correlated result is a notification about earlier work, not a new work request. For a current delivery, put the result in your final assistant response. Do not use galpon_send_agent to return the current delivery result. Galpón records and routes the final response automatically. Agents that you create are recorded as your descendants. Use galpon_cleanup_agents only when the user explicitly asks for cleanup: list the agents, select the exact relevant IDs, and do not clean agents whose results are still needed. Never create a synchronous wait cycle by asking an agent to wait for you while you wait for it. galpon_await_agent and galpon_await_agents are bounded observations and do not cancel unfinished work. Multi-message outcomes stay in message ID order. galpon_read_message and the await tools can observe the same durable result again.`,
		};
	});

	pi.on("context", event => {
		const messages = event.messages.map(canonicalMessageImages);
		return messages.some((message, index) => message !== event.messages[index]) ? { messages } : undefined;
	});

	pi.on("message_start", event => {
		if (event.message?.role !== "assistant") return;
		conversationMirror.enqueue(conversationEvent("assistant_message_start", {
			role: "assistant",
			content: normalContent(event.message.content),
			isDelta: false,
			createdAt: messageCreatedAt(event.message),
		}));
	});
	pi.on("message_update", event => {
		const update = event.assistantMessageEvent;
		if (update?.type !== "text_delta" || typeof update.delta !== "string" || update.delta.length === 0) return;
		conversationMirror.enqueue(conversationEvent("assistant_text_delta", {
			role: "assistant",
			content: update.delta,
			isDelta: true,
		}));
	});
	pi.on("message_end", async (event, ctx) => {
		const message = event.message;
		const sessionId = ctx.sessionManager.getSessionId();
		if (message?.role === "user" || message?.role === "custom" && message.customType === "galpon-operation") {
			conversationMirror.enqueueFinalMessage(conversationEvent("user_message", {
				role: "user",
				content: normalContent(message.content),
				images: conversationImages(message.content),
				createdAt: messageCreatedAt(message),
			}), message, ctx.sessionManager, sessionId, "user");
			return;
		}
		if (message?.role === "assistant") {
			if (protocolV2 && activeOperation) {
				lastAssistant = assistantText(message);
				lastAssistantBatchId = activeOperation.id;
			} else if (deliveryRunActive) {
				lastAssistant = assistantText(message);
				lastAssistantBatchId = deliveryRunBatchId;
			}
			conversationMirror.enqueueFinalMessage(conversationEvent("assistant_message_end", {
				role: "assistant",
				content: normalContent(message.content),
				images: normalImages(message.content),
				isDelta: false,
				createdAt: messageCreatedAt(message),
			}), message, ctx.sessionManager, sessionId, "assistant");
			return;
		}
		if (message?.role === "toolResult") {
			const pending = pendingToolEnds.get(message.toolCallId);
			pendingToolEnds.delete(message.toolCallId);
			if (pendingResultObservations.has(String(message.toolCallId))) {
				const observeAfterPersistence = () => {
					if (stopped) return;
					void flushPendingResultObservations().catch(error => {
						invalidateRegistration(error);
						schedule(0);
					});
				};
				const deferred = setTimeout(observeAfterPersistence, 0);
				deferred.unref?.();
			}
			conversationMirror.enqueueFinalMessage(conversationEvent("tool_execution_end", {
				content: normalContent(message.content),
				images: normalImages(message.content),
				toolName: String(message.toolName ?? "tool"),
				toolCallId: String(message.toolCallId),
				isDelta: false,
				isError: Boolean(pending?.isError ?? message.isError),
				createdAt: messageCreatedAt(message),
			}), message, ctx.sessionManager, sessionId, "tool-end");
		}
	});
	pi.on("tool_execution_start", (event, ctx) => {
		const entry = toolCallEntry(ctx.sessionManager, event.toolCallId);
		const sessionId = ctx.sessionManager.getSessionId();
		conversationMirror.enqueue(conversationEvent("tool_execution_start", {
			eventId: entry ? stablePiEventId(sessionId, entry.id, `tool-start-${event.toolCallId}`) : undefined,
			piEntryId: entry?.id,
			content: readableJSON(event.args),
			toolName: event.toolName,
			toolCallId: event.toolCallId,
			isDelta: false,
		}));
	});
	pi.on("tool_execution_update", event => {
		conversationMirror.enqueue(conversationEvent("tool_execution_update", {
			content: toolOutput(event.partialResult),
			toolName: event.toolName,
			toolCallId: event.toolCallId,
			isDelta: false,
		}));
	});
	pi.on("tool_execution_end", event => {
		pendingToolEnds.set(event.toolCallId, { isError: event.isError });
	});
	pi.on("session_before_compact", event => {
		conversationMirror.enqueue(conversationEvent("compaction_start", { content: event.reason }));
	});
	pi.on("session_compact", event => {
		const entry = event.compactionEntry;
		conversationMirror.enqueue(conversationEvent("compaction_end", {
			eventId: stablePiEventId(activeContext.sessionManager.getSessionId(), entry.id, "compaction"),
			piEntryId: entry.id,
			content: entry.summary,
			isDelta: false,
			createdAt: entryCreatedAt(entry),
		}));
	});
	pi.on("agent_start", async () => {
		piLifecycleActive = true;
		const contextualRefresh = delegatedStatusRefreshDone;
		if (contextualRefresh) await contextualRefresh;
		if (protocolV2 && activeOperation) {
			modelOperationAttempt = `${activeOperation.id}:${activeOperation.attempt}`;
			lastLeaseRenewal = Date.now();
			lastAssistant = "";
			lastAssistantBatchId = activeOperation.id;
		}
		if (!protocolV2 && activeMessageIds.length !== 0 && !deliveryRunActive) {
			deliveryRunActive = true;
			injectionPending = false;
			deliveryRunBatchId = activeBatchId;
			lastLeaseRenewal = 0;
			lastAssistant = "";
			lastAssistantBatchId = "";
		}
		if (registered) await api("POST", `/v1/runtime/agents/${agentId}/status`, { runtimeId, status: "running" }).catch(() => {});
		scheduleDelegatedStatus(0);
	});
	pi.on("agent_settled", async () => {
		if (protocolV2 && activeOperation) {
			const response = lastAssistantBatchId === activeOperation.id ? boundedDeliveryResponse(lastAssistant) : "";
			const failure = response ? "" : "Pi agent settled without a final text response for this operation";
			await settleCoordinationOperation(response, failure);
		} else if (deliveryRunActive) {
			deliveryRunActive = false;
			completionPending = true;
			await finishActive();
		}
		modelOperationAttempt = "";
		if (registered) await api("POST", `/v1/runtime/agents/${agentId}/status`, { runtimeId, status: "idle" }).catch(error => invalidateRegistration(error));
		piLifecycleActive = activeContext?.isIdle?.() !== true;
		schedule(0);
		// This timer runs after the normal agent_settled handlers. It can only
		// publish supplemental state when Pi still reports itself as idle.
		scheduleDelegatedStatus(0);
	});
	pi.on("session_before_switch", (_event, ctx) => {
		ctx.ui.notify("This Pi session belongs to one Galpón agent. Open or create another agent with Ctrl-K.", "warning");
		return { cancel: true };
	});
	pi.on("session_before_fork", (_event, ctx) => {
		ctx.ui.notify("Create another Galpón agent with Ctrl-K instead of forking this session.", "warning");
		return { cancel: true };
	});
	pi.on("session_shutdown", async event => {
		stopped = true;
		piLifecycleActive = true;
		pi.events.emit(todoOperationSnapshotEvent, { schemaVersion: 1, activeTaskIds: [], ownershipKnowledge: "unknown" });
		publishWorkSnapshot([], false);
		if (extensionWatcherStarted && extensionPath) {
			unwatchFile(extensionPath);
			unwatchFile(reviewExtensionPath);
		}
		if (timer) clearTimeout(timer);
		if (delegatedStatusTimer) clearTimeout(delegatedStatusTimer);
		conversationMirror.stop();
		// Pi reloads this extension inside the same process and with the same
		// runtime ID. Keep server ownership so the new instance can register.
		if (event.reason !== "reload") {
			await api("POST", `/v1/runtime/agents/${agentId}/stop`, { runtimeId }).catch(() => {});
		}
	});
}
