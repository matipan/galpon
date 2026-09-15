import { writeFileSync } from "node:fs";
import {
	compileReview,
	isReviewColumnBoundary,
	legacyReviewOffset,
	legacyReviewSelection,
	maxReviewBlocks,
	maxReviewDraftBytes,
	maxReviewItems,
	maxReviewSelectionBytes,
	maxReviewSourceBytes,
	parseReviewBlocks,
	parseReviewBuffer,
	reviewDraftEvent,
	reviewParserVersion,
	reviewSelection,
	sanitizeReviewText,
	type ReviewItem,
} from "../galpon-review.ts";

function assert(value: unknown, message: string): asserts value {
	if (!value) throw new Error(message);
}

function equal<T>(actual: T, expected: T, message: string) {
	if (actual !== expected) throw new Error(`${message}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
}

function testContracts() {
	equal(reviewDraftEvent, "galpon:review:draft:v1", "review draft event changed");
	equal(reviewParserVersion, 3, "review parser version changed");
	equal(maxReviewItems, 32, "review item limit changed");
	equal(maxReviewSelectionBytes, 24 * 1024, "review selection limit changed");
	equal(maxReviewDraftBytes, 128 * 1024, "review draft limit changed");
	equal(maxReviewSourceBytes, 512 * 1024, "review source limit changed");
	equal(maxReviewBlocks, 2048, "review block limit changed");
}

function testNormalizationAndLineParsing() {
	const raw = "\u001b[31m# Title\u001b[0m\r\n\r\n  exact\tspacing  \rnext\u0000 line\r\n";
	const normalized = "# Title\n\n  exact    spacing  \nnext line\n";
	equal(sanitizeReviewText(raw), normalized, "source normalization changed whitespace");
	const lines = parseReviewBuffer(raw);
	equal(lines.map(line => line.text).join("\n"), normalized, "line parser did not preserve normalized source");
	equal(lines.length, 5, "line parser did not preserve final blank line");
	equal(lines[0].startOffset, 0, "first line start offset is wrong");
	equal(lines[1].startOffset, 8, "CRLF was not normalized before offsets were calculated");
	equal(lines[2].endOffset, 29, "line end offset is wrong after tab normalization");

	const markdown = [
		"# ATX",
		"Setext",
		"------",
		"- bullet",
		"1. ordered",
		"```ts",
		"- code, not a list",
		"```",
		"> quote",
		"| cell |",
	].join("\r\n");
	const parsed = parseReviewBuffer(markdown);
	equal(parsed.map(line => line.kind).join(","), "heading,heading,heading,list,list,fence,code,fence,quote,table", "Markdown line classification changed");
	equal(parsed.map(line => line.text).join("\n"), markdown.replace(/\r\n/g, "\n"), "Markdown parser changed source text");
}

function testLegacyMigrationParsing() {
	const markdown = [
		"# Deployment plan",
		"",
		"Body text",
		"continued.",
		"",
		"- first",
		"- second",
		"",
		"```sh",
		"echo ready",
		"```",
	].join("\r\n");
	const blocks = parseReviewBlocks(markdown);
	equal(blocks.map(block => block.text).join("|"), "# Deployment plan|Body text\ncontinued.|- first|- second|```sh\necho ready\n```", "legacy block grouping changed");
	equal(legacyReviewSelection(blocks, 2, 3), "- first\n\n- second", "legacy block selection changed");

	const setext = parseReviewBlocks("Rendered title\r\n=\r\nBody");
	equal(setext.length, 2, "legacy setext heading count changed");
	equal(setext[0].text, "Rendered title\n=", "legacy setext heading grouping changed");

	const duplicateSource = "prefix target\n\ntarget";
	const duplicate = parseReviewBlocks(duplicateSource);
	equal(duplicate[1].startOffset, duplicateSource.lastIndexOf("target"), "legacy parser matched text inside an earlier line");

	const joinedSource = "👨‍👩‍👧 family\r\n\r\nAfter";
	const joined = parseReviewBlocks(joinedSource);
	const legacyAfter = joined[1].startOffset ?? -1;
	equal(legacyReviewOffset(joinedSource, legacyAfter), sanitizeReviewText(joinedSource).indexOf("After"), "legacy joiner offset conversion changed");
	equal(legacyReviewOffset(joinedSource, Number.POSITIVE_INFINITY), sanitizeReviewText(joinedSource).length, "legacy offset upper clamp changed");
	equal(legacyReviewOffset(joinedSource, -10), 0, "legacy offset lower clamp changed");
}

function testGraphemeBoundariesAndQuotes() {
	const family = "👨‍👩‍👧‍👦";
	const combined = "e\u0301";
	const line = `A${family} ${combined}B`;
	const familyStart = 1;
	const familyEnd = familyStart + family.length;
	const combinedStart = familyEnd + 1;
	assert(isReviewColumnBoundary(line, 0), "zero is not a grapheme boundary");
	assert(isReviewColumnBoundary(line, familyStart), "joined emoji start is not a grapheme boundary");
	assert(isReviewColumnBoundary(line, familyEnd), "joined emoji end is not a grapheme boundary");
	assert(!isReviewColumnBoundary(line, familyStart + 2), "joined emoji can be split at a UTF-16 joiner");
	assert(isReviewColumnBoundary(line, combinedStart), "combined grapheme start is not a boundary");
	assert(!isReviewColumnBoundary(line, combinedStart + 1), "combining mark can be split");
	assert(isReviewColumnBoundary(line, line.length), "line end is not a grapheme boundary");
	assert(!isReviewColumnBoundary(line, 0.5), "fractional UTF-16 column was accepted");

	const source = parseReviewBuffer("  first  \n\nsecond tail  ");
	equal(reviewSelection(source, 0, 2, 2, 6), "first  \n\nsecond", "multiline quote changed exact whitespace");
	equal(reviewSelection(source, 2, 0, 6, 2), "first  \n\nsecond", "reversed selection changed its quote");
	equal(reviewSelection(parseReviewBuffer(line), 0, 0, familyStart, familyEnd), family, "joined emoji quote was not exact");
}

function testCompilation() {
	const items: ReviewItem[] = [
		{
			id: "private-id",
			start: 0,
			end: 2,
			startColumn: 0,
			endColumn: 6,
			quote: "  exact text  \n\nsecond",
			comment: "  Keep this exact selection.  ",
		},
		{
			id: "private-id-2",
			start: 4,
			end: 4,
			quote: "final",
			comment: "Explain why.",
		},
	];
	const expected = [
		"I reviewed this response. Here is my feedback:",
		"",
		"### 1",
		"",
		">   exact text  ",
		">",
		"> second",
		"",
		"Keep this exact selection.",
		"",
		"### 2",
		"",
		"> final",
		"",
		"Explain why.",
	].join("\n");
	const compiled = compileReview(items);
	equal(compiled, expected, "compiled review changed quotes or comments");
	assert(!compiled.includes("private-id"), "compiled review exposed an internal item ID");
}

function run() {
	testContracts();
	testNormalizationAndLineParsing();
	testLegacyMigrationParsing();
	testGraphemeBoundariesAndQuotes();
	testCompilation();
}

export default async function () {
	const resultPath = process.env.GALPON_REVIEW_DATA_TEST_RESULT;
	try {
		run();
		if (resultPath) writeFileSync(resultPath, JSON.stringify({ ok: true }), { mode: 0o600 });
	} catch (error) {
		const message = error instanceof Error ? error.message : String(error);
		if (resultPath) writeFileSync(resultPath, JSON.stringify({ ok: false, error: message }), { mode: 0o600 });
		throw error;
	}
}
