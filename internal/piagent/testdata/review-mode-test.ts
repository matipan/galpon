import { writeFileSync } from "node:fs";
import { visibleWidth } from "@earendil-works/pi-tui";
import galpon from "../extension.ts";
import {
	ReviewMode,
	compileReview,
	firstReviewMatch,
	parseReviewBlocks,
	renderReviewMode,
	reviewSelection,
	reviewDraftEvent,
	type ReviewAction,
	type ReviewItem,
	type ReviewViewState,
} from "../galpon-review.ts";

const markdown = `# Deployment plan

Use the database as the source of truth.\nKeep transitions transactional.

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

function commandContext(pi: FakePi, actions: ReviewAction[], onComponent?: (component: ReviewMode) => void) {
	let editorText = "";
	return {
		context: {
			mode: "tui",
			waitForIdle: async () => {},
			sessionManager: { getBranch: () => pi.entries },
			ui: {
				notify: () => {},
				select: async (_title: string, choices: string[]) => choices[0],
				custom: async (factory: any) => {
					const component = factory({ requestRender: () => {} }, theme, {}, () => {});
					onComponent?.(component);
					return actions.shift();
				},
				input: async () => undefined,
				editor: async () => "These deployments \u001b[31mmust be independent.",
				getEditorText: () => "",
				setEditorText: (value: string) => { editorText = value; },
				confirm: async () => true,
			},
		},
		getEditorText: () => editorText,
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
		const lines = renderReviewMode(blocks, items, state, width, 12, theme);
		assert(lines.length <= 19, `review height exceeded its bound at width ${width}`);
		for (const line of lines) assert(visibleWidth(line) <= width, `review width exceeded ${width}: ${line}`);
		const view = lines.join("\n");
		assert(view.includes("Review"), `review header missing at width ${width}`);
		assert(!view.includes("\u001b[31m"), `terminal control data entered the view at width ${width}`);
		if (width >= 120) {
			assert(view.includes("SOURCE MESSAGE") && view.includes("REVIEW ITEMS"), "wide review did not show both panes");
			assert(view.includes("independent"), "wide review omitted feedback");
		}
	}

	const state: ReviewViewState = { focus: "source", cursor: 0, itemCursor: 0, query: "workers" };
	let action: ReviewAction | undefined;
	let renders = 0;
	let mode = new ReviewMode(blocks, items, state, theme, () => { renders++; }, value => { action = value; }, 10);
	mode.handleInput("j");
	mode.handleInput("v");
	mode.handleInput("j");
	mode.handleInput("v");
	mode.handleInput("c");
	assert(action?.kind === "comment" && action.start === 1 && action.end === 2, `range comment action = ${JSON.stringify(action)}`);
	assert(renders === 4, `navigation render count = ${renders}`);

	action = undefined;
	mode = new ReviewMode(blocks, items, state, theme, () => {}, value => { action = value; }, 10);
	mode.handleInput("\t");
	assert(state.cursor === 3 && state.anchor === 2, `feedback item did not reveal source range: ${JSON.stringify(state)}`);
	mode.handleInput("\r");
	assert(action?.kind === "edit" && action.item === 0, `item edit action = ${JSON.stringify(action)}`);

	action = undefined;
	mode = new ReviewMode(blocks, items, state, theme, () => {}, value => { action = value; }, 10);
	mode.handleInput("x");
	assert(action?.kind === "delete" && action.item === 0, `item delete action = ${JSON.stringify(action)}`);

	const navigationItems = [items[0], { id: "second", start: 4, end: 4, quote: blocks[4].text, comment: "Check this command." }];
	state.focus = "items";
	state.itemCursor = 0;
	mode = new ReviewMode(blocks, navigationItems, state, theme, () => {}, () => {}, 10);
	mode.handleInput("G");
	assert(state.itemCursor === 1 && state.cursor === 4, `G did not reveal the last feedback item: ${JSON.stringify(state)}`);
	mode.handleInput("g");
	assert(state.itemCursor === 0 && state.cursor === 3 && state.anchor === 2, `g did not reveal the first feedback item: ${JSON.stringify(state)}`);

	action = undefined;
	state.focus = "source";
	mode = new ReviewMode(blocks, items, state, theme, () => {}, value => { action = value; }, 10);
	mode.handleInput("s");
	assert(action?.kind === "finish", `finish action = ${JSON.stringify(action)}`);

	const pi = new FakePi();
	pi.entries.push(assistantEntry("assistant-1", markdown));
	galpon(pi as any);
	const review = pi.commands.get("review");
	assert(review?.handler, "the Galpon extension did not register /review");
	const prepared = commandContext(pi, [{ kind: "comment", start: 2, end: 3 }, { kind: "finish" }]);
	await review.handler("", prepared.context);
	assert(prepared.getEditorText().includes("These deployments must be independent."), "the command did not prepare feedback in Pi's editor");
	assert(!prepared.getEditorText().includes("\u001b"), "the command put terminal control data in Pi's editor");
	const drafts = pi.entries.filter(entry => entry.customType === reviewDraftEvent);
	assert(drafts.length === 2 && drafts[0].data.status === "open" && drafts[1].data.status === "prepared", "the command did not persist open and prepared draft states");

	const resumedPi = new FakePi();
	resumedPi.entries = [pi.entries[0], drafts[0]];
	galpon(resumedPi as any);
	let restoredCount = -1;
	const resumed = commandContext(resumedPi, [{ kind: "cancel" }], component => {
		const view = component.render(120).join("\n");
		restoredCount = view.includes("1 feedback item") ? 1 : 0;
	});
	await resumedPi.commands.get("review").handler("", resumed.context);
	assert(restoredCount === 1, "an open session draft was not restored");

	const damagedPi = new FakePi();
	const damaged = structuredClone(drafts[0]);
	damaged.data.items[0].end = 999;
	damaged.data.items[0].comment = "unsafe \u001b[31mfeedback";
	damagedPi.entries = [pi.entries[0], damaged];
	galpon(damagedPi as any);
	let damagedCount = -1;
	const damagedContext = commandContext(damagedPi, [{ kind: "cancel" }], component => {
		damagedCount = component.render(120).join("\n").includes("0 feedback items") ? 0 : 1;
	});
	await damagedPi.commands.get("review").handler("", damagedContext.context);
	assert(damagedCount === 0, "an invalid persisted draft was restored");
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
