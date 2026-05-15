// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/p1llus/skopos/templates"
)

func TestTemplateList_ContainsBearerSimple(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runTemplateList(); err != nil {
			t.Fatalf("runTemplateList: %v", err)
		}
	})

	if !strings.Contains(out, "bearer_simple") {
		t.Errorf("list output does not contain bearer_simple:\n%s", out)
	}
}

func TestTemplateList_SortedAlphabetically(t *testing.T) {
	names := templates.Names()
	if len(names) == 0 {
		t.Fatal("Names() returned empty slice")
	}
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("Names() not sorted: %q before %q", names[i-1], names[i])
		}
	}
	// Confirm list output matches Names() order.
	out := captureStdout(t, func() {
		if err := runTemplateList(); err != nil {
			t.Fatalf("runTemplateList: %v", err)
		}
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != len(names) {
		t.Fatalf("list line count = %d, want %d", len(lines), len(names))
	}
	for i, name := range names {
		if lines[i] != name {
			t.Errorf("line[%d] = %q, want %q", i, lines[i], name)
		}
	}
}

func TestTemplateShow_BearerSimple(t *testing.T) {
	want, err := templates.Read("bearer_simple")
	if err != nil {
		t.Fatalf("templates.Read: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runTemplateShow("bearer_simple", ""); err != nil {
			t.Fatalf("runTemplateShow: %v", err)
		}
	})

	// The output must contain the embedded bytes.
	if !strings.Contains(out, string(want)) && !bytes.Equal([]byte(out), want) {
		// Allow for trailing newline difference.
		if strings.TrimRight(out, "\n") != strings.TrimRight(string(want), "\n") {
			t.Errorf("show output does not match embedded bytes")
		}
	}
}

func TestTemplateShow_AcceptsYmlExtension(t *testing.T) {
	outWithout := captureStdout(t, func() {
		if err := runTemplateShow("bearer_simple", ""); err != nil {
			t.Fatalf("show without ext: %v", err)
		}
	})
	outYml := captureStdout(t, func() {
		if err := runTemplateShow("bearer_simple.yml", ""); err != nil {
			t.Fatalf("show with .yml: %v", err)
		}
	})
	outLegacyYaml := captureStdout(t, func() {
		if err := runTemplateShow("bearer_simple.yaml", ""); err != nil {
			t.Fatalf("show with legacy .yaml: %v", err)
		}
	})
	if outWithout != outYml {
		t.Errorf("show with and without .yml extension differ")
	}
	if outWithout != outLegacyYaml {
		t.Errorf("show with legacy .yaml extension differ")
	}
}

func TestTemplateShow_UnknownName(t *testing.T) {
	err := runTemplateShow("no_such_template_xyz", "")
	if err == nil {
		t.Fatal("expected error for unknown template, got nil")
	}
	msg := err.Error()
	// The error should mention the unknown name and at least one known template.
	if !strings.Contains(msg, "no_such_template_xyz") {
		t.Errorf("error does not mention the requested name: %s", msg)
	}
	// Should include at least one available name in the message.
	knownNames := templates.Names()
	if len(knownNames) == 0 {
		t.Fatal("no known names to check against")
	}
	found := false
	for _, n := range knownNames {
		if strings.Contains(msg, n) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("error does not mention any available template name: %s", msg)
	}
}

func TestRunTemplate_NoArgs(t *testing.T) {
	err := runTemplate(nil)
	if err == nil {
		t.Fatal("expected error for missing subcommand")
	}
}

func TestRunTemplate_UnknownSubcommand(t *testing.T) {
	err := runTemplate([]string{"frobnicate"})
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("error should mention unknown subcommand, got: %v", err)
	}
}

func TestRunTemplate_ShowMissingName(t *testing.T) {
	err := runTemplate([]string{"show"})
	if err == nil {
		t.Fatal("expected error when show has no name argument")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTemplateList_AllNamesValid(t *testing.T) {
	// Every name returned by Names() must be readable via Read().
	for _, name := range templates.Names() {
		name := name
		t.Run(name, func(t *testing.T) {
			data, err := templates.Read(name)
			if err != nil {
				t.Fatalf("Read(%q): %v", name, err)
			}
			if len(data) == 0 {
				t.Errorf("Read(%q) returned empty bytes", name)
			}
			// Sanity: every template should declare ir_version.
			if !strings.Contains(string(data), "ir_version") {
				t.Errorf("template %q does not contain ir_version", name)
			}
		})
	}
}

// Ensure the count matches what we know is in templates/.
func TestTemplateList_Count(t *testing.T) {
	names := templates.Names()
	if len(names) == 0 {
		t.Fatal("Names() returned no templates")
	}
	// We shipped 20 templates; guard against accidental empty embed.
	if len(names) < 10 {
		t.Errorf("suspiciously few templates: %d (want >= 10)", len(names))
	}
	fmt.Printf("template count: %d\n", len(names))
}
