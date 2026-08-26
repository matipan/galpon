import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { unlinkSync, writeFileSync } from "node:fs";
import galpon from "../extension.ts";

type Handler = (event: any, ctx: any) => any;
type RequestRecord = { path: string; body: any };

const socketPath = process.env.GALPON_SOCKET!;
const resultPath = process.env.GALPON_COMMUNICATION_V2_TEST_RESULT;

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
	let maintenance = false;
	let registrations = 0;
	let directCount = 0;
	let rejectNextClaim = false;
	let failNextDirectAfterCommit = false;
	let awaitReceipt = "";
	let todoSettlement: any;
	const server = createServer(async (req, res) => {
		const value = await body(req);
		const path = req.url ?? "";
		requests.push({ path, body: value });
		if (path === "/v1/communication/protocol") return response(res, 200, { generation: 2, complete: true, maintenance });
		if (/\/register$/.test(path)) { registrations++; return response(res, 200, { registered: true, protocol: { generation: 2, complete: true, maintenance } }); }
		if (/\/delegated-status$/.test(path)) return response(res, 200, { activeDelegatedAgents: 0 });
		if (/\/work$/.test(path)) return response(res, 200, { work: [] });
		if (/\/status$/.test(path) || /\/conversation-events$/.test(path) || /\/stop$/.test(path)) return response(res, 200, {});
		if (/\/operations\/direct$/.test(path)) {
			directCount++;
			if (failNextDirectAfterCommit) {
				failNextDirectAfterCommit = false;
				return response(res, 500, { error: "lost direct registration response" });
			}
			return response(res, 200, { id: `direct:${value.userEntryId}`, kind: "direct", state: "running", userEntryId: value.userEntryId, attempt: 1 });
		}
		if (/\/operations\/claim$/.test(path)) {
			if (rejectNextClaim) { rejectNextClaim = false; return response(res, 409, { error: "runtime is not registered for communication protocol generation 2" }); }
			const claimId = String(value.claimId ?? "");
			const delivery = claimRetries.has(claimId) ? claimRetries.get(claimId) : claims.shift() ?? null;
			if (delivery && claimId) claimRetries.set(claimId, delivery);
			return response(res, 200, { delivery });
		}
		const operationMatch = path.match(/\/operations\/([^/]+)\/(start|renew|settle)$/);
		if (operationMatch) {
			const operationId = decodeURIComponent(operationMatch[1]!);
			if (operationMatch[2] === "settle") {
				for (const [claimId, delivery] of claimRetries) if (delivery?.operation?.id === operationId) claimRetries.delete(claimId);
				return response(res, 200, settleModes.get(operationId) ?? { parked: false, operation: { id: operationId, state: "settled" } });
			}
			return response(res, 200, {});
		}
		const takeMatch = path.match(/\/operations\/([^/]+)\/receipts\/take$/);
		if (takeMatch) return response(res, 200, receiptBatches.get(decodeURIComponent(takeMatch[1]!)) ?? { receipts: [], results: [] });
		if (/\/receipts\/[^/]+\/present$/.test(path)) return response(res, 200, { presented: true });
		if (/\/todos\/links\/[^/]+\/claim$/.test(path)) return response(res, 200, { id: "todo:child", messageId: "child", todoId: 7, policy: "complete_on_success", state: "pending", operationAttempt: value.operationAttempt });
		if (/\/todos\/links\/[^/]+\/(apply|fail)$/.test(path)) return response(res, 200, {});
		if (/\/todos\/settlements\/claim$/.test(path)) {
			if (!todoSettlement) return response(res, 404, { error: "not found" });
			const result = todoSettlement;
			todoSettlement = undefined;
			return response(res, 200, result);
		}
		if (/\/todos\/settlements\/[^/]+\/(apply|ack|fail)$/.test(path)) return response(res, 200, {});
		if (path === "/v1/runtime/tools/read_message") return response(res, 200, { id: "child", status: "completed", response: "todo done" });
		if (path === "/v1/runtime/tools/await_agent") return response(res, 200, { messageId: "child", status: awaitReceipt ? "completed" : "parked", waitStatus: awaitReceipt ? "completed" : "pending", messageStatus: awaitReceipt ? "completed" : "queued", receiptId: awaitReceipt, response: awaitReceipt ? "await done" : "" });
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
	if (firstRegistration?.body.protocolGeneration !== 2) throw new Error("registration omitted protocolGeneration");

	maintenance = true;
	const blocked = await pi.emit("input", { text: "blocked", source: "interactive" }, ctx);
	if (blocked?.action !== "handled" || directCount !== 0) throw new Error("maintenance input started direct work");
	maintenance = false;
	const accepted = await pi.emit("input", { text: "direct", source: "interactive" }, ctx);
	if (accepted?.action !== "continue") throw new Error("direct input was not accepted");
	pi.entries.push({ type: "message", id: "stable-user-entry", message: { role: "user", content: "direct" }, timestamp: new Date().toISOString() });
	await pi.emit("before_agent_start", { systemPrompt: "system", prompt: "direct" }, ctx);
	if (registrations < 2) throw new Error("runtime did not re-register after communication maintenance ended");
	if (directCount !== 1) throw new Error("direct operation was not registered");
	const directRequest = requests.find((item) => /\/operations\/direct$/.test(item.path));
	if (directRequest?.body.userEntryId !== "stable-user-entry" || directRequest.body.protocolGeneration !== 2) throw new Error("direct operation did not use the stable Pi user entry");

	await pi.emit("tool_execution_start", { toolName: "todo", toolCallId: "todo-active-31", args: { action: "update", id: 31, status: "pending" } }, ctx);
	await pi.emit("tool_execution_end", { toolName: "todo", toolCallId: "todo-active-31", isError: false }, ctx);
	const associatedSnapshot = todoOperationSnapshots.at(-1);
	if (JSON.stringify(associatedSnapshot?.activeTaskIds) !== "[31]") throw new Error("a successful pending TODO update did not publish its active Pi operation association");
	if ("operationId" in associatedSnapshot || JSON.stringify(associatedSnapshot).includes("direct:stable-user-entry")) throw new Error("the Pi-local TODO association event exposed an operation ID");
	if (!pi.entries.some((entry) => entry.customType === "galpon-operation" && entry.data?.status === "todo_associated" && entry.data?.todoId === 31)) throw new Error("the Pi operation TODO association was not durable");
	await pi.emit("tool_execution_start", { toolName: "todo", toolCallId: "todo-failed-32", args: { action: "update", id: 32, status: "in_progress" } }, ctx);
	await pi.emit("tool_execution_end", { toolName: "todo", toolCallId: "todo-failed-32", isError: true }, ctx);
	if (JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[31]") throw new Error("a failed TODO update created an active Pi operation association");

	claims.push({ operation: { id: "notify-op", kind: "direct", state: "claimed", attempt: 1, protocolGeneration: 2 } });
	receiptBatches.set("notify-op", { receipts: [{ id: "notify-receipt", kind: "result", messageId: "notify-child", resultId: "result:notify-child" }], results: [{ id: "result:notify-child", messageId: "notify-child", status: "completed", response: "notify result" }] });
	await delay(450);
	if (pi.sent.length !== 0) throw new Error("notify receipt entered an unrelated direct operation");

	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "direct done" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	if (JSON.stringify(todoOperationSnapshots.at(-1)?.activeTaskIds) !== "[]") throw new Error("a settled Pi operation kept an active TODO association");
	if (!pi.entries.some((entry) => entry.customType === "galpon-operation" && entry.data?.status === "todo_associations_cleared")) throw new Error("the settled Pi operation did not durably clear its TODO associations");
	await waitFor(() => pi.sent.some((item) => String(item.content).includes("independent notification")), "independent notify operation did not run");
	if (!requests.some((item) => item.path.includes("notify-receipt/present"))) throw new Error("notify receipt was not presented");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "notify handled" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);

	settleModes.set("inbound-op", { parked: true, operation: { id: "inbound-op", state: "waiting" } });
	claims.push({ operation: { id: "inbound-op", kind: "inbound", state: "claimed", parentMessageId: "request-1", attempt: 1, protocolGeneration: 2 }, message: { id: "request-1", kind: "request", act: "request", prompt: "do work", senderTitle: "Sender" } });
	await waitFor(() => pi.sent.some((item) => JSON.stringify(item.content).includes("do work")), "inbound delivery did not start");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "waiting" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	claims.push({ operation: { id: "inbound-op", kind: "inbound", state: "claimed", parentMessageId: "request-1", attempt: 2, protocolGeneration: 2 }, message: { id: "request-1", kind: "request", act: "request", prompt: "do work" } });
	receiptBatches.set("inbound-op", { receipts: [{ id: "join-receipt", kind: "result", messageId: "child", resultId: "result:child" }], results: [{ id: "result:child", messageId: "child", status: "completed", response: "child done" }] });
	settleModes.set("inbound-op", { parked: false, operation: { id: "inbound-op", state: "settled" } });
	await waitFor(() => pi.sent.some((item) => String(item.content).includes("Resume the same Pi objective")), "parked operation did not resume");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "resumed done" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);

	// A child can finish before the parent settle reaches the daemon. The first
	// settle then parks directly in ready state. The next attempt must take the
	// new receipt instead of replaying the completion that parked attempt one.
	settleModes.set("ready-race-op", { parked: true, operation: { id: "ready-race-op", state: "ready" } });
	claims.push({ operation: { id: "ready-race-op", kind: "inbound", state: "claimed", parentMessageId: "request-ready-race", attempt: 1, protocolGeneration: 2 }, message: { id: "request-ready-race", kind: "request", act: "request", prompt: "ready race work" } });
	await waitFor(() => pi.sent.some((item) => JSON.stringify(item.content).includes("ready race work")), "ready-race operation did not start");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "ready race partial" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	claims.push({ operation: { id: "ready-race-op", kind: "inbound", state: "claimed", parentMessageId: "request-ready-race", attempt: 2, protocolGeneration: 2 }, message: { id: "request-ready-race", kind: "request", act: "request", prompt: "ready race work" } });
	receiptBatches.set("ready-race-op", { receipts: [{ id: "ready-race-receipt", kind: "result", messageId: "ready-race-child", resultId: "result:ready-race-child" }], results: [{ id: "result:ready-race-child", messageId: "ready-race-child", status: "completed", response: "ready race child result" }] });
	settleModes.set("ready-race-op", { parked: false, operation: { id: "ready-race-op", state: "settled" } });
	await waitFor(() => pi.sent.some((item) => String(item.content).includes("ready race child result")), "ready-state park replayed the old completion without taking its receipt");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "ready race done" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);

	pi.failNextSend = true;
	claims.push({ operation: { id: "injection-retry-op", kind: "inbound", state: "claimed", parentMessageId: "request-injection-retry", attempt: 1, protocolGeneration: 2 }, message: { id: "request-injection-retry", kind: "request", act: "request", prompt: "retry failed Pi injection" } });
	await waitFor(() => pi.sent.some((item) => JSON.stringify(item.content).includes("retry failed Pi injection")), "failed Pi injection was not retried with the stable operation claim");
	const injectionClaims = requests.filter((item) => /\/operations\/claim$/.test(item.path) && claimRetries.get(String(item.body.claimId ?? ""))?.operation?.id === "injection-retry-op");
	if (injectionClaims.length < 2 || injectionClaims[0]?.body.claimId !== injectionClaims[1]?.body.claimId) throw new Error("failed Pi injection did not retry its stable claim ID");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "injection retry done" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);

	await pi.emit("input", { text: "await", source: "interactive" }, ctx);
	pi.entries.push({ type: "message", id: "await-user", message: { role: "user", content: "await" }, timestamp: new Date().toISOString() });
	await pi.emit("before_agent_start", { systemPrompt: "system", prompt: "await" }, ctx);
	awaitReceipt = "await-receipt";
	const awaitTool = pi.tools.get("galpon_await_agent");
	const awaitResult = await awaitTool.execute("await-tool-request", { message_id: "child" }, undefined, undefined);
	pi.entries.push({ type: "message", id: "await-result-entry", message: { role: "toolResult", toolCallId: "await-tool-request", toolName: "galpon_await_agent", content: awaitResult.content, details: awaitResult.details, isError: false, timestamp: Date.now() }, timestamp: new Date().toISOString() });
	await pi.emit("message_end", { message: pi.entries[pi.entries.length - 1].message }, ctx);
	await waitFor(() => requests.some((item) => item.path.includes("await-receipt/present")), "await receipt presentation did not follow Pi persistence");
	const persistedIndex = pi.entries.findIndex((entry) => entry.customType === "galpon-operation" && entry.data?.receiptId === "await-receipt");
	const presentIndex = requests.findIndex((item) => item.path.includes("await-receipt/present"));
	if (persistedIndex < 0 || presentIndex < 0) throw new Error("await receipt was not persisted and presented");
	const awaitRequest = requests.find((item) => item.path === "/v1/runtime/tools/await_agent");
	if (awaitRequest?.body.operationId !== "direct:await-user" || awaitRequest.body.operationAttempt !== 1 || awaitRequest.body.protocolGeneration !== 2 || awaitRequest.body.requestId !== "await-tool-request") throw new Error("await request omitted generation-2 fencing");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [{ type: "text", text: "await handled" }], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);

	pi.aborted = false;
	failNextDirectAfterCommit = true;
	pi.failNextSend = true;
	const sentBeforeRecovery = pi.sent.length;
	await pi.emit("input", { text: "recover direct", source: "interactive" }, ctx);
	pi.entries.push({ type: "message", id: "recover-user", message: { role: "user", content: "recover direct" }, timestamp: new Date().toISOString() });
	await pi.emit("before_agent_start", { systemPrompt: "system", prompt: "recover direct" }, ctx);
	if (!pi.aborted) throw new Error("unknown direct registration result did not abort the first model start");
	await waitFor(() => pi.sent.length > sentBeforeRecovery && pi.sent.some((item) => item.details?.operationId === "direct:recover-user"), "unknown direct registration result was not recovered");
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
	claims.push({ operation: { id: "control-park-op", kind: "inbound", state: "claimed", parentMessageId: "request-control-park", attempt: 1, protocolGeneration: 2 }, message: { id: "request-control-park", kind: "request", act: "request", prompt: "control park work" } });
	await waitFor(() => pi.sent.some((item) => JSON.stringify(item.content).includes("control park work")), "control-park operation did not start");
	await pi.emit("agent_start", {}, ctx);
	await pi.emit("message_end", { message: { role: "assistant", content: [], timestamp: Date.now() } }, ctx);
	await pi.emit("agent_settled", {}, ctx);
	settleModes.set("control-park-op", { parked: false, operation: { id: "control-park-op", state: "failed" } });
	claims.push({ operation: { id: "control-park-op", kind: "inbound", state: "claimed", parentMessageId: "request-control-park", attempt: 2, protocolGeneration: 2 }, message: { id: "request-control-park", kind: "request", act: "request", prompt: "control park work" } });
	receiptBatches.set("control-park-op", { receipts: [{ id: "todo-link-receipt:todo:control-park", kind: "control", messageId: "control-child" }], results: [] });
	await waitFor(() => requests.filter((item) => /\/operations\/control-park-op\/settle$/.test(item.path)).length >= 2, "control-only resume did not re-submit its parked completion");
	const controlSettles = requests.filter((item) => /\/operations\/control-park-op\/settle$/.test(item.path));
	if (!controlSettles[0]?.body.error || controlSettles[1]?.body.error !== controlSettles[0]?.body.error) throw new Error("control-only resume lost the parked operation failure");

	claims.push({ operation: { id: "todo-link-op", kind: "direct", state: "claimed", attempt: 2, protocolGeneration: 2 } });
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
	galpon(second as any);
	await second.emit("session_start", { reason: "reload" }, context(second));
	await waitFor(() => registrations > beforeRecoveryRegistrations + 1, "extension reload did not register the new runtime instance");
	await second.emit("session_shutdown", { reason: "quit" }, context(second));
	await new Promise<void>((resolve) => server.close(() => resolve()));
}

export default async function () {
	try {
		await run();
		if (resultPath) writeFileSync(resultPath, JSON.stringify({ ok: true }), { mode: 0o600 });
	} catch (error) {
		const message = error instanceof Error ? error.stack ?? error.message : String(error);
		if (resultPath) writeFileSync(resultPath, JSON.stringify({ ok: false, error: message }), { mode: 0o600 });
		throw error;
	}
}
