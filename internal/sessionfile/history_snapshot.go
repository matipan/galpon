package sessionfile

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type historyEntry struct {
	id, parent, logical, kind string
	offset, size              int64
	sidechain, boundary       bool
}

var errHistoryChanged = errors.New("source conversation changed while its snapshot was copied")

// snapshotHistory fixes a complete-record prefix. Appends after the cut do not
// affect the fork. Rewrites and path replacements require another snapshot.
func snapshotHistory(path, target string) error {
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("conversation source is not a regular file")
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close() }()
	if _, err := io.CopyN(output, source, info.Size()); err != nil {
		if errors.Is(err, io.EOF) {
			return errHistoryChanged
		}
		return err
	}
	// A writer may be in the middle of its final JSON record.
	end := info.Size()
	buffer := make([]byte, 64<<10)
	for end > 0 {
		start := max(int64(0), end-int64(len(buffer)))
		n, err := output.ReadAt(buffer[:end-start], start)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if at := bytes.LastIndexByte(buffer[:n], '\n'); at >= 0 {
			end = start + int64(at) + 1
			break
		}
		end = start
	}
	if end == 0 {
		return fmt.Errorf("source conversation has no complete saved records")
	}
	if err := output.Truncate(end); err != nil {
		return err
	}
	copied, current := sha256.New(), sha256.New()
	if _, err := io.Copy(copied, io.NewSectionReader(output, 0, end)); err != nil {
		return err
	}
	if _, err := io.CopyN(current, io.NewSectionReader(source, 0, end), end); err != nil {
		return errHistoryChanged
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(info, after) || !bytes.Equal(copied.Sum(nil), current.Sum(nil)) {
		return errHistoryChanged
	}
	return output.Sync()
}

func decodeHistory(data []byte) (map[string]any, error) {
	var row map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&row); err != nil {
		return nil, err
	}
	if row == nil || !json.Valid(data) {
		return nil, fmt.Errorf("invalid conversation record")
	}
	return row, nil
}

func (h *History) entry(index int) (map[string]any, error) {
	entry := h.entries[index]
	data := make([]byte, entry.size)
	if _, err := h.source.ReadAt(data, entry.offset); err != nil {
		return nil, err
	}
	return decodeHistory(data)
}

func (h *History) indexSource() error {
	reader := bufio.NewReader(h.source)
	var offset int64
	previous := ""
	version := "3"
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		row, err := decodeHistory(line)
		if err != nil {
			return fmt.Errorf("invalid saved conversation at byte %d: %w", offset, err)
		}
		kind := str(row["type"])
		if offset == 0 {
			if h.Harness == "pi" {
				if kind != "session" {
					return fmt.Errorf("the Pi conversation must start with session metadata")
				}
				version = fmt.Sprint(row["version"])
				if row["version"] == nil {
					version = "1"
				}
			}
			h.cwd = str(row["cwd"])
			if h.Harness == "codex" {
				h.cwd = str(object(row["payload"])["cwd"])
			}
		}
		entry := historyEntry{kind: kind, offset: offset, size: int64(len(line)), sidechain: row["isSidechain"] == true,
			boundary: row["subtype"] == "compact_boundary"}
		if h.Harness == "pi" && kind != "session" {
			entry.id, entry.parent = str(row["id"]), str(row["parentId"])
			if version == "1" {
				entry.id, entry.parent = fmt.Sprintf("entry-%d", len(h.entries)), previous
			}
			if entry.id == "" {
				return fmt.Errorf("the Pi conversation entry has no identity")
			}
			if parent := row["parentId"]; version != "1" && parent != nil {
				if _, ok := parent.(string); !ok {
					return fmt.Errorf("source conversation has invalid ancestry")
				}
			}
			previous = entry.id
		} else if h.Harness == "claude" {
			entry.id, entry.parent, entry.logical = str(row["uuid"]), str(row["parentUuid"]), str(row["logicalParentUuid"])
			if entry.id == "" && (kind == "user" || kind == "assistant") {
				return fmt.Errorf("the Claude conversation message has no identity")
			}
		}
		h.entries = append(h.entries, entry)
		if entry.id != "" && !entry.sidechain {
			if _, exists := h.byID[entry.id]; exists && h.Harness == "pi" {
				return fmt.Errorf("source conversation has duplicate entry identities")
			}
			// Claude can append an updated record with the same UUID.
			h.byID[entry.id] = len(h.entries) - 1
			if h.Harness == "pi" || kind == "user" || kind == "assistant" || kind == "attachment" || kind == "system" {
				h.leaf = entry.id
			}
		}
		offset += int64(len(line))
	}
	if h.cwd == "" {
		h.cwd = filepath.Dir(h.originalPath)
	}
	return nil
}

func (h *History) branch() ([]int, error) {
	if h.Harness == "claude" {
		return h.claudeBranches()
	}
	if h.Harness == "codex" {
		out := make([]int, len(h.entries))
		for i := range out {
			out[i] = i
		}
		return out, nil
	}
	var out []int
	seen := make(map[string]bool)
	for id := h.leaf; id != ""; {
		index, ok := h.byID[id]
		if !ok || seen[id] {
			return nil, fmt.Errorf("source conversation has incomplete or cyclic ancestry")
		}
		seen[id] = true
		out = append(out, index)
		entry := h.entries[index]
		id = entry.parent
		if id == "" && entry.boundary {
			if _, exists := h.byID[entry.logical]; exists {
				id = entry.logical
			} else {
				// A compaction checkpoint can outlive the referenced older parent.
				// Its summary still supplies the complete saved active context.
				for i := index - 1; i >= 0; i-- {
					if e := h.entries[i]; e.id != "" && !e.sidechain {
						id = e.id
						break
					}
				}
			}
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
