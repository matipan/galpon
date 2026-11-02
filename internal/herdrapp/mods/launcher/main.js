// Galpon in a terminal dialog. `galpon herdr-app install` writes this file.
// Run that command again after the galpon executable moves.
//
// A key or the Galpon command center opens this mod as a small overlay
// without a frame. The mod starts galpon in a herdr-core terminal, shows the
// terminal in a dialog and closes its own overlay. props.args replaces the
// default galpon arguments. The dialog closes when galpon exits. The same key
// closes an open dialog. The dialog title bar is a copy of this mod with
// props {terminal}; it ends galpon when the dialog closes in another way.
import { View, div } from "gpui-kit";
import { h_flex } from "gpui-base";
import { closeOverlay, exec, focused, home, notify, openOverlay, props, setShared, shared, terminals, theme } from "herdr";

const GALPON = __GALPON__;
const ARGS = __ARGS__;
const NAME = __NAME__;
const OPEN = "galpon.dialog";

const live = (address) => Boolean(address) && terminals().some((t) => t.address === address);

export default class GalponTerminal extends View {
  init(_props, cx) {
    // Only an overlay that a key or a mod opens starts galpon. The title bar
    // copy and `herdr-app check` start nothing.
    const own = props()?.overlay;
    if (props()?.terminal || !own) return;
    cx.spawn(async (cx) => {
      try {
        const open = shared(OPEN);
        if (live(open?.terminal)) {
          closeOverlay(open.overlay);
          setShared(OPEN, null);
          await exec("hc", ["kill", open.terminal]);
          return;
        }
        const args = Array.isArray(props()?.args) ? props().args : ARGS;
        const f = focused() ?? {};
        const env = ["GALPON_RENDERER=herdr-app", "GALPON_APP_TERMINAL=" + (f.terminal ?? ""), "GALPON_APP_SPACE=" + (f.space ?? "")];
        const run = await exec("hc", ["run", "--bg", "--json", "--no-parent", "--no-notify", "--rm", "--cwd", f.cwd || home(), "--", "env", ...env, GALPON, ...args]);
        const address = run.code === 0 ? JSON.parse(run.stdout).address : null;
        if (!address) throw new Error((run.stderr || run.stdout || "hc did not start Galpon").trim());
        const overlay = openOverlay({
          kind: "dialog", modal: true, width: 0.9, height: 0.86,
          layout: { type: "column", children: [
            { type: "block", size: { px: 28 }, content: { mod: NAME, props: { terminal: address } } },
            { type: "block", content: { terminal: address } },
          ] },
        });
        setShared(OPEN, { overlay, terminal: address });
      } catch (error) {
        notify({ title: "Galpon did not open", body: String(error?.message ?? error), level: "error" });
      } finally {
        closeOverlay(own);
      }
    });
  }

  async close(_cx) {
    const terminal = props()?.terminal;
    if (live(terminal)) await exec("hc", ["kill", terminal]);
  }

  render(_cx) {
    const t = theme();
    if (!props()?.terminal) return div();
    return h_flex().size_full().px(12).gap(8).items_center().bg(t.surface).border_b(1).border_color(t.border).text_size(12)
      .child(div().font_semibold().text_color(t.foreground).child("Galpon"))
      .child(div().flex_1())
      .child(div().text_color(t.dim).child("esc closes"));
  }
}
