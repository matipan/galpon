import { createHash } from "node:crypto";
import { EventEmitter } from "node:events";
import { existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
	closeNativeReview, maxNativeSnapshotBytes, nativeReviewDirectory, nativeReviewEnvironment, nativeReviewEvent,
	nativeReviewInput, parseNativeReviewRuntime, readNativeReviewSnapshot, recoverNativeReview, runNativeReview,
	validateNativeReviewSnapshot, type NativeReviewHandle, type NativeReviewSnapshot,
} from "../galpon-neovim-review.ts";

function assert(value: unknown, message: string): asserts value { if (!value) throw new Error(message); }
function rejects(fn: () => unknown, message: string) {
	try { fn(); } catch { return; }
	throw new Error(message);
}
const wait = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));
const hash = (text: string) => createHash("sha256").update(text).digest("hex");
const text = "one e\u0301 👨‍👩‍👧‍👦 last\n\n  next  ";
const source = { entryId: "source-entry", text, hash: hash(text) };
const runId = "12345678-1234-1234-1234-123456789abc";
const handle: NativeReviewHandle = { version: 1, runId, sessionId: "session-a", sourceEntryId: source.entryId, sourceHash: source.hash, status: "open" };
const quote = "👨‍👩‍👧‍👦";
const item = { id: "first", start: 0, end: 0, startColumn: text.indexOf(quote), endColumn: text.indexOf(quote) + quote.length, quote, comment: "Keep the complete emoji." };
const snapshot = (): NativeReviewSnapshot => ({ version: 1, runId, sourceEntryId: source.entryId, sourceHash: source.hash, sourceTextHash: hash(text), revision: 1, status: "open", items: [{ ...item }] });
const palette = Object.fromEntries(["Background", "Surface", "SurfaceRaised", "Prompt", "Selection", "Border", "Foreground", "Muted", "Comment", "Status", "StatusInk", "Blue", "Cyan", "Purple", "Green", "Orange", "Red", "Yellow", "Teal"].map(key => [key, "#222436"]));

async function run() {
	const root = mkdtempSync(join(tmpdir(), "galpon-neovim-bridge-"));
	try {
		assert(validateNativeReviewSnapshot(snapshot(), source, runId).items[0].quote === quote, "valid Unicode snapshot was rejected");
		for (const mutate of [
			(value: any) => { value.version = 2; },
			(value: any) => { value.runId = "different"; },
			(value: any) => { value.sourceEntryId = "different"; },
			(value: any) => { value.sourceHash = hash("other text"); },
			(value: any) => { value.sourceTextHash = hash("another normalization"); },
			(value: any) => { value.corePid = -1; },
			(value: any) => { value.revision = -1; },
			(value: any) => { value.status = "send"; },
			(value: any) => { value.items[0].start = "0"; },
			(value: any) => { value.items[0].end = 99; },
			(value: any) => { value.items[0].startColumn++; },
			(value: any) => { value.items[0].endColumn = value.items[0].startColumn + 2; value.items[0].quote = "👨"; },
			(value: any) => { value.items[0].quote = "different"; },
			(value: any) => { value.items[0].comment = ""; },
			(value: any) => { value.items[0].comment = "x".repeat(140 * 1024); },
			(value: any) => { value.items[0].id = "x".repeat(129); },
			(value: any) => { value.items.push({ ...value.items[0] }); },
			(value: any) => { value.items = Array.from({ length: 33 }, (_, index) => ({ ...item, id: String(index) })); },
			(value: any) => { value.editing = { ...item, kind: "edit", itemId: "missing", buffer: "unfinished" }; },
			(value: any) => { value.editing = { ...item, kind: "new", visualMode: "block", buffer: "unfinished" }; },
			(value: any) => { value.status = "prepare"; value.items = []; },
			(value: any) => { value.status = "prepare"; value.editing = { ...item, kind: "edit", itemId: item.id, buffer: "unfinished" }; },
		]) {
			const value = snapshot(); mutate(value);
			rejects(() => validateNativeReviewSnapshot(value, source, runId), "unsafe native snapshot was accepted");
		}
		const editing = { kind: "edit" as const, itemId: item.id, start: item.start, end: item.end, startColumn: item.startColumn, endColumn: item.endColumn, buffer: "Keep unfinished text.\nSecond line." };
		assert(validateNativeReviewSnapshot({ ...snapshot(), editing }, source, runId).editing?.buffer === editing.buffer, "active comment did not survive validation");
		const input = nativeReviewInput(source, handle, [item], editing, palette);
		assert(input.text === text && input.graphemes.length === 2 && input.sourceTextHash === hash(text), "native source or grapheme map changed text");
		assert(input.graphemes.some(value => value.startColumn === item.startColumn && value.endColumn === item.endColumn), "joined emoji boundaries were not supplied");
		const unsafe = { entryId: "unsafe", text: "\x1b[31mred\x1b[0m\tline", hash: "original-hash" };
		assert(nativeReviewInput(unsafe, handle, [], undefined, palette).text === "red    line", "native buffer did not use the existing source sanitizer");

		const env = nativeReviewEnvironment({ PATH: "/usr/bin", TERM: "xterm-256color", HOME: "/unrelated-home", XDG_CONFIG_HOME: "/unrelated-config", VIMINIT: "bad-init", LUA_PATH: "/unrelated-lua", NVIM_LISTEN_ADDRESS: "unrelated-server", GALPON_SOCKET: "unrelated-socket", MODEL_API_KEY: "test-only" }, join(root, "env"), "/prepared-runtime", runId);
		assert(env.PATH === "/usr/bin" && env.TERM === "xterm-256color" && env.HOME === join(root, "env", "home"), "native environment did not keep only needed terminal settings");
		for (const key of ["VIMINIT", "LUA_PATH", "NVIM_LISTEN_ADDRESS", "GALPON_SOCKET", "MODEL_API_KEY"]) assert(env[key] === undefined, `native child inherited ${key}`);
		assert(!Object.values(env).some(value => value?.includes("unrelated")), "native child retained a user config path");
		const runtime = { runtime: "/prepared-runtime", neovim: "/usr/bin/nvim", version: "0.12.5", palette };
		assert(parseNativeReviewRuntime(JSON.stringify(runtime)).neovim === runtime.neovim, "runtime metadata was rejected");
		rejects(() => parseNativeReviewRuntime(JSON.stringify({ ...runtime, neovim: "nvim; command" })), "relative command text was accepted");
		rejects(() => parseNativeReviewRuntime(JSON.stringify({ ...runtime, palette: { Background: "bad" } })), "invalid palette was accepted");

		const directory = nativeReviewDirectory(root, handle);
		mkdirSync(directory, { recursive: true });
		const path = join(directory, "snapshot.json");
		writeFileSync(path, JSON.stringify(snapshot()));
		const branch = [{ type: "custom", customType: nativeReviewEvent, data: handle }];
		assert(recoverNativeReview(root, branch, handle.sessionId, source)?.snapshot?.items[0].quote === quote, "matching interrupted run was not recovered");
		assert(!recoverNativeReview(root, branch, "another-session", source), "another session recovered a private native draft");
		assert(!recoverNativeReview(root, [], handle.sessionId, source), "an abandoned branch recovered a native draft");
		assert(!recoverNativeReview(root, [...branch, { ...branch[0], data: { ...handle, status: "closed" } }], handle.sessionId, source), "acknowledged native output was replayed");
		rejects(() => nativeReviewDirectory(root, { ...handle, runId: "../../outside" }), "native handle escaped its run directory");
		writeFileSync(path, "{");
		assert(Boolean(recoverNativeReview(root, branch, handle.sessionId, source)?.error), "corrupt recovery did not preserve the last Pi draft");
		writeFileSync(path, "x".repeat(maxNativeSnapshotBytes + 1));
		rejects(() => readNativeReviewSnapshot(root, handle, source), "oversized native snapshot was read");
		rmSync(path);
		writeFileSync(join(root, "outside.json"), JSON.stringify(snapshot()));
		symlinkSync(join(root, "outside.json"), path);
		rejects(() => readNativeReviewSnapshot(root, handle, source), "native snapshot followed a symlink");
		rmSync(path);
		writeFileSync(path, JSON.stringify(snapshot()));
		rejects(() => closeNativeReview(root, handle, () => { throw new Error("Pi save failed"); }), "acknowledgement failure was hidden");
		assert(readNativeReviewSnapshot(root, handle, source)?.revision === 1, "recovery file was removed before Pi acknowledged it");
		let acknowledged = false;
		closeNativeReview(root, handle, () => { acknowledged = Boolean(readFileSync(path)); });
		assert(acknowledged && !readNativeReviewSnapshot(root, handle, source), "successful acknowledgement did not precede run cleanup");

		const entryPoint = join(root, "entry.lua");
		writeFileSync(entryPoint, "-- fake child tests do not execute this file\n");
		let untracked: NativeReviewHandle | undefined;
		try {
			await runNativeReview({
				ctx: { ui: { custom: () => { throw new Error("child must not launch"); } } },
				source, sessionId: handle.sessionId, runtime, root, entryPoint, items: [],
				terminal: { input: { isTTY: true }, output: { isTTY: true, write: (() => true) as any } },
				onHandle: value => { untracked = value; throw new Error("initial handle save failed"); }, onSnapshot: () => {},
			});
			throw new Error("initial persistence failure was ignored");
		} catch (error) { assert(error instanceof Error && error.message === "initial handle save failed", "initial persistence failure was not returned"); }
		assert(untracked && !existsSync(nativeReviewDirectory(root, untracked)), "initial handle failure left an untracked source copy");
		for (const scenario of ["prepare", "failed-exit", "spawn-error", "throw-spawn", "malformed", "changed-revision", "persist-error", "handle-error", "stop"] as const) {
			const calls: string[] = [];
			const handles: NativeReviewHandle[] = [];
			let recorded = 0;
			let stop: (() => Promise<void>) | undefined;
			let fake: any;
			let asyncTicks = 0;
			const ticker = setInterval(() => { asyncTicks++; }, 10);
			const running = runNativeReview({
				ctx: { ui: { custom: (factory: any) => new Promise(resolve => factory({ stop: () => calls.push("stop"), start: () => calls.push("start"), requestRender: () => calls.push("render") }, {}, {}, resolve)) } },
				source, sessionId: handle.sessionId, runtime, root, entryPoint, items: [],
				terminal: { input: { isTTY: true }, output: { isTTY: true, write: (() => true) as any } },
				onHandle: value => { handles.push({ ...value }); if (scenario === "handle-error" && handles.length === 2) throw new Error("PID handle save failed"); },
				onSnapshot: () => { if (scenario === "persist-error") throw new Error("Pi persistence failed"); recorded++; },
				onStop: value => { stop = value; },
				spawn: ((_binary: string, args: string[], options: any) => {
					assert(args.includes("--noplugin") && args.includes("NONE") && options.stdio === "inherit" && options.env.HOME !== process.env.HOME, "native process did not receive isolated terminal arguments");
					if (scenario === "throw-spawn") throw new Error("spawn failed");
					fake = new EventEmitter();
					fake.pid = 987654321;
					fake.kill = (signal: string) => {
						calls.push(signal);
						if (scenario !== "handle-error" || signal !== "SIGTERM") setTimeout(() => fake.emit("close", null, signal), 0);
						return true;
					};
					const write = (status: string, revision: number) => writeFileSync(options.env.GALPON_REVIEW_NVIM_OUTPUT, JSON.stringify({ ...snapshot(), runId: options.env.GALPON_REVIEW_NVIM_RUN_ID, revision, status }));
					setTimeout(() => {
						if (scenario === "spawn-error") { fake.emit("error", new Error("missing executable")); return; }
						write("open", 1);
						if (scenario === "stop" || scenario === "handle-error") return;
						setTimeout(() => {
							if (scenario === "malformed") writeFileSync(options.env.GALPON_REVIEW_NVIM_OUTPUT, "{");
							else write("prepare", scenario === "changed-revision" ? 1 : 2);
							fake.emit("close", scenario === "failed-exit" ? 1 : 0, null);
						}, 260);
					}, 10);
					return fake;
				}) as any,
			});
			if (scenario === "stop") { await wait(60); assert(stop, "native process did not register shutdown cleanup"); await stop(); }
			let result;
			try { result = await running; } finally { clearInterval(ticker); }
			assert(calls.filter(value => value === "stop").length === 1 && calls.filter(value => value === "start").length === 1 && calls.at(-1) === "render", `${scenario}: Pi terminal ownership was not restored exactly once`);
			assert(!stop, `${scenario}: native shutdown callback was left active`);
			if (scenario === "prepare") {
				assert(!result.error && result.snapshot?.status === "prepare" && recorded === 1 && asyncTicks > 5, "native review did not preserve drafts while the Pi event loop remained active");
			} else assert(result.error, `${scenario}: native failure was treated as a successful prepare`);
			assert(handles[0].status === "open", `${scenario}: no durable recovery handle was recorded`);
			if (scenario === "handle-error") assert(calls.includes("SIGKILL"), "a failed PID save did not bound termination of an unresponsive child");
		}
	} finally { rmSync(root, { recursive: true, force: true }); }
}

export default async function () {
	const path = process.env.GALPON_NEOVIM_BRIDGE_TEST_RESULT;
	if (!path) throw new Error("missing native bridge test result path");
	try { await run(); writeFileSync(path, JSON.stringify({ ok: true })); }
	catch (error) { writeFileSync(path, JSON.stringify({ ok: false, error: error instanceof Error ? error.stack : String(error) })); }
}
