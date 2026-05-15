// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/p1llus/skopos/internal/testserver"
)

var update = flag.Bool("update", false, "update testscript golden files")

// fixedTime is the deterministic time injected into testserver so events always
// get the same timestamps in golden files.
var fixedTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	os.Exit(testscript.RunMain(m, map[string]func() int{
		"skopos": Main,
	}))
}

func TestScripts(t *testing.T) {
	t.Parallel()

	// Start one shared testserver for the entire suite. Every scenario is
	// registered so any golden can reach any endpoint. Each scenario keeps its
	// own EventStore, so parallel scripts that hit different endpoints don't
	// interfere. The server is closed via t.Cleanup when TestScripts exits.
	opts := testserver.Options{
		PageSize:       2,
		EventsPerDrain: 5,
		Now:            func() time.Time { return fixedTime },
	}
	srv := testserver.New(opts, testserver.AllScenarios()...)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	sharedURL := hs.URL

	params := func(dir string) testscript.Params {
		return testscript.Params{
			Dir:           dir,
			UpdateScripts: *update,
			// Inject $URL and $URL_HOST for every script before it starts.
			// Scripts that need a custom isolated server can still call the
			// testserver command to get their own instance.
			Setup: func(e *testscript.Env) error {
				e.Setenv("URL", sharedURL)
				e.Setenv("URL_HOST", sharedURL)
				return nil
			},
			Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
				"testserver": cmdTestserver, // escape hatch for custom/isolated scenarios
				"expand":     cmdExpand,
				"normalize":  cmdNormalize,
				"replace":    cmdReplace,
			},
		}
	}

	// Root testdata directory.
	testscript.Run(t, params(filepath.Join("testdata")))

	// Walk one level of subdirectories so validate/, schema/, errors/ etc.
	// are each picked up as their own testscript.Run group.
	entries, err := os.ReadDir(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("readdir testdata: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join("testdata", e.Name())
		t.Run(e.Name(), func(t *testing.T) {
			t.Parallel()
			testscript.Run(t, params(dir))
		})
	}
}

// cmdTestserver boots an httptest.Server with the named scenarios (or all
// scenarios when no names are given). It sets $URL to the server's base URL
// and $URL_HOST to "scheme://host" (no path) for use in normalize patterns.
// The server is closed when the script exits.
//
// Usage:
//
//	testserver [scenario-name...]
func cmdTestserver(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("unsupported: ! testserver")
	}

	opts := testserver.Options{
		PageSize:       2,
		EventsPerDrain: 5,
		Now:            func() time.Time { return fixedTime },
	}

	all := testserver.AllScenarios()
	var scenarios []testserver.Scenario
	if len(args) == 0 {
		scenarios = all
	} else {
		byName := make(map[string]testserver.Scenario, len(all))
		for _, s := range all {
			byName[s.Name()] = s
		}
		for _, name := range args {
			s, ok := byName[name]
			if !ok {
				ts.Fatalf("testserver: unknown scenario %q", name)
			}
			scenarios = append(scenarios, s)
		}
	}

	srv := testserver.New(opts, scenarios...)
	hs := httptest.NewServer(srv.Handler())
	ts.Setenv("URL", hs.URL)
	// URL_HOST is useful for normalize replacements ("$URL_HOST/some/path").
	ts.Setenv("URL_HOST", hs.URL)
	ts.Defer(hs.Close)
}

// cmdExpand reads src, replaces $VAR references using the testscript
// environment, and writes the result to dst. Used to inject the ephemeral
// $URL into a template spec file before passing it to `skopos run`.
//
// Usage:
//
//	expand src dst
func cmdExpand(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("unsupported: ! expand")
	}
	if len(args) != 2 {
		ts.Fatalf("usage: expand src dst")
	}
	src, err := os.ReadFile(ts.MkAbs(args[0]))
	ts.Check(err)
	expanded := os.Expand(string(src), ts.Getenv)
	ts.Check(os.WriteFile(ts.MkAbs(args[1]), []byte(expanded), 0o644))
}

// cmdReplace performs a literal string replacement in a file. The replacement
// string is expanded with the testscript environment so you can substitute
// env vars (e.g. $URL) into the file.
//
// Usage:
//
//	replace file old new
func cmdReplace(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("unsupported: ! replace")
	}
	if len(args) != 3 {
		ts.Fatalf("usage: replace file old new")
	}
	path := ts.MkAbs(args[0])
	data, err := os.ReadFile(path)
	ts.Check(err)
	newVal := os.Expand(args[2], ts.Getenv)
	result := strings.ReplaceAll(string(data), args[1], newVal)
	ts.Check(os.WriteFile(path, []byte(result), 0o644))
}

// cmdNormalize scrubs volatile fields out of JSONL files so golden comparisons
// are deterministic. For each file it processes every line as JSON, replaces
// known volatile fields, and re-encodes the result in-place.
//
// Volatile fields scrubbed:
//   - "started_at" — wall-clock timestamp of each HTTP exchange
//   - "elapsed"    — round-trip duration
//   - URL host+port occurrences replaced with $URL_HOST value
//
// Usage:
//
//	normalize file...
func cmdNormalize(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("unsupported: ! normalize")
	}
	if len(args) == 0 {
		ts.Fatalf("usage: normalize file...")
	}
	urlHost := ts.Getenv("URL_HOST")
	for _, arg := range args {
		path := ts.MkAbs(arg)
		data, err := os.ReadFile(path)
		ts.Check(err)
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			if line == "" {
				continue
			}
			line = scrubJSONLine(line, urlHost)
			out = append(out, line)
		}
		ts.Check(os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644))
	}
}

// scrubJSONLine replaces volatile fields in a single JSON line. It parses the
// line as a JSON object, zeroes the known volatile keys, re-encodes, and then
// performs a string-level replacement of the real URL_HOST with "$URL_HOST" so
// the golden files are portable across runs.
func scrubJSONLine(line, urlHost string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		// Not a JSON object (e.g. event JSONL is also valid); leave as-is.
		// Still do URL host replacement.
		return replaceURLHost(line, urlHost)
	}

	// Scrub volatile Exchange fields.
	if _, ok := m["started_at"]; ok {
		m["started_at"] = json.RawMessage(`"<scrubbed>"`)
	}
	if _, ok := m["elapsed"]; ok {
		m["elapsed"] = json.RawMessage(`"<scrubbed>"`)
	}

	// Re-encode without HTML escaping (keep < > & readable).
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		// Fallback: return original with URL replacement only.
		return replaceURLHost(line, urlHost)
	}
	result := strings.TrimRight(sb.String(), "\n")
	return replaceURLHost(result, urlHost)
}

// replaceURLHost replaces the real httptest server address (which includes an
// ephemeral port) with the stable "$URL_HOST" placeholder.
func replaceURLHost(s, urlHost string) string {
	if urlHost == "" {
		return s
	}
	return strings.ReplaceAll(s, urlHost, "$URL_HOST")
}
