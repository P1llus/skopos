// Command gen-schema-doc walks the Go source files in schema/ and emits a
// Markdown per-field reference to docs/schema-reference.md.
//
// The output is a sidecar to the hand-written docs/schema.md (which keeps
// the prose authoring rules, namespace tables, Value catalogue, format-verb
// set, and codec invariants). Whenever a Go struct in schema/ gains, loses,
// or renames an exported field, the generator must be re-run to keep the
// sidecar in lockstep with the public API surface.
//
// Run modes:
//
//	go run ./tools/gen-schema-doc          # rewrite docs/schema-reference.md
//	go run ./tools/gen-schema-doc -check   # exit non-zero if the file drifted
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultPackageDir = "schema"
	defaultOutput     = "docs/schema-reference.md"
)

func main() {
	pkgDir := flag.String("pkg", defaultPackageDir, "schema package directory to parse")
	output := flag.String("out", defaultOutput, "output Markdown path")
	check := flag.Bool("check", false, "verify the on-disk file matches the generator's output; exit 1 on drift")
	flag.Parse()

	types, err := loadTypes(*pkgDir)
	if err != nil {
		fail("load %s: %v", *pkgDir, err)
	}

	var buf bytes.Buffer
	if err := render(&buf, *pkgDir, types); err != nil {
		fail("render: %v", err)
	}

	if *check {
		on, err := os.ReadFile(*output)
		if err != nil {
			fail("read %s: %v", *output, err)
		}
		if !bytes.Equal(on, buf.Bytes()) {
			fail("%s is out of date; run `go run ./tools/gen-schema-doc` to regenerate", *output)
		}
		return
	}

	if err := os.WriteFile(*output, buf.Bytes(), 0o644); err != nil {
		fail("write %s: %v", *output, err)
	}
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "gen-schema-doc: "+format+"\n", args...)
	os.Exit(1)
}

// structDoc captures an exported struct type and its fields, in source order.
type structDoc struct {
	Name    string
	Doc     string
	Fields  []fieldDoc
	SrcFile string
}

// fieldDoc captures one exported field on a struct.
type fieldDoc struct {
	Name     string
	Type     string
	YAML     string
	JSON     string
	Optional bool
	Doc      string
}

// loadTypes parses every non-test .go file in dir and returns the exported
// struct types in (file, source-order) order. Aliases, interfaces, and
// non-struct type declarations are skipped.
func loadTypes(dir string) ([]structDoc, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	fset := token.NewFileSet()
	var out []structDoc
	for _, name := range names {
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			// GenDecl.Doc is the doc block on the `type (` group or, when
			// there's a single TypeSpec without a parenthesised group, on
			// the `type X struct { ... }` declaration itself.
			groupDoc := gd.Doc
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				doc := commentText(ts.Doc)
				if doc == "" {
					doc = commentText(groupDoc)
				}
				sd := structDoc{
					Name:    ts.Name.Name,
					Doc:     doc,
					SrcFile: name,
				}
				for _, fld := range st.Fields.List {
					if len(fld.Names) == 0 {
						continue // embedded; schema/ has none
					}
					typeStr := exprString(fld.Type)
					yamlName, jsonName, omit := parseTags(fld.Tag)
					optional := omit || isPointer(fld.Type)
					fdoc := commentText(fld.Doc)
					for _, n := range fld.Names {
						if !n.IsExported() {
							continue
						}
						sd.Fields = append(sd.Fields, fieldDoc{
							Name:     n.Name,
							Type:     typeStr,
							YAML:     yamlName,
							JSON:     jsonName,
							Optional: optional,
							Doc:      fdoc,
						})
					}
				}
				out = append(out, sd)
			}
		}
	}
	return out, nil
}

// bufErr is a tiny io.Writer helper for assembling Markdown output. It
// records the first write error and silently no-ops subsequent calls so
// render can chain Fprintf/Fprintln without per-line error checks; the
// caller inspects the final error via Err.
type bufErr struct {
	w   io.Writer
	err error
}

func (b *bufErr) printf(format string, args ...any) {
	if b.err != nil {
		return
	}
	_, b.err = fmt.Fprintf(b.w, format, args...)
}

func (b *bufErr) println(args ...any) {
	if b.err != nil {
		return
	}
	_, b.err = fmt.Fprintln(b.w, args...)
}

// Err returns the first write error, or nil if all writes succeeded.
func (b *bufErr) Err() error { return b.err }

// render writes the generated Markdown for the given struct types to w.
// Returns the first write error (none, for the *bytes.Buffer that callers
// pass in practice).
func render(w io.Writer, pkgDir string, types []structDoc) error {
	b := &bufErr{w: w}
	b.println("<!-- Code generated by tools/gen-schema-doc. DO NOT EDIT. -->")
	b.println()
	b.println("# Schema reference (generated)")
	b.println()
	b.printf("Per-field reference for every exported struct in package `%s`,\n", pkgDir)
	b.println("regenerated from the Go sources by `tools/gen-schema-doc`. The")
	b.println("hand-written authoring guide — namespace tables, Value forms,")
	b.println("Predicate forms, format-verb set, design rules, examples — lives in")
	b.println("[`schema.md`](schema.md). Use that document to learn the spec; use")
	b.println("this one to grep field names.")
	b.println()
	b.println("Regenerate with `go run ./tools/gen-schema-doc`; CI runs")
	b.println("`go run ./tools/gen-schema-doc -check` and fails on drift.")
	b.println()

	// Alphabetical index.
	sorted := make([]structDoc, len(types))
	copy(sorted, types)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	b.println("## Index")
	b.println()
	for _, t := range sorted {
		b.printf("- [`%s`](#%s)\n", t.Name, anchor(t.Name))
	}
	b.println()

	// Detail sections in source-order.
	for _, t := range types {
		renderType(b, t)
	}
	return b.Err()
}

func renderType(b *bufErr, t structDoc) {
	b.printf("## `%s`\n\n", t.Name)
	b.printf("_Defined in `%s/%s`._\n\n", defaultPackageDir, t.SrcFile)
	if t.Doc != "" {
		b.println(t.Doc)
		b.println()
	}
	if len(t.Fields) == 0 {
		b.println("_No exported fields._")
		b.println()
		return
	}
	b.println("| Field | YAML | Type | Optional | Description |")
	b.println("| --- | --- | --- | --- | --- |")
	for _, f := range t.Fields {
		wire := f.YAML
		if wire == "" {
			wire = f.JSON
		}
		if wire == "" {
			wire = "_(custom codec)_"
		} else {
			wire = "`" + wire + "`"
		}
		opt := "no"
		if f.Optional {
			opt = "yes"
		}
		b.printf("| `%s` | %s | `%s` | %s | %s |\n",
			f.Name, wire, f.Type, opt, mdEscape(f.Doc))
	}
	b.println()
}

// commentText returns the joined comment text with the leading "//" markers
// stripped. ast.CommentGroup.Text already does that; we just trim trailing
// whitespace so the rendered output stays compact.
func commentText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	return strings.TrimRight(cg.Text(), "\n")
}

// parseTags reads a struct-field tag and returns (yamlName, jsonName, omitempty).
// The yamlName falls back to the JSON name when only json is set, and vice
// versa. omitempty is true when either tag carries the modifier.
func parseTags(tag *ast.BasicLit) (string, string, bool) {
	if tag == nil {
		return "", "", false
	}
	raw := tag.Value
	// raw is `"yaml:\"...\""` etc.; trim the surrounding backticks.
	raw = strings.Trim(raw, "`")
	st := reflectStructTag(raw)
	yname, yopt := pickTag(st, "yaml")
	jname, jopt := pickTag(st, "json")
	return yname, jname, yopt || jopt
}

// reflectStructTag mirrors reflect.StructTag — declared here so this tool
// stays a tiny self-contained binary without dragging in encoding-related
// imports.
type reflectStructTag string

// Get returns the value associated with key in the tag string.
func (tag reflectStructTag) Get(key string) string {
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			return ""
		}
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			return ""
		}
		name := string(tag[:i])
		tag = tag[i+1:]
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			return ""
		}
		quoted := string(tag[:i+1])
		tag = tag[i+1:]
		if name == key {
			value, err := unquoteTagValue(quoted)
			if err != nil {
				return ""
			}
			return value
		}
	}
	return ""
}

// unquoteTagValue is a stripped-down strconv.Unquote that handles the only
// escapes struct-tag values use in practice (\\ \" \n). Bringing strconv in
// would work too but this keeps the tool self-contained.
func unquoteTagValue(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("bad quoted string")
	}
	inner := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c == '\\' && i+1 < len(inner) {
			switch inner[i+1] {
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(inner[i+1])
			}
			i++
			continue
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}

// pickTag returns (name, omitempty) for the named tag (yaml or json). The
// special "-" name reads as empty.
func pickTag(st reflectStructTag, key string) (string, bool) {
	v := st.Get(key)
	if v == "" {
		return "", false
	}
	parts := strings.Split(v, ",")
	name := parts[0]
	if name == "-" {
		name = ""
	}
	omit := false
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omit = true
		}
	}
	return name, omit
}

// exprString renders an ast.Expr as the Go source it came from. Limited to
// the shapes schema/ uses today (named types, pointers, slices, maps,
// `interface{}`, anonymous empty struct, selector expressions for foreign
// package types).
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return "*" + exprString(v.X)
	case *ast.ArrayType:
		if v.Len == nil {
			return "[]" + exprString(v.Elt)
		}
		return "[" + exprString(v.Len) + "]" + exprString(v.Elt)
	case *ast.MapType:
		return "map[" + exprString(v.Key) + "]" + exprString(v.Value)
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.InterfaceType:
		if v.Methods == nil || len(v.Methods.List) == 0 {
			return "interface{}"
		}
		return "interface{...}"
	case *ast.StructType:
		if v.Fields == nil || len(v.Fields.List) == 0 {
			return "struct{}"
		}
		return "struct{...}"
	case *ast.BasicLit:
		return v.Value
	}
	return "<?>"
}

func isPointer(e ast.Expr) bool { _, ok := e.(*ast.StarExpr); return ok }

// mdEscape collapses the multi-line doc comment text into a single
// Markdown table cell. Newlines become a space; pipes inside the text are
// escaped so the table boundary stays intact.
func mdEscape(s string) string {
	if s == "" {
		return ""
	}
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

// anchor returns the Markdown auto-generated anchor for a heading. GitHub's
// renderer lower-cases the heading and strips backticks; we mirror that
// just enough for the type-name anchors used in the Index.
func anchor(name string) string {
	return strings.ToLower(name)
}
