// Package diff compares two texts for the Changes view. It finds the changed
// lines, pairs removed and added lines that belong together, marks the
// changed tokens inside a pair, finds moved blocks, labels noise, and makes
// the rows that the view draws. It knows nothing of Git or of Galpon: callers
// give it bytes and functions for the language.
package diff

// Op is the kind of an edit.
type Op uint8

const (
	// Equal lines are in both texts.
	Equal Op = iota
	// Delete lines are only in the old text.
	Delete
	// Insert lines are only in the new text.
	Insert
)

// Edit is a run of lines of one kind. A is the first line in the old text and
// B the first line in the new text, both from 0. For a Delete, B is the place
// in the new text where the lines were; for an Insert, A is that place in the
// old text.
type Edit struct {
	Op Op
	A  int
	B  int
	N  int
}

// maxChain is the most times that a line can occur in the old part of a
// region and still be an anchor of the histogram diff. Lines such as "}" or
// "" occur often and make bad anchors.
const maxChain = 64

// myersBudget is the most edit steps that one Myers comparison may take.
// After it, the region counts as changed, so a pathological pair of files
// cannot hang the view.
const myersBudget = 4_000_000

// Lines compares two lists of lines and returns the edits that turn a into b.
// It uses a histogram diff, which anchors on lines that occur rarely, with
// the Myers algorithm for regions without such anchors. Then it slides each
// pure insertion or deletion to the place that reads best.
func Lines(a, b []string) []Edit {
	ids := map[string]int{}
	intern := func(lines []string) []int {
		out := make([]int, len(lines))
		for i, line := range lines {
			id, ok := ids[line]
			if !ok {
				id = len(ids)
				ids[line] = id
			}
			out[i] = id
		}
		return out
	}
	d := &differ{a: intern(a), b: intern(b), budget: myersBudget}
	d.ra = make([]bool, len(a))
	d.rb = make([]bool, len(b))
	d.histogram(0, len(a), 0, len(b), 0)
	slide(d.a, d.ra, a)
	slide(d.b, d.rb, b)
	return edits(d.ra, d.rb)
}

type differ struct {
	a, b   []int
	ra, rb []bool
	budget int
}

// histogram compares a[aLo:aHi] with b[bLo:bHi]. It finds the longest common
// run around the line with the fewest occurrences in a, keeps it, and
// compares the parts before and after it.
func (d *differ) histogram(aLo, aHi, bLo, bHi, depth int) {
	for aLo < aHi && bLo < bHi && d.a[aLo] == d.b[bLo] {
		aLo++
		bLo++
	}
	for aLo < aHi && bLo < bHi && d.a[aHi-1] == d.b[bHi-1] {
		aHi--
		bHi--
	}
	if aLo == aHi || bLo == bHi {
		d.markAll(aLo, aHi, bLo, bHi)
		return
	}
	if depth > 512 {
		d.myers(aLo, aHi, bLo, bHi)
		return
	}
	positions := map[int][]int{}
	counts := map[int]int{}
	for i := aLo; i < aHi; i++ {
		v := d.a[i]
		counts[v]++
		if counts[v] <= maxChain {
			positions[v] = append(positions[v], i)
		}
	}
	// The best anchor is the common run whose rarest line is the rarest, and
	// among those the longest.
	bestCount := maxChain + 1
	bestA, bestB, bestN := -1, -1, 0
	for j := bLo; j < bHi; {
		v := d.b[j]
		count := counts[v]
		next := j + 1
		if count == 0 || count > maxChain {
			j = next
			continue
		}
		for _, i := range positions[v] {
			as, bs := i, j
			rare := count
			for as > aLo && bs > bLo && d.a[as-1] == d.b[bs-1] {
				as--
				bs--
				rare = min(rare, counts[d.a[as]])
			}
			ae, be := i+1, j+1
			for ae < aHi && be < bHi && d.a[ae] == d.b[be] {
				rare = min(rare, counts[d.a[ae]])
				ae++
				be++
			}
			n := ae - as
			if rare < bestCount || (rare == bestCount && n > bestN) {
				bestCount, bestA, bestB, bestN = rare, as, bs, n
			}
			if be > next {
				next = be
			}
		}
		j = next
	}
	if bestA < 0 {
		d.myers(aLo, aHi, bLo, bHi)
		return
	}
	d.histogram(aLo, bestA, bLo, bestB, depth+1)
	d.histogram(bestA+bestN, aHi, bestB+bestN, bHi, depth+1)
}

func (d *differ) markAll(aLo, aHi, bLo, bHi int) {
	for i := aLo; i < aHi; i++ {
		d.ra[i] = true
	}
	for j := bLo; j < bHi; j++ {
		d.rb[j] = true
	}
}

// myers compares a region with the linear space form of the Myers O(ND)
// algorithm: it finds the middle snake of a shortest edit path and compares
// the two halves.
func (d *differ) myers(aLo, aHi, bLo, bHi int) {
	for aLo < aHi && bLo < bHi && d.a[aLo] == d.b[bLo] {
		aLo++
		bLo++
	}
	for aLo < aHi && bLo < bHi && d.a[aHi-1] == d.b[bHi-1] {
		aHi--
		bHi--
	}
	if aLo == aHi || bLo == bHi {
		d.markAll(aLo, aHi, bLo, bHi)
		return
	}
	x, y, ok := d.middle(aLo, aHi, bLo, bHi)
	if !ok || (x == aLo && y == bLo) || (x == aHi && y == bHi) {
		d.markAll(aLo, aHi, bLo, bHi)
		return
	}
	d.myers(aLo, x, bLo, y)
	d.myers(x, aHi, y, bHi)
}

// middle finds a point on the middle snake of a shortest edit path of the
// region. Forward paths run from the top left, backward paths from the bottom
// right. vf holds the furthest x of each forward diagonal k = x - y, and vb
// the furthest reversed x of each backward diagonal in reversed coordinates.
// -1 marks a diagonal that no path in the region reaches. It returns false
// when the budget runs out.
func (d *differ) middle(aLo, aHi, bLo, bHi int) (int, int, bool) {
	n, m := aHi-aLo, bHi-bLo
	delta := n - m
	odd := delta&1 != 0
	limit := (n + m + 1) / 2
	offset := limit + 1
	vf := make([]int, 2*limit+3)
	vb := make([]int, 2*limit+3)
	for k := range vf {
		vf[k], vb[k] = -1, -1
	}
	vf[offset+1], vb[offset+1] = 0, 0
	// reach gives the x on diagonal k after one more edit, from the better of
	// the two neighbor diagonals, or false when no path reaches it.
	reach := func(v []int, k, step int) (int, bool) {
		if k == -step || (k != step && v[offset+k-1] < v[offset+k+1]) {
			x := v[offset+k+1]
			return x, x >= 0
		}
		x := v[offset+k-1]
		return x + 1, x >= 0
	}
	for step := 0; step <= limit; step++ {
		d.budget -= 2*step + 1
		if d.budget < 0 {
			return 0, 0, false
		}
		for k := -step; k <= step; k += 2 {
			x, ok := reach(vf, k, step)
			y := x - k
			if !ok || x > n || y > m {
				vf[offset+k] = -1
				continue
			}
			sx, sy := x, y
			for x < n && y < m && d.a[aLo+x] == d.b[bLo+y] {
				x++
				y++
			}
			vf[offset+k] = x
			back := delta - k
			if odd && back >= -(step-1) && back <= step-1 && vb[offset+back] >= 0 && x+vb[offset+back] >= n {
				return aLo + sx, bLo + sy, true
			}
		}
		for k := -step; k <= step; k += 2 {
			x, ok := reach(vb, k, step)
			y := x - k
			if !ok || x > n || y > m {
				vb[offset+k] = -1
				continue
			}
			sx, sy := x, y
			for x < n && y < m && d.a[aHi-1-x] == d.b[bHi-1-y] {
				x++
				y++
			}
			vb[offset+k] = x
			forward := delta - k
			if !odd && forward >= -step && forward <= step && vf[offset+forward] >= 0 && x+vf[offset+forward] >= n {
				return aHi - sx, bHi - sy, true
			}
		}
	}
	return 0, 0, false
}

// edits turns the change marks of both texts into a list of edits. A change
// lists its deletions before its insertions.
func edits(ra, rb []bool) []Edit {
	var out []Edit
	add := func(op Op, a, b int) {
		if last := len(out) - 1; last >= 0 && out[last].Op == op {
			out[last].N++
			return
		}
		out = append(out, Edit{Op: op, A: a, B: b, N: 1})
	}
	i, j := 0, 0
	for i < len(ra) || j < len(rb) {
		switch {
		case i < len(ra) && ra[i]:
			add(Delete, i, j)
			i++
		case j < len(rb) && rb[j]:
			add(Insert, i, j)
			j++
		default:
			add(Equal, i, j)
			i++
			j++
		}
	}
	return out
}

// slide moves each run of changed lines of one text to the place that reads
// best. A run can move up when the line before it equals its last line, and
// down when the line after it equals its first line; the text is the same
// either way. The run moves only through lines that are unchanged in this
// text, so it never joins a change of the other text.
func slide(ids []int, changed []bool, lines []string) {
	for start := 0; start < len(changed); {
		if !changed[start] {
			start++
			continue
		}
		end := start
		for end < len(changed) && changed[end] {
			end++
		}
		lo, hi := start, end
		for lo > 0 && !changed[lo-1] && ids[lo-1] == ids[hi-1] && (lo < 2 || !changed[lo-2]) {
			lo--
			hi--
		}
		top := lo
		lo, hi = start, end
		for hi < len(changed) && !changed[hi] && ids[hi] == ids[lo] && (hi+1 >= len(changed) || !changed[hi+1]) {
			lo++
			hi++
		}
		bottom := lo
		size := end - start
		best, bestScore := start, 1<<30
		for at := top; at <= bottom; at++ {
			if score := slideScore(lines, at, at+size); score <= bestScore {
				best, bestScore = at, score
			}
		}
		for i := start; i < end; i++ {
			changed[i] = false
		}
		for i := best; i < best+size; i++ {
			changed[i] = true
		}
		start = best + size
	}
}

// slideScore rates a place [start, end) for a run of changed lines: lower is
// better. A run reads best when a blank line comes before it, when it does not
// start with a closing bracket, and when it starts at its smallest indent.
func slideScore(lines []string, start, end int) int {
	score := 0
	if start > 0 && !blank(lines[start-1]) {
		score += 4
	}
	first := lines[start]
	if closer(first) {
		score += 3
	}
	if end < len(lines) && !blank(lines[end]) && blank(lines[end-1]) {
		score++
	}
	least := indent(first)
	for _, line := range lines[start:end] {
		if !blank(line) && indent(line) < least {
			score += 2
			break
		}
	}
	return score
}

func blank(line string) bool {
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' && line[i] != '\r' {
			return false
		}
	}
	return true
}

func closer(line string) bool {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ', '\t':
			continue
		case '}', ')', ']':
			return true
		default:
			return false
		}
	}
	return false
}

func indent(line string) int {
	n := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}
