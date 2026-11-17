package diff

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// apply rebuilds both texts from the edits and fails when the edits do not
// cover each line once, in order, with equal lines that match.
func apply(t *testing.T, a, b []string, edits []Edit) {
	t.Helper()
	i, j := 0, 0
	for _, e := range edits {
		if e.A != i || e.B != j {
			t.Fatalf("edit %+v starts at (%d,%d), want (%d,%d)", e, e.A, e.B, i, j)
		}
		switch e.Op {
		case Equal:
			for k := 0; k < e.N; k++ {
				if a[i+k] != b[j+k] {
					t.Fatalf("equal edit pairs %q with %q", a[i+k], b[j+k])
				}
			}
			i += e.N
			j += e.N
		case Delete:
			i += e.N
		case Insert:
			j += e.N
		}
	}
	if i != len(a) || j != len(b) {
		t.Fatalf("edits end at (%d,%d), want (%d,%d)", i, j, len(a), len(b))
	}
}

func randomLines(r *rand.Rand, n, alphabet int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprint(r.Intn(alphabet))
	}
	return out
}

func lcs(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	return dp[0][0]
}

func TestLinesGivesValidEditsForRandomTexts(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for round := 0; round < 3000; round++ {
		a := randomLines(r, r.Intn(40), 2+r.Intn(8))
		b := randomLines(r, r.Intn(40), 2+r.Intn(8))
		apply(t, a, b, Lines(a, b))
	}
}

func TestMyersFindsAShortestEditScript(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for round := 0; round < 2000; round++ {
		a := randomLines(r, r.Intn(30), 2+r.Intn(5))
		b := randomLines(r, r.Intn(30), 2+r.Intn(5))
		d := &differ{budget: myersBudget}
		ids := map[string]int{}
		for _, list := range [][]string{a, b} {
			for _, line := range list {
				if _, ok := ids[line]; !ok {
					ids[line] = len(ids)
				}
			}
		}
		for _, line := range a {
			d.a = append(d.a, ids[line])
		}
		for _, line := range b {
			d.b = append(d.b, ids[line])
		}
		d.ra, d.rb = make([]bool, len(a)), make([]bool, len(b))
		d.myers(0, len(a), 0, len(b))
		kept := 0
		for _, changed := range d.ra {
			if !changed {
				kept++
			}
		}
		if want := lcs(a, b); kept != want {
			t.Fatalf("round %d: Myers keeps %d lines, the longest common subsequence has %d\na=%v\nb=%v", round, kept, want, a, b)
		}
		apply(t, a, b, edits(d.ra, d.rb))
	}
}

func TestAddedFunctionStartsAtItsHeader(t *testing.T) {
	a := []string{"func a() {", "}", "", "func c() {", "}"}
	b := []string{"func a() {", "}", "", "func b() {", "}", "", "func c() {", "}"}
	got := Lines(a, b)
	for _, e := range got {
		if e.Op == Insert {
			if b[e.B] != "func b() {" || e.N != 3 {
				t.Fatalf("the insertion starts with %q and has %d lines, want the header and 3 lines", b[e.B], e.N)
			}
			return
		}
	}
	t.Fatalf("no insertion in %+v", got)
}

func TestHistogramKeepsUniqueLinesAligned(t *testing.T) {
	// A rewritten function between two unchanged ones must not pair the
	// braces and blank lines of the two versions instead of the headers.
	a := strings.Split("func one() {\n\treturn 1\n}\n\nfunc two() {\n\tx := 2\n\treturn x\n}\n\nfunc three() {\n\treturn 3\n}", "\n")
	b := strings.Split("func one() {\n\treturn 1\n}\n\nfunc two() {\n\ty := compute()\n\tif y > 0 {\n\t\treturn y\n\t}\n\treturn 0\n}\n\nfunc three() {\n\treturn 3\n}", "\n")
	edits := Lines(a, b)
	apply(t, a, b, edits)
	for _, e := range edits {
		if e.Op == Delete {
			for k := 0; k < e.N; k++ {
				if strings.HasPrefix(a[e.A+k], "func") {
					t.Fatalf("the header %q counts as removed", a[e.A+k])
				}
			}
		}
	}
}

func TestPairedLinesMarkOnlyTheChangedWords(t *testing.T) {
	pairs := PairLines([]string{"\treturn total + fee"}, []string{"\treturn total + tax"})
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs, want 1", len(pairs))
	}
	old := "\treturn total + fee"
	if len(pairs[0].OldMarks) != 1 || old[pairs[0].OldMarks[0].Start:pairs[0].OldMarks[0].End] != "fee" {
		t.Fatalf("old marks %+v, want only fee", pairs[0].OldMarks)
	}
}

func TestUnrelatedLinesDoNotPair(t *testing.T) {
	pairs := PairLines([]string{"if err != nil {"}, []string{"log.Printf(\"started %s\", name)"})
	if len(pairs) != 0 {
		t.Fatalf("pairs %+v, want none", pairs)
	}
}

func TestPairsKeepOrderAndFindTheMatchingLine(t *testing.T) {
	removed := []string{"name := user.Name", "age := user.Age"}
	added := []string{"// Read the user.", "name := account.Name", "age := account.Age"}
	pairs := PairLines(removed, added)
	if len(pairs) != 2 || pairs[0].New != 1 || pairs[1].New != 2 {
		t.Fatalf("pairs %+v, want removed 0→1 and 1→2", pairs)
	}
}

func TestWhitespaceOnlyPairHasNoMarks(t *testing.T) {
	pairs := PairLines([]string{"x  = 1"}, []string{"x = 1"})
	if len(pairs) != 1 || !pairs[0].Whitespace || len(pairs[0].NewMarks) != 0 {
		t.Fatalf("pairs %+v, want one whitespace pair without marks", pairs)
	}
}

// codeLanguage is a test language: tokens are the words and symbols of the
// text, and entities are lines that start with "func ".
type codeLanguage struct{ whitespace bool }

func (codeLanguage) Highlight(string, []string) [][]Span { return nil }
func (codeLanguage) Tokens(_ string, lines []string) []Token {
	var out []Token
	for i, line := range lines {
		for _, token := range Tokens(line) {
			if !isSpace(token) {
				out = append(out, Token{Text: token, First: i, Last: i})
			}
		}
	}
	return out
}
func (codeLanguage) Entities(_ string, lines []string) []Entity {
	var out []Entity
	for i, line := range lines {
		if name, ok := strings.CutPrefix(line, "func "); ok {
			name = strings.TrimSuffix(strings.Fields(name)[0], "()")
			end := i
			for end+1 < len(lines) && lines[end] != "}" {
				end++
			}
			body := strings.ReplaceAll(strings.Join(lines[i:end+1], "\n"), name, "_")
			out = append(out, Entity{Kind: "func", Name: name, Key: "func " + name, Header: i, Start: i, End: end, Body: body})
		}
	}
	return out
}
func (l codeLanguage) WhitespaceMatters() bool { return l.whitespace }

func compareOne(old, new string, lang Language) Result {
	return Compare([]Input{{Path: "f", Old: []byte(old), New: []byte(new), Language: lang}}, DefaultOptions)[0]
}

func flagsOf(rows []Row) int {
	flags := 0
	for _, row := range rows {
		if row.Kind == RowRemoved || row.Kind == RowAdded {
			flags |= row.Flags
		}
	}
	return flags
}

func TestReindentedBlockIsWhitespaceNoise(t *testing.T) {
	result := compareOne("a\n  b(1)\nc\n", "a\n    b(1)\nc\n", codeLanguage{})
	if result.Noise != 1 || flagsOf(result.Rows)&FlagWhitespace == 0 {
		t.Fatalf("noise %d flags %b, want one whitespace change", result.Noise, flagsOf(result.Rows))
	}
}

func TestLineBreakChangeIsFormatNoise(t *testing.T) {
	result := compareOne("x\ncall(a, b)\ny\n", "x\ncall(\n\ta,\n\tb)\ny\n", codeLanguage{})
	if result.Noise != 1 || flagsOf(result.Rows)&FlagFormat == 0 {
		t.Fatalf("noise %d flags %b, want one format change", result.Noise, flagsOf(result.Rows))
	}
}

func TestIndentChangeIsRealWhereWhitespaceMatters(t *testing.T) {
	result := compareOne("if a:\n    b()\nc()\n", "if a:\n    b()\n    c()\n", codeLanguage{whitespace: true})
	if result.Noise != 0 {
		t.Fatalf("noise %d, want a real change", result.Noise)
	}
}

func TestAddedLineWithContentIsNeverNoise(t *testing.T) {
	result := compareOne("a\nb\n", "a\n  \nb\nc\n", codeLanguage{})
	if result.Noise != 1 || result.Changes != 2 {
		t.Fatalf("noise %d of %d changes, want only the blank line as noise", result.Noise, result.Changes)
	}
}

func TestBlockMovedToAnotherFile(t *testing.T) {
	body := "func helper() {\n\tvalue := compute()\n\treturn value * 2\n}\n"
	results := Compare([]Input{
		{Path: "a.go", Old: []byte("package a\n\n" + body + "\nfunc keep() {}\n"), New: []byte("package a\n\nfunc keep() {}\n"), Language: codeLanguage{}},
		{Path: "b.go", Old: []byte("package a\n"), New: []byte("package a\n\n" + strings.ReplaceAll(body, "\t", "\t\t")), Language: codeLanguage{}},
	}, DefaultOptions)
	var to, from *Row
	for i := range results[0].Rows {
		if row := &results[0].Rows[i]; row.Kind == RowMove {
			to = row
		}
	}
	for i := range results[1].Rows {
		if row := &results[1].Rows[i]; row.Kind == RowMove {
			from = row
		}
	}
	if to == nil || from == nil {
		t.Fatalf("move rows %v and %v, want both ends", to, from)
	}
	if to.Link.File != "b.go" || to.Link.Line != 3 || from.Link.File != "a.go" || from.Link.Line != 3 {
		t.Fatalf("links %+v and %+v, want b.go:3 and a.go:3", to.Link, from.Link)
	}
	if flagsOf(results[1].Rows)&FlagMoved == 0 {
		t.Fatal("the added rows do not carry the moved flag")
	}
}

func TestCommonErrorCheckIsNotAMove(t *testing.T) {
	check := "\tif err != nil {\n\t\treturn err\n\t}\n"
	results := Compare([]Input{
		{Path: "a.go", Old: []byte("package a\n\nfunc open() error {\n\terr := start()\n" + check + "\treturn nil\n}\n"), New: []byte("package a\n\nfunc open() error {\n\treturn nil\n}\n"), Language: codeLanguage{}},
		{Path: "b.go", Old: []byte("package a\n"), New: []byte("package a\n\nfunc read() error {\n\terr := load()\n" + check + "\treturn nil\n}\n"), Language: codeLanguage{}},
		{Path: "c.go", Old: []byte("package a\n"), New: []byte("package a\n\nfunc write() error {\n\terr := save()\n" + check + "\treturn nil\n}\n"), Language: codeLanguage{}},
	}, DefaultOptions)
	for i, result := range results {
		if result.Moved {
			t.Fatalf("file %d has a move: an error check that other files also add is common code", i)
		}
	}
}

func TestRewriteInPlaceIsNotAMove(t *testing.T) {
	result := compareOne("start\nfirst line here\nsecond line here\nthird line here\nend\n", "start\nif ok {\n  first line here\n  second line here\n  third line here\n}\nend\n", codeLanguage{})
	if result.Moved {
		t.Fatal("a block that changed depth in place counts as moved")
	}
}

func TestLongUnchangedRunsFold(t *testing.T) {
	var old, changed []string
	for i := 1; i <= 100; i++ {
		old = append(old, fmt.Sprintf("line %d", i))
		changed = append(changed, fmt.Sprintf("line %d", i))
	}
	changed[49] = "line fifty"
	result := compareOne(strings.Join(old, "\n")+"\n", strings.Join(changed, "\n")+"\n", nil)
	var folds []Fold
	shown := 0
	for _, row := range result.Rows {
		switch row.Kind {
		case RowFold:
			folds = append(folds, *row.Fold)
		case RowContext:
			shown++
		}
	}
	if len(folds) != 2 || folds[0].Count != 46 || folds[1].Count != 47 || shown != 6 {
		t.Fatalf("folds %+v and %d context rows, want 46 and 47 hidden lines around 6 context rows", folds, shown)
	}
	if folds[1].Old != 54 || folds[1].New != 54 {
		t.Fatalf("second fold starts at %d/%d, want line 54", folds[1].Old, folds[1].New)
	}
}

func TestShortUnchangedRunsShow(t *testing.T) {
	result := compareOne("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n", "A\nb\nc\nd\ne\nf\ng\nh\ni\nJ\n", nil)
	for _, row := range result.Rows {
		if row.Kind == RowFold {
			t.Fatalf("a fold hides %d lines; a run of 8 lines between changes must show", row.Fold.Count)
		}
	}
}

func TestEntityChangesNameAddedRemovedRenamedAndModified(t *testing.T) {
	old := "func keep() {\n\treturn 1\n}\nfunc gone() {\n\treturn 2\n}\nfunc before() {\n\treturn unique\n}\n"
	new := "func keep() {\n\treturn 10\n}\nfunc after() {\n\treturn unique\n}\nfunc fresh() {\n\treturn 3\n}\n"
	result := compareOne(old, new, codeLanguage{})
	got := map[string]string{}
	for _, change := range result.Entities {
		got[change.Name] = change.Change
		if change.Change == "renamed" && change.OldName != "before" {
			t.Fatalf("rename from %q, want before", change.OldName)
		}
	}
	want := map[string]string{"keep": "modified", "after": "renamed", "fresh": "added", "gone": "removed"}
	for name, change := range want {
		if got[name] != change {
			t.Fatalf("entity changes %v, want %v", got, want)
		}
	}
}

func TestRowsPairRemovedAndAddedLines(t *testing.T) {
	result := compareOne("a\nvalue := 1\nb\n", "a\nvalue := 2\nb\n", nil)
	var removed, added int
	for i, row := range result.Rows {
		switch row.Kind {
		case RowRemoved:
			removed = i
		case RowAdded:
			added = i
		}
	}
	if result.Rows[removed].Pair != added+1 || result.Rows[added].Pair != removed+1 {
		t.Fatalf("pairs %d and %d, want each row to point at the other", result.Rows[removed].Pair, result.Rows[added].Pair)
	}
	emphasized := ""
	for _, seg := range result.Rows[added].Segs {
		if seg.Style&Emphasis != 0 {
			emphasized += seg.Text
		}
	}
	if emphasized != "2" {
		t.Fatalf("emphasis on %q, want 2", emphasized)
	}
}

func TestSegmentsExpandTabsAndCutLongLines(t *testing.T) {
	row := Row{}
	segs := segments("\tx", nil, nil, &row)
	if len(segs) != 1 || segs[0].Text != "    x" {
		t.Fatalf("segments %+v, want the tab as four spaces", segs)
	}
	long := strings.Repeat("a", maxLine+10)
	segs = segments(long, nil, nil, &row)
	if row.Flags&FlagCut == 0 || len(segs[0].Text) != maxLine {
		t.Fatalf("a long line keeps %d characters and flags %b, want %d and the cut flag", len(segs[0].Text), row.Flags, maxLine)
	}
}
