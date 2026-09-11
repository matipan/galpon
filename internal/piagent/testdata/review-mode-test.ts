import { writeFileSync } from "node:fs";
import { CURSOR_MARKER, Key, visibleWidth, wrapTextWithAnsi } from "@earendil-works/pi-tui";
import galpon from "../extension.ts";
import {
	ReviewMode,
	compileReview,
	firstReviewMatch,
	parseReviewBlocks,
	renderReviewMode,
	reviewSelection,
	reviewDraftEvent,
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

function prettyMarkdown(value: string, width: number): string[] {
	const pretty = value
		.replace(/^# /, "▣ ")
		.replaceAll("**", "")
		.replace(/^```.*$/gm, "┊ code")
		.replace(/^- /gm, "• ");
	return pretty.split("\n").flatMap(line => wrapTextWithAnsi(line, Math.max(1, width)));
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

async function run() {
	const blocks = parseReviewBlocks(markdown);
	assert(blocks.length === 6, `review block count = ${blocks.length}`);
	assert(blocks[0].text === "# Deployment plan", "heading was not isolated");
	assert(blocks[2].text === "- Deploy the API first.", "first list item was not isolated");
	assert(blocks[3].text === "- Deploy workers after the API is healthy.", "second list item was not isolated");
	assert(blocks[4].text.includes("galpon deploy") && blocks[4].text.endsWith("```"), "code fence was not preserved");
	assert(!blocks[5].text.includes("\u001b"), "terminal control data was not removed");
	const setext = parseReviewBlocks("Rendered title\n=\nBody");
	assert(setext.length === 2 && setext[0].text === "Rendered title\n=" && setext[1].text === "Body", "setext heading was split from its underline or merged with later text");
	assert(sanitizeReviewText("unsafe \u001b[31mred").trim() === "unsafe red", "ANSI text was not sanitized");

	const quote = reviewSelection(blocks, 3, 2);
	assert(quote.includes("Deploy the API") && quote.includes("Deploy workers"), "reverse range did not preserve both blocks");
	const items: ReviewItem[] = [{ id: "private-item-id", start: 2, end: 3, quote, comment: "These deployments must be independent." }];
	const compiled = compileReview(items);
	assert(compiled.includes("> - Deploy the API first."), "compiled review omitted the quote");
	assert(compiled.includes("These deployments must be independent."), "compiled review omitted feedback");
	assert(!compiled.includes("private-item-id"), "compiled review exposed an internal item ID");

	assert(firstReviewMatch(blocks, "workers", 0, 1) === 3, "forward search missed the worker block");
	assert(firstReviewMatch(blocks, "deploy", 4, -1) === 3, "reverse search did not move backward");
	assert(firstReviewMatch(blocks, "not present", 0, 1) === -1, "missing search text produced a match");

	for (const width of [18, 72, 120]) {
		const state: ReviewViewState = { focus: "source", cursor: 2, anchor: 3, itemCursor: 0, query: "deploy" };
		const lines = renderReviewMode(blocks, items, state, width, 12, theme, prettyMarkdown);
		assert(lines.length <= 13, `review body height exceeded its bound at width ${width}`);
		for (const line of lines) assert(visibleWidth(line) <= width, `review width exceeded ${width}: ${line}`);
		const view = lines.join("\n");
		assert(view.includes("RESPONSE"), `response heading missing at width ${width}`);
		assert(!view.includes("\u001b[31m"), `terminal control data entered the view at width ${width}`);
		if (width >= 120) {
			assert(view.includes("ANNOTATIONS"), "wide review did not show the annotations pane");
			assert(view.includes("independent"), "wide review omitted feedback");
		}
	}
	const prettyView = renderReviewMode(blocks, [], { focus: "source", cursor: 0, itemCursor: 0, query: "" }, 80, 12, theme, prettyMarkdown).join("\n");
	assert(prettyView.includes("▣ Deployment plan"), "review did not use the Markdown renderer");

	let dynamicHeight = 16;
	const dynamicState: ReviewViewState = { focus: "source", cursor: 0, itemCursor: 0, query: "" };
	const dynamicMode = new ReviewMode(blocks, items, dynamicState, theme, () => {}, () => {}, () => dynamicHeight, { renderMarkdown: prettyMarkdown });
	const shortView = dynamicMode.render(72);
	dynamicHeight = 30;
	const tallView = dynamicMode.render(72);
	dynamicHeight = 8;
	const tinyView = dynamicMode.render(48);
	assert(shortView.length === 16 && tallView.length === 30 && tinyView.length === 8, `review height did not follow terminal rows: ${shortView.length} -> ${tallView.length} -> ${tinyView.length}`);
	for (const line of tallView) assert(visibleWidth(line) <= 72, "resized review exceeded its width");

	const longText = ["```text", ...Array.from({ length: 24 }, (_value, index) => `long line ${index + 1}`), "```"].join("\n");
	const longBlocks = parseReviewBlocks(longText);
	const longState: ReviewViewState = { focus: "source", cursor: 0, itemCursor: 0, query: "" };
	const longMode = new ReviewMode(longBlocks, [], longState, theme, () => {}, () => {}, 8, { renderMarkdown: prettyMarkdown });
	assert(!longMode.render(60).join("\n").includes("long line 24"), "long block unexpectedly started at its final row");
	for (let index = 0; index < 30; index++) longMode.handleInput("j");
	assert(longState.cursor === 0 && (longState.sourceRowOffset ?? 0) > 0, "j did not move within a long rendered block");
	assert(longMode.render(60).join("\n").includes("long line 24"), "the end of a long rendered block remained inaccessible");

	const longComment = Array.from({ length: 20 }, (_value, index) => `comment line ${index + 1}`).join("\n");
	const longItemState: ReviewViewState = { focus: "items", cursor: 0, itemCursor: 0, query: "" };
	const longItemMode = new ReviewMode(blocks, [{ ...items[0], comment: longComment }], longItemState, theme, () => {}, () => {}, 8, { renderMarkdown: prettyMarkdown });
	assert(!longItemMode.render(60).join("\n").includes("comment line 20"), "long annotation unexpectedly started at its final row");
	for (let index = 0; index < 30; index++) longItemMode.handleInput("j");
	assert((longItemState.itemRowOffset ?? 0) > 0, "j did not move within a long annotation");
	assert(longItemMode.render(60).join("\n").includes("comment line 20"), "the end of a long annotation remained inaccessible");

	let tinyHeight = 3;
	const tinyInputMode = new ReviewMode(blocks, [], { focus: "source", cursor: 0, itemCursor: 0, query: "" }, theme, () => {}, () => {}, () => tinyHeight, {
		renderMarkdown: prettyMarkdown,
		confirmFinish: true,
	});
	tinyInputMode.handleInput("/");
	tinyInputMode.focused = true;
	type(tinyInputMode, "needle");
	assert(tinyInputMode.render(48).join("\n").includes("needle"), "search field disappeared at three terminal rows");
	assert(tinyInputMode.render(48).join("\n").includes(CURSOR_MARKER), "focused search did not render an IME cursor marker");
	tinyHeight = 4;
	assert(tinyInputMode.render(48).join("\n").includes("needle"), "search field disappeared at four terminal rows");
	tinyInputMode.handleInput("\u001b");
	tinyInputMode.handleInput("c");
	type(tinyInputMode, "Visible input.");
	tinyHeight = 3;
	assert(tinyInputMode.render(48).join("\n").includes("Visible input."), "comment field disappeared at three terminal rows");
	assert(tinyInputMode.render(48).join("\n").includes(CURSOR_MARKER), "focused comment editor did not render an IME cursor marker");
	tinyHeight = 4;
	assert(tinyInputMode.render(48).join("\n").includes("Visible input."), "comment field disappeared at four terminal rows");
	tinyHeight = 8;
	tinyInputMode.handleInput("\r");
	tinyInputMode.handleInput("s");
	assert(tinyInputMode.render(48).join("\n").includes("REPLACE UNSENT EDITOR TEXT"), "confirmation disappeared on a short terminal");

	const state: ReviewViewState = { focus: "source", cursor: 0, itemCursor: 0, query: "" };
	let action: any;
	let changed: ReviewItem[] = [];
	let renders = 0;
	let id = 0;
	let mode = new ReviewMode(blocks, [], state, theme, () => { renders++; }, value => { action = value; }, 24, {
		renderMarkdown: prettyMarkdown,
		onItemsChanged: next => { changed = next; },
		makeID: () => `item-${++id}`,
	});
	mode.handleInput("j");
	mode.handleInput("v");
	mode.handleInput("j");
	mode.handleInput("j");
	mode.handleInput("o");
	assert(state.cursor === 1 && state.anchor === 2, `visual selection did not swap ends: ${JSON.stringify(state)}`);
	mode.handleInput("c");
	assert(mode.render(80).join("\n").includes("COMMENT ON 2-3"), "comment editor did not open in place");
	type(mode, "These blocks must stay together.");
	mode.handleInput("\r");
	assert(changed.length === 1 && changed[0].start === 1 && changed[0].end === 2, `visual comment was not saved: ${JSON.stringify(changed)}`);
	assert(mode.render(80).join("\n").includes("Saved annotation 1"), "saved annotation did not update the live view");
	assert(action === undefined, "commenting closed Review Mode");
	assert(renders > 5, "modal interactions did not request renders");

	let renderedSelectedMarkdown = false;
	const selectedMode = new ReviewMode(blocks, changed, { focus: "source", cursor: 2, anchor: 1, itemCursor: 0, query: "" }, theme, () => {}, () => {}, 24, {
		renderMarkdown: (value, width, selected) => {
			if (selected) renderedSelectedMarkdown = true;
			return prettyMarkdown(value, width);
		},
	});
	selectedMode.render(80);
	assert(renderedSelectedMarkdown, "visual selection did not request selected Markdown styling");

	action = undefined;
	const confirmMode = new ReviewMode(blocks, changed, state, theme, () => {}, value => { action = value; }, 24, {
		renderMarkdown: prettyMarkdown,
		confirmFinish: true,
	});
	confirmMode.handleInput("s");
	assert(action === undefined && confirmMode.render(80).join("\n").includes("REPLACE UNSENT EDITOR TEXT"), "prepare confirmation was not shown in place");
	confirmMode.handleInput("n");
	confirmMode.handleInput("s");
	confirmMode.handleInput("y");
	assert(action?.kind === "finish", "prepare confirmation did not finish the review");

	mode = new ReviewMode(blocks, changed, state, theme, () => {}, value => { action = value; }, 24, {
		renderMarkdown: prettyMarkdown,
		onItemsChanged: next => { changed = next; },
	});
	mode.handleInput("/");
	type(mode, "workers");
	mode.handleInput("\r");
	assert(state.query === "workers" && state.cursor === 3, `embedded search did not move to its match: ${JSON.stringify(state)}`);
	mode.handleInput("g");
	mode.handleInput("g");
	assert(state.cursor === 0, "gg did not move to the first response block");
	mode.handleInput("]");
	mode.handleInput("a");
	assert(state.cursor === 2, "]a did not move to the next annotation");

	const unorderedItems: ReviewItem[] = [
		{ id: "later", start: 5, end: 5, quote: blocks[5].text, comment: "Later." },
		{ id: "nearer", start: 2, end: 2, quote: blocks[2].text, comment: "Nearer." },
	];
	const annotationState: ReviewViewState = { focus: "source", cursor: 0, itemCursor: 0, query: "" };
	const annotationMode = new ReviewMode(blocks, unorderedItems, annotationState, theme, () => {}, () => {}, 24, { renderMarkdown: prettyMarkdown });
	annotationMode.handleInput("]");
	annotationMode.handleInput("a");
	assert(annotationState.cursor === 2 && annotationState.itemCursor === 1, "]a did not select the nearest annotation by source position");
	annotationState.cursor = 5;
	annotationMode.handleInput("[");
	annotationMode.handleInput("a");
	assert(annotationState.cursor === 2 && annotationState.itemCursor === 1, "[a did not select the nearest earlier annotation by source position");

	mode.handleInput("l");
	assert(state.focus === "items", "l did not focus annotations");
	mode.handleInput("e");
	assert(mode.render(80).join("\n").includes("EDIT ANNOTATION 1"), "annotation editor did not open in place");
	type(mode, " Updated.");
	mode.handleInput("\r");
	assert(changed[0].comment.endsWith("Updated."), "annotation edit was not saved");
	mode.handleInput("x");
	assert(changed.length === 0 && state.focus === "source", "annotation delete did not update the live view");
	mode.handleInput("u");
	assert(changed.length === 1, "u did not restore the deleted annotation");
	mode.handleInput("l");
	mode.handleInput("d");
	mode.handleInput("d");
	assert(changed.length === 0, "dd did not delete the active annotation");

	const isolatedEditorMode = new ReviewMode(blocks, [], { focus: "source", cursor: 0, itemCursor: 0, query: "" }, theme, () => {}, () => {}, 24, { renderMarkdown: prettyMarkdown });
	isolatedEditorMode.handleInput("c");
	type(isolatedEditorMode, "Annotation A must not leak.");
	isolatedEditorMode.handleInput("\r");
	isolatedEditorMode.handleInput("c");
	type(isolatedEditorMode, "Annotation B.");
	isolatedEditorMode.handleInput(Key.ctrl("z"));
	assert(!isolatedEditorMode.render(80).join("\n").includes("Annotation A must not leak."), "annotation editor undo history leaked from an earlier annotation");
	isolatedEditorMode.handleInput("\u001b");

	const pi = new FakePi();
	pi.entries.push(assistantEntry("assistant-1", markdown));
	galpon(pi as any);
	const review = pi.commands.get("review");
	assert(review?.handler, "the Galpon extension did not register /review");
	const prepared = commandContext(pi, component => {
		component.handleInput("j");
		component.handleInput("v");
		component.handleInput("j");
		component.handleInput("j");
		component.handleInput("c");
		type(component, "These deployments must be independent.");
		component.handleInput("\r");
		component.handleInput("s");
	});
	await review.handler("", prepared.context);
	assert(prepared.getEditorText().includes("These deployments must be independent."), "the command did not prepare feedback in Pi's editor");
	assert(!prepared.getEditorText().includes("\u001b"), "the command put terminal control data in Pi's editor");
	const drafts = pi.entries.filter(entry => entry.customType === reviewDraftEvent);
	assert(drafts.length === 2 && drafts[0].data.status === "open" && drafts[1].data.status === "prepared", "the command did not persist open and prepared draft states");
	assert(drafts.every(entry => entry.data.version === 2 && entry.data.parserVersion === 2 && entry.data.items[0].quoteHash), "the command did not persist parser-bound version 2 drafts");
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
	const resumed = commandContext(resumedPi, component => component.handleInput("q"), component => {
		const view = component.render(120).join("\n");
		restoredCount = view.includes("1 annotation") ? 1 : 0;
	});
	await resumedPi.commands.get("review").handler("", resumed.context);
	assert(restoredCount === 1, "an open session draft was not restored");

	const preparedPi = new FakePi();
	preparedPi.entries = [pi.entries[0], drafts[1]];
	galpon(preparedPi as any);
	let preparedCount = -1;
	const preparedResume = commandContext(preparedPi, component => component.handleInput("q"), component => {
		preparedCount = component.render(120).join("\n").includes("1 annotation") ? 1 : 0;
	});
	await preparedPi.commands.get("review").handler("", preparedResume.context);
	assert(preparedCount === 1, "a prepared draft was not recoverable");

	const interruptedPi = new FakePi();
	interruptedPi.entries.push(assistantEntry("assistant-interrupted", markdown));
	galpon(interruptedPi as any);
	const interrupted = commandContext(interruptedPi, component => {
		component.handleInput("c");
		type(component, "Interrupted annotation buffer.");
		component.dispose();
	});
	await interruptedPi.commands.get("review").handler("", interrupted.context);
	const interruptedDraft = interruptedPi.entries.findLast(entry => entry.customType === reviewDraftEvent);
	assert(interruptedDraft?.data.editing?.buffer === "Interrupted annotation buffer.", "disposing Review Mode did not flush the active annotation buffer");
	galpon(interruptedPi as any);
	let restoredEditing = false;
	const interruptedResume = commandContext(interruptedPi, component => {
		restoredEditing = component.render(100).join("\n").includes("Interrupted annotation buffer.");
		component.handleInput("\u001b");
		component.handleInput("q");
	});
	await interruptedPi.commands.get("review").handler("", interruptedResume.context);
	assert(restoredEditing, "an interrupted annotation buffer was not restored");

	const v1Pi = new FakePi();
	const legacy = structuredClone(drafts[0]);
	legacy.data.version = 1;
	delete legacy.data.parserVersion;
	delete legacy.data.sourceBytes;
	delete legacy.data.items[0].quoteHash;
	v1Pi.entries = [pi.entries[0], legacy];
	galpon(v1Pi as any);
	let legacyCount = -1;
	const legacyResume = commandContext(v1Pi, component => component.handleInput("q"), component => {
		legacyCount = component.render(120).join("\n").includes("1 annotation") ? 1 : 0;
	});
	await v1Pi.commands.get("review").handler("", legacyResume.context);
	assert(legacyCount === 1, "a valid version 1 draft was not migrated");

	const changedParserPi = new FakePi();
	const changedParser = structuredClone(drafts[0]);
	changedParser.data.items[0].quoteHash = "0".repeat(64);
	changedParserPi.entries = [pi.entries[0], changedParser];
	galpon(changedParserPi as any);
	let changedParserCount = -1;
	const changedParserResume = commandContext(changedParserPi, component => component.handleInput("q"), component => {
		changedParserCount = component.render(120).join("\n").includes("0 annotations") ? 0 : 1;
	});
	await changedParserPi.commands.get("review").handler("", changedParserResume.context);
	assert(changedParserCount === 0, "a draft with changed block hashes was restored");

	const damagedPi = new FakePi();
	const damaged = structuredClone(drafts[0]);
	damaged.data.items[0].end = 999;
	damaged.data.items[0].comment = "unsafe \u001b[31mfeedback";
	damagedPi.entries = [pi.entries[0], drafts[0], damaged];
	galpon(damagedPi as any);
	let damagedCount = -1;
	const damagedContext = commandContext(damagedPi, component => component.handleInput("q"), component => {
		damagedCount = component.render(120).join("\n").includes("1 annotation") ? 1 : 0;
	});
	await damagedPi.commands.get("review").handler("", damagedContext.context);
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
