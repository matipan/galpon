package tui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type Palette struct {
	Background, Surface, SurfaceRaised, Prompt, Selection, Border lipgloss.Color
	Foreground, Muted, Comment, Status, StatusInk                 lipgloss.Color
	Blue, Cyan, Purple, Green, Orange, Red, Yellow, Teal          lipgloss.Color
}

var defaultPalette = Palette{
	Background: "#222436", Surface: "#1e2030", SurfaceRaised: "#2f334d", Prompt: "#2d3149", Selection: "#2d3f76", Border: "#589ed7",
	Foreground: "#c8d3f5", Muted: "#828bb8", Comment: "#636da6", Status: "#7aa2f7", StatusInk: "#2e2c2c",
	Blue: "#82aaff", Cyan: "#65bcff", Purple: "#c099ff", Green: "#c3e88d", Orange: "#ff966c", Red: "#c53b53", Yellow: "#ffc777", Teal: "#4fd6be",
}

var Tokyo = defaultPalette

var (
	appBackground lipgloss.Style
	brandStyle    lipgloss.Style
	mutedStyle    lipgloss.Style
	groupStyle    lipgloss.Style
	selectedStyle lipgloss.Style
	rowStyle      lipgloss.Style
	searchStyle   lipgloss.Style
	panelStyle    lipgloss.Style
)

func init() { applyPalette(defaultPalette) }

func applyPalette(palette Palette) {
	Tokyo = palette
	appBackground = lipgloss.NewStyle().Background(Tokyo.Background).Foreground(Tokyo.Foreground)
	brandStyle = lipgloss.NewStyle().Bold(true).Foreground(Tokyo.StatusInk).Background(Tokyo.Status).Padding(0, 1)
	mutedStyle = lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.Background)
	groupStyle = lipgloss.NewStyle().Foreground(Tokyo.Comment).Bold(true).Background(Tokyo.Surface).PaddingLeft(2)
	selectedStyle = lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(Tokyo.Selection)
	rowStyle = lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(Tokyo.Surface)
	searchStyle = lipgloss.NewStyle().Background(Tokyo.Prompt).Foreground(Tokyo.Foreground).Padding(0, 2)
	panelStyle = lipgloss.NewStyle().Background(Tokyo.Surface).Foreground(Tokyo.Foreground).Padding(0, 1)
}

func configuredPalette() Palette {
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultPalette
	}
	path := filepath.Join(home, ".local", "state", "omarchy", "current", "theme", "colors.toml")
	palette, err := loadOmarchyPalette(path)
	if err != nil {
		return defaultPalette
	}
	return palette
}

func loadOmarchyPalette(path string) (Palette, error) {
	file, err := os.Open(path)
	if err != nil {
		return Palette{}, err
	}
	defer func() { _ = file.Close() }()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value, ok := tomlString(strings.TrimSpace(raw))
		if ok {
			values[strings.TrimSpace(key)] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return Palette{}, err
	}

	color := func(name string) (string, error) {
		value := values[name]
		if !validHexColor(value) {
			return "", fmt.Errorf("omarchy theme has no valid %s color", name)
		}
		return value, nil
	}

	required := []string{"background", "foreground", "accent", "selection", "muted", "red", "yellow", "green", "cyan", "blue", "magenta"}
	colors := make(map[string]string, len(required))
	for _, name := range required {
		colors[name], err = color(name)
		if err != nil {
			return Palette{}, err
		}
	}
	orange := values["orange"]
	if !validHexColor(orange) {
		orange = values["bright_yellow"]
	}
	if !validHexColor(orange) {
		orange = colors["yellow"]
	}

	background := colors["background"]
	foreground := colors["foreground"]
	accent := colors["accent"]
	return Palette{
		Background:    lipgloss.Color(background),
		Surface:       lipgloss.Color(mixHex(background, foreground, 6)),
		SurfaceRaised: lipgloss.Color(mixHex(background, foreground, 10)),
		Prompt:        lipgloss.Color(mixHex(background, accent, 12)),
		Selection:     lipgloss.Color(colors["selection"]),
		Border:        lipgloss.Color(mixHex(background, foreground, 30)),
		Foreground:    lipgloss.Color(foreground),
		Muted:         lipgloss.Color(mixHex(foreground, background, 34)),
		Comment:       lipgloss.Color(colors["muted"]),
		Status:        lipgloss.Color(accent),
		StatusInk:     lipgloss.Color(background),
		Blue:          lipgloss.Color(colors["blue"]),
		Cyan:          lipgloss.Color(colors["cyan"]),
		Purple:        lipgloss.Color(colors["magenta"]),
		Green:         lipgloss.Color(colors["green"]),
		Orange:        lipgloss.Color(orange),
		Red:           lipgloss.Color(colors["red"]),
		Yellow:        lipgloss.Color(colors["yellow"]),
		Teal:          lipgloss.Color(colors["cyan"]),
	}, nil
}

func tomlString(raw string) (string, bool) {
	if len(raw) < 2 || (raw[0] != '"' && raw[0] != '\'') {
		return "", false
	}
	quote := raw[0]
	end := strings.IndexByte(raw[1:], quote)
	if end < 0 {
		return "", false
	}
	return raw[1 : end+1], true
}

func validHexColor(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	_, err := strconv.ParseUint(value[1:], 16, 24)
	return err == nil
}

func mixHex(start, end string, endPercent int) string {
	channel := func(value string, offset int) int {
		parsed, _ := strconv.ParseUint(value[offset:offset+2], 16, 8)
		return int(parsed)
	}
	mixed := func(offset int) int {
		return (channel(start, offset)*(100-endPercent) + channel(end, offset)*endPercent + 50) / 100
	}
	return fmt.Sprintf("#%02x%02x%02x", mixed(1), mixed(3), mixed(5))
}

func keyHint(key, label string) string {
	keyPart := lipgloss.NewStyle().Foreground(Tokyo.StatusInk).Background(Tokyo.Status).Bold(true).Padding(0, 1).Render(key)
	labelPart := lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.SurfaceRaised).Padding(0, 1).Render(label)
	return keyPart + labelPart
}

func titleLine(title, subtitle string, width int) string {
	left := brandStyle.Render("GALPÓN") + lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(Tokyo.SurfaceRaised).Bold(true).Padding(0, 2).Render(title)
	right := lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.SurfaceRaised).Padding(0, 1).Render(subtitle)
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	fill := lipgloss.NewStyle().Background(Tokyo.SurfaceRaised).Render(strings.Repeat(" ", gap))
	return left + fill + right
}

func emptyState(width int) string {
	title := lipgloss.NewStyle().Foreground(Tokyo.Blue).Background(Tokyo.Surface).Bold(true).Render("Your galpón is quiet")
	copy := lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.Surface).Render("Add a repository, then create a workspace.")
	return panelStyle.Width(max(20, width-4)).Align(lipgloss.Center).Padding(2, 2).Render(title + "\n" + copy)
}
func footer(parts ...string) string {
	separator := lipgloss.NewStyle().Background(Tokyo.SurfaceRaised).Render("  ")
	return strings.Join(parts, separator)
}
func footerBar(width int, parts ...string) string {
	return lipgloss.NewStyle().Background(Tokyo.SurfaceRaised).Width(width).Render(footer(parts...))
}
func debugSize(width, height int) string { return fmt.Sprintf("%d×%d", width, height) }
