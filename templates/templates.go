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
//	skopos template show bearer_simple > spec.yaml
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *.yaml
var FS embed.FS

// Names returns the sorted list of template names without the .yaml extension.
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
		if strings.HasSuffix(name, ".yaml") {
			names = append(names, strings.TrimSuffix(name, ".yaml"))
		}
	}
	sort.Strings(names)
	return names
}

// Read returns the raw bytes of the named template. The name may be supplied
// with or without the .yaml extension. Returns an error if the template does
// not exist.
func Read(name string) ([]byte, error) {
	if !strings.HasSuffix(name, ".yaml") {
		name = name + ".yaml"
	}
	data, err := FS.ReadFile(name)
	if err != nil {
		available := Names()
		return nil, fmt.Errorf("template %q not found (available: %s)", strings.TrimSuffix(name, ".yaml"), strings.Join(available, ", "))
	}
	return data, nil
}
