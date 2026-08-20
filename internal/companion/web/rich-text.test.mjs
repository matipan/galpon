import test from "node:test";
import assert from "node:assert/strict";
import { parseInline, parseRichText } from "./rich-text.mjs";

test("rich text keeps paragraphs, bounded language labels, lists, and code blocks", () => {
  assert.deepEqual(parseRichText("First `name`\n\n- one\n- two\n\n```javascript\nconst safe = true;\n```"), [
    { kind: "paragraph", content: [{ kind: "text", text: "First " }, { kind: "code", text: "name" }] },
    { kind: "list", items: [[{ kind: "text", text: "one" }], [{ kind: "text", text: "two" }]] },
    { kind: "code", language: "javascript", text: "const safe = true;" },
  ]);
});

test("markdown images stay typed for an authenticated attachment resolver", () => {
  assert.deepEqual(parseInline("Result: ![Mobile preview](.artifacts/mobile-preview.png)"), [
    { kind: "text", text: "Result: " },
    { kind: "image", text: "Mobile preview", reference: ".artifacts/mobile-preview.png", raw: "![Mobile preview](.artifacts/mobile-preview.png)" },
  ]);
});

test("inline links allow only absolute HTTP URLs", () => {
  assert.deepEqual(parseInline("[safe](https://example.test/path) [bad](javascript:alert(1))"), [
    { kind: "link", text: "safe", url: "https://example.test/path" },
    { kind: "text", text: " " },
    { kind: "text", text: "[bad](javascript:alert(1)" },
    { kind: "text", text: ")" },
  ]);
});

test("an unfinished code fence stays escaped code text", () => {
  assert.deepEqual(parseRichText("```html\n<script>alert(1)</script>"), [
    { kind: "code", language: "html", text: "<script>alert(1)</script>" },
  ]);
});
