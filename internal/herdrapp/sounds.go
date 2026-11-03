package herdrapp

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// soundFiles holds the barn sounds of the Galpon layout. synth.py in the
// same folder makes them.
//
//go:embed sounds/*.mp3
var soundFiles embed.FS

// The barn sounds, as settings.json names them: relative to the app home
// folder. The app plays the moo when an agent needs you and the rooster when
// an agent finished while you did not look at it. The Galpon layout plays
// the cluck when a background task of an agent ends.
const (
	MooSound     = "sounds/galpon-moo.mp3"
	RoosterSound = "sounds/galpon-rooster.mp3"
	CluckSound   = "sounds/galpon-cluck.mp3"
)

// barnSounds maps the sound keys of settings.json to the barn sounds.
var barnSounds = map[string]string{"blocked": MooSound, "done": RoosterSound}

// writeSounds copies the barn sounds into the sounds folder of the app home.
// It does not select them.
func writeSounds(home string) error {
	dir := filepath.Join(home, "sounds")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := soundFiles.ReadDir("sounds")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		data, err := soundFiles.ReadFile("sounds/" + entry.Name())
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(dir, entry.Name()), data); err != nil {
			return err
		}
	}
	return nil
}

// Sounds tells which alert sounds the app plays.
type Sounds struct {
	// Barn is true when the app plays the barn sounds.
	Barn bool
	// Enabled is false when the person turned off the app sounds.
	Enabled bool
}

// settings is settings.json of the app, with its sound settings apart. The
// other settings stay as they are.
type settings struct {
	path   string
	all    map[string]json.RawMessage
	sounds map[string]json.RawMessage
}

func readSettings(home string) (settings, error) {
	out := settings{path: filepath.Join(home, "settings.json"), all: map[string]json.RawMessage{}, sounds: map[string]json.RawMessage{}}
	data, err := os.ReadFile(out.path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out.all); err != nil {
		return out, fmt.Errorf("read %s: %w", out.path, err)
	}
	if raw, ok := out.all["sounds"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &out.sounds); err != nil {
			return out, fmt.Errorf("read the sounds of %s: %w", out.path, err)
		}
	}
	return out, nil
}

// file gives the sound file of key, or "".
func (s settings) file(key string) string {
	var file string
	_ = json.Unmarshal(s.sounds[key], &file)
	return file
}

func (s settings) state() Sounds {
	enabled := true
	if raw, ok := s.sounds["enabled"]; ok {
		_ = json.Unmarshal(raw, &enabled)
	}
	barn := true
	for key, file := range barnSounds {
		barn = barn && s.file(key) == file
	}
	return Sounds{Barn: barn, Enabled: enabled}
}

func (s settings) write() error {
	if len(s.sounds) == 0 {
		delete(s.all, "sounds")
	} else {
		raw, err := json.Marshal(s.sounds)
		if err != nil {
			return err
		}
		s.all["sounds"] = raw
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(s.all); err != nil {
		return err
	}
	return writeFile(s.path, out.Bytes())
}

// ReadSounds reads the sound settings of the app.
func ReadSounds(home string) (Sounds, error) {
	s, err := readSettings(home)
	if err != nil {
		return Sounds{}, err
	}
	return s.state(), nil
}

// UseBarnSounds makes the app play the barn sounds. It keeps the other
// settings, and it returns the sound files that it replaced.
func UseBarnSounds(home string) (map[string]string, Sounds, error) {
	for _, file := range barnSounds {
		if _, err := os.Stat(filepath.Join(home, file)); err != nil {
			return nil, Sounds{}, fmt.Errorf("the barn sounds are not in %s; run galpon herdr-app install first", filepath.Join(home, "sounds"))
		}
	}
	s, err := readSettings(home)
	if err != nil {
		return nil, Sounds{}, err
	}
	replaced := map[string]string{}
	for key, file := range barnSounds {
		if current := s.file(key); current != "" && current != file {
			replaced[key] = current
		}
		raw, err := json.Marshal(file)
		if err != nil {
			return nil, Sounds{}, err
		}
		s.sounds[key] = raw
	}
	return replaced, s.state(), s.write()
}

// UseAppSounds makes the app play its own alert sounds again. It removes
// only the barn sounds: it returns the other sound files, which stay.
func UseAppSounds(home string) (map[string]string, Sounds, error) {
	s, err := readSettings(home)
	if err != nil {
		return nil, Sounds{}, err
	}
	kept := map[string]string{}
	changed := false
	for key, file := range barnSounds {
		switch current := s.file(key); current {
		case "":
		case file:
			delete(s.sounds, key)
			changed = true
		default:
			kept[key] = current
		}
	}
	if !changed {
		return kept, s.state(), nil
	}
	return kept, s.state(), s.write()
}
