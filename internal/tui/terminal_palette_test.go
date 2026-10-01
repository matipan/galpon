package tui

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTerminalPaletteKeepsTextReadableOnLightAndDarkPanels(t *testing.T) {
	for _, background := range []string{"#fafafa", "#161616", "#808080"} {
		t.Run(background, func(t *testing.T) {
			colors := terminalColors{background: background, foreground: "#777777"}
			for index := range colors.ansi {
				colors.ansi[index] = "#6688aa"
			}
			p := colors.palette()
			if string(p.Background) != background {
				t.Fatalf("terminal background changed: %s", p.Background)
			}
			for _, text := range []string{string(p.Foreground), string(p.Muted), string(p.Blue), string(p.Red), string(p.Status)} {
				for _, panel := range []string{background, string(p.Surface), string(p.SurfaceRaised), string(p.Prompt), string(p.Selection)} {
					if contrast := contrastRatio(text, panel); contrast < 4.5 {
						t.Errorf("text %s on %s has contrast %.2f, need 4.5", text, panel, contrast)
					}
				}
			}
			if contrastRatio(string(p.StatusInk), string(p.Status)) < 4.5 {
				t.Fatal("status band text is not readable")
			}
			if p.Selection == p.Background || p.Prompt == p.Background {
				t.Fatal("focus and prompt bands lost their backgrounds")
			}
		})
	}
}

func TestTerminalPaletteUsesANSIWithoutReplies(t *testing.T) {
	p := (terminalColors{}).palette()
	if p.Background != "" || p.Foreground != "" {
		t.Fatal("unreported terminal defaults were replaced with fixed RGB colors")
	}
	if p.Blue != "4" || p.Red != "1" || p.Green != "2" {
		t.Fatal("fallback does not use the terminal's ANSI colors")
	}
	// Background-only replies still permit readable panel colors, without
	// inventing RGB values for ANSI colors that have not been reported.
	p = (terminalColors{background: "#ffffff"}).palette()
	if contrastRatio(string(p.Foreground), string(p.Prompt)) < 4.5 || p.Blue != "4" {
		t.Fatalf("background-only palette = %#v", p)
	}
}

func TestTerminalRepliesDoNotEnterKeyboardInput(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close(); _ = write.Close() }()
	var snapshots []terminalPaletteMsg
	input := &paletteInput{File: read, emit: func(msg tea.Msg) { snapshots = append(snapshots, msg.(terminalPaletteMsg)) }}
	roundTrip := func(data string) string {
		t.Helper()
		if _, err := write.WriteString(data); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 256)
		n, err := input.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf[:n])
	}
	if got := roundTrip("a\x1b]11;rgb:11"); got != "a" {
		t.Fatalf("partial response reached keyboard: %q", got)
	}
	if got := roundTrip("11/2222/3333\x1b\\b\x1b]10;rgb:f/f/f\a\x1b]4;4;rgb:12/34/56;1;rgb:ff/00/00\ac"); got != "bc" {
		t.Fatalf("response mixed with typing = %q", got)
	}
	if len(snapshots) != 1 || input.colors.background != "#112233" || input.colors.foreground != "#ffffff" || input.colors.ansi[4] != "#123456" || input.colors.ansi[1] != "#ff0000" {
		t.Fatalf("decoded colors = %#v; snapshots = %d", input.colors, len(snapshots))
	}
	keys := "\x1b[A\x1b[200~paste λ\x1b[201~\t\r"
	if got := roundTrip(keys); got != keys {
		t.Fatalf("keyboard/paste changed: %q", got)
	}
	if got := roundTrip("\x1b"); got != "\x1b" {
		t.Fatalf("standalone Escape was held: %q", got)
	}
	if got := roundTrip("\x1b]4;999;rgb:f/f/f\a\x1b]11;rgb:wrong\aok"); got != "ok" || len(snapshots) != 1 {
		t.Fatalf("malformed replies changed the palette or input: %q", got)
	}
	_ = roundTrip("\x1b]11;rgb:")
	input.started = time.Now().Add(-2 * time.Second)
	if got := roundTrip("next"); got != "next" {
		t.Fatalf("truncated reply swallowed later input: %q", got)
	}
}

type paletteTestModel struct{ Model }

func (m paletteTestModel) Init() tea.Cmd { return nil }

type paletteQueryWriter func([]byte) (int, error)

func (write paletteQueryWriter) Write(p []byte) (int, error) { return write(p) }

func TestTerminalProgramHandlesRepliesAndUnsupportedTerminals(t *testing.T) {
	for _, replies := range []string{"", "\x1b]11;rgb:fa/fa/fa\x1b\\\x1b]10;rgb:11/11/11\a\x1b]4;4;rgb:12/34/56\a"} {
		t.Run(map[bool]string{true: "reported", false: "unsupported"}[replies != ""], func(t *testing.T) {
			previous := Tokyo
			t.Cleanup(func() { applyPalette(previous) })
			applyPalette(ansiPalette)
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = read.Close(); _ = write.Close() }()
			queries := 0
			output := paletteQueryWriter(func(data []byte) (int, error) {
				if strings.Contains(string(data), "\x1b]10;?") {
					queries++
					_, err := io.WriteString(write, replies+"Reviewer λ\x03")
					return len(data), err
				}
				return len(data), nil
			})
			input := &paletteInput{File: read}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			program := tea.NewProgram(terminalModel{Model: paletteTestModel{New(nil, nil)}, output: output},
				tea.WithInput(input), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithContext(ctx), tea.WithoutSignalHandler())
			input.emit = program.Send
			final, err := program.Run()
			if err != nil {
				t.Fatal(err)
			}
			model := final.(terminalModel).Model.(Model)
			if model.query.Value() != "Reviewer λ" || queries != 1 {
				t.Fatalf("search = %q, query batches = %d", model.query.Value(), queries)
			}
			if replies != "" && Tokyo.Background != "#fafafa" || replies == "" && Tokyo.Background != "" {
				t.Fatalf("unexpected background: %q", Tokyo.Background)
			}
		})
	}
}

func TestTerminalPaletteUpdatePreservesSearch(t *testing.T) {
	previous := Tokyo
	t.Cleanup(func() { applyPalette(previous) })
	applyPalette(ansiPalette)
	m := New(nil, nil)
	m.query.SetValue("Reviewer")
	updated, _ := m.Update(terminalPaletteMsg{background: "#ffffff", foreground: "#111111"})
	next := updated.(Model)
	if next.query.Value() != "Reviewer" || !next.query.Focused() {
		t.Fatal("terminal reply reset the search or its focus")
	}
	if next.query.TextStyle.GetForeground() != Tokyo.Foreground || next.query.TextStyle.GetBackground() != Tokyo.Prompt {
		t.Fatal("search input kept the old colors after a palette reply")
	}
}
