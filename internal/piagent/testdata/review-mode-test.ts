import { createHash } from "node:crypto";
import { writeFileSync } from "node:fs";
import { CURSOR_MARKER, Key, visibleWidth } from "@earendil-works/pi-tui";
import galpon from "../extension.ts";
import {
	ReviewMode,
	compileReview,
	firstReviewMatch,
	legacyReviewSelection,
	parseReviewBlocks,
	parseReviewBuffer,
	renderReviewMode,
	reviewDraftEvent,
	reviewParserVersion,
	reviewSelection,
	sanitizeReviewText,
	type ReviewItem,
	type ReviewViewState,
} from "../galpon-review.ts";

const markdown = `# Deployment plan

Use the database as the **source of truth**.\nKeep transitions transactional.

- Deploy the API first.
- Deploy workers after the API is healthy.

\`\`\`sh
galpon deploy
\`\`\`

Final paragraph with a dangerous terminal escape: \u001b[31mred.`;

const theme = {
	fg: (_color: string, text: string) => text,
	bg: (_color: string, text: string) => text,
	bold: (text: string) => text,
};

function assert(value: unknown, message: string): asserts value {
	if (!value) throw new Error(message);
}

function type(component: ReviewMode, value: string) {
	for (const character of value) component.handleInput(character);
}

class FakePi {
	commands = new Map<string, any>();
	entries: any[] = [];
	events = { on: () => () => {}, emit: () => {} };
	on() {}
	registerTool() {}
	registerCommand(name: string, spec: any) { this.commands.set(name, spec); }
	appendEntry(customType: string, data: any) {
		this.entries.push({ type: "custom", id: `custom-${this.entries.length}`, customType, data });
	}
	sendUserMessage() {}
	sendMessage() {}
	setSessionName() {}
}

function assistantEntry(id: string, text: string) {
	return {
		type: "message",
		id,
		timestamp: new Date().toISOString(),
		message: { role: "assistant", content: [{ type: "text", text }], stopReason: "stop", timestamp: Date.now() },
	};
}

function commandContext(
	pi: FakePi,
	interact: (component: ReviewMode) => void,
	onComponent?: (component: ReviewMode) => void,
	initialEditorText = "",
) {
	let editorText = initialEditorText;
	let customOptions: any;
	return {
		context: {
			mode: "tui",
			waitForIdle: async () => {},
			sessionManager: { getBranch: () => pi.entries },
			ui: {
				notify: () => {},
				select: async (_title: string, choices: string[]) => choices[0],
				custom: async (factory: any, options: any) => {
					customOptions = options;
					let result: any;
					const component = factory({ requestRender: () => {}, terminal: { rows: 32, columns: 120 } }, theme, {}, (value: any) => { result = value; });
					component.focused = true;
					onComponent?.(component);
					interact(component);
					return result;
				},
				getEditorText: () => editorText,
				setEditorText: (value: string) => { editorText = value; },
				confirm: async () => true,
			},
		},
		getEditorText: () => editorText,
		getCustomOptions: () => customOptions,
	};
}

function hash(value: string): string {
	return createHash("sha256").update(value).digest("hex");
}

async function run() {
	const legacyBlocks = parseReviewBlocks(markdown);
	assert(legacyBlocks.length === 6, `legacy review block count = ${legacyBlocks.length}`);
	assert(legacyBlocks[0].text === "# Deployment plan", "legacy heading was not isolated");
	assert(legacyBlocks[4].text.includes("galpon deploy") && legacyBlocks[4].text.endsWith("```"), "legacy code fence was not preserved");
	const setext = parseReviewBlocks("Rendered title\n=\nBody");
	assert(setext.length === 2 && setext[0].text === "Rendered title\n=" && setext[1].text === "Body", "setext heading was split or merged");
	assert(sanitizeReviewText("unsafe \u001b[31mred").trim() === "unsafe red", "ANSI text was not sanitized");

	const lines = parseReviewBuffer(markdown);
	assert(lines.length === 13, `review buffer line count = ${lines.length}`);
	assert(lines[0].text === "# Deployment plan" && lines[1].text === "", "buffer did not preserve source lines");
	assert(lines[5].text === "- Deploy the API first." && lines[6].text.includes("workers"), "buffer changed list source text");
	assert(lines[8].kind === "fence" && lines[9].kind === "code", "buffer did not classify fenced source");
	assert(!lines[12].text.includes("\u001b"), "terminal control data entered the buffer");

	const quote = reviewSelection(lines, 5, 6, 0, lines[6].text.length);
	assert(quote === "- Deploy the API first.\n- Deploy workers after the API is healthy.", "line selection did not preserve exact source text");
	const items: ReviewItem[] = [{ id: "private-item-id", start: 5, end: 6, startColumn: 0, endColumn: lines[6].text.length, quote, comment: "These deployments must be independent." }];
	const compiled = compileReview(items);
	assert(compiled.includes("> - Deploy the API first."), "compiled review omitted the quote");
	assert(compiled.includes("These deployments must be independent."), "compiled review omitted feedback");
	assert(!compiled.includes("private-item-id"), "compiled review exposed an internal item ID");
	const whitespaceQuote = "  exact text  \n";
	const whitespaceReview = compileReview([{ id: "whitespace", start: 0, end: 1, startColumn: 0, endColumn: 0, quote: whitespaceQuote, comment: "Keep whitespace." }]);
	assert(whitespaceReview.includes(">   exact text  \n>\n\nKeep whitespace."), "compiled review changed selected whitespace");
	assert(firstReviewMatch(lines, "workers", 0, 1) === 6, "forward search missed the worker line");
	assert(firstReviewMatch(lines, "deploy", 8, -1) === 6, "reverse search did not move backward");

	for (const width of [18, 72, 120]) {
		const state: ReviewViewState = { focus: "source", cursor: 5, cursorColumn: 0, anchor: 6, anchorColumn: 0, visualMode: "line", itemCursor: 0, query: "deploy" };
		const rendered = renderReviewMode(lines, items, state, width, 12, theme);
		assert(rendered.length <= 13, `review body height exceeded its bound at width ${width}`);
		for (const line of rendered) assert(visibleWidth(line) <= width, `review width exceeded ${width}: ${line}`);
		const view = rendered.join("\n");
		assert(view.includes("RESPONSE"), `response heading missing at width ${width}`);
		assert(!/^\s*\d+[●◆]\s/m.test(view), "numbered block gutter remained visible");
		if (width >= 120) assert(view.includes("ANNOTATIONS") && view.includes("independent"), "wide annotation pane was incomplete");
	}
	const sourceView = renderReviewMode(lines, [], { focus: "source", cursor: 0, cursorColumn: 0, itemCursor: 0, query: "" }, 80, 12, theme).join("\n");
	assert(sourceView.includes("# Deployment plan"), "review did not show the original Markdown source");
	assert(!sourceView.includes("▣ Deployment plan"), "review transformed the source into rendered Markdown");

	let dynamicHeight = 16;
	const dynamicMode = new ReviewMode(lines, items, { focus: "source", cursor: 0, cursorColumn: 0, itemCursor: 0, query: "" }, theme, () => {}, () => {}, () => dynamicHeight);
	const shortView = dynamicMode.render(72);
	dynamicHeight = 30;
	const tallView = dynamicMode.render(72);
	dynamicHeight = 8;
	const tinyView = dynamicMode.render(48);
	assert(shortView.length === 16 && tallView.length === 30 && tinyView.length === 8, "review height did not follow terminal rows");
	for (const line of tallView) assert(visibleWidth(line) <= 72, "resized review exceeded its width");

	const navigationState: ReviewViewState = { focus: "source", cursor: 5, cursorColumn: 0, itemCursor: 0, query: "" };
	const navigation = new ReviewMode(lines, [], navigationState, theme, () => {}, () => {}, 16);
	navigation.handleInput("l");
	navigation.handleInput("l");
	assert(navigationState.cursorColumn === 2, "l did not move the character cursor");
	navigation.handleInput("j");
	assert(navigationState.cursor === 6 && navigationState.cursorColumn === 2, "j did not preserve the character column");
	navigation.handleInput("h");
	assert(navigationState.cursorColumn === 1, "h did not move the character cursor left");
	const unicodeLines = parseReviewBuffer("A👨‍👩‍👧‍👦B");
	const unicodeState: ReviewViewState = { focus: "source", cursor: 0, cursorColumn: 0, itemCursor: 0, query: "" };
	const unicodeMode = new ReviewMode(unicodeLines, [], unicodeState, theme, () => {}, () => {}, 8);
	unicodeMode.handleInput("l");
	assert(unicodeState.cursorColumn === 1, "l did not reach the next grapheme");
	unicodeMode.handleInput("l");
	assert(unicodeLines[0].text.slice(unicodeState.cursorColumn) === "B", `l split a multi-code-point grapheme: ${JSON.stringify(unicodeState)}`);

	let charItems: ReviewItem[] = [];
	const charState: ReviewViewState = { focus: "source", cursor: 5, cursorColumn: 2, itemCursor: 0, query: "" };
	const charMode = new ReviewMode(lines, [], charState, theme, () => {}, () => {}, 24, {
		onItemsChanged: next => { charItems = next; },
		makeID: () => "char-item",
	});
	charMode.handleInput("v");
	for (let index = 0; index < 5; index++) charMode.handleInput("l");
	charMode.handleInput("c");
	type(charMode, "Use this exact word.");
	charMode.handleInput("\r");
	assert(charItems[0]?.quote === "Deploy" && charItems[0].startColumn === 2 && charItems[0].endColumn === 8, `v did not save an exact character range: ${JSON.stringify(charItems)}`);

	let lineItems: ReviewItem[] = [];
	const lineState: ReviewViewState = { focus: "source", cursor: 5, cursorColumn: 7, itemCursor: 0, query: "" };
	const lineMode = new ReviewMode(lines, [], lineState, theme, () => {}, () => {}, 24, {
		onItemsChanged: next => { lineItems = next; },
		makeID: () => "line-item",
	});
	lineMode.handleInput("V");
	lineMode.handleInput("j");
	lineMode.handleInput("c");
	type(lineMode, "Keep these lines together.");
	lineMode.handleInput("\r");
	assert(lineItems[0]?.quote === quote && lineItems[0].startColumn === 0 && lineItems[0].endColumn === lines[6].text.length, "V did not save complete logical lines");

	const horizontalText = `${"x".repeat(100)} END`;
	const horizontalLines = parseReviewBuffer(horizontalText);
	const horizontalState: ReviewViewState = { focus: "source", cursor: 0, cursorColumn: 0, itemCursor: 0, query: "" };
	const horizontalMode = new ReviewMode(horizontalLines, [], horizontalState, theme, () => {}, () => {}, 8);
	horizontalMode.handleInput("$");
	const horizontalView = horizontalMode.render(30).join("\n");
	assert((horizontalState.sourceLeftColumn ?? 0) > 0 && horizontalView.includes("END"), "long source lines did not scroll horizontally");

	const longText = ["```text", ...Array.from({ length: 24 }, (_value, index) => `long line ${index + 1}`), "```"].join("\n");
	const longLines = parseReviewBuffer(longText);
	const longState: ReviewViewState = { focus: "source", cursor: 0, cursorColumn: 0, itemCursor: 0, query: "" };
	const longMode = new ReviewMode(longLines, [], longState, theme, () => {}, () => {}, 8);
	for (let index = 0; index < 24; index++) longMode.handleInput("j");
	assert(longState.cursor === 24 && longMode.render(60).join("\n").includes("long line 24"), "the end of a long source buffer remained inaccessible");

	const longComment = Array.from({ length: 20 }, (_value, index) => `comment line ${index + 1}`).join("\n");
	const longItemState: ReviewViewState = { focus: "items", cursor: 0, cursorColumn: 0, itemCursor: 0, query: "" };
	const longItemMode = new ReviewMode(lines, [{ ...items[0], comment: longComment }], longItemState, theme, () => {}, () => {}, 8);
	for (let index = 0; index < 30; index++) longItemMode.handleInput("j");
	assert((longItemState.itemRowOffset ?? 0) > 0 && longItemMode.render(60).join("\n").includes("comment line 20"), "the end of a long annotation remained inaccessible");

	let tinyHeight = 3;
	const tinyInputMode = new ReviewMode(lines, [], { focus: "source", cursor: 0, cursorColumn: 0, itemCursor: 0, query: "" }, theme, () => {}, () => {}, () => tinyHeight, { confirmFinish: true });
	tinyInputMode.handleInput("/");
	tinyInputMode.focused = true;
	type(tinyInputMode, "needle");
	assert(tinyInputMode.render(48).join("\n").includes("needle") && tinyInputMode.render(48).join("\n").includes(CURSOR_MARKER), "search input disappeared at three rows");
	tinyHeight = 4;
	assert(tinyInputMode.render(48).join("\n").includes("needle"), "search input disappeared at four rows");
	tinyInputMode.handleInput("\u001b");
	tinyInputMode.handleInput("c");
	type(tinyInputMode, "Visible input.");
	tinyHeight = 3;
	assert(tinyInputMode.render(48).join("\n").includes("Visible input.") && tinyInputMode.render(48).join("\n").includes(CURSOR_MARKER), "comment input disappeared at three rows");
	tinyHeight = 4;
	assert(tinyInputMode.render(48).join("\n").includes("Visible input."), "comment input disappeared at four rows");

	let action: any;
	let changed = lineItems;
	const interactionState: ReviewViewState = { focus: "source", cursor: 0, cursorColumn: 0, itemCursor: 0, query: "" };
	let mode = new ReviewMode(lines, changed, interactionState, theme, () => {}, value => { action = value; }, 24, {
		onItemsChanged: next => { changed = next; },
	});
	mode.handleInput("/");
	type(mode, "workers");
	mode.handleInput("\r");
	assert(interactionState.cursor === 6 && interactionState.cursorColumn === 9, `search did not move to the exact match: ${JSON.stringify(interactionState)}`);
	mode.handleInput("g");
	mode.handleInput("g");
	assert(interactionState.cursor === 0 && interactionState.cursorColumn === 0, "gg did not move to the buffer start");
	mode.handleInput("]");
	mode.handleInput("a");
	assert(interactionState.cursor === 5, "]a did not move to the annotation source");
	mode.handleInput("\t");
	assert(interactionState.focus === "items", "Tab did not focus annotations");
	mode.handleInput("e");
	type(mode, " Updated.");
	mode.handleInput("\r");
	assert(changed[0].comment.endsWith("Updated."), "annotation edit was not saved");
	mode.handleInput("x");
	assert(changed.length === 0 && interactionState.focus === "source", "annotation delete did not update the view");
	mode.handleInput("u");
	assert(changed.length === 1, "u did not restore the deleted annotation");

	const isolatedEditorMode = new ReviewMode(lines, [], { focus: "source", cursor: 5, cursorColumn: 0, itemCursor: 0, query: "" }, theme, () => {}, () => {}, 24);
	isolatedEditorMode.handleInput("c");
	type(isolatedEditorMode, "Annotation A must not leak.");
	isolatedEditorMode.handleInput("\r");
	isolatedEditorMode.handleInput("c");
	type(isolatedEditorMode, "Annotation B.");
	isolatedEditorMode.handleInput(Key.ctrl("z"));
	assert(!isolatedEditorMode.render(80).join("\n").includes("Annotation A must not leak."), "annotation editor undo history leaked");
	isolatedEditorMode.handleInput("\u001b");

	const pi = new FakePi();
	pi.entries.push(assistantEntry("assistant-1", markdown));
	galpon(pi as any);
	const review = pi.commands.get("review");
	assert(review?.handler, "the extension did not register /review");
	const prepared = commandContext(pi, component => {
		for (let index = 0; index < 5; index++) component.handleInput("j");
		component.handleInput("V");
		component.handleInput("j");
		component.handleInput("c");
		type(component, "These deployments must be independent.");
		component.handleInput("\r");
		component.handleInput("s");
	});
	await review.handler("", prepared.context);
	assert(prepared.getEditorText().includes("These deployments must be independent."), "the command did not prepare feedback in Pi's editor");
	const drafts = pi.entries.filter(entry => entry.customType === reviewDraftEvent);
	assert(drafts.length === 2 && drafts[0].data.status === "open" && drafts[1].data.status === "prepared", "the command did not persist draft states");
	assert(drafts.every(entry => entry.data.version === 2 && entry.data.parserVersion === reviewParserVersion && entry.data.items[0].quoteHash), "the command did not persist parser-bound buffer drafts");
	assert(prepared.getCustomOptions()?.overlay === true && prepared.getCustomOptions()?.overlayOptions?.width === "100%", "Review Mode did not use a full-terminal overlay");

	const unsentPi = new FakePi();
	unsentPi.entries.push(assistantEntry("assistant-unsent", markdown));
	galpon(unsentPi as any);
	let sawInlineConfirmation = false;
	const unsent = commandContext(unsentPi, component => {
		component.handleInput("c");
		type(component, "Keep this comment.");
		component.handleInput("\r");
		component.handleInput("s");
		sawInlineConfirmation = component.render(100).join("\n").includes("REPLACE UNSENT EDITOR TEXT");
		component.handleInput("n");
		component.handleInput("q");
	}, undefined, "Keep my unsent editor text.");
	await unsentPi.commands.get("review").handler("", unsent.context);
	assert(sawInlineConfirmation && unsent.getEditorText() === "Keep my unsent editor text.", "inline confirmation did not protect unsent editor text");

	const resumedPi = new FakePi();
	resumedPi.entries = [pi.entries[0], drafts[0]];
	galpon(resumedPi as any);
	let restoredCount = -1;
	await resumedPi.commands.get("review").handler("", commandContext(resumedPi, component => component.handleInput("q"), component => {
		restoredCount = component.render(120).join("\n").includes("1 annotation") ? 1 : 0;
	}).context);
	assert(restoredCount === 1, "an open buffer draft was not restored");

	const interruptedPi = new FakePi();
	interruptedPi.entries.push(assistantEntry("assistant-interrupted", markdown));
	galpon(interruptedPi as any);
	await interruptedPi.commands.get("review").handler("", commandContext(interruptedPi, component => {
		component.handleInput("c");
		type(component, "Interrupted annotation buffer.");
		component.dispose();
	}).context);
	const interruptedDraft = interruptedPi.entries.findLast(entry => entry.customType === reviewDraftEvent);
	assert(interruptedDraft?.data.editing?.buffer === "Interrupted annotation buffer.", "disposing Review Mode did not flush the active annotation buffer");
	galpon(interruptedPi as any);
	let restoredEditing = false;
	await interruptedPi.commands.get("review").handler("", commandContext(interruptedPi, component => {
		restoredEditing = component.render(100).join("\n").includes("Interrupted annotation buffer.");
		component.handleInput("\u001b");
		component.handleInput("q");
	}).context);
	assert(restoredEditing, "an interrupted annotation buffer was not restored");

	const oldQuote = legacyReviewSelection(legacyBlocks, 2, 3);
	const legacyV2 = {
		type: "custom",
		id: "legacy-v2",
		customType: reviewDraftEvent,
		data: {
			version: 2,
			parserVersion: 2,
			sourceEntryId: "assistant-1",
			sourceHash: hash(markdown),
			sourceBytes: Buffer.byteLength(markdown),
			status: "open",
			items: [{ id: "legacy-item", start: 2, end: 3, quote: oldQuote, quoteHash: hash(oldQuote), comment: "Legacy comment." }],
			updatedAt: Date.now(),
		},
	};
	const legacyPi = new FakePi();
	legacyPi.entries = [assistantEntry("assistant-1", markdown), legacyV2];
	galpon(legacyPi as any);
	let legacyCount = -1;
	await legacyPi.commands.get("review").handler("", commandContext(legacyPi, component => component.handleInput("q"), component => {
		legacyCount = component.render(120).join("\n").includes("1 annotation") ? 1 : 0;
	}).context);
	assert(legacyCount === 1, "a parser version 2 block draft was not migrated to buffer positions");

	const legacyV1 = structuredClone(legacyV2);
	legacyV1.data.version = 1;
	delete legacyV1.data.parserVersion;
	delete legacyV1.data.sourceBytes;
	delete legacyV1.data.items[0].quoteHash;
	const legacyV1Pi = new FakePi();
	legacyV1Pi.entries = [assistantEntry("assistant-1", markdown), legacyV1];
	galpon(legacyV1Pi as any);
	let legacyV1Count = -1;
	await legacyV1Pi.commands.get("review").handler("", commandContext(legacyV1Pi, component => component.handleInput("q"), component => {
		legacyV1Count = component.render(120).join("\n").includes("1 annotation") ? 1 : 0;
	}).context);
	assert(legacyV1Count === 1, "a version 1 block draft was not migrated");

	const damagedPi = new FakePi();
	const damaged = structuredClone(drafts[0]);
	damaged.data.items[0].endColumn = 99999;
	damagedPi.entries = [pi.entries[0], drafts[0], damaged];
	galpon(damagedPi as any);
	let damagedCount = -1;
	await damagedPi.commands.get("review").handler("", commandContext(damagedPi, component => component.handleInput("q"), component => {
		damagedCount = component.render(120).join("\n").includes("1 annotation") ? 1 : 0;
	}).context);
	assert(damagedCount === 1, "a damaged newest draft prevented recovery of an older valid draft");
}

export default async function () {
	const resultPath = process.env.GALPON_REVIEW_MODE_TEST_RESULT;
	try {
		await run();
		if (resultPath) writeFileSync(resultPath, JSON.stringify({ ok: true }), { mode: 0o600 });
	} catch (error) {
		const message = error instanceof Error ? error.message : String(error);
		if (resultPath) writeFileSync(resultPath, JSON.stringify({ ok: false, error: message }), { mode: 0o600 });
		throw error;
	}
}
