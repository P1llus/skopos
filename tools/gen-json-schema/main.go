// SPDX-License-Identifier: Apache-2.0

// Command gen-json-schema walks the Go source files in schema/ and emits a
// JSON Schema (draft-07) describing the YAML spec wire format to
// docs/schema/v1/skopos.schema.json.
//
// Editors that speak the YAML language-server protocol (the VS Code YAML
// extension, Neovim, JetBrains, ...) read the schema for autocomplete, hover
// docs, and inline validation whenever a spec carries a modeline:
//
//	# yaml-language-server: $schema=<url-or-path>
//
// The container structs in schema/ map directly to JSON Schema objects, with
// field names, optionality (pointer / omitempty), and descriptions (doc
// comments) harvested from the AST. The three polymorphic leaf types — Value,
// Path, Predicate — have custom (un)marshallers whose wire shape does not
// match their Go struct, so their schemas are hand-authored fragments spliced
// in under definitions.
//
// The schema is structural only: it cannot express the cross-field semantic
// rules in schema/validate.go (reference resolution, state-lifetime
// conflicts). `skopos validate` remains the source of truth.
//
// Run modes:
//
//	go run ./tools/gen-json-schema          # rewrite the schema file
//	go run ./tools/gen-json-schema -check   # exit 1 if the file drifted
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultPackageDir = "schema"
	defaultOutput     = "docs/schema/v1/skopos.schema.json"
	schemaID          = "https://raw.githubusercontent.com/p1llus/skopos/main/docs/schema/v1/skopos.schema.json"
)

func main() {
	pkgDir := flag.String("pkg", defaultPackageDir, "schema package directory to parse")
	output := flag.String("out", defaultOutput, "output JSON Schema path")
	check := flag.Bool("check", false, "verify the on-disk file matches the generator's output; exit 1 on drift")
	flag.Parse()

	types, err := loadTypes(*pkgDir)
	if err != nil {
		fail("load %s: %v", *pkgDir, err)
	}

	root, err := buildSchema(types)
	if err != nil {
		fail("build: %v", err)
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		fail("marshal: %v", err)
	}
	out = append(out, '\n')

	if *check {
		on, err := os.ReadFile(*output)
		if err != nil {
			fail("read %s: %v", *output, err)
		}
		if !bytes.Equal(on, out) {
			fail("%s is out of date; run `go run ./tools/gen-json-schema` to regenerate", *output)
		}
		return
	}

	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		fail("mkdir %s: %v", filepath.Dir(*output), err)
	}
	if err := os.WriteFile(*output, out, 0o644); err != nil {
		fail("write %s: %v", *output, err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gen-json-schema: "+format+"\n", args...)
	os.Exit(1)
}

// ---- Schema building ----

// unionTypes are the discriminated-union structs whose every field is a
// mutually-exclusive variant key. They get a `oneOf` of single-required
// branches so exactly one variant must be present.
var unionTypes = map[string]bool{
	"Auth":        true,
	"OAuth2Auth":  true,
	"Body":        true,
	"Pagination":  true,
	"DecodeStage": true,
}

// handAuthored names the types whose schema is supplied verbatim by
// handDefsJSON rather than derived from the Go struct. Their wire shape is
// produced by custom (un)marshallers and does not match the struct fields.
var handAuthored = map[string]bool{
	"Value":       true,
	"Path":        true,
	"Predicate":   true,
	"PredicateEq": true,
	"SelectValue": true,
	"Reducer":     true,
	"SliceExpr":   true,
	"RegexExpr":   true,
	// Sub-structs reached only through the hand-authored fragments above;
	// never emitted from the AST.
	"RefValue":     true,
	"NowValue":     true,
	"SelectBranch": true,
	"FormatValue":  true,
	"ArithExpr":    true,
}

// fieldOverrides supplies a verbatim property schema for a (struct, Go field)
// pair, replacing the type-derived schema. Used for closed-set enums and
// consts that the struct tags alone cannot express. The field's doc comment
// is still attached as the description.
var fieldOverrides = map[string]map[string]string{
	"Doc":       {"IRVersion": `{"type":"string","const":"1"}`},
	"FieldDecl": {"Type": `{"type":"string","enum":["string","int","bool","secret","duration","timestamp","url","enum"]}`},
	"Request": {
		"Method":   `{"type":"string","enum":["GET","POST","PUT","PATCH","DELETE","HEAD"]}`,
		"OnStatus": `{"type":"object","propertyNames":{"pattern":"^[1-5][0-9][0-9]$"},"additionalProperties":{"type":"string","enum":["skip","fail","empty_events","invalidate_cache"]}}`,
		"Decode":   `{"oneOf":[{"type":"string","enum":["json","ndjson"]},{"type":"array","minItems":1,"items":{"$ref":"#/definitions/DecodeStage"}}]}`,
	},
	"ErrorBlock": {"Mode": `{"type":"string","enum":["standard","warn","fail"]}`},
	"FanOut":     {"Merge": `{"type":"string","enum":["flatten","wrap"]}`},
	"CSVDecode":  {"Header": `{"type":"string","enum":["present","absent"]}`},
}

// forceOptional lists (struct, Go field) pairs that are optional at validation
// time even though the struct tag carries no omitempty and the field is not a
// pointer. The custom Path codec treats the zero value as a meaningful
// "absent" form.
var forceOptional = map[string]map[string]bool{}

func buildSchema(types []structDoc) (map[string]any, error) {
	defs := map[string]any{}

	// Auto-derived container defs.
	var doc map[string]any
	for _, t := range types {
		if handAuthored[t.Name] {
			continue
		}
		schema := structSchema(t)
		if t.Name == "Doc" {
			doc = schema
			continue
		}
		defs[t.Name] = schema
	}
	if doc == nil {
		return nil, fmt.Errorf("type Doc not found in package")
	}

	// Named non-struct type: Progress is `[]ProgressWrite`.
	defs["Progress"] = map[string]any{
		"type":        "array",
		"description": "Flat list of state writes evaluated after each accepted page-response.",
		"items":       ref("ProgressWrite"),
	}

	// Hand-authored leaf defs.
	var hand map[string]any
	if err := json.Unmarshal([]byte(handDefsJSON), &hand); err != nil {
		return nil, fmt.Errorf("hand defs: %w", err)
	}
	maps.Copy(defs, hand)

	// Root = the Doc object schema, with metadata and definitions alongside.
	doc["$schema"] = "http://json-schema.org/draft-07/schema#"
	doc["$id"] = schemaID
	doc["$comment"] = "Code generated by tools/gen-json-schema. DO NOT EDIT."
	doc["title"] = "Skopos spec (ir_version 1)"
	doc["definitions"] = defs
	return doc, nil
}

// structSchema renders one container struct as a JSON Schema object.
func structSchema(t structDoc) map[string]any {
	props := map[string]any{}
	var required []string
	for _, f := range t.Fields {
		wire := f.YAML
		if wire == "" {
			wire = f.JSON
		}
		if wire == "" {
			continue // field excluded from the wire format
		}
		var fs map[string]any
		if raw, ok := fieldOverrides[t.Name][f.Name]; ok {
			fs = mustObject(raw)
		} else {
			fs = typeToSchema(f.Type)
		}
		if f.Doc != "" {
			fs["description"] = oneLine(f.Doc)
		}
		props[wire] = fs

		optional := f.Optional || forceOptional[t.Name][f.Name]
		if !optional {
			required = append(required, wire)
		}
	}

	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if t.Doc != "" {
		schema["description"] = oneLine(t.Doc)
	}
	if unionTypes[t.Name] {
		oneOf := make([]any, 0, len(props))
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			oneOf = append(oneOf, map[string]any{"required": []any{k}})
		}
		schema["oneOf"] = oneOf
	} else if len(required) > 0 {
		sort.Strings(required)
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		schema["required"] = req
	}
	return schema
}

// typeToSchema maps a Go type expression (as rendered by exprString) to a
// JSON Schema fragment. Pointers are unwrapped — optionality is decided by the
// caller. Named types become a $ref into definitions.
func typeToSchema(typ string) map[string]any {
	typ = strings.TrimPrefix(typ, "*")
	switch {
	case typ == "string":
		return map[string]any{"type": "string"}
	case typ == "int" || typ == "int64":
		return map[string]any{"type": "integer"}
	case typ == "bool":
		return map[string]any{"type": "boolean"}
	case typ == "struct{}":
		// Variant-selector forms like `none: {}`.
		return map[string]any{"type": "object"}
	case strings.HasPrefix(typ, "[]"):
		return map[string]any{"type": "array", "items": typeToSchema(typ[2:])}
	case strings.HasPrefix(typ, "map["):
		i := strings.Index(typ, "]")
		return map[string]any{"type": "object", "additionalProperties": typeToSchema(typ[i+1:])}
	default:
		return ref(typ)
	}
}

func ref(name string) map[string]any { return map[string]any{"$ref": "#/definitions/" + name} }

func mustObject(raw string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		fail("override %q: %v", raw, err)
	}
	return m
}

// oneLine collapses a multi-line doc comment into a single description string.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

// handDefsJSON holds the schemas for the polymorphic leaf types. Their wire
// shape is produced by custom codecs in schema/value.go, schema/path.go, and
// schema/predicate.go, so it cannot be derived from the Go struct fields.
const handDefsJSON = `{
  "Value": {
    "description": "Universal dynamic-value type (schema/value.go). A scalar string, integer, boolean, or null; or a single-discriminator expression object (ref, now, concat, select, format, base64, list, object, add, subtract, max, min, first, last, count, slice, regex). A string scalar is scanned for ${path} interpolation.",
    "oneOf": [
      {"type": "string"},
      {"type": "integer"},
      {"type": "boolean"},
      {"type": "null"},
      {"type": "object", "required": ["literal_string"], "additionalProperties": false, "properties": {"literal_string": {"type": "string"}}},
      {"type": "object", "required": ["ref"], "additionalProperties": false, "properties": {"ref": {"$ref": "#/definitions/Path"}, "default": {"$ref": "#/definitions/Value"}}},
      {"type": "object", "required": ["now"], "additionalProperties": false, "properties": {"now": {"const": true}}},
      {"type": "object", "required": ["concat"], "additionalProperties": false, "properties": {"concat": {"type": "array", "minItems": 2, "items": {"$ref": "#/definitions/Value"}}}},
      {"type": "object", "required": ["select"], "additionalProperties": false, "properties": {"select": {"$ref": "#/definitions/SelectValue"}}},
      {"type": "object", "required": ["format"], "additionalProperties": false, "properties": {"format": {"type": "string", "description": "Format verb (string, int, bool, rfc3339, rfc3339nano, unix_seconds, unix_millis, duration, url_encode, parse_duration) or a Go date layout."}, "value": {"$ref": "#/definitions/Value"}}},
      {"type": "object", "required": ["base64"], "additionalProperties": false, "properties": {"base64": {"$ref": "#/definitions/Value"}}},
      {"type": "object", "required": ["list"], "additionalProperties": false, "properties": {"list": {"type": "array", "items": {"$ref": "#/definitions/Value"}}}},
      {"type": "object", "required": ["object"], "additionalProperties": false, "properties": {"object": {"type": "object", "additionalProperties": {"$ref": "#/definitions/Value"}}}},
      {"type": "object", "required": ["add"], "additionalProperties": false, "properties": {"add": {"type": "array", "minItems": 2, "maxItems": 2, "items": {"$ref": "#/definitions/Value"}}}},
      {"type": "object", "required": ["subtract"], "additionalProperties": false, "properties": {"subtract": {"type": "array", "minItems": 2, "maxItems": 2, "items": {"$ref": "#/definitions/Value"}}}},
      {"type": "object", "required": ["max"], "additionalProperties": false, "properties": {"max": {"$ref": "#/definitions/Reducer"}}},
      {"type": "object", "required": ["min"], "additionalProperties": false, "properties": {"min": {"$ref": "#/definitions/Reducer"}}},
      {"type": "object", "required": ["first"], "additionalProperties": false, "properties": {"first": {"$ref": "#/definitions/Reducer"}}},
      {"type": "object", "required": ["last"], "additionalProperties": false, "properties": {"last": {"$ref": "#/definitions/Reducer"}}},
      {"type": "object", "required": ["count"], "additionalProperties": false, "properties": {"count": {"$ref": "#/definitions/Reducer"}}},
      {"type": "object", "required": ["slice"], "additionalProperties": false, "properties": {"slice": {"$ref": "#/definitions/SliceExpr"}}},
      {"type": "object", "required": ["regex"], "additionalProperties": false, "properties": {"regex": {"$ref": "#/definitions/RegexExpr"}}}
    ]
  },
  "SliceExpr": {
    "description": "List-slice operand: a list-shaped Value (ref, list, concat, select) given by its own discriminator key, plus optional zero-based integer from (inclusive start) and to (exclusive end). Out-of-range and inverted bounds clamp to a sub-list.",
    "type": "object",
    "additionalProperties": false,
    "properties": {
      "ref": {"$ref": "#/definitions/Path"},
      "default": {"$ref": "#/definitions/Value"},
      "list": {"type": "array", "items": {"$ref": "#/definitions/Value"}},
      "concat": {"type": "array", "minItems": 2, "items": {"$ref": "#/definitions/Value"}},
      "select": {"$ref": "#/definitions/SelectValue"},
      "from": {"type": "integer"},
      "to": {"type": "integer"}
    }
  },
  "Reducer": {
    "description": "Reducer operand: a bare array of Values (sugar for {list: [...]}) or a single list-shaped Value projection.",
    "oneOf": [
      {"type": "array", "items": {"$ref": "#/definitions/Value"}},
      {"$ref": "#/definitions/Value"}
    ]
  },
  "SelectValue": {
    "type": "object",
    "required": ["branches", "default"],
    "additionalProperties": false,
    "properties": {
      "branches": {
        "type": "array",
        "items": {
          "type": "object",
          "required": ["when", "value"],
          "additionalProperties": false,
          "properties": {"when": {"$ref": "#/definitions/Predicate"}, "value": {"$ref": "#/definitions/Value"}}
        }
      },
      "default": {"$ref": "#/definitions/Value"}
    }
  },
  "RegexExpr": {
    "type": "object",
    "required": ["pattern", "from"],
    "additionalProperties": false,
    "properties": {
      "pattern": {"type": "string", "description": "Go regular expression source."},
      "from": {"$ref": "#/definitions/Value"},
      "capture": {"type": "integer", "description": "1-based capture-group index; 0/omitted returns the full match."},
      "default": {"$ref": "#/definitions/Value"}
    }
  },
  "Path": {
    "description": "Namespace-rooted reference (schema/path.go). Dotted string (state.url, events.last.timestamp), {parts: [...]} escape form for segments containing dots, or null for the empty path. Roots: state, cache, events, extract, steps, response, or a fan_out.as alias.",
    "oneOf": [
      {"type": "string"},
      {"type": "object", "required": ["parts"], "additionalProperties": false, "properties": {"parts": {"type": "array", "minItems": 1, "items": {"type": "string"}}}},
      {"type": "null"}
    ]
  },
  "Predicate": {
    "description": "Boolean condition (schema/predicate.go). Exactly one form: eq, gt, lt, gte, lte, present, and, or, not, literal_bool.",
    "oneOf": [
      {"type": "object", "required": ["eq"], "additionalProperties": false, "properties": {"eq": {"$ref": "#/definitions/PredicateEq"}}},
      {"type": "object", "required": ["gt"], "additionalProperties": false, "properties": {"gt": {"$ref": "#/definitions/PredicateEq"}}},
      {"type": "object", "required": ["lt"], "additionalProperties": false, "properties": {"lt": {"$ref": "#/definitions/PredicateEq"}}},
      {"type": "object", "required": ["gte"], "additionalProperties": false, "properties": {"gte": {"$ref": "#/definitions/PredicateEq"}}},
      {"type": "object", "required": ["lte"], "additionalProperties": false, "properties": {"lte": {"$ref": "#/definitions/PredicateEq"}}},
      {"type": "object", "required": ["present"], "additionalProperties": false, "properties": {"present": {"$ref": "#/definitions/Path"}}},
      {"type": "object", "required": ["and"], "additionalProperties": false, "properties": {"and": {"type": "array", "minItems": 1, "items": {"$ref": "#/definitions/Predicate"}}}},
      {"type": "object", "required": ["or"], "additionalProperties": false, "properties": {"or": {"type": "array", "minItems": 1, "items": {"$ref": "#/definitions/Predicate"}}}},
      {"type": "object", "required": ["not"], "additionalProperties": false, "properties": {"not": {"$ref": "#/definitions/Predicate"}}},
      {"type": "object", "required": ["literal_bool"], "additionalProperties": false, "properties": {"literal_bool": {"type": "boolean"}}}
    ]
  },
  "PredicateEq": {
    "type": "object",
    "required": ["path", "value"],
    "additionalProperties": false,
    "properties": {
      "path": {"$ref": "#/definitions/Path"},
      "value": {"$ref": "#/definitions/Value"}
    }
  }
}`

// ---- AST harvesting (mirrors tools/gen-schema-doc) ----

// structDoc captures an exported struct type and its fields, in source order.
type structDoc struct {
	Name   string
	Doc    string
	Fields []fieldDoc
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
// struct types in (file, source-order) order.
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
				sd := structDoc{Name: ts.Name.Name, Doc: doc}
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

func commentText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	return strings.TrimRight(cg.Text(), "\n")
}

// parseTags reads a struct-field tag and returns (yamlName, jsonName, omitempty).
func parseTags(tag *ast.BasicLit) (string, string, bool) {
	if tag == nil {
		return "", "", false
	}
	raw := strings.Trim(tag.Value, "`")
	st := reflectStructTag(raw)
	yname, yopt := pickTag(st, "yaml")
	jname, jopt := pickTag(st, "json")
	return yname, jname, yopt || jopt
}

// reflectStructTag mirrors reflect.StructTag so the tool stays self-contained.
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

// unquoteTagValue is a stripped-down strconv.Unquote handling the escapes
// struct-tag values use in practice.
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

// pickTag returns (name, omitempty) for the named tag. The special "-" name
// reads as empty.
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

// exprString renders an ast.Expr as the Go source it came from. Limited to the
// shapes schema/ uses today.
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
		return "interface{}"
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
