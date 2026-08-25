import test from "node:test";
import assert from "node:assert/strict";
import { parseInline, parseRichText } from "./rich-text.mjs";

test("rich text keeps paragraphs, bounded language labels, lists, and code blocks", () => {
  assert.deepEqual(parseRichText("First `name`\n\n- one\n- two\n\n```javascript extra\nconst safe = true;\n```"), [
    { kind: "paragraph", content: [{ kind: "text", text: "First " }, { kind: "code", text: "name" }] },
    {
      kind: "list",
      ordered: false,
      start: 1,
      items: [
        { checked: null, content: [{ kind: "text", text: "one" }] },
        { checked: null, content: [{ kind: "text", text: "two" }] },
      ],
    },
    { kind: "code", language: "javascript", text: "const safe = true;" },
  ]);
});

test("GitHub tables keep alignment, inline formatting, and code pipes", () => {
  const blocks = parseRichText([
    "| Check | State | Notes |",
    "| :--- | :---: | ---: |",
    "| Unit | **Pass** | `180 | ok` |",
    "| Mobile | Pass | no overflow |",
  ].join("\n"));
  assert.equal(blocks.length, 1);
  assert.equal(blocks[0].kind, "table");
  assert.deepEqual(blocks[0].alignments, ["left", "center", "right"]);
  assert.deepEqual(blocks[0].rows[0][1], [{
    kind: "strong",
    content: [{ kind: "text", text: "Pass" }],
  }]);
  assert.deepEqual(blocks[0].rows[0][2], [{ kind: "code", text: "180 | ok" }]);
});

test("headings, quotes, ordered lists, tasks, and rules stay typed", () => {
  const blocks = parseRichText("## Results\n\n> Keep **HTML** escaped.\n\n3. First\n4. Second\n\n- [x] Done\n- [ ] Open\n\n---");
  assert.deepEqual(blocks.map((block) => block.kind), ["heading", "quote", "list", "list", "rule"]);
  assert.equal(blocks[0].level, 2);
  assert.equal(blocks[2].ordered, true);
  assert.equal(blocks[2].start, 3);
  assert.deepEqual(blocks[3].items.map((item) => item.checked), [true, false]);
});

test("inline emphasis, strong text, strike text, and autolinks stay typed", () => {
  const tokens = parseInline("Use **bold**, *care*, ~~old~~, and <https://example.test>.");
  assert.deepEqual(tokens.map((token) => token.kind), ["text", "strong", "text", "emphasis", "text", "strike", "text", "link", "text"]);
  assert.equal(tokens.find((token) => token.kind === "link").url, "https://example.test/");
});

test("markdown images stay typed for an authenticated attachment resolver", () => {
  assert.deepEqual(parseInline("Result: ![Mobile preview](.artifacts/mobile-preview.png)"), [
    { kind: "text", text: "Result: " },
    { kind: "image", text: "Mobile preview", reference: ".artifacts/mobile-preview.png", raw: "![Mobile preview](.artifacts/mobile-preview.png)" },
  ]);
});

test("inline links allow only absolute HTTP URLs", () => {
  assert.deepEqual(parseInline("[safe](https://example.test/path) [bad](javascript:alert(1))"), [
    { kind: "link", content: [{ kind: "text", text: "safe" }], url: "https://example.test/path" },
    { kind: "text", text: " [bad](javascript:alert(1))" },
  ]);
});

test("an unfinished code fence stays escaped code text", () => {
  assert.deepEqual(parseRichText("```html\n<script>alert(1)</script>"), [
    { kind: "code", language: "html", text: "<script>alert(1)</script>" },
  ]);
});
