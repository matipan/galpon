import { visibleWidth } from "@earendil-works/pi-tui";
import { ReviewMode, parseReviewBuffer, type ReviewItem, type ReviewViewState } from "../galpon-review.ts";

const theme = {
	fg: (_color: string, text: string) => text,
	bg: (_color: string, text: string) => text,
	bold: (text: string) => text,
};

function assert(value: unknown, message: string): asserts value {
	if (!value) throw new Error(message);
}

function review(text: string, width = 8, height = 8) {
	const state: ReviewViewState = { focus: "source", cursor: 0, cursorColumn: 0, itemCursor: 0, query: "" };
	let items: ReviewItem[] = [];
	const mode = new ReviewMode(parseReviewBuffer(text), [], state, theme, () => {}, () => {}, height, {
		onItemsChanged: next => { items = next; },
	});
	const render = () => mode.render(width);
	const key = (data: string) => { mode.handleInput(data); return render(); };
	const keys = (data: string) => { for (const character of data) key(character); };
	render();
	return { mode, state, render, key, keys, items: () => items };
}

export function testReviewVimKeys() {
	const words = review("  alpha_beta42... gamma");
	for (const [key, column] of [["w", 2], ["w", 14], ["w", 18], ["w", 22], ["b", 18], ["b", 14], ["b", 2], ["b", 0]] as const) {
		words.key(key);
		assert(words.state.cursor === 0 && words.state.cursorColumn === column, `${key} did not follow Vim word starts at column ${column}`);
	}
	words.state.cursorColumn = 8;
	words.key("b");
	assert(words.state.cursorColumn === 2, "b did not return to the start of the current word");
	words.key("\x1b[119u");
	assert(words.state.cursorColumn === 14, "Kitty-encoded w did not move to the punctuation word");
	words.key("\x1b[98u");
	assert(words.state.cursorColumn === 2, "Kitty-encoded b did not move to the previous word");

	const acrossLines = review("one\n\n  \ntwo\n  ");
	for (const [key, line, column] of [["w", 1, 0], ["w", 3, 0], ["w", 4, 1], ["w", 4, 1], ["b", 3, 0], ["b", 1, 0], ["b", 0, 0]] as const) {
		acrossLines.key(key);
		assert(acrossLines.state.cursor === line && acrossLines.state.cursorColumn === column, `${key} mishandled blank lines or the buffer boundary`);
	}

	const unicodeText = "e\u0301lan_42,👨‍👩‍👧‍👦 中文 next";
	const unicode = review(unicodeText, 5);
	for (const text of [",", "👨‍👩‍👧‍👦", "中文", "next"]) {
		unicode.key("w");
		assert(unicode.state.cursorColumn === unicodeText.indexOf(text), `w split a Unicode word or grapheme before ${text}`);
	}
	for (const text of ["中文", "👨‍👩‍👧‍👦", ",", "e\u0301"]) {
		unicode.key("b");
		assert(unicode.state.cursorColumn === unicodeText.indexOf(text), `b split a Unicode word or grapheme before ${text}`);
	}
	unicode.key("v"); unicode.key("w"); unicode.key("c");
	unicode.keys("Keep this word."); unicode.key("\r");
	assert(unicode.items()[0]?.quote === "e\u0301lan_42,", "word motion changed the exact visual-selection quote");

	const text = Array.from({ length: 10 }, (_, index) => `${index}abcdefg`).join("");
	const aligned = review(text);
	aligned.state.cursorColumn = 42;
	aligned.state.anchor = 0;
	aligned.state.anchorColumn = 10;
	aligned.state.visualMode = "character";
	aligned.render();
	for (const [key, top, position] of [["zz", 24, 2], ["zt", 40, 0], ["zb", 8, 4]] as const) {
		aligned.keys(key);
		assert(aligned.state.cursorColumn === 42 && aligned.state.anchorColumn === 10, `${key} moved the source cursor or selection anchor`);
		assert(aligned.state.sourceTopColumn === top && aligned.render()[2 + position] === "5abcdefg", `${key} did not position the active wrapped row`);
	}
	aligned.key("\x1b[122u"); aligned.key("\x1b[116u");
	assert(aligned.state.sourceTopColumn === 40, "Kitty-encoded zt was not recognized");
	aligned.state.cursorColumn = 79;
	aligned.render();
	aligned.keys("zt");
	assert(aligned.state.sourceTopColumn === 72 && aligned.render()[2] === "9abcdefg", "zt could not place the last row at the top");
	aligned.keys("zz");
	assert(aligned.state.sourceTopColumn === 56 && aligned.render()[4] === "9abcdefg", "zz could not center the last row");
	aligned.keys("zb");
	assert(aligned.state.sourceTopColumn === 40 && aligned.render()[6] === "9abcdefg", "zb could not place the last row at the bottom");

	const scrolled = review(text);
	scrolled.state.cursorColumn = 26;
	scrolled.state.anchor = 0;
	scrolled.state.anchorColumn = 0;
	scrolled.state.visualMode = "character";
	scrolled.render();
	for (let step = 1; step <= 3; step++) {
		const rows = scrolled.key("\x05");
		assert(scrolled.state.sourceTopColumn === step * 8 && scrolled.state.cursorColumn === 26 && rows[2] === `${step}abcdefg`, "Ctrl-e did not scroll one row while preserving visible source text under the cursor");
	}
	scrolled.key("\x05");
	assert(scrolled.state.sourceTopColumn === 32 && scrolled.state.cursorColumn === 34 && scrolled.state.anchorColumn === 0, "Ctrl-e did not keep an offscreen cursor visible with minimal movement");
	for (let step = 0; step < 20; step++) scrolled.key("\x05");
	assert(scrolled.state.sourceTopColumn === 72 && scrolled.state.cursorColumn === 74, "Ctrl-e moved beyond the final source row");
	scrolled.key("\x1b[6~");
	assert(scrolled.state.sourceTopColumn === 72, "PageDown moved the view backwards after explicit end-of-buffer scrolling");

	const logical = review(Array.from({ length: 12 }, (_, index) => `row ${index}`).join("\n"), 72);
	logical.state.cursor = 6;
	logical.state.cursorColumn = 2;
	logical.render();
	for (const [key, top] of [["zt", 6], ["zz", 4], ["zb", 2]] as const) {
		logical.keys(key);
		assert(logical.state.sourceTopLine === top && logical.state.cursor === 6 && logical.state.cursorColumn === 2, `${key} did not align ordinary logical lines`);
	}
	logical.state.cursor = 0;
	logical.keys("zb");
	assert(logical.state.sourceTopLine === 0, "zb scrolled above the start of the buffer");
	logical.key("z"); logical.key("\x1b"); logical.key("b");
	assert(logical.state.cursorColumn === 0 && logical.state.sourceTopLine === 0, "Esc did not cancel the pending z command");

	const resized = review("x".repeat(1000), 120);
	resized.state.cursorColumn = 424;
	resized.render(); resized.keys("zz");
	assert(resized.state.sourceTopColumn === 243, "zz ignored the split response-pane width");
	resized.mode.render(18);
	resized.mode.handleInput("z"); resized.mode.handleInput("t");
	const narrowRows = resized.mode.render(18);
	assert(resized.state.cursorColumn === 424 && resized.state.sourceTopColumn === 414 && narrowRows.every(row => visibleWidth(row) <= 18), "viewport commands used stale wrapping after resize");

	const tiny = review("\n👨‍👩‍👧‍👦A", 1, 4);
	tiny.key("\x05");
	assert(tiny.state.cursor === 1 && tiny.state.cursorColumn === 0, "one-row scrolling lost an empty line or split a wide grapheme");
	tiny.key("\x05");
	assert(tiny.state.cursorColumn === "👨‍👩‍👧‍👦".length, "one-row scrolling split a ZWJ sequence");
	for (const key of ["zz", "zt", "zb", "w", "b"]) {
		tiny.keys(key);
		assert(tiny.render().length === 4 && tiny.render().every(row => visibleWidth(row) <= 1), `${key} exceeded tiny terminal bounds`);
	}

	const input = review("w b zz zt zb\nOther text", 72, 20);
	input.key("c"); input.keys("w b zz zt zb"); input.key("\x01"); input.key("\x05"); input.key("!"); input.key("\r");
	assert(input.items()[0]?.comment === "w b zz zt zb!" && input.state.cursorColumn === 0, "source bindings intercepted comment input or Ctrl-e line-end motion");
	input.key("/"); input.keys("w b zz zt zb"); input.key("\x05"); input.key("\r");
	assert(input.state.query === "w b zz zt zb", "source bindings intercepted search input");
	input.key("\t");
	const itemPosition = [input.state.cursor, input.state.cursorColumn, input.state.itemCursor];
	input.keys("wbzzztzb"); input.key("\x05");
	assert(input.state.focus === "items" && JSON.stringify(itemPosition) === JSON.stringify([input.state.cursor, input.state.cursorColumn, input.state.itemCursor]), "source bindings changed annotation-pane navigation");
	for (const value of [words, acrossLines, unicode, aligned, scrolled, logical, resized, tiny, input]) value.mode.dispose();
}
