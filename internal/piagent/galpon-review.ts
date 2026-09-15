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

// Parser for persisted v1 and v2 drafts. Keep block grouping and text stable
// for stored indexes and quote hashes. Offsets refer to the legacy source,
// not to the trimmed text assembled for each block.
export function parseReviewBlocks(markdown: string): ReviewBlock[] {
	const source = sanitizeLegacyReviewText(markdown);
	let sourceOffset = 0;
	const lines = source.split("\n").map(text => {
		const startOffset = sourceOffset;
		sourceOffset += text.length + 1;
		return { text, startOffset };
	});
	const values: Array<Omit<ReviewBlock, "index">> = [];
	let current: Array<{ text: string; startOffset: number }> = [];
	let kind: "paragraph" | "list" | "table" | "fence" = "paragraph";
	let fence = "";
	const flush = () => {
		const text = current.map(line => line.text).join("\n").trim();
		if (text) {
			const first = current.find(line => line.text.trim());
			const last = [...current].reverse().find(line => line.text.trim());
			if (first && last) {
				values.push({
					text,
					startOffset: first.startOffset + first.text.length - first.text.trimStart().length,
					endOffset: last.startOffset + last.text.trimEnd().length,
				});
			}
		}
		current = [];
		kind = "paragraph";
	};

	for (const sourceLine of lines) {
		const line = sourceLine.text;
		const positioned = (text: string) => ({ text, startOffset: sourceLine.startOffset });
		if (kind === "fence") {
			current.push(positioned(line));
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
			current.push(positioned(line));
			continue;
		}
		if (!line.trim()) {
			flush();
			continue;
		}
		if (isSetextUnderline(line) && kind === "paragraph" && current.length > 0) {
			current.push(positioned(line.trimEnd()));
			flush();
			continue;
		}
		if (isATXHeading(line) || isSetextUnderline(line)) {
			flush();
			current.push(positioned(line));
			flush();
			continue;
		}
		if (isListItem(line)) {
			flush();
			kind = "list";
			current.push(positioned(line.trimEnd()));
			continue;
		}
		if (isTableLine(line)) {
			if (kind !== "table") {
				flush();
				kind = "table";
			}
			current.push(positioned(line.trimEnd()));
			continue;
		}
		if (kind === "table") flush();
		current.push(positioned(line.trimEnd()));
	}
	flush();

	return values.map((value, index) => ({ index, ...value }));
}

// Convert an offset produced by the v1/v2 parser, which removed joiners, to
// the equivalent UTF-16 offset in the current normalized source.
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

const reviewSegmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });
const reviewGraphemeCache = new Map<string, Array<{ start: number; end: number }>>();

function graphemeColumns(value: string): Array<{ start: number; end: number }> {
	const cached = reviewGraphemeCache.get(value);
	if (cached) return cached;
	const segments = [...reviewSegmenter.segment(value)];
	const result = segments.map((segment, index) => ({
		start: segment.index,
		end: segments[index + 1]?.index ?? value.length,
	}));
	if (reviewGraphemeCache.size >= maxReviewBlocks * 2) reviewGraphemeCache.clear();
	reviewGraphemeCache.set(value, result);
	return result;
}

export function isReviewColumnBoundary(line: string, column: number): boolean {
	return Number.isInteger(column) && column >= 0 && column <= line.length
		&& (column === line.length || graphemeColumns(line).some(grapheme => grapheme.start === column));
}
