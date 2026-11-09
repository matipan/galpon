// Ctrl-Alt-N for Galpon. `galpon herdr-app install` writes this mod.
//
// A key opens this mod as a small overlay without a frame. The mod opens the
// Galpon command center at its New Agent form for the terminal that has the
// keyboard, and closes its own overlay. The command center finds the Galpon
// workspace of that terminal.
import { View, div } from "gpui-kit";
import { closeOverlay, focused, openOverlay, props } from "herdr";

export default class GalponNewAgent extends View {
  init(_props, cx) {
    // Only an overlay that a key opens does something; `herdr-app check`
    // opens nothing.
    const own = props()?.overlay;
    if (!own) return;
    const focus = focused() ?? {};
    cx.spawn(async () => {
      try {
        openOverlay({
          mod: "galpon", kind: "dialog", modal: true, width: 0.5, height: 0.62, frame: "none",
          props: { start: "new-agent", terminal: focus.terminal ?? "", space: focus.space ?? "" },
        });
      } finally {
        closeOverlay(own);
      }
    });
  }

  render(_cx) {
    return div();
  }
}
