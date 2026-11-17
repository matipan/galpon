package syntax

import (
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/changes/diff"
)

func styled(t *testing.T, language *Language, text string) map[string]int {
	t.Helper()
	lines, _ := diff.SplitLines([]byte(text))
	out := map[string]int{}
	for i, spans := range language.Highlight(text, lines) {
		for _, span := range spans {
			out[lines[i][span.Start:span.End]] = span.Style
		}
	}
	return out
}

func TestGoHighlightNamesTheKindsOfTokens(t *testing.T) {
	got := styled(t, Detect("main.go", nil), "package main\n\n// Run starts it.\nfunc Run(name string) error {\n\tfmt.Println(\"hi\", 42, nil)\n\treturn nil\n}\n")
	want := map[string]int{"package": Keyword, "// Run starts it.": Comment, "Run": Function, "string": Type, "Println": Function, `"hi"`: String, "42": Number, "nil": Constant, "return": Keyword}
	for text, style := range want {
		if got[text] != style {
			t.Errorf("%q has style %d, want %d", text, got[text], style)
		}
	}
}

func TestMultilineTokensColorEachLine(t *testing.T) {
	got := styled(t, Detect("a.go", nil), "var s = `one\ntwo`\n/* a\nb */\n")
	for _, text := range []string{"`one", "two`"} {
		if got[text] != String {
			t.Errorf("%q has style %d, want a string", text, got[text])
		}
	}
	if got["b */"] != Comment {
		t.Errorf("the second line of a block comment has style %d, want a comment", got["b */"])
	}
}

func TestGoEntitiesNameFunctionsMethodsTypesAndValues(t *testing.T) {
	text := "package p\n\n// Doc of T.\ntype T struct{}\n\nconst (\n\tA = 1\n\tB = 2\n)\n\n// Get gets.\nfunc (t *T) Get() int {\n\treturn A\n}\n\nfunc helper[V any](v V) V { return v }\n"
	lines, _ := diff.SplitLines([]byte(text))
	entities := Detect("p.go", nil).Entities(text, lines)
	got := map[string]diff.Entity{}
	for _, entity := range entities {
		got[entity.Key] = entity
	}
	for _, key := range []string{"type T", "const A", "const B", "method T.Get", "func helper"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("entities %v, want %s", keys(got), key)
		}
	}
	method := got["method T.Get"]
	if method.Header != 11 || method.Start != 10 || method.End != 13 {
		t.Fatalf("method lines %d %d %d, want header 11, start 10 with its doc, end 13", method.Header, method.Start, method.End)
	}
	if !strings.Contains(method.Body, ") _() int") {
		t.Fatalf("method body %q does not mask the name", method.Body)
	}
}

func keys(m map[string]diff.Entity) []string {
	var out []string
	for key := range m {
		out = append(out, key)
	}
	return out
}

func TestPythonOutlineUsesIndentAndClassNames(t *testing.T) {
	text := "class Store:\n    def get(self):\n        return 1\n\n    def put(self, v):\n        pass\n\ndef get():\n    return 2\n"
	lines, _ := diff.SplitLines([]byte(text))
	got := map[string][2]int{}
	for _, entity := range Detect("store.py", nil).Entities(text, lines) {
		got[entity.Key] = [2]int{entity.Start, entity.End}
	}
	want := map[string][2]int{"class Store": {0, 5}, "def Store.get": {1, 2}, "def Store.put": {4, 5}, "def get": {7, 8}}
	for key, lines := range want {
		if got[key] != lines {
			t.Fatalf("entities %v, want %v", got, want)
		}
	}
}

func TestTypeScriptOutlineUsesBraces(t *testing.T) {
	text := "export class Store {\n  get(id: string): Item {\n    if (id) {\n      return items[id];\n    }\n  }\n}\n\nexport const load = async (url: string) => {\n  return fetch(url);\n};\n"
	lines, _ := diff.SplitLines([]byte(text))
	got := map[string][2]int{}
	for _, entity := range Detect("store.ts", nil).Entities(text, lines) {
		got[entity.Key] = [2]int{entity.Start, entity.End}
	}
	want := map[string][2]int{"class Store": {0, 6}, "method Store.get": {1, 5}, "function load": {8, 10}}
	for key, lines := range want {
		if got[key] != lines {
			t.Fatalf("entities %v, want %v", got, want)
		}
	}
}

func TestYAMLKeysAndComments(t *testing.T) {
	got := styled(t, Detect("deploy.yaml", nil), "spec:\n  replicas: 3 # more later\n  name: \"web\"\n")
	if got["spec"] != Property || got["replicas"] != Property || got["# more later"] != Comment || got["3"] != Number || got[`"web"`] != String {
		t.Fatalf("styles %v", got)
	}
}

func TestMarkdownHeadingsAndFences(t *testing.T) {
	text := "# Title\n\nSome `code` and [a link](https://x.y).\n\n```go\nx := 1\n```\n"
	got := styled(t, Detect("README.md", nil), text)
	if got["# Title"] != Heading || got["`code`"] != String || got["x := 1"] != String || got["[a link]"] != Function {
		t.Fatalf("styles %v", got)
	}
}

func compare(name, old, new string) diff.Result {
	return diff.Compare([]diff.Input{{Path: name, Old: []byte(old), New: []byte(new), Language: Detect(name, []byte(new))}}, diff.DefaultOptions)[0]
}

func TestGoReflowIsFormatOnly(t *testing.T) {
	result := compare("a.go", "package a\n\nvar x = call(alpha, beta)\n", "package a\n\nvar x = call(\n\talpha,\n\tbeta,\n)\n")
	if result.Noise != 1 || result.Changes != 1 {
		t.Fatalf("noise %d of %d changes, want a format change", result.Noise, result.Changes)
	}
}

func TestWhitespaceInsideAStringIsARealChange(t *testing.T) {
	old := "package a\n\nconst config = `\nkey:\n  value: 1\n`\n"
	new := "package a\n\nconst config = `\nkey:\nvalue: 1\n`\n"
	if result := compare("a.go", old, new); result.Noise != 0 {
		t.Fatalf("noise %d, want the indent change inside the raw string to count", result.Noise)
	}
}

func TestMarkdownRewrapIsFormatOnly(t *testing.T) {
	result := compare("doc.md", "# A\n\nOne two three four five six.\n", "# A\n\nOne two three\nfour five six.\n")
	if result.Noise != 1 {
		t.Fatalf("noise %d, want the rewrapped paragraph to be a format change", result.Noise)
	}
}

func TestEntityChangesComeFromGoDeclarations(t *testing.T) {
	old := "package a\n\nfunc keep() int {\n\treturn 1\n}\n\nfunc oldName(v int) int {\n\treturn v * 2\n}\n"
	new := "package a\n\nfunc keep() int {\n\treturn 2\n}\n\nfunc newName(v int) int {\n\treturn v * 2\n}\n"
	got := map[string]string{}
	for _, change := range compare("a.go", old, new).Entities {
		got[change.Name] = change.Change
	}
	if got["keep"] != "modified" || got["newName"] != "renamed" {
		t.Fatalf("entity changes %v, want keep modified and newName renamed", got)
	}
}

func TestDetectUsesNamesExtensionsAndShebangs(t *testing.T) {
	cases := map[string]string{"Makefile": "make", "Dockerfile.dev": "dockerfile", "go.mod": "gomod", "x.tsx": "typescript", "a/b.yml": "yaml"}
	for name, want := range cases {
		if got := Detect(name, nil); got == nil || got.Name != want {
			t.Errorf("Detect(%q) = %v, want %s", name, got, want)
		}
	}
	if got := Detect("tool", []byte("#!/usr/bin/env python3\nprint(1)\n")); got == nil || got.Name != "python" {
		t.Errorf("a python shebang gives %v", got)
	}
	if got := Detect("notes.txt", nil); got != nil {
		t.Errorf("a text file gives %s, want no language", got.Name)
	}
}
