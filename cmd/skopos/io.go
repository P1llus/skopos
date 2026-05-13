// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"os"
)

// readBytes reads from path or stdin and returns the raw bytes. An empty
// path or the explicit "-" sentinel both mean stdin.
func readBytes(path string) ([]byte, error) {
	if path == "" || path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}
