import {
	CURSOR_MARKER,
	Editor,
	type EditorTheme,
	Input,
	Key,
	matchesKey,
	sliceByColumn,
	truncateToWidth,
	visibleWidth,
	wrapTextWithAnsi,
} from "@earendil-works/pi-tui";

export const reviewDraftEvent = "galpon:review:draft:v1";
export const reviewParserVersion = 3;
export const maxReviewItems = 32;
export const maxReviewSelectionBytes = 24 * 1024;
export const maxReviewDraftBytes = 128 * 1024;
export const maxReviewSourceBytes = 512 * 1024;
export const maxReviewBlocks = 2048;

export type ReviewBlock = {
	index: number;
	text: string;
	kind?: "text" | "heading" | "list" | "quote" | "table" | "fence" | "code";
	startOffset?: number;
	endOffset?: number;
};

export type ReviewItem = {
	id: string;
	start: number;
	end: number;
	startColumn?: number;
	endColumn?: number;
	quote: string;
	comment: string;
};

export type ReviewFocus = "source" | "items";

export type ReviewViewState = {
	focus: ReviewFocus;
	cursor: number;
	cursorColumn?: number;
	anchor?: number;
	anchorColumn?: number;
	visualMode?: "character" | "line";
	itemCursor: number;
	query: string;
	sourceTopLine?: number;
	sourceLeftColumn?: number;
	itemRowOffset?: number;
};

export type ReviewAction = { kind: "cancel" } | { kind: "finish" };

export type ReviewEditingDraft = {
	kind: "new" | "edit";
	itemId?: string;
	start: number;
	end: number;
	startColumn?: number;
	endColumn?: number;
	visualMode?: "character" | "line";
	buffer: string;
};

export type ReviewModeOptions = {
	tui?: any;
	onItemsChanged?: (items: ReviewItem[]) => void;
	onEditingChanged?: (editing: ReviewEditingDraft | undefined) => void;
	editing?: ReviewEditingDraft;
	makeID?: () => string;
	confirmFinish?: boolean;
};

type ReviewInputMode = "normal" | "search" | "comment" | "confirm";
type ReviewRange = { start: number; end: number; startColumn: number; endColumn: number };

function isReviewKey(data: string, key: string): boolean {
	return data === key || matchesKey(data, key);
}

function safeReviewLine(value: string): string {
	return value.replace(/[\p{Cc}\p{Cf}]/gu, character => {
		if (character === "\t") return "    ";
		if (character === "\u200C" || character === "\u200D") return character;
		return "";
	});
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

function sanitizeLegacyReviewText(value: string): string {
	return sanitizeReviewText(value).replace(/[\u200C\u200D]/g, "");
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
	const lines = sanitizeLegacyReviewText(markdown).split("\n");
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
		const line = unsafeLine;
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
	const source = sanitizeLegacyReviewText(markdown);
	let searchFrom = 0;
	return values.map((text, index) => {
		let startOffset = source.indexOf(text, searchFrom);
		while (startOffset >= 0) {
			const lineStart = source.lastIndexOf("\n", startOffset - 1) + 1;
			const endOffset = startOffset + text.length;
			const nextBreak = source.indexOf("\n", endOffset);
			const lineEnd = nextBreak < 0 ? source.length : nextBreak;
			if (!source.slice(lineStart, startOffset).trim() && !source.slice(endOffset, lineEnd).trim()) break;
			startOffset = source.indexOf(text, startOffset + 1);
		}
		startOffset = Math.max(0, startOffset);
		const endOffset = startOffset + text.length;
		searchFrom = endOffset;
		return { index, text, startOffset, endOffset };
	});
}

export function legacyReviewOffset(markdown: string, legacyOffset: number): number {
	const source = sanitizeReviewText(markdown);
	legacyOffset = Math.max(0, Math.min(Math.floor(legacyOffset), sanitizeLegacyReviewText(markdown).length));
	let currentOffset = 0;
	let retainedOffset = 0;
	while (currentOffset < source.length && retainedOffset < legacyOffset) {
		if (source[currentOffset] !== "\u200C" && source[currentOffset] !== "\u200D") retainedOffset++;
		currentOffset++;
	}
	return currentOffset;
}

export function parseReviewBuffer(markdown: string): ReviewBlock[] {
	const lines = sanitizeReviewText(markdown).split("\n");
	let inFence = false;
	let fenceMarker = "";
	let offset = 0;
	return lines.map((text, index) => {
		let kind: ReviewBlock["kind"] = "text";
		const marker = isFence(text);
		if (marker) {
			kind = "fence";
			if (!inFence) {
				inFence = true;
				fenceMarker = marker;
			} else if (marker[0] === fenceMarker[0] && marker.length >= fenceMarker.length) {
				inFence = false;
				fenceMarker = "";
			}
		} else if (inFence) kind = "code";
		else if (isATXHeading(text) || isSetextUnderline(text) || (index + 1 < lines.length && isSetextUnderline(lines[index + 1]))) kind = "heading";
		else if (isListItem(text)) kind = "list";
		else if (/^\s*>/.test(text)) kind = "quote";
		else if (isTableLine(text)) kind = "table";
		const startOffset = offset;
		const endOffset = offset + text.length;
		offset = endOffset + (index + 1 < lines.length ? 1 : 0);
		return { index, text, kind, startOffset, endOffset };
	});
}

export function legacyReviewSelection(blocks: ReviewBlock[], start: number, end: number): string {
	const first = Math.max(0, Math.min(start, end));
	const last = Math.min(blocks.length - 1, Math.max(start, end));
	return blocks.slice(first, last + 1).map(block => block.text).join("\n\n").trim();
}

export function reviewSelection(lines: ReviewBlock[], start: number, end: number, startColumn = 0, endColumn?: number): string {
	if (lines.length === 0) return "";
	let first = { line: start, column: startColumn };
	let last = { line: end, column: endColumn ?? lines[Math.max(0, Math.min(end, lines.length - 1))]?.text.length ?? 0 };
	if (first.line > last.line || (first.line === last.line && first.column > last.column)) [first, last] = [last, first];
	first.line = Math.max(0, Math.min(first.line, lines.length - 1));
	last.line = Math.max(0, Math.min(last.line, lines.length - 1));
	first.column = Math.max(0, Math.min(first.column, lines[first.line].text.length));
	last.column = Math.max(0, Math.min(last.column, lines[last.line].text.length));
	if (first.line === last.line) return lines[first.line].text.slice(first.column, last.column);
	return [
		lines[first.line].text.slice(first.column),
		...lines.slice(first.line + 1, last.line).map(line => line.text),
		lines[last.line].text.slice(0, last.column),
	].join("\n");
}

function quoteMarkdown(value: string): string {
	return value.split("\n").map(line => line ? `> ${line}` : ">").join("\n");
}

export function compileReview(items: ReviewItem[]): string {
	const sections = items.map((item, index) => [
		`### ${index + 1}`,
		"",
		quoteMarkdown(item.quote),
		"",
		item.comment.trim(),
	].join("\n"));
	return ["I reviewed this response. Here is my feedback:", ...sections].join("\n\n").trim();
}

function reviewMatchColumns(text: string, query: string): number[] {
	const needle = query.trim().toLocaleLowerCase();
	if (!needle) return [];
	let folded = "";
	const originalColumns = new Map<number, number>();
	for (const grapheme of graphemeColumns(text)) {
		originalColumns.set(folded.length, grapheme.start);
		folded += grapheme.text.toLocaleLowerCase();
	}
	const matches: number[] = [];
	for (let match = folded.indexOf(needle); match >= 0; match = folded.indexOf(needle, match + 1)) {
		const column = originalColumns.get(match);
		if (column !== undefined) matches.push(column);
	}
	return matches;
}

export function firstReviewMatch(blocks: ReviewBlock[], query: string, from: number, direction: 1 | -1): number {
	if (!query.trim() || blocks.length === 0) return -1;
	for (let offset = 1; offset <= blocks.length; offset++) {
		const index = (from + direction * offset + blocks.length * 2) % blocks.length;
		if (reviewMatchColumns(blocks[index].text, query).length > 0) return index;
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

const reviewSegmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });
const reviewGraphemeCache = new Map<string, Array<{ start: number; end: number; text: string }>>();

function graphemeColumns(value: string): Array<{ start: number; end: number; text: string }> {
	const cached = reviewGraphemeCache.get(value);
	if (cached) return cached;
	const segments = [...reviewSegmenter.segment(value)];
	const result = segments.map((segment, index) => ({
		start: segment.index,
		end: segments[index + 1]?.index ?? value.length,
		text: segment.segment,
	}));
	if (reviewGraphemeCache.size >= maxReviewBlocks * 2) reviewGraphemeCache.clear();
	reviewGraphemeCache.set(value, result);
	return result;
}

export function isReviewColumnBoundary(line: string, column: number): boolean {
	return Number.isInteger(column) && column >= 0 && column <= line.length
		&& (column === line.length || graphemeColumns(line).some(grapheme => grapheme.start === column));
}

function clampColumn(line: string, column: number): number {
	column = Math.max(0, Math.min(Math.floor(column || 0), line.length));
	let result = 0;
	for (const grapheme of graphemeColumns(line)) {
		if (grapheme.start > column) break;
		result = grapheme.start;
	}
	return result;
}

function nextColumn(line: string, column: number): number {
	for (const grapheme of graphemeColumns(line)) if (grapheme.start > column) return grapheme.start;
	return clampColumn(line, column);
}

function endOfGrapheme(line: string, column: number): number {
	for (const grapheme of graphemeColumns(line)) if (grapheme.start >= column) return grapheme.end;
	return line.length;
}

function previousColumn(line: string, column: number): number {
	let result = 0;
	for (const grapheme of graphemeColumns(line)) {
		if (grapheme.start >= column) break;
		result = grapheme.start;
	}
	return result;
}

function comparePoint(leftLine: number, leftColumn: number, rightLine: number, rightColumn: number): number {
	return leftLine - rightLine || leftColumn - rightColumn;
}

function itemRange(lines: ReviewBlock[], item: ReviewItem): ReviewRange {
	const start = Math.max(0, Math.min(item.start, lines.length - 1));
	const end = Math.max(start, Math.min(item.end, lines.length - 1));
	return {
		start,
		end,
		startColumn: Math.max(0, Math.min(item.startColumn ?? 0, lines[start]?.text.length ?? 0)),
		endColumn: Math.max(0, Math.min(item.endColumn ?? lines[end]?.text.length ?? 0, lines[end]?.text.length ?? 0)),
	};
}

function visualRange(lines: ReviewBlock[], state: ReviewViewState): ReviewRange | undefined {
	if (state.anchor === undefined || !state.visualMode || lines.length === 0) return undefined;
	const cursor = Math.max(0, Math.min(state.cursor, lines.length - 1));
	const anchor = Math.max(0, Math.min(state.anchor, lines.length - 1));
	if (state.visualMode === "line") {
		const start = Math.min(cursor, anchor);
		const end = Math.max(cursor, anchor);
		return { start, end, startColumn: 0, endColumn: lines[end].text.length };
	}
	const cursorColumn = clampColumn(lines[cursor].text, state.cursorColumn ?? 0);
	const anchorColumn = clampColumn(lines[anchor].text, state.anchorColumn ?? 0);
	const firstIsAnchor = comparePoint(anchor, anchorColumn, cursor, cursorColumn) <= 0;
	const firstLine = firstIsAnchor ? anchor : cursor;
	const firstColumn = firstIsAnchor ? anchorColumn : cursorColumn;
	const lastLine = firstIsAnchor ? cursor : anchor;
	const lastColumn = firstIsAnchor ? cursorColumn : anchorColumn;
	return {
		start: firstLine,
		end: lastLine,
		startColumn: firstColumn,
		endColumn: endOfGrapheme(lines[lastLine].text, lastColumn),
	};
}

function positionSelected(range: ReviewRange | undefined, line: number, startColumn: number, endColumn: number): boolean {
	if (!range || line < range.start || line > range.end) return false;
	const rangeStart = line === range.start ? range.startColumn : 0;
	const rangeEnd = line === range.end ? range.endColumn : Number.MAX_SAFE_INTEGER;
	return endColumn > rangeStart && startColumn < rangeEnd;
}

function syntaxStyle(theme: any, line: ReviewBlock, text: string, column: number): string {
	if (line.kind === "heading") return theme.fg("accent", theme.bold(text));
	if (line.kind === "fence") return theme.fg("dim", text);
	if (line.kind === "code") return theme.fg("text", text);
	if (line.kind === "quote") return theme.fg(column < (line.text.match(/^\s*>\s?/)?.[0].length ?? 0) ? "accent" : "muted", text);
	if (line.kind === "list") return theme.fg(column < (line.text.match(/^\s*(?:[-+*]|\d+[.)])\s+/)?.[0].length ?? 0) ? "accent" : "text", text);
	if (line.kind === "table") return theme.fg(text === "|" ? "accent" : "text", text);
	if ("*_`[]()~".includes(text)) return theme.fg("dim", text);
	return theme.fg("text", text);
}

function visibleSourceRows(
	lines: ReviewBlock[],
	items: ReviewItem[],
	state: ReviewViewState,
	width: number,
	height: number,
	theme: any,
): string[] {
	if (lines.length === 0) return Array(Math.max(0, height)).fill("");
	const cursorLine = Math.max(0, Math.min(state.cursor, lines.length - 1));
	const cursorColumn = clampColumn(lines[cursorLine].text, state.cursorColumn ?? 0);
	state.cursor = cursorLine;
	state.cursorColumn = cursorColumn;
	let top = Math.max(0, Math.min(state.sourceTopLine ?? 0, Math.max(0, lines.length - height)));
	if (cursorLine < top) top = cursorLine;
	else if (cursorLine >= top + height) top = cursorLine - height + 1;
	state.sourceTopLine = Math.max(0, Math.min(top, Math.max(0, lines.length - height)));
	const cursorCell = visibleWidth(lines[cursorLine].text.slice(0, cursorColumn));
	let left = Math.max(0, state.sourceLeftColumn ?? 0);
	if (cursorCell < left) left = cursorCell;
	else if (cursorCell >= left + width) left = cursorCell - width + 1;
	state.sourceLeftColumn = Math.max(0, left);
	const annotationRanges = items.map(item => itemRange(lines, item));
	const range = state.focus === "items" && items[state.itemCursor]
		? annotationRanges[state.itemCursor]
		: visualRange(lines, state);
	const output: string[] = [];
	for (let index = state.sourceTopLine; index < Math.min(lines.length, state.sourceTopLine + height); index++) {
		const line = lines[index];
		let styled = "";
		let cell = 0;
		let renderStartCell = state.sourceLeftColumn;
		let started = false;
		for (const grapheme of graphemeColumns(line.text)) {
			const graphemeWidth = visibleWidth(grapheme.text);
			if (cell + graphemeWidth <= state.sourceLeftColumn) {
				cell += graphemeWidth;
				continue;
			}
			if (cell >= state.sourceLeftColumn + width) break;
			if (!started) {
				started = true;
				renderStartCell = cell;
			}
			const annotated = annotationRanges.some(annotation => positionSelected(annotation, index, grapheme.start, grapheme.end));
			let value = annotated ? theme.fg("warning", grapheme.text) : syntaxStyle(theme, line, grapheme.text, grapheme.start);
			if (positionSelected(range, index, grapheme.start, grapheme.end)) value = theme.bg("selectedBg", value);
			if (state.focus === "source" && index === cursorLine && grapheme.start === cursorColumn) value = theme.bg("selectedBg", theme.bold(value));
			styled += value;
			cell += graphemeWidth;
		}
		if (!line.text && range && index >= range.start && index <= range.end) styled += theme.bg("selectedBg", " ");
		else if (state.focus === "source" && index === cursorLine && !line.text) styled += theme.bg("selectedBg", " ");
		const relativeLeft = Math.max(0, state.sourceLeftColumn - renderStartCell);
		output.push(sliceByColumn(styled, relativeLeft, relativeLeft + width, true));
	}
	while (output.length < height) output.push("");
	return output;
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
): string[] {
	width = Math.max(1, width);
	bodyHeight = Math.max(1, Math.floor(bodyHeight));
	const sourceTitle = state.focus === "source" ? theme.fg("accent", theme.bold("RESPONSE")) : theme.fg("muted", theme.bold("RESPONSE"));
	const itemsTitle = state.focus === "items" ? theme.fg("accent", theme.bold("ANNOTATIONS")) : theme.fg("muted", theme.bold("ANNOTATIONS"));
	if (width >= 108) {
		const leftWidth = Math.floor(width * 0.68);
		const rightWidth = width - leftWidth;
		const left = [sourceTitle, ...visibleSourceRows(blocks, items, state, leftWidth, bodyHeight, theme)];
		const right = [itemsTitle, ...itemRows(items, state, rightWidth, bodyHeight, theme)];
		return joinReviewColumns(left, right, leftWidth, rightWidth).map(line => truncateToWidth(line, width, "…"));
	}
	if (state.focus === "items") return [itemsTitle, ...itemRows(items, state, width, bodyHeight, theme)].map(line => truncateToWidth(line, width, "…"));
	return [sourceTitle, ...visibleSourceRows(blocks, items, state, width, bodyHeight, theme)].map(line => truncateToWidth(line, width, "…"));
}

export class ReviewMode {
	private inputMode: ReviewInputMode = "normal";
	private pendingKey = "";
	private notice = "";
	private editingItem: number | undefined;
	private readonly searchInput = new Input();
	private commentEditor: Editor | undefined;
	private readonly tui: any;
	private readonly editorTheme: EditorTheme;
	private readonly confirmFinish: boolean;
	private readonly onItemsChanged: (items: ReviewItem[]) => void;
	private readonly onEditingChanged: (editing: ReviewEditingDraft | undefined) => void;
	private readonly makeID: () => string;
	private readonly itemHistory: ReviewItem[][] = [];
	private editingSaveTimer: NodeJS.Timeout | undefined;
	private lastItemsPaneWidth = 80;
	private preferredColumn: number | undefined;
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
		this.tui = options.tui ?? { requestRender: this.onRender, terminal: { rows: 24, columns: 80 } };
		this.editorTheme = {
			borderColor: (text: string) => this.theme.fg("accent", text),
			selectList: {
				selectedPrefix: (text: string) => this.theme.fg("accent", text),
				selectedText: (text: string) => this.theme.fg("accent", text),
				description: (text: string) => this.theme.fg("muted", text),
				scrollInfo: (text: string) => this.theme.fg("dim", text),
				noMatch: (text: string) => this.theme.fg("warning", text),
			},
		};
		this.onItemsChanged = options.onItemsChanged ?? (() => {});
		this.onEditingChanged = options.onEditingChanged ?? (() => {});
		this.makeID = options.makeID ?? (() => `${Date.now()}-${Math.random().toString(16).slice(2)}`);
		this.confirmFinish = options.confirmFinish === true;
		this.searchInput.onSubmit = value => this.applySearch(value);
		this.searchInput.onEscape = () => this.leaveInputMode();
		this.clamp();
		if (options.editing) this.restoreEditing(options.editing);
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
		if (this.commentEditor) this.commentEditor.focused = this._focused && this.inputMode === "comment";
	}

	private refresh() {
		this.syncInputFocus();
		this.onRender();
	}

	private currentBodyHeight(): number {
		const value = typeof this.bodyHeight === "function" ? this.bodyHeight() : this.bodyHeight;
		return Number.isFinite(value) ? Math.max(1, Math.floor(value)) : 24;
	}

	private itemRowCount(index: number): number {
		const item = this.items[index];
		if (!item) return 1;
		const contentWidth = Math.max(1, this.lastItemsPaneWidth - 4);
		return 1 + item.comment.split("\n").flatMap(value => wrapPlainLine(value, contentWidth)).length;
	}

	private clamp() {
		this.state.cursor = Math.max(0, Math.min(this.state.cursor, Math.max(0, this.blocks.length - 1)));
		this.state.cursorColumn = clampColumn(this.blocks[this.state.cursor]?.text ?? "", this.state.cursorColumn ?? 0);
		if (this.state.anchor !== undefined) {
			this.state.anchor = Math.max(0, Math.min(this.state.anchor, Math.max(0, this.blocks.length - 1)));
			this.state.anchorColumn = clampColumn(this.blocks[this.state.anchor]?.text ?? "", this.state.anchorColumn ?? 0);
		}
		this.state.itemCursor = Math.max(0, Math.min(this.state.itemCursor, Math.max(0, this.items.length - 1)));
		this.state.itemRowOffset = Math.max(0, Math.min(this.state.itemRowOffset ?? 0, this.itemRowCount(this.state.itemCursor) - 1));
		if (this.items.length === 0 && this.state.focus === "items") this.state.focus = "source";
	}

	private setNotice(message: string) {
		this.notice = sanitizeReviewText(message).replace(/\n+/g, " ").trim();
		this.refresh();
	}

	private selection(): ReviewRange {
		const visual = visualRange(this.blocks, this.state);
		if (visual) return visual;
		if (this.state.focus === "items" && this.items[this.state.itemCursor]) return itemRange(this.blocks, this.items[this.state.itemCursor]);
		const line = this.state.cursor;
		return { start: line, end: line, startColumn: 0, endColumn: this.blocks[line]?.text.length ?? 0 };
	}

	private clearVisual() {
		this.state.anchor = undefined;
		this.state.anchorColumn = undefined;
		this.state.visualMode = undefined;
	}

	private revealItem() {
		const item = this.items[this.state.itemCursor];
		if (!item) return;
		this.state.cursor = item.start;
		this.state.cursorColumn = item.startColumn ?? 0;
		this.clearVisual();
	}

	private moveSourceLines(amount: number) {
		if (this.preferredColumn === undefined) this.preferredColumn = this.state.cursorColumn ?? 0;
		this.state.cursor = Math.max(0, Math.min(this.state.cursor + amount, this.blocks.length - 1));
		this.state.cursorColumn = clampColumn(this.blocks[this.state.cursor]?.text ?? "", this.preferredColumn);
	}

	private moveHorizontal(direction: -1 | 1) {
		const line = this.blocks[this.state.cursor]?.text ?? "";
		const column = this.state.cursorColumn ?? 0;
		this.state.cursorColumn = direction > 0 ? nextColumn(line, column) : previousColumn(line, column);
		this.preferredColumn = undefined;
		this.notice = "";
		this.refresh();
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
		if (this.state.focus === "source") this.moveSourceLines(amount);
		else this.moveItemRows(amount);
		this.clamp();
		if (this.state.focus === "items") this.revealItem();
		this.refresh();
	}

	private moveMatch(direction: 1 | -1) {
		if (!this.state.query.trim()) return;
		for (let offset = 0; offset <= this.blocks.length; offset++) {
			const line = (this.state.cursor + direction * offset + this.blocks.length * 2) % this.blocks.length;
			let columns = reviewMatchColumns(this.blocks[line].text, this.state.query);
			if (offset === 0) {
				const cursorColumn = this.state.cursorColumn ?? 0;
				columns = columns.filter(column => direction > 0 ? column > cursorColumn : column < cursorColumn);
			}
			const column = direction > 0 ? columns[0] : columns[columns.length - 1];
			if (column === undefined) continue;
			this.state.cursor = line;
			this.state.cursorColumn = column;
			this.state.focus = "source";
			this.clearVisual();
			this.notice = "";
			this.preferredColumn = undefined;
			this.refresh();
			return;
		}
		this.notice = `No response text matches “${this.state.query}”.`;
		this.refresh();
	}

	private moveAnnotation(direction: 1 | -1) {
		if (this.items.length === 0) return this.setNotice("There are no annotations.");
		const candidates = this.items.map((item, index) => ({ item, index }));
		const ordered = direction > 0
			? candidates.sort((left, right) => comparePoint(left.item.start, left.item.startColumn ?? 0, right.item.start, right.item.startColumn ?? 0) || left.index - right.index)
			: candidates.sort((left, right) => comparePoint(right.item.end, right.item.endColumn ?? 0, left.item.end, left.item.endColumn ?? 0) || left.index - right.index);
		const target = ordered.find(candidate => direction > 0
			? comparePoint(candidate.item.start, candidate.item.startColumn ?? 0, this.state.cursor, this.state.cursorColumn ?? 0) > 0
			: comparePoint(candidate.item.end, candidate.item.endColumn ?? 0, this.state.cursor, this.state.cursorColumn ?? 0) < 0) ?? ordered[0];
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

	private editingDraft(): ReviewEditingDraft | undefined {
		if (this.inputMode !== "comment" || !this.commentEditor) return undefined;
		const range = this.editingItem === undefined ? this.selection() : this.items[this.editingItem];
		if (!range) return undefined;
		return {
			kind: this.editingItem === undefined ? "new" : "edit",
			...(this.editingItem === undefined ? {} : { itemId: this.items[this.editingItem]?.id }),
			start: range.start,
			end: range.end,
			startColumn: range.startColumn,
			endColumn: range.endColumn,
			...(this.editingItem === undefined ? { visualMode: this.state.visualMode ?? "line" as const } : {}),
			buffer: sanitizeReviewText(this.commentEditor.getText()),
		};
	}

	private flushEditingDraft() {
		if (this.editingSaveTimer) clearTimeout(this.editingSaveTimer);
		this.editingSaveTimer = undefined;
		const editing = this.editingDraft();
		if (!editing) return;
		if (new TextEncoder().encode(editing.buffer).byteLength > maxReviewDraftBytes) {
			this.onEditingChanged(undefined);
			return;
		}
		this.onEditingChanged(editing);
	}

	private queueEditingDraft() {
		if (this.editingSaveTimer) clearTimeout(this.editingSaveTimer);
		this.editingSaveTimer = setTimeout(() => this.flushEditingDraft(), 750);
	}

	private createCommentEditor(text: string) {
		const editor = new Editor(this.tui, this.editorTheme);
		editor.setText(text);
		editor.onSubmit = value => this.saveComment(value);
		editor.onChange = () => this.queueEditingDraft();
		this.commentEditor = editor;
	}

	private restoreEditing(editing: ReviewEditingDraft) {
		if (editing.kind === "edit") {
			const index = this.items.findIndex(item => item.id === editing.itemId);
			if (index < 0) return;
			this.editingItem = index;
			this.state.itemCursor = index;
			this.state.focus = "items";
		} else {
			this.editingItem = undefined;
			this.state.cursor = editing.end;
			const endColumn = editing.endColumn ?? this.blocks[editing.end]?.text.length ?? 0;
			this.state.cursorColumn = endColumn > 0 ? previousColumn(this.blocks[editing.end]?.text ?? "", endColumn) : 0;
			this.state.anchor = editing.start;
			this.state.anchorColumn = editing.startColumn ?? 0;
			this.state.visualMode = editing.visualMode === "character" ? "character" : "line";
			this.state.focus = "source";
		}
		this.inputMode = "comment";
		this.createCommentEditor(editing.buffer);
	}

	private enterComment(item?: number) {
		if (item === undefined && this.items.length >= maxReviewItems) return this.setNotice(`A review can contain at most ${maxReviewItems} annotations.`);
		this.editingItem = item;
		this.inputMode = "comment";
		this.createCommentEditor(item === undefined ? "" : this.items[item]?.comment ?? "");
		this.pendingKey = "";
		this.notice = "";
		this.queueEditingDraft();
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
			const quote = reviewSelection(this.blocks, range.start, range.end, range.startColumn, range.endColumn);
			if (!quote) return this.setNotice("Select text before you add a comment.");
			if (new TextEncoder().encode(quote).byteLength > maxReviewSelectionBytes) return this.setNotice("The selected passage is too large. Select less text.");
			next = [...this.items, { id: this.makeID(), ...range, quote, comment }];
		}
		if (new TextEncoder().encode(compileReview(next)).byteLength > maxReviewDraftBytes) return this.setNotice("The review draft is too large.");
		this.rememberItems();
		this.items = next;
		this.state.itemCursor = this.editingItem ?? next.length - 1;
		this.state.itemRowOffset = 0;
		this.clearVisual();
		this.inputMode = "normal";
		this.editingItem = undefined;
		if (this.editingSaveTimer) clearTimeout(this.editingSaveTimer);
		this.editingSaveTimer = undefined;
		this.commentEditor = undefined;
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
		this.clearVisual();
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
		const wasEditing = this.inputMode === "comment";
		if (this.editingSaveTimer) clearTimeout(this.editingSaveTimer);
		this.editingSaveTimer = undefined;
		this.inputMode = "normal";
		this.editingItem = undefined;
		this.commentEditor = undefined;
		if (wasEditing) this.onEditingChanged(undefined);
		this.notice = "";
		this.refresh();
	}

	private handlePending(data: string): boolean {
		if (!this.pendingKey) return false;
		const pending = this.pendingKey;
		this.pendingKey = "";
		if (pending === "g" && data === "g") {
			if (this.state.focus === "source") {
				this.state.cursor = 0;
				this.state.cursorColumn = 0;
				this.preferredColumn = undefined;
			} else {
				this.state.itemCursor = 0;
				this.state.itemRowOffset = 0;
				this.revealItem();
			}
			this.clearVisual();
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
			this.commentEditor?.handleInput(data);
			this.refresh();
			return;
		}
		if (matchesKey(data, Key.escape)) {
			this.pendingKey = "";
			this.notice = "";
			if (this.state.anchor !== undefined) this.clearVisual();
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
		const pending = ["g", "]", "[", "d"].find(key => data === key || (key !== "g" && isReviewKey(data, key)));
		if (pending && (pending !== "d" || this.state.focus === "items")) {
			this.pendingKey = pending;
			this.refresh();
			return;
		}
		if (matchesKey(data, Key.tab)) {
			if (this.items.length > 0) {
				this.state.focus = this.state.focus === "source" ? "items" : "source";
				this.clearVisual();
				if (this.state.focus === "items") {
					this.state.itemRowOffset = 0;
					this.revealItem();
				}
			}
			this.refresh();
			return;
		}
		if (this.state.focus === "source" && (matchesKey(data, Key.left) || isReviewKey(data, "h"))) return this.moveHorizontal(-1);
		if (this.state.focus === "source" && (matchesKey(data, Key.right) || isReviewKey(data, "l"))) return this.moveHorizontal(1);
		if (data === "n") return this.moveMatch(1);
		if (data === "N") return this.moveMatch(-1);
		if (isReviewKey(data, "u")) return this.undoItems();
		if (matchesKey(data, Key.up) || isReviewKey(data, "k")) return this.move(-1);
		if (matchesKey(data, Key.down) || isReviewKey(data, "j")) return this.move(1);
		if (matchesKey(data, Key.ctrl("u")) || matchesKey(data, "pageUp")) return this.move(-Math.max(5, Math.floor(this.currentBodyHeight() / 2)));
		if (matchesKey(data, Key.ctrl("d")) || matchesKey(data, "pageDown")) return this.move(Math.max(5, Math.floor(this.currentBodyHeight() / 2)));
		if (isReviewKey(data, "0") && this.state.focus === "source") {
			this.state.cursorColumn = 0;
			this.preferredColumn = undefined;
			this.refresh();
			return;
		}
		if (isReviewKey(data, "$") && this.state.focus === "source") {
			const line = this.blocks[this.state.cursor]?.text ?? "";
			this.state.cursorColumn = clampColumn(line, line.length);
			this.preferredColumn = undefined;
			this.refresh();
			return;
		}
		if (data === "G") {
			if (this.state.focus === "source") {
				this.state.cursor = Math.max(0, this.blocks.length - 1);
				this.state.cursorColumn = clampColumn(this.blocks[this.state.cursor]?.text ?? "", this.preferredColumn ?? 0);
			} else {
				this.state.itemCursor = Math.max(0, this.items.length - 1);
				this.state.itemRowOffset = this.itemRowCount(this.state.itemCursor) - 1;
				this.revealItem();
			}
			this.refresh();
			return;
		}
		if ((data === "v" || data === "V") && this.state.focus === "source") {
			const mode = data === "V" ? "line" : "character";
			if (this.state.visualMode === mode && this.state.anchor !== undefined) this.clearVisual();
			else {
				if (this.state.anchor === undefined) {
					this.state.anchor = this.state.cursor;
					this.state.anchorColumn = this.state.cursorColumn ?? 0;
				}
				this.state.visualMode = mode;
			}
			this.notice = "";
			this.refresh();
			return;
		}
		if (isReviewKey(data, "o") && this.state.focus === "source" && this.state.anchor !== undefined) {
			const cursor = this.state.cursor;
			const cursorColumn = this.state.cursorColumn ?? 0;
			this.state.cursor = this.state.anchor;
			this.state.cursorColumn = this.state.anchorColumn ?? 0;
			this.state.anchor = cursor;
			this.state.anchorColumn = cursorColumn;
			this.preferredColumn = undefined;
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
		if (this.inputMode === "comment" && this.commentEditor) {
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
		const sourcePaneWidth = width >= 108 ? Math.floor(width * 0.68) : width;
		this.lastItemsPaneWidth = width >= 108 ? width - sourcePaneWidth : width;
		this.clamp();
		const totalHeight = this.currentBodyHeight();
		const visual = this.state.anchor !== undefined;
		const mode = this.inputMode === "search" ? "SEARCH" : this.inputMode === "comment" ? "COMMENT" : this.inputMode === "confirm" ? "CONFIRM" : visual ? this.state.visualMode === "line" ? "VISUAL LINE" : "VISUAL" : "NORMAL";
		const modeColor = mode === "COMMENT" ? "success" : mode.startsWith("VISUAL") || mode === "CONFIRM" ? "warning" : "accent";
		const meta = `${this.blocks.length} lines · ${this.items.length} annotation${this.items.length === 1 ? "" : "s"} · ${this.state.cursor + 1}:${(this.state.cursorColumn ?? 0) + 1}${this.state.query ? ` · /${this.state.query}` : ""}`;
		const title = padReviewLine(`${this.theme.fg("accent", this.theme.bold("GALPÓN REVIEW"))}  ${this.theme.fg(modeColor, mode)}  ${this.theme.fg("dim", meta)}`, width);
		const command = this.pendingKey ? `${this.pendingKey}_` : this.notice;
		const help = this.state.focus === "items"
			? "j/k move · Enter/e edit · x/dd delete · u undo · Tab response · s prepare · q close"
			: "h/j/k/l move · 0/$ line · gg/G ends · v chars · V lines · o swap · c comment · / search · Tab pane · s prepare · q close";
		const statusText = truncateToWidth(command || help, width, "…");
		const status = this.theme.bg("selectedBg", padReviewLine(this.theme.fg(command ? "text" : "dim", statusText), width));
		if (totalHeight === 1) return [padReviewLine(status, width)];
		const titleLine = truncateToWidth(title, width, "…");
		const available = Math.max(0, totalHeight - 2);
		const input = this.renderInput(width, available);
		const separatorRows = input.length > 0 && available - input.length >= 2 ? 1 : 0;
		const mainRows = Math.max(0, available - input.length - separatorRows);
		const main = mainRows > 0
			? renderReviewMode(this.blocks, this.items, this.state, width, Math.max(1, mainRows - 1), this.theme).slice(0, mainRows)
			: [];
		const content = [titleLine, ...main, ...(separatorRows ? [""] : []), ...input];
		while (content.length < totalHeight - 1) content.push("");
		return [...content.slice(0, totalHeight - 1), status].map(line => padReviewLine(line, width));
	}

	invalidate() {
		this.searchInput.invalidate();
		this.commentEditor?.invalidate();
	}

	dispose() {
		this.flushEditingDraft();
	}
}
