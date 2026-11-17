package diff

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Range is a part [Start, End) of a line, in bytes.
type Range struct {
	Start int
	End   int
}

// Pair joins a removed line and an added line of one change block: Old and
// New are their indexes in the block. OldMarks and NewMarks are the changed
// parts of each line. Whitespace is true when the two lines differ only in
// whitespace.
type Pair struct {
	Old        int
	New        int
	OldMarks   []Range
	NewMarks   []Range
	Whitespace bool
}

// Limits of the refinement. Longer lines and larger blocks keep their line
// tint without token marks, so the work stays small.
const (
	maxRefineLine   = 1000
	maxRefineTokens = 500
	maxPairCells    = 40_000
	// pairScore is the least similarity of two lines that form a pair.
	pairScore = 0.5
	// markShare is the share of a line that may change before its marks go:
	// marks on almost all of a line only add noise to its tint.
	markShare = 0.8
)

// PairLines pairs the removed lines of a change block with its added lines.
// A pair keeps the order of both lists and has a similarity of at least
// pairScore. Among such pairings it takes the one with the largest total
// similarity.
func PairLines(removed, added []string) []Pair {
	n, m := len(removed), len(added)
	if n == 0 || m == 0 {
		return nil
	}
	oldWords := make([]map[string]int, n)
	newWords := make([]map[string]int, m)
	oldSizes := make([]int, n)
	newSizes := make([]int, m)
	for i, line := range removed {
		oldWords[i], oldSizes[i] = words(line)
	}
	for j, line := range added {
		newWords[j], newSizes[j] = words(line)
	}
	score := func(i, j int) float64 {
		if strings.TrimSpace(removed[i]) == strings.TrimSpace(added[j]) {
			return 1
		}
		return dice(oldWords[i], oldSizes[i], newWords[j], newSizes[j])
	}
	var pairs []Pair
	if n*m > maxPairCells {
		for i := 0; i < min(n, m); i++ {
			if score(i, i) >= pairScore {
				pairs = append(pairs, refinePair(removed[i], added[i], i, i))
			}
		}
		return pairs
	}
	// best[i][j] is the largest total similarity of a pairing of the first
	// i removed lines with the first j added lines.
	best := make([][]float64, n+1)
	for i := range best {
		best[i] = make([]float64, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			value := max(best[i-1][j], best[i][j-1])
			if s := score(i-1, j-1); s >= pairScore && best[i-1][j-1]+s > value {
				value = best[i-1][j-1] + s
			}
			best[i][j] = value
		}
	}
	for i, j := n, m; i > 0 && j > 0; {
		switch best[i][j] {
		case best[i-1][j]:
			i--
		case best[i][j-1]:
			j--
		default:
			pairs = append(pairs, refinePair(removed[i-1], added[j-1], i-1, j-1))
			i--
			j--
		}
	}
	for left, right := 0, len(pairs)-1; left < right; left, right = left+1, right-1 {
		pairs[left], pairs[right] = pairs[right], pairs[left]
	}
	return pairs
}

// words counts the word and symbol tokens of a line, without whitespace.
// Symbols count half, so that two lines with the same punctuation and other
// words do not look alike.
func words(line string) (map[string]int, int) {
	out := map[string]int{}
	size := 0
	for _, token := range Tokens(line) {
		if isSpace(token) {
			continue
		}
		weight := 2
		if r, _ := utf8.DecodeRuneInString(token); !isWord(r) {
			weight = 1
		}
		out[token] += weight
		size += weight
	}
	return out, size
}

func dice(a map[string]int, aSize int, b map[string]int, bSize int) float64 {
	if aSize+bSize == 0 {
		return 1
	}
	common := 0
	for token, count := range a {
		common += min(count, b[token])
	}
	return 2 * float64(common) / float64(aSize+bSize)
}

// refinePair marks the changed tokens of a pair of lines.
func refinePair(oldLine, newLine string, oldIndex, newIndex int) Pair {
	pair := Pair{Old: oldIndex, New: newIndex}
	if len(oldLine) > maxRefineLine || len(newLine) > maxRefineLine {
		return pair
	}
	oldTokens := Tokens(oldLine)
	newTokens := Tokens(newLine)
	if len(oldTokens) > maxRefineTokens || len(newTokens) > maxRefineTokens {
		return pair
	}
	oldChanged, newChanged := tokenDiff(oldTokens, newTokens)
	if onlySpace(oldTokens, oldChanged) && onlySpace(newTokens, newChanged) {
		pair.Whitespace = true
		return pair
	}
	joinShortGaps(oldTokens, oldChanged, newTokens, newChanged)
	pair.OldMarks = marks(oldTokens, oldChanged)
	pair.NewMarks = marks(newTokens, newChanged)
	if share(oldLine, pair.OldMarks) > markShare && share(newLine, pair.NewMarks) > markShare {
		pair.OldMarks, pair.NewMarks = nil, nil
	}
	return pair
}

// Tokens cuts a line into words, numbers, runs of whitespace and single
// symbols. Joining the tokens gives the line again.
func Tokens(line string) []string {
	var out []string
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRuneInString(line[i:])
		j := i + size
		switch {
		case r == ' ' || r == '\t':
			for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
				j++
			}
		case isWord(r):
			for j < len(line) {
				next, nextSize := utf8.DecodeRuneInString(line[j:])
				if !isWord(next) {
					break
				}
				j += nextSize
			}
		}
		out = append(out, line[i:j])
		i = j
	}
	return out
}

func isWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isSpace(token string) bool {
	return token != "" && (token[0] == ' ' || token[0] == '\t')
}

// tokenDiff marks the tokens that the Myers algorithm finds changed.
func tokenDiff(a, b []string) ([]bool, []bool) {
	ids := map[string]int{}
	intern := func(tokens []string) []int {
		out := make([]int, len(tokens))
		for i, token := range tokens {
			id, ok := ids[token]
			if !ok {
				id = len(ids)
				ids[token] = id
			}
			out[i] = id
		}
		return out
	}
	d := &differ{a: intern(a), b: intern(b), budget: 1 << 20}
	d.ra = make([]bool, len(a))
	d.rb = make([]bool, len(b))
	d.myers(0, len(a), 0, len(b))
	return d.ra, d.rb
}

func onlySpace(tokens []string, changed []bool) bool {
	for i, token := range tokens {
		if changed[i] && !isSpace(token) {
			return false
		}
	}
	return true
}

// joinShortGaps marks short unchanged runs between two changed runs as
// changed, on both lines, so that a change reads as one part and not as
// confetti. A run is short when it is whitespace or at most two characters
// that are not a word.
func joinShortGaps(oldTokens []string, oldChanged []bool, newTokens []string, newChanged []bool) {
	// The unchanged tokens of the two lines are equal and in the same order,
	// so the k-th unchanged run of one line is the k-th of the other.
	type run struct{ start, end int }
	gaps := func(tokens []string, changed []bool) []run {
		var out []run
		for i := 0; i < len(tokens); {
			if changed[i] {
				i++
				continue
			}
			j := i
			for j < len(tokens) && !changed[j] {
				j++
			}
			out = append(out, run{i, j})
			i = j
		}
		return out
	}
	oldGaps := gaps(oldTokens, oldChanged)
	newGaps := gaps(newTokens, newChanged)
	if len(oldGaps) != len(newGaps) {
		return
	}
	for k, gap := range oldGaps {
		other := newGaps[k]
		inside := func(r run, changed []bool) bool {
			return r.start > 0 && r.end < len(changed) && changed[r.start-1] && changed[r.end]
		}
		if !inside(gap, oldChanged) && !inside(other, newChanged) {
			continue
		}
		text := strings.Join(oldTokens[gap.start:gap.end], "")
		if !shortGap(text) {
			continue
		}
		for i := gap.start; i < gap.end; i++ {
			oldChanged[i] = true
		}
		for i := other.start; i < other.end; i++ {
			newChanged[i] = true
		}
	}
}

func shortGap(text string) bool {
	if strings.TrimSpace(text) == "" {
		return true
	}
	if utf8.RuneCountInString(text) > 2 {
		return false
	}
	for _, r := range text {
		if isWord(r) {
			return false
		}
	}
	return true
}

// marks turns changed tokens into byte ranges of the line, without leading
// or trailing whitespace of a range.
func marks(tokens []string, changed []bool) []Range {
	var out []Range
	at := 0
	for i, token := range tokens {
		start, end := at, at+len(token)
		at = end
		if !changed[i] {
			continue
		}
		if isSpace(token) {
			continue
		}
		if last := len(out) - 1; last >= 0 && out[last].End == start {
			out[last].End = end
			continue
		}
		// A whitespace token between two marks joins them.
		if last := len(out) - 1; last >= 0 && i > 0 && changed[i-1] && isSpace(tokens[i-1]) && out[last].End == start-len(tokens[i-1]) {
			out[last].End = end
			continue
		}
		out = append(out, Range{start, end})
	}
	return out
}

func share(line string, ranges []Range) float64 {
	size := len(strings.TrimSpace(line))
	if size == 0 {
		return 0
	}
	marked := 0
	for _, r := range ranges {
		marked += r.End - r.Start
	}
	return float64(marked) / float64(size)
}
