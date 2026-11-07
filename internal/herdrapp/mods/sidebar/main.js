// Galpon sidebar of the Galpon layout. `galpon herdr-app install` writes this
// mod and the layout. One mod draws these blocks of the layout, which
// props().role selects:
//
//   header         the Galpon name and buttons. The block moves the window.
//   agents         each workspace with its open agents, then its other tabs
//   pull-requests  the pull requests that need someone
//   status         the status bar below the terminals
//   barn           the barn below the terminals: the agents as animals
//   menu           the right-click menu of a row, in a popup
//
// Without a role and with props().pane, it is the header of a pane. The
// pane header and the status bar show the app status with the same marks as
// the sidebar. The agents block shares its turns with the status bar, and
// the agents and pull request blocks share their Galpon facts with the barn.
// So the barn reads nothing from Galpon itself.
//
// The app gives the spaces, tabs, terminals and agent status, live.
// `galpon herdr-app sidebar` gives the Galpon names, the delegated work and
// the pull requests. It reads Galpon without a GitHub call.
//
// On a screen, another computer runs Galpon: the host. The mod then runs
// galpon on the host through herdr-core, and names and focuses here what
// Galpon opens there.

import { View, div, image, svg } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import {
  agents, appHome, closeOverlay, exec, focused, home, keys, layout, nextCommand, openOverlay, panes, playSound, props, readTextFile, run,
  setAgentOrder, setShared, settings, shared, spaces, terminals, theme, window as appWindow,
} from "herdr";
import {
  MARKS, RANK, buildWorkspaces, commandName, duration, freshOf, keepOrder, lookAt, menuItems, menuTarget, nextItem,
  nextTurn, openAgentPanes, orderRows, pullRequestSections, rowWords, signature, stateOf, summary, taskCount, turnOrder,
} from "./sidebar.js";
import {
  CHICK_LIFE, GROUND, HEIGHT, P, SOUNDS, alpha, anchor, barnScene, barnSounds, costWords, hatchesOf, night, palette, pounds,
  spriteOf,
} from "./barn.js";
import { createBrain, hover, planFor, poseOf, think } from "./brain.js";

const GALPON = __GALPON__;
// HOST is the herdr-core machine name of the computer that runs Galpon, or
// null when Galpon runs on this computer.
const HOST = __HOST__;
// BUILD is the build of the galpon that installed this mod. On a screen,
// SCREEN_GALPON is the galpon of this computer. It updates this computer when
// Galpon on the host has another build, and the app then loads the new mods.
// It is null on the host.
const BUILD = __BUILD__;
const SCREEN_GALPON = __SCREEN_GALPON__;
// The mod asks again for an update to the same build after this time.
const UPDATE_RETRY = 10 * 60 * 1000;
// On a screen, this is how long the mod waits until this window shows a
// terminal that Galpon opened on the host. The window adds it after its next
// core listing, in about a second.
const SCREEN_WAIT = 10000;

// lastUpdate is the update that this mod asked for last: { build, at }. The
// mod also keeps it in localStorage, for the next copy after a reload.
let lastUpdate = null;

// askUpdate starts an update of this screen when the result of the host has
// another build than this mod. The update runs in its own process. A failed
// request does not change the result.
async function askUpdate(out) {
  const build = out?.build;
  if (!HOST || !SCREEN_GALPON || typeof build !== "string" || !build || build === BUILD) return;
  if (!lastUpdate) {
    try {
      lastUpdate = JSON.parse(localStorage.getItem("galpon.update") ?? "null");
    } catch {
      lastUpdate = null;
    }
  }
  if (lastUpdate?.build === build && Date.now() - lastUpdate.at < UPDATE_RETRY) return;
  lastUpdate = { build, at: Date.now() };
  try {
    localStorage.setItem("galpon.update", JSON.stringify(lastUpdate));
  } catch {
    // The mod keeps it in memory.
  }
  try {
    await exec(SCREEN_GALPON, ["screen", "update"]);
  } catch {
    // The mod asks again after UPDATE_RETRY.
  }
}

// The sidebar reads Galpon this often, and also when an open agent changes.
const READ_EVERY = 30000;
// While Galpon gets pull requests from GitHub, the sidebar reads more often,
// for at most a minute.
const REFRESH_EVERY = 1500;
const REFRESH_LIMIT = 60000;
// The round marks are pictures, because a font draws these signs too small
// next to the text. svg() tints a picture with the text color. A mark
// without a picture is its sign.
const glyph = (look, size) => (look.icon ? svg(`mods/galpon-sidebar/${look.icon}.svg`).size(size).flex_none() : look.mark);
const SPINNER = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const ROW = 28;
const WIDTH_KEY = "galpon.sidebar.width";
// The right-click menu is a popup. It closes at once, so it gives slow work
// to the block that owns the row: it writes the request here and sends the
// command "galpon-sidebar.request".
const REQUEST = "galpon.sidebar.request";
const REQUEST_LIFE = 10000;
const MENU_WIDTH = 248;
// The turns that the person looked at, for each block with turns. The
// blocks keep them, because a pull request can wait for days.
const LOOKED = { agents: "lookedAgents", "pull-requests": "lookedPullRequests" };
// The agents block shares its turns here: the status bar shows their counts,
// and takes the jump key while the sidebar is hidden.
const TURNS = "galpon.sidebar.turns";
// The key command that goes to the next agent turn.
const NEXT_TURN = "next-turn";
// The agents block shares its Galpon facts and the recent hatches here, and
// the pull request block shares the pull requests that need someone. It
// shares the facts again after this time, so that the barn knows that they
// are new.
const FACTS = "galpon.sidebar.facts";
const PULLS = "galpon.sidebar.pulls";
const FACTS_AGAIN = 5 * 60 * 1000;
// The layout node of the barn, and the picture of the windmill blades.
const BARN = "galpon-barn";
const WINDMILL = "mods/galpon-sidebar/barn-windmill.svg";
// The barn reads the sound settings this often.
const SOUNDS_EVERY = 30000;

// showOnScreen names and focuses a terminal that Galpon opened on the host,
// as Galpon does in the window of the host. place comes from Galpon: the
// terminal address and the names of its tab and space on the host. It
// returns false when this window did not show the terminal in time.
async function showOnScreen(place, cx) {
  const terminal = `${HOST}/${place.terminal}`;
  const begin = Date.now();
  while (Date.now() - begin < SCREEN_WAIT) {
    const pane = panes().find((item) => item.terminal === terminal);
    if (pane) {
      const space = spaces().find((item) => item.tabs.some((tab) => tab.id === pane.tab));
      if (place.tabName) run("tab.rename", { id: pane.tab, name: place.tabName });
      if (space && place.spaceName) run("space.rename", { id: space.id, name: place.spaceName });
      run(place.agent ? "agent.focus" : "pane.focus", { terminal });
      return true;
    }
    await cx.sleep(200);
  }
  return false;
}

function find(node, id) {
  if (node?.id === id) return node;
  for (const child of node?.children ?? []) {
    const found = find(child, id);
    if (found) return found;
  }
  return null;
}

function pathTo(node, id) {
  if (!node) return null;
  if (node.id === id) return [node];
  for (const child of node.children ?? []) {
    const path = pathTo(child, id);
    if (path) return [node, ...path];
  }
  return null;
}

// hiddenInLayout tells whether the layout hides the node id: the node or a
// node above it is hidden. A node that the layout does not have is not
// hidden.
const hiddenInLayout = (id) => Boolean(pathTo(layout(), id)?.some((node) => node.hidden));

// Narrower than 160 px, the sidebar is a rail of state marks.
const narrow = () => (find(layout(), "sidebar")?.size?.px ?? Infinity) < 160;

// The rail, or back to the width that the sidebar had.
function toggleSidebar() {
  const width = find(layout(), "sidebar")?.size?.px;
  if (!narrow()) setShared(WIDTH_KEY, width);
  run("layout.resize", { id: "sidebar", px: narrow() ? (shared(WIDTH_KEY) ?? 300) : appWindow()?.platform === "macos" ? 76 : 44 });
}

// tones gives the theme color of each tone of MARKS. Yellow asks for
// attention, green is ready, and red says that something went wrong. Yellow
// is also the color of work, as in the terminal command center: the filled
// badge and the band show "needs you".
function tones(t) {
  return { attention: t.working, ready: t.done, working: t.working, error: t.ansi1 ?? t.blocked, quiet: t.idle ?? t.dim };
}

// animate adds loops to el. A loop has a property, from, to, a period and an
// easing.
function animate(el, loops) {
  return loops.reduce((out, loop) => out.loop(loop.property, {
    from: loop.from, to: loop.to, period: loop.period, easing: loop.easing ?? "ease-in-out",
  }), el);
}

// offset puts el at x, y, and its loops of left and top move it only by an
// offset from there. The app does not apply its zoom to the values of a
// loop, so the offsets must stay small.
function offset(x, y, el, loops = []) {
  return div().absolute().left(x).top(y).child(animate(el.absolute().left(0).top(0), loops));
}

// stepPeriod is the period of the steps of a walking animal: a calf steps
// fast, and a heavy cow slowly.
function stepPeriod(animal) {
  const scale = animal.cost?.size.scale ?? 3;
  if (scale <= 2) return 300;
  return scale >= 3.5 ? 560 : 400;
}

// badge is the mark of a turn of the person in a square. A new turn has a
// filled square with a dark mark. A turn that the person saw has an outline
// in the color, with the mark in the color. A count makes it wider.
function badge(t, color, mark, fresh, count = null) {
  return h_flex().h(16).min_w(16).px(count === null ? 0 : 4).gap(3).flex_none().items_center().justify_center()
    .rounded(3).text_size(10.5).font_bold()
    .when(fresh, (el) => el.bg(color).text_color(t.chrome))
    .when(!fresh, (el) => el.border(1).border_color(color).text_color(color))
    .child(mark)
    .when(count !== null, (el) => el.child(String(count)));
}

// bar is the left edge of a row, as high as the row: the color of a turn,
// or empty.
function bar(color) {
  return div().w(3).flex_none().self_stretch()
    .when(Boolean(color), (el) => el.bg(color));
}

// band is the faint color of a turn behind a row.
const band = (color) => `${color}24`;

// The pane header shows the program icon of the app's program table.
function programIcon(program, size, color) {
  if (!program?.icon) return null;
  return program.tint ? svg(program.icon).size(size).flex_none().text_color(color) : image(program.icon).w(size).h(size).flex_none();
}

function folder(cwd) {
  if (!cwd) return "";
  const h = home();
  return cwd === h ? "~" : cwd.startsWith(`${h}/`) ? `~${cwd.slice(h.length)}` : cwd;
}

// keyLabel names a key binding as the person presses it, or "".
function keyLabel(binding) {
  if (!binding) return "";
  const mac = appWindow()?.platform === "macos";
  const names = { cmd: mac ? "⌘" : "Ctrl+", ctrl: mac ? "⌃" : "Ctrl+", shift: mac ? "⇧" : "Shift+", alt: mac ? "⌥" : "Alt+", option: mac ? "⌥" : "Alt+" };
  const parts = binding.keys.endsWith("--") ? [...binding.keys.slice(0, -2).split("-"), "-"] : binding.keys.split("-");
  return parts.map((part) => names[part.toLowerCase()] ?? part.toUpperCase()).join("");
}

// paletteHint names the key of the command palette as it is bound.
function paletteHint() {
  const key = keyLabel(keys().find((binding) => binding.section === "global" && binding.action === "app.palette"));
  return key ? `${key}  commands` : "commands";
}

// turnHint explains the turn counter, with the jump key as it is bound.
function turnHint() {
  const key = keyLabel(keys().find((binding) => binding.action === `galpon-sidebar.${NEXT_TURN}`));
  return `Go to the next turn: the agents that need you first, the oldest first${key ? ` · ${key}` : ""}`;
}

const message = (error) => String(error?.message ?? error);
// Galpon errors start with a lowercase letter; the sidebar shows sentences.
const sentence = (text) => (text ? String(text)[0].toUpperCase() + String(text).slice(1) : "");

export default class GalponSidebar extends View {
  init(_props, cx) {
    this.role = props()?.role ?? (props()?.pane ? "pane" : "agents");
    this.hovered = false;
    if (this.role === "status") {
      // A key command reaches only shown copies. So the status bar takes the
      // jump key only while the agents block is hidden, with the turns that
      // the agents block shares.
      cx.spawn(async (cx) => {
        for (;;) {
          if (commandName(await nextCommand()) === NEXT_TURN && hiddenInLayout("galpon-agents")) this.jump(shared(TURNS)?.turns ?? []);
        }
      });
      return;
    }
    if (this.role === "header" || this.role === "pane") return;
    if (this.role === "barn") {
      this.width = 0;
      this.sounds = null;
      this.soundBusy = false;
      this.soundError = "";
      this.brain = createBrain();
      this.animals = [];
      this.hoverKey = null;
      cx.spawn(async (cx) => {
        for (;;) {
          await this.readSounds();
          cx.notify();
          await cx.sleep(SOUNDS_EVERY);
        }
      });
      // The brain runs only while the barn shows. It reads the animals of
      // the last draw, so a new state shows in a second.
      cx.spawn(async (cx) => {
        for (;;) {
          if (hiddenInLayout(BARN) || this.animals.length === 0) {
            await cx.sleep(1500);
            continue;
          }
          const { changed, next } = think(this.brain, this.animals, Date.now());
          if (changed) cx.notify();
          await cx.sleep(Math.min(1000, Math.max(40, next - Date.now())));
        }
      });
      return;
    }
    if (this.role === "menu") {
      this.items = menuItems(props()?.target);
      this.cursor = nextItem(this.items, -1, 1);
      this.focusHandle = cx.focus_handle();
      this.focusHandle.focus();
      return;
    }
    this.facts = null;
    this.error = "";
    this.dirty = true;
    this.readAt = 0;
    this.agentIds = [];
    this.seen = null;
    this.order = new Map();
    this.hovering = false;
    this.held = null;
    this.looked = new Map(JSON.parse(localStorage.getItem(LOOKED[this.role]) ?? "[]"));
    this.savedLooked = JSON.stringify([...this.looked]);
    this.published = null;
    this.folded = new Set(JSON.parse(localStorage.getItem("folded") ?? "[]"));
    this.busy = "";
    this.notice = null;
    this.pendingMove = "";
    this.refreshAsked = 0;
    this.fastSince = 0;
    this.hatched = [];
    this.sharedFacts = null;
    this.sharedFactsAt = 0;
    this.sharedPulls = null;
    if (this.role === "agents") this.scan();
    // A request from before this copy started is not for it.
    this.handled = shared(REQUEST)?.seq ?? null;
    cx.spawn(async (cx) => {
      for (;;) {
        this.handle(cx);
        if (this.role === "agents") this.publish();
        if (this.dirty || Date.now() - this.readAt >= this.every()) await this.read(cx);
        await cx.sleep(1000);
      }
    });
    cx.spawn(async (cx) => {
      for (;;) {
        const command = commandName(await nextCommand());
        if (command === "request") this.handle(cx);
        if (command === NEXT_TURN && this.role === "agents") this.jump(turnOrder(this.scan().flatMap((workspace) => workspace.rows)));
      }
    });
  }

  // --- Turns

  // unseenOf gives the keys of the new turns of rows. It keeps what the
  // person looked at.
  unseenOf(rows) {
    const unseen = lookAt(rows, this.looked);
    this.saveLooked();
    return unseen;
  }

  saveLooked() {
    const text = JSON.stringify([...this.looked]);
    if (text === this.savedLooked) return;
    this.savedLooked = text;
    localStorage.setItem(LOOKED[this.role], text);
  }

  // publish shares the agent turns, in jump order, and the number of agents
  // that run. It sets the shared value only when it changes, because a
  // shared value redraws every mod.
  publish() {
    const rows = this.scan().flatMap((workspace) => workspace.rows);
    const unseen = this.unseenOf(rows);
    const value = {
      turns: turnOrder(rows).map((row) => ({ key: row.key, pane: row.pane, state: row.state, fresh: unseen.has(row.key) })),
      running: summary(rows).running,
    };
    const text = JSON.stringify(value);
    if (text === this.published) return;
    this.published = text;
    setShared(TURNS, value);
  }

  // jump goes to the next turn of turns: the turn after the tab that has
  // the focus, else the first turn.
  jump(turns) {
    const turn = nextTurn(turns, focused()?.tab ?? null);
    if (turn?.pane) run("pane.focus", { pane: turn.pane });
  }

  // turnCounter draws the counts of turns. A click goes to the next turn.
  turnCounter(t, id, turns) {
    const fresh = new Set(turns.filter((turn) => turn.fresh).map((turn) => turn.key));
    return h_flex().id(id).flex_none().h(22).px(4).rounded(3).items_center().cursor_pointer()
      .hover((s) => s.bg(t.hover))
      .tooltip(turnHint())
      .on_click((_e, cx) => {
        cx.stop_propagation();
        this.jump(turns);
      })
      .child(this.counts(t, { ...summary(turns), running: 0 }, freshOf(turns, fresh)));
  }

  // --- Menu

  // openMenu shows the menu of a row at the pointer.
  openMenu(target) {
    const items = menuItems(target);
    if (items.length === 0) return;
    const height = 42 + items.reduce((sum, item) => sum + (item.separator ? 9 : item.off || item.hint ? 46 : 30), 0);
    openOverlay({ mod: "galpon-sidebar", kind: "popup", width: MENU_WIDTH, height, props: { role: "menu", target } });
  }

  // choose runs a menu item and closes the menu.
  choose(item) {
    if (!item || item.separator || item.off) return;
    const { target, overlay } = props();
    if (item.galpon) {
      openOverlay({
        mod: "galpon", kind: "dialog", modal: true, width: 0.82, height: 0.8,
        props: { start: "action", action: item.galpon, kind: target.kind, id: target.id },
      });
    } else if (item.app) {
      run(item.app, item.args ?? {});
    } else if (item.request) {
      setShared(REQUEST, { seq: `${Date.now()}-${Math.random()}`, at: Date.now(), action: item.request, target });
      run("galpon-sidebar.request");
    }
    closeOverlay(overlay);
  }

  menuKey(event, cx) {
    if (event.key === "up") this.cursor = nextItem(this.items, this.cursor, -1);
    else if (event.key === "down" || event.key === "tab") this.cursor = nextItem(this.items, this.cursor, 1);
    else if (event.key === "enter") this.choose(this.items[this.cursor]);
    else if (event.key === "escape") closeOverlay(props().overlay);
    else return;
    cx.stop_propagation();
    cx.notify();
  }

  menu(t) {
    const target = props()?.target ?? {};
    const rows = this.items.map((item, index) => {
      if (item.separator) return div().h(9).flex_none().mx(10).border_b(1).border_color(t.border).mb(4);
      const selected = index === this.cursor;
      return v_flex().id(`galpon-menu-${item.id}`).flex_none().px(12).py(item.off || item.hint ? 5 : 0).justify_center()
        .h(item.off || item.hint ? 46 : 30)
        .when(selected, (el) => el.bg(t.selected))
        .when(!item.off, (el) => el.cursor_pointer().hover((s) => s.bg(t.hover)).on_click(() => this.choose(item)))
        .child(div().truncate().text_color(item.off ? t.dim : t.foreground).child(item.label))
        .when(Boolean(item.off || item.hint), (el) => el.child(div().truncate().text_size(11.5).text_color(t.muted).child(item.off || item.hint)));
    });
    return v_flex().id("galpon-menu").track_focus(this.focusHandle).size_full().min_h(0).py(5).bg(t.popover).text_size(13)
      .on_key_down((event, cx) => this.menuKey(event, cx))
      .child(div().h(28).flex_none().px(12).flex().items_center().truncate().text_size(11.5).font_semibold().text_color(t.dim)
        .child(target.title ?? ""))
      .children(rows);
  }

  // handle runs a request of the menu once, in the block that owns its row.
  handle(cx) {
    const request = shared(REQUEST);
    if (!request || request.seq === this.handled || Date.now() - request.at > REQUEST_LIFE) return;
    const mine = this.role === "pull-requests" ? request.target?.type === "pull-request" : request.target?.type === "agent";
    if (!mine) return;
    this.handled = request.seq;
    if (this.role === "agents" && request.action === "close") this.closeAgent(request.target, cx);
    if (this.role === "pull-requests") {
      const item = this.facts?.pullRequests?.find((candidate) => candidate.id === request.target.key);
      if (request.action === "open" && item) this.openPullRequest(item, openAgentPanes(panes(), terminals()), cx);
      if (request.action === "browse") {
        if (item) this.look(item);
        this.browse(request.target.url, cx);
      }
      if (request.action === "refresh") this.refresh(cx);
    }
    cx.notify();
  }

  // closeAgent closes the terminal of an idle agent through Galpon. The row
  // shows the work, and an error stays under the row.
  closeAgent(target, cx) {
    if (this.busy) return;
    this.busy = target.key;
    this.notice = null;
    cx.spawn(async (cx) => {
      let out;
      try {
        out = await this.galpon(["close", target.id]);
      } catch (error) {
        out = { error: message(error) };
      }
      this.busy = "";
      if (out.error) this.notice = { key: target.key, text: sentence(out.error) };
      cx.notify();
    });
  }

  // --- Galpon

  async galpon(args) {
    const result = HOST
      ? await exec(GALPON, ["herdr-app", "--screen", ...args], { on: HOST })
      : await exec(GALPON, ["herdr-app", ...args]);
    const text = (result.stdout ?? "").trim();
    if (!text) throw new Error((result.stderr ?? "").trim() || `galpon exited with code ${result.code}`);
    const out = JSON.parse(text);
    await askUpdate(out);
    return out;
  }

  every() {
    const refreshing = Boolean(this.facts?.refreshing) || this.refreshAsked > 0;
    if (!refreshing) {
      this.fastSince = 0;
      return READ_EVERY;
    }
    if (!this.fastSince) this.fastSince = Date.now();
    return Date.now() - this.fastSince < REFRESH_LIMIT ? REFRESH_EVERY : READ_EVERY;
  }

  async read(cx) {
    this.dirty = false;
    this.readAt = Date.now();
    const args = this.role === "agents" ? ["agents", ...this.agentIds] : ["pull-requests"];
    try {
      const out = await this.galpon(["sidebar", ...args]);
      if (out.error) {
        this.error = sentence(out.error);
      } else {
        const before = this.facts?.agents ?? null;
        this.facts = out;
        this.error = "";
        if (this.role === "agents") this.shareFacts(before, cx);
        if (this.role === "pull-requests") this.sharePulls();
      }
    } catch (error) {
      this.error = sentence(message(error));
    }
    // A refresh that the person asked for ends when Galpon has the new data.
    if (this.refreshAsked && Date.now() - this.refreshAsked > 3000 && !this.facts?.refreshing) this.refreshAsked = 0;
    cx.notify();
  }

  fold(key) {
    if (this.folded.has(key)) this.folded.delete(key);
    else this.folded.add(key);
    localStorage.setItem("folded", JSON.stringify([...this.folded]));
  }

  render(cx) {
    const t = theme();
    if (this.role === "header") return this.header(t);
    if (this.role === "pane") return this.paneHeader(t);
    if (this.role === "status") return this.statusBar(t);
    if (this.role === "menu") return this.menu(t);
    if (this.role === "pull-requests") return this.pullRequests(t);
    if (this.role === "barn") return this.barn(t);
    return this.workspaces(t);
  }

  // --- Header

  button(id, child, tip, action) {
    const t = theme();
    return div().id(id).size(26).flex_none().flex().items_center().justify_center().text_color(t.muted)
      .hover((s) => s.bg(t.hover).text_color(t.foreground)).cursor_pointer()
      .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
      .tooltip(tip).on_click(action).child(child);
  }

  header(t) {
    const rail = narrow();
    const w = appWindow();
    const icon = (name) => svg(`mods/workspace-chrome/${name}.svg`).size(15);
    return h_flex().size_full().bg(t.chrome).border_b(1).border_color(t.border)
      .pl(rail ? 0 : 14).pr(rail ? 0 : 6).gap(2).items_center().text_size(13.5)
      .when(rail, (el) => el.justify_center())
      .when(w?.platform === "macos" && !rail, (el) => el.pl(80))
      .when(!rail, (el) => el
        .child(h_flex().flex_1().min_w(0).gap(8).items_center()
          .child(div().flex_none().text_color(t.accent).child("⌂"))
          .child(div().min_w(0).truncate().font_semibold().text_color(t.foreground).child("Galpon")))
        .child(this.button("galpon-command-center", icon("search"), "Command center · Ctrl+K", () => run("mod.open", { name: "galpon" })))
        .child(this.button("galpon-new-agent", icon("plus"), "New agent · Ctrl+Alt+N", () => run("mod.open", { name: "galpon-new-agent" })))
        .child(this.button("galpon-settings", icon("settings"), "Settings", () => run("app.settings"))))
      .when(!(rail && w?.platform === "macos"), (el) => el
        .child(this.button("galpon-rail", icon("sidebar"), rail ? "Open the sidebar" : "Fold the sidebar", toggleSidebar)))
      .when(Boolean(w?.buttons) && !rail, (el) => el
        .child(this.button("galpon-minimize", "−", "Minimize", () => run("window.minimize")))
        .child(this.button("galpon-maximize", "□", "Maximize", () => run("window.maximize")))
        .child(this.button("galpon-close", svg("mods/pane/close.svg").size(13), "Close", () => run("window.close"))));
  }

  // --- Pane header and status bar

  // paneHeader is the header of one pane: the program icon, the name, the
  // folder and the state mark. While the pointer is on it, buttons split,
  // zoom and close the pane. A click focuses the pane.
  paneHeader(t) {
    const p = panes().find((candidate) => candidate.pane === props()?.pane);
    if (!p) return div();
    // The title of a mod pane is the name of its mod folder. The name of its
    // tab says more, and the mod can set it.
    const tab = p.mod ? spaces().flatMap((space) => space.tabs ?? []).find((candidate) => candidate.id === p.tab) : null;
    const name = tab?.name || ((p.program?.agent || p.program?.shell) && p.program.name) || p.title || p.program?.name || "shell";
    const state = p.status ? stateOf(p.status).state : "";
    const button = (id, icon, command, args) => div().id(id).p(3).rounded(4).text_color(t.dim)
      .hover((s) => s.bg(t.hover).text_color(t.foreground))
      .on_click((_e, cx) => {
        cx.stop_propagation();
        run(command, args);
      })
      .child(svg(`mods/pane/${icon}.svg`).size(14));
    const pic = programIcon(p.program, 14, t.dim);
    return h_flex().id(`galpon-pane-${p.pane}`).size_full().px(10).gap(8).items_center()
      .bg(t.background).border_b(1).border_color(t.border).text_size(12)
      .on_hover((hovered, cx) => {
        this.hovered = hovered;
        cx.notify();
      })
      .on_click(() => run("pane.focus", { pane: p.pane }))
      .when(Boolean(pic), (el) => el.child(pic))
      .child(div().flex_none().truncate().max_w(260).font_medium().text_color(t.foreground).child(name))
      .child(div().flex_1().truncate().text_color(t.dim).child(folder(p.cwd)))
      .when(state === "running" || Boolean(MARKS[state]?.turn), (el) => el.child(div().flex_none()
        .tooltip(MARKS[state].words).child(this.mark(t, state, { spin: true }))))
      .when(this.hovered, (el) => el
        .child(button(`galpon-pane-right-${p.pane}`, "split-right", "pane.split", { direction: "right", pane: p.pane }))
        .child(button(`galpon-pane-down-${p.pane}`, "split-down", "pane.split", { direction: "down", pane: p.pane }))
        .child(button(`galpon-pane-zoom-${p.pane}`, p.zoomed ? "unzoom" : "zoom", "pane.zoom", { pane: p.pane }))
        .child(button(`galpon-pane-close-${p.pane}`, "close", "pane.close", { pane: p.pane })));
  }

  // statusBar shows the machine and branch of the shown space, the number
  // of agents with the counts of their states, and the palette key.
  statusBar(t) {
    const space = spaces().find((candidate) => candidate.active);
    const list = agents();
    // The turns come from the agents block, so that the counts agree with
    // the sidebar. Before it shares them, the app status gives the counts.
    const published = shared(TURNS);
    const turns = published?.turns ?? list.map((agent) => ({ key: agent.tab, pane: agent.pane, state: stateOf(agent.status).state, fresh: true }))
      .filter((turn) => MARKS[turn.state]?.turn);
    const running = published ? published.running ?? 0 : list.filter((agent) => agent.status === "working").length;
    const label = (icon, text) => h_flex().gap(6).items_center().child(svg(`mods/workspace-chrome/${icon}.svg`).size(13)).child(text);
    return h_flex().size_full().px(14).gap(16).items_center().bg(t.chrome).border_t(1).border_color(t.border)
      .text_size(11.8).text_color(t.muted)
      .child(label(space?.machine ? "server" : "monitor", space?.machine ?? "This computer"))
      .when(Boolean(space?.branch), (el) => el.child(label("branch", space.branch)))
      .child(h_flex().gap(8).items_center().child(`${list.length} agents`)
        .when(turns.length > 0, (el) => el.child(this.turnCounter(t, "galpon-status-turns", turns)))
        .when(running > 0, (el) => el.child(this.counts(t, { needs: 0, ready: 0, running }))))
      .child(div().flex_1())
      .when(Boolean(find(layout(), BARN)), (el) => el.child(this.barnMark(t)))
      .child(div().id("galpon-palette").cursor_pointer().text_color(t.dim).on_click(() => run("app.palette")).child(paletteHint()));
  }

  // --- Workspaces and agents

  // scan reads the live app data. A Galpon agent that opens, closes or
  // changes its status makes the sidebar read Galpon again: its name, and
  // whether it waits for delegated work.
  scan() {
    const all = buildWorkspaces({
      spaces: spaces(), panes: panes(), terminals: terminals(), agents: agents(), focused: focused(), facts: this.facts, host: HOST,
    });
    const seen = signature(all);
    if (seen !== this.seen) {
      this.seen = seen;
      this.agentIds = [...new Set(all.flatMap((workspace) => workspace.rows.map((row) => row.agentId).filter(Boolean)))];
      this.dirty = true;
    }
    return all;
  }

  // Looking at an agent that finished turns "done" into "idle". The agent
  // keeps its place until you go to another tab, so that it does not move
  // away under the pointer.
  hold(rows) {
    const current = rows.find((row) => row.focused && row.kind === "agent");
    if (!current) {
      this.held = null;
      return;
    }
    if (this.held?.key !== current.key || current.state !== "idle") this.held = { key: current.key, state: current.state };
  }

  rank(row) {
    if (this.held?.key === row.key && row.state === "idle") return RANK[this.held.state] ?? RANK.idle;
    return RANK[row.state] ?? 0;
  }

  // mark draws the mark of a state: a badge for a turn, filled when it is
  // new, the spinner for an agent that works, else the mark in its tone.
  mark(t, state, { fresh = false, spin = false, busy = false } = {}) {
    const look = MARKS[state] ?? MARKS.idle;
    const color = tones(t)[look.tone];
    const box = div().w(16).flex_none().flex().justify_center();
    if (busy) return box.text_color(t.dim).frames(SPINNER, 80);
    if (look.turn) return box.child(badge(t, color, look.mark, fresh));
    if (spin && state === "running") return box.text_color(color).frames(SPINNER, 80);
    return box.text_color(color).child(glyph(look, 12));
  }

  // look records that the person looked at a pull request: its turn is
  // then seen.
  look(item) {
    if (!MARKS[item.state]?.turn || this.looked.get(item.id) === item.state) return;
    this.looked.set(item.id, item.state);
    this.saveLooked();
  }

  // counts draws counts with the marks of their states. A turn badge is
  // filled when a turn in it is new.
  counts(t, counts, fresh = {}) {
    const c = tones(t);
    return h_flex().flex_none().gap(6).items_center()
      .when(counts.needs > 0, (el) => el.child(badge(t, c.attention, MARKS.needs.mark, Boolean(fresh.needs), counts.needs)))
      .when(counts.ready > 0, (el) => el.child(badge(t, c.ready, MARKS.ready.mark, Boolean(fresh.ready), counts.ready)))
      .when(counts.running > 0, (el) => el.child(h_flex().gap(3).items_center().text_size(11.5).text_color(c.working)
        .child(glyph(MARKS.running, 11)).child(String(counts.running))));
  }

  workspaces(t) {
    const rail = narrow();
    const now = Date.now();
    const c = tones(t);
    const all = this.scan();
    this.hold(all.flatMap((workspace) => workspace.rows));
    const unseen = this.unseenOf(all.flatMap((workspace) => workspace.rows));
    // Rows do not move while the pointer is on the list.
    const list = all.map((workspace) => {
      const rows = this.hovering
        ? keepOrder(workspace.rows, this.order.get(workspace.id))
        : orderRows(workspace.rows, (row) => this.rank(row));
      this.order.set(workspace.id, rows.map((row) => row.key));
      return { ...workspace, rows };
    });
    setAgentOrder(list.flatMap((workspace) => workspace.rows.filter((row) => row.address).map((row) => row.address)));

    // The words of a row go into its tooltip. The row shows only its mark and,
    // at the right, a number: the running time, the delegated agents or the
    // background tasks.
    const words = (r) => (this.busy === r.key && r.kind !== "terminal" ? `${r.title} · Closing` : rowWords(r, now));
    const row = (r) => {
      const turn = MARKS[r.state]?.turn ? c[r.tone] : null;
      const fresh = Boolean(turn) && unseen.has(r.key);
      const quiet = r.kind === "terminal" || r.state === "idle";
      let number = "";
      if (r.state === "running" && r.workingSince) number = duration(now - r.workingSince);
      if (r.state === "delegating") number = String(r.delegating);
      if (r.state === "background") number = String(taskCount(r.background));
      if (this.busy === r.key) number = "";
      const note = this.notice?.key === r.key ? this.notice.text : "";
      const mark = this.mark(t, r.state, { fresh, spin: true, busy: this.busy === r.key });
      const line = h_flex().id(`galpon-row-${r.key}`).h(ROW).flex_none().items_center().cursor_pointer()
        .when(fresh && !r.focused, (el) => el.bg(band(turn)))
        .when(r.focused, (el) => el.bg(t.selected))
        .hover((s) => s.bg(t.hover))
        .tooltip(words(r))
        .on_click((_e, cx) => {
          this.notice = null;
          if (r.pane) run("pane.focus", { pane: r.pane });
          cx.notify();
        })
        .on_mouse_down("right", (_e, cx) => {
          cx.stop_propagation();
          this.openMenu(menuTarget(r));
        })
        .child(bar(turn))
        .when(rail, (el) => el.child(h_flex().flex_1().justify_center().child(mark)).child(div().w(3).flex_none()))
        .when(!rail, (el) => el.child(h_flex().flex_1().min_w(0).h_full().pl(24).pr(12).gap(8).items_center()
          .child(mark)
          .child(div().flex_1().min_w(0).truncate()
            .text_color(quiet && !r.focused ? t.muted : t.foreground)
            .when(fresh, (el) => el.font_semibold())
            .child(r.title))
          .when(Boolean(number), (el) => el.child(div().flex_none().text_size(11.5).text_color(t.dim).child(number)))));
      if (!note || rail) return line;
      return v_flex().flex_none().child(line).child(div().pl(51).pr(12).pb(4).text_size(11.5).text_color(c.error).child(note));
    };

    const header = (w) => {
      const open = !this.folded.has(w.id);
      const counts = summary(w.rows);
      if (rail) return div().h(9).flex_none().mx(8).border_b(1).border_color(t.border);
      // A folded workspace shows the turns inside it on its header: the bar
      // in the color of the most urgent turn, and the band in the color of
      // the most urgent new turn.
      const inside = w.rows.filter((r) => MARKS[r.state]?.turn);
      const fresh = freshOf(inside, unseen);
      const turn = !open && inside.length > 0 ? c[MARKS[inside.some((r) => r.state === "needs") ? "needs" : "ready"].tone] : null;
      const news = open ? null : fresh.needs ? c.attention : fresh.ready ? c.ready : null;
      const target = w.workspaceId
        ? { type: "workspace", kind: "workspace", id: w.workspaceId, title: w.title }
        : { type: "space", spaceId: w.id, title: w.title };
      const any = counts.needs + counts.ready + counts.running > 0;
      return h_flex().id(`galpon-space-${w.id}`).h(30).flex_none().items_center().cursor_pointer()
        .text_size(12)
        .when(Boolean(news), (el) => el.bg(band(news)))
        .hover((s) => s.bg(t.hover))
        .on_click((_e, cx) => {
          this.fold(w.id);
          cx.notify();
        })
        .on_mouse_down("right", (_e, cx) => {
          cx.stop_propagation();
          this.openMenu(target);
        })
        .child(bar(turn))
        .child(h_flex().flex_1().min_w(0).h_full().pl(7).pr(10).gap(6).items_center()
          .child(div().w(10).flex_none().text_color(t.dim).child(open ? "▾" : "▸"))
          .child(div().flex_1().min_w(0).truncate().font_semibold().text_color(w.active ? t.foreground : t.muted).child(w.title))
          .when(any, (el) => el.child(this.counts(t, counts, fresh)))
          .when(!open && !any, (el) => el.child(div().flex_none().text_size(11.5).text_color(t.dim).child(String(w.rows.length)))));
    };

    const rows = list.flatMap((w) => [header(w), ...(this.folded.has(w.id) && !rail ? [] : w.rows.map(row))]);
    // The title counts the turns of every workspace, also when the list
    // scrolls or a workspace is folded. A click goes to the next turn.
    const turns = turnOrder(list.flatMap((w) => w.rows)).map((r) => ({ ...r, fresh: unseen.has(r.key) }));
    return v_flex().id("galpon-agents").size_full().min_h(0).bg(t.chrome).text_size(13)
      .on_hover((on, cx) => {
        this.hovering = on;
        if (!on) cx.notify();
      })
      .when(!rail, (el) => el.child(h_flex().h(30).flex_none().pl(14).pr(8).gap(6).items_center()
        .text_size(11).font_semibold().text_color(t.dim).child("WORKSPACES")
        .child(div().flex_1())
        .when(turns.length > 0, (el) => el.child(this.turnCounter(t, "galpon-turns", turns)))))
      .when(list.length === 0 && !rail, (el) => el.child(div().px(14).py(4).text_size(12).text_color(t.dim)
        .child("No agent is open. Ctrl+K opens the command center, and Ctrl+Alt+N starts a new agent.")))
      .child(v_flex().id("galpon-agents-list").flex_1().min_h(0).overflow_y_scrollbar().children(rows))
      .when(Boolean(this.error) && !rail, (el) => el.child(div().flex_none().px(14).py(6).text_size(11.5).text_color(t.dim)
        .child(this.error)));
  }

  // --- Barn

  // shareFacts shares the Galpon facts of the open agents with the barn. A
  // background task that ended since the last read hatches a chick, and with
  // the barn sounds, a cluck plays. A shared value redraws every mod, so the
  // facts go out only when they change, and again after FACTS_AGAIN.
  shareFacts(before, cx) {
    const now = Date.now();
    const hatches = hatchesOf(before, this.facts?.agents ?? null, now);
    this.hatched = [...this.hatched, ...hatches].filter((hatch) => now - hatch.at < CHICK_LIFE);
    if (hatches.length > 0) this.cluck(cx);
    const value = { agents: this.facts?.agents ?? {}, workspaces: this.facts?.workspaces ?? {}, hatched: this.hatched };
    const text = JSON.stringify(value);
    if (text === this.sharedFacts && now - this.sharedFactsAt < FACTS_AGAIN) return;
    this.sharedFacts = text;
    this.sharedFactsAt = now;
    setShared(FACTS, { ...value, at: now });
  }

  // sharePulls shares the pull requests that need someone with the barn.
  sharePulls() {
    const items = (this.facts?.pullRequests ?? []).filter((item) => item.state === "needs" || item.state === "ready" || item.state === "agent");
    const unseen = this.unseenOf(items.map((item) => ({ key: item.id, state: item.state })));
    const value = items.map((item) => ({ id: item.id, ref: item.ref, title: item.title, state: item.state, url: item.url, fresh: unseen.has(item.id) }));
    const text = JSON.stringify(value);
    if (text === this.sharedPulls) return;
    this.sharedPulls = text;
    setShared(PULLS, { items: value });
  }

  // cluck plays the cluck when the app plays the barn sounds, and its sounds
  // are on.
  cluck(cx) {
    cx.spawn(async () => {
      try {
        const sounds = barnSounds(await readTextFile(`${appHome()}/settings.json`));
        if (sounds.on && sounds.enabled) playSound(SOUNDS.cluck);
      } catch {
        // Without the settings, the barn is quiet.
      }
    });
  }

  async readSounds() {
    try {
      this.sounds = barnSounds(await readTextFile(`${appHome()}/settings.json`));
    } catch {
      this.sounds = null;
    }
  }

  // toggleSounds selects the barn sounds or the app sounds with the galpon of
  // this computer: the settings are the settings of this app.
  toggleSounds(cx) {
    const local = HOST ? SCREEN_GALPON : GALPON;
    if (!local || this.soundBusy) return;
    this.soundBusy = true;
    this.soundError = "";
    const choice = this.sounds?.on ? "default" : "barn";
    cx.spawn(async (cx) => {
      try {
        const result = await exec(local, ["herdr-app", "sounds", choice]);
        if (result.code !== 0) this.soundError = sentence((result.stderr || result.stdout || "").trim().replace(/^galpon: /, ""));
      } catch (error) {
        this.soundError = sentence(message(error));
      }
      await this.readSounds();
      this.soundBusy = false;
      cx.notify();
    });
  }

  // pixels draws a sprite of barn.js: one rectangle for each run of one
  // color. colors gives the color of each color name.
  pixels(colors, item, scale = P) {
    const sprite = spriteOf(item.name);
    return div().relative().flex_none().w(sprite.w * scale).h(sprite.h * scale)
      .children(sprite.runs.map((run) => {
        const x = item.flip ? sprite.w - run.x - run.length : run.x;
        return div().absolute().left(x * scale).top(run.y * scale).w(run.length * scale).h(scale)
          .bg(colors[item.colors?.[run.ch] ?? run.color]);
      }));
  }

  // barnMark is the barn in the status bar. A click shows or hides the barn.
  barnMark(t) {
    const shown = !hiddenInLayout(BARN);
    const key = keyLabel(keys().find((binding) => binding.action === `layout.toggle:${BARN}`));
    return div().id("galpon-barn-mark").flex_none().p(3).rounded(3).cursor_pointer()
      .hover((el) => el.bg(t.hover))
      .tooltip(`${shown ? "Hide" : "Show"} the barn${key ? ` · ${key}` : ""}`)
      .on_click(() => run("layout.toggle", { id: BARN }))
      .child(this.pixels({ mark: shown ? t.accent : t.muted }, { name: "mark" }, 2));
  }

  // barn draws the scene of barn.js. The field scrolls when the pens do not
  // fit. The facts of the agents block are older than FACTS_AGAIN only when
  // the sidebar does not run; the barn then uses the app status alone.
  barn(t) {
    const colors = palette(t);
    const published = shared(FACTS);
    const facts = published && Date.now() - (published.at ?? 0) < 2 * FACTS_AGAIN ? published : null;
    const now = Date.now();
    const workspaces = buildWorkspaces({
      spaces: spaces(), panes: panes(), terminals: terminals(), agents: agents(), focused: focused(), facts, host: HOST,
    });
    const scene = barnScene({
      workspaces, turns: shared(TURNS)?.turns ?? [], pulls: shared(PULLS)?.items ?? [], hatched: facts?.hatched ?? [],
      now, width: this.width, dark: night(new Date(now)),
    });
    // The brain plans for the animals of this draw.
    this.animals = scene.pens.flatMap((pen) => pen.animals);
    return div().id("galpon-barn").relative().size_full().bg(t.chrome).border_t(1).border_color(t.border).overflow_hidden()
      .on_size_change((size, cx) => {
        // The size is in window pixels. The scene is in mod pixels, and the
        // app zoom scales them.
        const width = size.width / (Number(settings()?.zoom) || 1);
        if (Math.abs(width - this.width) < 1) return;
        this.width = width;
        cx.notify();
      })
      .child(div().id("galpon-barn-field").absolute().inset_0().overflow_x_scrollbar()
        .child(div().relative().flex_none().w(scene.width).h(scene.height)
          .children(scene.items.map((item, index) => this.barnItem(t, colors, item, index, now)))
          .children(scene.pens.map((pen) => this.pen(t, colors, pen, now)))
          .children(scene.labels.map((item, index) => this.barnItem(t, colors, item, index, now)))
          .when(Boolean(scene.scale), (el) => el.child(this.cattleScale(colors, scene)))
          .children(scene.hits.map((item, index) => this.barnHit(item, index, now)))))
      .child(this.barnControls(t));
  }

  // pen draws one pen: its fence and things, its animals, and the places
  // where the pointer acts on its dog or hen. The animals have places in the
  // pen, so they stay when the pen moves.
  pen(t, colors, pen, now) {
    return div().absolute().left(pen.x).top(0).w(pen.w).h(HEIGHT)
      .children(pen.items.map((item, index) => this.barnItem(t, colors, item, index, now)))
      .children(pen.animals.map((animal) => this.animal(t, colors, animal, now)))
      .children(pen.hits.map((item, index) => this.barnHit(item, `${pen.id}-${index}`, now)));
  }

  // cattleScale shows the weight of the cow under the pointer, else of the
  // herd.
  cattleScale(colors, scene) {
    const animal = this.hoverKey ? this.animals.find((item) => item.key === this.hoverKey) : null;
    const text = animal?.cost ? pounds(animal.cost.total) : `herd ${pounds(scene.scale.herd)}`;
    const { x, y, w, h } = scene.scale;
    return div().absolute().left(x).top(y).w(w).h(h).flex().items_center().justify_center().rounded(2)
      .bg(colors.screen).border_1().border_color(colors.scale)
      .text_size(9.5).font_semibold().text_color(colors.screenText).whitespace_nowrap().child(text);
  }

  // animal draws an animal as the brain plans it. The app moves it to its
  // place with a transition, and makes it jump with another one. What it
  // wears moves with it, and what it says stays over its head.
  animal(t, colors, animal, now) {
    const plan = planFor(this.brain, animal, now);
    const pose = poseOf(plan, animal, now);
    const s = animal.scale;
    const flip = pose.facing < 0;
    const sprite = spriteOf(pose.sprite);
    // A front view is narrower, and a lying animal is lower.
    const ox = Math.round((animal.w - sprite.w * s) / 2);
    const oy = animal.h - sprite.h * s;
    const at = (name) => {
      const point = anchor(name, pose.sprite, s, flip);
      return point ? { x: ox + point.x, y: oy + point.y } : null;
    };
    const cost = animal.cost;
    const prizes = new Set(cost?.prizes ?? []);
    const art = (name, scale = s) => this.pixels(colors, { name, flip }, scale);
    const size = (name, scale = s) => ({ w: spriteOf(name).w * scale, h: spriteOf(name).h * scale });

    const body = [];
    if (pose.plow) {
      const plow = size("plow");
      body.push(art("plow").absolute().left(flip ? animal.w - 2 : 2 - plow.w).top(animal.h - plow.h));
    }
    body.push(this.pixels(colors, { name: pose.sprite, flip, colors: prizes.has("spots") ? { s: "gold" } : null }, s)
      .absolute().left(ox).top(oy).when(animal.pale, (el) => el.opacity(0.5)));
    const neck = at("neck");
    if (prizes.has("bell") && neck) body.push(art("bell").absolute().left(neck.x - size("bell").w / 2).top(neck.y));
    const side = at("side");
    if (prizes.has("ribbon") && side) body.push(art("ribbon").absolute().left(side.x - size("ribbon").w / 2).top(side.y));
    const mouth = at("mouth");
    if (pose.chew && mouth) {
      body.push(offset(mouth.x - s, mouth.y - s, div().w(2 * s).h(s).bg(colors.pink), [{ property: "top", from: 0, to: s * 0.8, period: 420 }]));
    }
    const back = at("back");
    if (pose.bird && back) {
      const bird = size("bird", 2);
      body.push(offset(back.x - bird.w / 2, back.y - bird.h, art("bird", 2), [{ property: "top", from: 0, to: -1, period: 900 }]));
    }
    const head = at("head") ?? { x: animal.w / 2, y: 0 };
    if (prizes.has("halo")) {
      const halo = size("halo");
      body.push(offset(head.x - halo.w / 2, head.y - halo.h - s, art("halo"), [{ property: "top", from: 0, to: -2, period: 1700 }]));
    }
    const ear = at("ear");
    if (cost && ear) {
      // The tag hangs from the ear, behind the head.
      const tagW = Math.round(3 + cost.tag.length * 4.2);
      body.push(div().absolute().left(flip ? ear.x : ear.x - tagW).top(ear.y + s).w(tagW).h(9).flex().items_center().justify_center()
        .rounded(1).bg(colors.gold).text_size(7).font_bold().text_color(colors.screen).whitespace_nowrap().child(cost.tag));
    }

    // What the animal says stays over its head while it moves.
    const over = [];
    let top = head.y - (prizes.has("halo") ? 4 * s + 4 : 4);
    if (animal.state === "needs") {
      top -= 18;
      over.push(offset(head.x - 8, top, badge(t, tones(t)[MARKS.needs.tone], MARKS.needs.mark, animal.fresh), [{ property: "top", from: 0, to: -3, period: 1200 }]));
    }
    if (pose.moo) {
      const word = { cow: "MOO!", horse: "NEIGH!", goat: "MEH!", pig: "OINK!" }[animal.family] ?? "MOO!";
      over.push(div().absolute().left(head.x + 10).top(top + 2).whitespace_nowrap().text_size(10).font_bold().text_color(colors.attention)
        .frames(["", "", "", word, word, word, "", ""], 400));
    }
    if (pose.sleeping) {
      over.push(div().absolute().left(head.x + (flip ? -14 : 4)).top(top - 10).whitespace_nowrap().text_size(10.5).text_color(colors.muted)
        .frames(["z", "zz", "zzz", " "], 700));
    }
    if (pose.heart) over.push(div().absolute().left(head.x + 4).top(top - 12).text_size(11).text_color(colors.pink).child("♥"));
    for (let stream = 0; stream < pose.coins; stream++) {
      // A coin rises step by step: each row shows it at its own step of the
      // shared clock of the app.
      const steps = 5;
      for (let row = 0; row < 4; row++) {
        const list = Array.from({ length: steps }, (_, step) => ((step + stream * 2) % steps === row ? "$" : ""));
        over.push(div().absolute().left(head.x - 4 + stream * 7 - (flip ? 4 : 0)).top(top - 8 - row * 6).text_size(8.5).font_bold()
          .text_color(alpha(colors.gold, ["ff", "dd", "aa", "66"][row])).frames(list, [170, 140, 110][pose.coins - 1]));
      }
    }
    if (pose.puff && mouth) {
      over.push(div().absolute().left(mouth.x + (flip ? -9 : 3)).top(mouth.y - 9).text_size(9).text_color(colors.muted)
        .frames(["", "°", "°°", ""], 260));
    }

    const bob = pose.walking
      ? { property: "top", from: 0, to: -2, period: stepPeriod(animal) }
      : pose.moo ? { property: "top", from: 0, to: -1.5, period: 420 } : null;
    const words = [rowWords(animal.row, now), costWords(cost)].filter(Boolean).join(" · ");
    return div().id(`galpon-barn-animal-${animal.key}`).absolute().left(pose.x).top(GROUND - animal.h).w(animal.w).h(animal.h)
      .transition("left", { duration: Math.max(0, pose.moveMs), easing: "linear" })
      .cursor_pointer().tooltip(words)
      .on_hover((on, cx) => {
        if (on) this.hoverKey = animal.key;
        else if (this.hoverKey === animal.key) this.hoverKey = null;
        hover(this.brain, animal.key, on, Date.now());
        cx.notify();
      })
      .on_click(() => animal.row.pane && run("pane.focus", { pane: animal.row.pane }))
      .on_mouse_down("right", (_e, cx) => {
        cx.stop_propagation();
        this.openMenu(menuTarget(animal.row));
      })
      .child(div().id(`galpon-barn-jump-${animal.key}`).absolute().left(0).top(pose.jumpY).w(animal.w).h(animal.h)
        .transition("top", { duration: pose.jumpMs, easing: pose.jumpEase })
        .child(div().absolute().left(0).top(0).w(animal.w).h(animal.h).when(Boolean(bob), (el) => el.loop("top", bob)).children(body))
        .children(over))
      .when(animal.focused, (el) => el.child(div().absolute().left(4).top(animal.h + 2).w(Math.max(4, animal.w - 8)).h(2).bg(t.accent)));
  }

  // placed puts el at the place of item in the scene, with the loops of the
  // item. The app does not apply its zoom to the values of a loop. So a loop
  // of left or top does not hold a place in the scene: el gets a fixed place,
  // and the loop moves el only by an offset from that place.
  placed(item, el) {
    const loops = (item.loops ?? []).map((loop) => {
      const base = loop.property === "left" ? item.x : loop.property === "top" ? item.y : 0;
      return { ...loop, from: loop.from - base, to: loop.to - base };
    });
    if (!loops.some((loop) => loop.property === "left" || loop.property === "top")) {
      return animate(el.absolute().left(item.x).top(item.y), loops);
    }
    return offset(item.x, item.y, el, loops);
  }

  barnItem(t, colors, item, index, now) {
    switch (item.kind) {
      case "rect":
        // A rect with an id grows to its new width, such as a furrow.
        return this.placed(item, div().w(item.w).h(item.h).bg(colors[item.color])
          .when(Boolean(item.grow), (el) => el.id(`galpon-barn-${item.id}`).transition("width", { duration: 1500, easing: "linear" })));
      case "sprite":
        return this.placed(item, this.pixels(colors, item, item.scale ?? P).when(item.opacity != null, (el) => el.opacity(item.opacity)));
      case "badge": {
        const look = MARKS[item.state];
        return this.placed(item, div().child(badge(t, tones(t)[look.tone], look.mark, Boolean(item.fresh), item.count ?? null)));
      }
      case "text": {
        const el = div().absolute().left(item.x).top(item.y).whitespace_nowrap().text_size(item.size ?? 11)
          .text_color(colors[item.color] ?? t.muted);
        return item.frames ? el.frames(item.frames, item.period) : el.child(item.text);
      }
      case "label":
        return div().absolute().left(item.x).top(item.y).w(item.w).truncate().text_size(10.5).font_semibold()
          .text_color(item.active ? t.foreground : t.muted).child(item.text);
      case "windmill":
        return div().absolute().left(item.x).top(item.y).size(item.size)
          .child(svg(WINDMILL).size(item.size).text_color(colors.fg).when(item.spin > 0, (el) => el.spin(item.spin)));
      case "hit":
        return this.barnHit(item, index, now);
    }
    return div();
  }

  // barnHit is the place where the pointer can act on a thing of the barn.
  // An agent shows its tooltip, a click shows the agent and a right click
  // opens its menu, as in the sidebar. The things do not move under it.
  barnHit(item, index, now) {
    const row = item.row;
    const el = div().id(row ? `galpon-barn-${row.key}` : `galpon-barn-hit-${index}`).absolute().left(item.x).top(item.y).w(item.w).h(item.h);
    if (row) {
      return el.cursor_pointer().tooltip(rowWords(row, now))
        .on_click(() => row.pane && run("pane.focus", { pane: row.pane }))
        .on_mouse_down("right", (_e, cx) => {
          cx.stop_propagation();
          this.openMenu(menuTarget(row));
        });
    }
    const action = item.action;
    return el.when(Boolean(action), (it) => it.cursor_pointer())
      .when(Boolean(item.tooltip), (it) => it.tooltip(item.tooltip))
      .on_click(() => {
        if (action?.type === "command-center") run("mod.open", { name: "galpon" });
        if (action?.type === "pull-request") {
          // The pull request block opens it, as from its menu.
          setShared(REQUEST, {
            seq: `${Date.now()}-${Math.random()}`, at: Date.now(), action: "open",
            target: { type: "pull-request", key: action.item.id, url: action.item.url ?? "" },
          });
          run("galpon-sidebar.request");
        }
      });
  }

  // barnControls are the buttons at the top right of the barn: the barn
  // sounds, and hide.
  barnControls(t) {
    const local = HOST ? SCREEN_GALPON : GALPON;
    const on = Boolean(this.sounds?.on);
    let tip = on
      ? "Barn sounds are on: a moo when an agent needs you, a rooster when an agent finishes, and a cluck when a background task ends. Click to turn them off."
      : "Turn on the barn sounds: a moo when an agent needs you, a rooster when an agent finishes, and a cluck when a background task ends.";
    if (on && this.sounds && !this.sounds.enabled) tip = "The app sounds are off. Turn them on in the app with Settings > Alerts.";
    if (this.soundError) tip = this.soundError;
    const key = keyLabel(keys().find((binding) => binding.action === `layout.toggle:${BARN}`));
    const chip = (id, label, tooltip, color, action) => div().id(id).h(22).px(7).flex().items_center().rounded(3).cursor_pointer()
      .text_size(11.5).text_color(color).bg(alpha(t.chrome, "cc"))
      .hover((el) => el.bg(t.hover).text_color(t.foreground))
      .tooltip(tooltip).on_click(action).child(label);
    return h_flex().absolute().top(6).right(8).gap(4).items_center()
      .when(Boolean(local), (el) => el.child(chip("galpon-barn-sounds", on ? "♪ on" : "♪ off", tip, on ? t.foreground : t.dim,
        (_e, cx) => this.toggleSounds(cx))))
      .child(chip("galpon-barn-hide", "×", `Hide the barn${key ? ` · ${key}` : ""}`, t.dim, () => run("layout.hide", { id: BARN })));
  }

  // --- Pull requests

  browse(url, cx) {
    if (!url) return;
    const opener = appWindow()?.platform === "macos" ? "open" : "xdg-open";
    cx.spawn(async (cx) => {
      try {
        await exec(opener, [url]);
      } catch (error) {
        this.notice = { key: "", text: sentence(message(error)) };
        cx.notify();
      }
    });
  }

  // A click opens the agent of the pull request: its tab when it is open in
  // the app, else Galpon opens it. A pull request without an agent opens on
  // GitHub. An idle agent that is open in Herdr moves after a second click,
  // because its process restarts.
  openPullRequest(item, open, cx) {
    this.look(item);
    if (!item.agentId) return this.browse(item.url, cx);
    const pane = open.get(item.agentId);
    if (pane) {
      this.notice = null;
      run("pane.focus", { pane });
      return;
    }
    if (this.busy) return;
    const move = this.pendingMove === item.agentId;
    this.busy = item.id;
    this.notice = null;
    cx.notify();
    cx.spawn(async (cx) => {
      let out;
      try {
        out = await this.galpon(["open", item.agentId, ...(move ? ["--move"] : [])]);
      } catch (error) {
        out = { error: message(error) };
      }
      if (!out.error && HOST && out.place?.terminal) await showOnScreen(out.place, cx);
      this.busy = "";
      this.pendingMove = out.code === "open-elsewhere" ? item.agentId : "";
      if (out.code === "open-elsewhere") this.notice = { key: item.id, text: `${sentence(out.error)}. Click again to move it.` };
      else if (out.error) this.notice = { key: item.id, text: sentence(out.error) };
      cx.notify();
    });
  }

  refresh(cx) {
    this.refreshAsked = Date.now();
    this.notice = null;
    cx.notify();
    cx.spawn(async (cx) => {
      try {
        const out = await this.galpon(["refresh"]);
        if (out.error) this.notice = { key: "", text: sentence(out.error) };
      } catch (error) {
        this.notice = { key: "", text: sentence(message(error)) };
      }
      this.dirty = true;
      cx.notify();
    });
  }

  pullRequests(t) {
    const rail = narrow();
    const c = tones(t);
    const facts = this.facts;
    const sections = pullRequestSections(facts);
    const open = openAgentPanes(panes(), terminals());
    const items = facts?.pullRequests ?? [];
    // A pull request is looked at when the person opens it. Without Galpon
    // facts, the block forgets nothing.
    const turnRows = items.map((item) => ({ key: item.id, state: item.state }));
    const unseen = facts ? this.unseenOf(turnRows) : new Set();
    const refreshing = Boolean(facts?.refreshing) || this.refreshAsked > 0;
    const age = facts?.pullRequestsAt ? duration(Date.now() - facts.pullRequestsAt) : "";

    const row = (item) => {
      const look = MARKS[item.state] ?? MARKS.pending;
      const turn = look.turn ? c[look.tone] : null;
      const fresh = Boolean(turn) && unseen.has(item.id);
      const note = this.notice?.key === item.id ? this.notice.text : "";
      const mark = this.mark(t, item.state in MARKS ? item.state : "pending", { fresh, busy: this.busy === item.id });
      const opens = item.agentId ? `click opens ${item.agentTitle}` : "click opens GitHub";
      const base = h_flex().id(`galpon-pr-${item.id}`).flex_none().cursor_pointer()
        .when(fresh, (el) => el.bg(band(turn)))
        .hover((s) => s.bg(t.hover))
        .tooltip([item.ref, look.words, item.label, opens].filter(Boolean).join(" · "))
        .on_click((_e, cx) => this.openPullRequest(item, open, cx))
        .on_mouse_down("right", (_e, cx) => {
          cx.stop_propagation();
          this.openMenu({
            type: "pull-request", key: item.id, title: `${item.ref} · ${item.title}`, url: item.url,
            agentId: item.agentId ?? "", agentTitle: item.agentTitle ?? "",
          });
        });
      if (rail) {
        return base.h(ROW).items_center().child(bar(turn))
          .child(h_flex().flex_1().justify_center().child(mark)).child(div().w(3).flex_none());
      }
      return base
        .child(bar(turn))
        .child(v_flex().flex_1().min_w(0).pl(24).pr(12).py(5).gap(1)
          .child(h_flex().gap(8).items_center()
            .child(mark)
            .child(div().flex_1().min_w(0).truncate().text_color(t.foreground)
              .when(fresh, (el) => el.font_semibold())
              .child(item.title)))
          .child(div().pl(24).truncate().text_size(11.5).text_color(t.dim)
            .child([item.ref, item.agentTitle].filter(Boolean).join(" · ")))
          .when(Boolean(note), (el) => el.child(div().pl(24).text_size(11.5).text_color(c.error).child(note))));
    };
    const header = (section) => {
      const key = `pr:${section.group}`;
      const open = !this.folded.has(key);
      if (rail) return div().h(9).flex_none().mx(8).border_b(1).border_color(t.border);
      // A folded group shows the turns inside it on its header, as a folded
      // workspace does.
      const inside = section.items.filter((item) => MARKS[item.state]?.turn).map((item) => ({ key: item.id, state: item.state }));
      const fresh = freshOf(inside, unseen);
      const turn = !open && inside.length > 0 ? c[MARKS[inside.some((item) => item.state === "needs") ? "needs" : "ready"].tone] : null;
      const news = open ? null : fresh.needs ? c.attention : fresh.ready ? c.ready : null;
      return h_flex().id(`galpon-pr-group-${section.group}`).h(28).flex_none().items_center().cursor_pointer()
        .text_size(11)
        .when(Boolean(news), (el) => el.bg(band(news)))
        .hover((s) => s.bg(t.hover))
        .on_click((_e, cx) => {
          this.fold(key);
          cx.notify();
        })
        .child(bar(turn))
        .child(h_flex().flex_1().min_w(0).h_full().pl(7).pr(10).gap(6).items_center()
          .child(div().w(10).flex_none().text_color(t.dim).child(open ? "▾" : "▸"))
          .child(div().flex_1().min_w(0).truncate().font_semibold().text_color(t.muted).child(section.title))
          .child(div().flex_none().text_color(t.dim).child(String(section.items.length))));
    };

    let empty = "";
    if (!facts && !this.error) empty = "Reading Galpon…";
    else if (facts && !facts.github) empty = "No Galpon repository has a GitHub remote.";
    else if (facts && sections.length === 0) empty = facts.pullRequestsAt ? "No pull request needs anyone." : "No GitHub data yet. The refresh button gets it.";
    const problem = this.notice?.key === "" ? this.notice.text : this.error || facts?.pullRequestError || "";
    const rows = sections.flatMap((section) => [header(section), ...(this.folded.has(`pr:${section.group}`) && !rail ? [] : section.items.map(row))]);
    const counts = summary(turnRows);

    return v_flex().id("galpon-pull-requests").size_full().min_h(0).bg(t.chrome).text_size(13)
      .when(!rail, (el) => el.child(h_flex().h(30).flex_none().pl(14).pr(6).gap(6).items_center().text_size(11)
        .child(div().font_semibold().text_color(t.dim).child("PULL REQUESTS"))
        .child(this.counts(t, { ...counts, running: 0 }, freshOf(turnRows, unseen)))
        .child(div().flex_1())
        .when(Boolean(age) && !refreshing, (el) => el.child(div().text_color(t.dim).child(age)))
        .child(div().id("galpon-pr-refresh").size(24).flex_none().flex().items_center().justify_center().cursor_pointer()
          .text_size(13).text_color(t.muted).hover((s) => s.bg(t.hover).text_color(t.foreground))
          .tooltip(refreshing ? "Galpon gets the pull requests from GitHub" : "Get the pull requests from GitHub now")
          .on_click((_e, cx) => !refreshing && this.refresh(cx))
          .when(refreshing, (el) => el.text_color(t.working).frames(SPINNER, 80))
          .when(!refreshing, (el) => el.child("↻")))))
      .when(Boolean(problem) && !rail, (el) => el.child(div().flex_none().px(14).pb(6).text_size(11.5).text_color(c.error).child(problem)))
      .when(Boolean(empty) && !rail, (el) => el.child(div().px(14).py(4).text_size(12).text_color(t.dim).child(empty)))
      .child(v_flex().id("galpon-pull-requests-list").flex_1().min_h(0).overflow_y_scrollbar().children(rows));
  }
}
