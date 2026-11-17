package syntax

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// stringRule is a kind of string literal: the text between open and close.
// Prefixes are letters that can come right before open, such as the r of a
// Python raw string.
type stringRule struct {
	open      string
	close     string
	escape    bool
	multiline bool
	prefixes  string
}

// lexer is a small configurable lexer. It knows enough of a language to
// color it and to compare its tokens: comments, strings, numbers, words and
// symbols.
type lexer struct {
	lineComments []string
	// commentAfterSpace is true when a line comment starts only at the start
	// of a line or after whitespace, as # in shells and YAML.
	commentAfterSpace bool
	blockComments     [][2]string
	strings           []stringRule
	keywords          set
	types             set
	constants         set
	// defineFunction and defineType are keywords whose next word is the name
	// of a function or a type.
	defineFunction set
	defineType     set
	foldCase       bool
	// wordExtra are characters, other than letters, digits and _, that a word
	// can hold after its first character.
	wordExtra string
	// wordStart are characters, other than letters and _, that can start a
	// word.
	wordStart string
	// calls marks a word before ( as a function.
	calls bool
	// keyColon marks a word or a string before : as a property.
	keyColon bool
	// keyEquals marks the first word of a line before = as a property.
	keyEquals bool
	// variable is the character that starts a variable, as $ in shells.
	variable byte
	// decorator is the character that starts a decorator, as @ in Python.
	decorator byte
	// sections marks a line [name] as a section header.
	sections bool
	// lineKeywords marks the first word of a line as a keyword when it is in
	// keywords, as the instructions of a Dockerfile.
	lineKeywords bool
	// tags marks <name and </name as tags, for HTML and XML.
	tags bool
	// markdown scans Markdown: headings, fences, code spans and links.
	markdown bool
}

type set map[string]bool

func words(list string) set {
	out := set{}
	for _, word := range strings.Fields(list) {
		out[word] = true
	}
	return out
}

func (l *lexer) scan(text string) []token {
	if l.markdown {
		return scanMarkdown(text)
	}
	var out []token
	lineStart := true
	firstWord := true
	previous := ""
	for i := 0; i < len(text); {
		c := text[i]
		switch c {
		case '\n':
			lineStart, firstWord = true, true
			i++
			continue
		case ' ', '\t', '\r':
			i++
			continue
		}
		if end, ok := l.lineComment(text, i, lineStart); ok {
			out = append(out, token{i, end, Comment})
			i = end
			continue
		}
		if end, ok := l.blockComment(text, i); ok {
			out = append(out, token{i, end, Comment})
			i = end
			lineStart = false
			continue
		}
		if l.sections && lineStart && c == '[' {
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				end = len(text)
			} else {
				end += i
			}
			if close := strings.LastIndexByte(text[i:end], ']'); close > 0 {
				out = append(out, token{i, i + close + 1, Type})
				i += close + 1
				lineStart, firstWord = false, false
				continue
			}
		}
		if end, ok := l.stringAt(text, i); ok {
			style := String
			if l.keyColon && nextByte(text, end) == ':' && nextByteAfter(text, end, 1) != ':' {
				style = Property
			}
			out = append(out, token{i, end, style})
			i = end
			lineStart, firstWord = false, false
			previous = ""
			continue
		}
		if isDigit(c) || (c == '.' && i+1 < len(text) && isDigit(text[i+1]) && (i == 0 || !l.wordByte(text[i-1]))) {
			j := i + 1
			for j < len(text) {
				d := text[j]
				if (d == 'e' || d == 'E' || d == 'p' || d == 'P') && j+1 < len(text) && (text[j+1] == '+' || text[j+1] == '-') {
					j += 2
					continue
				}
				if !isDigit(d) && !isLetter(d) && d != '_' && d != '.' {
					break
				}
				j++
			}
			out = append(out, token{i, j, Number})
			i = j
			lineStart, firstWord = false, false
			continue
		}
		if l.variable != 0 && c == l.variable && i+1 < len(text) {
			j := i + 1
			if text[j] == '{' || text[j] == '(' {
				closer := byte('}')
				if text[j] == '(' {
					closer = ')'
				}
				if end := strings.IndexByte(text[j:], closer); end > 0 && !strings.Contains(text[j:j+end], "\n") {
					j += end + 1
				}
			} else {
				for j < len(text) && (l.wordByte(text[j]) || text[j] == '@' || text[j] == '#' || text[j] == '?') {
					j++
					if !l.wordByte(text[j-1]) {
						break
					}
				}
			}
			if j > i+1 {
				out = append(out, token{i, j, Variable})
				i = j
				lineStart, firstWord = false, false
				continue
			}
		}
		if l.decorator != 0 && c == l.decorator && i+1 < len(text) && l.startsWord(text, i+1) {
			j := l.wordEnd(text, i+1)
			for j < len(text) && text[j] == '.' && l.startsWord(text, j+1) {
				j = l.wordEnd(text, j+1)
			}
			out = append(out, token{i, j, Function})
			i = j
			lineStart, firstWord = false, false
			continue
		}
		if l.tags && c == '<' && i+1 < len(text) {
			j := i + 1
			if text[j] == '/' || text[j] == '!' || text[j] == '?' {
				j++
			}
			if l.startsWord(text, j) {
				end := l.wordEnd(text, j)
				out = append(out, token{i, end, Tag})
				i = end
				lineStart, firstWord = false, false
				continue
			}
		}
		if l.startsWord(text, i) {
			end := l.wordEnd(text, i)
			word := text[i:end]
			style := l.classify(text, word, end, previous, firstWord)
			out = append(out, token{i, end, style})
			previous = ""
			if style == Keyword {
				previous = l.fold(word)
			}
			i = end
			lineStart, firstWord = false, false
			continue
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		style := Plain
		if strings.IndexByte("+-*/%=<>!&|^~?:", c) >= 0 {
			style = Operator
		}
		out = append(out, token{i, i + size, style})
		i += size
		lineStart = false
		if c != '-' && c != '*' {
			firstWord = false
		}
		previous = ""
	}
	return out
}

func (l *lexer) fold(word string) string {
	if l.foldCase {
		return strings.ToLower(word)
	}
	return word
}

func (l *lexer) classify(text, word string, end int, previous string, firstWord bool) int {
	key := l.fold(word)
	switch {
	case l.lineKeywords && !firstWord:
		// Only the first word of a line is an instruction.
	case l.keywords[key]:
		return Keyword
	}
	switch {
	case l.constants[key]:
		return Constant
	case l.types[key]:
		return Type
	case previous != "" && l.defineFunction[previous]:
		return Function
	case previous != "" && l.defineType[previous]:
		return Type
	}
	next := nextByte(text, end)
	switch {
	case l.keyColon && next == ':' && nextByteAfter(text, end, 1) != ':':
		return Property
	case l.keyEquals && firstWord && next == '=' && nextByteAfter(text, end, 1) != '=':
		return Property
	case l.calls && next == '(':
		return Function
	}
	return Plain
}

func (l *lexer) lineComment(text string, i int, lineStart bool) (int, bool) {
	for _, prefix := range l.lineComments {
		if !strings.HasPrefix(text[i:], prefix) {
			continue
		}
		if l.commentAfterSpace && !lineStart && i > 0 && text[i-1] != ' ' && text[i-1] != '\t' {
			continue
		}
		end := strings.IndexByte(text[i:], '\n')
		if end < 0 {
			return len(text), true
		}
		return i + end, true
	}
	return 0, false
}

func (l *lexer) blockComment(text string, i int) (int, bool) {
	for _, pair := range l.blockComments {
		if !strings.HasPrefix(text[i:], pair[0]) {
			continue
		}
		rest := text[i+len(pair[0]):]
		end := strings.Index(rest, pair[1])
		if end < 0 {
			return len(text), true
		}
		return i + len(pair[0]) + end + len(pair[1]), true
	}
	return 0, false
}

func (l *lexer) stringAt(text string, i int) (int, bool) {
	for _, rule := range l.strings {
		k := i
		if rule.prefixes != "" {
			for k < len(text) && k-i < 2 && strings.IndexByte(rule.prefixes, text[k]) >= 0 {
				k++
			}
			// Prefix letters inside a longer word are not a prefix.
			if k > i && i > 0 && l.wordByte(text[i-1]) {
				continue
			}
		}
		if !strings.HasPrefix(text[k:], rule.open) {
			continue
		}
		j := k + len(rule.open)
		for j < len(text) {
			if rule.escape && text[j] == '\\' {
				j += 2
				continue
			}
			if !rule.multiline && text[j] == '\n' {
				break
			}
			if strings.HasPrefix(text[j:], rule.close) {
				j += len(rule.close)
				return min(j, len(text)), true
			}
			j++
		}
		return min(j, len(text)), true
	}
	return 0, false
}

func (l *lexer) startsWord(text string, i int) bool {
	if i >= len(text) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text[i:])
	return r == '_' || unicode.IsLetter(r) || (r < utf8.RuneSelf && strings.IndexByte(l.wordStart, byte(r)) >= 0)
}

func (l *lexer) wordEnd(text string, i int) int {
	_, size := utf8.DecodeRuneInString(text[i:])
	j := i + size
	for j < len(text) {
		r, size := utf8.DecodeRuneInString(text[j:])
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) && (r >= utf8.RuneSelf || strings.IndexByte(l.wordExtra, byte(r)) < 0) {
			break
		}
		j += size
	}
	return j
}

func (l *lexer) wordByte(c byte) bool {
	return c == '_' || isLetter(c) || isDigit(c) || c >= utf8.RuneSelf || strings.IndexByte(l.wordExtra, c) >= 0
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// nextByte gives the first byte after i that is not a space or a tab, or 0.
func nextByte(text string, i int) byte {
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if i < len(text) {
		return text[i]
	}
	return 0
}

// nextByteAfter gives the byte that comes skip bytes after the next byte
// that is not a space or a tab.
func nextByteAfter(text string, i, skip int) byte {
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if i+skip < len(text) {
		return text[i+skip]
	}
	return 0
}

// scanMarkdown colors Markdown: headings, fenced code, quotes, list marks,
// code spans and links. Words are tokens too, so a rewrapped paragraph keeps
// its tokens and shows as a format change.
func scanMarkdown(text string) []token {
	var out []token
	fence := ""
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		start := offset
		offset += len(line)
		body := strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimLeft(body, " ")
		lead := len(body) - len(trimmed)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
				out = append(out, token{start + lead, start + len(body), Comment})
			} else if body != "" {
				out = append(out, token{start, start + len(body), String})
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			out = append(out, token{start + lead, start + len(body), Comment})
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			if level <= 6 && (len(trimmed) == level || trimmed[level] == ' ') {
				out = append(out, token{start + lead, start + len(body), Heading})
				continue
			}
		}
		at := lead
		if strings.HasPrefix(trimmed, ">") {
			out = append(out, token{start + lead, start + lead + 1, Comment})
			at++
		} else if mark := listMark(trimmed); mark > 0 {
			out = append(out, token{start + lead, start + lead + mark, Operator})
			at += mark
		}
		out = append(out, markdownInline(body, at, start)...)
	}
	return out
}

func listMark(line string) int {
	if len(line) >= 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') && line[1] == ' ' {
		return 1
	}
	i := 0
	for i < len(line) && isDigit(line[i]) {
		i++
	}
	if i > 0 && i+1 < len(line) && (line[i] == '.' || line[i] == ')') && line[i+1] == ' ' {
		return i + 1
	}
	return 0
}

// markdownInline scans one line of prose from at: code spans, links and
// words.
func markdownInline(body string, at, base int) []token {
	var out []token
	for i := at; i < len(body); {
		c := body[i]
		switch c {
		case '`':
			ticks := 1
			for i+ticks < len(body) && body[i+ticks] == '`' {
				ticks++
			}
			closer := strings.Repeat("`", ticks)
			if end := strings.Index(body[i+ticks:], closer); end >= 0 {
				stop := i + ticks + end + ticks
				out = append(out, token{base + i, base + stop, String})
				i = stop
				continue
			}
			out = append(out, token{base + i, base + i + ticks, Plain})
			i += ticks
		case '[':
			close := strings.Index(body[i:], "](")
			if close > 0 {
				end := strings.IndexByte(body[i+close:], ')')
				if end > 0 {
					out = append(out, token{base + i, base + i + close + 1, Function})
					out = append(out, token{base + i + close + 1, base + i + close + end + 1, String})
					i += close + end + 1
					continue
				}
			}
			out = append(out, token{base + i, base + i + 1, Plain})
			i++
		case ' ', '\t':
			i++
		default:
			j := i
			for j < len(body) && body[j] != ' ' && body[j] != '\t' && body[j] != '`' && body[j] != '[' {
				j++
			}
			if j == i {
				j++
			}
			out = append(out, token{base + i, base + j, Plain})
			i = j
		}
	}
	return out
}
