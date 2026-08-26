import { randomUUID } from "node:crypto";
import { request as httpRequest } from "node:http";
import { unwatchFile, watchFile } from "node:fs";
import { StringEnum, Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Key, matchesKey, truncateToWidth, visibleWidth } from "@earendil-works/pi-tui";

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
const workSnapshotEvent = "galpon:work:snapshot:v1";

const socketPath = process.env.GALPON_SOCKET ?? "";
const agentId = process.env.GALPON_AGENT_ID ?? "";
const agentTitle = process.env.GALPON_AGENT_TITLE ?? "Agent";
const agentRole = process.env.GALPON_AGENT_ROLE ?? "";
const workspaceTitle = process.env.GALPON_WORKSPACE_TITLE ?? "Workspace";
const workspaceId = process.env.GALPON_WORKSPACE_ID ?? "";
const placement = process.env.GALPON_PLACEMENT ?? "";
const runtimeId = process.env.GALPON_RUNTIME_ID ?? "";
const extensionPath = process.env.GALPON_PI_EXTENSION ?? "";
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
	if (/\[delivery [A-Za-z0-9:_-]{1,64}\]/.test(normalContent(content))) return [];
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

type OperationsRow = { item: any; depth: number };

function operationsRows(items: any[], depth = 0, output: OperationsRow[] = []): OperationsRow[] {
	for (const item of Array.isArray(items) ? items : []) {
		output.push({ item, depth });
		operationsRows(item?.children, depth + 1, output);
	}
	return output;
}

function operationMark(state: string): string {
	if (state === "started" || state === "running") return "◐";
	if (state === "queued" || state === "starting") return "○";
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
	const rows = operationsRows(value?.work ?? []);
	const summary = value?.summary ?? {};
	const truncated = value?.truncation?.truncated === true ? " · more facts omitted" : "";
	const header = theme.fg("accent", theme.bold(`GALPÓN  Operations · ${plainLabel(value?.workspace?.title, workspaceTitle, 96)}`));
	const queue = value?.queue ?? {};
	const summaryLine = `${Number(summary.agents ?? 0)} agents · ${Number(summary.activeWork ?? 0)} active · ${Number(summary.queuedWork ?? 0)} queued work · ${Number(queue.inboundQueued ?? 0)} durable inbound queued · ${Number(queue.inboundClaimed ?? 0)} durable claims · ${Number(summary.reportedBlockers ?? 0)} reported blockers · ${Number(summary.staleObservations ?? 0)} stale observations${truncated}`;
	const outline = [theme.fg("muted", theme.bold("WORK OUTLINE"))];
	if (rows.length === 0) outline.push(theme.fg("dim", "No active or recent delegated work"));
	const visibleStart = selected >= 8 ? selected - 7 : 0;
	for (let index = visibleStart; index < rows.length && outline.length < 10; index++) {
		const row = rows[index];
		const state = String(row.item?.observation?.state ?? "unknown");
		const prefix = index === selected ? "❯ " : "  ";
		const markColor = state === "completed" ? "success" : ["failed", "canceled", "expired"].includes(state) ? "error" : state === "started" ? "warning" : "dim";
		const mark = theme.fg(markColor, operationMark(state));
		const label = `${prefix}${"  ".repeat(Math.min(row.depth, 5))}${mark} ${plainLabel(row.item?.title, "Delegated work", 96)} · ${plainLabel(row.item?.priority, "recent fact", 40).replaceAll("_", " ")}`;
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
	const agents = [theme.fg("muted", theme.bold("AGENT RUNTIME"))];
	for (const agent of (Array.isArray(value?.agents) ? value.agents : []).slice(0, 6)) {
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
		? [theme.fg("accent", theme.bold("GALPÓN  Operations")), theme.fg("muted", "Loading current workspace facts…"), theme.fg("dim", "q close")]
		: [theme.fg("error", theme.bold("Operations unavailable")), theme.fg("muted", "Galpón could not load this workspace. Close this view and open it again."), theme.fg("dim", "q close")];
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
			if (Number(value?.version) !== 1 || String(value?.workspace?.id ?? "") !== workspaceId) throw new Error("invalid operations projection");
			this.value = value;
			this.selected = Math.min(this.selected, Math.max(0, operationsRows(value?.work ?? []).length - 1));
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
		const rows = operationsRows(this.value?.work ?? []);
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
	let operationClaimSequence = 0;
	let pendingOperationClaimId = "";
	let pendingTodoSettlementClaimId = "";
	let operationSettling = false;
	let operationRequestedPark = false;
	let directInputPending = false;
	let pendingDirectUserEntryId = "";
	const operationCompletions = new Map<string, { response: string; error: string }>();
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
	const pendingAwaitPresentations = new Map<string, Array<{ receiptId: string; toolRequestId: string }>>();
	const todoAcknowledgements = new Map<string, any>();
	pi.events.on(todoAckEvent, value => {
		const acknowledgement = value as any;
		if (acknowledgement?.schemaVersion === 1 && typeof acknowledgement.operationId === "string") {
			todoAcknowledgements.set(acknowledgement.operationId, acknowledgement);
		}
	});

	const setDelegatedStatus = (count?: number) => {
		const value = count === undefined ? "…" : String(count);
		activeContext?.ui.setStatus("galpon", `🛖  ${workspaceTitle}  ·  🤖 ${value}`);
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
		try {
			if (!await ensureRegistered()) return;
			const [status, work] = await Promise.all([
				api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/delegated-status`, { runtimeId }),
				api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/work`, { runtimeId }),
			]);
			const count = Number(status?.activeDelegatedAgents);
			if (Number.isSafeInteger(count) && count >= 0) setDelegatedStatus(count);
			pi.events.emit(workSnapshotEvent, { schemaVersion: 1, work: Array.isArray(work?.work) ? work.work : [], truncated: work?.truncated === true });
		} catch {
			// Keep the last known count and work snapshot while the daemon reconnects.
		} finally {
			delegatedStatusRefreshing = false;
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
		if (status === 401 || status === 409 || status === 503 || /runtime|register|generation|maintenance/i.test(message)) {
			registered = false;
		}
	};

	const refreshProtocol = async (force = false) => {
		if (!force && Date.now() - protocolObservedAt < 500) return;
		const state = await api("GET", "/v1/communication/protocol");
		protocolObservedAt = Date.now();
		protocolMaintenance = state?.maintenance === true;
		if (state?.complete === true && Number.isSafeInteger(Number(state.generation)) && Number(state.generation) > 1) {
			const nextGeneration = Number(state.generation);
			if (nextGeneration !== protocolGeneration) registered = false;
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

	const latestTodoSnapshot = (operationId: string): string => {
		const branch: any[] = activeContext?.sessionManager?.getBranch?.() ?? [];
		for (let index = branch.length - 1; index >= 0; index--) {
			const entry = branch[index];
			if (entry?.type === "custom" && entry.customType === "rpiv-todo:galpon-state:v1" && entry.data?.operationId === operationId) {
				return JSON.stringify(entry.data);
			}
		}
		return "";
	};

	const presentReceipt = async (operation: ActiveCoordinationOperation, receiptId: string, toolRequestId: string, payload?: any) => {
		const existing = pendingReceiptPresentations.get(receiptId);
		pendingReceiptPresentations.set(receiptId, { operationId: operation.id, operationAttempt: operation.attempt, toolRequestId, ...(existing?.toolCallId ? { toolCallId: existing.toolCallId } : {}) });
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
		}
	};

	const callTool = async (name: string, args: Record<string, any>, signal: AbortSignal | undefined, toolCallId: string) => {
		let lastError: unknown;
		const retryable = name === "list_repositories" || name === "list_workspaces" || name === "list_agents"
			|| name === "read_message" || name === "await_agent" || name === "await_agents" || name === "send_agent";
		for (let attempt = 0; attempt < (retryable ? 3 : 1); attempt++) {
			if (!await ensureRegistered()) throw new Error("Galpón runtime registration is not available");
			if (protocolV2 && !activeOperation) throw new Error("Galpón protocol v2 tool calls require an active operation");
			try {
				return await api("POST", `/v1/runtime/tools/${name}`, {
					agentId,
					runtimeId,
					requestId: toolCallId,
					toolCallId,
					...(protocolV2 && activeOperation ? {
						operationId: activeOperation.id,
						operationAttempt: activeOperation.attempt,
						protocolGeneration,
						currentMessageId: activeOperation.parentMessageId,
						currentAttempt: activeOperation.attempt,
					} : {
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
		name: "galpon_create_workspace",
		label: "Create workspace",
		description: "Create an empty durable Galpón coordination workspace for future foreground agents. Never create a workspace for a background delegated agent; use your current workspace for delegated agents.",
		parameters: Type.Object({
			title: Type.String({ description: "Workspace title" }),
		}),
		async execute(id, params, signal) { return toolResult(await callTool("create_workspace", params, signal, id)); },
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
		description: "Create and start a durable background Pi agent with an independent context source and file placement. If no repository, placement agent, or cwd is set, Galpón creates a private managed directory for the agent. It runs without a Herdr tab until the user promotes it. If prompt is set, Galpón queues it before Pi starts so the agent begins work as soon as its runtime is ready. The result then includes initialMessage, whose ID can be used with galpon_read_message or galpon_await_agent. Omit result_mode normally: Galpón uses join during an active inbound delivery and notify during a direct user turn. Use notify explicitly only when the result must remain useful after the current turn finishes.",
		parameters: Type.Object({
			title: Type.String({ description: "Agent title" }),
			workspace: Type.String({ description: "Workspace ID or exact title. For a background delegated agent, use your current workspace." }),
			role: Type.Optional(Type.String({ description: "Optional role, such as implementer, reviewer, or coordinator" })),
			prompt: Type.Optional(Type.String({ description: "Initial work request to queue before the new agent starts" })),
			result_mode: Type.Optional(Type.Union([Type.Literal("join"), Type.Literal("notify")], { description: "Omit normally. Galpón selects join during an active inbound delivery and notify during a direct user turn. Set notify only for a detached result that must remain useful later." })),
			todo_id: Type.Optional(Type.Integer({ minimum: 1, description: "Parent todo ID that this delegated request owns. Galpón forces notify mode and completes it when a successful result returns." })),
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
			if (todo_id !== undefined) request.result_mode = "notify";
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
		description: "Queue a typed durable message for another Galpón agent and start that agent if necessary. request and query require a reply. inform is one-way coordination and does not send the target's final reply back. Omit result_mode normally: Galpón uses join during an active inbound delivery and notify during a direct user turn. Use notify explicitly only for detached work that must remain useful after the current turn finishes. Returns a message ID immediately. Do not use this tool to return the result of a delivery that you are processing; put that complete result in your final assistant response.",
		parameters: Type.Object({
			agent: Type.String({ description: "Target agent ID or exact title" }),
			prompt: Type.String({ description: "Message text" }),
			act: Type.Optional(Type.Union([Type.Literal("request"), Type.Literal("query"), Type.Literal("inform")], { description: "Message intent. Defaults to request." })),
			result_mode: Type.Optional(Type.Union([Type.Literal("join"), Type.Literal("notify")], { description: "For request or query, omit normally. Galpón selects join during an active inbound delivery and notify during a direct user turn. Set notify only for a detached result." })),
			todo_id: Type.Optional(Type.Integer({ minimum: 1, description: "Parent todo ID that this request owns. Galpón forces notify mode and completes it when a successful result returns." })),
			todo_policy: Type.Optional(Type.Union([Type.Literal("complete_on_success"), Type.Literal("annotate")], { description: "How the linked todo changes when the result returns. Defaults to complete_on_success." })),
		}),
		async execute(id, params, signal) {
			if (params.todo_id !== undefined && params.act === "inform") throw new Error("todo_id requires request or query intent");
			const { todo_id, todo_policy, ...request } = params;
			if (todo_id !== undefined) request.result_mode = "notify";
			const value = await callTool("send_agent", protocolV2 ? { ...request, todo_id, todo_policy } : request, signal, id);
			if (!protocolV2 && todo_id !== undefined) value.todoLink = linkTodo(todo_id, todo_policy, value, value);
			return toolResult(value);
		},
	});
	pi.registerTool({
		name: "galpon_report_progress",
		label: "Report delegated work progress",
		description: "Report one safe, factual checkpoint only while processing an active inbound delegated request. Direct user turns and completed-result notifications are not eligible. An unavailable report returns recorded=false. The update is attempt-fenced and does not wake the parent model. Do not include reasoning, prompts, tool data, secrets, paths, percentages, estimates, or ETA values.",
		promptSnippet: "Report a safe checkpoint only for an active inbound delegated request",
		promptGuidelines: ["Use galpon_report_progress only for meaningful phase, milestone, blocker, or factual-count changes while processing an active inbound delegated request. Do not use it for direct user turns or completed-result notifications."],
		parameters: Type.Object({
			version: Type.Literal(1),
			event_id: Type.String({ minLength: 1, maxLength: 100, description: "Stable unique ID for this report" }),
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
			try {
				return toolResult(await callTool("report_progress", params, signal, id));
			} catch (error) {
				if (signal?.aborted) throw error;
				if (isUnavailableProgressError(error)) return toolResult(unavailableProgressResult());
				const status = Number((error as any)?.statusCode ?? 0);
				if (status > 0 && status < 500) throw error;
				try {
					return toolResult(await callTool("report_progress", params, signal, id));
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
		async execute(id, params, signal) { return toolResult(await callTool("read_message", params, signal, id)); },
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
				operationRequestedPark = value?.status === "parked";
				const presentations = (value?.outcomes ?? []).flatMap((outcome: any) => outcome?.receiptId ? [{ receiptId: String(outcome.receiptId), toolRequestId: id }] : []);
				if (presentations.length > 0) {
					pendingAwaitPresentations.set(id, presentations);
					for (const presentation of presentations) {
						if (activeOperation) pendingReceiptPresentations.set(presentation.receiptId, { operationId: activeOperation.id, operationAttempt: activeOperation.attempt, toolRequestId: id, toolCallId: id });
					}
				}
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
				operationRequestedPark = value?.status === "parked" || value?.waitStatus === "pending";
				if (value?.receiptId) {
					const receiptId = String(value.receiptId);
					pendingAwaitPresentations.set(id, [{ receiptId, toolRequestId: id }]);
					if (activeOperation) pendingReceiptPresentations.set(receiptId, { operationId: activeOperation.id, operationAttempt: activeOperation.attempt, toolRequestId: id, toolCallId: id });
				}
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
		description: "Open the read-only Galpón workspace operations cockpit",
		handler: async (_args, ctx) => {
			if (ctx.mode !== "tui") {
				ctx.ui.notify("The Operations cockpit is available in the interactive terminal.", "info");
				return;
			}
			if (!workspaceId || !await ensureRegistered()) {
				ctx.ui.notify("Galpón could not load this workspace.", "error");
				return;
			}
			await ctx.ui.custom<void>((tui, theme, _keybindings, done) => new OperationsCockpit(
				theme,
				() => tui.requestRender(),
				() => done(undefined),
				(signal) => api("GET", `/v1/workspaces/${encodeURIComponent(workspaceId)}/operations`, undefined, signal),
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
		if (!extensionReloadNeeded || extensionReloading || stopped || activeMessageIds.length !== 0 || activeOperation || !activeContext?.isIdle()) return false;
		extensionReloading = true;
		try {
			pi.sendUserMessage("/galpon-reload-extension", { expandPromptTemplates: true });
			return true;
		} catch {
			extensionReloading = false;
			return false;
		}
	};

	const settleCoordinationOperation = async (response: string, failure: string): Promise<boolean> => {
		const operation = activeOperation;
		if (!operation || operationSettling) return false;
		operationSettling = true;
		const boundedResponse = boundedDeliveryResponse(response);
		const saved = operationCompletions.get(operation.id);
		if (!saved || saved.response !== boundedResponse || saved.error !== failure) {
			operationCompletions.set(operation.id, { response: boundedResponse, error: failure });
			pi.appendEntry("galpon-operation", {
				operationId: operation.id,
				operationAttempt: operation.attempt,
				status: "completion_pending",
				response: boundedResponse,
				error: failure,
			});
		}
		try {
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
			if (!value?.parked || value?.operation?.state === "waiting") operationCompletions.delete(operation.id);
			activeOperation = undefined;
			return true;
		} catch (error) {
			invalidateRegistration(error);
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
			return `${label} for message ${String(receipt.messageId ?? "unknown")} [receipt ${String(receipt.id ?? "unknown")}]:\n\n${body}`;
		});
		if (sections.length === 0) return "";
		const independent = !operation.parentMessageId && !operation.userEntryId;
		const instruction = independent
			? "Process this independent notification in this operation. Do not treat it as a reply to an unrelated direct-user objective."
			: "Resume the same Pi objective and causal operation. Use these durable receipts, then give the final result for that objective.";
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
		operationRequestedPark = false;
		pi.appendEntry("galpon-operation", { operationId: operation.id, operationAttempt: operation.attempt, status: "claimed", userEntryId });
		const recovered = operationCompletions.get(operation.id);
		if (recovered) {
			pendingDirectUserEntryId = "";
			await settleCoordinationOperation(recovered.response, recovered.error);
			return true;
		}
		if (injectedOperationAttempts.has(`${operation.id}:${operation.attempt}`) && !operationCompletions.has(operation.id)) {
			activeOperation = undefined;
			pi.appendEntry("galpon-operation", { operationId: operation.id, operationAttempt: operation.attempt, status: "direct_registration_registered", userEntryId });
			pendingDirectUserEntryId = "";
			return true;
		}
		injectedOperationAttempts.add(`${operation.id}:${operation.attempt}`);
		pi.sendMessage({
			customType: "galpon-operation",
			content: "Resume the direct user objective from its durable Pi session entry. This is the same causal operation after registration recovery.",
			display: true,
			details: { operationId: operation.id, operationAttempt: operation.attempt, claimId: operation.claimId },
		}, { deliverAs: "followUp", triggerTurn: true });
		pi.appendEntry("galpon-operation", { operationId: operation.id, operationAttempt: operation.attempt, status: "direct_registration_registered", userEntryId });
		pendingDirectUserEntryId = "";
		return true;
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
		operationRequestedPark = false;
		pi.appendEntry("galpon-operation", { operationId: operation.id, operationAttempt: operation.attempt, status: "claimed", claimId: operation.claimId });
		if (injectedOperationAttempts.has(`${operation.id}:${operation.attempt}`) && !operationCompletions.has(operation.id)) {
			// The exact attempt already entered the Pi session before extension reload.
			// Do not duplicate steering. Let its lease recover to a new attempt.
			activeOperation = undefined;
			pendingOperationClaimId = "";
			return false;
		}
		try {
		if (!operation.started) {
			await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/${encodeURIComponent(operation.id)}/start`, operationBody(operation, `start:${operation.id}:${operation.attempt}`));
			operation.started = true;
		}
		const recovered = operationCompletions.get(operation.id);
		if (recovered) {
			await settleCoordinationOperation(recovered.response, recovered.error);
			return true;
		}
		const toolRequestId = `receipts:${operation.id}:${operation.attempt}`;
		const batch = await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/${encodeURIComponent(operation.id)}/receipts/take`, operationBody(operation, toolRequestId, { toolRequestId })) as CoordinationReceiptBatch;
		const receipts = Array.isArray(batch.receipts) ? batch.receipts : [];
		const results = Array.isArray(batch.results) ? batch.results : [];
		const resultsByID = new Map(results.map(result => [String(result.id ?? ""), result]));
		for (const receipt of receipts) {
			if (await processTodoLinkReceipt(operation, receipt)) continue;
			if (receipt.kind === "result" || receipt.kind === "blocker") {
				await presentReceipt(operation, String(receipt.id), toolRequestId, { receipt, result: resultsByID.get(String(receipt.resultId ?? "")) });
			}
		}
		const modelReceipts = receipts.filter(receipt => receipt.kind === "result" || receipt.kind === "blocker");
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
		injectedOperationAttempts.add(`${operation.id}:${operation.attempt}`);
		pi.sendMessage({
			customType: "galpon-operation",
			content,
			display: true,
			details: { operationId: operation.id, operationAttempt: operation.attempt, claimId: operation.claimId },
		}, { deliverAs: "followUp", triggerTurn: true });
		return true;
		} catch (error) {
			if (activeOperation === operation) activeOperation = undefined;
			pendingOperationClaimId = operation.claimId;
			throw error;
		}
	};

	const poll = async () => {
		if (stopped || polling || !activeContext) return;
		polling = true;
		try {
			if (extensionReloadNeeded && reloadInstalledExtension()) return;
			if (!await ensureRegistered()) return;
			if (protocolV2) {
				await refreshProtocol(true);
				if (protocolMaintenance || directInputPending) return;
				if (activeOperation) {
					const pendingCompletion = operationCompletions.get(activeOperation.id);
					if (pendingCompletion) {
						await settleCoordinationOperation(pendingCompletion.response, pendingCompletion.error);
						return;
					}
					if (Date.now() - lastLeaseRenewal >= 30_000) {
						await api("POST", `/v1/runtime/agents/${encodeURIComponent(agentId)}/operations/${encodeURIComponent(activeOperation.id)}/renew`, operationBody(activeOperation, `renew:${activeOperation.id}:${activeOperation.attempt}`));
						lastLeaseRenewal = Date.now();
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
		if (!extensionWatcherStarted && extensionPath) {
			extensionWatcherStarted = true;
			watchFile(extensionPath, { interval: 1000, persistent: false }, (current, previous) => {
				if (current.mtimeMs === previous.mtimeMs && current.size === previous.size) return;
				extensionReloadNeeded = true;
				schedule(0);
			});
		}
		ctx.ui.setTitle(`${agentTitle} · ${workspaceTitle}`);
		setDelegatedStatus();
		pi.setSessionName(agentTitle);
		const sessionId = ctx.sessionManager.getSessionId();
		const branch = ctx.sessionManager.getBranch();
		registration = { sessionId, sessionPath: ctx.sessionManager.getSessionFile() ?? "", branch };
		for (const entry of branch) {
			if (entry?.type === "custom_message" && entry.customType === "galpon-operation" && typeof entry.details?.operationId === "string") {
				injectedOperationAttempts.add(`${entry.details.operationId}:${Number(entry.details.operationAttempt)}`);
			}
			if (entry?.type !== "custom") continue;
			const data = entry.data ?? {};
			if (entry.customType === "galpon-operation" && data.status === "direct_registration_pending" && typeof data.userEntryId === "string") pendingDirectUserEntryId = data.userEntryId;
			if (entry.customType === "galpon-operation" && data.status === "direct_registration_registered" && data.userEntryId === pendingDirectUserEntryId) pendingDirectUserEntryId = "";
			if (entry.customType === "galpon-delivery") {
				if (typeof data.messageId !== "string") continue;
				if (data.status === "completion_pending") {
					recoverableCompletions.set(data.messageId, { response: String(data.response ?? ""), error: String(data.error ?? "") });
				} else if (data.status === "completed" || data.status === "failed") {
					recoverableCompletions.delete(data.messageId);
				}
			}
			if (entry.customType === "galpon-operation" && typeof data.operationId === "string") {
				if (data.status === "claimed" && typeof data.claimId === "string") pendingOperationClaimId = data.claimId;
				if (["settled", "failed", "parked"].includes(String(data.status))) pendingOperationClaimId = "";
				if (data.status === "todo_settlement_claimed" && typeof data.claimId === "string") pendingTodoSettlementClaimId = data.claimId;
				if (data.status === "todo_settlement_acknowledged") pendingTodoSettlementClaimId = "";
				if (data.status === "receipt_persisted" && typeof data.receiptId === "string") {
					pendingReceiptPresentations.set(data.receiptId, { operationId: data.operationId, operationAttempt: Number(data.operationAttempt), toolRequestId: String(data.toolRequestId ?? "") });
				} else if (data.status === "receipt_presented" && typeof data.receiptId === "string") {
					pendingReceiptPresentations.delete(data.receiptId);
				}
				if (data.status === "completion_pending") {
					operationCompletions.set(data.operationId, { response: String(data.response ?? ""), error: String(data.error ?? "") });
				} else if (data.status === "settled" || data.status === "failed" || data.status === "parked" && data.operationState === "waiting") {
					operationCompletions.delete(data.operationId);
				}
			}
		}
		schedule(0);
		scheduleDelegatedStatus(0);
	});

	pi.on("input", async (event, ctx) => {
		if (event.source === "extension") return { action: "continue" as const };
		if (protocolV2 && activeOperation) {
			ctx.ui.notify("Finish the active Galpón operation before you start another user objective.", "warning");
			ctx.ui.setEditorText(event.text);
			return { action: "handled" as const };
		}
		try {
			await refreshProtocol(true);
		} catch {
			if (configuredProtocolGeneration > 1) {
				ctx.ui.notify("Galpón cannot register this input while the daemon is unavailable.", "error");
				ctx.ui.setEditorText(event.text);
				return { action: "handled" as const };
			}
			return { action: "continue" as const };
		}
		if (protocolV2 && protocolMaintenance) {
			ctx.ui.notify("Galpón communication maintenance is active. The model did not start.", "warning");
			ctx.ui.setEditorText(event.text);
			return { action: "handled" as const };
		}
		directInputPending = protocolV2;
		return { action: "continue" as const };
	});

	const latestUserEntryID = (ctx: any): string => {
		const branch: any[] = ctx.sessionManager.getBranch();
		for (let index = branch.length - 1; index >= 0; index--) {
			const entry = branch[index];
			if (entry?.type === "message" && entry.message?.role === "user" && typeof entry.id === "string") return entry.id;
		}
		return "";
	};

	pi.on("before_agent_start", async (event, ctx) => {
		if (protocolV2 && !activeOperation) {
			const userEntryId = latestUserEntryID(ctx);
			if (userEntryId) {
				pendingDirectUserEntryId = userEntryId;
				pi.appendEntry("galpon-operation", { status: "direct_registration_pending", userEntryId });
			}
			try {
				if (!directInputPending || !userEntryId || !await ensureRegistered()) throw new Error("the stable Pi user entry is not registered");
				await refreshProtocol(true);
				if (protocolMaintenance) throw new Error("communication maintenance is active");
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
				operationRequestedPark = false;
				pi.appendEntry("galpon-operation", { operationId: activeOperation.id, operationAttempt: activeOperation.attempt, status: "claimed", userEntryId });
				pi.appendEntry("galpon-operation", { operationId: activeOperation.id, operationAttempt: activeOperation.attempt, status: "direct_registration_registered", userEntryId });
				pendingDirectUserEntryId = "";
				directInputPending = false;
			} catch (error) {
				directInputPending = false;
				invalidateRegistration(error);
				ctx.ui.notify(`Galpón did not start the model: ${error instanceof Error ? error.message : String(error)}`, "error");
				ctx.abort();
			}
		}
		return {
			systemPrompt: event.systemPrompt + `\n\nYou are the durable Galpón agent ${agentTitle} in workspace ${workspaceTitle}.${agentRole ? ` Your role is ${agentRole}.` : ""}${placement ? ` Your placement is ${placement}.` : ""} Galpón provides optional tools for repository, workspace, agent, and cross-agent operations. Agent roles and names do not have special built-in behavior. Use these tools only when the user requests coordination or when the current task clearly requires it. Create a new workspace only for work that a foreground agent will own. Always create background delegated agents in your current workspace; never create a new workspace for delegated work. Use the inform act for one-way coordination that does not need an agent reply. When a delegated request owns one of your todos, pass its id as todo_id so Galpón can reconcile it when the result settles; linked requests always use notify mode so late results are not suppressed, and you must keep separate todos for review or integration work. Normally omit result_mode. For request and query, Galpón selects join during an active inbound delivery and notify during a direct user turn. A joined result that arrives after the delivery settles remains durable but does not wake you. Set result_mode to notify only for detached work that must remain useful after the current turn. Progress reports are only for active inbound delegated requests, not direct user turns or completed-result notifications. Galpón delivers one queued cross-agent message per Pi turn so each response stays correlated to its request. Address every delivered message. A delivery with a completed correlated result is a notification about earlier work, not a new work request. For a current delivery, put the result in your final assistant response. Do not use galpon_send_agent to return the current delivery result. Galpón records and routes the final response automatically. Agents that you create are recorded as your descendants. Use galpon_cleanup_agents only when the user explicitly asks for cleanup: list the agents, select the exact relevant IDs, and do not clean agents whose results are still needed. Never create a synchronous wait cycle by asking an agent to wait for you while you wait for it. galpon_await_agents uses one global timeout and does not cancel unfinished agent work. Its outcomes stay in message ID order. A queued or delivered result is still pending; do not wait repeatedly without finishing the current turn or doing other useful work.`,
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
			if (protocolV2 && activeOperation) {
				const operation = activeOperation;
				const toolCallId = String(message.toolCallId);
				const presentations = pendingAwaitPresentations.get(toolCallId) ?? [];
				pendingAwaitPresentations.delete(toolCallId);
				const presentAfterPersistence = () => {
					if (stopped) return;
					const persisted = ctx.sessionManager.getBranch().some((entry: any) => entry?.type === "message" && entry.message?.role === "toolResult" && entry.message.toolCallId === toolCallId);
					if (!persisted) {
						const retry = setTimeout(presentAfterPersistence, 10);
						retry.unref?.();
						return;
					}
					for (const presentation of presentations) void presentReceipt(operation, presentation.receiptId, presentation.toolRequestId).catch(() => schedule(0));
				};
				if (presentations.length > 0) {
					const deferred = setTimeout(presentAfterPersistence, 0);
					deferred.unref?.();
				}
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
		if (protocolV2 && activeOperation) {
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
	});
	pi.on("agent_settled", async () => {
		if (protocolV2 && activeOperation) {
			const response = lastAssistantBatchId === activeOperation.id ? boundedDeliveryResponse(lastAssistant) : "";
			const failure = response || operationRequestedPark ? "" : "Pi agent settled without a final text response for this operation";
			await settleCoordinationOperation(response, failure);
		} else if (deliveryRunActive) {
			deliveryRunActive = false;
			completionPending = true;
			await finishActive();
		}
		if (registered) await api("POST", `/v1/runtime/agents/${agentId}/status`, { runtimeId, status: "idle" }).catch(error => invalidateRegistration(error));
		schedule(0);
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
		pi.events.emit(workSnapshotEvent, { schemaVersion: 1, work: [], truncated: false });
		if (extensionWatcherStarted && extensionPath) unwatchFile(extensionPath);
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
