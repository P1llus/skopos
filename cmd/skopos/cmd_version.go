// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
)

// Build metadata. GoReleaser injects these via -ldflags -X on tagged builds;
// for `go install ...@latest` and plain `go build` they stay empty and the
// version printer falls back to runtime/debug.ReadBuildInfo (vcs.* settings
// the Go toolchain stamps onto module-aware builds).
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func runVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return writeVersion(os.Stdout)
}

func writeVersion(w io.Writer) error {
	v, c, d := resolveBuildInfo()
	if _, err := fmt.Fprintf(w, "skopos %s\n", v); err != nil {
		return err
	}
	if c != "" {
		if _, err := fmt.Fprintf(w, "  %-9s %s\n", "commit:", c); err != nil {
			return err
		}
	}
	if d != "" {
		if _, err := fmt.Fprintf(w, "  %-9s %s\n", "built:", d); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "  %-9s %s\n", "go:", runtime.Version()); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "  %-9s %s/%s\n", "os/arch:", runtime.GOOS, runtime.GOARCH)
	return err
}

// resolveBuildInfo merges the ldflag-injected values with runtime/debug
// VCS settings. ldflags win; BuildInfo fills the blanks so users who
// `go install` from source still get an actionable commit + timestamp.
func resolveBuildInfo() (string, string, string) {
	v, c, d := version, commit, date
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return v, c, d
	}
	var rev, stamp string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		case "vcs.time":
			stamp = s.Value
		}
	}
	if c == "" && rev != "" {
		short := rev
		if len(short) > 12 {
			short = short[:12]
		}
		if modified {
			short += "-dirty"
		}
		c = short
	}
	if d == "" && stamp != "" {
		d = stamp
	}
	// info.Main.Version is "(devel)" for unpinned local builds; only adopt
	// it when it looks like a real module version so we don't clobber "dev".
	if v == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		v = info.Main.Version
	}
	return v, c, d
}
