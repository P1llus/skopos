// SPDX-License-Identifier: Apache-2.0

// Package templates embeds the bundled skopos spec templates.
//
// The templates are ready-to-use YAML spec documents, one per API pattern.
// They are embedded into the binary at build time so users can browse and
// copy them without cloning the repository.
//
// Typical CLI usage:
//
//	skopos template list          # list all template names
//	skopos template show bearer_simple > spec.yml
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *.yml

// FS is the embedded filesystem containing all bundled spec templates.
var FS embed.FS

// Names returns the sorted list of template names without the .yml extension.
func Names() []string {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		// The embedded FS is always readable; this should never happen.
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".yml") {
			names = append(names, strings.TrimSuffix(name, ".yml"))
		}
	}
	sort.Strings(names)
	return names
}

// templateFileName resolves the user's name to an embedded template path.
func templateFileName(name string) string {
	name = strings.TrimSuffix(name, ".yaml")
	name = strings.TrimSuffix(name, ".yml")
	return name + ".yml"
}

// Read returns the raw bytes of the named template. The name may be supplied
// with or without a .yml extension (legacy .yaml is also accepted).
// Returns an error if the template does not exist.
func Read(name string) ([]byte, error) {
	file := templateFileName(name)
	data, err := FS.ReadFile(file)
	if err != nil {
		base := strings.TrimSuffix(file, ".yml")
		available := Names()
		return nil, fmt.Errorf("template %q not found (available: %s)", base, strings.Join(available, ", "))
	}
	return data, nil
}
