import assert from "node:assert/strict";
import test from "node:test";
import { companionTheme, contrast } from "./theme.mjs";

const themes = {
  fallback: undefined,
  "rose pine": {
    background: "#191724", foreground: "#e0def4", status: "#ebbcba",
    blue: "#9ccfd8", green: "#31748f", purple: "#c4a7e7", red: "#eb6f92", yellow: "#f6c177",
  },
  light: {
    background: "#faf4ed", foreground: "#575279", status: "#d7827e",
    blue: "#56949f", green: "#286983", purple: "#907aa9", red: "#b4637a", yellow: "#ea9d34",
  },
};

for (const [name, input] of Object.entries(themes)) {
  test(`${name} keeps text and state labels readable on the app surfaces`, () => {
    const { colors } = companionTheme(input);
    for (const text of ["foreground", "muted", "comment", "accent-text", "purple", "green", "red", "yellow"]) {
      for (const surface of ["background", "surface", "surface-raised", "side"]) {
        assert.ok(contrast(colors[text], colors[surface]) >= 4.5, `${text} on ${surface}`);
      }
    }
    assert.ok(contrast(colors.foreground, colors.selection) >= 4.5);
    assert.ok(contrast(colors["accent-ink"], colors.accent) >= 4.5);
    assert.ok(contrast(colors["status-ink"], colors.status) >= 4.5);
  });
}

test("host colors do not permit CSS expressions or arbitrary properties", () => {
  const { colors } = companionTheme({
    background: 'url("https://example.test/track")', foreground: "red; color: transparent",
    status: "#ABCDEF", "background-image": 'url("https://example.test/track")',
  });
  assert.equal(colors.background, companionTheme().colors.background);
  assert.equal(colors.foreground, companionTheme().colors.foreground);
  assert.equal(colors["background-image"], undefined);
  for (const value of Object.values(colors)) assert.match(value, /^#[0-9a-f]{6}$/);
});

test("a teal host green becomes a green completion mark, separate from work and links", () => {
  const { colors, scheme } = companionTheme(themes["rose pine"]);
  const parts = (value) => [1, 3, 5].map((offset) => parseInt(value.slice(offset, offset + 2), 16));
  const [r, g, b] = parts(colors.green);
  assert.ok(g > r && g > b, "completion uses a green hue");
  const [pr, pg, pb] = parts(colors.purple);
  assert.ok(pb > pg && pr > pg, "working uses a purple hue");
  assert.equal(scheme, "dark");
  assert.equal(companionTheme(themes.light).scheme, "light");
});
