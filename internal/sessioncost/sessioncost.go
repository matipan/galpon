// Package sessioncost reads what a Pi session cost at API prices. It sums the
// cost of each reply in the session file, as the Galpon footer in Pi does.
//
// Session files get long, so a Cache keeps how far it read each file, and a
// later read reads only the new lines.
package sessioncost

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Window is the time that Cost.Recent counts.
const Window = 10 * time.Minute

// forget is the time after which the cache forgets a file that no read asked
// for.
const forget = 7 * 24 * time.Hour

// markSize is the number of bytes before the read offset that a Cache
// compares to find a file that something wrote again from the start.
const markSize = 64

// Cost is what a session cost, in US dollars at API prices. Pi records the
// price also when a subscription pays for the use.
type Cost struct {
	Total float64 `json:"total"`
	// Recent is the cost of the replies of the last Window.
	Recent float64 `json:"recent,omitempty"`
}

// Cache keeps the read position and the sums of session files. It is a JSON
// file. Two processes that use the same file at the same time lose only
// work: the next read reads again what the other one read.
type Cache struct {
	path  string
	Files map[string]*file `json:"files"`
}

type file struct {
	Offset int64    `json:"offset"`
	Mark   string   `json:"mark"`
	Total  float64  `json:"total"`
	Recent []sample `json:"recent,omitempty"`
	// Used is when a read last asked for the file, in Unix milliseconds.
	Used int64 `json:"used"`
}

type sample struct {
	At   int64   `json:"at"`
	Cost float64 `json:"cost"`
}

// Open reads the cache in path. A missing or damaged cache is empty.
func Open(path string) *Cache {
	cache := &Cache{path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, cache)
	}
	if cache.Files == nil {
		cache.Files = map[string]*file{}
	}
	return cache
}

// Read gives the cost of the session file session at the time now. It reads
// the lines that the file got since the last read. A line without its end
// waits for the next read. When the file is shorter than before, or its
// bytes before the read position changed, it reads the file again from the
// start.
func (c *Cache) Read(session string, now time.Time) (Cost, error) {
	f, err := os.Open(session)
	if err != nil {
		return Cost{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return Cost{}, err
	}
	state := c.Files[session]
	if state != nil && (info.Size() < state.Offset || mark(f, state.Offset) != state.Mark) {
		state = nil
	}
	if state == nil {
		state = &file{}
	}
	if info.Size() > state.Offset {
		if err := state.read(f, now); err != nil {
			return Cost{}, err
		}
		state.Mark = mark(f, state.Offset)
	}
	state.prune(now)
	state.Used = now.UnixMilli()
	c.Files[session] = state
	cost := Cost{Total: state.Total}
	for _, item := range state.Recent {
		cost.Recent += item.Cost
	}
	return cost, nil
}

// Save writes the cache. It forgets the files that no read asked for in a
// week.
func (c *Cache) Save(now time.Time) error {
	for path, state := range c.Files {
		if now.Sub(time.UnixMilli(state.Used)) > forget {
			delete(c.Files, path)
		}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(c.path), ".session-costs-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), c.path)
}

func (state *file) read(f *os.File, now time.Time) error {
	if _, err := f.Seek(state.Offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(f, 1<<20)
	since := now.Add(-Window).UnixMilli()
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			// A line without its end is still being written.
			return nil
		}
		if err != nil {
			return err
		}
		state.Offset += int64(len(line))
		if !bytes.Contains(line, []byte(`"cost"`)) {
			continue
		}
		cost, at := lineCost(line)
		if cost == 0 {
			continue
		}
		state.Total += cost
		if at == 0 {
			at = now.UnixMilli()
		}
		if at >= since {
			state.Recent = append(state.Recent, sample{At: at, Cost: cost})
		}
	}
}

func (state *file) prune(now time.Time) {
	since := now.Add(-Window).UnixMilli()
	kept := state.Recent[:0]
	for _, item := range state.Recent {
		if item.At >= since {
			kept = append(kept, item)
		}
	}
	state.Recent = kept
}

type usage struct {
	Cost struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

// lineCost gives the cost of one session entry and its time in Unix
// milliseconds. The replies and tool results of the model have a cost, and
// so have compactions, branch summaries and other use that Pi records.
func lineCost(line []byte) (float64, int64) {
	var entry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Usage     *usage `json:"usage"`
		Message   *struct {
			Role  string `json:"role"`
			Usage *usage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &entry) != nil {
		return 0, 0
	}
	var cost float64
	switch {
	case entry.Type == "message" && entry.Message != nil && entry.Message.Usage != nil &&
		(entry.Message.Role == "assistant" || entry.Message.Role == "toolResult"):
		cost = entry.Message.Usage.Cost.Total
	case (entry.Type == "usage" || entry.Type == "compaction" || entry.Type == "branch_summary") && entry.Usage != nil:
		cost = entry.Usage.Cost.Total
	}
	at, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		return cost, 0
	}
	return cost, at.UnixMilli()
}

// mark identifies the bytes before offset, so that a later read can find a
// file that something wrote again.
func mark(f *os.File, offset int64) string {
	start := max(0, offset-markSize)
	buffer := make([]byte, offset-start)
	if _, err := f.ReadAt(buffer, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	sum := sha256.Sum256(buffer)
	return hex.EncodeToString(sum[:8])
}
