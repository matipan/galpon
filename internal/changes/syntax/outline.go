package syntax

import (
	"regexp"
	"strings"

	"github.com/matipan/galpon/internal/changes/diff"
)

// outlineRule finds the header line of a named part. The name is the
// submatch "name".
type outlineRule struct {
	kind string
	re   *regexp.Regexp
	// container parts give their name to the parts inside them, as a class
	// to its methods.
	container bool
}

func rule(kind, pattern string) outlineRule {
	return outlineRule{kind: kind, re: regexp.MustCompile(pattern)}
}

func container(kind, pattern string) outlineRule {
	r := rule(kind, pattern)
	r.container = true
	return r
}

// rangeMode says where a part ends.
type rangeMode int

const (
	// untilNext parts end before the next header.
	untilNext rangeMode = iota
	// byBraces parts end at the brace that closes the first brace of the
	// header.
	byBraces
	// byIndent parts end before the next line with the same or a smaller
	// indent, as in Python and YAML.
	byIndent
	// byHeading parts end before the next heading of the same or a higher
	// level, as in Markdown.
	byHeading
)

// outlineEntities finds the parts of a text by the outline rules. scanned
// and starts are the tokens and the line starts of the text, or nil to scan
// it here.
func (l *Language) outlineEntities(text string, lines []string, scanned []token, starts []int) []diff.Entity {
	type found struct {
		rule   outlineRule
		name   string
		header int
	}
	var headers []found
	for i, line := range lines {
		if len(line) > 400 {
			continue
		}
		for _, r := range l.outline {
			match := r.re.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			name := strings.TrimSpace(match[r.re.SubexpIndex("name")])
			if name == "" || l.lex.keywords[l.lex.fold(name)] {
				continue
			}
			headers = append(headers, found{r, name, i})
			break
		}
	}
	if len(headers) == 0 {
		return nil
	}
	var depths []int
	if l.ranges == byBraces {
		if scanned == nil {
			scanned, starts = l.scan(text), lineStarts(text)
		}
		depths = braceDepths(text, scanned, starts, len(lines))
	}
	out := make([]diff.Entity, 0, len(headers))
	for index, h := range headers {
		end := len(lines) - 1
		switch l.ranges {
		case untilNext:
			if index+1 < len(headers) {
				end = headers[index+1].header - 1
			}
		case byBraces:
			end = braceEnd(depths, lines, h.header)
		case byIndent:
			end = indentEnd(lines, h.header)
		case byHeading:
			level := headingLevel(lines[h.header])
			for _, next := range headers[index+1:] {
				if headingLevel(lines[next.header]) <= level {
					end = next.header - 1
					break
				}
			}
		}
		for end > h.header && strings.TrimSpace(lines[end]) == "" {
			end--
		}
		body := strings.Join(lines[h.header:end+1], "\n")
		if at := strings.Index(lines[h.header], h.name); at >= 0 {
			body = lines[h.header][:at] + "_" + body[at+len(h.name):]
		}
		out = append(out, diff.Entity{Kind: h.rule.kind, Name: h.name, Header: h.header, Start: h.header, End: end, Body: body})
	}
	// A part inside a container part takes the name of the container, so that
	// two methods with one name in two classes stay apart.
	for i := range out {
		parent := ""
		for j := range out {
			if j != i && headers[j].rule.container && out[j].Start < out[i].Start && out[i].End <= out[j].End {
				parent = out[j].Name
			}
		}
		if parent != "" {
			out[i].Name = parent + "." + out[i].Name
		}
		out[i].Key = out[i].Kind + " " + out[i].Name
	}
	return out
}

// braceDepths gives the brace depth at the start of each line, counting only
// braces outside strings and comments.
func braceDepths(text string, scanned []token, starts []int, count int) []int {
	depths := make([]int, count+1)
	delta := make([]int, count+1)
	var at lineCursor
	for _, t := range scanned {
		if t.end-t.start != 1 || t.style != Plain {
			continue
		}
		line := at.line(starts, t.start)
		if line >= count {
			continue
		}
		switch text[t.start] {
		case '{':
			delta[line]++
		case '}':
			delta[line]--
		}
	}
	for i := 1; i <= count; i++ {
		depths[i] = depths[i-1] + delta[i-1]
	}
	return depths
}

// braceEnd finds the line where the depth comes back to the depth of the
// header after the header opened a brace. A header without a brace in its
// first three lines ends at itself.
func braceEnd(depths []int, lines []string, header int) int {
	base := depths[header]
	opened := false
	for i := header; i < len(lines); i++ {
		if depths[i+1] > base {
			opened = true
		}
		if opened && depths[i+1] <= base {
			return i
		}
		if !opened && i >= header+2 {
			return header
		}
	}
	return len(lines) - 1
}

func indentEnd(lines []string, header int) int {
	level := leading(lines[header])
	end := header
	for i := header + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if leading(lines[i]) <= level {
			break
		}
		end = i
	}
	return end
}

func leading(line string) int {
	n := 0
	for _, c := range line {
		switch c {
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

func headingLevel(line string) int {
	trimmed := strings.TrimLeft(line, " ")
	level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
	if level == 0 {
		return 7
	}
	return level
}
