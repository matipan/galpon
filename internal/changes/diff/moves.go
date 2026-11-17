package diff

import (
	"strings"
	"unicode"
)

// Limits of the move search. A moved block has at least minMoveLines lines
// and minMoveChars letters and digits, as git --color-moved asks, so that
// short common lines such as "}" or "return nil" do not count as moves.
const (
	minMoveLines = 3
	minMoveChars = 20
)

// findMoves finds blocks that a change set removed in one place and added in
// another place, in the same file or in another file. Lines compare without
// leading and trailing whitespace, so a block that moved to another depth
// still counts. A removed block and an added block of the same change are a
// rewrite in place, not a move. A block that two places received in the
// same length is common code, such as an error check, and not a move.
func findMoves(files []*fileWork) {
	type ref struct{ file, line int }
	index := map[string][]ref{}
	for f, w := range files {
		for j, line := range w.new {
			if w.newBlock[j] == 0 {
				continue
			}
			key := strings.TrimSpace(line)
			if moveAnchor(key) {
				index[key] = append(index[key], ref{f, j})
			}
		}
	}
	next := 0
	for f, w := range files {
		for i := 0; i < len(w.old); {
			blockNumber := w.oldBlock[i]
			key := strings.TrimSpace(w.old[i])
			if blockNumber == 0 || w.movedOld[i] != 0 || !moveAnchor(key) {
				i++
				continue
			}
			bestFile, bestLine, bestCount, ties := -1, -1, 0, 0
			for _, candidate := range index[key] {
				target := files[candidate.file]
				if target.movedNew[candidate.line] != 0 {
					continue
				}
				if candidate.file == f && target.newBlock[candidate.line] == blockNumber {
					continue
				}
				count := 0
				for i+count < len(w.old) && candidate.line+count < len(target.new) {
					oldAt, newAt := i+count, candidate.line+count
					if w.oldBlock[oldAt] != blockNumber || target.newBlock[newAt] != target.newBlock[candidate.line] {
						break
					}
					if w.movedOld[oldAt] != 0 || target.movedNew[newAt] != 0 {
						break
					}
					if strings.TrimSpace(w.old[oldAt]) != strings.TrimSpace(target.new[newAt]) {
						break
					}
					count++
				}
				if count > bestCount {
					bestFile, bestLine, bestCount, ties = candidate.file, candidate.line, count, 0
				} else if count == bestCount {
					ties++
				}
			}
			if bestCount < minMoveLines || ties > 0 || moveChars(w.old[i:i+bestCount]) < minMoveChars {
				i++
				continue
			}
			next++
			target := files[bestFile]
			for k := 0; k < bestCount; k++ {
				w.movedOld[i+k] = next
				target.movedNew[bestLine+k] = next
			}
			ends := moveEnds{oldFile: f, oldLine: i, newFile: bestFile, newLine: bestLine, count: bestCount}
			if w.moves == nil {
				w.moves = map[int]moveEnds{}
			}
			if target.moves == nil {
				target.moves = map[int]moveEnds{}
			}
			w.moves[next] = ends
			target.moves[next] = ends
			i += bestCount
		}
	}
}

// moveAnchor reports whether a trimmed line can start a moved block: it has
// at least two characters that are not punctuation.
func moveAnchor(key string) bool {
	letters := 0
	for _, r := range key {
		if isWord(r) {
			letters++
			if letters >= 2 {
				return true
			}
		}
	}
	return false
}

// moveChars counts the letters and digits of lines.
func moveChars(lines []string) int {
	n := 0
	for _, line := range lines {
		for _, r := range line {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				n++
			}
		}
	}
	return n
}
