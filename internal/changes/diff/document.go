package diff

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Span gives a style to a part [Start, End) of a line, in bytes. Style is a
// syntax style number of the language layer.
type Span struct {
	Start int
	End   int
	Style int
}

// Entity is a named part of a text, such as a function or a type. Lines are
// from 0. Header is the line that names it. Key identifies it in the other
// version of the text, and Body is its text with its own name masked, to
// find a rename.
type Entity struct {
	Kind   string
	Name   string
	Key    string
	Header int
	Start  int
	End    int
	Body   string
}

// Token is a token of a text that carries meaning, with the first and the
// last line that it covers, from 0.
type Token struct {
	Text  string
	First int
	Last  int
}

// Language is what the diff needs to know about the language of a file.
type Language interface {
	// Highlight gives the style spans of each line of a text.
	Highlight(text string, lines []string) [][]Span
	// Tokens gives the tokens of a whole text that carry meaning, without
	// whitespace. A string or a comment is one token, so whitespace inside
	// it is a real change. Two texts with the same tokens differ only in
	// format.
	Tokens(text string, lines []string) []Token
	// Entities gives the named parts of a text.
	Entities(text string, lines []string) []Entity
	// WhitespaceMatters is true when indentation changes the meaning, as in
	// Python or YAML.
	WhitespaceMatters() bool
}

// Analyzer is a Language that reads a text once for its spans, its tokens
// and its entities. Tokens are left out when tokens is false.
type Analyzer interface {
	Analyze(text string, lines []string, tokens bool) Analysis
}

// Analysis is what a Language reads from one text.
type Analysis struct {
	Spans    [][]Span
	Tokens   []Token
	Entities []Entity
}

// analyze reads a text with a language, once when the language can.
func analyze(lang Language, text string, lines []string, tokens bool) Analysis {
	if analyzer, ok := lang.(Analyzer); ok {
		return analyzer.Analyze(text, lines, tokens)
	}
	a := Analysis{Spans: lang.Highlight(text, lines), Entities: lang.Entities(text, lines)}
	if tokens {
		a.Tokens = lang.Tokens(text, lines)
	}
	return a
}

// Row flags.
const (
	// FlagWhitespace marks rows of a change that is only whitespace.
	FlagWhitespace = 1 << iota
	// FlagFormat marks rows of a change that keeps every token: a format
	// change, such as a line break or a trailing comma.
	FlagFormat
	// FlagMoved marks rows of a block that moved.
	FlagMoved
	// FlagUncommitted marks added rows that uncommitted work changed.
	FlagUncommitted
	// FlagNew marks added rows that changed after the person looked.
	FlagNew
	// FlagCut marks a line that the view shows only in part.
	FlagCut
)

// Emphasis is added to the style of a changed token.
const Emphasis = 32

// maxLine is the most characters of a line that a row shows.
const maxLine = 4000

// Row kinds.
const (
	RowContext = "c"
	RowRemoved = "d"
	RowAdded   = "a"
	RowFold    = "f"
	RowHunk    = "h"
	RowMove    = "m"
)

// Row is one row of the diff of a file. Line numbers start at 1.
type Row struct {
	Kind  string `json:"k"`
	Old   int    `json:"o,omitempty"`
	New   int    `json:"n,omitempty"`
	Segs  []Seg  `json:"s,omitempty"`
	Pair  int    `json:"p,omitempty"`
	Flags int    `json:"f,omitempty"`
	Block int    `json:"b,omitempty"`
	Text  string `json:"t,omitempty"`
	Fold  *Fold  `json:"x,omitempty"`
	Link  *Link  `json:"l,omitempty"`
}

// Seg is a piece of a line with one style. It is written as [text, style].
type Seg struct {
	Text  string
	Style int
}

// MarshalJSON writes a segment as a two-item array.
func (s Seg) MarshalJSON() ([]byte, error) {
	text, err := json.Marshal(s.Text)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteByte('[')
	out.Write(text)
	out.WriteByte(',')
	out.WriteString(strconv.Itoa(s.Style))
	out.WriteByte(']')
	return out.Bytes(), nil
}

// UnmarshalJSON reads a segment written by MarshalJSON.
func (s *Seg) UnmarshalJSON(data []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 2 {
		return &json.UnmarshalTypeError{Value: string(data), Type: nil}
	}
	if err := json.Unmarshal(parts[0], &s.Text); err != nil {
		return err
	}
	return json.Unmarshal(parts[1], &s.Style)
}

// Fold is a run of unchanged lines that the view hides. Old and New are its
// first lines, from 1.
type Fold struct {
	Old      int      `json:"o"`
	New      int      `json:"n"`
	Count    int      `json:"c"`
	Entities []string `json:"e,omitempty"`
}

// Link points from a moved block to its other end.
type Link struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// EntityChange is a named part of a file that the change touches.
type EntityChange struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	OldName string `json:"oldName,omitempty"`
	Change  string `json:"change"`
	Line    int    `json:"line"`
	Old     bool   `json:"old,omitempty"`
}

// Input is one file of a change set.
type Input struct {
	Path     string
	OldPath  string
	Old      []byte
	New      []byte
	Language Language
	// Overlays mark added rows: each maps lines of the new text, from 0, to
	// a flag such as FlagUncommitted or FlagNew.
	Overlays map[int]int
}

// Result is the diff of one file.
type Result struct {
	Rows     []Row
	Added    int
	Removed  int
	Changes  int
	Noise    int
	Moved    bool
	Entities []EntityChange
	Notices  []string
}

// Options tune the rows.
type Options struct {
	// Context is the count of unchanged lines around a change.
	Context int
	// MinFold is the least count of lines that a fold hides. A shorter run
	// shows, because the fold row would take as much room.
	MinFold int
}

// DefaultOptions are the options of the Changes view.
var DefaultOptions = Options{Context: 3, MinFold: 4}

// maxMoveLines is the most removed lines of a change set that the move
// search reads.
const maxMoveLines = 20_000

// Compare makes the diff of each file of a change set. It finds moved blocks
// across all the files.
func Compare(inputs []Input, opts Options) []Result {
	// Files diff apart from each other, so they use every processor. Only
	// the moves between files need all of them at once.
	works := make([]*fileWork, len(inputs))
	eachFile(len(inputs), func(i int) { works[i] = newFileWork(inputs[i]) })
	removed := 0
	for _, work := range works {
		removed += work.removedCount
	}
	results := make([]Result, len(inputs))
	if removed <= maxMoveLines {
		findMoves(works)
	} else {
		for i := range results {
			results[i].Notices = append(results[i].Notices, "Moved code is not shown: the change removes more than 20,000 lines.")
		}
	}
	eachFile(len(works), func(i int) {
		result := works[i].result(inputs, opts)
		result.Notices = append(results[i].Notices, result.Notices...)
		results[i] = result
	})
	return results
}

// eachFile runs work for each index from 0 to n, on all processors.
func eachFile(n int, work func(int)) {
	workers := min(n, runtime.GOMAXPROCS(0))
	if workers <= 1 {
		for i := 0; i < n; i++ {
			work(i)
		}
		return
	}
	var next atomic.Int64
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := int(next.Add(1) - 1); i < n; i = int(next.Add(1) - 1) {
				work(i)
			}
		}()
	}
	group.Wait()
}

// block is a change: a run of removed lines and a run of added lines that
// edit one place. Either run can be empty.
type block struct {
	oldStart, oldCount int
	newStart, newCount int
	pairs              []Pair
	flags              int
}

type fileWork struct {
	input        Input
	old, new     []string
	oldFinal     bool
	newFinal     bool
	edits        []Edit
	blocks       []block
	oldBlock     []int
	newBlock     []int
	movedOld     []int
	movedNew     []int
	moves        map[int]moveEnds
	removedCount int
}

type moveEnds struct {
	oldFile, oldLine int
	newFile, newLine int
	count            int
}

func newFileWork(input Input) *fileWork {
	w := &fileWork{input: input}
	w.old, w.oldFinal = SplitLines(input.Old)
	w.new, w.newFinal = SplitLines(input.New)
	w.edits = Lines(w.old, w.new)
	w.oldBlock = make([]int, len(w.old))
	w.newBlock = make([]int, len(w.new))
	w.movedOld = make([]int, len(w.old))
	w.movedNew = make([]int, len(w.new))
	for i := 0; i < len(w.edits); i++ {
		e := w.edits[i]
		if e.Op == Equal {
			continue
		}
		b := block{oldStart: e.A, newStart: e.B}
		if e.Op == Delete {
			b.oldCount = e.N
			if i+1 < len(w.edits) && w.edits[i+1].Op == Insert {
				b.newCount = w.edits[i+1].N
				i++
			}
		} else {
			b.newCount = e.N
		}
		w.blocks = append(w.blocks, b)
		number := len(w.blocks)
		for j := b.oldStart; j < b.oldStart+b.oldCount; j++ {
			w.oldBlock[j] = number
		}
		for j := b.newStart; j < b.newStart+b.newCount; j++ {
			w.newBlock[j] = number
		}
		w.removedCount += b.oldCount
	}
	return w
}

// SplitLines splits a text into lines without line breaks. It drops the
// carriage return of a CRLF break. Final is true when the text ends with a
// line break.
func SplitLines(text []byte) ([]string, bool) {
	if len(text) == 0 {
		return nil, true
	}
	s := string(text)
	final := strings.HasSuffix(s, "\n")
	if final {
		s = s[:len(s)-1]
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines, final
}

func (w *fileWork) result(inputs []Input, opts Options) Result {
	var r Result
	lang := w.input.Language
	var oldTokens, newTokens []Token
	var oldSpans, newSpans [][]Span
	var oldEntities, newEntities []Entity
	if lang != nil {
		tokens := len(w.blocks) > 0 && !lang.WhitespaceMatters()
		oldA := analyze(lang, string(w.input.Old), w.old, tokens)
		newA := analyze(lang, string(w.input.New), w.new, tokens)
		oldTokens, newTokens = oldA.Tokens, newA.Tokens
		oldSpans, newSpans = oldA.Spans, newA.Spans
		oldEntities, newEntities = oldA.Entities, newA.Entities
	}
	for i := range w.blocks {
		b := &w.blocks[i]
		r.Removed += b.oldCount
		r.Added += b.newCount
		b.pairs = w.pairBlock(*b)
		b.flags = w.noise(*b, lang, oldTokens, newTokens)
		if b.flags != 0 {
			r.Noise++
		}
	}
	r.Changes = len(w.blocks)
	for _, id := range w.movedNew {
		if id != 0 {
			r.Moved = true
			break
		}
	}
	if !r.Moved {
		for _, id := range w.movedOld {
			if id != 0 {
				r.Moved = true
				break
			}
		}
	}
	r.Entities = w.entityChanges(oldEntities, newEntities)
	r.Rows = w.rows(inputs, opts, oldSpans, newSpans, newEntities, oldEntities)
	if !w.oldFinal && len(w.old) > 0 && w.newFinal {
		r.Notices = append(r.Notices, "The old version has no line break at the end.")
	}
	if !w.newFinal && len(w.new) > 0 && w.oldFinal && len(w.old) > 0 {
		r.Notices = append(r.Notices, "The new version has no line break at the end.")
	}
	return r
}

// pairBlock pairs the removed and added lines of a change that did not move:
// a moved line shows as moved, not as an edit of a line beside it.
func (w *fileWork) pairBlock(b block) []Pair {
	var oldIndex, newIndex []int
	var removed, added []string
	for k := 0; k < b.oldCount; k++ {
		if w.movedOld[b.oldStart+k] == 0 {
			oldIndex = append(oldIndex, k)
			removed = append(removed, w.old[b.oldStart+k])
		}
	}
	for k := 0; k < b.newCount; k++ {
		if w.movedNew[b.newStart+k] == 0 {
			newIndex = append(newIndex, k)
			added = append(added, w.new[b.newStart+k])
		}
	}
	pairs := PairLines(removed, added)
	for i := range pairs {
		pairs[i].Old, pairs[i].New = oldIndex[pairs[i].Old], newIndex[pairs[i].New]
	}
	return pairs
}

// noise labels a change that keeps the meaning: FlagWhitespace when each
// line changed only in whitespace, FlagFormat when the change keeps every
// token. An added or removed line with content is never noise.
func (w *fileWork) noise(b block, lang Language, oldTokens, newTokens []Token) int {
	if b.oldCount == 0 || b.newCount == 0 {
		blankOnly := true
		for _, line := range w.old[b.oldStart : b.oldStart+b.oldCount] {
			blankOnly = blankOnly && blank(line)
		}
		for _, line := range w.new[b.newStart : b.newStart+b.newCount] {
			blankOnly = blankOnly && blank(line)
		}
		if blankOnly {
			return FlagWhitespace
		}
		return 0
	}
	oldLines := w.old[b.oldStart : b.oldStart+b.oldCount]
	newLines := w.new[b.newStart : b.newStart+b.newCount]
	switch {
	case lang == nil:
		// Without a language, runs of whitespace are noise.
		if equalLines(collapsed(oldLines), collapsed(newLines)) {
			return FlagWhitespace
		}
		return 0
	case lang.WhitespaceMatters():
		// Indentation carries meaning: only trailing whitespace and blank
		// lines are noise.
		if equalLines(trimmedRight(oldLines), trimmedRight(newLines)) {
			return FlagWhitespace
		}
		return 0
	}
	// The tokens come from the whole text and keep strings and comments
	// whole, so whitespace inside them is a real change, also when the
	// string starts before the change.
	oldIn := tokensIn(oldTokens, b.oldStart, b.oldStart+b.oldCount-1)
	newIn := tokensIn(newTokens, b.newStart, b.newStart+b.newCount-1)
	if len(oldIn) != len(newIn) {
		return 0
	}
	sameLines := true
	for i := range oldIn {
		if oldIn[i].Text != newIn[i].Text {
			return 0
		}
		sameLines = sameLines && oldIn[i].First-b.oldStart == newIn[i].First-b.newStart && oldIn[i].Last-b.oldStart == newIn[i].Last-b.newStart
	}
	if sameLines && len(nonBlank(oldLines)) == len(nonBlank(newLines)) {
		return FlagWhitespace
	}
	return FlagFormat
}

// tokensIn gives the tokens that cover any line from first to last.
func tokensIn(tokens []Token, first, last int) []Token {
	var out []Token
	for _, token := range tokens {
		if token.Last >= first && token.First <= last {
			out = append(out, token)
		}
	}
	return out
}

func nonBlank(lines []string) []string {
	var out []string
	for _, line := range lines {
		if !blank(line) {
			out = append(out, line)
		}
	}
	return out
}

func trimmedRight(lines []string) []string {
	var out []string
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func collapsed(lines []string) []string {
	var out []string
	for _, line := range lines {
		if fields := strings.Fields(line); len(fields) > 0 {
			out = append(out, strings.Join(fields, " "))
		}
	}
	return out
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
