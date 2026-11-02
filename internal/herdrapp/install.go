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

// modFiles holds the mods that Install writes: the command center and the
// Ctrl-Alt-N launcher that opens its New Agent form.
//
//go:embed mods
var modFiles embed.FS

// mods maps each mod folder that Install writes to its embedded source.
var mods = map[string]string{"galpon": "mods/galpon", "galpon-new-agent": "mods/new-agent"}

// obsoleteMods are Galpon mod folders that Install removes, because Galpon
// does not use them.
var obsoleteMods = []string{"galpon-terminal"}

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
	for name, source := range mods {
		if err := writeModFiles(filepath.Join(home, "mods", name), source, galpon); err != nil {
			return nil, err
		}
	}
	for _, name := range obsoleteMods {
		if err := os.RemoveAll(filepath.Join(home, "mods", name)); err != nil {
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

// writeModFiles copies an embedded mod folder. __GALPON__ in main.js becomes
// the galpon executable as a JSON string.
func writeModFiles(dir, source, galpon string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := modFiles.ReadDir(source)
	if err != nil {
		return err
	}
	executable, err := json.Marshal(galpon)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		data, err := modFiles.ReadFile(source + "/" + entry.Name())
		if err != nil {
			return err
		}
		if entry.Name() == "main.js" {
			data = bytes.ReplaceAll(data, []byte("__GALPON__"), executable)
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
