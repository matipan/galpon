const fallback = {
  background: "#222436", foreground: "#c8d3f5", status: "#7aa2f7",
  blue: "#82aaff", purple: "#c099ff", green: "#c3e88d", red: "#ff757f", yellow: "#ffc777",
};

function channels(hex) {
  return [1, 3, 5].map((offset) => Number.parseInt(hex.slice(offset, offset + 2), 16));
}

function hex(values) {
  return `#${values.map((value) => Math.round(value).toString(16).padStart(2, "0")).join("")}`;
}

function mix(a, b, share) {
  const other = channels(b);
  return hex(channels(a).map((value, index) => value + (other[index] - value) * share));
}

function luminance(color) {
  const [r, g, b] = channels(color).map((v) => v / 255)
    .map((v) => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4);
  return .2126 * r + .7152 * g + .0722 * b;
}

export function contrast(a, b) {
  const [light, dark] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (light + .05) / (dark + .05);
}

function readable(value, backgrounds, foreground, minimum = 4.5) {
  for (let step = 0; step <= 20; step++) {
    const next = mix(value, foreground, step / 20);
    if (backgrounds.every((bg) => contrast(next, bg) >= minimum)) return next;
  }
  return foreground;
}

function ink(background) {
  return contrast(background, "#000000") >= contrast(background, "#ffffff") ? "#000000" : "#ffffff";
}

function hsl(color) {
  const [r, g, b] = channels(color).map((v) => v / 255);
  const high = Math.max(r, g, b), low = Math.min(r, g, b);
  const lightness = (high + low) / 2;
  if (high === low) return [0, 0, lightness];
  const delta = high - low;
  const hue = high === r ? (g - b) / delta + (g < b ? 6 : 0)
    : high === g ? (b - r) / delta + 2 : (r - g) / delta + 4;
  return [hue * 60, delta / (1 - Math.abs(2 * lightness - 1)), lightness];
}

function fromHsl(hue, saturation, lightness) {
  const c = (1 - Math.abs(2 * lightness - 1)) * saturation;
  const x = c * (1 - Math.abs((hue / 60) % 2 - 1));
  const m = lightness - c / 2;
  const rgb = hue < 60 ? [c, x, 0] : hue < 120 ? [x, c, 0] : hue < 180 ? [0, c, x]
    : hue < 240 ? [0, x, c] : hue < 300 ? [x, 0, c] : [c, 0, x];
  return hex(rgb.map((v) => (v + m) * 255));
}

// State hues follow the app's shared/look.js rules. Some host themes use teal
// for green; completed work must remain distinct from blue links and controls.
function stateColor(value, range, references) {
  const [hue, saturation] = hsl(value);
  if (saturation >= .25 && hue >= range.from && hue <= range.to) return value;
  const colors = references.map(hsl).filter(([, s]) => s >= .25);
  const average = (i, base) => colors.length ? colors.reduce((sum, c) => sum + c[i], 0) / colors.length : base;
  return fromHsl(range.at, Math.min(.6, Math.max(.35, average(1, .5))), Math.min(.75, Math.max(.35, average(2, .6))));
}

export function companionTheme(palette) {
  // Bootstrap may supply named hex colors, never CSS expressions or URLs.
  const source = Object.fromEntries(Object.entries(fallback).map(([name, value]) => [name,
    typeof palette?.[name] === "string" && /^#[0-9a-f]{6}$/i.test(palette[name]) ? palette[name].toLowerCase() : value]));
  const bg = source.background;
  const fg = source.foreground;
  const dark = luminance(bg) < .35;
  const surface = mix(bg, fg, .035);
  const raised = mix(bg, fg, .065);
  const side = mix(bg, dark ? "#000000" : fg, dark ? .22 : .03);
  const backgrounds = [bg, surface, raised, side];
  const text = readable(source.status, backgrounds, fg);
  const accent = readable(source.status, [bg], fg, 3);
  const references = [source.red, source.yellow];
  let selection = mix(bg, fg, .11);
  // A selected row must keep its normal text contrast.
  if (contrast(selection, fg) < 4.5) selection = raised;
  return {
    scheme: dark ? "dark" : "light",
    colors: {
      background: bg, foreground: fg, side, surface, "surface-raised": raised,
      prompt: surface, selection, rule: mix(bg, fg, .09), border: mix(bg, fg, .15),
      "accent-line": mix(bg, accent, .5),
      muted: readable(mix(fg, bg, .3), backgrounds, fg, 6),
      comment: readable(mix(fg, bg, .52), [...backgrounds, selection], fg),
      accent, "accent-text": text, "accent-ink": ink(accent),
      status: source.blue, "status-ink": ink(source.blue),
      blue: readable(source.blue, backgrounds, fg),
      purple: readable(stateColor(source.purple, { at: 270, from: 235, to: 350 }, references), backgrounds, fg),
      green: readable(stateColor(source.green, { at: 135, from: 55, to: 175 }, references), backgrounds, fg),
      red: readable(source.red, backgrounds, fg), yellow: readable(source.yellow, backgrounds, fg),
    },
  };
}
