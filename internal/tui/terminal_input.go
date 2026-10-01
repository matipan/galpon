package tui

import (
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

// Color replies share the keyboard stream. Consume them before Bubble Tea v1
// interprets them as keys. Keep the real file descriptor so Tea still owns raw
// mode, cancellation, and terminal release while an external editor runs.
// Only this reader accesses the terminal input; there is no second read loop.
type paletteInput struct {
	*os.File
	colors  terminalColors
	emit    func(tea.Msg)
	escape  bool
	osc     bool
	control []byte
	started time.Time
}

func (input *paletteInput) Read(p []byte) (int, error) {
	if len(p) < 2 {
		return 0, io.ErrShortBuffer
	}
	// Reserve a byte only when an Escape was held at a read boundary.
	size := len(p)
	if input.escape && !input.osc {
		size--
	}
	n, err := input.File.Read(p[:size])
	if n == 0 {
		return n, err
	}
	data := append([]byte(nil), p[:n]...)
	if input.osc && time.Since(input.started) > time.Second {
		// A truncated reply must not consume subsequent user input forever.
		input.osc, input.escape = false, false
		input.control = input.control[:0]
	}
	output := p[:0]
	changed := false
	for _, b := range data {
		if input.osc {
			if b == '\a' || b == '\\' && input.escape {
				if changedReply := input.colors.accept(string(input.control)); changedReply {
					changed = true
				}
				input.osc, input.escape = false, false
				input.control = input.control[:0]
				continue
			}
			if b == '\x1b' {
				input.escape = true
				continue
			}
			input.escape = false
			input.control = append(input.control, b)
			if len(input.control) > 1024 {
				input.osc = false
				input.control = input.control[:0]
			}
			continue
		}
		if input.escape {
			input.escape = false
			if b == ']' {
				input.osc = true
				input.started = time.Now()
				continue
			}
			output = append(output, '\x1b')
		}
		if b == '\x1b' {
			input.escape = true
		} else {
			output = append(output, b)
		}
	}
	if input.escape && !input.osc {
		// Do not hold a user's standalone Escape until another key is pressed.
		// A short look-ahead also joins OSC prefixes split across OS reads.
		ready, _ := unix.Poll([]unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}, 10)
		if ready <= 0 {
			output = append(output, '\x1b')
			input.escape = false
		}
	}
	if changed && input.emit != nil {
		input.emit(terminalPaletteMsg(input.colors))
	}
	return len(output), err
}

type terminalModel struct {
	tea.Model
	output io.Writer
}

func (m terminalModel) Init() tea.Cmd {
	if m.output == nil {
		return m.Model.Init()
	}
	return tea.Batch(m.Model.Init(), func() tea.Msg {
		_, _ = io.WriteString(m.output, terminalColorQueries())
		return nil
	})
}

func (m terminalModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.Model.Update(msg)
	m.Model = updated
	return m, cmd
}

func runTerminalProgram(makeModel func() tea.Model) (tea.Model, error) {
	applyPalette(ansiPalette)
	input := os.Stdin
	if !term.IsTerminal(input.Fd()) {
		if tty, err := os.Open("/dev/tty"); err == nil {
			input = tty
			defer func() { _ = tty.Close() }()
		}
	}
	model := terminalModel{Model: makeModel()}
	options := []tea.ProgramOption{tea.WithAltScreen()}
	var reader *paletteInput
	if term.IsTerminal(input.Fd()) && term.IsTerminal(os.Stdout.Fd()) && os.Getenv("TERM") != "dumb" {
		reader = &paletteInput{File: input}
		options = append(options, tea.WithInput(reader))
		model.output = os.Stdout
	}
	program := tea.NewProgram(model, options...)
	if reader != nil {
		reader.emit = program.Send
	}
	final, err := program.Run()
	if wrapped, ok := final.(terminalModel); ok {
		return wrapped.Model, err
	}
	return final, err
}
