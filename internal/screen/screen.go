// Package screen lets another computer use the Galpon of this computer.
//
// Galpon runs on one computer: the host. A screen is another computer with
// the Herdr app. herdr-core shows the terminals of the host in the app of the
// screen, and the galpon command of the screen runs each command on the host
// over SSH, with the terminal of the screen.
//
// During a command, the host sends messages to the screen in the output of
// the command, as a private OSC sequence: a terminal that the command opened,
// which the screen names and focuses in its own app window, or a web link,
// which the screen opens in its own browser.
package screen

import (
	"crypto/rand"
	"debug/elf"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/matipan/galpon/internal/herdrapp"
)

// Variable is set in the environment of a command that a screen runs on the
// host. Its value is the token of the messages to that screen.
const Variable = "GALPON_SCREEN"

// Active reports whether a screen runs this command.
func Active() bool { return os.Getenv(Variable) != "" }

// Message is what the host sends to the screen during a command.
type Message struct {
	// Place is a terminal that the command opened on the host, with the IDs
	// of the host.
	Place *herdrapp.Placement `json:"place,omitempty"`
	// URL is a web link to open in the browser of the screen.
	URL string `json:"url,omitempty"`
	// Update means that the galpon of the host is another build than the
	// galpon of the screen.
	Update bool `json:"update,omitempty"`
}

// prefix starts a message: ESC ] 7742 ; galpon ; <token> ; <JSON> BEL.
const prefix = "\x1b]7742;galpon;"

// Send writes a message to the screen that runs this command, on stderr.
// Without a screen, it does nothing.
func Send(message Message) error {
	token := os.Getenv(Variable)
	if token == "" {
		return nil
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	// One write keeps the sequence whole between the writes of a TUI to the
	// same terminal.
	_, err = os.Stderr.Write([]byte(prefix + token + ";" + string(data) + "\a"))
	return err
}

// OpenURL opens a web link in the browser. When a screen runs this command,
// the screen opens the link in its browser.
func OpenURL(link string) error {
	if Active() {
		return Send(Message{URL: link})
	}
	return openLocally(link)
}

// openLocally opens a web link in the browser of this computer. It opens only
// http and https links, because the link can come from another computer. The
// opener runs in its own session and without the herdr-core scope, so the
// browser stays open after the popup or the terminal closes.
func openLocally(link string) error {
	parsed, err := url.Parse(link)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("open %s: not a web link", link)
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	command := exec.Command(name, link)
	command.Env = herdrapp.WithoutScope(os.Environ())
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("open %s: %w", link, err)
	}
	go func() { _ = command.Wait() }()
	return nil
}

// Info is what `galpon version --json` prints. The screen reads it from the
// host.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	// Build identifies the program: its Go build ID, or else its commit. It
	// changes with each change of the program, also without a new commit.
	Build string `json:"build"`
	OS    string `json:"os"`
	Arch  string `json:"arch"`
	// Executable is the galpon executable, without symbolic links.
	Executable string `json:"executable"`
}

// BuildID returns the Go build ID of an executable. A copy of the file has
// the same ID. It reads only ELF files, the format of Linux.
func BuildID(path string) (string, error) {
	file, err := elf.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	section := file.Section(".note.go.buildid")
	if section == nil {
		return "", errors.New("the executable has no Go build ID")
	}
	data, err := section.Data()
	if err != nil {
		return "", err
	}
	// An ELF note: name size, description size, type, the name "Go" with
	// padding to 4 bytes, then the ID.
	if len(data) < 12 {
		return "", errors.New("the Go build ID note is too short")
	}
	nameSize, idSize := uint64(file.ByteOrder.Uint32(data[0:4])), uint64(file.ByteOrder.Uint32(data[4:8]))
	start := 12 + (nameSize+3)&^3
	if start+idSize > uint64(len(data)) || idSize == 0 {
		return "", errors.New("the Go build ID note is not valid")
	}
	return string(data[start : start+idSize]), nil
}

// Setting makes this computer a screen of a host. galpon connect writes it.
type Setting struct {
	// Host is the herdr-core machine name of the host.
	Host string `json:"host"`
	// Target is how ssh reaches the host: the target of that machine.
	Target string `json:"target"`
	// Galpon is the galpon command on the host, as the login shell of the
	// host finds it, for example a version manager shim.
	Galpon string `json:"galpon"`
	// Fixed means that the person gave Galpon. Without it, each update finds
	// galpon on the host again, so the screen follows a move.
	Fixed bool `json:"fixed,omitempty"`
	// OS and Arch are the system and the CPU of the host.
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Path returns the setting file: galpon/screen.json in the configuration
// folder.
func Path() (string, error) {
	root := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(root) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate the Galpon configuration: %w", err)
		}
		root = filepath.Join(home, ".config")
	}
	return filepath.Join(root, "galpon", "screen.json"), nil
}

// Load returns the setting of this computer, and false when this computer is
// not a screen.
func Load() (Setting, bool, error) {
	path, err := Path()
	if err != nil {
		return Setting{}, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Setting{}, false, nil
	}
	if err != nil {
		return Setting{}, false, err
	}
	var setting Setting
	if err := json.Unmarshal(data, &setting); err != nil {
		return Setting{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	if setting.Host == "" || setting.Target == "" || !filepath.IsAbs(setting.Galpon) {
		return Setting{}, false, fmt.Errorf("read %s: it needs host, target and the absolute galpon path", path)
	}
	return setting, true, nil
}

// Save writes the setting of this computer.
func Save(setting Setting) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(setting, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// Remove removes the setting of this computer. It returns false when this
// computer was not a screen.
func Remove() (bool, error) {
	path, err := Path()
	if err != nil {
		return false, err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Request is a command that a screen runs on the host, as
// `galpon screen run <encoded request>`. The encoded form needs no quotes in
// the login shell of the host.
type Request struct {
	Args []string `json:"args"`
	// Env has the settings of the screen terminal that change how the
	// command draws.
	Env map[string]string `json:"env,omitempty"`
	// Token marks the messages to this command of the screen.
	Token string `json:"token"`
	// Build is the build of the galpon of the screen.
	Build string `json:"build,omitempty"`
}

// DisplayVariables are the variables of the screen terminal that a command
// on the host gets. ssh sets TERM.
var DisplayVariables = []string{"COLORTERM", "TERM_PROGRAM", "NO_COLOR", "GALPON_ASCII", "GALPON_UI_MOTION"}

// Encode returns the request as one shell word.
func (r Request) Encode() string {
	data, _ := json.Marshal(r)
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeRequest reads an encoded request.
func DecodeRequest(text string) (Request, error) {
	data, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return Request{}, fmt.Errorf("read the screen request: %w", err)
	}
	var request Request
	if err := json.Unmarshal(data, &request); err != nil {
		return Request{}, fmt.Errorf("read the screen request: %w", err)
	}
	if request.Token == "" {
		return Request{}, errors.New("read the screen request: it has no token")
	}
	return request, nil
}

func newToken() string {
	var data [16]byte
	_, _ = rand.Read(data[:])
	return hex.EncodeToString(data[:])
}
