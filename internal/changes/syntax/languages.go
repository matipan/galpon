package syntax

// The languages of the Changes view, their file names and their outline
// rules.

var (
	cBlock     = [][2]string{{"/*", "*/"}}
	cStrings   = []stringRule{{open: `"`, close: `"`, escape: true}, {open: `'`, close: `'`, escape: true}}
	jsStrings  = append([]stringRule{{open: "`", close: "`", escape: true, multiline: true}}, cStrings...)
	pyStrings  = []stringRule{{open: `"""`, close: `"""`, escape: true, multiline: true, prefixes: "rRbBuUfF"}, {open: `'''`, close: `'''`, escape: true, multiline: true, prefixes: "rRbBuUfF"}, {open: `"`, close: `"`, escape: true, prefixes: "rRbBuUfF"}, {open: `'`, close: `'`, escape: true, prefixes: "rRbBuUfF"}}
	shStrings  = []stringRule{{open: `"`, close: `"`, escape: true, multiline: true}, {open: `'`, close: `'`, multiline: true}}
	tomlString = []stringRule{{open: `"""`, close: `"""`, escape: true, multiline: true}, {open: `'''`, close: `'''`, multiline: true}, {open: `"`, close: `"`, escape: true}, {open: `'`, close: `'`}}
)

const jsKeywords = "async await break case catch class const continue debugger default delete do else export extends finally for function if import in instanceof let new return static super switch this throw try typeof var void while with yield"
const tsKeywords = jsKeywords + " abstract declare enum implements interface keyof namespace private protected public readonly satisfies type infer is module override"

var jsOutline = []outlineRule{
	container("class", `^\s*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+(?P<name>[A-Za-z_$][\w$]*)`),
	rule("function", `^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*(?P<name>[A-Za-z_$][\w$]*)`),
	rule("function", `^\s*(?:export\s+)?(?:const|let|var)\s+(?P<name>[A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*(?::[^=]+)?=>|[A-Za-z_$][\w$]*\s*=>)`),
	container("interface", `^\s*(?:export\s+)?(?:declare\s+)?interface\s+(?P<name>[A-Za-z_$][\w$]*)`),
	rule("type", `^\s*(?:export\s+)?(?:declare\s+)?type\s+(?P<name>[A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\s*=`),
	rule("enum", `^\s*(?:export\s+)?(?:const\s+)?enum\s+(?P<name>[A-Za-z_$][\w$]*)`),
	rule("method", `^\s+(?:(?:public|private|protected|static|async|readonly|override|get|set)\s+)*(?P<name>[A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\([^)]*\)?\s*(?::\s*[^{]+)?\{\s*$`),
}

func clike(keywords, types, constants string) *lexer {
	return &lexer{
		lineComments: []string{"//"}, blockComments: cBlock, strings: cStrings,
		keywords: words(keywords), types: words(types), constants: words(constants),
		calls: true,
	}
}

var languages = map[string]*Language{}
var byExtension = map[string]*Language{}
var byName = map[string]*Language{}

func register(language *Language, extensions []string, names ...string) {
	languages[language.Name] = language
	for _, ext := range extensions {
		byExtension[ext] = language
	}
	for _, name := range names {
		byName[name] = language
	}
}

func init() {
	register(&Language{Name: "go", golang: true}, []string{".go"})

	js := &lexer{
		lineComments: []string{"//"}, blockComments: cBlock, strings: jsStrings,
		keywords: words(jsKeywords), constants: words("true false null undefined NaN Infinity"),
		types:          words("Array Boolean Date Error Map Number Object Promise RegExp Set String Symbol"),
		defineFunction: words("function"), defineType: words("class extends"),
		wordStart: "$", wordExtra: "$", calls: true,
	}
	register(&Language{Name: "javascript", lex: js, outline: jsOutline, ranges: byBraces}, []string{".js", ".mjs", ".cjs", ".jsx"})
	ts := *js
	ts.keywords = words(tsKeywords)
	ts.types = words("any bigint boolean never number object string symbol unknown void Array Map Promise Record Set Partial Readonly")
	ts.defineType = words("class extends interface type enum implements")
	register(&Language{Name: "typescript", lex: &ts, outline: jsOutline, ranges: byBraces}, []string{".ts", ".mts", ".cts", ".tsx"})

	register(&Language{
		Name: "python", whitespace: true, ranges: byIndent,
		lex: &lexer{
			lineComments: []string{"#"}, strings: pyStrings,
			keywords:       words("and as assert async await break class continue def del elif else except finally for from global if import in is lambda match case nonlocal not or pass raise return try while with yield"),
			constants:      words("True False None NotImplemented Ellipsis self cls"),
			types:          words("bool bytes dict float frozenset int list object set str tuple type"),
			defineFunction: words("def"), defineType: words("class"), calls: true, decorator: '@',
		},
		outline: []outlineRule{
			container("class", `^\s*class\s+(?P<name>\w+)`),
			rule("def", `^\s*(?:async\s+)?def\s+(?P<name>\w+)`),
		},
	}, []string{".py", ".pyi"})

	register(&Language{
		Name: "rust", ranges: byBraces,
		lex: &lexer{
			lineComments: []string{"//"}, blockComments: cBlock,
			strings:        []stringRule{{open: `"`, close: `"`, escape: true, multiline: true, prefixes: "b"}},
			keywords:       words("as async await break const continue crate dyn else enum extern fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait type unsafe use where while"),
			constants:      words("true false None Some Ok Err"),
			types:          words("bool char f32 f64 i8 i16 i32 i64 i128 isize str u8 u16 u32 u64 u128 usize String Vec Option Result Box"),
			defineFunction: words("fn"), defineType: words("struct enum trait type impl"), calls: true,
		},
		outline: []outlineRule{
			container("impl", `^\s*impl(?:<[^>]*>)?\s+(?:[\w:<>, ]+\s+for\s+)?(?P<name>[\w:]+)`),
			container("trait", `^\s*(?:pub(?:\([\w:]+\))?\s+)?(?:unsafe\s+)?trait\s+(?P<name>\w+)`),
			rule("fn", `^\s*(?:pub(?:\([\w:]+\))?\s+)?(?:const\s+)?(?:async\s+)?(?:unsafe\s+)?(?:extern\s+"[^"]*"\s+)?fn\s+(?P<name>\w+)`),
			rule("struct", `^\s*(?:pub(?:\([\w:]+\))?\s+)?struct\s+(?P<name>\w+)`),
			rule("enum", `^\s*(?:pub(?:\([\w:]+\))?\s+)?enum\s+(?P<name>\w+)`),
			rule("mod", `^\s*(?:pub(?:\([\w:]+\))?\s+)?mod\s+(?P<name>\w+)`),
		},
	}, []string{".rs"})

	register(&Language{
		Name: "shell", ranges: byBraces,
		lex: &lexer{
			lineComments: []string{"#"}, commentAfterSpace: true, strings: shStrings,
			keywords:  words("case do done elif else esac exit export fi for function if in local readonly return select set shift then trap unset until while"),
			wordExtra: "-", variable: '$', calls: true,
		},
		outline: []outlineRule{
			rule("function", `^\s*(?:function\s+)?(?P<name>[\w.:-]+)\s*\(\)\s*\{?`),
			rule("function", `^\s*function\s+(?P<name>[\w.:-]+)`),
		},
	}, []string{".sh", ".bash", ".zsh", ".ksh"}, ".bashrc", ".zshrc", ".profile", "pkgbuild")

	register(&Language{
		Name: "markdown", ranges: byHeading, lex: &lexer{markdown: true},
		outline: []outlineRule{rule("section", `^\s{0,3}#{1,6}\s+(?P<name>.+?)\s*#*\s*$`)},
	}, []string{".md", ".markdown", ".mdx"})

	register(&Language{
		Name: "yaml", whitespace: true, ranges: byIndent,
		lex: &lexer{
			lineComments: []string{"#"}, commentAfterSpace: true, strings: cStrings,
			constants: words("true false yes no on off null ~ True False Yes No On Off Null TRUE FALSE NULL"),
			wordExtra: "-./", keyColon: true, variable: '&',
		},
		outline: []outlineRule{rule("key", `^(?P<name>[A-Za-z0-9_][\w.-]*)\s*:`)},
	}, []string{".yaml", ".yml"})

	register(&Language{
		Name: "json",
		lex: &lexer{
			lineComments: []string{"//"}, blockComments: cBlock, strings: []stringRule{{open: `"`, close: `"`, escape: true}},
			constants: words("true false null"), keyColon: true,
		},
	}, []string{".json", ".jsonc", ".json5", ".jsonl", ".ndjson", ".code-workspace"}, ".prettierrc", ".babelrc", ".eslintrc", "tsconfig.json")

	register(&Language{
		Name: "toml",
		lex: &lexer{
			lineComments: []string{"#"}, strings: tomlString,
			constants: words("true false inf nan"), wordExtra: "-", keyEquals: true, sections: true,
		},
		outline: []outlineRule{rule("table", `^\s*\[\[?\s*(?P<name>[^\]]+?)\s*\]\]?`)},
	}, []string{".toml"}, "cargo.lock", "poetry.lock", "uv.lock", "pipfile")

	register(&Language{
		Name: "sql",
		lex: &lexer{
			lineComments: []string{"--"}, blockComments: cBlock,
			strings:  []stringRule{{open: `'`, close: `'`, multiline: true}, {open: `"`, close: `"`}},
			keywords: words("add all alter and as asc begin between by case cascade check column commit constraint create cross database default delete desc distinct drop else end exists foreign from full function grant group having if in index inner insert into is join key left like limit materialized not null offset on or order outer primary procedure references replace returning revoke right rollback schema select set table then to transaction trigger truncate union unique update using values view when where with"),
			types:    words("bigint bigserial boolean bool bytea char date decimal double float integer int interval json jsonb numeric real serial smallint text time timestamp timestamptz uuid varchar"),
			foldCase: true, calls: true,
		},
		outline: []outlineRule{rule("create", `(?i)^\s*create\s+(?:or\s+replace\s+)?(?:temp(?:orary)?\s+)?(?:unique\s+)?(?:materialized\s+)?(?:table|view|function|index|trigger|procedure|type|schema|sequence)\s+(?:if\s+not\s+exists\s+)?(?P<name>[\w."]+)`)},
	}, []string{".sql"})

	register(&Language{
		Name: "css", ranges: byBraces,
		lex: &lexer{
			lineComments: nil, blockComments: cBlock, strings: cStrings,
			wordStart: "-#.@", wordExtra: "-", keyColon: true, calls: true,
		},
		outline: []outlineRule{rule("rule", `^(?P<name>[^\s{}/][^{};]*?)\s*\{\s*$`)},
	}, []string{".css", ".scss", ".less", ".sass"})

	register(&Language{
		Name: "markup",
		lex: &lexer{
			blockComments: [][2]string{{"<!--", "-->"}}, strings: []stringRule{{open: `"`, close: `"`, multiline: true}, {open: `'`, close: `'`, multiline: true}},
			wordExtra: "-:", tags: true, keyEquals: false,
		},
	}, []string{".html", ".htm", ".xml", ".svg", ".xhtml", ".plist", ".vue", ".svelte"})

	register(&Language{
		Name: "dockerfile", ranges: untilNext,
		lex: &lexer{
			lineComments: []string{"#"}, commentAfterSpace: true, strings: cStrings,
			keywords: words("from run cmd label maintainer expose env add copy entrypoint volume user workdir arg onbuild stopsignal healthcheck shell as"),
			foldCase: true, lineKeywords: true, variable: '$',
		},
		outline: []outlineRule{rule("stage", `(?i)^\s*FROM\s+\S+(?:\s+AS\s+(?P<name>\S+))`)},
	}, nil, "dockerfile", "containerfile")

	register(&Language{
		Name: "make", whitespace: true, ranges: untilNext,
		lex: &lexer{
			lineComments: []string{"#"}, commentAfterSpace: true, strings: cStrings,
			keywords:  words("ifeq ifneq ifdef ifndef else endif include define endef export override"),
			wordExtra: "-./%", variable: '$',
		},
		outline: []outlineRule{rule("target", `^(?P<name>[\w./%-]+)\s*:(?:[^=]|$)`)},
	}, []string{".mk"}, "makefile", "gnumakefile")

	register(&Language{
		Name: "proto", ranges: byBraces,
		lex: clike("syntax package import option message service rpc returns enum oneof map repeated optional required reserved extend stream", "double float int32 int64 uint32 uint64 sint32 sint64 fixed32 fixed64 sfixed32 sfixed64 bool string bytes", "true false"),
		outline: []outlineRule{
			container("message", `^\s*message\s+(?P<name>\w+)`),
			container("service", `^\s*service\s+(?P<name>\w+)`),
			rule("enum", `^\s*enum\s+(?P<name>\w+)`),
			rule("rpc", `^\s*rpc\s+(?P<name>\w+)`),
		},
	}, []string{".proto"})

	graphql := clike("query mutation subscription fragment on type input enum interface union scalar schema extend implements directive repeatable", "Int Float String Boolean ID", "true false null")
	graphql.lineComments = []string{"#"}
	graphql.blockComments = nil
	graphql.strings = []stringRule{{open: `"""`, close: `"""`, multiline: true}, {open: `"`, close: `"`, escape: true}}
	graphql.keyColon = true
	register(&Language{
		Name: "graphql", ranges: byBraces, lex: graphql,
		outline: []outlineRule{rule("type", `^\s*(?:extend\s+)?(?:type|input|enum|interface|union|scalar|query|mutation|subscription|fragment)\s+(?P<name>\w+)`)},
	}, []string{".graphql", ".gql", ".graphqls"})

	hcl := clike("resource data variable output locals module provider terraform for in if else", "string number bool list map set object any", "true false null")
	hcl.lineComments = []string{"#", "//"}
	hcl.keyEquals = true
	register(&Language{
		Name: "hcl", ranges: byBraces, lex: hcl,
		outline: []outlineRule{rule("block", `^(?P<name>(?:resource|data|variable|output|module|provider)\s+"[^"]+"(?:\s+"[^"]+")?)`)},
	}, []string{".tf", ".tfvars", ".hcl", ".nomad"})

	register(&Language{
		Name: "gomod",
		lex: &lexer{
			lineComments: []string{"//"}, strings: []stringRule{{open: `"`, close: `"`, escape: true}, {open: "`", close: "`"}},
			keywords:  words("module go toolchain require replace exclude retract use godebug tool"),
			wordExtra: "-./+",
		},
	}, nil, "go.mod", "go.work")

	register(&Language{
		Name: "java", ranges: byBraces,
		lex: clike("abstract assert break case catch class const continue default do else enum extends final finally for if implements import instanceof interface native new package private protected public return static super switch synchronized this throw throws transient try var void volatile while record sealed permits yield fun val when object companion data suspend override open internal lateinit", "boolean byte char double float int long short String Int Long Boolean Unit", "true false null"),
		outline: []outlineRule{
			container("class", `^\s*(?:(?:public|private|protected|abstract|final|static|sealed|data|open|internal)\s+)*(?:class|interface|enum|record|object)\s+(?P<name>\w+)`),
			rule("method", `^\s+(?:(?:public|private|protected|static|final|abstract|synchronized|override|suspend|open|internal)\s+)*(?:fun\s+)?(?:[\w<>\[\], ?]+\s+)?(?P<name>\w+)\s*\([^;]*$`),
		},
	}, []string{".java", ".kt", ".kts", ".scala"})

	register(&Language{
		Name: "c", ranges: byBraces,
		lex: clike("auto break case char class const constexpr continue default delete do else enum explicit extern for friend goto if inline namespace new operator private protected public register return sizeof static struct switch template this throw try typedef typename union using virtual void volatile while", "bool char double float int long short signed size_t uint8_t uint16_t uint32_t uint64_t int8_t int16_t int32_t int64_t unsigned auto string", "true false NULL nullptr"),
		outline: []outlineRule{
			container("class", `^\s*(?:class|struct)\s+(?P<name>\w+)[^;]*$`),
			rule("function", `^(?:[\w:*&<>]+\s+)+\**(?P<name>[\w:~]+)\s*\([^;]*$`),
		},
	}, []string{".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".m", ".mm", ".cs", ".swift", ".zig", ".dart"})

	lua := clike("and break do else elseif end for function goto if in local not or repeat return then until while", "", "true false nil")
	lua.lineComments = []string{"--"}
	lua.blockComments = [][2]string{{"--[[", "]]"}}
	lua.defineFunction = words("function")
	register(&Language{Name: "lua", ranges: untilNext, lex: lua, outline: []outlineRule{rule("function", `^\s*(?:local\s+)?function\s+(?P<name>[\w.:]+)`)}}, []string{".lua"})

	ruby := &lexer{
		lineComments: []string{"#"}, strings: cStrings,
		keywords:       words("alias and begin break case class def defined do else elsif end ensure for if in module next nil not or redo rescue retry return self super then undef unless until when while yield"),
		constants:      words("true false nil"),
		defineFunction: words("def"), defineType: words("class module"), calls: true, variable: '@',
	}
	register(&Language{Name: "ruby", ranges: untilNext, lex: ruby, outline: []outlineRule{
		container("class", `^\s*(?:class|module)\s+(?P<name>[\w:]+)`),
		rule("def", `^\s*def\s+(?P<name>[\w.?!=]+)`),
	}}, []string{".rb", ".rake", ".gemspec"}, "gemfile", "rakefile")
}
