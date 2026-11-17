// Package syntax gives the diff of the Changes view what it knows about the
// language of a file: the colors of its tokens, the tokens that carry
// meaning, and its named parts, such as functions and types. Go files use the
// Go parser of the standard library. Other languages use a small lexer and
// outline rules: they find comments, strings, keywords and the headers of
// functions, classes and sections, which is enough to color a diff, to see a
// format-only change and to name the part of a file that a change touches.
package syntax

import (
	"path"
	"strings"

	"github.com/matipan/galpon/internal/changes/diff"
)

// Styles of highlighted spans. The view maps each style to a theme color.
const (
	Plain = iota
	Keyword
	String
	Comment
	Number
	Function
	Type
	Constant
	Operator
	Property
	Variable
	Heading
	Tag
)

// Language is a language of files. It implements diff.Language.
type Language struct {
	// Name is the name of the language, such as "go" or "python".
	Name       string
	lex        *lexer
	golang     bool
	outline    []outlineRule
	ranges     rangeMode
	whitespace bool
}

var (
	_ diff.Language = (*Language)(nil)
	_ diff.Analyzer = (*Language)(nil)
)

// Analyze reads a text once for its spans, its tokens and its entities.
func (l *Language) Analyze(text string, lines []string, tokens bool) diff.Analysis {
	starts := lineStarts(text)
	scanned := l.scan(text)
	a := diff.Analysis{Spans: highlight(scanned, starts, len(lines))}
	if tokens {
		a.Tokens = meaningTokens(text, scanned, starts)
	}
	a.Entities = l.entities(text, lines, scanned, starts)
	return a
}

// WhitespaceMatters is true when indentation changes the meaning.
func (l *Language) WhitespaceMatters() bool { return l.whitespace }

// Highlight gives the style spans of each line of a text.
func (l *Language) Highlight(text string, lines []string) [][]diff.Span {
	return highlight(l.scan(text), lineStarts(text), len(lines))
}

func highlight(scanned []token, starts []int, count int) [][]diff.Span {
	out := make([][]diff.Span, count)
	var at lineCursor
	for _, t := range scanned {
		if t.style == Plain {
			continue
		}
		first, last := at.line(starts, t.start), at.peek(starts, max(t.start, t.end-1))
		for line := first; line <= last && line < len(out); line++ {
			from := max(t.start, starts[line]) - starts[line]
			to := t.end - starts[line]
			if line+1 < len(starts) {
				to = min(t.end, starts[line+1]-1) - starts[line]
			}
			if to > from {
				out[line] = append(out[line], diff.Span{Start: from, End: to, Style: t.style})
			}
		}
	}
	return out
}

// Tokens gives the tokens of a text that carry meaning. A comma before a
// closing bracket is format, not meaning, so it is left out.
func (l *Language) Tokens(text string, lines []string) []diff.Token {
	return meaningTokens(text, l.scan(text), lineStarts(text))
}

func meaningTokens(text string, scanned []token, starts []int) []diff.Token {
	out := make([]diff.Token, 0, len(scanned))
	var at lineCursor
	for i, t := range scanned {
		value := text[t.start:t.end]
		if value == "," && i+1 < len(scanned) {
			if next := text[scanned[i+1].start:scanned[i+1].end]; next == ")" || next == "]" || next == "}" {
				continue
			}
		}
		if t.style == Comment {
			value = trimLines(value)
		}
		out = append(out, diff.Token{Text: value, First: at.line(starts, t.start), Last: at.peek(starts, max(t.start, t.end-1))})
	}
	return out
}

// Entities gives the named parts of a text.
func (l *Language) Entities(text string, lines []string) []diff.Entity {
	if l.golang {
		return goEntities(text, lines)
	}
	if len(l.outline) == 0 {
		return nil
	}
	return l.outlineEntities(text, lines, nil, nil)
}

// entities is Entities with the tokens of the text, which the ranges by
// braces need.
func (l *Language) entities(text string, lines []string, scanned []token, starts []int) []diff.Entity {
	if l.golang {
		return goEntities(text, lines)
	}
	if len(l.outline) == 0 {
		return nil
	}
	return l.outlineEntities(text, lines, scanned, starts)
}

func (l *Language) scan(text string) []token {
	if l.golang {
		return goScan(text)
	}
	return l.lex.scan(text)
}

// token is a scanned token: bytes [start, end) of the text and a style.
type token struct {
	start int
	end   int
	style int
}

func lineStarts(text string) []int {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineCursor finds the lines of offsets that only grow, as the offsets of
// scanned tokens do: it walks forward from the last line instead of
// searching each time.
type lineCursor struct{ at int }

// line gives the line, from 0, of an offset, and moves the cursor there.
func (c *lineCursor) line(starts []int, offset int) int {
	for c.at+1 < len(starts) && starts[c.at+1] <= offset {
		c.at++
	}
	return c.at
}

// peek gives the line of an offset at or after the cursor, and keeps the
// cursor where it is.
func (c *lineCursor) peek(starts []int, offset int) int {
	line := c.at
	for line+1 < len(starts) && starts[line+1] <= offset {
		line++
	}
	return line
}

func trimLines(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.Join(lines, "\n")
}

// Detect gives the language of a file from its name and the start of its
// content, or nil when the view has no language for it.
func Detect(name string, head []byte) *Language {
	base := path.Base(name)
	lower := strings.ToLower(base)
	if language, ok := byName[lower]; ok {
		return language
	}
	if strings.HasPrefix(lower, "dockerfile") || strings.HasSuffix(lower, ".dockerfile") || lower == "containerfile" {
		return languages["dockerfile"]
	}
	if ext := strings.ToLower(path.Ext(base)); ext != "" {
		if language, ok := byExtension[ext]; ok {
			return language
		}
	}
	if first, _, _ := strings.Cut(string(head), "\n"); strings.HasPrefix(first, "#!") {
		switch {
		case strings.Contains(first, "python"):
			return languages["python"]
		case strings.Contains(first, "node") || strings.Contains(first, "deno") || strings.Contains(first, "bun"):
			return languages["javascript"]
		case strings.Contains(first, "sh"):
			return languages["shell"]
		}
	}
	return nil
}
