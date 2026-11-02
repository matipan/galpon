package herdrapp

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// modFiles holds the mods that Install writes: the native command center and
// the terminal launcher.
//
//go:embed mods
var modFiles embed.FS

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
	if err := writeNativeMod(filepath.Join(home, "mods", "galpon"), galpon); err != nil {
		return nil, err
	}
	for name, args := range map[string][]string{"galpon-terminal": nil, "galpon-new-agent": {"herdr-app", "new-agent"}} {
		if err := writeTerminalMod(filepath.Join(home, "mods", name), galpon, args); err != nil {
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

// writeNativeMod writes the native command center. Its main.js names the
// galpon executable.
func writeNativeMod(dir, galpon string) error {
	return writeModFiles(dir, "mods/galpon", map[string]string{"__GALPON__": galpon})
}

// writeTerminalMod writes a launcher that shows galpon in a terminal dialog.
func writeTerminalMod(dir, galpon string, args []string) error {
	if args == nil {
		args = []string{}
	}
	argv, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return writeModFiles(dir, "mods/launcher", map[string]string{"__GALPON__": galpon, "__NAME__": filepath.Base(dir), "__ARGS_JSON__": string(argv)})
}

// writeModFiles copies an embedded mod folder. Placeholders in main.js get
// JSON strings, except __ARGS_JSON__, which is JSON already.
func writeModFiles(dir, source string, values map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := modFiles.ReadDir(source)
	if err != nil {
		return err
	}
	var replacements []string
	for placeholder, value := range values {
		if placeholder == "__ARGS_JSON__" {
			replacements = append(replacements, "__ARGS__", value)
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		replacements = append(replacements, placeholder, string(encoded))
	}
	replacer := strings.NewReplacer(replacements...)
	for _, entry := range entries {
		data, err := modFiles.ReadFile(source + "/" + entry.Name())
		if err != nil {
			return err
		}
		if entry.Name() == "main.js" {
			data = []byte(replacer.Replace(string(data)))
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
