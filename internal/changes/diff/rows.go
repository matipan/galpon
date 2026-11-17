package diff

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// rows makes the rows of a file: each change with its context lines, folds
// for long runs of unchanged lines, a scope row after each fold, and a row
// at each end of a moved block.
func (w *fileWork) rows(inputs []Input, opts Options, oldSpans, newSpans [][]Span, newEntities, oldEntities []Entity) []Row {
	if len(w.blocks) == 0 {
		return nil
	}
	var rows []Row
	context := func(o, n int) {
		rows = append(rows, Row{Kind: RowContext, Old: o + 1, New: n + 1, Segs: segments(w.new[n], spanAt(newSpans, n), nil, &Row{})})
	}
	equal := func(o, n, count int, first, last bool) {
		before, after := opts.Context, opts.Context
		if first {
			before = 0
		}
		if last {
			after = 0
		}
		if count < before+after+opts.MinFold {
			for k := 0; k < count; k++ {
				context(o+k, n+k)
			}
			return
		}
		for k := 0; k < before; k++ {
			context(o+k, n+k)
		}
		hidden := count - before - after
		fold := &Fold{Old: o + before + 1, New: n + before + 1, Count: hidden}
		for _, entity := range newEntities {
			if entity.Header >= n+before && entity.Header < n+before+hidden && len(fold.Entities) < 4 {
				fold.Entities = append(fold.Entities, entity.Kind+" "+entity.Name)
			}
		}
		rows = append(rows, Row{Kind: RowFold, Old: fold.Old, New: fold.New, Fold: fold})
		for k := count - after; k < count; k++ {
			context(o+k, n+k)
		}
	}
	oldAt, newAt := 0, 0
	for index, b := range w.blocks {
		gap := b.oldStart - oldAt
		before := len(rows)
		equal(oldAt, newAt, gap, index == 0, false)
		folded := false
		for _, row := range rows[before:] {
			folded = folded || row.Kind == RowFold
		}
		if folded || (index == 0 && gap == 0) {
			if scope := scopeOf(b, newEntities, oldEntities); scope != "" {
				// The scope row goes before the context lines of the change.
				at := len(rows)
				for at > before && rows[at-1].Kind == RowContext {
					at--
				}
				rows = append(rows[:at], append([]Row{{Kind: RowHunk, Old: b.oldStart + 1, New: b.newStart + 1, Text: scope}}, rows[at:]...)...)
			}
		}
		rows = w.blockRows(rows, index, b, inputs, oldSpans, newSpans)
		oldAt, newAt = b.oldStart+b.oldCount, b.newStart+b.newCount
	}
	equal(oldAt, newAt, len(w.old)-oldAt, false, true)
	return rows
}

// ContextRows gives the rows of count unchanged lines of a text, from line
// from of the new text and line oldFrom of the old text, both from 1. The
// view asks for them when the person opens a fold.
func ContextRows(text []byte, lang Language, from, oldFrom, count int) []Row {
	lines, _ := SplitLines(text)
	var spans [][]Span
	if lang != nil {
		spans = lang.Highlight(string(text), lines)
	}
	var rows []Row
	for k := 0; k < count; k++ {
		line := from - 1 + k
		if line < 0 || line >= len(lines) {
			break
		}
		row := Row{Kind: RowContext, Old: oldFrom + k, New: line + 1}
		row.Segs = segments(lines[line], spanAt(spans, line), nil, &row)
		rows = append(rows, row)
	}
	return rows
}

// blockRows adds the rows of one change: its removed lines, then its added
// lines, with a row at each end of a moved block.
func (w *fileWork) blockRows(rows []Row, index int, b block, inputs []Input, oldSpans, newSpans [][]Span) []Row {
	number := index + 1
	oldMarks := map[int]Pair{}
	newMarks := map[int]Pair{}
	for _, pair := range b.pairs {
		oldMarks[pair.Old] = pair
		newMarks[pair.New] = pair
	}
	removedAt := map[int]int{}
	addedAt := map[int]int{}
	lastMove := 0
	for k := 0; k < b.oldCount; k++ {
		line := b.oldStart + k
		flags := b.flags
		if id := w.movedOld[line]; id != 0 {
			flags |= FlagMoved
			if id != lastMove {
				ends := w.moves[id]
				target := inputs[ends.newFile].Path
				rows = append(rows, Row{Kind: RowMove, Old: line + 1, Block: number, Text: fmt.Sprintf("Moved to %s:%d", target, ends.newLine+1), Link: &Link{File: target, Line: ends.newLine + 1}})
			}
		}
		lastMove = w.movedOld[line]
		pair, paired := oldMarks[k]
		if paired && pair.Whitespace {
			flags |= FlagWhitespace
		}
		row := Row{Kind: RowRemoved, Old: line + 1, Flags: flags, Block: number}
		row.Segs = segments(w.old[line], spanAt(oldSpans, line), pair.OldMarks, &row)
		removedAt[k] = len(rows)
		rows = append(rows, row)
	}
	lastMove = 0
	for k := 0; k < b.newCount; k++ {
		line := b.newStart + k
		flags := b.flags | w.input.Overlays[line]
		if id := w.movedNew[line]; id != 0 {
			flags |= FlagMoved
			if id != lastMove {
				ends := w.moves[id]
				source := inputs[ends.oldFile].OldPath
				if source == "" {
					source = inputs[ends.oldFile].Path
				}
				rows = append(rows, Row{Kind: RowMove, New: line + 1, Block: number, Text: fmt.Sprintf("Moved from %s:%d", source, ends.oldLine+1), Link: &Link{File: inputs[ends.oldFile].Path, Line: ends.oldLine + 1}})
			}
		}
		lastMove = w.movedNew[line]
		pair, paired := newMarks[k]
		if paired && pair.Whitespace {
			flags |= FlagWhitespace
		}
		row := Row{Kind: RowAdded, New: line + 1, Flags: flags, Block: number}
		row.Segs = segments(w.new[line], spanAt(newSpans, line), pair.NewMarks, &row)
		addedAt[k] = len(rows)
		rows = append(rows, row)
	}
	for _, pair := range b.pairs {
		removed, added := removedAt[pair.Old], addedAt[pair.New]
		rows[removed].Pair = added + 1
		rows[added].Pair = removed + 1
	}
	return rows
}

// scopeOf names the innermost entity around the start of a change, in the
// new text, else in the old text.
func scopeOf(b block, newEntities, oldEntities []Entity) string {
	if entity, ok := innermost(newEntities, b.newStart); ok {
		return entity.Kind + " " + entity.Name
	}
	if entity, ok := innermost(oldEntities, b.oldStart); ok {
		return entity.Kind + " " + entity.Name
	}
	return ""
}

func innermost(entities []Entity, line int) (Entity, bool) {
	var best Entity
	found := false
	for _, entity := range entities {
		if entity.Start <= line && line <= entity.End && (!found || entity.End-entity.Start < best.End-best.Start) {
			best, found = entity, true
		}
	}
	return best, found
}

func spanAt(spans [][]Span, line int) []Span {
	if line < len(spans) {
		return spans[line]
	}
	return nil
}

// segments cuts a line into pieces of one style each: the syntax style of
// the piece, plus Emphasis inside a changed range. Tabs become four spaces.
// A line longer than maxLine shows its start and sets FlagCut on the row.
func segments(line string, spans []Span, marked []Range, row *Row) []Seg {
	if line == "" {
		return nil
	}
	if len(line) > maxLine {
		cut := maxLine
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		line = line[:cut]
		row.Flags |= FlagCut
	}
	points := []int{0, len(line)}
	for _, span := range spans {
		points = append(points, clampTo(span.Start, len(line)), clampTo(span.End, len(line)))
	}
	for _, r := range marked {
		points = append(points, clampTo(r.Start, len(line)), clampTo(r.End, len(line)))
	}
	sort.Ints(points)
	var out []Seg
	for i := 0; i+1 < len(points); i++ {
		start, end := points[i], points[i+1]
		if start == end {
			continue
		}
		style := 0
		for _, span := range spans {
			if span.Start <= start && end <= span.End {
				style = span.Style
				break
			}
		}
		for _, r := range marked {
			if r.Start <= start && end <= r.End {
				style |= Emphasis
				break
			}
		}
		text := strings.ReplaceAll(line[start:end], "\t", "    ")
		if last := len(out) - 1; last >= 0 && out[last].Style == style {
			out[last].Text += text
			continue
		}
		out = append(out, Seg{Text: text, Style: style})
	}
	return out
}

func clampTo(value, limit int) int {
	return max(0, min(value, limit))
}

// entityChanges lists the entities that the change adds, removes, renames,
// moves or edits, in the order of the new text.
func (w *fileWork) entityChanges(oldEntities, newEntities []Entity) []EntityChange {
	if len(oldEntities) == 0 && len(newEntities) == 0 {
		return nil
	}
	oldByKey := map[string]Entity{}
	for _, entity := range oldEntities {
		if _, ok := oldByKey[entity.Key]; !ok {
			oldByKey[entity.Key] = entity
		}
	}
	newByKey := map[string]Entity{}
	for _, entity := range newEntities {
		if _, ok := newByKey[entity.Key]; !ok {
			newByKey[entity.Key] = entity
		}
	}
	// touched gives the flags of the changes in a range of lines: ok when a
	// change touches it, noise when every such change is noise.
	touched := func(blocks []int, start, end int) (bool, bool) {
		hit, noise := false, true
		for line := max(0, start); line <= end && line < len(blocks); line++ {
			if number := blocks[line]; number != 0 {
				hit = true
				noise = noise && w.blocks[number-1].flags != 0
			}
		}
		return hit, noise
	}
	var out []EntityChange
	var added, removed []Entity
	for _, entity := range newEntities {
		old, ok := oldByKey[entity.Key]
		if !ok {
			added = append(added, entity)
			continue
		}
		newHit, newNoise := touched(w.newBlock, entity.Start, entity.End)
		oldHit, oldNoise := touched(w.oldBlock, old.Start, old.End)
		if !newHit && !oldHit {
			continue
		}
		change := "modified"
		switch {
		case entity.Header < len(w.movedNew) && w.movedNew[entity.Header] != 0 && old.Body == entity.Body:
			change = "moved"
		case newNoise && oldNoise:
			change = "format"
		}
		out = append(out, EntityChange{Kind: entity.Kind, Name: entity.Name, Change: change, Line: entity.Header + 1})
	}
	for _, entity := range oldEntities {
		if _, ok := newByKey[entity.Key]; !ok {
			removed = append(removed, entity)
		}
	}
	used := map[int]bool{}
	for _, entity := range added {
		change := EntityChange{Kind: entity.Kind, Name: entity.Name, Change: "added", Line: entity.Header + 1}
		for i, old := range removed {
			if !used[i] && old.Kind == entity.Kind && old.Body != "" && old.Body == entity.Body {
				used[i] = true
				change.Change, change.OldName = "renamed", old.Name
				break
			}
		}
		out = append(out, change)
	}
	for i, entity := range removed {
		if !used[i] {
			out = append(out, EntityChange{Kind: entity.Kind, Name: entity.Name, Change: "removed", Line: entity.Header + 1, Old: true})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}
