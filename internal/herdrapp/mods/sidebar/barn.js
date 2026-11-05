// The barn of the Galpon layout, without drawing: the pixel art and the
// place of each thing in the scene. main.js draws the scene with the theme
// colors, and brain.js moves the animals.
//
// Each workspace is a pen, and each open agent is an animal. The harness
// selects the animal: a cow for Pi, a horse for Claude Code, a goat for
// Codex and a pig for others. The state of the agent selects what the animal
// does (see brain.js). A delegating agent is a sheepdog that herds one sheep
// for each delegated agent, and an agent that waits for background tasks is
// a hen on one egg for each task; a task that ends hatches a chick.
//
// A Pi cow shows what its session cost at API prices: it weighs one pound
// for each dollar, and its weight gives its size. It has an ear tag with the
// price, a prize at each cost, a cow pie in its pen for each $100, and coins
// over it while the session costs money.
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
    colors: { w: "white", s: "spot", d: "dark", g: "gray", p: "pink" },
    art: [
      "..............g..g",
      "..............wwww",
      ".wwwwwwwwwwwwwwdww",
      "dwwwsswwwwwsswwwww",
      ".wwssswwwwwssswwpp",
      ".wwwsswwwwwwswwwpp",
      ".wwwwwwwwwwwwww...",
      "..wwpp.....ww.....",
      "..ww.......ww.....",
      "..ww.......ww.....",
      "..dd.......dd.....",
    ],
  },
  cowEat: {
    colors: { w: "white", s: "spot", d: "dark", g: "gray", p: "pink" },
    art: [
      "..................",
      "..................",
      ".wwwwwwwwwwwwww...",
      "dwwwsswwwwwsswww..",
      ".wwssswwwwwssswww.",
      ".wwwsswwwwwwswwwwg",
      ".wwwwwwwwwwwwwwwww",
      "..wwpp.....ww..wdw",
      "..ww.......ww..www",
      "..ww.......ww..wpp",
      "..dd.......dd..wpp",
    ],
  },
  cowLie: {
    colors: { w: "white", s: "spot", d: "dark", g: "gray", p: "pink" },
    art: [
      "..............g..g",
      "..............wwww",
      ".wwwwwwwwwwwwwwdww",
      "dwwwsswwwwwsswwwpp",
      ".wwssswwwwwssswwpp",
      ".wwwwwwwwwwwwwww..",
      ".ddwwwwwwwwwwwdd..",
    ],
  },
  cowSleep: {
    colors: { w: "white", s: "spot", d: "dark", g: "gray", p: "pink" },
    art: [
      "..............g..g",
      "..............wwww",
      ".wwwwwwwwwwwwwwddw",
      "dwwwsswwwwwsswwwpp",
      ".wwssswwwwwssswwpp",
      ".wwwwwwwwwwwwwww..",
      ".ddwwwwwwwwwwwdd..",
    ],
  },
  cowFront: {
    colors: { w: "white", s: "spot", d: "dark", g: "gray", p: "pink" },
    art: [
      "...g......g...",
      "...gwwwwwwg...",
      ".ggwwwwwwwwgg.",
      "...wdwwwwdw...",
      "...wwwwwwww...",
      "..wwppppppww..",
      ".wwwpdppdpwww.",
      ".wswwppppwwsw.",
      ".wwwwwwwwwwww.",
      ".ww........ww.",
      ".dd........dd.",
    ],
  },
  bull: {
    colors: { w: "white", s: "spot", d: "dark", h: "horn", p: "pink", y: "gold" },
    art: [
      ".............h....h",
      "..............hwwh.",
      ".wwwwwwwwwwwwwwdww.",
      "dwwwsswwwwwsswwwww.",
      ".wwssswwwwwssswwpp.",
      ".wwwsswwwwwwswwwpp.",
      ".wwwwwwwwwwwwww.yy.",
      "..ww.......ww......",
      "..ww.......ww......",
      "..ww.......ww......",
      "..dd.......dd......",
    ],
  },
  bullEat: {
    colors: { w: "white", s: "spot", d: "dark", h: "horn", p: "pink", y: "gold" },
    art: [
      "...................",
      "...................",
      ".wwwwwwwwwwwwww....",
      "dwwwsswwwwwsswww...",
      ".wwssswwwwwssswww..",
      ".wwwsswwwwwwswwwwh.",
      ".wwwwwwwwwwwwwwwwwh",
      "..ww.......ww..wdw.",
      "..ww.......ww..www.",
      "..ww.......ww..wppy",
      "..dd.......dd..wppy",
    ],
  },
  bullLie: {
    colors: { w: "white", s: "spot", d: "dark", h: "horn", p: "pink", y: "gold" },
    art: [
      ".............h....h",
      "..............hwwh.",
      ".wwwwwwwwwwwwwwdww.",
      "dwwwsswwwwwsswwwpp.",
      ".wwssswwwwwssswwppy",
      ".wwwwwwwwwwwwwww.y.",
      ".ddwwwwwwwwwwwdd...",
    ],
  },
  bullSleep: {
    colors: { w: "white", s: "spot", d: "dark", h: "horn", p: "pink", y: "gold" },
    art: [
      ".............h....h",
      "..............hwwh.",
      ".wwwwwwwwwwwwwwddw.",
      "dwwwsswwwwwsswwwpp.",
      ".wwssswwwwwssswwppy",
      ".wwwwwwwwwwwwwww.y.",
      ".ddwwwwwwwwwwwdd...",
    ],
  },
  bullFront: {
    colors: { w: "white", s: "spot", d: "dark", h: "horn", p: "pink", y: "gold" },
    art: [
      ".h............h.",
      "..h..wwwwww..h..",
      "...hwwwwwwwwh...",
      "...wwdwwwwdww...",
      "....wwwwwwww....",
      "...wwppppppww...",
      "..wwwpdppdpwww..",
      "..wswwppppwwsw..",
      "..wwwwwyywwwww..",
      "..ww........ww..",
      "..dd........dd..",
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
  horseEat: {
    colors: { h: "gray", d: "dark", w: "white" },
    art: [
      "...................",
      "...................",
      "...................",
      "...................",
      "...................",
      "...................",
      ".dhhhhhhhhhhhd.....",
      "ddhhhhhhhhhhhhd....",
      "d.hhhhhhhhhhhhhd...",
      "d.hhhhhhhhhhh.hhd..",
      "..hh.......hh..hhd.",
      "..hh.......hh...hhd",
      "..hh.......hh...hdh",
      "..dd.......dd...ww.",
    ],
  },
  horseLie: {
    colors: { h: "gray", d: "dark", w: "white" },
    art: [
      "..............d....",
      ".............dhd...",
      "............dhhhh..",
      "...........dhhhdhh.",
      ".dhhhhhhhhhhhhhhhww",
      "ddhhhhhhhhhhhh.....",
      "d.hhhhhhhhhhhh.....",
      ".ddhhhhhhhhhdd.....",
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
  goatEat: {
    colors: { w: "white", d: "dark", g: "gray" },
    art: [
      "................",
      "................",
      "w...............",
      ".wwwwwwwwwwwww..",
      "wwwwwwwwwwwwwwg.",
      ".wwwwwwwwwwwwwwg",
      ".wwwwwwwwwww.wwg",
      ".w.w......w.wdww",
      ".w.w......w..wwd",
      ".d.d......d..gg.",
    ],
  },
  goatLie: {
    colors: { w: "white", d: "dark", g: "gray" },
    art: [
      "...........ggg..",
      "..........g.wwg.",
      "............wdww",
      ".wwwwwwwwwwwwwwd",
      "wwwwwwwwwwwww.g.",
      ".wwwwwwwwwwww.g.",
      ".dwwwwwwwwwd....",
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
  pigEat: {
    colors: { p: "pink", m: "magenta", d: "dark" },
    art: [
      "................",
      "...pppppppppp.p.",
      "p.pppppppppppppp",
      ".pppppppppppppdp",
      "pppppppppppppppp",
      ".pppppppppppppmm",
      "..ppp......ppmm.",
      "..pp........pp..",
      "..dd........dd..",
    ],
  },
  pigLie: {
    colors: { p: "pink", m: "magenta", d: "dark" },
    art: [
      "...........p.p..",
      "...pppppppppppp.",
      "p.pppppppppppdpp",
      ".pppppppppppppmm",
      ".pppppppppppppmm",
      ".ddppppppppppdd.",
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
  bell: { colors: { y: "gold", d: "dark" }, art: [".y.", "yyy", "yyy", ".d."] },
  ribbon: { colors: { b: "ribbon", y: "gold" }, art: [".bb.", "bybb", ".bb.", ".bb.", "b..b"] },
  halo: { colors: { y: "gold" }, art: [".yyyyy.", "y.....y", ".yyyyy."] },
  plow: { colors: { d: "wood", g: "gray" }, art: ["d.......", ".d......", "..dddddd", "..dg....", ".gg....."] },
  bird: { colors: { d: "bird", o: "orange" }, art: [".dd..", "ddddo", ".d.d."] },
  pie: { colors: { p: "pie", d: "pieDark" }, art: [".ppp.", "pdppd"] },
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

// ANCHORS are points on the poses of the animals, in pixels of the art of a
// sprite that looks to the right, where the pixel in column c spans c to
// c + 1: the top of the head (a halo, a badge, a word), the ear (the ear
// tag), the neck (the bell), the side (the ribbon), the back (a bird), the
// tail (the plow) and the mouth (it chews).
const COW_UP = { head: [16, 0], ear: [14, 1], neck: [14, 6.5], side: [7.5, 3], back: [7.5, 2], tail: [0, 3], mouth: [17, 5.5] };
const COW_EAT = { head: [16.5, 5], ear: [15, 6], neck: [14.5, 8], side: [7.5, 3], back: [7.5, 2], tail: [0, 3], mouth: [17, 10.5] };
const COW_DOWN = { head: [16, 0], ear: [14, 1], neck: [14, 5], side: [7.5, 3], back: [7.5, 2], tail: [0, 3], mouth: [17, 4.5] };
const HORSE_DOWN = { head: [14.5, 0], ear: [14, 1], neck: [12, 5], side: [7.5, 5], back: [7.5, 4], tail: [0, 5], mouth: [18, 4.5] };
const GOAT_DOWN = { head: [13.5, 0], ear: [12, 1], neck: [12.5, 5], side: [6.5, 4], back: [6.5, 3], tail: [0, 3], mouth: [15, 3.5] };
const PIG_DOWN = { head: [12.5, 0], ear: [11, 0], neck: [12, 5], side: [7.5, 3], back: [7.5, 1], tail: [0, 2], mouth: [15, 4.5] };
export const ANCHORS = {
  cow: COW_UP, cowEat: COW_EAT, cowLie: COW_DOWN, cowSleep: COW_DOWN,
  cowFront: { head: [7, 0], ear: [2, 2], neck: [7, 8], side: [11.5, 7], back: [7, 1], mouth: [7, 7.5] },
  bull: COW_UP, bullEat: COW_EAT, bullLie: COW_DOWN, bullSleep: COW_DOWN,
  bullFront: { head: [8, 0], ear: [4, 2], neck: [8, 9], side: [12.5, 7], back: [8, 1], mouth: [8, 7.5] },
  horse: { head: [14.5, 0], ear: [14, 1], neck: [12, 6.5], side: [7.5, 7], back: [7.5, 6], tail: [0, 7], mouth: [18, 5.5] },
  horseEat: { head: [17, 10], ear: [17, 10], neck: [14, 9], side: [7.5, 7], back: [7.5, 6], tail: [0, 7], mouth: [16.5, 13.5] },
  horseLie: HORSE_DOWN, horseSleep: HORSE_DOWN,
  goat: { head: [13.5, 0], ear: [12, 1], neck: [12.5, 5], side: [6.5, 4], back: [6.5, 3], tail: [0, 2], mouth: [15, 3.5] },
  goatEat: { head: [14.5, 4], ear: [14, 4], neck: [13, 6], side: [6.5, 4], back: [6.5, 3], tail: [0, 2], mouth: [15, 8.5] },
  goatLie: GOAT_DOWN, goatSleep: GOAT_DOWN,
  pig: { head: [12.5, 0], ear: [11, 0], neck: [12, 5.5], side: [7.5, 3], back: [7.5, 1], tail: [0, 2], mouth: [15, 4.5] },
  pigEat: { head: [13.5, 1], ear: [14, 1], neck: [12, 6], side: [7.5, 3], back: [7.5, 1], tail: [0, 2], mouth: [15, 6.5] },
  pigLie: PIG_DOWN, pigSleep: PIG_DOWN,
};

// anchor gives an anchor of a sprite at scale in px, from the top left of
// the sprite, or null. flip is set for a sprite that looks to the left.
export function anchor(name, sprite, scale, flip) {
  const at = ANCHORS[sprite]?.[name];
  if (!at) return null;
  return { x: (flip ? spriteOf(sprite).w - at[0] : at[0]) * scale, y: at[1] * scale };
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
  const brown = mix(pick("ansi11", "ansi3"), pick("ansi8", "dim"), 0.55);
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
    spot: pick("ansi8", "dim"), horn: mix(white, yellow, 0.3), gold: yellow, ribbon: pick("accent"), bird: dim,
    wood: brown, pie: brown, pieDark: mix(brown, pick("chrome", "background"), 0.35), soil: alpha(brown, "aa"),
    scale: pick("muted"), screen: pick("chrome", "background"), screenText: green,
  };
}

// mix mixes two #rrggbb colors: share 0 is a, and 1 is b.
export function mix(a, b, share) {
  const parts = (hex) => [1, 3, 5].map((i) => parseInt(String(hex).slice(i, i + 2), 16) || 0);
  const [x, y] = [parts(a), parts(b)];
  return `#${x.map((value, i) => Math.round(value + (y[i] - value) * share).toString(16).padStart(2, "0")).join("")}`;
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

// --- The cost of a Pi session

// A Pi cow weighs one pound for each dollar that its session cost at API
// prices. Its weight gives its size, and how fast it walks.
export const SIZES = [
  { below: 100, name: "newborn calf", scale: 1.5, speed: 42 },
  { below: 500, name: "calf", scale: 2, speed: 36 },
  { below: 1000, name: "young cow", scale: 2.5, speed: 30 },
  { below: 1500, name: "cow", scale: 3, speed: 24 },
  { below: Infinity, name: "bull", scale: 4, speed: 16, bull: true },
];

// A cow gets a prize at each cost, and keeps the prizes before it.
export const PRIZES = [
  { at: 100, name: "bell", title: "cash cow" },
  { at: 500, name: "ribbon", title: "prize cow" },
  { at: 1000, name: "spots", title: "Wagyu" },
  { at: 1500, name: "halo", title: "holy cow" },
];

// The pen of a Pi cow gets a cow pie for each PIE_COST dollars.
export const PIE_COST = 100;
const MAX_PIES = 24;

// The speed of an animal without a cost, in px a second.
const SPEED = 26;

const thousands = (digits) => digits.replace(/\B(?=(\d{3})+(?!\d))/g, ",");

// dollars writes a cost, such as "$1,757.87".
export function dollars(value) {
  const [whole, cents] = Number(value).toFixed(2).split(".");
  return `$${thousands(whole)}.${cents}`;
}

// pounds writes a weight, such as "1,758 lb".
export const pounds = (value) => `${thousands(String(Math.round(value)))} lb`;

// tagText is the price on the ear tag: "$84", "$1.8k".
export const tagText = (total) => (total < 1000 ? `$${Math.round(total)}` : `$${(total / 1000).toFixed(1)}k`);

export const sizeOf = (total) => SIZES.find((size) => total < size.below);
export const prizesOf = (total) => PRIZES.filter((prize) => total >= prize.at);

// coinStreams gives the number of coin streams over a cow that runs, from
// the cost of the last 10 minutes.
export function coinStreams(recent) {
  const perMinute = (recent ?? 0) / 10;
  if (perMinute >= 1.5) return 3;
  if (perMinute >= 0.5) return 2;
  return perMinute >= 0.05 ? 1 : 0;
}

// costOf gives what a Pi cow gets from the cost of its session, or null for
// another animal and for a cow without a cost.
export function costOf(row) {
  const total = row.cost?.total;
  if (animalOf(row.app) !== "cow" || typeof total !== "number" || !Number.isFinite(total)) return null;
  const recent = row.cost.recent ?? 0;
  const prizes = prizesOf(total);
  return {
    total, recent, size: sizeOf(total), prizes: prizes.map((prize) => prize.name), title: prizes.at(-1)?.title ?? "",
    tag: tagText(total), coins: coinStreams(recent), pies: Math.min(MAX_PIES, Math.floor(total / PIE_COST)),
  };
}

// costWords tells the cost of a cow in its tooltip.
export function costWords(cost) {
  if (!cost) return "";
  const parts = [`${dollars(cost.total)} at API prices`, `${pounds(cost.total)}, a ${cost.size.name}`];
  if (cost.title) parts.push(cost.title);
  if (cost.coins > 0) parts.push(`${dollars(cost.recent / 10)} a minute`);
  return parts.join(" · ");
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

// hash gives a seed from a text.
function hash(text) {
  let value = 7;
  for (const ch of String(text)) value = (value * 31 + ch.charCodeAt(0)) % 2147483647;
  return value;
}

function place(name, x, bottom, extra = {}, scale = P) {
  const s = spriteOf(name);
  return { kind: "sprite", name, x, y: bottom - s.h * scale, w: s.w * scale, h: s.h * scale, scale, ...extra };
}

function bob(from, size, period) {
  return { property: "top", from, to: from - size, period };
}

// The states in which an agent is its animal. A delegating agent is a
// sheepdog, and an agent that waits for background tasks is a hen.
const ANIMAL_STATES = new Set(["needs", "ready", "running", "idle", "pending"]);

// animalSlot places one agent that is its animal, from x in its pen. The
// brain moves the animal. The slot has the things that stay: the bucket of
// an agent that is ready, the furrow of an agent that runs, and the pies of
// a Pi cow.
function animalSlot(row, x, fresh, now) {
  const family = animalOf(row.app);
  const cost = costOf(row);
  const kind = cost?.size.bull ? "bull" : family;
  const scale = cost?.size.scale ?? P;
  const stand = spriteOf(kind);
  const w = stand.w * scale;
  const h = stand.h * scale;
  const items = [];
  const animal = {
    key: row.key, row, kind, family, state: ANIMAL_STATES.has(row.state) ? row.state : "pending", fresh, scale, w, h, cost,
    speed: cost?.size.speed ?? SPEED, focused: Boolean(row.focused), pale: !["needs", "ready", "running", "idle"].includes(row.state),
    changedAt: row.changedAt ?? null, workingSince: row.workingSince ?? null,
  };
  let width = w + 34;
  if (row.state === "running") {
    const plowW = spriteOf("plow").w * scale;
    width = w + 2 * plowW + 44;
    animal.lane = { lo: x + plowW + 4, hi: x + width - w - plowW - 4 };
    // The furrow gets longer while the agent runs: all of the slot after 15
    // minutes.
    const minutes = row.workingSince ? Math.max(0, now - row.workingSince) / 60000 : 0;
    items.push({
      kind: "rect", id: `furrow-${row.key}`, grow: true, x: x + 4, y: GROUND - 3,
      w: Math.max(4, Math.round((width - 8) * Math.min(1, minutes / 15))), h: 3, color: "soil",
    });
  }
  if (row.state === "ready") {
    const bucket = place("bucket", x + w + 30, GROUND);
    items.push(bucket, { kind: "badge", x: bucket.x + 2, y: bucket.y - 22, state: "ready", fresh });
    animal.bucket = bucket.x - w - 2;
    width = w + 30 + bucket.w + 10;
  }
  animal.home = animal.bucket ?? x + Math.round((width - w) / 2);
  if (cost?.pies > 0) {
    const random = seeded(hash(row.key));
    const pieW = spriteOf("pie").w * 2;
    let last = null;
    for (let i = 0; i < cost.pies; i++) {
      last = place("pie", x + Math.round(random() * (width - pieW)), GROUND + 1, {}, 2);
      items.push(last);
    }
    // Two flies circle the newest pie.
    [[0, 820, 610], [5, 690, 940]].forEach(([dx, periodX, periodY]) => {
      const fx = last.x + 2 + dx;
      const fy = last.y - 8;
      items.push({ kind: "rect", x: fx, y: fy, w: 2, h: 2, color: "bird", loops: [
        { property: "left", from: fx - 3, to: fx + 4, period: periodX }, { property: "top", from: fy, to: fy - 5, period: periodY },
      ] });
    });
  }
  return { items, width, animal, hits: [] };
}

// thingSlot places an agent that is not its animal: a sheepdog with one
// sheep for each delegated agent, or a hen on one egg for each background
// task.
function thingSlot(row, index, x) {
  const items = [];
  let width = 60;
  if (row.state === "delegating") {
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
  } else {
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
  }
  if (row.focused) items.push({ kind: "rect", x: x + 4, y: GROUND + 2, w: width - 26, h: 2, color: "accent" });
  return { items, width, animal: null, hits: [{ kind: "hit", x, y: GROUND - 70, w: width, h: 72, row }] };
}

// chickItems places the chicks of the background tasks that ended.
function chickItems(x, chicks) {
  return Array.from({ length: Math.min(chicks, 3) }, (_, k) => {
    const chick = place("chick", x + k * (7 * P + 3), GROUND);
    chick.loops = [bob(chick.y, 3, 460 + k * 70)];
    return chick;
  });
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
//
// items are in the scene, and do not move. Each pen has its own items,
// hits and animals, at places in the pen, so that a pen can move without
// moving its animals. The brain moves the animals in their pens: an animal
// has its lane for a walk, the place at the gate when it needs you, and the
// place at its bucket when it is ready.
export function barnScene({ workspaces = [], turns = [], pulls = [], hatched = [], now = Date.now(), width = 0, dark = false }) {
  const items = [];
  const hits = [];
  const pens = [];
  const labels = [];
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

  // The cattle scale weighs the Pi cows: one pound for each dollar. It
  // shows the herd, or the cow under the pointer.
  const herd = agentRows.map(costOf).filter(Boolean);
  let scale = null;
  if (herd.length > 0) {
    scale = { x, y: GROUND - 47, w: 76, h: 17, herd: herd.reduce((sum, cost) => sum + cost.total, 0) };
    items.push(
      { kind: "rect", x: x + 8, y: GROUND - 30, w: 3, h: 26, color: "scale" },
      { kind: "rect", x: x + 4, y: GROUND - 5, w: 68, h: 5, color: "scale" },
      { kind: "rect", x: x + 6, y: GROUND - 7, w: 64, h: 2, color: "fence" },
    );
    hits.push({
      kind: "hit", x, y: scale.y, w: scale.w, h: GROUND - scale.y,
      tooltip: "Cattle scale: a Pi cow weighs one pound for each dollar that its session cost at API prices. Point at a cow to weigh it.",
    });
    x += scale.w + 22;
  }

  // The pens, in the order of the sidebar: the most urgent agents first.
  const chicksOf = new Map();
  for (const hatch of hatched) {
    if (now - hatch.at < CHICK_LIFE) chicksOf.set(hatch.agentId, (chicksOf.get(hatch.agentId) ?? 0) + hatch.count);
  }
  for (const workspace of workspaces) {
    const rows = orderRows(workspace.rows).filter((row) => row.kind === "agent");
    if (rows.length === 0) continue;
    const pen = { id: workspace.id, x, items: [], hits: [], animals: [] };
    let slotX = 12;
    rows.forEach((row, index) => {
      const slot = row.state === "delegating" || row.state === "background"
        ? thingSlot(row, index, slotX)
        : animalSlot(row, slotX, fresh.has(row.key), now);
      pen.items.push(...slot.items);
      pen.hits.push(...slot.hits);
      if (slot.animal) pen.animals.push({ ...slot.animal, penId: pen.id });
      slotX += slot.width;
      const chicks = chicksOf.get(row.agentId) ?? 0;
      if (chicks > 0) {
        pen.items.push(...chickItems(slotX, chicks));
        slotX += Math.min(chicks, 3) * (7 * P + 3) + 4;
      }
    });
    pen.w = slotX + 8;
    // The animals walk in all of the pen, and the ones that need you wait at
    // the gate, next to the barn.
    let gate = 10;
    for (const animal of pen.animals) {
      animal.pen = { lo: 8, hi: Math.max(8, pen.w - 8 - animal.w) };
      if (animal.state === "needs") {
        animal.gate = Math.min(gate, animal.pen.hi);
        gate += Math.round(animal.w * 0.8);
      }
    }
    const fence = [];
    for (let postX = 0; postX <= pen.w - 4; postX += 24) fence.push({ kind: "rect", x: postX, y: GROUND - 24, w: 4, h: 24, color: "fence" });
    fence.push(
      { kind: "rect", x: pen.w - 4, y: GROUND - 24, w: 4, h: 24, color: "fence" },
      { kind: "rect", x: 0, y: GROUND - 20, w: pen.w, h: 3, color: "fence" },
      { kind: "rect", x: 0, y: GROUND - 11, w: pen.w, h: 3, color: "fence" },
    );
    pen.items.unshift(...fence);
    pens.push(pen);
    labels.push({ kind: "label", x: x + 4, y: GROUND + 2, w: pen.w - 8, text: workspace.title, active: workspace.active });
    x += pen.w + 18;
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
  return { width: sceneWidth, height: HEIGHT, items: [...sky, ...ground, ...items], pens, labels, hits, scale };
}
