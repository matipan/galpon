package herdrapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Keys are the app key bindings that Install adds to the global section.
var Keys = map[string]string{
	"ctrl-k":     "mod.open:galpon",
	"ctrl-alt-n": "mod.open:galpon-new-agent",
}

// Home returns the app home folder: HERDR_APP_HOME, else ~/.config/herdr-app.
func Home() (string, error) {
	if home := strings.TrimSpace(os.Getenv("HERDR_APP_HOME")); home != "" {
		return home, nil
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(user, ".config", "herdr-app"), nil
}

// Install writes the Galpon mods into the app home folder and adds Keys to its
// keys.json. galpon is the executable that the mods start. The app reloads
// saved mods and keys at once. Install returns the bindings that it replaced.
func Install(home, galpon string) (map[string]string, error) {
	keysPath := filepath.Join(home, "keys.json")
	data, err := os.ReadFile(keysPath)
	if errors.Is(err, os.ErrNotExist) {
		// keys.json holds every binding. A file with only the Galpon keys
		// would remove the app defaults.
		return nil, fmt.Errorf("%s is missing; open the Herdr app once so that it writes its keys, then run this command again", keysPath)
	}
	if err != nil {
		return nil, err
	}
	var keys map[string]map[string]string
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, fmt.Errorf("read %s: %w", keysPath, err)
	}
	for name, args := range map[string][]string{"galpon": nil, "galpon-new-agent": {"herdr-app", "new-agent"}} {
		if err := writeMod(filepath.Join(home, "mods", name), galpon, args); err != nil {
			return nil, err
		}
	}
	if keys == nil {
		keys = map[string]map[string]string{}
	}
	if keys["global"] == nil {
		keys["global"] = map[string]string{}
	}
	replaced := map[string]string{}
	for key, action := range Keys {
		if current, ok := keys["global"][key]; ok && current != action {
			replaced[key] = current
		}
		keys["global"][key] = action
		// A terminal binding wins over a global one while a terminal has focus.
		if current, ok := keys["terminal"][key]; ok {
			replaced[key] = current
			delete(keys["terminal"], key)
		}
	}
	backup := keysPath + ".before-galpon"
	if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(backup, data, 0o600); err != nil {
			return nil, err
		}
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(keys); err != nil {
		return nil, err
	}
	return replaced, writeFile(keysPath, out.Bytes())
}

func writeMod(dir, galpon string, args []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if args == nil {
		args = []string{}
	}
	executable, err := json.Marshal(galpon)
	if err != nil {
		return err
	}
	argv, err := json.Marshal(args)
	if err != nil {
		return err
	}
	source := strings.NewReplacer("__GALPON__", string(executable), "__ARGS__", string(argv)).Replace(modSource)
	// The launcher overlay is two pixels and has no frame, so it is not visible.
	manifest := `{ "place": "none", "title": "Galpon", "overlay": { "kind": "popup", "width": 2, "height": 2, "frame": "none" } }` + "\n"
	if err := writeFile(filepath.Join(dir, "mod.json"), []byte(manifest)); err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, "main.js"), []byte(source))
}

// writeFile replaces path in one step, so the app never reloads half a file.
func writeFile(path string, data []byte) error {
	temp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".galpon")
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// modSource opens the Galpon command center in a dialog. A key opens this mod
// as a small overlay. The mod starts Galpon in a herdr-core terminal, shows
// that terminal in a dialog and closes its own overlay. The dialog closes when
// Galpon exits. The same key closes an open dialog. The dialog title bar is a
// copy of the galpon mod with props {terminal}; it ends Galpon when the dialog
// closes in another way.
const modSource = `// Galpon command center. ` + "`galpon herdr-app install`" + ` writes this file.
// Run that command again after the galpon executable moves.
import { View, div } from "gpui-kit";
import { h_flex } from "gpui-base";
import { closeOverlay, exec, focused, home, notify, openOverlay, props, setShared, shared, terminals, theme } from "herdr";

const GALPON = __GALPON__;
const ARGS = __ARGS__;
const OPEN = "galpon.dialog";

const live = (address) => Boolean(address) && terminals().some((t) => t.address === address);

export default class Galpon extends View {
  init(_props, cx) {
    // Only the overlay that a key opens starts Galpon. The title bar copy and
    // ` + "`herdr-app check`" + ` start nothing.
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
        const f = focused() ?? {};
        const env = ["GALPON_RENDERER=herdr-app", "GALPON_APP_TERMINAL=" + (f.terminal ?? ""), "GALPON_APP_SPACE=" + (f.space ?? "")];
        const run = await exec("hc", ["run", "--bg", "--json", "--no-parent", "--no-notify", "--rm", "--cwd", f.cwd || home(), "--", "env", ...env, GALPON, ...ARGS]);
        const address = run.code === 0 ? JSON.parse(run.stdout).address : null;
        if (!address) throw new Error((run.stderr || run.stdout || "hc did not start Galpon").trim());
        const overlay = openOverlay({
          kind: "dialog", modal: true, width: 0.9, height: 0.86,
          layout: { type: "column", children: [
            { type: "block", size: { px: 28 }, content: { mod: "galpon", props: { terminal: address } } },
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
      .child(div().text_color(t.dim).child("ctrl+k closes"));
  }
}
`
