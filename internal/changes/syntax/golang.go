package syntax

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	gotoken "go/token"
	"strings"

	"github.com/matipan/galpon/internal/changes/diff"
)

var (
	goTypes = words("any bool byte comparable complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint uint8 uint16 uint32 uint64 uintptr")
	goConst = words("true false nil iota")
)

// goScan scans Go source with the scanner of the standard library.
func goScan(text string) []token {
	fset := gotoken.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(text))
	var s scanner.Scanner
	s.Init(file, []byte(text), func(gotoken.Position, string) {}, scanner.ScanComments)
	type raw struct {
		start int
		end   int
		tok   gotoken.Token
		lit   string
	}
	var all []raw
	for {
		pos, tok, lit := s.Scan()
		if tok == gotoken.EOF {
			break
		}
		if tok == gotoken.SEMICOLON && lit == "\n" {
			// The scanner adds this semicolon at a line break.
			continue
		}
		start := file.Offset(pos)
		end := start + len(lit)
		switch {
		case lit == "":
			end = start + len(tok.String())
		case tok == gotoken.COMMENT && strings.HasPrefix(lit, "//"):
			// The scanner drops carriage returns from the text of a comment.
			if stop := strings.IndexByte(text[start:], '\n'); stop >= 0 {
				end = start + stop
			} else {
				end = len(text)
			}
		case tok == gotoken.COMMENT:
			if stop := strings.Index(text[start+2:], "*/"); stop >= 0 {
				end = start + 2 + stop + 2
			} else {
				end = len(text)
			}
		case tok == gotoken.STRING && strings.HasPrefix(lit, "`"):
			if stop := strings.IndexByte(text[start+1:], '`'); stop >= 0 {
				end = start + 1 + stop + 1
			} else {
				end = len(text)
			}
		}
		all = append(all, raw{start, min(end, len(text)), tok, lit})
	}
	out := make([]token, 0, len(all))
	for i, r := range all {
		style := Plain
		switch {
		case r.tok == gotoken.COMMENT:
			style = Comment
		case r.tok == gotoken.STRING || r.tok == gotoken.CHAR:
			style = String
		case r.tok == gotoken.INT || r.tok == gotoken.FLOAT || r.tok == gotoken.IMAG:
			style = Number
		case r.tok.IsKeyword():
			style = Keyword
		case r.tok == gotoken.IDENT:
			var previous, next gotoken.Token
			if i > 0 {
				previous = all[i-1].tok
			}
			if i+1 < len(all) {
				next = all[i+1].tok
			}
			switch {
			case goConst[r.lit]:
				style = Constant
			case previous == gotoken.FUNC || next == gotoken.LPAREN && !goTypes[r.lit]:
				style = Function
			case previous == gotoken.TYPE || goTypes[r.lit]:
				style = Type
			}
		case r.tok.IsOperator():
			switch r.tok {
			case gotoken.LPAREN, gotoken.RPAREN, gotoken.LBRACK, gotoken.RBRACK, gotoken.LBRACE, gotoken.RBRACE,
				gotoken.COMMA, gotoken.PERIOD, gotoken.SEMICOLON, gotoken.COLON:
			default:
				style = Operator
			}
		}
		out = append(out, token{r.start, r.end, style})
	}
	return out
}

// goEntities lists the functions, methods, types, constants and variables of
// Go source. A file with syntax errors gives the declarations that the parser
// could read.
func goEntities(text string, _ []string) []diff.Entity {
	fset := gotoken.NewFileSet()
	file, _ := parser.ParseFile(fset, "", text, parser.ParseComments|parser.SkipObjectResolution)
	if file == nil {
		return nil
	}
	line := func(pos gotoken.Pos) int { return fset.Position(pos).Line - 1 }
	offset := func(pos gotoken.Pos) int { return fset.Position(pos).Offset }
	body := func(from, to gotoken.Pos, name *ast.Ident) string {
		start, end := offset(from), offset(to)
		if start < 0 || end > len(text) || start >= end {
			return ""
		}
		at := offset(name.Pos())
		if at < start || at+len(name.Name) > end {
			return text[start:end]
		}
		return text[start:at] + "_" + text[at+len(name.Name):end]
	}
	var out []diff.Entity
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil {
				continue
			}
			kind, name := "func", d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind, name = "method", receiverName(d.Recv.List[0].Type)+"."+name
			}
			start := line(d.Pos())
			if d.Doc != nil {
				start = line(d.Doc.Pos())
			}
			out = append(out, diff.Entity{Kind: kind, Name: name, Key: kind + " " + name, Header: line(d.Pos()), Start: start, End: line(d.End() - 1), Body: body(d.Pos(), d.End(), d.Name)})
		case *ast.GenDecl:
			grouped := d.Lparen.IsValid()
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					header, start, end := line(s.Pos()), line(s.Pos()), line(s.End()-1)
					from := s.Pos()
					if !grouped {
						header, start, from = line(d.Pos()), line(d.Pos()), d.Pos()
						if d.Doc != nil {
							start = line(d.Doc.Pos())
						}
					}
					out = append(out, diff.Entity{Kind: "type", Name: s.Name.Name, Key: "type " + s.Name.Name, Header: header, Start: start, End: end, Body: body(from, s.End(), s.Name)})
				case *ast.ValueSpec:
					if len(s.Names) == 0 {
						continue
					}
					var names []string
					for _, ident := range s.Names {
						names = append(names, ident.Name)
					}
					kind := d.Tok.String()
					name := strings.Join(names, ", ")
					header, start, from := line(s.Pos()), line(s.Pos()), s.Pos()
					if !grouped {
						header, start, from = line(d.Pos()), line(d.Pos()), d.Pos()
					}
					out = append(out, diff.Entity{Kind: kind, Name: name, Key: kind + " " + name, Header: header, Start: start, End: line(s.End() - 1), Body: body(from, s.End(), s.Names[0])})
				}
			}
		}
	}
	return out
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.ParenExpr:
		return receiverName(t.X)
	}
	return "?"
}
