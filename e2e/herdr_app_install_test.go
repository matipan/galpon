package e2e

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHerdrAppInstallKeepsTheKeysOfThePerson runs galpon herdr-app install
// against keys.json files of the Herdr app. It keeps each run's keys.json and
// output under the test folder, and logs them.
func TestHerdrAppInstallKeepsTheKeysOfThePerson(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "galpon")
	runRaw(t, "..", nil, "go", "build", "-o", bin, "./cmd/galpon")

	type keys map[string]map[string]string
	install := func(t *testing.T, before keys) (keys, string) {
		t.Helper()
		home := filepath.Join(t.TempDir(), "herdr-app")
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(before)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "keys.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		env := append(os.Environ(), "HERDR_APP_HOME="+home, "GALPON_STATE_DIR="+filepath.Join(root, "state"))
		output := runRaw(t, "", env, bin, "herdr-app", "install")
		after, err := os.ReadFile(filepath.Join(home, "keys.json"))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("output:\n%s\nkeys.json:\n%s", output, after)
		var out keys
		if err := json.Unmarshal(after, &out); err != nil {
			t.Fatal(err)
		}
		backup, err := os.ReadFile(filepath.Join(home, "keys.json.before-galpon"))
		if err != nil || string(backup) != string(data) {
			t.Fatalf("keys.json.before-galpon = %q, %v; want the first keys.json", backup, err)
		}
		return out, output
	}
	galpon := keys{
		"global":        {"ctrl-k": "mod.open:galpon", "ctrl-alt-n": "mod.open:galpon-new-agent"},
		"layout:galpon": {"ctrl-alt-j": "galpon-sidebar.next-turn", "ctrl-alt-b": "layout.toggle:galpon-barn"},
	}
	withGalpon := func(base keys) keys {
		out := keys{}
		for section, bindings := range base {
			out[section] = maps.Clone(bindings)
		}
		for section, bindings := range galpon {
			if out[section] == nil {
				out[section] = map[string]string{}
			}
			maps.Copy(out[section], bindings)
		}
		return out
	}
	equal := func(a, b keys) bool {
		for _, pair := range [][2]keys{{a, b}, {b, a}} {
			for section, bindings := range pair[0] {
				if len(bindings) > 0 && !maps.Equal(bindings, pair[1][section]) {
					return false
				}
			}
		}
		return true
	}

	t.Run("a first install adds every Galpon key", func(t *testing.T) {
		before := keys{"global": {"alt-1": "space.select:1"}, "terminal": {"ctrl-k": "terminal.clear-line"}}
		after, output := install(t, before)
		want := withGalpon(keys{"global": {"alt-1": "space.select:1"}})
		if !equal(after, want) {
			t.Fatalf("keys = %v, want %v", after, want)
		}
		if !strings.Contains(output, `Ctrl+K was "terminal.clear-line" before.`) {
			t.Fatalf("output does not tell about the replaced terminal key:\n%s", output)
		}
	})

	t.Run("a second install changes nothing", func(t *testing.T) {
		installed := withGalpon(keys{"global": {"alt-1": "space.select:1"}})
		after, _ := install(t, installed)
		if !equal(after, installed) {
			t.Fatalf("keys = %v, want %v", after, installed)
		}
	})

	t.Run("keys of the person for Galpon actions stay the only ones", func(t *testing.T) {
		before := keys{
			"global": {
				"alt-1":      "space.select:1",
				"ctrl-alt-k": "mod.open:galpon",
				"ctrl-n":     "mod.open:galpon-new-agent",
				// The key of a layout action in the global section also works in the
				// Galpon layout.
				"ctrl-alt-t": "layout.toggle:galpon-barn",
				// These keys keep their actions: Galpon does not need them.
				"ctrl-k":     "palette.open",
				"ctrl-alt-n": "space.new",
			},
			// A key of another layout does not work in the Galpon layout.
			"layout:main": {"ctrl-alt-x": "galpon-sidebar.next-turn"},
		}
		after, output := install(t, before)
		want := keys{
			"global":        maps.Clone(before["global"]),
			"layout:main":   maps.Clone(before["layout:main"]),
			"layout:galpon": {"ctrl-alt-j": "galpon-sidebar.next-turn"},
		}
		if !equal(after, want) {
			t.Fatalf("keys = %v, want %v", after, want)
		}
		for _, line := range []string{
			"Ctrl+Alt+K opens the command center: your key, so install did not add another one.",
			"Ctrl+N opens New agent: your key, so install did not add another one.",
			"Ctrl+Alt+T shows or hides the barn in the Galpon layout: your key, so install did not add another one.",
			"Ctrl+Alt+J goes to the next agent turn in the Galpon layout.",
		} {
			if !strings.Contains(output, line) {
				t.Fatalf("output omits %q:\n%s", line, output)
			}
		}
		if strings.Contains(output, " was ") {
			t.Fatalf("output tells about a replaced key, but no key changed:\n%s", output)
		}
	})
}
