import { createHash, randomUUID } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import galpon from "../extension.ts";
import { nativeReviewDirectory, nativeReviewEvent } from "../galpon-neovim-review.ts";
import { legacyReviewSelection, maxReviewBlocks, maxReviewSourceBytes, parseReviewBlocks, parseReviewBuffer, reviewDraftEvent, reviewParserVersion, reviewSelection } from "../galpon-review.ts";

const hash = (text: string) => createHash("sha256").update(text).digest("hex");
const assert = (value: unknown, message: string) => { if (!value) throw new Error(message); };
const text = "# Plan\n\nKeep **exact** wording.  \nAnother line.\n\nFinal paragraph.";
const source = (id: string, body = text, stopReason = "stop") => ({
	type: "message", id, timestamp: new Date().toISOString(),
	message: { role: "assistant", content: [{ type: "text", text: body }], stopReason, timestamp: Date.now() },
});
const palette = Object.fromEntries(["Background", "Surface", "SurfaceRaised", "Prompt", "Selection", "Border", "Foreground", "Muted", "Comment", "Status", "StatusInk", "Blue", "Cyan", "Purple", "Green", "Orange", "Red", "Yellow", "Teal"].map(key => [key, "#222436"]));
const root = join(dirname(process.env.GALPON_PI_EXTENSION!), "review-runs");

type Options = { args?: string; ready?: boolean; selection?: number; mode?: string; editor?: string };
async function invoke(entries: any[], options: Options = {}) {
	const commands = new Map<string, any>();
	const hooks = new Map<string, any>();
	const notices: string[] = [];
	const sessionId = randomUUID();
	let input: any;
	let inspected = 0;
	let editor = options.editor ?? "Unsent text";
	let choices: string[] = [];
	const pi = {
		events: { on: () => () => {}, emit: () => {} },
		on: (name: string, callback: any) => hooks.set(name, callback),
		registerMessageRenderer: () => {},
		registerTool: () => {}, registerCommand: (name: string, command: any) => commands.set(name, command),
		appendEntry: (customType: string, data: any) => entries.push({ type: "custom", customType, data }),
		exec: async (name: string, args: string[]) => {
			assert(name === "galpon" && args.join(" ") === "review config", "Review must inspect, not install dependencies");
			inspected++;
			return { code: options.ready === false ? 1 : 0, stdout: JSON.stringify({ runtime: "/unused-runtime", neovim: "/unused-nvim", version: "0.11.5", palette }) };
		},
		sendUserMessage: () => { throw new Error("Review must not send a prompt"); },
		sendMessage: () => { throw new Error("Review must not send a message"); },
	};
	galpon(pi as any);
	try {
		await commands.get("review").handler(options.args ?? "", {
			mode: options.mode ?? "tui", waitForIdle: async () => {},
			sessionManager: { getBranch: () => entries, getSessionId: () => sessionId },
			ui: {
				notify: (notice: string) => notices.push(notice),
				select: async (_title: string, labels: string[]) => { choices = labels; return labels[options.selection ?? 0]; },
				getEditorText: () => editor, setEditorText: (value: string) => { editor = value; },
				confirm: async () => { throw new Error("No review was prepared"); },
				custom: async () => {
					// Inspect the real bridge input without calling the factory or spawning an editor.
					const handle = entries.findLast(entry => entry.customType === nativeReviewEvent).data;
					input = JSON.parse(readFileSync(join(nativeReviewDirectory(root, handle), "input.json"), "utf8"));
					return { error: "Test stopped before terminal launch" };
				},
			},
		});
	} finally {
		await hooks.get("session_shutdown")({ reason: "reload" });
	}
	assert(editor === (options.editor ?? "Unsent text"), "command validation or recovery changed unsent text");
	return { input, inspected, choices, notices, entries };
}

async function run() {
	const normal = await invoke([source("latest")]);
	assert(normal.input?.sourceEntryId === "latest" && normal.inspected === 1, "/review did not use native Review");
	for (const args of ["nvim", "nvim pick", "unknown"]) {
		const rejected = await invoke([source("latest")], { args });
		assert(!rejected.input && rejected.inspected === 0 && rejected.notices.includes("Use /review or /review pick."), `unsupported /review ${args} opened an editor`);
	}
	const missing = await invoke([source("latest")], { ready: false, editor: " \n " });
	assert(!missing.input && missing.notices.some(notice => notice.includes("galpon review setup")), "missing setup did not give explicit setup advice");
	const rpc = await invoke([source("latest")], { mode: "rpc" });
	assert(!rpc.input && rpc.inspected === 0, "Review opened without an interactive terminal");
	for (const body of ["x".repeat(maxReviewSourceBytes + 1), Array(maxReviewBlocks + 1).fill("line").join("\n")]) {
		const oversized = await invoke([source("large", body)]);
		assert(!oversized.input && oversized.inspected === 0, "oversized source reached native Review");
	}
	const empty = await invoke([source("aborted", "Partial text", "aborted")]);
	assert(!empty.input && empty.inspected === 0, "Review selected an incomplete response");
	const picked = await invoke([source("earlier", "# Earlier\n\nBody not in the label"), source("latest")], { args: "pick", selection: 1 });
	assert(picked.input?.sourceEntryId === "earlier", "/review pick did not select the earlier source");
	assert(picked.choices.length === 2 && !picked.choices.some(label => label.includes("Body not in the label")), "picker labels exposed response bodies");
	const cancelled = await invoke([source("latest")], { args: "pick", selection: -1 });
	assert(!cancelled.input && cancelled.inspected === 0, "cancelled source selection opened Review");

	const blocks = parseReviewBlocks(text);
	const oldQuote = legacyReviewSelection(blocks, 1, 1);
	const legacy = (version: number) => ({
		type: "custom", customType: reviewDraftEvent,
		data: { version, parserVersion: 2, sourceEntryId: "latest", sourceHash: hash(text), sourceBytes: Buffer.byteLength(text), status: "open",
			items: [{ id: "saved", start: 1, end: 1, quote: oldQuote, quoteHash: hash(oldQuote), comment: "Keep the saved feedback." }] },
	});
	for (const version of [1, 2]) {
		const migrated = await invoke([source("latest"), legacy(version)]);
		const item = migrated.input?.items[0];
		assert(item?.comment === "Keep the saved feedback." && item.quote === "Keep **exact** wording.  \nAnother line.", `v${version} draft lost its exact source or comment: ${JSON.stringify(migrated.input)}; ${migrated.notices.join("; ")}`);
		assert(item.start === 2 && item.end === 3 && item.startColumn === 0 && item.endColumn === "Another line.".length, `v${version} draft did not migrate block positions`);
	}
	const joined = "# Plan\n\nTail\u200d";
	const joinedDraft = { type: "custom", customType: reviewDraftEvent, data: {
		version: 1, sourceEntryId: "joined", sourceHash: hash(joined), status: "open",
		items: [{ id: "joined", start: 1, end: 1, quote: "Tail", comment: "Keep the complete grapheme." }],
	} };
	const joinedResult = await invoke([source("joined", joined), joinedDraft]);
	assert(joinedResult.input?.items[0]?.quote === "Tail\u200d" && joinedResult.input.items[0].endColumn === 5, "legacy migration split a restored joiner grapheme");
	const oldEditing: any = legacy(2);
	oldEditing.data.editing = { kind: "edit", itemId: "saved", start: 1, end: 1, quoteHash: hash(oldQuote), buffer: "Legacy unfinished\ncomment" };
	const migratedEditing = await invoke([source("latest"), oldEditing]);
	assert(migratedEditing.input?.editing?.start === 2 && migratedEditing.input.editing.end === 3
		&& migratedEditing.input.editing.buffer === "Legacy unfinished\ncomment", "legacy unfinished comment lost its source range or text");
	const lines = parseReviewBuffer(text);
	const range = { start: 2, end: 2, startColumn: 5, endColumn: 14 };
	const quote = reviewSelection(lines, range.start, range.end, range.startColumn, range.endColumn);
	const saved = { type: "custom", customType: reviewDraftEvent, data: {
		version: 2, parserVersion: reviewParserVersion, sourceEntryId: "latest", sourceHash: hash(text), sourceBytes: Buffer.byteLength(text), status: "open",
		items: [{ id: "saved", ...range, quote, quoteHash: hash(quote), comment: "Exact range feedback." }],
		editing: { kind: "edit", itemId: "saved", ...range, quoteHash: hash(quote), buffer: "Unfinished\ncomment" },
	} };
	const resumed = await invoke([source("latest"), saved]);
	assert(resumed.input?.items[0].quote === quote && resumed.input.editing?.buffer === "Unfinished\ncomment", "current draft or unfinished editor was not restored");
	const damaged = structuredClone(saved);
	damaged.data.items[0].quoteHash = "invalid";
	const fallback = await invoke([source("latest"), saved, damaged]);
	assert(fallback.input?.editing?.buffer === "Unfinished\ncomment", "damaged latest draft hid a valid earlier draft");
	const unrelated = structuredClone(saved);
	unrelated.data.sourceHash = "different-source";
	const excluded = await invoke([source("latest"), unrelated]);
	assert(excluded.input?.items.length === 0 && !excluded.input.editing, "draft from a different source was restored");
}

export default async function () {
	const resultPath = process.env.GALPON_REVIEW_COMMAND_TEST_RESULT!;
	const inputTTY = Object.getOwnPropertyDescriptor(process.stdin, "isTTY");
	const outputTTY = Object.getOwnPropertyDescriptor(process.stdout, "isTTY");
	try {
		Object.defineProperty(process.stdin, "isTTY", { value: true, configurable: true });
		Object.defineProperty(process.stdout, "isTTY", { value: true, configurable: true });
		await run();
		writeFileSync(resultPath, JSON.stringify({ ok: true }));
	} catch (error) {
		writeFileSync(resultPath, JSON.stringify({ ok: false, error: error instanceof Error ? error.message : String(error) }));
		throw error;
	} finally {
		if (inputTTY) Object.defineProperty(process.stdin, "isTTY", inputTTY); else delete (process.stdin as any).isTTY;
		if (outputTTY) Object.defineProperty(process.stdout, "isTTY", outputTTY); else delete (process.stdout as any).isTTY;
	}
}
