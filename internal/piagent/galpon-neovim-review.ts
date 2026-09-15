import { spawn as spawnProcess, type ChildProcess } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { constants, closeSync, fstatSync, lstatSync, mkdirSync, openSync, readFileSync, readSync, rmSync, writeFileSync } from "node:fs";
import { isAbsolute, join } from "node:path";
import {
	compileReview, isReviewColumnBoundary, maxReviewDraftBytes, maxReviewItems,
	maxReviewSelectionBytes, maxReviewSourceBytes, parseReviewBuffer, reviewSelection,
	sanitizeReviewText, type ReviewEditingDraft, type ReviewItem,
} from "./galpon-review.ts";

export const nativeReviewEvent = "galpon:review:nvim:v1";
export const maxNativeSnapshotBytes = 1024 * 1024;
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const digest = (text: string) => createHash("sha256").update(text).digest("hex");

export type NativeReviewSource = { entryId: string; text: string; hash: string };
export type NativeReviewHandle = {
	version: 1;
	runId: string;
	sessionId: string;
	sourceEntryId: string;
	sourceHash: string;
	status: "open" | "closed";
	pid?: number;
	corePid?: number;
};
export type NativeReviewSnapshot = {
	version: 1;
	runId: string;
	sourceEntryId: string;
	sourceHash: string;
	sourceTextHash: string;
	revision: number;
	corePid?: number;
	status: "open" | "prepare" | "cancel";
	items: ReviewItem[];
	editing?: ReviewEditingDraft;
};
export type NativeReviewRuntime = {
	runtime: string;
	neovim: string;
	version: string;
	palette: Record<string, string>;
};

export function parseNativeReviewRuntime(text: string): NativeReviewRuntime {
	if (Buffer.byteLength(text) > 64 * 1024) throw new Error("Neovim Review configuration is too large.");
	const value = JSON.parse(text);
	if (!value || typeof value !== "object" || typeof value.version !== "string"
		|| typeof value.runtime !== "string" || !isAbsolute(value.runtime) || value.runtime.includes("\0")
		|| typeof value.neovim !== "string" || !isAbsolute(value.neovim) || value.neovim.includes("\0")
		|| !value.palette || typeof value.palette !== "object" || Array.isArray(value.palette)) {
		throw new Error("Neovim Review configuration is invalid.");
	}
	const palette: Record<string, string> = {};
	for (const name of ["Background", "Surface", "SurfaceRaised", "Prompt", "Selection", "Border", "Foreground", "Muted", "Comment", "Status", "StatusInk", "Blue", "Cyan", "Purple", "Green", "Orange", "Red", "Yellow", "Teal"]) {
		if (typeof value.palette[name] !== "string" || !/^#[0-9a-f]{6}$/i.test(value.palette[name])) throw new Error("Neovim Review palette is invalid.");
		palette[name] = value.palette[name];
	}
	return { runtime: value.runtime, neovim: value.neovim, version: value.version, palette };
}

export function nativeReviewInput(source: NativeReviewSource, handle: NativeReviewHandle, items: ReviewItem[], editing: ReviewEditingDraft | undefined, palette: Record<string, string>) {
	if (Buffer.byteLength(source.text) > maxReviewSourceBytes) throw new Error("The response is too large for Neovim Review.");
	const lines = parseReviewBuffer(source.text);
	const text = lines.map(line => line.text).join("\n");
	const graphemes: Array<{ line: number; startColumn: number; endColumn: number }> = [];
	const segmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });
	for (const line of lines) {
		for (const segment of segmenter.segment(line.text)) {
			const firstLength = (segment.segment.codePointAt(0) ?? 0) > 0xffff ? 2 : 1;
			if (segment.segment.length > firstLength) graphemes.push({ line: line.index, startColumn: segment.index, endColumn: segment.index + segment.segment.length });
		}
	}
	return {
		version: 1, runId: handle.runId, sourceEntryId: source.entryId, sourceHash: source.hash,
		sourceTextHash: digest(text), text, items, ...(editing ? { editing } : {}), graphemes, palette,
		limits: { items: maxReviewItems, selectionBytes: maxReviewSelectionBytes, draftBytes: maxReviewDraftBytes },
	};
}

export function validateNativeReviewSnapshot(raw: any, source: NativeReviewSource, runId: string): NativeReviewSnapshot {
	const fail = (): never => { throw new Error("Neovim Review returned an invalid draft. The saved Pi draft was kept."); };
	if (!raw || raw.version !== 1 || raw.runId !== runId || raw.sourceEntryId !== source.entryId || raw.sourceHash !== source.hash
		|| !Number.isSafeInteger(raw.revision) || raw.revision < 1 || !["open", "prepare", "cancel"].includes(raw.status)
		|| !Array.isArray(raw.items) || raw.items.length > maxReviewItems
		|| (raw.corePid !== undefined && (!Number.isSafeInteger(raw.corePid) || raw.corePid < 1))) fail();
	const lines = parseReviewBuffer(source.text);
	const sourceTextHash = digest(lines.map(line => line.text).join("\n"));
	if (raw.sourceTextHash !== sourceTextHash) fail();
	const range = (item: any) => {
		if (!item) return fail();
		const { start, end, startColumn, endColumn } = item;
		if (![start, end, startColumn, endColumn].every(value => Number.isInteger(value))
			|| start < 0 || end < start || end >= lines.length || startColumn < 0 || endColumn < 0
			|| !isReviewColumnBoundary(lines[start].text, startColumn) || !isReviewColumnBoundary(lines[end].text, endColumn)
			|| (start === end && startColumn >= endColumn)) fail();
		const quote = reviewSelection(lines, start, end, startColumn, endColumn);
		if (!quote || Buffer.byteLength(quote) > maxReviewSelectionBytes) fail();
		return { start, end, startColumn, endColumn, quote };
	};
	const ids = new Set<string>();
	const items: ReviewItem[] = raw.items.map((item: any) => {
		if (typeof item?.id !== "string" || !item.id || item.id.length > 128 || /[\p{Cc}\p{Cf}]/u.test(item.id)
			|| ids.has(item.id) || typeof item.comment !== "string") fail();
		ids.add(item.id);
		const selected = range(item);
		const comment = sanitizeReviewText(item.comment).trim();
		if (item.quote !== selected.quote || !comment) fail();
		return { id: item.id, ...selected, comment };
	});
	let editing: ReviewEditingDraft | undefined;
	if (raw.editing !== undefined && raw.editing !== null) {
		const value = raw.editing;
		if (!["new", "edit"].includes(value.kind) || typeof value.buffer !== "string" || raw.status === "prepare") fail();
		const { quote: _quote, ...selected } = range(value);
		if (value.kind === "edit") {
			const item = items.find(candidate => candidate.id === value.itemId);
			if (!item || Object.entries(selected).some(([key, position]) => (item as any)[key] !== position)) fail();
			editing = { kind: "edit", itemId: item.id, ...selected, buffer: sanitizeReviewText(value.buffer) };
		} else {
			if (value.visualMode !== "character" && value.visualMode !== "line") fail();
			editing = { kind: "new", ...selected, visualMode: value.visualMode, buffer: sanitizeReviewText(value.buffer) };
		}
	}
	if (Buffer.byteLength(compileReview(items)) + Buffer.byteLength(editing?.buffer ?? "") > maxReviewDraftBytes) fail();
	if (raw.status === "prepare" && items.length === 0) fail();
	return {
		version: 1, runId, sourceEntryId: source.entryId, sourceHash: source.hash, sourceTextHash,
		revision: raw.revision, status: raw.status, items, ...(editing ? { editing } : {}),
		...(raw.corePid !== undefined ? { corePid: raw.corePid } : {}),
	};
}

function validHandle(value: any): value is NativeReviewHandle {
	return value?.version === 1 && typeof value.runId === "string" && uuidPattern.test(value.runId)
		&& typeof value.sessionId === "string" && value.sessionId.length > 0 && value.sessionId.length <= 256
		&& typeof value.sourceEntryId === "string" && typeof value.sourceHash === "string"
		&& ["open", "closed"].includes(value.status)
		&& [value.pid, value.corePid].every(pid => pid === undefined || (Number.isSafeInteger(pid) && pid > 0));
}

export function nativeReviewDirectory(root: string, handle: NativeReviewHandle): string {
	if (!isAbsolute(root) || !validHandle(handle)) throw new Error("Neovim Review run identity is invalid.");
	return join(root, digest(handle.sessionId), handle.runId);
}

export function readNativeReviewSnapshot(root: string, handle: NativeReviewHandle, source: NativeReviewSource): NativeReviewSnapshot | undefined {
	const path = join(nativeReviewDirectory(root, handle), "snapshot.json");
	let fd: number;
	try { fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW); }
	catch (error: any) { if (error?.code === "ENOENT") return undefined; throw error; }
	try {
		const stat = fstatSync(fd);
		if (!stat.isFile() || stat.size > maxNativeSnapshotBytes) throw new Error("Neovim Review snapshot is not a bounded file.");
		const bytes = Buffer.alloc(Math.min(stat.size + 1, maxNativeSnapshotBytes + 1));
		const length = readSync(fd, bytes, 0, bytes.length, 0);
		if (length > stat.size || length > maxNativeSnapshotBytes) throw new Error("Neovim Review snapshot changed while reading.");
		return validateNativeReviewSnapshot(JSON.parse(bytes.subarray(0, length).toString("utf8")), source, handle.runId);
	} finally { closeSync(fd); }
}

function nativePIDState(pid: number | undefined, runId: string): "live" | "absent" | "unknown" {
	if (!pid) return "absent";
	if (process.platform === "linux") {
		try {
			return readFileSync(`/proc/${pid}/environ`, "utf8").split("\0").includes(`GALPON_REVIEW_NVIM_RUN_ID=${runId}`) ? "live" : "absent";
		} catch (error: any) { return error?.code === "ENOENT" || error?.code === "ESRCH" ? "absent" : "unknown"; }
	}
	try { process.kill(pid, 0); return "unknown"; } catch { return "absent"; }
}

function nativeRunIsLive(handle: NativeReviewHandle): boolean {
	return [handle.pid, handle.corePid].some(pid => nativePIDState(pid, handle.runId) !== "absent");
}

async function drainNativeCore(handle: NativeReviewHandle) {
	// Modern Neovim runs its core below the terminal UI process. Let the core
	// flush VimLeavePre after the UI exits before Pi imports the final snapshot.
	const live = () => nativePIDState(handle.corePid, handle.runId) === "live";
	const waitUntil = async (milliseconds: number) => {
		const deadline = Date.now() + milliseconds;
		while (live() && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 20));
	};
	await waitUntil(200);
	if (live()) { try { process.kill(handle.corePid!, "SIGTERM"); } catch {} }
	await waitUntil(1000);
	if (live()) { try { process.kill(handle.corePid!, "SIGKILL"); } catch {} }
	await waitUntil(200);
	if (live()) throw new Error("The owned Neovim core did not stop.");
}

export function recoverNativeReview(root: string, branch: any[], sessionId: string, source: NativeReviewSource): { handle: NativeReviewHandle; snapshot?: NativeReviewSnapshot; error?: string } | undefined {
	for (let index = branch.length - 1; index >= 0; index--) {
		const entry = branch[index];
		const handle = entry?.data;
		if (entry?.type !== "custom" || entry.customType !== nativeReviewEvent || !validHandle(handle)
			|| handle.sessionId !== sessionId || handle.sourceEntryId !== source.entryId || handle.sourceHash !== source.hash) continue;
		if (handle.status === "closed") return undefined;
		if (nativeRunIsLive(handle)) throw new Error("Close the earlier Neovim review before opening another view of this response.");
		let snapshot: NativeReviewSnapshot | undefined;
		try { snapshot = readNativeReviewSnapshot(root, handle, source); }
		catch { return { handle, error: "The interrupted Neovim snapshot could not be read. The saved Pi draft is still available." }; }
		if (snapshot?.corePid && nativeRunIsLive({ ...handle, corePid: snapshot.corePid })) throw new Error("Close the earlier Neovim review before opening another view of this response.");
		return { handle, snapshot };
	}
	return undefined;
}

export function closeNativeReview(root: string, handle: NativeReviewHandle, append: (handle: NativeReviewHandle) => void, keepFiles = false) {
	// Persist acknowledgement first. Do not remove the recovery copy if Pi fails
	// to record it. Invalid snapshots remain available for manual inspection.
	append({ ...handle, status: "closed" });
	if (!keepFiles) rmSync(nativeReviewDirectory(root, handle), { recursive: true, force: true });
}

export function nativeReviewEnvironment(base: NodeJS.ProcessEnv, directory: string, runtime: string, runId: string): NodeJS.ProcessEnv {
	const env: NodeJS.ProcessEnv = {};
	for (const [key, value] of Object.entries(base)) {
		if (["PATH", "TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "LANG", "TZ"].includes(key) || key.startsWith("LC_")) env[key] = value;
	}
	for (const name of ["home", "config", "data", "state", "cache", "runtime", "tmp", "work"]) mkdirSync(join(directory, name), { recursive: true, mode: 0o700 });
	return {
		...env, HOME: join(directory, "home"), XDG_CONFIG_HOME: join(directory, "config"), XDG_CONFIG_DIRS: join(directory, "config"),
		XDG_DATA_HOME: join(directory, "data"), XDG_DATA_DIRS: join(directory, "data"), XDG_STATE_HOME: join(directory, "state"),
		XDG_CACHE_HOME: join(directory, "cache"), XDG_RUNTIME_DIR: join(directory, "runtime"), TMPDIR: join(directory, "tmp"),
		NVIM_APPNAME: "galpon-review", NVIM_LOG_FILE: join(directory, "nvim.log"),
		GALPON_REVIEW_NVIM_RUN_ID: runId, GALPON_REVIEW_NVIM_RUNTIME: runtime,
		GALPON_REVIEW_NVIM_INPUT: join(directory, "input.json"), GALPON_REVIEW_NVIM_OUTPUT: join(directory, "snapshot.json"),
	};
}

type NativeReviewOptions = {
	ctx: any;
	source: NativeReviewSource;
	sessionId: string;
	runtime: NativeReviewRuntime;
	root: string;
	entryPoint: string;
	items: ReviewItem[];
	editing?: ReviewEditingDraft;
	onHandle: (handle: NativeReviewHandle) => void;
	onSnapshot: (snapshot: NativeReviewSnapshot) => void;
	onStop?: (stop: (() => Promise<void>) | undefined) => void;
	// Dependency injection is local to this helper; no model-facing switch.
	spawn?: typeof spawnProcess;
	terminal?: { input: Pick<NodeJS.ReadStream, "isTTY">; output: Pick<NodeJS.WriteStream, "isTTY" | "write"> };
};

export async function runNativeReview(options: NativeReviewOptions): Promise<{ handle: NativeReviewHandle; snapshot?: NativeReviewSnapshot; error?: string }> {
	const terminal = options.terminal ?? { input: process.stdin, output: process.stdout };
	if (!terminal.input.isTTY || !terminal.output.isTTY) throw new Error("Neovim Review requires a real terminal.");
	if (!isAbsolute(options.entryPoint) || !lstatSync(options.entryPoint).isFile()) throw new Error("The Neovim Review UI is not installed.");
	const handle: NativeReviewHandle = { version: 1, runId: randomUUID(), sessionId: options.sessionId, sourceEntryId: options.source.entryId, sourceHash: options.source.hash, status: "open" };
	const directory = nativeReviewDirectory(options.root, handle);
	let env: NodeJS.ProcessEnv;
	try {
		mkdirSync(directory, { recursive: true, mode: 0o700 });
		env = nativeReviewEnvironment(process.env, directory, options.runtime.runtime, handle.runId);
		writeFileSync(join(directory, "input.json"), JSON.stringify(nativeReviewInput(options.source, handle, options.items, options.editing, options.runtime.palette)), { mode: 0o600 });
		options.onHandle({ ...handle });
	} catch (error) {
		// No child exists yet. The source and initial draft remain in Pi.
		rmSync(directory, { recursive: true, force: true });
		throw error;
	}
	let last: NativeReviewSnapshot | undefined;
	let lastDraft = JSON.stringify({ items: options.items, editing: options.editing });
	const importSnapshot = () => {
		const snapshot = readNativeReviewSnapshot(options.root, handle, options.source);
		if (!snapshot) return undefined;
		if (snapshot.corePid && handle.corePid !== snapshot.corePid) {
			if (handle.corePid) throw new Error("Neovim Review changed its core process identity.");
			handle.corePid = snapshot.corePid;
			options.onHandle({ ...handle });
		}
		if (last && (snapshot.revision < last.revision || (snapshot.revision === last.revision && JSON.stringify(snapshot) !== JSON.stringify(last)))) {
			throw new Error("Neovim Review returned a changed or older snapshot revision.");
		}
		if (last && snapshot.revision === last.revision) return snapshot;
		const draft = JSON.stringify({ items: snapshot.items, editing: snapshot.editing });
		if (draft !== lastDraft) {
			options.onSnapshot(snapshot);
			lastDraft = draft;
		}
		last = snapshot;
		return snapshot;
	};
	const result = await options.ctx.ui.custom<{ error?: string }>((tui: any, _theme: any, _keys: any, done: (value: { error?: string }) => void) => {
		let child: ChildProcess | undefined;
		let timer: NodeJS.Timeout | undefined;
		let ended = false;
		let stopped = false;
		let childFailure: string | undefined;
		let resolveClosed: () => void = () => {};
		const closed = new Promise<void>(resolve => { resolveClosed = resolve; });
		const finish = async (error?: string) => {
			if (ended) return;
			ended = true;
			if (timer) clearInterval(timer);
			try {
				try { importSnapshot(); } catch { /* Final validation follows the core's exit flush. */ }
				await drainNativeCore(handle);
				const final = importSnapshot();
				if (!final) error ??= "Neovim closed without a review snapshot. The saved Pi draft was kept.";
				else if (last && final.revision < last.revision) error ??= "Neovim returned an older draft. The saved Pi draft was kept.";
			} catch { error ??= "The final Neovim snapshot could not be read. The saved Pi draft was kept."; }
			finally {
				try { options.onStop?.(undefined); }
				finally {
					try {
						if (stopped) { tui.start(); tui.requestRender(true); }
					} finally { resolveClosed(); done({ error }); }
				}
			}
		};
		let stopping: Promise<void> | undefined;
		const stopChild = (): Promise<void> => {
			if (ended) return closed;
			return stopping ??= (async () => {
				if (nativePIDState(handle.corePid, handle.runId) === "live") {
					try { process.kill(handle.corePid!, "SIGTERM"); } catch { child?.kill("SIGTERM"); }
				} else child?.kill("SIGTERM");
				const force = setTimeout(() => child?.kill("SIGKILL"), 1500);
				try { await closed; } finally { clearTimeout(force); }
			})();
		};
		try {
			stopped = true;
			tui.stop();
			terminal.output.write("\x1b[2J\x1b[H");
			child = (options.spawn ?? spawnProcess)(options.runtime.neovim, ["-n", "--noplugin", "-i", "NONE", "-u", options.entryPoint], {
				stdio: "inherit", cwd: join(directory, "work"), env,
			});
			child.once("error", () => finish("Neovim could not start. The saved Pi draft was kept."));
			child.once("close", (code, signal) => finish(childFailure ?? (code === 0 && !signal ? undefined : "Neovim stopped before the review was complete. Its last valid draft was kept.")));
			handle.pid = child.pid;
			options.onStop?.(stopChild);
			options.onHandle({ ...handle });
			timer = setInterval(() => { try { importSnapshot(); } catch { /* Keep the last valid Pi draft; final validation reports an error. */ } }, 200);
		} catch {
			childFailure = "Neovim Review could not start. The saved Pi draft was kept.";
			if (child?.pid && !ended) void stopChild();
			else void finish(childFailure);
		}
		return { render: () => [], invalidate: () => {} };
	});
	return { handle, snapshot: last, ...(result?.error ? { error: result.error } : {}) };
}
