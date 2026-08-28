import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { unlinkSync, writeFileSync } from "node:fs";
import galpon from "../extension.ts";

type Handler = (event: any, ctx: any) => any;

const socketPath = process.env.GALPON_SOCKET!;
const resultPath = process.env.GALPON_CONTEXTUAL_ACTIVITY_TEST_RESULT!;

function delay(ms: number) {
	return new Promise(resolve => setTimeout(resolve, ms));
}

async function waitFor(predicate: () => boolean, message: string, timeout = 5000) {
	const deadline = Date.now() + timeout;
	while (!predicate()) {
		if (Date.now() >= deadline) throw new Error(message);
		await delay(20);
	}
}

async function requestBody(req: IncomingMessage) {
	const chunks: Buffer[] = [];
	for await (const chunk of req) chunks.push(Buffer.from(chunk));
	const text = Buffer.concat(chunks).toString("utf8");
	return text ? JSON.parse(text) : {};
}

function response(res: ServerResponse, value: any) {
	res.writeHead(200, { "content-type": "application/json" });
	res.end(JSON.stringify(value));
}

class FakePi {
	handlers = new Map<string, Handler[]>();
	entries: any[] = [];
	emitted: Array<{ name: string; value: any }> = [];
	events = {
		on: () => () => {},
		emit: (name: string, value: any) => { this.emitted.push({ name, value }); },
	};
	on(name: string, handler: Handler) {
		const values = this.handlers.get(name) ?? [];
		values.push(handler);
		this.handlers.set(name, values);
	}
	registerTool() {}
	registerCommand() {}
	appendEntry(customType: string, data: any) {
		this.entries.push({ type: "custom", id: `entry-${this.entries.length + 1}`, customType, data });
	}
	sendUserMessage() {}
	sendMessage() {}
	setSessionName() {}
	async emit(name: string, event: any, ctx: any) {
		let result: any;
		for (const handler of this.handlers.get(name) ?? []) result = await handler(event, ctx);
		return result;
	}
}

function makeContext(pi: FakePi, state: { idle: boolean }, statusWrites: string[]) {
	return {
		mode: "tui",
		hasUI: true,
		sessionManager: {
			getSessionId: () => "session",
			getSessionFile: () => "/tmp/session.jsonl",
			getBranch: () => pi.entries,
			getLeafId: () => "leaf",
			getLeafEntry: () => undefined,
		},
		isIdle: () => state.idle,
		abort: () => {},
		shutdown: () => {},
		ui: {
			setStatus: (_key: string, value: string) => { statusWrites.push(value); }, setTitle: () => {}, notify: () => {}, setEditorText: () => {},
			confirm: async () => false,
		},
	};
}

async function run() {
	try { unlinkSync(socketPath); } catch {}
	const events: string[] = [];
	const delegated: Array<{ project: boolean; active: number }> = [];
	let active = 2;
	const server = createServer(async (req, res) => {
		const body = await requestBody(req);
		const path = req.url ?? "";
		if (path === "/v1/communication/protocol") return response(res, { generation: 1, complete: false, maintenance: false });
		if (/\/register$/.test(path)) return response(res, { registered: true });
		if (/\/delegated-status$/.test(path)) {
			const project = body.projectContextualActivity === true;
			delegated.push({ project, active });
			events.push(`delegated:${project}:${active}`);
			return response(res, { activeDelegatedAgents: active, activeDelegatedRequests: 0, waitingJoinedWork: 0, activeDelegatedWork: active });
		}
		if (/\/status$/.test(path)) {
			events.push(`status:${String(body.status)}`);
			return response(res, {});
		}
		if (/\/work$/.test(path)) return response(res, { work: [] });
		if (/\/claim$/.test(path)) return response(res, { messages: [] });
		if (/\/conversation-events$/.test(path) || /\/stop$/.test(path)) return response(res, {});
		return response(res, {});
	});
	await new Promise<void>((resolve, reject) => server.listen(socketPath, (error?: Error) => error ? reject(error) : resolve()));

	const pi = new FakePi();
	galpon(pi as any);
	let normalSettledRan = false;
	pi.on("agent_settled", () => {
		normalSettledRan = true;
		events.push("normal-agent-settled");
	});
	const state = { idle: true };
	const statusWrites: string[] = [];
	const ctx = makeContext(pi, state, statusWrites);
	await pi.emit("session_start", { reason: "startup" }, ctx);
	await waitFor(() => delegated.some(item => item.project && item.active === 2), "startup did not replay idle contextual activity");

	const activeStart = events.length;
	state.idle = false;
	await pi.emit("agent_start", {}, ctx);
	await waitFor(() => events.slice(activeStart).some(item => item === "delegated:false:2"), "active Pi did not suppress contextual projection");
	const activeEvents = events.slice(activeStart);
	if (activeEvents.indexOf("status:running") < 0 || activeEvents.indexOf("status:running") > activeEvents.indexOf("delegated:false:2")) {
		throw new Error(`active ordering was unsafe: ${activeEvents.join(",")}`);
	}

	const settledStart = events.length;
	state.idle = true;
	await pi.emit("agent_settled", {}, ctx);
	await waitFor(() => events.slice(settledStart).some(item => item === "delegated:true:2"), "settled Pi did not restore delegated working state");
	const settledEvents = events.slice(settledStart);
	if (!normalSettledRan || settledEvents.indexOf("normal-agent-settled") > settledEvents.indexOf("delegated:true:2")) {
		throw new Error(`contextual projection ran before normal settled handlers: ${settledEvents.join(",")}`);
	}
	if (statusWrites.length !== 2) throw new Error(`unchanged delegated status redrew the Pi footer: ${statusWrites.join(",")}`);
	const workSnapshots = pi.emitted.filter(item => item.name === "galpon:work:snapshot:v1");
	if (workSnapshots.length !== 1) throw new Error(`unchanged work snapshots redrew the Pi widget ${workSnapshots.length} times`);

	active = 0;
	const refreshStart = delegated.length;
	await waitFor(() => delegated.slice(refreshStart).some(item => item.project && item.active === 0), "periodic refresh did not correct contextual state", 4500);

	await pi.emit("session_shutdown", { reason: "reload" }, ctx);
	const replayStart = delegated.length;
	active = 1;
	await pi.emit("session_start", { reason: "reload" }, ctx);
	await waitFor(() => delegated.slice(replayStart).some(item => item.project && item.active === 1), "reload did not replay contextual state");
	await pi.emit("session_shutdown", { reason: "exit" }, ctx);
	await new Promise<void>(resolve => server.close(() => resolve()));
}

export default async function () {
	try {
		await run();
		writeFileSync(resultPath, JSON.stringify({ ok: true, error: "" }), { mode: 0o600 });
	} catch (error) {
		const message = error instanceof Error ? error.stack ?? error.message : String(error);
		writeFileSync(resultPath, JSON.stringify({ ok: false, error: message }), { mode: 0o600 });
		throw error;
	}
}
