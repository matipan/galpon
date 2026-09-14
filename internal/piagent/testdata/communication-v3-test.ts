import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { unlinkSync, writeFileSync } from "node:fs";
import galpon from "../extension.ts";

type Handler = (event: any, ctx: any) => any;
type RequestRecord = { path: string; body: any };

const socketPath = process.env.GALPON_SOCKET!;
const resultPath = process.env.GALPON_COMMUNICATION_V3_TEST_RESULT;

function delay(ms: number) {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

function response(res: ServerResponse, status: number, value: any) {
	res.writeHead(status, { "content-type": "application/json" });
	res.end(JSON.stringify(value));
}

async function body(req: IncomingMessage) {
	const chunks: Buffer[] = [];
	for await (const chunk of req) chunks.push(Buffer.from(chunk));
	const text = Buffer.concat(chunks).toString("utf8");
	return text ? JSON.parse(text) : {};
}

class FakePi {
	handlers = new Map<string, Handler[]>();
	tools = new Map<string, any>();
	commands = new Map<string, any>();
	entries: any[] = [];
	sent: any[] = [];
	aborted = false;
	failNextSend = false;
	private eventHandlers = new Map<string, Array<(value: any) => void>>();
	events = {
		on: (name: string, handler: (value: any) => void) => {
			const values = this.eventHandlers.get(name) ?? [];
			values.push(handler);
			this.eventHandlers.set(name, values);
			return () => this.eventHandlers.set(name, (this.eventHandlers.get(name) ?? []).filter((item) => item !== handler));
		},
		emit: (name: string, value: any) => {
			for (const handler of [...(this.eventHandlers.get(name) ?? [])]) handler(value);
		},
	};
	on(name: string, handler: Handler) {
		const values = this.handlers.get(name) ?? [];
		values.push(handler);
		this.handlers.set(name, values);
	}
	registerTool(tool: any) { this.tools.set(tool.name, tool); }
	registerCommand(name: string, command: any) { this.commands.set(name, command); }
	appendEntry(customType: string, data: any) {
		this.entries.push({ type: "custom", id: `custom-${this.entries.length + 1}`, customType, data, timestamp: new Date().toISOString() });
	}
	sendUserMessage(content: any, options: any) { this.sent.push({ content, options }); }
	sendMessage(message: any, options: any) {
		if (this.failNextSend) {
			this.failNextSend = false;
			throw new Error("injected Pi send failure");
		}
		this.sent.push({ content: message.content, options, details: message.details });
		this.entries.push({ type: "custom_message", id: `message-${this.entries.length + 1}`, customType: message.customType, content: message.content, details: message.details, timestamp: new Date().toISOString() });
	}
	setSessionName() {}
	async emit(name: string, event: any, ctx: any) {
		let result: any;
		for (const handler of this.handlers.get(name) ?? []) result = await handler(event, ctx);
		return result;
	}
}

function context(pi: FakePi) {
	return {
		mode: "tui",
		hasUI: true,
		sessionManager: {
			getSessionId: () => "session",
			getSessionFile: () => "/tmp/session.jsonl",
			getBranch: () => pi.entries,
			getLeafId: () => pi.entries[pi.entries.length - 1]?.id ?? "session-root",
			getLeafEntry: () => pi.entries[pi.entries.length - 1],
		},
		isIdle: () => true,
		abort: () => { pi.aborted = true; },
		shutdown: () => {},
		ui: {
			setStatus: () => {}, setTitle: () => {}, notify: () => {}, setEditorText: () => {},
			confirm: async () => false,
		},
	};
}

async function waitFor(predicate: () => boolean, message: string, timeout = 4000) {
	const deadline = Date.now() + timeout;
	while (!predicate()) {
		if (Date.now() >= deadline) throw new Error(message);
		await delay(20);
	}
}

async function run() {
	try { unlinkSync(socketPath); } catch {}
	const requests: RequestRecord[] = [];
	const claims: any[] = [];
	const claimRetries = new Map<string, any>();
	const receiptBatches = new Map<string, any>();
	const settleModes = new Map<string, any>();
	const settleFailures = new Map<string, { status: number; error: string; remaining: number }>();
	const renewFailures = new Map<string, { status: number; error: string; remaining: number }>();
	const observationFailures = new Map<string, { status: number; error: string; remaining: number }>();
	const observedMessages = new Map<string, Set<string>>();
	const holdObservations = new Map<string, number>();
	const heldObservationReplies = new Map<string, () => void>();
	let maintenance = false;
	let registrations = 0;
	let directCount = 0;
	let rejectNextClaim = false;
	let failNextDirectAfterCommit = false;
	let todoSettlement: any;
	let failOwnershipReconciliation = false;
	let malformedOwnershipReconciliation = false;
	let failNextProgressResponse = false;
	const operationOwnershipStates = new Map<string, string>();
	const server = createServer(async (req, res) => {
		const value = await body(req);
		const path = req.url ?? "";
		requests.push({ path, body: value });
		if (path === "/v1/communication/protocol") return response(res, 200, { generation: 3, complete: true, maintenance });
		if (/\/register$/.test(path)) { registrations++; return response(res, 200, { registered: true, protocol: { generation: 3, complete: true, maintenance } }); }
		if (/\/delegated-status$/.test(path)) return response(res, 200, { activeDelegatedAgents: 0 });
		if (/\/work$/.test(path)) return response(res, 200, { work: [] });
		if (/\/status$/.test(path) || /\/conversation-events$/.test(path) || /\/stop$/.test(path)) return response(res, 200, {});
		if (/\/operations\/direct$/.test(path)) {
			directCount++;
			if (failNextDirectAfterCommit) {
				failNextDirectAfterCommit = false;
				return response(res, 500, { error: "lost direct registration response" });
			}
			const id = `direct:${value.userEntryId}`;
			operationOwnershipStates.set(id, "running");
			return response(res, 200, { id, kind: "direct", state: "running", userEntryId: value.userEntryId, attempt: 1 });
		}
		if (/\/operations\/reconcile-ownership$/.test(path)) {
			if (failOwnershipReconciliation) return response(res, 503, { error: "injected ownership reconciliation failure" });
			if (malformedOwnershipReconciliation) return response(res, 200, { invalid: true });
			if (value.protocolGeneration !== 3 || !Array.isArray(value.operationIds) || value.operationIds.length > 256) return response(res, 400, { error: "invalid ownership reconciliation fence" });
			const ownedOperationIds = value.operationIds.filter((id: string) => ["ready", "claimed", "running", "waiting", "settling"].includes(operationOwnershipStates.get(id) ?? "missing"));
			return response(res, 200, { ownedOperationIds });
		}
		if (/\/operations\/claim$/.test(path)) {
			if (rejectNextClaim) { rejectNextClaim = false; return response(res, 409, { error: "runtime is not registered for communication protocol generation 3" }); }
			const claimId = String(value.claimId ?? "");
			const delivery = claimRetries.has(claimId) ? claimRetries.get(claimId) : claims.shift() ?? null;
			if (delivery && claimId) {
				claimRetries.set(claimId, delivery);
				operationOwnershipStates.set(String(delivery.operation?.id ?? ""), String(delivery.operation?.state ?? "claimed"));
			}
			return response(res, 200, { delivery });
		}
		const operationMatch = path.match(/\/operations\/([^/]+)\/(start|renew|settle)$/);
		if (operationMatch) {
			const operationId = decodeURIComponent(operationMatch[1]!);
			if (operationMatch[2] === "settle") {
				for (const [claimId, delivery] of claimRetries) if (delivery?.operation?.id === operationId) claimRetries.delete(claimId);
				const failure = settleFailures.get(operationId);
				if (failure && failure.remaining > 0) {
					failure.remaining--;
					operationOwnershipStates.set(operationId, "ready");
					return response(res, failure.status, { error: failure.error });
				}
				const result = settleModes.get(operationId) ?? { parked: false, operation: { id: operationId, state: "settled" } };
				operationOwnershipStates.set(operationId, String(result.operation?.state ?? "settled"));
				return response(res, 200, result);
			}
			if (operationMatch[2] === "renew") {
				const failure = renewFailures.get(operationId);
				if (failure && failure.remaining > 0) {
					failure.remaining--;
					operationOwnershipStates.set(operationId, "ready");
					return response(res, failure.status, { error: failure.error });
				}
			}
			if (operationMatch[2] === "start") operationOwnershipStates.set(operationId, "running");
			return response(res, 200, {});
		}
		const takeMatch = path.match(/\/operations\/([^/]+)\/receipts\/take$/);
		if (takeMatch) {
			const id = decodeURIComponent(takeMatch[1]!);
			const batch = receiptBatches.get(id) ?? { receipts: [], results: [] };
			const observed = observedMessages.get(id);
			return response(res, 200, observed ? {
				receipts: batch.receipts.filter((receipt: any) => !observed.has(receipt.messageId)),
				results: batch.results.filter((result: any) => !observed.has(result.messageId)),
			} : batch);
		}
		if (/\/receipts\/[^/]+\/present$/.test(path)) return response(res, 200, { presented: true });
		const observationMatch = path.match(/\/operations\/([^/]+)\/observe-results$/);
		if (observationMatch) {
			const id = decodeURIComponent(observationMatch[1]!);
			const key = `${id}:${value.attempt}`;
			const heldStatus = holdObservations.get(key);
			if (heldStatus) {
				holdObservations.delete(key);
				heldObservationReplies.set(id, () => response(res, heldStatus, heldStatus === 200 ? { recorded: true } : { error: "result observation no longer belongs to this operation attempt" }));
				return;
			}
			const failure = observationFailures.get(key);
			if (failure && failure.remaining > 0) {
				failure.remaining--;
				if (failure.status === 404 || failure.error === "result observation no longer belongs to this operation attempt") {
					operationOwnershipStates.set(id, "ready");
					observedMessages.get(id)?.clear();
					for (const [claimId, delivery] of claimRetries) if (delivery.operation?.id === id) claimRetries.delete(claimId);
				}
				return response(res, failure.status, { error: failure.error });
			}
			for (const messageId of value.messageIds ?? []) observedMessages.get(id)?.add(messageId);
			return response(res, 200, { recorded: true });
		}
		if (/\/todos\/links\/[^/]+\/claim$/.test(path)) return response(res, 200, { id: "todo:child", messageId: "child", todoId: 7, policy: "complete_on_success", state: "pending", operationAttempt: value.operationAttempt });
		if (/\/todos\/links\/[^/]+\/(apply|fail)$/.test(path)) return response(res, 200, {});
		if (/\/todos\/settlements\/claim$/.test(path)) {
			if (!todoSettlement) return response(res, 404, { error: "not found" });
			const result = todoSettlement;
			todoSettlement = undefined;
			return response(res, 200, result);
		}
		if (/\/todos\/settlements\/[^/]+\/(apply|ack|fail)$/.test(path)) return response(res, 200, {});
		if (path === "/v1/runtime/tools/read_message") return response(res, 200, { id: value.args?.message_id ?? "child", status: "completed", response: "todo done" });
		if (path === "/v1/runtime/tools/await_agent") {
			if (value.args?.message_id === "pending-child") return response(res, 200, { messageId: "pending-child", status: "queued", waitStatus: "timeout", messageStatus: "queued", targetRuntimeStatus: "idle", attempt: 0, waitError: { kind: "timeout", message: "The bounded wait reached its deadline." } });
			return response(res, 200, { messageId: value.args?.message_id ?? "child", status: "completed", waitStatus: "completed", messageStatus: "completed", targetRuntimeStatus: "idle", attempt: 1, response: "await done" });
		}
		if (path === "/v1/runtime/tools/await_agents") return response(res, 200, {
			status: "timeout", returnWhen: value.args?.return_when, completed: 1, total: value.args?.message_ids?.length ?? 0,
			outcomes: (value.args?.message_ids ?? []).map((messageId: string, index: number) => index === 0
				? { messageId, status: "completed", waitStatus: "completed", messageStatus: "completed", targetRuntimeStatus: "idle", attempt: 1, response: "done" }
				: { messageId, status: "queued", waitStatus: "timeout", messageStatus: "queued", targetRuntimeStatus: "idle", attempt: 0, waitError: { kind: "timeout", message: "The bounded wait reached its deadline." } }),
		});
		if (path === "/v1/runtime/tools/send_agent") return response(res, 200, { id: "todo-child", status: "queued" });
		if (path === "/v1/runtime/tools/create_agent") return response(res, 200, { id: "new-agent", initialMessage: { id: "created-child", status: "queued" } });
		if (path === "/v1/runtime/tools/update_agent") return response(res, 200, { messageId: value.args?.message_id, status: "updated" });
		if (path === "/v1/runtime/tools/report_progress") {
			if (failNextProgressResponse) {
				failNextProgressResponse = false;
				return response(res, 500, { error: "lost progress response" });
			}
			return response(res, 200, { accepted: true, recorded: true, progress: value.args });
		}
		if (path.startsWith("/v1/runtime/tools/")) return response(res, 200, {});
		return response(res, 404, { error: `unhandled ${path}` });
	});
	await new Promise<void>((resolve, reject) => server.listen(socketPath, (error?: Error) => error ? reject(error) : resolve()));

	const pi = new FakePi();
	galpon(pi as any);
	const todoOperationSnapshots: any[] = [];
	pi.events.on("galpon:todo:operation-snapshot:v1", (value) => todoOperationSnapshots.push(value));
	const ctx = context(pi);
	await pi.emit("session_start", { reason: "startup" }, ctx);
	await waitFor(() => registrations >= 1, "runtime did not register");
	const firstRegistration = requests.find((item) => /\/register$/.test(item.path));
	if (firstRegistration?.body.protocolGeneration !== 3) throw new Error("registration omitted protocolGeneration");
	for (const name of ["galpon_create_agent", "galpon_send_agent"]) {
		if (JSON.stringify(pi.tools.get(name)?.parameters).includes("result_mode")) throw new Error(`${name} still exposes result_mode`);
	}
	const progressTool = pi.tools.get("galpon_report_progress");
	const progressRequestsBefore = requests.filter((item) => item.path === "/v1/runtime/tools/report_progress").length;
	const unavailableProgress = await progressTool.execute("inactive-progress", { phase: "planning", summary: "Safe summary" }, undefined);
	if (unavailableProgress.details?.recorded !== false || unavailableProgress.details?.reason !== "no_active_delegated_request") throw new Error("progress without delegated work was not safely declined");
	if (requests.filter((item) => item.path === "/v1/runtime/tools/report_progress").length !== progressRequestsBefore) throw new Error("declined progress reached storage without active delegated work");
	const readTool = pi.tools.get("galpon_read_message");
	const inactiveRead = await readTool.execute("inactive-read", { message_id: "read-outside-operation" }, undefined);
	const repeatedRead = await readTool.execute("inactive-read-again", { message_id: "read-outside-operation" }, undefined);
	if (inactiveRead.details?.status !== "completed" || repeatedRead.details?.status !== "completed") throw new Error("repeatable read without an active operation failed");
	const inactiveReadRequest = requests.find((item) => item.path === "/v1/runtime/tools/read_message" && item.body.requestId === "inactive-read");
	if (inactiveReadRequest?.body.operationId || inactiveReadRequest?.body.protocolGeneration !== 3) throw new Error("read without an active operation used an operation fence or omitted runtime generation");
	const awaitTool = pi.tools.get("galpon_await_agent");
	const timedWait = await awaitTool.execute("inactive-timeout", { message_id: "pending-child", timeout_seconds: 1 }, undefined, undefined);
	if (timedWait.details?.waitStatus !== "timeout" || timedWait.details?.status === "parked" || "receiptId" in timedWait.details) throw new Error("bounded await returned a parked or receipt-bearing result");
	const inactiveAwaitRequest = requests.find((item) => item.path === "/v1/runtime/tools/await_agent" && item.body.requestId === "inactive-timeout");
	if (inactiveAwaitRequest?.body.operationId || inactiveAwaitRequest?.body.args?.timeout_seconds !== 1) throw new Error("bounded await without an operation changed its deadline or acquired an operation fence");
	const awaitManyTool = pi.tools.get("galpon_await_agents");
	const timedMany = await awaitManyTool.execute("inactive-many", { message_ids: ["first", "second"], return_when: "all", timeout_seconds: 1 }, undefined);
	if (timedMany.details?.status !== "timeout" || JSON.stringify(timedMany.details?.outcomes?.map((outcome: any) => outcome.messageId)) !== '["first","second"]') throw new Error("multi-message wait did not preserve input order and bounded timeout state");

	maintenance = true;
	const blocked = await pi.emit("input", { text: "blocked", source: "interactive" }, ctx);
	if (blocked?.action !== "handled" || directCount !== 0) throw new Error("maintenance input started direct work");
	maintenance = false;
	const accepted = await pi.emit("input", { text: "direct", source: "interactive" }, ctx);
	if (accepted?.action !== "continue") throw new Error("direct input was not accepted");
	if (registrations < 2) throw new Error("runtime did not re-register after communication maintenance ended");
	if (directCount !== 1) throw new Error("direct operation was not registered before model start");
	const directRequest = requests.find((item) => /\/operations\/direct$/.test(item.path));
	if (!String(directRequest?.body.userEntryId ?? "").startsWith("pi-input:") || directRequest?.body.protocolGeneration !== 3) throw new Error("direct operation did not use the stable input identity");
	const directOperationId = `direct:${directRequest?.body.userEntryId}`;
	pi.entries.push({ type: "message", id: "stable-user-entry", message: { role: "user", content: "direct" }, timestamp: new Date().toISOString() });
	await pi.emit("before_agent_start", { systemPrompt: "system", prompt: "direct" }, ctx);
	(ctx as any).isIdle = () => false;
	const steering = await pi.emit("input", { text: "steer the active model", source: "interactive" }, ctx);
	if (steering?.action !== "continue") throw new Error("active model steering was blocked as a new direct objective");
	(ctx as any).isIdle = () => true;
	await waitFor(() => todoOperationSnapshots.at(-1)?.ownershipKnowledge === "exact", "initial ownership reconciliation did not finish");
	const sendTool = pi.tools.get("galpon_send_agent");
	await sendTool.execute("todo-linked-send", { agent: "worker", prompt: "Do queued work", act: "request", todo_id: 31 }, undefined);
	const todoLinkedSend = requests.find((item) => item.path === "/v1/runtime/tools/send_agent" && item.body.requestId === "todo-linked-send");
	if (todoLinkedSend?.body.args?.todo_id !== 31 || "result_mode" in (todoLinkedSend?.body.args ?? {})) throw new Error("TODO-linked send forced or exposed a result mode");
	const updateTool = pi.tools.get("galpon_update_agent");
	const updateResult = await updateTool.execute("update-queued-send", { message_id: "todo-child", prompt: "Use the corrected assignment" }, undefined);
	if (updateResult.details?.status !== "updated") throw new Error("queued assignment update did not return updated");
	const updateRequest = requests.find((item) => item.path === "/v1/runtime/tools/update_agent" && item.body.requestId === "update-queued-send");
	if (updateRequest?.body.args?.message_id !== "todo-child" || updateRequest?.body.args?.prompt !== "Use the corrected assignment") throw new Error("assignment update did not use message_id and prompt");

	pi.events.emit("rpiv-todo:mutation:v1", { action: "update", taskId: 31, finalStatus: "pending", effect: "changed" });
	const associatedSnapshot = todoOperationSnapshots.at(-1);
	if (JSON.stringify(associatedSnapshot?.activeTaskIds) !== "[31]" || associatedSnapshot?.ownershipKnowledge !== "exact") throw new Error("a changed pending TODO update did not publish its exact operation association");
	if ("operationId" in associatedSnapshot || JSON.stringify(associatedSnapshot).includes(directOperationId)) throw new Error("the Pi-local TODO association event exposed an operation ID");
	if (!pi.entries.some((entry) => entry.customType === "galpon-operation" && entry.data?.status === "todo_associated" && entry.data?.todoId === 31)) throw new Error("the Pi operation TODO association was not durable");
	// Reducer failures are in-band. Pi reports isError=false for this tool result,
	// so only the reducer-result mutation event can prevent a false association.
	pi.events.emit("rpiv-todo:mutation:v1", { action: "update", effect: "rejected" });
	await pi.emit("tool_execution_end", { toolName: "todo", toolCallId: "todo-failed-32", isError: false }, ctx);
	pi.events.emit("rpiv-todo:mutation:v1", { action: "update", taskId: 33, finalStatus: "pending", effect: "no_change" });
	if (JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[31]") throw new Error("a rejected or no-change TODO update created an operation association");

	claims.push({ operation: { id: "notify-op", kind: "direct", state: "claimed", attempt: 1, protocolGeneration: 3 } });
	receiptBatches.set("notify-op", { receipts: [{ id: "notify-receipt", kind: "result", messageId: "notify-child", resultId: "result:notify-child" }], results: [{ id: "result:notify-child", messageId: "notify-child", status: "completed", response: "notify result" }] });
	await delay(450);
	if (pi.sent.length !== 0) throw new Error("notify receipt entered an unrelated direct operation");

	settleModes.set(directOperationId, { parked: true, operation: { id: directOperationId, state: "waiting" } });
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "direct done" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	if (JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[31]") throw new Error("a parked waiting operation lost TODO ownership");
	await waitFor(() => pi.sent.some((item) => String(item.content).includes("notify result")), "independent notify operation did not run");
	if (!requests.some((item) => item.path.includes("notify-receipt/present"))) throw new Error("notify receipt was not presented");
	pi.events.emit("rpiv-todo:mutation:v1", { action: "update", taskId: 31, finalStatus: "in_progress", effect: "changed" });
	if (pi.entries.filter((entry) => entry.customType === "galpon-operation" && entry.data?.status === "todo_associated" && entry.data?.todoId === 31).length !== 2) throw new Error("one TODO was not associated with two nonterminal operations");
	pi.events.emit("rpiv-todo:mutation:v1", { action: "update", taskId: 31, finalStatus: "completed", effect: "changed" });
	if (JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[]") throw new Error("TODO completion did not remove ownership from every operation");
	pi.events.emit("rpiv-todo:mutation:v1", { action: "update", taskId: 34, finalStatus: "pending", effect: "changed" });
	pi.events.emit("rpiv-todo:mutation:v1", { action: "delete", taskId: 34, finalStatus: "deleted", effect: "changed" });
	if (JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[]") throw new Error("global TODO delete did not remove operation ownership");
	pi.events.emit("rpiv-todo:mutation:v1", { action: "update", taskId: 1, finalStatus: "pending", effect: "changed" });
	pi.events.emit("rpiv-todo:mutation:v1", { action: "clear", effect: "changed" });
	pi.events.emit("rpiv-todo:mutation:v1", { action: "update", taskId: 1, finalStatus: "pending", effect: "changed" });
	if (JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[1]") throw new Error("a global clear blocked ownership after task-ID reuse");
	if (!pi.entries.some((entry) => entry.customType === "galpon-operation" && entry.data?.status === "todo_globally_dissociated") || !pi.entries.some((entry) => entry.customType === "galpon-operation" && entry.data?.status === "todo_associations_globally_cleared")) throw new Error("global TODO removals were not durable");
	settleModes.set("notify-op", { parked: true, operation: { id: "notify-op", state: "waiting" } });
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "notify handled" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	if (JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[1]") throw new Error("a second parked waiting operation lost TODO ownership");
	operationOwnershipStates.set("notify-op", "expired");
	await waitFor(() => JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) === "[]", "external terminal expiry did not clear TODO ownership", 5000);
	if (!pi.entries.some((entry) => entry.customType === "galpon-operation" && entry.data?.operationId === "notify-op" && entry.data?.status === "todo_associations_cleared")) throw new Error("external expiry removal was not durable");

	settleModes.set("inbound-op", { parked: true, operation: { id: "inbound-op", state: "waiting" } });
	claims.push({ operation: { id: "inbound-op", kind: "inbound", state: "claimed", parentMessageId: "request-1", attempt: 1, protocolGeneration: 3 }, message: { id: "request-1", kind: "request", act: "request", prompt: "do work", senderTitle: "Sender" } });
	await waitFor(() => pi.sent.some((item) => JSON.stringify(item.content).includes("do work")), "inbound delivery did not start");
	pi.events.emit("rpiv-todo:mutation:v1", { action: "update", taskId: 50, finalStatus: "pending", effect: "changed" });
	const codexCallId = "call_progress_123|fc_0123456789abcdef0123456789abcdef";
	const generatedIds = new Set<string>();
	for (const id of ["generated-progress-id", codexCallId, `${codexCallId}-other`, `call_${"x".repeat(300)}|fc_unsafe\n`]) {
		const params = { phase: "working", summary: "Regression checks are running" };
		const reported = await progressTool.execute(id, params, undefined);
		const repeated = await progressTool.execute(id, params, undefined);
		const eventId = reported.details?.progress?.event_id;
		if (!reported.details?.recorded || !repeated.details?.recorded || reported.details?.progress?.version !== 1) throw new Error("active delegated progress was not recorded");
		if (!/^progress:(?:[a-f0-9]{16}:){3}[a-f0-9]{16}$/.test(eventId)) throw new Error("automatic progress ID is not safe and bounded");
		if (eventId !== repeated.details?.progress?.event_id || generatedIds.has(eventId)) throw new Error("automatic progress ID is not stable and distinct");
		if (id === codexCallId && eventId !== "progress:188875c3ec62cf1b:f3fc0581a0fec167:02b02bc8329b8574:f4b65344f63bf4b2") throw new Error("Pi and daemon progress IDs differ");
		generatedIds.add(eventId);
	}
	const explicitProgress = await progressTool.execute("manual-progress-call", { event_id: "manual-checkpoint", phase: "working", summary: "Checking explicit IDs" }, undefined);
	if (explicitProgress.details?.progress?.event_id !== "manual-checkpoint") throw new Error("an explicit progress ID was changed");
	failNextProgressResponse = true;
	await progressTool.execute("retry-progress-call", { phase: "working", summary: "Checking retry identity" }, undefined);
	const retriedProgress = requests.filter(item => item.path === "/v1/runtime/tools/report_progress" && item.body.requestId === "retry-progress-call");
	if (retriedProgress.length !== 2 || retriedProgress[0].body.args.event_id !== retriedProgress[1].body.args.event_id) throw new Error("progress retry changed its event ID");
	failOwnershipReconciliation = true;
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "waiting" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	if (todoOperationSnapshots.at(-1)?.ownershipKnowledge !== "unknown" || JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[50]") throw new Error("reconciliation failure showed an exact ready count");
	failOwnershipReconciliation = false;
	malformedOwnershipReconciliation = true;
	await delay(3200);
	if (todoOperationSnapshots.at(-1)?.ownershipKnowledge !== "unknown" || JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[50]") throw new Error("a malformed reconciliation response showed false exact ownership");
	malformedOwnershipReconciliation = false;
	await waitFor(() => todoOperationSnapshots.at(-1)?.ownershipKnowledge === "exact" && JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) === "[50]", "parked waiting ownership did not recover after the daemon returned", 5000);
	claims.push({ operation: { id: "inbound-op", kind: "inbound", state: "claimed", parentMessageId: "request-1", attempt: 2, protocolGeneration: 3 }, message: { id: "request-1", kind: "request", act: "request", prompt: "do work" } });
	receiptBatches.set("inbound-op", { receipts: [{ id: "join-receipt", kind: "result", messageId: "child", resultId: "result:child" }], results: [{ id: "result:child", messageId: "child", status: "completed", response: "child done" }] });
	settleModes.set("inbound-op", { parked: false, operation: { id: "inbound-op", state: "settled" } });
	await waitFor(() => pi.sent.some((item) => String(item.content).includes("Continue the original task from the saved conversation")), "parked operation did not resume");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "resumed done" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	if (JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[]") throw new Error("terminal waiting recovery kept TODO ownership");

	// A child can finish before the parent settle reaches the daemon. The first
	// settle then parks directly in ready state. The next attempt must take the
	// new receipt instead of replaying the completion that parked attempt one.
	settleModes.set("ready-race-op", { parked: true, operation: { id: "ready-race-op", state: "ready" } });
	claims.push({ operation: { id: "ready-race-op", kind: "inbound", state: "claimed", parentMessageId: "request-ready-race", attempt: 1, protocolGeneration: 3 }, message: { id: "request-ready-race", kind: "request", act: "request", prompt: "ready race work" } });
	await waitFor(() => pi.sent.some((item) => JSON.stringify(item.content).includes("ready race work")), "ready-race operation did not start");
	pi.events.emit("rpiv-todo:mutation:v1", { action: "update", taskId: 42, finalStatus: "pending", effect: "changed" });
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "ready race partial" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	if (JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[42]") throw new Error("a parked ready operation lost TODO ownership");
	claims.push({ operation: { id: "ready-race-op", kind: "inbound", state: "claimed", parentMessageId: "request-ready-race", attempt: 2, protocolGeneration: 3 }, message: { id: "request-ready-race", kind: "request", act: "request", prompt: "ready race work" } });
	receiptBatches.set("ready-race-op", { receipts: [{ id: "ready-race-receipt", kind: "result", messageId: "ready-race-child", resultId: "result:ready-race-child" }], results: [{ id: "result:ready-race-child", messageId: "ready-race-child", status: "completed", response: "ready race child result" }] });
	settleModes.set("ready-race-op", { parked: false, operation: { id: "ready-race-op", state: "settled" } });
	await waitFor(() => pi.sent.some((item) => String(item.content).includes("ready race child result")), "ready-state park replayed the old completion without taking its receipt");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "ready race done" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	if (JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[]") throw new Error("terminal ready-race recovery kept TODO ownership");

	pi.failNextSend = true;
	claims.push({ operation: { id: "injection-retry-op", kind: "inbound", state: "claimed", parentMessageId: "request-injection-retry", attempt: 1, protocolGeneration: 3 }, message: { id: "request-injection-retry", kind: "request", act: "request", prompt: "retry failed Pi injection" } });
	await waitFor(() => pi.sent.some((item) => JSON.stringify(item.content).includes("retry failed Pi injection")), "failed Pi injection was not retried with the stable operation claim");
	const injectionClaims = requests.filter((item) => /\/operations\/claim$/.test(item.path) && claimRetries.get(String(item.body.claimId ?? ""))?.operation?.id === "injection-retry-op");
	if (injectionClaims.length < 2 || injectionClaims[0]?.body.claimId !== injectionClaims[1]?.body.claimId) throw new Error("failed Pi injection did not retry its stable claim ID");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "injection retry done" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);

	// The daemon can restart after Pi stores a final response but before the
	// fenced settle call. A 404 means that the local attempt no longer owns the
	// operation. Reclaim a new attempt and submit the saved response without a
	// second model turn.
	settleFailures.set("stale-settle-op", { status: 404, error: "operation attempt is no longer active", remaining: 1 });
	renewFailures.set("stale-settle-op", { status: 404, error: "operation attempt is no longer active", remaining: 1 });
	receiptBatches.set("stale-settle-op", { receipts: [{ id: "stale-settle-receipt", kind: "result", messageId: "stale-settle-child", resultId: "result:stale-settle-child" }], results: [{ id: "result:stale-settle-child", messageId: "stale-settle-child", status: "completed", response: "stale child result" }] });
	claims.push(
		{ operation: { id: "stale-settle-op", kind: "inbound", state: "claimed", parentMessageId: "request-stale-settle", attempt: 1, protocolGeneration: 3 }, message: { id: "request-stale-settle", kind: "request", act: "request", prompt: "finish before restart" } },
		{ operation: { id: "stale-settle-op", kind: "inbound", state: "claimed", parentMessageId: "request-stale-settle", attempt: 2, protocolGeneration: 3 }, message: { id: "request-stale-settle", kind: "request", act: "request", prompt: "finish before restart" } },
	);
	await waitFor(() => pi.sent.some((item) => JSON.stringify(item.content).includes("stale child result")), "stale-settle operation did not start");
	await pi.emit("agent_start", {}, ctx);
	const realDateNow = Date.now;
	const advancedNow = realDateNow() + 31_000;
	Date.now = () => advancedNow;
	try {
		await delay(800);
	} finally {
		Date.now = realDateNow;
	}
	if (!requests.some((item) => /\/operations\/stale-settle-op\/renew$/.test(item.path))) throw new Error("stale operation renewal was not attempted");
	if (pi.entries.some((entry) => entry.customType === "galpon-operation" && entry.data?.operationId === "stale-settle-op" && entry.data?.status === "stale_attempt" && entry.data?.phase === "renew")) throw new Error("stale renewal released operation correlation during its model turn");
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "saved before restart" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	await waitFor(() => requests.filter((item) => /\/operations\/stale-settle-op\/settle$/.test(item.path)).length >= 2, "stale settle did not retry on a new attempt");
	const staleSettles = requests.filter((item) => /\/operations\/stale-settle-op\/settle$/.test(item.path));
	if (staleSettles[0]?.body.attempt !== 1 || staleSettles[1]?.body.attempt !== 2) throw new Error("stale settle did not use a new fenced attempt");
	if (staleSettles[1]?.body.response !== "saved before restart") throw new Error("stale settle lost the saved final response");
	if (pi.sent.filter((item) => JSON.stringify(item.content).includes("stale child result")).length !== 1) throw new Error("stale settle recovery replayed a persisted result receipt");
	if (requests.filter((item) => item.path.includes("stale-settle-receipt/present")).length !== 2) throw new Error("stale settle recovery did not re-present the persisted receipt under the new attempt");
	if (!pi.entries.some((entry) => entry.customType === "galpon-operation" && entry.data?.operationId === "stale-settle-op" && entry.data?.status === "stale_attempt" && entry.data?.phase === "settle")) throw new Error("stale settle recovery was not recorded in the Pi session");

	await pi.emit("input", { text: "await", source: "interactive" }, ctx);
	pi.entries.push({ type: "message", id: "await-user", message: { role: "user", content: "await" }, timestamp: new Date().toISOString() });
	await pi.emit("before_agent_start", { systemPrompt: "system", prompt: "await" }, ctx);
	const observationsBefore = requests.filter((item) => /\/observe-results$/.test(item.path)).length;
	const awaitResult = await awaitTool.execute("await-tool-request", { message_id: "child" }, undefined, undefined);
	if (awaitResult.details?.waitStatus !== "completed" || "receiptId" in awaitResult.details || awaitResult.details?.status === "parked") throw new Error("completed await exposed parked or receipt state");
	if (requests.filter((item) => /\/observe-results$/.test(item.path)).length !== observationsBefore) throw new Error("await observed notification state before Pi persisted its tool result");
	if (!pi.entries.some((entry) => entry.customType === "galpon-operation" && entry.data?.status === "result_observation_pending" && entry.data?.toolCallId === "await-tool-request")) throw new Error("await observation intent was not persisted");
	pi.entries.push({ type: "message", id: "await-result-entry", message: { role: "toolResult", toolCallId: "await-tool-request", toolName: "galpon_await_agent", content: awaitResult.content, details: awaitResult.details, isError: false, timestamp: Date.now() }, timestamp: new Date().toISOString() });
	await pi.emit("message_end", { message: pi.entries[pi.entries.length - 1].message }, ctx);
	await waitFor(() => requests.filter((item) => /\/observe-results$/.test(item.path)).length > observationsBefore, "await result observation did not follow Pi persistence");
	const observationRequest = requests.filter((item) => /\/observe-results$/.test(item.path)).at(-1);
	const awaitRequest = requests.find((item) => item.path === "/v1/runtime/tools/await_agent" && item.body.requestId === "await-tool-request");
	const awaitDirectRequest = requests.filter((item) => /\/operations\/direct$/.test(item.path)).at(-1);
	if (awaitRequest?.body.operationId !== `direct:${awaitDirectRequest?.body.userEntryId}` || awaitRequest.body.operationAttempt !== 1 || awaitRequest.body.protocolGeneration !== 3 || awaitRequest.body.requestId !== "await-tool-request") throw new Error("await request omitted generation-3 fencing");
	if (JSON.stringify(observationRequest?.body.messageIds) !== '["child"]' || observationRequest?.body.toolCallId !== "await-tool-request" || observationRequest?.body.operationId !== awaitRequest.body.operationId || observationRequest?.body.operationAttempt !== 1) throw new Error("post-persistence observation used the wrong operation, messages, or tool ID");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "await handled" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);

	failNextDirectAfterCommit = true;
	const failedDirect = await pi.emit("input", { text: "recover direct", source: "interactive" }, ctx);
	if (failedDirect?.action !== "handled") throw new Error("unknown direct registration result did not block model start");
	const retryDirect = await pi.emit("input", { text: "recover direct", source: "interactive" }, ctx);
	if (retryDirect?.action !== "continue") throw new Error("stable direct registration retry was not accepted");
	const recoveryRequests = requests.filter((item) => /\/operations\/direct$/.test(item.path)).slice(-2);
	if (recoveryRequests.length !== 2 || recoveryRequests[0]?.body.userEntryId !== recoveryRequests[1]?.body.userEntryId) throw new Error("direct registration retry changed stable input identity");
	pi.entries.push({ type: "message", id: "recover-user", message: { role: "user", content: "recover direct" }, timestamp: new Date().toISOString() });
	await pi.emit("before_agent_start", { systemPrompt: "system", prompt: "recover direct" }, ctx);
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "recovered direct" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);

	pi.events.on("galpon:todo:link:v1", (event: any) => {
		pi.appendEntry("rpiv-todo:galpon-state:v1", { integration: "galpon", operationId: event.operationId, tasks: [], nextId: 8 });
		pi.events.emit("rpiv-todo:galpon:ack:v1", { schemaVersion: 1, operationId: event.operationId, sessionId: "session", phase: "link", status: "applied", todoId: event.todoId });
	});
	pi.events.on("galpon:todo:settle:v1", (event: any) => {
		pi.appendEntry("rpiv-todo:galpon-state:v1", { integration: "galpon", operationId: event.operationId, tasks: [], nextId: 8 });
		pi.events.emit("rpiv-todo:galpon:ack:v1", { schemaVersion: 1, operationId: event.operationId, sessionId: "session", phase: "settle", status: "applied", todoId: 7 });
	});

	settleModes.set("control-park-op", { parked: true, operation: { id: "control-park-op", state: "ready" } });
	claims.push({ operation: { id: "control-park-op", kind: "inbound", state: "claimed", parentMessageId: "request-control-park", attempt: 1, protocolGeneration: 3 }, message: { id: "request-control-park", kind: "request", act: "request", prompt: "control park work" } });
	await waitFor(() => pi.sent.some((item) => JSON.stringify(item.content).includes("control park work")), "control-park operation did not start");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	settleModes.set("control-park-op", { parked: false, operation: { id: "control-park-op", state: "failed" } });
	claims.push({ operation: { id: "control-park-op", kind: "inbound", state: "claimed", parentMessageId: "request-control-park", attempt: 2, protocolGeneration: 3 }, message: { id: "request-control-park", kind: "request", act: "request", prompt: "control park work" } });
	receiptBatches.set("control-park-op", { receipts: [{ id: "todo-link-receipt:todo:control-park", kind: "control", messageId: "control-child" }], results: [] });
	await waitFor(() => requests.filter((item) => /\/operations\/control-park-op\/settle$/.test(item.path)).length >= 2, "control-only resume did not re-submit its parked completion");
	const controlSettles = requests.filter((item) => /\/operations\/control-park-op\/settle$/.test(item.path));
	if (!controlSettles[0]?.body.error || controlSettles[1]?.body.error !== controlSettles[0]?.body.error) throw new Error("control-only resume lost the parked operation failure");

	claims.push({ operation: { id: "todo-link-op", kind: "direct", state: "claimed", attempt: 2, protocolGeneration: 3 } });
	receiptBatches.set("todo-link-op", { receipts: [{ id: "todo-link-receipt:todo:child", kind: "control", messageId: "child" }], results: [] });
	await waitFor(() => requests.some((item) => /\/todos\/links\/.*\/apply$/.test(item.path)), "TODO link intent was not applied");

	todoSettlement = { id: "todo-settlement:todo:child", intentId: "todo:child", resultId: "result:child", operationId: "todo-operation:todo-settlement:todo:child", operationAttempt: 1, state: "pending" };
	await waitFor(() => requests.some((item) => /\/todos\/settlements\/.*\/ack$/.test(item.path)), "TODO settlement was not acknowledged");
	const applySettlement = requests.find((item) => /\/todos\/settlements\/.*\/apply$/.test(item.path));
	if (!String(applySettlement?.body.snapshot ?? "").includes("daemon-settle")) throw new Error("TODO settlement snapshot was not sent after local persistence");

	const beforeRecoveryRegistrations = registrations;
	rejectNextClaim = true;
	await waitFor(() => registrations > beforeRecoveryRegistrations, "runtime did not re-register after recovery registration clearing", 5000);

	await pi.emit("session_shutdown", { reason: "reload" }, ctx);
	const second = new FakePi();
	second.entries = [...pi.entries];
	galpon(second as any);
	const replaySnapshots: any[] = [];
	second.events.on("galpon:todo:operation-snapshot:v1", value => replaySnapshots.push(value));
	const secondCtx = context(second);
	await second.emit("session_start", { reason: "reload" }, secondCtx);
	await waitFor(() => registrations > beforeRecoveryRegistrations + 1, "extension reload did not register the new runtime instance");
	await waitFor(() => replaySnapshots.at(-1)?.ownershipKnowledge === "exact", "reload replay did not reconcile ownership");
	if (JSON.stringify(replaySnapshots.at(-1)?.activeTaskIds) !== "[]") throw new Error("reload replay resurrected a globally removed TODO association");
	await second.emit("session_compact", { compactionEntry: { id: "compact", summary: "bounded", timestamp: new Date().toISOString() } }, secondCtx);
	if (JSON.stringify(replaySnapshots.at(-1)?.activeTaskIds) !== "[]" || replaySnapshots.at(-1)?.ownershipKnowledge !== "exact") throw new Error("compaction changed reconciled TODO ownership");
	await second.emit("session_shutdown", { reason: "reload" }, secondCtx);

	const observationReplay = new FakePi();
	observationReplay.entries.push(
		{ type: "custom", id: "pending-observation", customType: "galpon-operation", data: { operationId: "replay-operation", operationAttempt: 4, toolCallId: "replay-read-tool", messageIds: ["replay-child"], status: "result_observation_pending" }, timestamp: new Date().toISOString() },
		{ type: "message", id: "persisted-read-result", message: { role: "toolResult", toolCallId: "replay-read-tool", toolName: "galpon_read_message", content: [{ type: "text", text: "completed" }], details: { id: "replay-child", status: "completed" }, isError: false, timestamp: Date.now() }, timestamp: new Date().toISOString() },
	);
	observationReplay.entries.push(
		{ type: "custom", id: "presented-observation", customType: "galpon-operation", data: { operationId: "replay-operation", operationAttempt: 4, toolCallId: "replay-read-tool", messageIds: ["replay-child"], status: "result_observation_presented" }, timestamp: new Date().toISOString() },
		{ type: "custom", id: "interrupted-observation", customType: "galpon-operation", data: { operationId: "replay-operation", operationAttempt: 4, toolCallId: "interrupted-read-tool", messageIds: ["replay-child"], status: "result_observation_pending" }, timestamp: new Date().toISOString() },
	);
	galpon(observationReplay as any);
	const observationReplayCtx = context(observationReplay);
	await observationReplay.emit("session_start", { reason: "reload" }, observationReplayCtx);
	await delay(400);
	if (requests.some((item) => /\/observe-results$/.test(item.path) && item.body.toolCallId === "replay-read-tool")) throw new Error("replay used the old operation attempt before recovery");
	if (!observationReplay.entries.some((entry) => entry.data?.status === "result_observation_discarded" && entry.data?.toolCallId === "interrupted-read-tool")) throw new Error("unsaved tool result blocked operation recovery");
	claims.push({ operation: { id: "replay-operation", kind: "direct", state: "claimed", attempt: 5, protocolGeneration: 3 } });
	await waitFor(() => requests.some((item) => /\/observe-results$/.test(item.path) && item.body.toolCallId === "replay-read-tool"), "pending result observation did not replay after restart");
	const replayObservationRequest = requests.find((item) => /\/observe-results$/.test(item.path) && item.body.toolCallId === "replay-read-tool");
	if (replayObservationRequest?.body.operationId !== "replay-operation" || replayObservationRequest?.body.operationAttempt !== 5 || JSON.stringify(replayObservationRequest?.body.messageIds) !== '["replay-child"]') throw new Error("replayed observation lost its operation fence or message handles");
	if (!observationReplay.entries.some((entry) => entry.customType === "galpon-operation" && entry.data?.status === "result_observation_presented" && entry.data?.toolCallId === "replay-read-tool")) throw new Error("replayed observation completion was not persisted");
	await observationReplay.emit("session_shutdown", { reason: "reload" }, observationReplayCtx);

	// A live attempt can expire after a result read but before its acknowledgement.
	// Unlike restart replay, the extension still holds that attempt in memory.
	for (const scenario of [
		{ name: "busy-conflict", status: 409, stale: true, busy: true },
		{ name: "settle-conflict", status: 409, stale: true, busy: false },
		{ name: "missing-attempt", status: 404, stale: true, busy: true },
		{ name: "temporary-server-error", status: 503, stale: false, busy: true },
		{ name: "registration-conflict", status: 409, stale: false, busy: true },
		{ name: "late-conflict", status: 409, stale: true, busy: true, lateStatus: 409 },
		{ name: "late-success", status: 409, stale: true, busy: true, lateStatus: 200 },
	]) {
		const recovery = new FakePi();
		const recoveryCtx = context(recovery);
		const operationId = `observation-recovery-${scenario.name}`;
		const messageIds = [`${operationId}-first`, `${operationId}-second`];
		observedMessages.set(operationId, new Set());
		observationFailures.set(`${operationId}:1`, {
			status: scenario.status,
			error: scenario.stale ? "result observation no longer belongs to this operation attempt" : "runtime registration is temporarily unavailable",
			remaining: scenario.stale ? Infinity : 1,
		});
		if (scenario.lateStatus) holdObservations.set(`${operationId}:1`, scenario.lateStatus);
		galpon(recovery as any);
		await recovery.emit("session_start", { reason: "startup" }, recoveryCtx);
		claims.push({ operation: { id: operationId, kind: "direct", userEntryId: `${operationId}-input`, state: "claimed", attempt: 1, protocolGeneration: 3 } });
		await waitFor(() => recovery.sent.length === 1, `${scenario.name}: initial objective did not start`);
		(recoveryCtx as any).isIdle = () => false;
		await recovery.emit("agent_start", {}, recoveryCtx);
		const savedResults: any[] = [];
		for (const [index, messageId] of messageIds.entries()) {
			const toolCallId = `${operationId}-read-${index}`;
			const result = await recovery.tools.get("galpon_read_message").execute(toolCallId, { message_id: messageId }, undefined);
			savedResults.push({ type: "message", id: `${toolCallId}-entry`, message: { role: "toolResult", toolCallId, toolName: "galpon_read_message", content: result.content, details: result.details, isError: false, timestamp: Date.now() }, timestamp: new Date().toISOString() });
		}
		if (scenario.busy) {
			// Scheduler had one acknowledged read before its attempt expired.
			if (scenario.name === "busy-conflict") observationFailures.get(`${operationId}:1`)!.remaining = 0;
			for (const [index, entry] of savedResults.entries()) {
				recovery.entries.push(entry);
				await recovery.emit("message_end", { message: entry.message }, recoveryCtx);
				if (scenario.name === "busy-conflict" && index === 0) {
					await waitFor(() => recovery.entries.some(item => item.data?.toolCallId === entry.message.toolCallId && item.data?.status === "result_observation_presented"), "first result observation was not saved before expiry");
					observationFailures.get(`${operationId}:1`)!.remaining = Infinity;
				}
			}
			await waitFor(() => requests.some(item => item.path.includes(`${operationId}/observe-results`)), `${scenario.name}: acknowledgement was not attempted`);
			await delay(800);
			if (recovery.entries.some(entry => entry.data?.operationId === operationId && entry.data?.status === "stale_attempt")) throw new Error(`${scenario.name}: lost model correlation before its final response`);
			if (scenario.stale) {
				const count = requests.filter(item => item.path.includes(`${operationId}/observe-results`)).length;
				await delay(800);
				if (requests.filter(item => item.path.includes(`${operationId}/observe-results`)).length !== count) throw new Error(`${scenario.name}: repeatedly retried an expired observation attempt`);
			}
		} else {
			recovery.entries.push(...savedResults);
		}
		if (scenario.stale) claims.push({ operation: { id: operationId, kind: "direct", userEntryId: `${operationId}-input`, state: "claimed", attempt: 2, protocolGeneration: 3 } });
		receiptBatches.set(operationId, {
			receipts: messageIds.map(messageId => ({ id: `receipt:${messageId}`, kind: "result", messageId, resultId: `result:${messageId}` })),
			results: messageIds.map(messageId => ({ id: `result:${messageId}`, messageId, status: "completed", response: "already read helper result" })),
		});
		const finalText = `Saved final response for ${scenario.name}`;
		const finalMessage = { role: "assistant", content: [{ type: "text", text: finalText }], timestamp: Date.now() };
		recovery.entries.push({ type: "message", id: `${operationId}-final`, message: finalMessage, timestamp: new Date().toISOString() });
		await recovery.emit("message_end", { message: finalMessage }, recoveryCtx);
		(recoveryCtx as any).isIdle = () => true;
		await recovery.emit("agent_settled", {}, recoveryCtx);
		await waitFor(() => recovery.entries.some(entry => entry.data?.operationId === operationId && entry.data?.status === "settled"), `${scenario.name}: saved completion is stuck behind result acknowledgement`);
		const settles = requests.filter(item => item.path.includes(`${operationId}/settle`));
		if (settles.length !== 1 || settles[0].body.response !== finalText || settles[0].body.attempt !== (scenario.stale ? 2 : 1)) throw new Error(`${scenario.name}: completion was lost, repeated, or submitted under stale ownership`);
		if (observedMessages.get(operationId)?.size !== messageIds.length) throw new Error(`${scenario.name}: result observation evidence was lost`);
		if (recovery.sent.length !== 1) throw new Error(`${scenario.name}: recovery triggered another model turn for results already read`);
		if (recovery.entries.some(entry => entry.data?.status === "result_observation_discarded")) throw new Error(`${scenario.name}: recovery discarded a saved result`);
		const recovered = recovery.entries.some(entry => entry.data?.operationId === operationId && entry.data?.status === "stale_attempt");
		if (recovered !== scenario.stale) throw new Error(`${scenario.name}: wrong stale-attempt classification`);
		const nextInput = await recovery.emit("input", { text: `New objective after ${scenario.name}`, source: "interactive" }, recoveryCtx);
		if (nextInput?.action !== "continue") throw new Error(`${scenario.name}: recovered operation still blocks user input`);
		if (scenario.lateStatus) {
			const presentCount = recovery.entries.filter(entry => entry.data?.operationId === operationId && entry.data?.status === "result_observation_presented").length;
			const reply = heldObservationReplies.get(operationId);
			if (!reply) throw new Error(`${scenario.name}: no delayed response to test`);
			reply();
			await delay(100);
			if (recovery.entries.filter(entry => entry.data?.operationId === operationId && entry.data?.status === "result_observation_presented").length !== presentCount) throw new Error(`${scenario.name}: late response changed settled observation evidence`);
			await recovery.tools.get("galpon_send_agent").execute(`${operationId}-new-send`, { agent: "worker", prompt: "New objective work", act: "inform" }, undefined);
			const newSend = requests.find(item => item.body.requestId === `${operationId}-new-send`);
			if (!newSend?.body.operationId || newSend.body.operationId === operationId) throw new Error(`${scenario.name}: late response cleared or reused the new objective`);
		}
		await recovery.emit("agent_start", {}, recoveryCtx);
		await recovery.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "New objective finished" }], timestamp: Date.now() } }, recoveryCtx);
		await recovery.emit("agent_settled", {}, recoveryCtx);
		await recovery.emit("session_shutdown", { reason: "quit" }, recoveryCtx);
	}

	const overflow = new FakePi();
	for (let index = 0; index < 257; index++) {
		const operationId = `overflow-${index}`;
		overflow.entries.push({ type: "custom", id: `overflow-entry-${index}`, customType: "galpon-operation", data: { operationId, operationAttempt: 1, status: "todo_associated", todoId: index + 1 }, timestamp: new Date().toISOString() });
		operationOwnershipStates.set(operationId, "waiting");
	}
	galpon(overflow as any);
	const overflowSnapshots: any[] = [];
	overflow.events.on("galpon:todo:operation-snapshot:v1", value => overflowSnapshots.push(value));
	const overflowCtx = context(overflow);
	await overflow.emit("session_start", { reason: "reload" }, overflowCtx);
	await waitFor(() => overflowSnapshots.length > 0, "overflow replay did not publish ownership state");
	await delay(400);
	if (overflowSnapshots.at(-1)?.ownershipKnowledge !== "unknown" || overflowSnapshots.at(-1)?.activeTaskIds?.length !== 256) throw new Error("more than 256 associations showed a false exact ready count");
	await overflow.emit("session_shutdown", { reason: "quit" }, overflowCtx);
	await new Promise<void>((resolve) => server.close(() => resolve()));
	return requests.filter(item => item.path === "/v1/runtime/tools/report_progress").map(item => item.body.args.event_id);
}

export default async function () {
	try {
		const progressEventIds = await run();
		if (resultPath) writeFileSync(resultPath, JSON.stringify({ ok: true, progressEventIds }), { mode: 0o600 });
	} catch (error) {
		const message = error instanceof Error ? error.stack ?? error.message : String(error);
		if (resultPath) writeFileSync(resultPath, JSON.stringify({ ok: false, error: message }), { mode: 0o600 });
		throw error;
	}
}
