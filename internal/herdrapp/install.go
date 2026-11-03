package herdrapp

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// modFiles holds the mods that Install writes: the command center, the
// Ctrl-Alt-N launcher that opens its New Agent form, and the sidebar of the
// Galpon layout.
//
//go:embed mods
var modFiles embed.FS

// layoutFiles holds the Galpon layout and the picture that the app settings
// show for it.
//
//go:embed layouts
var layoutFiles embed.FS

// mods maps each mod folder that Install writes to its embedded source.
var mods = map[string]string{
	"galpon": "mods/galpon", "galpon-new-agent": "mods/new-agent", "galpon-sidebar": "mods/sidebar",
}

// LayoutName names the Galpon layout.
const LayoutName = "galpon"

// obsoleteMods are Galpon mod folders that Install removes, because Galpon
// does not use them.
var obsoleteMods = []string{"galpon-terminal"}

// Keys are the app key bindings that Install adds to the global section.
var Keys = map[string]string{
	"ctrl-k":     "mod.open:galpon",
	"ctrl-alt-n": "mod.open:galpon-new-agent",
}

// LayoutKeys are the app key bindings that Install adds to the section of
// the Galpon layout. They work only while the app shows that layout, so in
// other layouts the key goes to the terminal. ctrl-alt-j goes to the next
// agent turn: the sidebar gets it as the key command "next-turn".
// ctrl-alt-b shows or hides the barn below the terminals.
var LayoutKeys = map[string]string{
	"ctrl-alt-j": "galpon-sidebar.next-turn",
	"ctrl-alt-b": "layout.toggle:galpon-barn",
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

// Target is the Galpon that the installed mods run.
type Target struct {
	// Galpon is the galpon executable. With a Host, it is the path on the
	// host.
	Galpon string
	// Host is empty when Galpon runs on this computer. On a screen, it is the
	// herdr-core machine name of the host: the computer that runs Galpon.
	// The mods then run galpon on the host through hc.
	Host string
	// Build is the build of the galpon that installs the mods. On a screen,
	// the mods compare it with the build of the host, and start an update
	// when the two are different.
	Build string
	// Screen is the galpon executable of this computer. On a screen, the mods
	// run it to start an update.
	Screen string
}

// buildLine is the line of main.js that holds the build of the mods.
var buildLine = regexp.MustCompile(`(?m)^const BUILD = "([^"]*)";$`)

// InstalledBuild returns the build of the galpon that installed the mods in
// the app home folder, or "" when it is not known.
func InstalledBuild(home string) string {
	data, err := os.ReadFile(filepath.Join(home, "mods", "galpon", "main.js"))
	if err != nil {
		return ""
	}
	match := buildLine.FindSubmatch(data)
	if match == nil {
		return ""
	}
	return string(match[1])
}

// Install writes the Galpon mods, the Galpon layout and the barn sounds into
// the app home folder, and adds Keys and LayoutKeys to its keys.json. It does
// not select the barn sounds. target is the
// Galpon that the mods run. The app reloads saved files at once, but it keeps
// the shown layout. Install returns the key bindings that it replaced, or
// that a Galpon key hides in the Galpon layout.
func Install(home string, target Target) (map[string]string, error) {
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
	for name, source := range mods {
		if err := writeModFiles(filepath.Join(home, "mods", name), source, target); err != nil {
			return nil, err
		}
	}
	for _, name := range obsoleteMods {
		if err := os.RemoveAll(filepath.Join(home, "mods", name)); err != nil {
			return nil, err
		}
	}
	if err := writeLayout(home); err != nil {
		return nil, err
	}
	if err := writeSounds(home); err != nil {
		return nil, err
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
	section := "layout:" + LayoutName
	if keys[section] == nil {
		keys[section] = map[string]string{}
	}
	for key, action := range LayoutKeys {
		if current, ok := keys[section][key]; ok && current != action {
			replaced[key] = current
		}
		// The layout section wins over the global section in the Galpon
		// layout. The global binding still works in other layouts.
		if current, ok := keys["global"][key]; ok && current != action {
			replaced[key] = current
		}
		keys[section][key] = action
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

// writeLayout copies the Galpon layout files into the layouts folder. The
// Galpon layout gets the theme that the person chose for the main layout, so
// that a switch keeps the colors. Without a choice, the app theme applies.
func writeLayout(home string) error {
	dir := filepath.Join(home, "layouts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := layoutFiles.ReadDir("layouts")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		data, err := layoutFiles.ReadFile("layouts/" + entry.Name())
		if err != nil {
			return err
		}
		if entry.Name() == LayoutName+".json" {
			if data, err = withMainTheme(home, data); err != nil {
				return err
			}
		}
		if err := writeFile(filepath.Join(dir, entry.Name()), data); err != nil {
			return err
		}
	}
	return nil
}

// withMainTheme sets the root theme of a layout to the theme that the person
// chose for the main layout. The app keeps that choice in
// layouts/.state/main.json. A missing or unreadable choice leaves the layout
// as it is.
func withMainTheme(home string, layout []byte) ([]byte, error) {
	state, err := os.ReadFile(filepath.Join(home, "layouts", ".state", "main.json"))
	if err != nil {
		return layout, nil
	}
	var choice struct {
		Theme json.RawMessage `json:"theme"`
	}
	if json.Unmarshal(state, &choice) != nil || len(choice.Theme) == 0 || string(choice.Theme) == "null" {
		return layout, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(layout, &root); err != nil {
		return nil, err
	}
	root["theme"] = choice.Theme
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(root); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// writeModFiles copies an embedded mod folder. In main.js, __GALPON__ becomes
// the galpon executable as a JSON string, __BUILD__ its build, and __HOST__
// and __SCREEN_GALPON__ the host machine and the galpon of this computer, or
// null when Galpon runs on this computer.
func writeModFiles(dir, source string, target Target) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := modFiles.ReadDir(source)
	if err != nil {
		return err
	}
	executable, err := json.Marshal(target.Galpon)
	if err != nil {
		return err
	}
	build, err := json.Marshal(target.Build)
	if err != nil {
		return err
	}
	host, screen := []byte("null"), []byte("null")
	if target.Host != "" {
		if host, err = json.Marshal(target.Host); err != nil {
			return err
		}
		if screen, err = json.Marshal(target.Screen); err != nil {
			return err
		}
	}
	for _, entry := range entries {
		data, err := modFiles.ReadFile(source + "/" + entry.Name())
		if err != nil {
			return err
		}
		if entry.Name() == "main.js" {
			data = bytes.ReplaceAll(data, []byte("__GALPON__"), executable)
			data = bytes.ReplaceAll(data, []byte("__BUILD__"), build)
			data = bytes.ReplaceAll(data, []byte("__HOST__"), host)
			data = bytes.ReplaceAll(data, []byte("__SCREEN_GALPON__"), screen)
		}
		if err := writeFile(filepath.Join(dir, entry.Name()), data); err != nil {
			return err
		}
	}
	return nil
}

// writeFile replaces path in one step, so the app never reloads half a file.
func writeFile(path string, data []byte) error {
	temp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".galpon")
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
