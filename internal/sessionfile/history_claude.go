package sessionfile

import (
	"fmt"
	"slices"
)

// Claude reinserts retained messages by relinking their parents in memory.
// Their saved parents still refer to the pre-compaction conversation.
func (h *History) claudeBranches() ([]int, error) {
	parents := make(map[string]string, len(h.byID))
	latest := -1
	for id, index := range h.byID {
		parents[id] = h.entries[index].parent
		if h.entries[index].boundary && index > latest {
			latest = index
		}
	}
	if latest >= 0 {
		row, err := h.entry(latest)
		if err != nil {
			return nil, err
		}
		metadata := object(row["compactMetadata"])
		kept := object(metadata["preservedMessages"])
		anchor := str(kept["anchorUuid"])
		var ids []string
		for _, id := range array(kept["uuids"]) {
			ids = append(ids, str(id))
		}
		if kept == nil {
			segment := object(metadata["preservedSegment"])
			anchor = str(segment["anchorUuid"])
			seen := make(map[string]bool)
			for id := str(segment["tailUuid"]); id != "" && !seen[id]; id = parents[id] {
				seen[id] = true
				ids = append(ids, id)
				if id == str(segment["headUuid"]) {
					break
				}
			}
			slices.Reverse(ids)
		}
		_, valid := parents[anchor]
		for _, id := range ids {
			if _, exists := parents[id]; !exists || id == anchor {
				valid = false
			}
		}
		if valid && len(ids) > 0 {
			previous := anchor
			for _, id := range ids {
				parents[id], previous = previous, id
			}
			for id, parent := range parents {
				if parent == anchor && id != ids[0] {
					parents[id] = ids[len(ids)-1]
				}
			}
		}
	}
	walk := func(start string, context bool) ([]int, error) {
		var out []int
		seen := make(map[string]bool)
		for id := start; id != ""; {
			index, exists := h.byID[id]
			if !exists {
				// Native snapshots can start after the referenced parent.
				break
			}
			if seen[id] {
				return nil, fmt.Errorf("source Claude conversation has cyclic ancestry")
			}
			seen[id] = true
			out = append(out, index)
			entry := h.entries[index]
			if context {
				if entry.boundary {
					break
				}
				id = parents[id]
			} else {
				id = entry.parent
				if entry.boundary && id == "" {
					id = entry.logical
					if _, exists := h.byID[id]; !exists {
						for prior := index - 1; prior >= 0; prior-- {
							if candidate := h.entries[prior]; candidate.id != "" && !candidate.sidechain {
								id = candidate.id
								break
							}
						}
					}
				}
			}
		}
		slices.Reverse(out)
		return out, nil
	}
	var err error
	h.claudeContext, err = walk(h.leaf, true)
	if err != nil {
		return nil, err
	}
	archive, err := walk(h.leaf, false)
	if err != nil {
		return nil, err
	}
	for _, index := range h.claudeContext {
		archive = append(archive, index)
		if h.entries[index].boundary {
			older, err := walk(h.entries[index].id, false)
			if err != nil {
				return nil, err
			}
			archive = append(archive, older...)
		}
	}
	slices.Sort(archive)
	return slices.Compact(archive), nil
}

func (h *History) projectClaude(_ []int) error {
	for _, index := range h.claudeContext {
		refs, err := h.entryMessages(index)
		if err != nil {
			return err
		}
		h.context = append(h.context, refs...)
	}
	return nil
}
