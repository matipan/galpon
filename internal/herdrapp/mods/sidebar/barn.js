// The barn of the Galpon layout, without drawing: the pixel art and the
// place of each thing in the scene. main.js draws the scene with the theme
// colors.
//
// Each workspace is a pen, and each open agent is an animal. The harness
// selects the animal, and the state selects what it does:
//
//   needs you        it waits at the fence under a "!" badge
//   ready for you    it stands next to a full milk bucket, under a check badge
//   running          it walks up and down
//   delegating       a sheepdog herds one sheep for each delegated agent
//   background       a hen sits on one egg for each background task; a task
//                    that ends hatches a chick
//   idle             it sleeps
//
// Pull requests: a truck takes the hay bales that are ready to merge, the
// mailbox raises its flag for review requests, and a fox visits the henhouse
// when the checks fail or the agent must do more. The windmill turns while an
// agent runs. At night the sky has stars.

import { MARKS, orderRows, taskCount } from "./sidebar.js";

// One pixel of the art is P px.
export const P = 3;
// The scene is this high. The ground starts at GROUND, and things stand on it.
export const HEIGHT = 150;
export const GROUND = HEIGHT - 16;
// A chick stays this long after its egg hatches.
export const CHICK_LIFE = 10 * 60 * 1000;

// The sound files that `galpon herdr-app install` writes, relative to the app
// folder. The app plays the moo and the rooster itself, when
// `galpon herdr-app sounds barn` selects them in settings.json.
export const SOUNDS = {
  moo: "sounds/galpon-moo.mp3",
  rooster: "sounds/galpon-rooster.mp3",
  cluck: "sounds/galpon-cluck.mp3",
};

// SPRITES is the pixel art. Each character of art is a pixel, "." is empty,
// and colors maps a character to a color name of palette().
export const SPRITES = {
  cow: {
    colors: { w: "white", d: "dark", g: "gray", p: "pink" },
    art: [
      "..............g..g",
      "..............wwww",
      ".wwwwwwwwwwwwwwdww",
      "dwwwddwwwwwddwwwww",
      ".wwdddwwwwwdddwwpp",
      ".wwwddwwwwwwdwwwpp",
      ".wwwwwwwwwwwwww...",
      "..wwpp.....ww.....",
      "..ww.......ww.....",
      "..ww.......ww.....",
      "..dd.......dd.....",
    ],
  },
  cowSleep: {
    colors: { w: "white", d: "dark", g: "gray", p: "pink" },
    art: [
      "..............g..g",
      "..............wwww",
      ".wwwwwwwwwwwwwwddw",
      "dwwwddwwwwwddwwwpp",
      ".wwdddwwwwwdddwwpp",
      ".wwwwwwwwwwwwwww..",
      ".ddwwwwwwwwwwwdd..",
    ],
  },
  horse: {
    colors: { h: "gray", d: "dark", w: "white" },
    art: [
      "..............d....",
      ".............dhd...",
      "............dhhhh..",
      "...........dhhhdhh.",
      "..........dhhhhhhhh",
      ".........dhhh..hhww",
      ".dhhhhhhhhhhh......",
      "ddhhhhhhhhhhh......",
      "d.hhhhhhhhhhh......",
      "d.hhhhhhhhhhh......",
      "..hh.......hh......",
      "..hh.......hh......",
      "..hh.......hh......",
      "..dd.......dd......",
    ],
  },
  horseSleep: {
    colors: { h: "gray", d: "dark", w: "white" },
    art: [
      "..............d....",
      ".............dhd...",
      "............dhhhh..",
      "...........dhhddhh.",
      ".dhhhhhhhhhhhhhhhww",
      "ddhhhhhhhhhhhh.....",
      "d.hhhhhhhhhhhh.....",
      ".ddhhhhhhhhhdd.....",
    ],
  },
  goat: {
    colors: { w: "white", d: "dark", g: "gray" },
    art: [
      "...........ggg..",
      "..........g.wwg.",
      "w...........wdww",
      ".wwwwwwwwwwwwwwd",
      "wwwwwwwwwwwww.g.",
      ".wwwwwwwwwwww.g.",
      ".wwwwwwwwwww....",
      ".w.w......w.w...",
      ".w.w......w.w...",
      ".d.d......d.d...",
    ],
  },
  goatSleep: {
    colors: { w: "white", d: "dark", g: "gray" },
    art: [
      "...........ggg..",
      "..........g.wwg.",
      "............wddw",
      ".wwwwwwwwwwwwwwd",
      "wwwwwwwwwwwww.g.",
      ".wwwwwwwwwwww.g.",
      ".dwwwwwwwwwd....",
    ],
  },
  pig: {
    colors: { p: "pink", m: "magenta", d: "dark" },
    art: [
      "...........p.p..",
      "...pppppppppppp.",
      "p.pppppppppppdpp",
      ".pppppppppppppmm",
      "pppppppppppppmm.",
      ".pppppppppppppp.",
      "..ppp......ppp..",
      "..pp........pp..",
      "..dd........dd..",
    ],
  },
  pigSleep: {
    colors: { p: "pink", m: "magenta", d: "dark" },
    art: [
      "...........p.p..",
      "...pppppppppppp.",
      "p.pppppppppppddp",
      ".pppppppppppppmm",
      ".pppppppppppppmm",
      ".ddppppppppppdd.",
    ],
  },
  dog: {
    colors: { d: "dark", w: "white" },
    art: [
      ".............dd...",
      "............dddd..",
      "dd..........ddwddd",
      ".dd.........dwww..",
      "..dddddddddddww...",
      "..dddddddddddw....",
      "...wwwwwwwwww.....",
      "...dd......dd.....",
      "..dd........dd....",
      ".dd..........dd...",
    ],
  },
  sheep: {
    colors: { w: "white", d: "dark" },
    art: [
      "..wwwwww....",
      ".wwwwwwwwdd.",
      "wwwwwwwwwdwd",
      "wwwwwwwwwddd",
      ".wwwwwwwww..",
      "..d.....d...",
      "..d.....d...",
    ],
  },
  hen: {
    colors: { w: "white", p: "pink", o: "orange", d: "dark" },
    art: [
      "........pp..",
      ".......wwww.",
      "w......wdwwo",
      "ww.....wwww.",
      "www..wwwwww.",
      "wwwwwwwwwww.",
      ".wwwwwwwwww.",
      "..wwwwwwww..",
    ],
  },
  egg: {
    colors: { e: "egg", d: "dark" },
    art: [".ddd.", "deeed", "deeed", "deeed", ".ddd."],
  },
  nest: {
    colors: { h: "hay", H: "hayBand" },
    art: [".hHhHhHhHhHhHh.", "hHhHhHhHhHhHhHh", ".hhhhhhhhhhhhh."],
  },
  chick: {
    colors: { y: "yellow", o: "orange", d: "dark" },
    art: [
      ".yyy...",
      ".ydyyo.",
      "yyyyy..",
      ".yyyy..",
      ".o..o..",
    ],
  },
  bucket: {
    colors: { g: "gray", w: "white", d: "dark" },
    art: [
      ".gggggg.",
      "g......g",
      "gwwwwwwg",
      ".gggggg.",
      ".gddddg.",
      ".gggggg.",
      "..gggg..",
    ],
  },
  fox: {
    colors: { r: "error", w: "white", d: "dark" },
    art: [
      "............r.r..",
      "...........rrrrr.",
      "...........rrdrrr",
      "rr.........rrrrwd",
      "wrrrrrrrrrrrrrw..",
      ".rrrrrrrrrrrrr...",
      "..rrrrrrrrrrr....",
      "..d.d......d.d...",
      "..d.d......d.d...",
    ],
  },
  bale: {
    colors: { h: "hay", H: "hayBand" },
    art: ["hhhhhhhh", "hHhhhhHh", "hhhhhhhh", "hHhhhhHh", "hhhhhhhh"],
  },
  truck: {
    colors: { t: "teal", w: "glass", d: "dark", g: "gray", y: "yellow" },
    art: [
      ".................ttttttt......",
      ".................twwwwwtt.....",
      ".................twwwwwttt....",
      ".................twwwwwtttt...",
      "tttttttttttttttttttttttttttty.",
      "tttttttttttttttttttttttttttttt",
      "tt..dddddd..........dddddd..tt",
      "...dddddddd........dddddddd...",
      "...dddggddd........dddggddd...",
      "...dddddddd........dddddddd...",
      "....dddddd..........dddddd....",
    ],
  },
  mailbox: {
    colors: { g: "gray", k: "dark", d: "dim", y: "flag" },
    art: [
      "........yyy",
      "........yyy",
      ".ggggggg.d.",
      "gggggggggd.",
      "gkkkkkkgg..",
      "ggggggggg..",
      "....d......",
      "....d......",
      "....d......",
      "...ddd.....",
    ],
  },
  henhouse: {
    colors: { r: "roof", b: "wall", k: "dark", d: "dim" },
    art: [
      "......rrrr......",
      "....rrrrrrrr....",
      "..rrrrrrrrrrrr..",
      "rrrrrrrrrrrrrrrr",
      ".bbbbbbbbbbbbbb.",
      ".bbbbbkkkkbbbbb.",
      ".bbbbkkkkkkbbbb.",
      ".bbbbkkkkkkbbbb.",
      ".bbbbbbbbbbbbbb.",
      ".dd..........dd.",
      ".dd..........dd.",
    ],
  },
  barn: {
    colors: { r: "roof", b: "wall", w: "trim", h: "hay", k: "dark" },
    art: [
      ".............rrrrrrrr.............",
      "..........rrrrrrrrrrrrrr..........",
      "........rrrrrrrrrrrrrrrrrr........",
      "......rrrrrrrrrrrrrrrrrrrrrr......",
      ".....rrrrrrrrrrrrrrrrrrrrrrrr.....",
      "....rrrrrrrrrrrrrrrrrrrrrrrrrr....",
      "...rrrrrrrrrrrrrrrrrrrrrrrrrrrr...",
      "..rrrrrrrrrrrrrrrrrrrrrrrrrrrrrr..",
      ".rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr.",
      "..wbbbbbbbbbbbbbbbbbbbbbbbbbbbbw..",
      "..wbbbbbbbbbbwwwwwwwwbbbbbbbbbbw..",
      "..wbbbbbbbbbbwhhhhhhwbbbbbbbbbbw..",
      "..wbbbbbbbbbbwhhhhhhwbbbbbbbbbbw..",
      "..wbbbbbbbbbbwwwwwwwwbbbbbbbbbbw..",
      "..wbbbbbbbbbbbbbbbbbbbbbbbbbbbbw..",
      "..wwwwwwwwwwwwwwwwwwwwwwwwwwwwww..",
      "..wbbbbbwwwwwwwwwwwwwwwwwwbbbbbw..",
      "..wbbbbbwwbbbbbwwwwbbbbbwwbbbbbw..",
      "..wbwwwbwbwbbbwbwwbwbbbwbwbwwwbw..",
      "..wbwkwbwbwbbbwbwwbwbbbwbwbwkwbw..",
      "..wbwkwbwbbwbwbbwwbbwbwbbwbwkwbw..",
      "..wbwwwbwbbbwbbbwwbbbwbbbwbwwwbw..",
      "..wbbbbbwbbbwbbbwwbbbwbbbwbbbbbw..",
      "..wbbbbbwbbwbwbbwwbbwbwbbwbbbbbw..",
      "..wbbbbbwbwbbbwbwwbwbbbwbwbbbbbw..",
      "..wbbbbbwbwbbbwbwwbwbbbwbwbbbbbw..",
      "..wbbbbbwwbbbbbwwwwbbbbbwwbbbbbw..",
      "..wbbbbbwwwwwwwwwwwwwwwwwwbbbbbw..",
    ],
  },
  tower: { colors: { d: "dim" }, art: towerArt() },
  cloud: {
    colors: { c: "cloud" },
    art: ["....cccc....", "..cccccccc..", "cccccccccccc", ".cccccccccc."],
  },
  moon: {
    colors: { m: "moon" },
    art: ["..mmm.", ".mm...", "mm....", "mm....", ".mm...", "..mmm."],
  },
  tuft: { colors: { g: "tuft" }, art: ["g.g", ".g."] },
  // The mark of the barn in the status bar.
  mark: {
    colors: { c: "mark" },
    art: ["...c...", "..ccc..", ".ccccc.", "ccccccc", ".cc.cc.", ".cc.cc."],
  },
};

// The windmill tower is a lattice: two legs that spread to the ground, with
// a brace every few rows.
function towerArt() {
  const rows = [];
  for (let y = 0; y < 30; y++) {
    const spread = Math.floor((y * 3) / 29);
    const row = [..."........"];
    row[3 - spread] = "d";
    row[4 + spread] = "d";
    if (y % 7 === 6) row.fill("d", 3 - spread, 5 + spread);
    rows.push(row.join(""));
  }
  return rows;
}

// spriteOf turns the art of a sprite into horizontal runs of one color, so
// that one rectangle draws each run. A thing can give a run another color
// name by its character. The result is kept.
const runCache = new Map();
export function spriteOf(name) {
  if (runCache.has(name)) return runCache.get(name);
  const sprite = SPRITES[name];
  const w = Math.max(...sprite.art.map((row) => row.length));
  const runs = [];
  sprite.art.forEach((row, y) => {
    let x = 0;
    while (x < row.length) {
      const ch = row[x];
      if (ch === "." || !sprite.colors[ch]) {
        x++;
        continue;
      }
      let end = x + 1;
      while (end < row.length && row[end] === ch) end++;
      runs.push({ x, y, length: end - x, ch, color: sprite.colors[ch] });
      x = end;
    }
  });
  const out = { w, h: sprite.art.length, runs };
  runCache.set(name, out);
  return out;
}

// alpha gives a #rrggbb color with an alpha, as two hex digits.
export function alpha(color, value) {
  const hex = String(color ?? "#888888");
  return `${hex.length === 9 ? hex.slice(0, 7) : hex}${value}`;
}

// palette gives each color name of the art its theme color. Red is only for
// the fox, because red tells that something went wrong. Yellow and green are
// the colors of the turns, so the art uses them only in small things.
export function palette(t) {
  const pick = (...names) => names.map((name) => t[name]).find(Boolean) ?? "#888888";
  const white = pick("ansi15", "foreground");
  const yellow = pick("ansi3", "working");
  const green = pick("ansi2", "done");
  const dim = pick("dim");
  return {
    white, gray: pick("muted"), dark: pick("ansi8", "dim"), dim, fg: pick("foreground"),
    pink: pick("ansi13", "ansi5"), magenta: pick("ansi5", "ansi13"), orange: pick("ansi11", "ansi3"), yellow,
    error: pick("ansi1", "blocked"), teal: pick("ansi6", "accent"), accent: pick("accent"),
    attention: pick("working"), ready: pick("done"), muted: pick("muted"),
    hay: alpha(yellow, "cc"), hayBand: alpha(pick("ansi11", "ansi3"), "cc"),
    roof: dim, wall: alpha(pick("accent"), "99"), trim: pick("foreground"), glass: alpha(white, "88"),
    egg: white, cloud: alpha(white, "2c"), moon: alpha(white, "cc"), star: white,
    ground: alpha(green, "1a"), grassEdge: alpha(green, "55"), tuft: alpha(green, "70"), fence: alpha(dim, "cc"),
    flag: dim, mark: pick("muted"),
  };
}

// animalOf gives the animal of an agent from the app that it reports: a cow
// for Pi, a horse for Claude Code, a goat for Codex, and a pig for others.
export function animalOf(app) {
  const name = String(app ?? "").toLowerCase();
  if (name === "pi" || name.startsWith("pi-")) return "cow";
  if (name.includes("claude")) return "horse";
  if (name.includes("codex")) return "goat";
  return "pig";
}

// night tells whether the barn shows the night sky at this local time.
export function night(date) {
  const hour = date.getHours();
  return hour >= 20 || hour < 6;
}

// hatchesOf finds the background tasks that ended between two Galpon reads:
// an agent that still has facts and fewer tasks than before. A drop to no
// facts is not a hatch, because the agent can be gone.
export function hatchesOf(before, after, now) {
  if (!before || !after) return [];
  const out = [];
  for (const [id, facts] of Object.entries(after)) {
    const was = taskCount(before[id]?.background);
    const is = taskCount(facts?.background);
    if (before[id] && is < was) out.push({ agentId: id, count: was - is, at: now });
  }
  return out;
}

// barnSounds reads settings.json: on when the app uses the barn sounds for
// an agent that needs you and one that finished, and enabled unless the app
// sounds are off.
export function barnSounds(text) {
  let sounds = {};
  try {
    sounds = JSON.parse(text ?? "{}")?.sounds ?? {};
  } catch {
    sounds = {};
  }
  return { on: sounds.blocked === SOUNDS.moo && sounds.done === SOUNDS.rooster, enabled: sounds.enabled !== false };
}

// A random number generator with a seed, so that stars and tufts stay in
// their place from one draw to the next.
function seeded(seed) {
  let value = seed;
  return () => {
    value = (value * 1103515245 + 12345) % 2147483648;
    return value / 2147483648;
  };
}

function place(name, x, bottom, extra = {}) {
  const s = spriteOf(name);
  return { kind: "sprite", name, x, y: bottom - s.h * P, w: s.w * P, h: s.h * P, ...extra };
}

function bob(from, size, period) {
  return { property: "top", from, to: from - size, period };
}

// agentSlot places one agent in its pen, from x. It returns the things to
// draw and the width that the agent takes.
function agentSlot(row, index, x, fresh, chicks) {
  const items = [];
  const animal = animalOf(row.app);
  const stand = spriteOf(animal);
  const sw = stand.w * P;
  let width = sw + 14;
  switch (row.state) {
    case "running": {
      const walk = place(animal, x + 4, GROUND, { flip: index % 2 === 1 });
      walk.loops = [
        { property: "left", from: x + 4, to: x + 34, period: 2600 + index * 190 },
        bob(walk.y, 2, 380 + index * 30),
      ];
      items.push(walk);
      width = sw + 42;
      break;
    }
    case "needs": {
      const body = place(animal, x + 4, GROUND);
      items.push(body);
      const y = body.y - 22;
      items.push({ kind: "badge", x: x + sw - 16, y, state: "needs", fresh, loops: [bob(y, 3, 1200)] });
      break;
    }
    case "ready": {
      const body = place(animal, x + 4, GROUND);
      const bucket = place("bucket", x + 6 + sw, GROUND);
      items.push(body, bucket);
      items.push({ kind: "badge", x: bucket.x + 2, y: bucket.y - 22, state: "ready", fresh });
      width = sw + bucket.w + 18;
      break;
    }
    case "delegating": {
      const shown = Math.min(row.delegating ?? 0, 4);
      const area = Math.max(shown, 1) * 32 + 8;
      for (let j = 0; j < shown; j++) {
        const sheep = place("sheep", x + 6 + j * 32, GROUND, { flip: j % 2 === 1 });
        sheep.loops = [bob(sheep.y, 1, 1700 + j * 260)];
        items.push(sheep);
      }
      if ((row.delegating ?? 0) > shown) {
        items.push({ kind: "text", x: x + 8 + area - 8, y: GROUND - 34, text: `+${row.delegating - shown}`, color: "muted", size: 10.5 });
      }
      const dog = place("dog", x, GROUND);
      dog.loops = [
        { property: "left", from: x - 2, to: x + Math.max(area - 40, 12), period: 2300 + index * 150 },
        bob(dog.y, 2, 300),
      ];
      items.push(dog);
      width = Math.max(area, spriteOf("dog").w * P) + 26;
      break;
    }
    case "background": {
      const tasks = taskCount(row.background);
      const shown = Math.min(tasks, 4);
      // The hen sits on a nest, and its eggs lie in front of it.
      const nest = place("nest", x + 2, GROUND);
      const hen = place("hen", x + 4, nest.y + 3);
      hen.loops = [bob(hen.y, 1, 2000)];
      items.push(nest, hen);
      const eggX = x + 4 + spriteOf("hen").w * P - 10;
      for (let j = 0; j < shown; j++) items.push(place("egg", eggX + j * (5 * P + 2), GROUND + 1));
      if (tasks > shown) items.push({ kind: "text", x: eggX + shown * (5 * P + 2) + 2, y: GROUND - 14, text: `+${tasks - shown}`, color: "muted", size: 10.5 });
      width = Math.max(nest.w, eggX - x + shown * (5 * P + 2)) + 20;
      break;
    }
    case "idle": {
      const sleep = place(`${animal}Sleep`, x + 4, GROUND);
      items.push(sleep);
      items.push({ kind: "text", x: x + sw - 8, y: sleep.y - 16, frames: ["z", "zz", "zzz", " "], period: 700, color: "muted", size: 10.5 });
      break;
    }
    default:
      items.push(place(animal, x + 4, GROUND, { opacity: 0.5 }));
  }
  for (let k = 0; k < Math.min(chicks, 3); k++) {
    const chick = place("chick", x + width + k * (7 * P + 3), GROUND);
    chick.loops = [bob(chick.y, 3, 460 + k * 70)];
    items.push(chick);
  }
  if (chicks > 0) width += Math.min(chicks, 3) * (7 * P + 3) + 4;
  if (row.focused) items.push({ kind: "rect", x: x + 4, y: GROUND + 2, w: Math.min(sw, width - 8), h: 2, color: "accent" });
  return { items, width, hit: { kind: "hit", x, y: GROUND - 70, w: width, h: 72, row } };
}

// pullTooltip names the pull requests of a thing in the barn.
function pullTooltip(list, words) {
  const lines = list.slice(0, 4).map((item) => `${item.ref} · ${item.title}`);
  if (list.length > 4) lines.push(`and ${list.length - 4} more`);
  return `${words}: ${lines.join(" · ")}`;
}

// barnScene places everything. workspaces come from buildWorkspaces, turns
// from the agents block (with fresh flags), pulls are the pull requests that
// need someone, hatched are the recent hatches, and width is the width of
// the view: the ground and the sky fill it.
export function barnScene({ workspaces = [], turns = [], pulls = [], hatched = [], now = Date.now(), width = 0, dark = false }) {
  const items = [];
  const hits = [];
  const fresh = new Set(turns.filter((turn) => turn.fresh).map((turn) => turn.key));
  const agentRows = workspaces.flatMap((workspace) => workspace.rows.filter((row) => row.kind === "agent"));
  const running = agentRows.filter((row) => row.state === "running" || row.state === "delegating" || row.state === "background").length;
  let x = 18;

  // The windmill turns faster when more agents run.
  const tower = place("tower", x, GROUND);
  items.push(tower);
  items.push({
    kind: "windmill", x: x + tower.w / 2 - 26, y: tower.y - 26, size: 52,
    spin: running > 0 ? Math.max(900, 4200 - 600 * running) : 0,
  });
  x += tower.w + 10;

  const barn = place("barn", x, GROUND);
  items.push(barn);
  hits.push({ kind: "hit", x: barn.x, y: barn.y, w: barn.w, h: barn.h, action: { type: "command-center" }, tooltip: "Galpon · open the command center" });
  x += barn.w + 18;

  const ready = pulls.filter((item) => item.state === "ready");
  const needs = pulls.filter((item) => item.state === "needs");
  const broken = pulls.filter((item) => item.state === "agent");

  if (ready.length > 0) {
    const truck = place("truck", x, GROUND);
    items.push(truck);
    const bed = truck.y + 4 * P;
    const spots = [[1, 0], [9, 0], [5, 1]];
    ready.slice(0, spots.length).forEach((item, j) => {
      const [column, layer] = spots[j];
      const bale = place("bale", x + column * P, bed - layer * 5 * P);
      items.push(bale);
      hits.push({ kind: "hit", x: bale.x, y: bale.y, w: bale.w, h: bale.h, action: { type: "pull-request", item }, tooltip: `${item.ref} · Ready for you · ${item.title}` });
    });
    items.push({ kind: "badge", x: x + 19 * P, y: truck.y - 22, state: "ready", fresh: ready.some((item) => item.fresh), count: ready.length });
    hits.push({ kind: "hit", x: truck.x, y: truck.y + 4 * P, w: truck.w, h: truck.h - 4 * P, action: { type: "pull-request", item: ready[0] }, tooltip: pullTooltip(ready, "Ready to merge") });
    x += truck.w + 16;
  }

  const mailbox = place("mailbox", x, GROUND, { colors: { y: needs.length > 0 ? "attention" : "dim" } });
  items.push(mailbox);
  if (needs.length > 0) {
    const y = mailbox.y - 22;
    items.push({ kind: "badge", x: x + 2, y, state: "needs", fresh: needs.some((item) => item.fresh), count: needs.length, loops: [bob(y, 3, 1200)] });
    hits.push({ kind: "hit", x: mailbox.x, y: mailbox.y - 22, w: mailbox.w, h: mailbox.h + 22, action: { type: "pull-request", item: needs[0] }, tooltip: pullTooltip(needs, "Review requests") });
  } else {
    hits.push({ kind: "hit", x: mailbox.x, y: mailbox.y, w: mailbox.w, h: mailbox.h, tooltip: "No review request waits for you" });
  }
  x += mailbox.w + 16;

  const henhouse = place("henhouse", x, GROUND);
  items.push(henhouse);
  x += henhouse.w;
  if (broken.length > 0) {
    const fox = place("fox", x + 4, GROUND, { flip: true });
    fox.loops = [{ property: "left", from: x + 4, to: x + 14, period: 3000 }];
    items.push(fox);
    items.push({ kind: "text", x: x + 10, y: fox.y - 16, text: `${MARKS.agent.mark} ${broken.length}`, color: "error", size: 11 });
    hits.push({ kind: "hit", x: x, y: fox.y - 16, w: fox.w + 14, h: fox.h + 16, action: { type: "pull-request", item: broken[0] }, tooltip: pullTooltip(broken, "Agent's turn") });
    x += fox.w + 14;
  }
  x += 24;

  // The pens, in the order of the sidebar: the most urgent agents first.
  const chicksOf = new Map();
  for (const hatch of hatched) {
    if (now - hatch.at < CHICK_LIFE) chicksOf.set(hatch.agentId, (chicksOf.get(hatch.agentId) ?? 0) + hatch.count);
  }
  const labels = [];
  for (const workspace of workspaces) {
    const rows = orderRows(workspace.rows).filter((row) => row.kind === "agent");
    if (rows.length === 0) continue;
    const penX = x;
    let slotX = penX + 12;
    const slots = rows.map((row, index) => {
      const slot = agentSlot(row, index, slotX, fresh.has(row.key), chicksOf.get(row.agentId) ?? 0);
      slotX += slot.width;
      return slot;
    });
    const penW = slotX - penX + 8;
    for (let postX = penX; postX <= penX + penW - 4; postX += 24) items.push({ kind: "rect", x: postX, y: GROUND - 24, w: 4, h: 24, color: "fence" });
    items.push({ kind: "rect", x: penX + penW - 4, y: GROUND - 24, w: 4, h: 24, color: "fence" });
    items.push({ kind: "rect", x: penX, y: GROUND - 20, w: penW, h: 3, color: "fence" });
    items.push({ kind: "rect", x: penX, y: GROUND - 11, w: penW, h: 3, color: "fence" });
    for (const slot of slots) {
      items.push(...slot.items);
      hits.push(slot.hit);
    }
    labels.push({ kind: "label", x: penX + 4, y: GROUND + 2, w: penW - 8, text: workspace.title, active: workspace.active });
    x = penX + penW + 18;
  }
  if (agentRows.length === 0) items.push({ kind: "text", x, y: GROUND - 30, text: "No agent is open.", color: "muted", size: 12 });

  const sceneWidth = Math.max(x + 12, width);
  const sky = [];
  if (dark) {
    const random = seeded(42);
    for (let i = 0; i < Math.round(sceneWidth / 60); i++) {
      const starX = Math.round(random() * (sceneWidth - 8));
      const starY = Math.round(4 + random() * 56);
      sky.push({ kind: "rect", x: starX, y: starY, w: P, h: P, color: "star", loops: [{ property: "opacity", from: 0.2, to: 0.9, period: 1300 + Math.round(random() * 1500) }] });
    }
    sky.push(place("moon", Math.max(sceneWidth - 70, x), 6 + 6 * P));
  } else {
    [[0.14, 14], [0.5, 30], [0.83, 8]].forEach(([share, y], i) => {
      const cloud = place("cloud", Math.round(sceneWidth * share), y + 4 * P);
      cloud.loops = [{ property: "left", from: cloud.x, to: cloud.x + 70, period: 52000 + i * 9000, easing: "linear" }];
      sky.push(cloud);
    });
  }
  const ground = [
    { kind: "rect", x: 0, y: GROUND, w: sceneWidth, h: HEIGHT - GROUND, color: "ground" },
    { kind: "rect", x: 0, y: GROUND, w: sceneWidth, h: 2, color: "grassEdge" },
  ];
  const random = seeded(7);
  for (let tuftX = 30; tuftX < sceneWidth; tuftX += 50 + Math.round(random() * 60)) ground.push(place("tuft", tuftX, GROUND + 1));
  return { width: sceneWidth, height: HEIGHT, items: [...sky, ...ground, ...items, ...labels, ...hits] };
}
