package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"

	"github.com/p1llus/skopos/schema"
)

// errValidationFailed is returned by runValidate when at least one
// error-severity diagnostic was printed. main.go maps this sentinel to
// exit code 1 without prefixing it with "skopos: " so the validate output
// remains diagnostic-only.
var errValidationFailed = errors.New("validation failed")

// runValidate is the entry point for "skopos validate".
// It reads an IR document from -i (or stdin), validates it, and prints
// diagnostics to stdout. Exits 1 when any error-severity diagnostic is present.
func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	input := fs.String("i", "", "Input IR file (YAML or JSON). Defaults to stdin. Use - for explicit stdin.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	data, err := readBytes(*input)
	if err != nil {
		return err
	}

	var diags []schema.Diagnostic
	doc, parseErr := schema.Parse(data)
	if parseErr != nil {
		diag := schema.Diagnostic{
			Message:  parseErr.Error(),
			Severity: "error",
		}
		if line := extractYAMLLine(parseErr.Error()); line > 0 {
			diag.Line = line
		}
		diags = []schema.Diagnostic{diag}
	} else {
		diags = schema.Validate(doc)
	}

	fileLabel := diagFileLabel(*input)
	for _, d := range diags {
		if _, err := fmt.Fprintln(os.Stdout, formatDiag(d, fileLabel)); err != nil {
			return err
		}
	}

	if hasErrorSeverity(diags) {
		return errValidationFailed
	}
	return nil
}

// diagFileLabel returns a display label for the source location annotation.
// Empty / "-" both map to "stdin".
func diagFileLabel(input string) string {
	if input == "" || input == "-" {
		return "stdin"
	}
	return input
}

// formatDiag renders one diagnostic as a human-readable line.
func formatDiag(d schema.Diagnostic, file string) string {
	var head string
	if d.Path == "" {
		head = fmt.Sprintf("%s: %s", d.Severity, d.Message)
	} else {
		head = fmt.Sprintf("%s: %s: %s", d.Path, d.Severity, d.Message)
	}
	if d.Line == 0 && d.Column == 0 {
		return head
	}
	if d.Column == 0 {
		// Drop the trailing ":0" so the suffix is `editor +line`-friendly
		// (vim / VS Code / etc. parse file:line, but file:line:0 means
		// "column 0" to no one).
		return fmt.Sprintf("%s (%s:%d)", head, file, d.Line)
	}
	return fmt.Sprintf("%s (%s:%d:%d)", head, file, d.Line, d.Column)
}

// hasErrorSeverity reports whether any diagnostic has Severity "error".
func hasErrorSeverity(diags []schema.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

// yamlLineRE matches the line-number token yaml.v3 prepends to its parse
// errors ("yaml: line 42: ..."). Anchored to that prefix so unrelated
// "... at line N inside ..." strings from future error sources don't
// silently swap in a wrong line number. The prefix is matched anywhere in
// the string because schema.Parse wraps the yaml.v3 error.
var yamlLineRE = regexp.MustCompile(`yaml: line (\d+):`)

func extractYAMLLine(s string) int {
	m := yamlLineRE.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}
