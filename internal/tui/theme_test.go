package tui

import (
	"os"
	"path/filepath"
	"testing"
)

const omarchyTestColors = `
mode = "dark"
accent = "#7aa2f7"
selection = "#292e42"
muted = "#414868"
background = "#1a1b26"
foreground = "#a9b1d6"
red = "#f7768e"
yellow = "#e0af68"
bright_yellow = "#ff9e64"
green = "#9ece6a"
cyan = "#449dab"
blue = "#7aa2f7"
magenta = "#ad8ee6"
`

func TestConfiguredPaletteUsesCurrentOmarchyTheme(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".local", "state", "omarchy", "current", "theme", "colors.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(omarchyTestColors), 0o600); err != nil {
		t.Fatal(err)
	}

	palette := configuredPalette()
	checks := map[string]struct {
		got  string
		want string
	}{
		"background":      {string(palette.Background), "#1a1b26"},
		"surface":         {string(palette.Surface), "#232431"},
		"raised surface":  {string(palette.SurfaceRaised), "#282a38"},
		"prompt":          {string(palette.Prompt), "#262b3f"},
		"selection":       {string(palette.Selection), "#292e42"},
		"foreground":      {string(palette.Foreground), "#a9b1d6"},
		"muted text":      {string(palette.Muted), "#787e9a"},
		"status":          {string(palette.Status), "#7aa2f7"},
		"orange fallback": {string(palette.Orange), "#ff9e64"},
	}
	for name, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %s, want %s", name, check.got, check.want)
		}
	}
}

func TestConfiguredPaletteFallsBackWithoutValidOmarchyTheme(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := configuredPalette(); got != defaultPalette {
		t.Fatalf("palette without Omarchy = %#v, want default", got)
	}

	path := filepath.Join(home, ".local", "state", "omarchy", "current", "theme", "colors.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`background = "not-a-color"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := configuredPalette(); got != defaultPalette {
		t.Fatalf("palette with invalid Omarchy theme = %#v, want default", got)
	}
}
