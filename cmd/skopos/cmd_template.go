// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/p1llus/skopos/templates"
)

// runTemplate is the entry point for "skopos template".
//
//	skopos template list
//	skopos template show <name>
func runTemplate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("template: subcommand required (list, show)")
	}
	switch args[0] {
	case "list":
		return runTemplateList()
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("template show: name is required")
		}
		return runTemplateShow(args[1])
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

func runTemplateShow(name string) error {
	data, err := templates.Read(name)
	if err != nil {
		// templates.Read already includes the available list in the message.
		return err
	}
	// Ensure the output ends with a newline so shell redirections don't
	// produce a file missing its trailing newline.
	if _, err := os.Stdout.Write(data); err != nil {
		return err
	}
	if !strings.HasSuffix(string(data), "\n") {
		if _, err := fmt.Fprintln(os.Stdout); err != nil {
			return err
		}
	}
	return nil
}
