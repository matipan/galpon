package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var Tokyo = struct {
	Background, Surface, SurfaceRaised, Prompt, Selection, Border lipgloss.Color
	Foreground, Muted, Comment, Status, StatusInk                 lipgloss.Color
	Blue, Cyan, Purple, Green, Orange, Red, Yellow, Teal          lipgloss.Color
}{
	Background: "#222436", Surface: "#1e2030", SurfaceRaised: "#2f334d", Prompt: "#2d3149", Selection: "#2d3f76", Border: "#589ed7",
	Foreground: "#c8d3f5", Muted: "#828bb8", Comment: "#636da6", Status: "#7aa2f7", StatusInk: "#2e2c2c",
	Blue: "#82aaff", Cyan: "#65bcff", Purple: "#c099ff", Green: "#c3e88d", Orange: "#ff966c", Red: "#c53b53", Yellow: "#ffc777", Teal: "#4fd6be",
}

var (
	appBackground = lipgloss.NewStyle().Background(Tokyo.Background).Foreground(Tokyo.Foreground)
	brandStyle    = lipgloss.NewStyle().Bold(true).Foreground(Tokyo.StatusInk).Background(Tokyo.Status).Padding(0, 1)
	mutedStyle    = lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.Background)
	groupStyle    = lipgloss.NewStyle().Foreground(Tokyo.Comment).Bold(true).Background(Tokyo.Surface).PaddingLeft(2)
	selectedStyle = lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(Tokyo.Selection)
	rowStyle      = lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(Tokyo.Surface)
	searchStyle   = lipgloss.NewStyle().Background(Tokyo.Prompt).Foreground(Tokyo.Foreground).Padding(0, 2)
	panelStyle    = lipgloss.NewStyle().Background(Tokyo.Surface).Foreground(Tokyo.Foreground).Padding(0, 1)
)

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
