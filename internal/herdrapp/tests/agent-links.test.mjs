import test from "node:test";
import assert from "node:assert/strict";
import {prettyLinks} from "../mods/agent/links.js";
import {splitMarkdown} from "../mods/agent/transcript.js";

const url = "https://github.com/dagger/dagger/pull/15132";
const pretty = '[dagger/dagger #15132](<https://github.com/dagger/dagger/pull/15132> "https://github.com/dagger/dagger/pull/15132")';

test("link labels keep exact destinations and surrounding punctuation", () => {
  assert.equal(prettyLinks(`See (${url}).`), `See (${pretty}).`);
  assert.equal(prettyLinks(`<${url}> [${url}](${url})`), `${pretty} ${pretty}`);
  assert.equal(prettyLinks(`**${url}** |${url}|`), `**${pretty}** |${pretty}|`);
  assert.equal(prettyLinks("https://example.com/a_b?q=one&sort=two"),
    '[example.com/a\\_b?q=one&amp;sort=two](<https://example.com/a_b?q=one&amp;sort=two> "https://example.com/a_b?q=one&amp;sort=two")');
  assert.equal(prettyLinks("https://example.com/wiki/Function_(mathematics)"),
    '[example.com/wiki/Function\\_(mathematics)](<https://example.com/wiki/Function_(mathematics)> "https://example.com/wiki/Function_(mathematics)")');
});

test("Markdown constructs retain their text while adjacent bare links are formatted", () => {
  const unchanged = [
    `\`${url}\``, `\`\`${url}\`\``, `[guide](${url})`, `[a [nested] label](${url})`,
    `![${url}](${url})`, `[guide][ref]\n\n[ref]: ${url}`, `    curl ${url}`, `\tcurl ${url}`,
    `<a href="${url}">guide</a>`, `<code>${url}</code>`, `<!-- ${url} -->`,
    `word${url}`, `\\${url}`, "https://user:password@example.com/private", "http://example.com/path",
  ];
  for (const text of unchanged) {
    assert.equal(prettyLinks(`${text}\n\nSee ${url}.`), `${text}\n\nSee ${pretty}.`, text);
  }
  for (const text of [`[unfinished ${url}`, `\`unfinished ${url}`, "https://"]) {
    assert.equal(prettyLinks(text), text);
  }
  const parts = splitMarkdown(`See ${url}\n\n\`\`\`sh\ncurl ${url}\n\`\`\``);
  assert.equal(prettyLinks(parts[0].text), `See ${pretty}\n`);
  assert.deepEqual(parts[1], {kind: "code", language: "sh", text: `curl ${url}`});
});

test("long delivery text remains complete around links and code", () => {
  const paragraphs = "The build completed. Review the result before starting the next task.\n".repeat(1200);
  assert.equal(prettyLinks(paragraphs), paragraphs);
  const suffix = `\n\`curl ${url}\`\n\n[Review](${url})\n`;
  assert.equal(prettyLinks(`${paragraphs}${url}${suffix}${paragraphs}${url}`),
    `${paragraphs}${pretty}${suffix}${paragraphs}${pretty}`);
});
