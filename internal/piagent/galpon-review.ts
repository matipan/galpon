import {
	CURSOR_MARKER,
	Editor,
	type EditorTheme,
	Input,
	Key,
	matchesKey,
	truncateToWidth,
	visibleWidth,
	wrapTextWithAnsi,
} from "@earendil-works/pi-tui";

export const reviewDraftEvent = "galpon:review:draft:v1";
export const maxReviewItems = 32;
export const maxReviewSelectionBytes = 24 * 1024;
export const maxReviewDraftBytes = 128 * 1024;
export const maxReviewSourceBytes = 512 * 1024;
export const maxReviewBlocks = 2048;

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
	sourceRowOffset?: number;
	itemRowOffset?: number;
};

export type ReviewAction = { kind: "cancel" } | { kind: "finish" };

export type ReviewModeOptions = {
	tui?: any;
	renderMarkdown?: (markdown: string, width: number, selected: boolean) => string[];
	onItemsChanged?: (items: ReviewItem[]) => void;
	makeID?: () => string;
	confirmFinish?: boolean;
};

type ReviewInputMode = "normal" | "search" | "comment" | "confirm";
type RenderedSourceRow = { block: number; row: number; first: boolean; line: string };

function isReviewKey(data: string, key: string): boolean {
	return data === key || matchesKey(data, key);
}

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

function isATXHeading(line: string): boolean {
	return /^\s{0,3}#{1,6}\s+\S/.test(line);
}

function isSetextUnderline(line: string): boolean {
	return /^\s{0,3}(?:-+|=+)\s*$/.test(line);
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
		if (isSetextUnderline(line) && kind === "paragraph" && current.length > 0) {
			current.push(line.trimEnd());
			flush();
			continue;
		}
		if (isATXHeading(line) || isSetextUnderline(line)) {
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

function plainMarkdown(markdown: string, width: number): string[] {
	return markdown.split("\n").flatMap(line => wrapPlainLine(line, width));
}

function sourceRows(
	blocks: ReviewBlock[],
	items: ReviewItem[],
	state: ReviewViewState,
	width: number,
	theme: any,
	renderMarkdown: (markdown: string, width: number, selected: boolean) => string[],
): RenderedSourceRow[] {
	const output: RenderedSourceRow[] = [];
	const contentWidth = Math.max(1, width - 8);
	const visualFirst = state.anchor === undefined ? state.cursor : Math.min(state.anchor, state.cursor);
	const visualLast = state.anchor === undefined ? state.cursor : Math.max(state.anchor, state.cursor);
	const activeItem = state.focus === "items" ? items[state.itemCursor] : undefined;
	const selectedFirst = activeItem?.start ?? visualFirst;
	const selectedLast = activeItem?.end ?? visualLast;
	for (const block of blocks) {
		const annotation = items.some(item => block.index >= item.start && block.index <= item.end);
		const matched = Boolean(state.query && block.text.toLocaleLowerCase().includes(state.query.toLocaleLowerCase()));
		const selected = block.index >= selectedFirst && block.index <= selectedLast;
		const rendered = renderMarkdown(block.text, contentWidth, selected);
		const blockLines = rendered.length > 0 ? rendered : [""];
		const activeRow = Math.min(Math.max(0, state.sourceRowOffset ?? 0), blockLines.length - 1);
		for (let index = 0; index < blockLines.length; index++) {
			const first = index === 0;
			const cursor = block.index === state.cursor && index === activeRow && state.focus === "source" ? "❯" : " ";
			const number = first ? String(block.index + 1).padStart(3, " ") : "   ";
			const signal = first ? (annotation ? "●" : matched ? "◆" : " ") : " ";
			const gutter = theme.fg(block.index === state.cursor ? "accent" : "dim", `${cursor}${number}${signal}  `);
			const styledGutter = selected ? theme.bg("selectedBg", gutter) : gutter;
			const line = `${styledGutter}${blockLines[index]}`;
			output.push({ block: block.index, row: index, first, line: truncateToWidth(line, width, "…") });
		}
		output.push({ block: block.index, row: blockLines.length, first: false, line: "" });
	}
	return output;
}

function visibleSourceRows(
	blocks: ReviewBlock[],
	items: ReviewItem[],
	state: ReviewViewState,
	width: number,
	height: number,
	theme: any,
	renderMarkdown: (markdown: string, width: number, selected: boolean) => string[] = (markdown, width) => plainMarkdown(markdown, width),
): string[] {
	const rows = sourceRows(blocks, items, state, width, theme, renderMarkdown);
	const rowOffset = Math.max(0, state.sourceRowOffset ?? 0);
	const target = Math.max(0, rows.findIndex(row => row.block === state.cursor && row.row === rowOffset));
	const start = Math.max(0, Math.min(target - Math.floor(height / 3), Math.max(0, rows.length - height)));
	const visible = rows.slice(start, start + height).map(row => row.line);
	while (visible.length < height) visible.push("");
	return visible;
}

function itemRows(items: ReviewItem[], state: ReviewViewState, width: number, height: number, theme: any): string[] {
	if (items.length === 0) {
		return [theme.fg("dim", "No annotations yet."), theme.fg("dim", "Select a passage and press c."), ...Array(Math.max(0, height - 2)).fill("")];
	}
	const rows: Array<{ item: number; row: number; line: string }> = [];
	const contentWidth = Math.max(1, width - 4);
	for (let index = 0; index < items.length; index++) {
		const item = items[index];
		const active = index === state.itemCursor;
		const prefix = active ? "❯ " : "  ";
		const summary = truncateToWidth(`“${firstVisibleLine(item.quote)}”`, contentWidth, "…");
		let heading = `${prefix}${index + 1}. ${summary}`;
		if (active && state.focus === "items") heading = theme.bg("selectedBg", padReviewLine(theme.fg("text", heading), width));
		else heading = theme.fg(active ? "accent" : "text", heading);
		rows.push({ item: index, row: 0, line: heading });
		const commentLines = item.comment.split("\n").flatMap(value => wrapPlainLine(value, contentWidth));
		for (let row = 0; row < commentLines.length; row++) {
			rows.push({ item: index, row: row + 1, line: theme.fg("muted", `    ${commentLines[row]}`) });
		}
		rows.push({ item: index, row: commentLines.length + 1, line: "" });
	}
	const rowOffset = Math.max(0, state.itemRowOffset ?? 0);
	const target = Math.max(0, rows.findIndex(row => row.item === state.itemCursor && row.row === rowOffset));
	const start = Math.max(0, Math.min(target - Math.floor(height / 3), Math.max(0, rows.length - height)));
	const visible = rows.slice(start, start + height).map(row => row.line);
	while (visible.length < height) visible.push("");
	return visible;
}

export function renderReviewMode(
	blocks: ReviewBlock[],
	items: ReviewItem[],
	state: ReviewViewState,
	width: number,
	bodyHeight: number,
	theme: any,
	renderMarkdown: (markdown: string, width: number, selected: boolean) => string[] = (markdown, width) => plainMarkdown(markdown, width),
): string[] {
	width = Math.max(1, width);
	bodyHeight = Math.max(1, Math.floor(bodyHeight));
	const sourceTitle = state.focus === "source" ? theme.fg("accent", theme.bold("RESPONSE")) : theme.fg("muted", theme.bold("RESPONSE"));
	const itemsTitle = state.focus === "items" ? theme.fg("accent", theme.bold("ANNOTATIONS")) : theme.fg("muted", theme.bold("ANNOTATIONS"));
	if (width >= 108) {
		const leftWidth = Math.floor(width * 0.68);
		const rightWidth = width - leftWidth;
		const left = [sourceTitle, ...visibleSourceRows(blocks, items, state, leftWidth, bodyHeight, theme, renderMarkdown)];
		const right = [itemsTitle, ...itemRows(items, state, rightWidth, bodyHeight, theme)];
		return joinReviewColumns(left, right, leftWidth, rightWidth).map(line => truncateToWidth(line, width, "…"));
	}
	if (state.focus === "items") return [itemsTitle, ...itemRows(items, state, width, bodyHeight, theme)].map(line => truncateToWidth(line, width, "…"));
	return [sourceTitle, ...visibleSourceRows(blocks, items, state, width, bodyHeight, theme, renderMarkdown)].map(line => truncateToWidth(line, width, "…"));
}

export class ReviewMode {
	private inputMode: ReviewInputMode = "normal";
	private pendingKey = "";
	private notice = "";
	private editingItem: number | undefined;
	private readonly searchInput = new Input();
	private readonly commentEditor: Editor;
	private readonly renderMarkdown: (markdown: string, width: number, selected: boolean) => string[];
	private readonly markdownCache = new Map<string, string[]>();
	private readonly confirmFinish: boolean;
	private readonly onItemsChanged: (items: ReviewItem[]) => void;
	private readonly makeID: () => string;
	private readonly itemHistory: ReviewItem[][] = [];
	private lastSourcePaneWidth = 80;
	private lastItemsPaneWidth = 80;
	private _focused = false;

	constructor(
		private blocks: ReviewBlock[],
		private items: ReviewItem[],
		private state: ReviewViewState,
		private theme: any,
		private onRender: () => void,
		private onDone: (action: ReviewAction) => void,
		private bodyHeight: number | (() => number) = 24,
		options: ReviewModeOptions = {},
	) {
		const tui = options.tui ?? { requestRender: this.onRender, terminal: { rows: 24, columns: 80 } };
		const editorTheme: EditorTheme = {
			borderColor: (text: string) => this.theme.fg("accent", text),
			selectList: {
				selectedPrefix: (text: string) => this.theme.fg("accent", text),
				selectedText: (text: string) => this.theme.fg("accent", text),
				description: (text: string) => this.theme.fg("muted", text),
				scrollInfo: (text: string) => this.theme.fg("dim", text),
				noMatch: (text: string) => this.theme.fg("warning", text),
			},
		};
		this.commentEditor = new Editor(tui, editorTheme);
		this.renderMarkdown = options.renderMarkdown ?? plainMarkdown;
		this.onItemsChanged = options.onItemsChanged ?? (() => {});
		this.makeID = options.makeID ?? (() => `${Date.now()}-${Math.random().toString(16).slice(2)}`);
		this.confirmFinish = options.confirmFinish === true;
		this.searchInput.onSubmit = value => this.applySearch(value);
		this.searchInput.onEscape = () => this.leaveInputMode();
		this.commentEditor.onSubmit = value => this.saveComment(value);
		this.clamp();
		this.syncInputFocus();
	}

	get focused(): boolean {
		return this._focused;
	}

	set focused(value: boolean) {
		this._focused = value;
		this.syncInputFocus();
	}

	private syncInputFocus() {
		this.searchInput.focused = this._focused && this.inputMode === "search";
		this.commentEditor.focused = this._focused && this.inputMode === "comment";
	}

	private refresh() {
		this.syncInputFocus();
		this.onRender();
	}

	private currentBodyHeight(): number {
		const value = typeof this.bodyHeight === "function" ? this.bodyHeight() : this.bodyHeight;
		return Number.isFinite(value) ? Math.max(1, Math.floor(value)) : 24;
	}

	private renderMarkdownBlock(markdown: string, width: number, selected: boolean): string[] {
		const key = `${selected ? "selected" : "normal"}\u0000${width}\u0000${markdown}`;
		const cached = this.markdownCache.get(key);
		if (cached) return cached;
		if (this.markdownCache.size > this.blocks.length * 4) this.markdownCache.clear();
		const rendered = this.renderMarkdown(markdown, width, selected).map(line => truncateToWidth(line, Math.max(1, width), "…"));
		this.markdownCache.set(key, rendered);
		return rendered;
	}

	private blockRowCount(index: number): number {
		const block = this.blocks[index];
		if (!block) return 1;
		const selected = index >= this.selection().start && index <= this.selection().end;
		return Math.max(1, this.renderMarkdownBlock(block.text, Math.max(1, this.lastSourcePaneWidth - 8), selected).length);
	}

	private itemRowCount(index: number): number {
		const item = this.items[index];
		if (!item) return 1;
		const contentWidth = Math.max(1, this.lastItemsPaneWidth - 4);
		return 1 + item.comment.split("\n").flatMap(value => wrapPlainLine(value, contentWidth)).length;
	}

	private clamp() {
		this.state.cursor = Math.max(0, Math.min(this.state.cursor, Math.max(0, this.blocks.length - 1)));
		if (this.state.anchor !== undefined) this.state.anchor = Math.max(0, Math.min(this.state.anchor, Math.max(0, this.blocks.length - 1)));
		this.state.itemCursor = Math.max(0, Math.min(this.state.itemCursor, Math.max(0, this.items.length - 1)));
		this.state.sourceRowOffset = Math.max(0, Math.min(this.state.sourceRowOffset ?? 0, this.blockRowCount(this.state.cursor) - 1));
		this.state.itemRowOffset = Math.max(0, Math.min(this.state.itemRowOffset ?? 0, this.itemRowCount(this.state.itemCursor) - 1));
		if (this.items.length === 0 && this.state.focus === "items") this.state.focus = "source";
	}

	private setNotice(message: string) {
		this.notice = sanitizeReviewText(message).replace(/\n+/g, " ").trim();
		this.refresh();
	}

	private selection(): { start: number; end: number } {
		return {
			start: Math.min(this.state.anchor ?? this.state.cursor, this.state.cursor),
			end: Math.max(this.state.anchor ?? this.state.cursor, this.state.cursor),
		};
	}

	private revealItem() {
		const item = this.items[this.state.itemCursor];
		if (!item) return;
		this.state.cursor = item.end;
		this.state.sourceRowOffset = 0;
		this.state.anchor = undefined;
	}

	private moveSourceRows(amount: number) {
		let remaining = Math.abs(amount);
		const direction = amount < 0 ? -1 : 1;
		while (remaining > 0) {
			const offset = this.state.sourceRowOffset ?? 0;
			if (direction > 0) {
				const available = this.blockRowCount(this.state.cursor) - 1 - offset;
				if (available >= remaining) {
					this.state.sourceRowOffset = offset + remaining;
					break;
				}
				if (this.state.cursor >= this.blocks.length - 1) {
					this.state.sourceRowOffset = this.blockRowCount(this.state.cursor) - 1;
					break;
				}
				remaining -= available + 1;
				this.state.cursor++;
				this.state.sourceRowOffset = 0;
			} else {
				if (offset >= remaining) {
					this.state.sourceRowOffset = offset - remaining;
					break;
				}
				if (this.state.cursor <= 0) {
					this.state.sourceRowOffset = 0;
					break;
				}
				remaining -= offset + 1;
				this.state.cursor--;
				this.state.sourceRowOffset = this.blockRowCount(this.state.cursor) - 1;
			}
		}
	}

	private moveItemRows(amount: number) {
		let remaining = Math.abs(amount);
		const direction = amount < 0 ? -1 : 1;
		while (remaining > 0) {
			const offset = this.state.itemRowOffset ?? 0;
			if (direction > 0) {
				const available = this.itemRowCount(this.state.itemCursor) - 1 - offset;
				if (available >= remaining) {
					this.state.itemRowOffset = offset + remaining;
					break;
				}
				if (this.state.itemCursor >= this.items.length - 1) {
					this.state.itemRowOffset = this.itemRowCount(this.state.itemCursor) - 1;
					break;
				}
				remaining -= available + 1;
				this.state.itemCursor++;
				this.state.itemRowOffset = 0;
			} else {
				if (offset >= remaining) {
					this.state.itemRowOffset = offset - remaining;
					break;
				}
				if (this.state.itemCursor <= 0) {
					this.state.itemRowOffset = 0;
					break;
				}
				remaining -= offset + 1;
				this.state.itemCursor--;
				this.state.itemRowOffset = this.itemRowCount(this.state.itemCursor) - 1;
			}
		}
	}

	private move(amount: number) {
		this.notice = "";
		if (this.state.focus === "source") this.moveSourceRows(amount);
		else this.moveItemRows(amount);
		this.clamp();
		if (this.state.focus === "items") this.revealItem();
		this.refresh();
	}

	private moveMatch(direction: 1 | -1) {
		const match = firstReviewMatch(this.blocks, this.state.query, this.state.cursor, direction);
		if (match >= 0) {
			this.state.cursor = match;
			this.state.sourceRowOffset = 0;
			this.state.focus = "source";
			this.state.anchor = undefined;
			this.notice = "";
		} else if (this.state.query) {
			this.notice = `No response block matches “${this.state.query}”.`;
		}
		this.refresh();
	}

	private moveAnnotation(direction: 1 | -1) {
		if (this.items.length === 0) return this.setNotice("There are no annotations.");
		const candidates = this.items.map((item, index) => ({ item, index }));
		const ordered = direction > 0
			? candidates.sort((left, right) => left.item.start - right.item.start || left.item.end - right.item.end || left.index - right.index)
			: candidates.sort((left, right) => right.item.end - left.item.end || right.item.start - left.item.start || left.index - right.index);
		const target = ordered.find(candidate => direction > 0 ? candidate.item.start > this.state.cursor : candidate.item.end < this.state.cursor) ?? ordered[0];
		this.state.itemCursor = target.index;
		this.state.itemRowOffset = 0;
		this.state.focus = "source";
		this.revealItem();
		this.notice = `Annotation ${target.index + 1} of ${this.items.length}.`;
		this.refresh();
	}

	private enterSearch() {
		this.inputMode = "search";
		this.pendingKey = "";
		this.searchInput.setValue(this.state.query);
		this.notice = "";
		this.refresh();
	}

	private applySearch(value: string) {
		this.state.query = sanitizeReviewText(value).replace(/\n+/g, " ").trim();
		this.inputMode = "normal";
		if (this.state.query) this.moveMatch(1);
		else this.refresh();
	}

	private enterComment(item?: number) {
		if (item === undefined && this.items.length >= maxReviewItems) return this.setNotice(`A review can contain at most ${maxReviewItems} annotations.`);
		this.editingItem = item;
		this.commentEditor.setText(item === undefined ? "" : this.items[item]?.comment ?? "");
		this.inputMode = "comment";
		this.pendingKey = "";
		this.notice = "";
		this.refresh();
	}

	private rememberItems() {
		this.itemHistory.push(this.items.map(item => ({ ...item })));
		if (this.itemHistory.length > 32) this.itemHistory.shift();
	}

	private saveComment(value: string) {
		const comment = sanitizeReviewText(value).trim();
		if (!comment) return this.setNotice("Write a comment or press Esc to cancel.");
		let next: ReviewItem[];
		if (this.editingItem !== undefined) {
			if (!this.items[this.editingItem]) return this.leaveInputMode();
			next = this.items.map((item, index) => index === this.editingItem ? { ...item, comment } : item);
		} else {
			const range = this.selection();
			const quote = reviewSelection(this.blocks, range.start, range.end);
			if (new TextEncoder().encode(quote).byteLength > maxReviewSelectionBytes) return this.setNotice("The selected passage is too large. Select fewer blocks.");
			next = [...this.items, { id: this.makeID(), start: range.start, end: range.end, quote, comment }];
		}
		if (new TextEncoder().encode(compileReview(next)).byteLength > maxReviewDraftBytes) return this.setNotice("The review draft is too large.");
		this.rememberItems();
		this.items = next;
		this.state.itemCursor = this.editingItem ?? next.length - 1;
		this.state.itemRowOffset = 0;
		this.state.anchor = undefined;
		this.inputMode = "normal";
		this.editingItem = undefined;
		this.commentEditor.setText("");
		this.onItemsChanged(next.map(item => ({ ...item })));
		this.notice = `Saved annotation ${this.state.itemCursor + 1}.`;
		this.refresh();
	}

	private deleteItem() {
		if (!this.items[this.state.itemCursor]) return;
		const deleted = this.state.itemCursor;
		this.rememberItems();
		this.items = this.items.filter((_item, index) => index !== deleted);
		this.state.itemCursor = Math.min(deleted, Math.max(0, this.items.length - 1));
		this.state.itemRowOffset = 0;
		this.state.anchor = undefined;
		if (this.items.length === 0) this.state.focus = "source";
		else this.revealItem();
		this.onItemsChanged(this.items.map(item => ({ ...item })));
		this.notice = `Deleted annotation ${deleted + 1}.`;
		this.refresh();
	}

	private undoItems() {
		const previous = this.itemHistory.pop();
		if (!previous) return this.setNotice("There is no annotation change to undo.");
		this.items = previous;
		this.state.itemCursor = Math.min(this.state.itemCursor, Math.max(0, this.items.length - 1));
		this.state.itemRowOffset = 0;
		if (this.items.length === 0) this.state.focus = "source";
		else if (this.state.focus === "items") this.revealItem();
		this.onItemsChanged(this.items.map(item => ({ ...item })));
		this.notice = "Restored the previous annotation change.";
		this.refresh();
	}

	private leaveInputMode() {
		this.inputMode = "normal";
		this.editingItem = undefined;
		this.commentEditor.setText("");
		this.notice = "";
		this.refresh();
	}

	private handlePending(data: string): boolean {
		if (!this.pendingKey) return false;
		const pending = this.pendingKey;
		this.pendingKey = "";
		if (pending === "g" && isReviewKey(data, "g")) {
			if (this.state.focus === "source") {
				this.state.cursor = 0;
				this.state.sourceRowOffset = 0;
			} else {
				this.state.itemCursor = 0;
				this.state.itemRowOffset = 0;
				this.revealItem();
			}
			this.state.anchor = undefined;
			this.refresh();
			return true;
		}
		if ((pending === "]" || pending === "[") && isReviewKey(data, "a")) {
			this.moveAnnotation(pending === "]" ? 1 : -1);
			return true;
		}
		if (pending === "d" && isReviewKey(data, "d") && this.state.focus === "items") {
			this.deleteItem();
			return true;
		}
		this.refresh();
		return true;
	}

	handleInput(data: string) {
		if (this.inputMode === "confirm") {
			if (isReviewKey(data, "y") || matchesKey(data, Key.enter)) return this.onDone({ kind: "finish" });
			if (isReviewKey(data, "n") || matchesKey(data, Key.escape)) return this.leaveInputMode();
			return;
		}
		if (this.inputMode === "search") {
			if (matchesKey(data, Key.escape)) return this.leaveInputMode();
			this.searchInput.handleInput(data);
			this.refresh();
			return;
		}
		if (this.inputMode === "comment") {
			if (matchesKey(data, Key.escape)) return this.leaveInputMode();
			this.commentEditor.handleInput(data);
			this.refresh();
			return;
		}
		if (matchesKey(data, Key.escape)) {
			this.pendingKey = "";
			this.notice = "";
			if (this.state.anchor !== undefined) this.state.anchor = undefined;
			else if (this.state.focus === "items") this.state.focus = "source";
			this.refresh();
			return;
		}
		if (this.handlePending(data)) return;
		if (isReviewKey(data, "q")) return this.onDone({ kind: "cancel" });
		if (isReviewKey(data, "s")) {
			if (this.items.length === 0) return this.setNotice("Add an annotation before you prepare the review.");
			if (this.confirmFinish) {
				this.inputMode = "confirm";
				this.notice = "";
				this.refresh();
				return;
			}
			return this.onDone({ kind: "finish" });
		}
		if (isReviewKey(data, "/")) return this.enterSearch();
		const pending = ["g", "]", "[", "d"].find(key => isReviewKey(data, key));
		if (pending && (pending !== "d" || this.state.focus === "items")) {
			this.pendingKey = pending;
			this.refresh();
			return;
		}
		if (matchesKey(data, Key.tab)) {
			if (this.items.length > 0) {
				this.state.focus = this.state.focus === "source" ? "items" : "source";
				this.state.anchor = undefined;
				if (this.state.focus === "items") {
					this.state.itemRowOffset = 0;
					this.revealItem();
				}
			}
			this.refresh();
			return;
		}
		if (isReviewKey(data, "h")) {
			this.state.focus = "source";
			this.state.anchor = undefined;
			this.refresh();
			return;
		}
		if (isReviewKey(data, "l") && this.items.length > 0) {
			this.state.focus = "items";
			this.state.itemRowOffset = 0;
			this.state.anchor = undefined;
			this.revealItem();
			this.refresh();
			return;
		}
		if (isReviewKey(data, "n")) return this.moveMatch(1);
		if (isReviewKey(data, "N")) return this.moveMatch(-1);
		if (isReviewKey(data, "u")) return this.undoItems();
		if (matchesKey(data, Key.up) || isReviewKey(data, "k")) return this.move(-1);
		if (matchesKey(data, Key.down) || isReviewKey(data, "j")) return this.move(1);
		if (matchesKey(data, Key.ctrl("u")) || matchesKey(data, "pageUp")) return this.move(-Math.max(5, Math.floor(this.currentBodyHeight() / 2)));
		if (matchesKey(data, Key.ctrl("d")) || matchesKey(data, "pageDown")) return this.move(Math.max(5, Math.floor(this.currentBodyHeight() / 2)));
		if (isReviewKey(data, "G")) {
			if (this.state.focus === "source") {
				this.state.cursor = Math.max(0, this.blocks.length - 1);
				this.state.sourceRowOffset = this.blockRowCount(this.state.cursor) - 1;
			} else {
				this.state.itemCursor = Math.max(0, this.items.length - 1);
				this.state.itemRowOffset = this.itemRowCount(this.state.itemCursor) - 1;
				this.revealItem();
			}
			this.state.anchor = undefined;
			this.refresh();
			return;
		}
		if ((isReviewKey(data, "v") || isReviewKey(data, "V")) && this.state.focus === "source") {
			this.state.anchor = this.state.anchor === undefined ? this.state.cursor : undefined;
			this.notice = "";
			this.refresh();
			return;
		}
		if (isReviewKey(data, "o") && this.state.focus === "source" && this.state.anchor !== undefined) {
			const cursor = this.state.cursor;
			this.state.cursor = this.state.anchor;
			this.state.sourceRowOffset = 0;
			this.state.anchor = cursor;
			this.refresh();
			return;
		}
		if ((isReviewKey(data, "c") || isReviewKey(data, "a") || matchesKey(data, Key.enter)) && this.state.focus === "source") return this.enterComment();
		if ((isReviewKey(data, "c") || isReviewKey(data, "e") || matchesKey(data, Key.enter)) && this.state.focus === "items" && this.items.length > 0) return this.enterComment(this.state.itemCursor);
		if (isReviewKey(data, "x") && this.state.focus === "items") return this.deleteItem();
	}

	private fitInputRows(rows: string[], maxRows: number): string[] {
		maxRows = Math.max(0, maxRows);
		if (rows.length <= maxRows) return rows;
		if (maxRows === 0) return [];
		const hasTextInput = this.inputMode === "search" || this.inputMode === "comment";
		const cursorRow = Math.max(0, rows.findIndex(line => line.includes(CURSOR_MARKER) || line.includes("\x1b[7m")));
		const fallbackCursorRow = this.inputMode === "search" ? 1 : Math.min(2, rows.length - 1);
		const activeRow = cursorRow > 0 ? cursorRow : fallbackCursorRow;
		if (maxRows === 1) return [hasTextInput ? rows[activeRow] : rows[0]];
		if (maxRows === 2) return hasTextInput ? [rows[0], rows[activeRow]] : [rows[0], rows[rows.length - 1]];
		const middle = rows.slice(1, -1);
		const middleRows = maxRows - 2;
		const cursor = Math.max(0, middle.findIndex(line => line.includes(CURSOR_MARKER) || line.includes("\x1b[7m")));
		const start = Math.max(0, Math.min(cursor - Math.floor(middleRows / 2), Math.max(0, middle.length - middleRows)));
		return [rows[0], ...middle.slice(start, start + middleRows), rows[rows.length - 1]];
	}

	private renderInput(width: number, maxRows: number): string[] {
		let rows: string[] = [];
		if (this.inputMode === "confirm") {
			rows = [
				this.theme.fg("warning", this.theme.bold("REPLACE UNSENT EDITOR TEXT?")),
				this.theme.fg("text", "Preparing this review will replace the current Pi editor draft."),
				this.theme.fg("dim", "y/Enter replace · n/Esc cancel"),
			];
		}
		if (this.inputMode === "search") {
			rows = [
				this.theme.fg("accent", this.theme.bold("SEARCH")),
				...this.searchInput.render(Math.max(1, width)),
				this.theme.fg("dim", "Enter find · Esc cancel"),
			];
		}
		if (this.inputMode === "comment") {
			const range = this.editingItem === undefined ? this.selection() : undefined;
			const label = this.editingItem === undefined
				? `COMMENT ON ${range!.start + 1}${range!.start === range!.end ? "" : `-${range!.end + 1}`}`
				: `EDIT ANNOTATION ${this.editingItem + 1}`;
			rows = [
				this.theme.fg("success", this.theme.bold(label)),
				...this.commentEditor.render(Math.max(1, width)),
				this.theme.fg("dim", "Enter save · Shift+Enter newline · Esc cancel"),
			];
		}
		return this.fitInputRows(rows, maxRows);
	}

	render(width: number): string[] {
		width = Math.max(1, width);
		this.lastSourcePaneWidth = width >= 108 ? Math.floor(width * 0.68) : width;
		this.lastItemsPaneWidth = width >= 108 ? width - this.lastSourcePaneWidth : width;
		this.clamp();
		const totalHeight = this.currentBodyHeight();
		const visual = this.state.anchor !== undefined;
		const mode = this.inputMode === "search" ? "SEARCH" : this.inputMode === "comment" ? "COMMENT" : this.inputMode === "confirm" ? "CONFIRM" : visual ? "VISUAL" : "NORMAL";
		const modeColor = mode === "COMMENT" ? "success" : mode === "VISUAL" || mode === "CONFIRM" ? "warning" : "accent";
		const selection = this.selection();
		const meta = `${this.blocks.length} blocks · ${this.items.length} annotation${this.items.length === 1 ? "" : "s"} · ${selection.start + 1}${selection.start === selection.end ? "" : `-${selection.end + 1}`}${this.state.query ? ` · /${this.state.query}` : ""}`;
		const title = padReviewLine(`${this.theme.fg("accent", this.theme.bold("GALPÓN REVIEW"))}  ${this.theme.fg(modeColor, mode)}  ${this.theme.fg("dim", meta)}`, width);
		const command = this.pendingKey ? `${this.pendingKey}_` : this.notice;
		const help = this.state.focus === "items"
			? "j/k move · Enter/e edit · x/dd delete · u undo · h/Tab response · s prepare · q close"
			: "j/k move · gg/G ends · v visual · o swap · c comment · / search · ]a/[a annotations · l/Tab pane · s prepare · q close";
		const statusText = truncateToWidth(command || help, width, "…");
		const status = this.theme.bg("selectedBg", padReviewLine(this.theme.fg(command ? "text" : "dim", statusText), width));
		if (totalHeight === 1) return [padReviewLine(status, width)];
		const titleLine = truncateToWidth(title, width, "…");
		const available = Math.max(0, totalHeight - 2);
		const input = this.renderInput(width, available);
		const separatorRows = input.length > 0 && available - input.length >= 2 ? 1 : 0;
		const mainRows = Math.max(0, available - input.length - separatorRows);
		const main = mainRows > 0
			? renderReviewMode(this.blocks, this.items, this.state, width, Math.max(1, mainRows - 1), this.theme, (markdown, availableWidth, selected) => this.renderMarkdownBlock(markdown, availableWidth, selected)).slice(0, mainRows)
			: [];
		const content = [titleLine, ...main, ...(separatorRows ? [""] : []), ...input];
		while (content.length < totalHeight - 1) content.push("");
		return [...content.slice(0, totalHeight - 1), status].map(line => padReviewLine(line, width));
	}

	invalidate() {
		this.markdownCache.clear();
		this.searchInput.invalidate();
		this.commentEditor.invalidate();
	}
}
