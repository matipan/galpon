import { randomUUID } from "node:crypto";
import { request as httpRequest } from "node:http";
import { Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

type JSONValue = Record<string, any> | any[] | string | number | boolean | null;

type ConversationEventKind =
	| "user_message"
	| "assistant_message_start"
	| "assistant_reasoning_start"
	| "assistant_reasoning_delta"
	| "assistant_reasoning_end"
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
	createdAt: number;
};

type ConversationEvent = PendingConversationEvent & { runtimeSeq: number };

const maxPendingConversationEvents = 512;
const maxConversationBatchEvents = 50;
const maxConversationBatchBytes = 512 * 1024;
const maxConversationContentBytes = 64 * 1024;
// One request per Pi turn keeps each durable response correlated to one request.
const maxDeliveryBatchMessages = 1;
const maxDeliveryResponseBytes = 512 * 1024;

const socketPath = process.env.GALPON_SOCKET ?? "";
const agentId = process.env.GALPON_AGENT_ID ?? "";
const agentTitle = process.env.GALPON_AGENT_TITLE ?? "Agent";
const agentRole = process.env.GALPON_AGENT_ROLE ?? "";
const workspaceTitle = process.env.GALPON_WORKSPACE_TITLE ?? "Workspace";
const placement = process.env.GALPON_PLACEMENT ?? "";
const runtimeId = process.env.GALPON_RUNTIME_ID ?? "";

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
			if (current.type === "image" && typeof current.data === "string") {
				return { ...current, data: "[binary image omitted]" };
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
		if (part?.type === "image") return [`[image: ${String(part.mimeType ?? "unknown type")}]`];
		return [];
	}).join("\n");
}

function reasoningContent(part: any): string {
	return part?.type === "thinking" ? String(part.thinking ?? "") : "";
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
		if (entry?.type === "message" && entry.message?.role === "user") {
			yield conversationEvent("user_message", {
				eventId: stablePiEventId(sessionId, entry.id, "user"),
				piEntryId: entry.id,
				role: "user",
				content: normalContent(entry.message.content),
				createdAt: entryCreatedAt(entry),
			});
			continue;
		}
		if (entry?.type === "message" && entry.message?.role === "assistant") {
			const createdAt = entryCreatedAt(entry);
			for (const [index, part] of (Array.isArray(entry.message.content) ? entry.message.content : []).entries()) {
				const reasoning = reasoningContent(part);
				if (reasoning) {
					yield conversationEvent("assistant_reasoning_start", {
						eventId: stablePiEventId(sessionId, entry.id, `reasoning-start-${index}`),
						piEntryId: entry.id,
						role: "assistant",
						createdAt,
					});
					yield conversationEvent("assistant_reasoning_end", {
						eventId: stablePiEventId(sessionId, entry.id, `reasoning-end-${index}`),
						piEntryId: entry.id,
						role: "assistant",
						content: reasoning,
						isDelta: false,
						createdAt,
					});
				}
			}
			yield conversationEvent("assistant_message_end", {
				eventId: stablePiEventId(sessionId, entry.id, "assistant"),
				piEntryId: entry.id,
				role: "assistant",
				content: normalContent(entry.message.content),
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
		if ((tail?.kind === "assistant_text_delta" && event.kind === "assistant_text_delta")
			|| (tail?.kind === "assistant_reasoning_delta" && event.kind === "assistant_reasoning_delta")) {
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
			if (entry?.type !== "message") continue;
			const candidate = entry.message;
			const sameReference = candidate === expected;
			const sameValue = candidate?.role === expected?.role
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
			let index = this.pending.findIndex(event => event.kind === "assistant_text_delta" || event.kind === "assistant_reasoning_delta" || event.kind === "tool_execution_update");
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
		const timeout = setTimeout(() => controller.abort(), 2000);
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

function assistantText(message: any): string {
	if (message?.role !== "assistant") return "";
	return normalContent(message.content).trim();
}

export default function galpon(pi: ExtensionAPI) {
	let timer: NodeJS.Timeout | undefined;
	let stopped = false;
	let polling = false;
	let registered = false;
	let registrationPromise: Promise<boolean> | undefined;
	let registrationDelay = 250;
	let mirrorStarted = false;
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
	const awaitedMessageIds = new Set<string>();
	const conversationMirror = new ConversationMirror();
	const pendingToolEnds = new Map<string, { isError: boolean }>();

	const callTool = async (name: string, args: Record<string, any>, signal: AbortSignal | undefined, toolCallId: string) => {
		let lastError: unknown;
		const retryable = name === "list_repositories" || name === "list_workspaces" || name === "list_agents"
			|| name === "read_message" || name === "await_agent" || name === "send_agent";
		for (let attempt = 0; attempt < (retryable ? 3 : 1); attempt++) {
			if (!await ensureRegistered()) throw new Error("Galpón runtime registration is not available");
			try {
				return await api("POST", `/v1/runtime/tools/${name}`, {
					agentId,
					runtimeId,
					requestId: toolCallId,
					currentMessageId: activeMessageIds[0] ?? "",
					args,
				}, signal);
			} catch (error) {
				lastError = error;
				if (signal?.aborted) throw error;
				const status = Number((error as any)?.statusCode ?? 0);
				if (status > 0 && status < 500) throw error;
				if (/runtime|register/i.test(error instanceof Error ? error.message : String(error))) registered = false;
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
		description: "Create an empty durable Galpón coordination workspace. Agent placement creates worktrees later.",
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
		description: "Create and start a durable background Pi agent with an independent context source and file placement. It runs without a Herdr tab until the user promotes it. If prompt is set, Galpón queues it before Pi starts so the agent begins work as soon as its runtime is ready. The result then includes initialMessage, whose ID can be used with galpon_read_message or galpon_await_agent.",
		parameters: Type.Object({
			title: Type.String({ description: "Agent title" }),
			workspace: Type.String({ description: "Workspace ID or exact title" }),
			role: Type.Optional(Type.String({ description: "Optional role, such as implementer, reviewer, or coordinator" })),
			prompt: Type.Optional(Type.String({ description: "Initial work request to queue before the new agent starts" })),
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
			cwd: Type.Optional(Type.String({ description: "Absolute directory for an agent with no managed worktree" })),
		}),
		async execute(id, params, signal) { return toolResult(await callTool("create_agent", params, signal, id)); },
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
		description: "Queue a new durable work request for another Galpón agent and start that agent if necessary. Returns a message ID immediately. Do not use this tool to return the result of a delivery that you are processing; put that complete result in your final assistant response.",
		parameters: Type.Object({
			agent: Type.String({ description: "Target agent ID or exact title" }),
			prompt: Type.String({ description: "Work request or question" }),
		}),
		async execute(id, params, signal) { return toolResult(await callTool("send_agent", params, signal, id)); },
	});
	pi.registerTool({
		name: "galpon_read_message",
		label: "Read agent message",
		description: "Read the current state and result of a Galpón agent message.",
		parameters: Type.Object({ message_id: Type.String({ description: "Message ID from galpon_send_agent" }) }),
		async execute(id, params, signal) { return toolResult(await callTool("read_message", params, signal, id)); },
	});
	pi.registerTool({
		name: "galpon_await_agent",
		label: "Wait for agent",
		description: "Wait for up to 60 seconds by default for a Galpón agent message. Galpón rejects circular waits. A queued or delivered result is still pending; do not wait repeatedly without finishing the current turn or doing other useful work.",
		parameters: Type.Object({
			message_id: Type.String({ description: "Message ID from galpon_send_agent" }),
			timeout_seconds: Type.Optional(Type.Integer({ minimum: 1, maximum: 300, description: "Maximum wait for this call, from 1 to 300 seconds (default 60)" })),
		}),
		async execute(id, params, signal, onUpdate) {
			if (activeMessageIds.length !== 0 && !deliveryRunActive) {
				return toolResult({
					messageId: params.message_id,
					status: "interrupted",
					error: "Inbound work is already queued for this agent. Finish the current turn so Galpón can deliver it before you wait again.",
				});
			}
			const interrupt = new AbortController();
			awaitInterrupts.add(interrupt);
			awaitedMessageIds.add(params.message_id);
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
				return toolResult(await callTool("await_agent", params, waitSignal, id));
			} catch (error) {
				if (interrupt.signal.aborted && !signal?.aborted) {
					return toolResult({
						messageId: params.message_id,
						status: "interrupted",
						error: "The wait stopped because this agent received inbound work. Address that work before you wait again.",
					});
				}
				throw error;
			} finally {
				clearInterval(progress);
				awaitInterrupts.delete(interrupt);
				awaitedMessageIds.delete(params.message_id);
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

	const ensureRegistered = async (): Promise<boolean> => {
		if (registered) return true;
		if (registrationPromise) return registrationPromise;
		if (!registration || stopped) return false;
		registrationPromise = (async () => {
			try {
				await api("POST", `/v1/runtime/agents/${agentId}/register`, {
					runtimeId,
					sessionId: registration.sessionId,
					sessionPath: registration.sessionPath,
				});
				registered = true;
				registrationDelay = 250;
				if (!mirrorStarted) {
					mirrorStarted = true;
					conversationMirror.startBackfill(conversationBackfill(registration.sessionId, registration.branch));
				}
				return true;
			} catch {
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

	const formatMessages = (messages: any[]) => {
		const body = messages.map((message, index) => {
			const senderLabel = message.senderTitle || message.senderAgentId;
			const sender = senderLabel ? ` from Galpón agent ${senderLabel}` : "";
			if (message.kind === "result") {
				const reply = message.replyTo ? ` for message ${message.replyTo}` : "";
				const result = String(message.prompt ?? message.response ?? message.error ?? "No result text was provided.");
				return `${messages.length > 1 ? `Message ${index + 1} of ${messages.length}` : "Message"}${sender} [delivery ${message.id}]:\n\nCompleted correlated result${reply}. This is a result notification, not a new work request.\n\n${result}`;
			}
			return `${messages.length > 1 ? `Message ${index + 1} of ${messages.length}` : "Message"}${sender} [delivery ${message.id}]:\n\n${message.prompt}`;
		}).join("\n\n---\n\n");
		return `${body}\n\n---\n\nDelivery instructions: Address every delivery in this batch. Your final assistant text is the durable result for this batch. State what you completed, the main result, and any error or remaining work. Do not use galpon_send_agent to return a result for a current delivery. Galpón sends your final text to the requester when this turn settles.`;
	};

	const poll = async () => {
		if (stopped || polling || !activeContext) return;
		polling = true;
		try {
			if (!await ensureRegistered()) return;
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
				if (message.kind === "result" && awaitedMessageIds.has(message.replyTo)) {
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
			const steering = deliveryRunActive || (wasBusy && inbound.every(message => message.kind === "result"));
			lastAssistant = "";
			lastAssistantBatchId = "";
			if (steering && !deliveryRunActive) {
				deliveryRunActive = true;
				deliveryRunBatchId = activeBatchId;
				lastLeaseRenewal = 0;
			}
			for (const interrupt of awaitInterrupts) interrupt.abort();
			injectionPending = true;
			try {
				pi.sendUserMessage(formatMessages(inbound), { deliverAs: steering ? "steer" : "followUp" });
				injectionPending = false;
			} catch {
				// The durable claim remains active. A later idle poll retries injection.
				return;
			}
		} catch {
			registered = false;
			// The daemon can restart while Pi stays open. Stable claim keys and
			// completion attempts reconcile requests with unknown HTTP outcomes.
		} finally {
			polling = false;
			schedule(registered ? (activeMessageIds.length !== 0 ? 700 : 350) : registrationDelay);
		}
	};

	pi.on("session_start", (_event, ctx) => {
		activeContext = ctx;
		stopped = false;
		registered = false;
		ctx.ui.setTitle(`${agentTitle} · ${workspaceTitle}`);
		ctx.ui.setStatus("galpon", `GALPÓN  ${workspaceTitle}`);
		pi.setSessionName(agentTitle);
		const sessionId = ctx.sessionManager.getSessionId();
		const branch = ctx.sessionManager.getBranch();
		registration = { sessionId, sessionPath: ctx.sessionManager.getSessionFile() ?? "", branch };
		for (const entry of branch) {
			if (entry?.type !== "custom" || entry.customType !== "galpon-delivery") continue;
			const data = entry.data ?? {};
			if (typeof data.messageId !== "string") continue;
			if (data.status === "completion_pending") {
				recoverableCompletions.set(data.messageId, { response: String(data.response ?? ""), error: String(data.error ?? "") });
			} else if (data.status === "completed" || data.status === "failed") {
				recoverableCompletions.delete(data.messageId);
			}
		}
		schedule(0);
	});

	pi.on("before_agent_start", event => ({
		systemPrompt: event.systemPrompt + `\n\nYou are the durable Galpón agent ${agentTitle} in workspace ${workspaceTitle}.${agentRole ? ` Your role is ${agentRole}.` : ""}${placement ? ` Your placement is ${placement}.` : ""} Galpón provides optional tools for repository, workspace, agent, and cross-agent operations. Agent roles and names do not have special built-in behavior. Use these tools only when the user requests coordination or when the current task clearly requires it. Galpón delivers one queued cross-agent message per Pi turn so each response stays correlated to its request. Address every delivered message. A delivery with a completed correlated result is a notification about earlier work, not a new work request. For a current delivery, put the result in your final assistant response. Do not use galpon_send_agent to return the current delivery result. Galpón records and routes the final response automatically. Agents that you create are recorded as your descendants. Use galpon_cleanup_agents only when the user explicitly asks for cleanup: list the agents, select the exact relevant IDs, and do not clean agents whose results are still needed. Never create a synchronous wait cycle by asking an agent to wait for you while you wait for it. If galpon_await_agent returns a queued or delivered result, it is still pending; do not wait repeatedly without finishing the current turn or doing other useful work.`,
	}));

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
		if (update?.type === "thinking_start") {
			conversationMirror.enqueue(conversationEvent("assistant_reasoning_start", {
				role: "assistant",
			}));
			return;
		}
		if (update?.type === "thinking_delta" && typeof update.delta === "string" && update.delta.length > 0) {
			conversationMirror.enqueue(conversationEvent("assistant_reasoning_delta", {
				role: "assistant",
				content: update.delta,
				isDelta: true,
			}));
			return;
		}
		if (update?.type === "thinking_end") {
			conversationMirror.enqueue(conversationEvent("assistant_reasoning_end", {
				role: "assistant",
				content: typeof update.content === "string" ? update.content : "",
				isDelta: false,
			}));
			return;
		}
		if (update?.type !== "text_delta" || typeof update.delta !== "string" || update.delta.length === 0) return;
		conversationMirror.enqueue(conversationEvent("assistant_text_delta", {
			role: "assistant",
			content: update.delta,
			isDelta: true,
		}));
	});
	pi.on("message_end", (event, ctx) => {
		const message = event.message;
		const sessionId = ctx.sessionManager.getSessionId();
		if (message?.role === "user") {
			conversationMirror.enqueueFinalMessage(conversationEvent("user_message", {
				role: "user",
				content: normalContent(message.content),
				createdAt: messageCreatedAt(message),
			}), message, ctx.sessionManager, sessionId, "user");
			return;
		}
		if (message?.role === "assistant") {
			if (deliveryRunActive) {
				lastAssistant = assistantText(message);
				lastAssistantBatchId = deliveryRunBatchId;
			}
			conversationMirror.enqueueFinalMessage(conversationEvent("assistant_message_end", {
				role: "assistant",
				content: normalContent(message.content),
				isDelta: false,
				createdAt: messageCreatedAt(message),
			}), message, ctx.sessionManager, sessionId, "assistant");
			return;
		}
		if (message?.role === "toolResult") {
			const pending = pendingToolEnds.get(message.toolCallId);
			pendingToolEnds.delete(message.toolCallId);
			conversationMirror.enqueueFinalMessage(conversationEvent("tool_execution_end", {
				content: normalContent(message.content),
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
		if (activeMessageIds.length !== 0 && !deliveryRunActive) {
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
		if (deliveryRunActive) {
			deliveryRunActive = false;
			completionPending = true;
			await finishActive();
		}
		if (registered) await api("POST", `/v1/runtime/agents/${agentId}/status`, { runtimeId, status: "idle" }).catch(() => {});
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
	pi.on("session_shutdown", async () => {
		stopped = true;
		if (timer) clearTimeout(timer);
		conversationMirror.stop();
		await api("POST", `/v1/runtime/agents/${agentId}/stop`, { runtimeId }).catch(() => {});
	});
}
