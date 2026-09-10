import { Key, matchesKey, truncateToWidth, visibleWidth, wrapTextWithAnsi } from "@earendil-works/pi-tui";

export const reviewDraftEvent = "galpon:review:draft:v1";
export const maxReviewItems = 32;
export const maxReviewSelectionBytes = 24 * 1024;
export const maxReviewDraftBytes = 128 * 1024;

export type ReviewBlock = {
	index: number;
	text: string;
};

export type ReviewItem = {
	id: string;
	start: number;
	end: number;
	quote: string;
	comment: string;
};

export type ReviewFocus = "source" | "items";

export type ReviewViewState = {
	focus: ReviewFocus;
	cursor: number;
	anchor?: number;
	itemCursor: number;
	query: string;
};

export type ReviewAction =
	| { kind: "cancel" }
	| { kind: "finish" }
	| { kind: "search" }
	| { kind: "comment"; start: number; end: number }
	| { kind: "edit"; item: number }
	| { kind: "delete"; item: number };

function safeReviewLine(value: string): string {
	return value.replace(/[\p{Cc}\p{Cf}]/gu, character => character === "\t" ? "    " : "");
}

export function sanitizeReviewText(value: string): string {
	return String(value ?? "")
		.replace(/\x1B\][^\x07]*(?:\x07|\x1B\\)/g, "")
		.replace(/(?:\x1B\[|\u009B)[0-?]*[ -/]*[@-~]/g, "")
		.replace(/\x1B[@-_]/g, "")
		.replace(/\r\n?/g, "\n")
		.split("\n")
		.map(safeReviewLine)
		.join("\n");
}

function isFence(line: string): string {
	return line.match(/^\s*(`{3,}|~{3,})/)?.[1] ?? "";
}

function isHeading(line: string): boolean {
	return /^\s{0,3}#{1,6}\s+\S/.test(line) || /^\s*(?:---+|===+)\s*$/.test(line);
}

function isListItem(line: string): boolean {
	return /^\s*(?:[-+*]|\d+[.)])\s+\S/.test(line);
}

function isTableLine(line: string): boolean {
	return /^\s*\|.*\|\s*$/.test(line);
}

export function parseReviewBlocks(markdown: string): ReviewBlock[] {
	const lines = sanitizeReviewText(markdown).split("\n");
	const values: string[] = [];
	let current: string[] = [];
	let kind: "paragraph" | "list" | "table" | "fence" = "paragraph";
	let fence = "";
	const flush = () => {
		const text = current.join("\n").trim();
		if (text) values.push(text);
		current = [];
		kind = "paragraph";
	};

	for (const unsafeLine of lines) {
		const line = safeReviewLine(unsafeLine);
		if (kind === "fence") {
			current.push(line);
			if (new RegExp(`^\\s*${fence[0]}{${fence.length},}\\s*$`).test(line)) {
				flush();
				fence = "";
			}
			continue;
		}
		const marker = isFence(line);
		if (marker) {
			flush();
			kind = "fence";
			fence = marker;
			current.push(line);
			continue;
		}
		if (!line.trim()) {
			flush();
			continue;
		}
		if (isHeading(line)) {
			flush();
			values.push(line.trim());
			continue;
		}
		if (isListItem(line)) {
			flush();
			kind = "list";
			current.push(line.trimEnd());
			continue;
		}
		if (isTableLine(line)) {
			if (kind !== "table") {
				flush();
				kind = "table";
			}
			current.push(line.trimEnd());
			continue;
		}
		if (kind === "table") flush();
		current.push(line.trimEnd());
	}
	flush();
	return values.map((text, index) => ({ index, text }));
}

export function reviewSelection(blocks: ReviewBlock[], start: number, end: number): string {
	const first = Math.max(0, Math.min(start, end));
	const last = Math.min(blocks.length - 1, Math.max(start, end));
	return blocks.slice(first, last + 1).map(block => block.text).join("\n\n").trim();
}

function quoteMarkdown(value: string): string {
	return value.split("\n").map(line => line ? `> ${line}` : ">").join("\n");
}

export function compileReview(items: ReviewItem[]): string {
	const sections = items.map((item, index) => [
		`### ${index + 1}`,
		"",
		quoteMarkdown(item.quote.trim()),
		"",
		item.comment.trim(),
	].join("\n"));
	return ["I reviewed this response. Here is my feedback:", ...sections].join("\n\n").trim();
}

export function firstReviewMatch(blocks: ReviewBlock[], query: string, from: number, direction: 1 | -1): number {
	const needle = query.trim().toLocaleLowerCase();
	if (!needle || blocks.length === 0) return -1;
	for (let offset = 1; offset <= blocks.length; offset++) {
		const index = (from + direction * offset + blocks.length * 2) % blocks.length;
		if (blocks[index].text.toLocaleLowerCase().includes(needle)) return index;
	}
	return -1;
}

function firstVisibleLine(value: string): string {
	return value.split("\n").map(line => line.trim()).find(Boolean) ?? "";
}

function wrapPlainLine(value: string, width: number): string[] {
	const lines = wrapTextWithAnsi(safeReviewLine(value), Math.max(1, width));
	return lines.length > 0 ? lines : [""];
}

function styleSourceLine(line: string, block: number, state: ReviewViewState, theme: any): string {
	const first = state.anchor === undefined ? state.cursor : Math.min(state.anchor, state.cursor);
	const last = state.anchor === undefined ? state.cursor : Math.max(state.anchor, state.cursor);
	if (block >= first && block <= last) return theme.bg("selectedBg", theme.fg("text", line));
	if (block === state.cursor) return theme.fg("accent", line);
	return theme.fg("text", line);
}

function sourceRows(blocks: ReviewBlock[], state: ReviewViewState, width: number, theme: any): Array<{ block: number; line: string }> {
	const output: Array<{ block: number; line: string }> = [];
	const contentWidth = Math.max(1, width - 6);
	for (const block of blocks) {
		const blockLines = block.text.split("\n");
		let first = true;
		for (const sourceLine of blockLines) {
			for (const wrapped of wrapPlainLine(sourceLine, contentWidth)) {
				const marker = block.index === state.cursor && first ? "❯" : " ";
				const number = first ? String(block.index + 1).padStart(3, " ") : "   ";
				const line = `${marker}${number}  ${wrapped}`;
				output.push({ block: block.index, line: styleSourceLine(line, block.index, state, theme) });
				first = false;
			}
		}
		output.push({ block: block.index, line: "" });
	}
	return output;
}

function visibleSourceRows(blocks: ReviewBlock[], state: ReviewViewState, width: number, height: number, theme: any): string[] {
	const rows = sourceRows(blocks, state, width, theme);
	const target = Math.max(0, rows.findIndex(row => row.block === state.cursor));
	const start = Math.max(0, Math.min(target - Math.floor(height / 2), Math.max(0, rows.length - height)));
	const visible = rows.slice(start, start + height).map(row => row.line);
	while (visible.length < height) visible.push("");
	return visible;
}

function itemRows(items: ReviewItem[], state: ReviewViewState, width: number, height: number, theme: any): string[] {
	if (items.length === 0) {
		return [theme.fg("dim", "No feedback yet."), theme.fg("dim", "Select source text and press c."), ...Array(Math.max(0, height - 2)).fill("")];
	}
	const rows: Array<{ item: number; line: string }> = [];
	const contentWidth = Math.max(1, width - 4);
	for (let index = 0; index < items.length; index++) {
		const item = items[index];
		const active = index === state.itemCursor;
		const prefix = active ? "❯ " : "  ";
		const quote = `“${firstVisibleLine(item.quote)}”`;
		const summary = truncateToWidth(quote, contentWidth, "…");
		rows.push({ item: index, line: active ? theme.fg("accent", `${prefix}${index + 1}. ${summary}`) : theme.fg("text", `${prefix}${index + 1}. ${summary}`) });
		for (const line of item.comment.split("\n").flatMap(value => wrapPlainLine(value, contentWidth))) {
			rows.push({ item: index, line: theme.fg("muted", `    ${line}`) });
		}
		rows.push({ item: index, line: "" });
	}
	const target = Math.max(0, rows.findIndex(row => row.item === state.itemCursor));
	const start = Math.max(0, Math.min(target - Math.floor(height / 3), Math.max(0, rows.length - height)));
	const visible = rows.slice(start, start + height).map(row => row.line);
	while (visible.length < height) visible.push("");
	return visible;
}

function padReviewLine(value: string, width: number): string {
	const fitted = truncateToWidth(value, Math.max(1, width), "…");
	return fitted + " ".repeat(Math.max(0, width - visibleWidth(fitted)));
}

function joinReviewColumns(left: string[], right: string[], leftWidth: number, rightWidth: number): string[] {
	const output: string[] = [];
	for (let index = 0; index < Math.max(left.length, right.length); index++) {
		output.push(padReviewLine(left[index] ?? "", leftWidth) + truncateToWidth(right[index] ?? "", rightWidth, "…"));
	}
	return output;
}

export function renderReviewMode(blocks: ReviewBlock[], items: ReviewItem[], state: ReviewViewState, width: number, bodyHeight: number, theme: any): string[] {
	width = Math.max(1, width);
	bodyHeight = Math.max(6, Math.min(22, bodyHeight));
	const selection = state.anchor === undefined ? `${state.cursor + 1}` : `${Math.min(state.anchor, state.cursor) + 1}-${Math.max(state.anchor, state.cursor) + 1}`;
	const header = theme.fg("accent", theme.bold("GALPÓN  Review"));
	const meta = `${blocks.length} source blocks · ${items.length} feedback ${items.length === 1 ? "item" : "items"} · selected ${selection}${state.query ? ` · search: ${state.query}` : ""}`;
	const sourceTitle = (state.focus === "source" ? theme.fg("accent", theme.bold("SOURCE MESSAGE")) : theme.fg("muted", theme.bold("SOURCE MESSAGE")));
	const itemsTitle = (state.focus === "items" ? theme.fg("accent", theme.bold("REVIEW ITEMS")) : theme.fg("muted", theme.bold("REVIEW ITEMS")));
	const lines = [truncateToWidth(header, width, "…"), truncateToWidth(theme.fg("dim", meta), width, "…"), ""];

	if (width >= 100) {
		const leftWidth = Math.floor(width * 0.62);
		const rightWidth = width - leftWidth;
		const left = [sourceTitle, ...visibleSourceRows(blocks, state, leftWidth, bodyHeight, theme)];
		const right = [itemsTitle, ...itemRows(items, state, rightWidth, bodyHeight, theme)];
		lines.push(...joinReviewColumns(left, right, leftWidth, rightWidth));
	} else if (state.focus === "source") {
		lines.push(sourceTitle, ...visibleSourceRows(blocks, state, width, bodyHeight, theme));
	} else {
		lines.push(itemsTitle, ...itemRows(items, state, width, bodyHeight, theme));
	}
	lines.push("", theme.fg("dim", "j/k move · v range · c comment/edit · tab pane · / search · n/N match · x delete · s prepare · q close"));
	return lines.map(line => truncateToWidth(line, width, "…"));
}

export class ReviewMode {
	constructor(
		private blocks: ReviewBlock[],
		private items: ReviewItem[],
		private state: ReviewViewState,
		private theme: any,
		private onRender: () => void,
		private onDone: (action: ReviewAction) => void,
		private bodyHeight: number | (() => number) = 18,
	) {
		this.clamp();
	}

	private currentBodyHeight(): number {
		const value = typeof this.bodyHeight === "function" ? this.bodyHeight() : this.bodyHeight;
		return Number.isFinite(value) ? value : 18;
	}

	private clamp() {
		this.state.cursor = Math.max(0, Math.min(this.state.cursor, Math.max(0, this.blocks.length - 1)));
		if (this.state.anchor !== undefined) this.state.anchor = Math.max(0, Math.min(this.state.anchor, Math.max(0, this.blocks.length - 1)));
		this.state.itemCursor = Math.max(0, Math.min(this.state.itemCursor, Math.max(0, this.items.length - 1)));
		if (this.items.length === 0 && this.state.focus === "items") this.state.focus = "source";
	}

	private revealItem() {
		const item = this.items[this.state.itemCursor];
		if (!item) return;
		this.state.cursor = item.end;
		this.state.anchor = item.start === item.end ? undefined : item.start;
	}

	private move(amount: number) {
		if (this.state.focus === "source") this.state.cursor += amount;
		else this.state.itemCursor += amount;
		this.clamp();
		if (this.state.focus === "items") this.revealItem();
		this.onRender();
	}

	private moveMatch(direction: 1 | -1) {
		const match = firstReviewMatch(this.blocks, this.state.query, this.state.cursor, direction);
		if (match >= 0) {
			this.state.cursor = match;
			this.state.focus = "source";
			this.state.anchor = undefined;
		}
		this.onRender();
	}

	handleInput(data: string) {
		if (matchesKey(data, Key.escape) || data === "q") return this.onDone({ kind: "cancel" });
		if (data === "s") return this.onDone({ kind: "finish" });
		if (data === "/") return this.onDone({ kind: "search" });
		if (matchesKey(data, Key.tab)) {
			if (this.items.length > 0) {
				this.state.focus = this.state.focus === "source" ? "items" : "source";
				this.revealItem();
			}
			this.onRender();
			return;
		}
		if (data === "n") return this.moveMatch(1);
		if (data === "N") return this.moveMatch(-1);
		if (matchesKey(data, Key.up) || data === "k") return this.move(-1);
		if (matchesKey(data, Key.down) || data === "j") return this.move(1);
		if (matchesKey(data, Key.ctrl("u")) || matchesKey(data, "pageUp")) return this.move(-Math.max(5, this.currentBodyHeight() - 2));
		if (matchesKey(data, Key.ctrl("d")) || matchesKey(data, "pageDown")) return this.move(Math.max(5, this.currentBodyHeight() - 2));
		if (data === "g") {
			if (this.state.focus === "source") this.state.cursor = 0;
			else {
				this.state.itemCursor = 0;
				this.revealItem();
			}
			this.onRender();
			return;
		}
		if (data === "G") {
			if (this.state.focus === "source") this.state.cursor = Math.max(0, this.blocks.length - 1);
			else {
				this.state.itemCursor = Math.max(0, this.items.length - 1);
				this.revealItem();
			}
			this.onRender();
			return;
		}
		if (data === "v" && this.state.focus === "source") {
			if (this.state.anchor === undefined) this.state.anchor = this.state.cursor;
			this.onRender();
			return;
		}
		if ((data === "c" || matchesKey(data, Key.enter)) && this.state.focus === "source") {
			return this.onDone({ kind: "comment", start: this.state.anchor ?? this.state.cursor, end: this.state.cursor });
		}
		if ((data === "c" || matchesKey(data, Key.enter)) && this.state.focus === "items" && this.items.length > 0) {
			return this.onDone({ kind: "edit", item: this.state.itemCursor });
		}
		if ((data === "x" || data === "d") && this.state.focus === "items" && this.items.length > 0) {
			return this.onDone({ kind: "delete", item: this.state.itemCursor });
		}
	}

	render(width: number): string[] {
		return renderReviewMode(this.blocks, this.items, this.state, width, this.currentBodyHeight(), this.theme);
	}

	invalidate() {}
}
