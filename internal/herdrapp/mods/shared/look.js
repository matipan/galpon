// The Taller look of the Galpon mods. `galpon herdr-app install` copies this
// file and the tl-*.svg icons into each Galpon mod, so each mod imports
// "./look.js".
//
// Taller is a quiet workbench, after T3 Code and Cursor: neutral surfaces,
// hairline borders, Geist type, kbd chips and one accent.
//
// Colors come from the theme of the app on this computer (theme()), as the
// terminals do, so the mods sit on the same background as the terminals and
// follow a theme change. The rules:
//   - Neutrals mix the background with the foreground: surfaces, raised
//     controls, hairlines and three text levels.
//   - The accent marks only focus, the primary action and the row that the
//     window shows. It is never a status.
//   - States have one tone and one mark each: work is a purple dot that
//     fades out and in, needs you is a yellow dot, ready is a green dot
//     with a check, failed is a red x, idle is a quiet ring. Only work
//     moves.
//   - Green and purple must be true hues: a theme whose green is blue, such
//     as Rosé Pine, gets a green made in the style of its other colors, so
//     that ready does not look like the accent.
//   - Each color that holds text has at least 4.5:1 against the background,
//     and quiet text at least 3.6:1. A theme color below that moves toward
//     the foreground until it reaches it.

import { div, svg, PathBuilder } from "gpui-kit";
import { h_flex } from "gpui-base";
import { theme, window as appWindow } from "herdr";

export const SANS = "Geist";
export const MONO = "Geist Mono";
// The type scale: labels and ages, facts, controls, reading, titles.
export const S = { small: 11, meta: 12, ui: 13, read: 14, title: 15, head: 18 };

// MOD is the folder of the mod that imported this file, for its icons.
const MOD = (() => {
  const places = [];
  try {
    places.push(String(import.meta.url));
  } catch {
    // Not every runtime has import.meta.
  }
  try {
    places.push(String(new Error().stack));
  } catch {
    // The stack is only a second source of the path.
  }
  for (const place of places) {
    const found = /\/(mods\/[^/?#:]+)\/look\.js/.exec(place);
    if (found) return found[1];
  }
  return "mods/galpon";
})();

const HEX = /^#[0-9a-fA-F]{6}$/;
const color = (value, fallback) => (HEX.test(String(value ?? "")) ? String(value).toLowerCase() : fallback);

// mix mixes two #rrggbb colors: share 0 is a, and 1 is b.
export function mix(a, b, share) {
  const parts = (hex) => [1, 3, 5].map((i) => parseInt(String(hex).slice(i, i + 2), 16) || 0);
  const [x, y] = [parts(a), parts(b)];
  return `#${x.map((value, i) => Math.round(value + (y[i] - value) * share).toString(16).padStart(2, "0")).join("")}`;
}

function luminance(hex) {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const value = (parseInt(hex.slice(i, i + 2), 16) || 0) / 255;
    return value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

// contrast is the WCAG contrast ratio of two colors.
export function contrast(a, b) {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

// hsl gives the hue (degrees), saturation and lightness (0 to 1) of a color.
function hsl(hex) {
  const [r, g, b] = [1, 3, 5].map((i) => (parseInt(hex.slice(i, i + 2), 16) || 0) / 255);
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  if (max === min) return [0, 0, l];
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  const h = max === r ? (g - b) / d + (g < b ? 6 : 0) : max === g ? (b - r) / d + 2 : (r - g) / d + 4;
  return [h * 60, s, l];
}

function fromHsl(h, s, l) {
  const c = (1 - Math.abs(2 * l - 1)) * s;
  const x = c * (1 - Math.abs(((h / 60) % 2) - 1));
  const m = l - c / 2;
  const [r, g, b] = h < 60 ? [c, x, 0] : h < 120 ? [x, c, 0] : h < 180 ? [0, c, x] : h < 240 ? [0, x, c] : h < 300 ? [x, 0, c] : [c, 0, x];
  return `#${[r, g, b].map((v) => Math.round((v + m) * 255).toString(16).padStart(2, "0")).join("")}`;
}

// HUES are the hues, in degrees, that a theme color must have to show a
// state, and the hue to make when it does not.
const HUES = { green: { at: 135, from: 55, to: 175 }, purple: { at: 270, from: 235, to: 350 } };

// stateColor keeps a theme color that has the hue of its state. Else it
// makes that hue with the saturation and lightness of the reference colors
// of the theme (its red and yellow), so that the color fits the theme.
function stateColor(value, hue, references) {
  const [h, s] = hsl(value);
  if (s >= 0.25 && h >= hue.from && h <= hue.to) return value;
  const known = references.map(hsl).filter(([, rs]) => rs >= 0.25);
  const average = (i, fallback) => (known.length ? known.reduce((sum, c) => sum + c[i], 0) / known.length : fallback);
  return fromHsl(hue.at, Math.min(0.6, Math.max(0.35, average(1, 0.5))), Math.min(0.75, Math.max(0.35, average(2, 0.6))));
}

// readable moves a color toward the foreground until it has min contrast
// against the background.
export function readable(value, bg, fg, min = 4.5) {
  let out = value;
  for (let step = 1; step <= 10 && contrast(out, bg) < min; step++) out = mix(value, fg, step / 10);
  return out;
}

// palette gives the colors of the mods from the theme of the app.
export function palette() {
  const t = theme() ?? {};
  const bg = color(t.background, "#1a1b26");
  const fg = color(t.foreground, "#d4d4d8");
  const dark = luminance(bg) < 0.35;
  const accent = readable(color(t.accent, "#8b9cf7"), bg, fg, 3);
  const ink = color(t.accent_foreground, bg);
  const raw = (index, fallback) => color(t[`ansi${index}`], fallback);
  const ansi = (index, fallback) => readable(raw(index, fallback), bg, fg);
  const references = [raw(1, "#f87171"), raw(3, "#fbbf24")];
  const P = {
    dark,
    bg,
    fg,
    // The sidebar and side panels, and the window behind the cards.
    side: color(t.chrome, mix(bg, dark ? "#000000" : fg, dark ? 0.22 : 0.03)),
    canvas: color(t.canvas, mix(bg, dark ? "#000000" : fg, dark ? 0.4 : 0.06)),
    surface: mix(bg, fg, 0.035),
    raised: mix(bg, fg, 0.065),
    raised2: mix(bg, fg, 0.11),
    hover: mix(bg, fg, 0.05),
    line: mix(bg, fg, 0.09),
    line2: mix(bg, fg, 0.15),
    text: fg,
    text2: readable(mix(fg, bg, 0.3), bg, fg, 6),
    text3: readable(mix(fg, bg, 0.52), bg, fg, 3.6),
    accent,
    accentInk: contrast(accent, ink) >= 4.5 ? ink : contrast(accent, bg) >= contrast(accent, fg) ? bg : fg,
    accentSoft: mix(bg, accent, 0.14),
    accentLine: mix(bg, accent, 0.5),
    red: ansi(1, "#f87171"),
    green: readable(stateColor(raw(2, "#4ade80"), HUES.green, references), bg, fg),
    yellow: ansi(3, "#fbbf24"),
    blue: ansi(4, "#60a5fa"),
    purple: readable(stateColor(raw(5, "#c084fc"), HUES.purple, references), bg, fg),
    cyan: ansi(6, "#22d3ee"),
  };
  // The side panel holds text too.
  P.sideText3 = readable(P.text3, P.side, fg, 3.6);
  return P;
}

// tone gives the color of a state: run, need, done, fail, idle, stop.
export function toneColor(P, tone) {
  switch (tone) {
    case "need": return P.yellow;
    case "done": return P.green;
    case "fail": return P.red;
    case "run": return P.purple;
    default: return P.text3;
  }
}

// icon draws an icon in the text color of its element.
export function icon(name, size = 14) {
  return svg(`${MOD}/tl-${name}.svg`).size(size).flex_none();
}

// spinner is the mark of a wait for data, such as a list that loads: an
// arc that turns. Work has the pulse.
export function spinner(color, size = 14) {
  return icon("spinner", size).text_color(color).spin(900);
}

// dotSize is the size of the dot of a state in a box of a size.
const dotSize = (size) => Math.max(6, Math.round(size * 0.56));

// dot is a still dot of a color, in a box of a size.
export function dot(color, size = 14) {
  return h_flex().w(size).h(size).flex_none().items_center().justify_center()
    .child(div().w(dotSize(size)).h(dotSize(size)).rounded_full().bg(color));
}

// pulse is the mark of work: a dot that fades out and in again, slowly. It
// is the only mark that moves. The app runs the loop natively, so render
// never runs for it.
export function pulse(color, size = 14) {
  return h_flex().w(size).h(size).flex_none().items_center().justify_center()
    .child(div().w(dotSize(size)).h(dotSize(size)).rounded_full().bg(color).loop("opacity", { from: 1, to: 0.35, period: 1500 }));
}

// readyMark is the mark of an answer that is ready for the person: a
// green dot with a check cut out of it.
export function readyMark(P, size = 14) {
  return icon("ready", size).text_color(P.green);
}

// mark draws the mark of a state at a size: a pulse, a dot, a dot with a
// check, an x, a ring or a square.
export function mark(P, tone, size = 14) {
  const box = (child) => h_flex().w(size).h(size).flex_none().items_center().justify_center().child(child);
  switch (tone) {
    case "run": return pulse(P.purple, size);
    case "need": return dot(P.yellow, size);
    case "done": return readyMark(P, size);
    case "fail": return icon("x", size).text_color(P.red);
    case "stop": return box(div().w(Math.round(size * 0.45)).h(Math.round(size * 0.45)).rounded(2).bg(P.line2));
    case "pending": return icon("circle-dashed", size - 2).text_color(P.text3);
    default: return box(div().w(Math.round(size * 0.5)).h(Math.round(size * 0.5)).rounded_full().border_1().border_color(P.text3));
  }
}

// kbd is a key as a small chip, such as "Ctrl K".
export function kbd(P, keys) {
  return h_flex().h(18).px(5).flex_none().items_center().rounded(4).bg(P.raised2).border_1().border_color(P.line2)
    .font_family(MONO).text_size(10).text_color(P.text2).child(keys);
}

// badge is a small tinted label.
export function badge(text, fore, back) {
  return h_flex().h(20).px(7).flex_none().items_center().rounded(5).bg(back).text_color(fore).text_size(S.small).font_weight(500).child(text);
}

// toneBadge is a badge in the tint of a state.
export function toneBadge(P, text, tone) {
  if (tone === "idle" || tone === "stop" || tone === "run" || !tone) return badge(text, P.text2, P.raised);
  const fore = toneColor(P, tone);
  return badge(text, fore, mix(P.bg, fore, 0.15));
}

// label is the small heading of a section, in sentence case.
export function label(P, text) {
  return div().text_size(S.small).font_weight(600).text_color(P.text3).child(text);
}

// button is a control with a label: primary has the accent.
export function button(P, id, text, { primary = false, disabled = false, keys = "", onClick = null, iconName = "", focused = false } = {}) {
  let el = h_flex().id(id).h(30).px(12).gap(8).flex_none().items_center().rounded(8).text_size(S.ui);
  if (primary && !disabled) {
    el = el.bg(P.accent).text_color(P.accentInk).font_weight(600);
  } else {
    el = el.bg(P.raised).border_1().border_color(P.line2).text_color(disabled ? P.text3 : P.text);
    if (!disabled) el = el.hover((s) => s.bg(P.raised2));
  }
  if (focused) el = el.border_2().border_color(primary ? P.text : P.accentLine);
  if (!disabled && onClick) {
    el = el.cursor_pointer()
      .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
      .on_click((event, cx) => onClick(event, cx));
  }
  if (iconName) el = el.child(icon(iconName, 14));
  el = el.child(div().child(text));
  if (keys) {
    el = el.child(h_flex().h(18).px(5).items_center().rounded(4).font_family(MONO).text_size(10)
      .bg(primary && !disabled ? mix(P.accent, P.dark ? "#000000" : "#ffffff", 0.18) : P.raised2)
      .child(keys));
  }
  return el;
}

// iconButton is a square control with an icon. It stops the press, so a
// window_drag block under it does not move the window.
export function iconButton(P, id, name, onClick, { size = 26, iconSize = 15, tip = "", active = false, color = null } = {}) {
  let el = h_flex().id(id).w(size).h(size).flex_none().items_center().justify_center().rounded(6).cursor_pointer()
    .text_color(color ?? (active ? P.text : P.text3)).hover((s) => s.bg(P.raised).text_color(P.text))
    .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
    .on_click((event, cx) => onClick(event, cx))
    .child(icon(name, iconSize));
  if (active) el = el.bg(P.raised);
  if (tip) el = el.tooltip(tip);
  return el;
}

// ring draws a share as a ring: a faint track and a lit arc.
export function ring(share, fore, track, { size = 18, width = 2.5 } = {}) {
  const radius = size / 2 - width;
  const center = size / 2;
  const value = Math.min(0.999, Math.max(0, Number(share) || 0));
  const point = (turn) => {
    const a = turn * Math.PI * 2 - Math.PI / 2;
    return [center + radius * Math.cos(a), center + radius * Math.sin(a)];
  };
  const [sx, sy] = point(0);
  const [mx, my] = point(0.5);
  const full = PathBuilder.stroke(width).move_to(sx, sy).arc_to(radius, radius, 0, false, true, mx, my).arc_to(radius, radius, 0, false, true, sx, sy).build();
  // window is the drawing window of gpui-kit, a global of the mod runtime.
  let el = div().relative().w(size).h(size).flex_none().child(window.paint_path(full, track).absolute().top(0).left(0).size_full());
  if (value > 0.005) {
    const [ex, ey] = point(value);
    const lit = PathBuilder.stroke(width).move_to(sx, sy).arc_to(radius, radius, 0, value > 0.5, true, ex, ey).build();
    el = el.child(window.paint_path(lit, fore).absolute().top(0).left(0).size_full());
  }
  return el;
}

// platform is the system of the app window.
export const platform = () => appWindow()?.platform ?? "linux";

// keyLabel writes a key for people: "ctrl-alt-n" as "Ctrl Alt N".
export function keyLabel(keys) {
  return String(keys ?? "").split("-").map((part) => {
    if (part === "alt" && platform() === "macos") return "⌥";
    if (part === "cmd" || part === "platform") return "⌘";
    if (part === "enter") return "↵";
    if (part === "escape") return "Esc";
    return part.length === 1 ? part.toUpperCase() : part[0].toUpperCase() + part.slice(1);
  }).join(" ");
}

// sentence makes "PULL REQUESTS" into "Pull requests".
export function sentence(text) {
  const value = String(text ?? "").toLowerCase();
  return value ? value[0].toUpperCase() + value.slice(1) : "";
}
