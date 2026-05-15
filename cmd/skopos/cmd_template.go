// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/p1llus/skopos/templates"
)

// runTemplate is the entry point for "skopos template".
//
//	skopos template list
//	skopos template show [-o path] <name>
func runTemplate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("template: subcommand required (list, show)")
	}
	switch args[0] {
	case "list":
		return runTemplateList()
	case "show":
		return runTemplateShowCmd(args[1:])
	default:
		return fmt.Errorf("template: unknown subcommand %q (use list or show)", args[0])
	}
}

func runTemplateList() error {
	names := templates.Names()
	for _, n := range names {
		if _, err := fmt.Fprintln(os.Stdout, n); err != nil {
			return err
		}
	}
	return nil
}

// runTemplateShowCmd parses "skopos template show [-o path] <name>" and writes
// the named template to -o path (or stdout when -o is omitted).
func runTemplateShowCmd(args []string) error {
	fs := flag.NewFlagSet("template show", flag.ContinueOnError)
	out := fs.String("o", "", "Write template to this file instead of stdout. Use - for explicit stdout.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("template show: name is required")
	}
	return runTemplateShow(fs.Arg(0), *out)
}

func runTemplateShow(name, outPath string) error {
	data, err := templates.Read(name)
	if err != nil {
		// templates.Read already includes the available list in the message.
		return err
	}
	// Ensure the output ends with a newline so file writes don't produce a
	// file missing its trailing newline.
	if !strings.HasSuffix(string(data), "\n") {
		data = append(data, '\n')
	}
	if outPath == "" || outPath == "-" {
		_, err = os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(outPath, data, 0o644)
}
