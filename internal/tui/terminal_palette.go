package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Empty colors leave the terminal's foreground/background unchanged. ANSI
// indices use the terminal's own palette, including when OSC is unsupported.
var ansiPalette = Palette{
	Border: "8", Status: "4", StatusInk: "7",
	Blue: "4", Cyan: "6", Purple: "5", Green: "2", Orange: "3",
	Red: "1", Yellow: "3", Teal: "6",
}

type terminalColors struct {
	foreground, background string
	ansi                   [16]string
}

type terminalPaletteMsg terminalColors

func terminalColorQueries() string {
	var query strings.Builder
	query.WriteString("\x1b]10;?\x1b\\\x1b]11;?\x1b\\")
	for index := range 16 {
		fmt.Fprintf(&query, "\x1b]4;%d;?\x1b\\", index)
	}
	return query.String()
}

// OSC colors use one to four hex digits per channel. Scale the entire value;
// truncating the first byte gives incorrect results for shorter components.
func parseTerminalRGB(value string) (string, bool) {
	if !strings.HasPrefix(value, "rgb:") {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(value, "rgb:"), "/")
	if len(parts) != 3 {
		return "", false
	}
	var channels [3]uint64
	for index, part := range parts {
		if len(part) < 1 || len(part) > 4 {
			return "", false
		}
		channel, err := strconv.ParseUint(part, 16, 16)
		if err != nil {
			return "", false
		}
		maximum := uint64(1)<<(4*len(part)) - 1
		channels[index] = (channel*255 + maximum/2) / maximum
	}
	return fmt.Sprintf("#%02x%02x%02x", channels[0], channels[1], channels[2]), true
}

func (colors *terminalColors) accept(response string) bool {
	parts := strings.Split(response, ";")
	if len(parts) < 2 {
		return false
	}
	switch parts[0] {
	case "10", "11":
		if len(parts) != 2 {
			return false
		}
		value, ok := parseTerminalRGB(parts[1])
		if !ok {
			return false
		}
		if parts[0] == "10" {
			colors.foreground = value
		} else {
			colors.background = value
		}
		return true
	case "4":
		changed := false
		for index := 1; index+1 < len(parts); index += 2 {
			slot, err := strconv.Atoi(parts[index])
			value, ok := parseTerminalRGB(parts[index+1])
			if err == nil && slot >= 0 && slot < len(colors.ansi) && ok {
				colors.ansi[slot] = value
				changed = true
			}
		}
		return changed
	}
	return false
}

func (colors terminalColors) palette() Palette {
	p := ansiPalette
	p.Foreground, p.Background = lipgloss.Color(colors.foreground), lipgloss.Color(colors.background)
	// Without a reported background we cannot calculate contrast or panel fills.
	if colors.background == "" {
		return p
	}
	background := colors.background
	foreground := colors.foreground
	if foreground == "" {
		foreground = "#ffffff"
		if contrastRatio("#000000", background) > contrastRatio(foreground, background) {
			foreground = "#000000"
		}
	}
	foreground = readableColor(foreground, []string{background}, 4.5)
	accent := colors.ansi[4]
	if accent == "" {
		accent = foreground
	}
	p.Foreground = lipgloss.Color(foreground)
	p.Surface = lipgloss.Color(mixHex(background, foreground, 4))
	p.SurfaceRaised = lipgloss.Color(mixHex(background, foreground, 8))
	p.Prompt = lipgloss.Color(mixHex(background, accent, 10))
	p.Selection = lipgloss.Color(mixHex(background, accent, 20))
	panels := []string{background, string(p.Surface), string(p.SurfaceRaised), string(p.Prompt), string(p.Selection)}
	p.Foreground = lipgloss.Color(readableColor(foreground, panels, 4.5))
	p.Muted = lipgloss.Color(readableColor(mixHex(background, foreground, 65), panels, 4.5))
	p.Comment = p.Muted
	p.Border = lipgloss.Color(readableColor(mixHex(background, foreground, 35), panels, 3))
	color := func(index int) lipgloss.Color {
		value := colors.ansi[index]
		if value == "" {
			return lipgloss.Color(strconv.Itoa(index))
		}
		return lipgloss.Color(readableColor(value, panels, 4.5))
	}
	p.Blue, p.Cyan, p.Purple = color(4), color(6), color(5)
	p.Green, p.Red, p.Yellow = color(2), color(1), color(3)
	p.Orange, p.Teal = color(11), p.Cyan
	// Keep the status band blue, but choose its text for contrast independently.
	p.Status = lipgloss.Color(readableColor(accent, panels, 4.5))
	p.StatusInk = "#000000"
	if contrastRatio("#ffffff", string(p.Status)) > contrastRatio(string(p.StatusInk), string(p.Status)) {
		p.StatusInk = "#ffffff"
	}
	return p
}

func luminance(hex string) float64 {
	var channels [3]float64
	for index := range channels {
		value, _ := strconv.ParseUint(hex[1+index*2:3+index*2], 16, 8)
		channel := float64(value) / 255
		if channel <= 0.04045 {
			channels[index] = channel / 12.92
		} else {
			channels[index] = math.Pow((channel+0.055)/1.055, 2.4)
		}
	}
	return channels[0]*0.2126 + channels[1]*0.7152 + channels[2]*0.0722
}

func contrastRatio(first, second string) float64 {
	a, b := luminance(first), luminance(second)
	return (math.Max(a, b) + 0.05) / (math.Min(a, b) + 0.05)
}

func readableColor(value string, backgrounds []string, minimum float64) string {
	contrast := func(color string) float64 {
		result := math.Inf(1)
		for _, background := range backgrounds {
			result = math.Min(result, contrastRatio(color, background))
		}
		return result
	}
	if contrast(value) >= minimum {
		return value
	}
	target := "#ffffff"
	if contrast("#000000") > contrast(target) {
		target = "#000000"
	}
	for percent := 1; percent <= 100; percent++ {
		adjusted := mixHex(value, target, percent)
		if contrast(adjusted) >= minimum {
			return adjusted
		}
	}
	return target
}
