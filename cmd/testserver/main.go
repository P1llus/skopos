// SPDX-License-Identifier: Apache-2.0

// testserver is a unified HTTP stub for all starter-subset skopos templates.
//
// Usage:
//
//	go run ./cmd/testserver [flags]
//
// Flags:
//
//	-addr string          Listen address (default ":9999")
//	-page-size int        Events per paginated page (default 2)
//	-events-per-drain int Events appended at drain start (default 5)
//	-scenarios string     Comma-separated list of scenarios to host (default: all)
//
// Scenario prefixes and their matching templates:
//
//	bearer_simple              templates/bearer_simple.yml
//	cursor_token               templates/cursor_token.yml
//	page_number                templates/page_number.yml
//	offset                     templates/offset_pagination.yml
//	link_header                templates/link_header.yml
//	oauth2                     templates/oauth2_client_credentials.yml
//	api_key_auth               templates/api_key_auth.yml
//	basic_auth                 templates/basic_auth.yml
//	custom_auth                templates/custom_auth.yml
//	simple_get_object          templates/simple_get_object.yml
//	ndjson_response            templates/ndjson_response.yml
//	multi_mode_auth            templates/multi_mode_auth.yml
//	post_raw_body              templates/post_raw_body.yml
//	post_form_body             templates/post_form_body.yml
//	async_poll                 templates/async_poll.yml
//	etag_conditional           templates/etag_conditional.yml
//	next_url_in_body           templates/next_url_in_body.yml
//	post_json_body             templates/post_json_body.yml
//	scroll_id                  templates/scroll_id.yml
//	session_cookie             templates/session_cookie.yml
//
// Example — run all scenarios then exercise the cursor_token template:
//
//	go run ./cmd/testserver &
//	skopos template show cursor_token > /tmp/spec.yml
//	skopos run -i /tmp/spec.yml --once
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/p1llus/skopos/internal/testserver"
)

func main() {
	addr := flag.String("addr", ":9999", "listen address")
	pageSize := flag.Int("page-size", 2, "events per paginated page")
	eventsPerDrain := flag.Int("events-per-drain", 5, "events appended at drain start")
	only := flag.String("scenarios", "", "comma-separated scenario names to host (empty = all)")
	flag.Parse()

	opts := testserver.Options{
		PageSize:       *pageSize,
		EventsPerDrain: *eventsPerDrain,
		Now:            time.Now,
	}

	all := testserver.AllScenarios()
	scenarios := filterScenarios(all, *only)
	if len(scenarios) == 0 {
		fmt.Fprintln(os.Stderr, "testserver: no scenarios matched; check -scenarios flag")
		os.Exit(1)
	}

	srv := testserver.New(opts, scenarios...)

	httpSrv := &http.Server{
		Addr:    *addr,
		Handler: srv.Handler(),
	}

	// Graceful shutdown on SIGINT / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("testserver: listening on http://localhost%s", *addr)
		log.Printf("testserver: hosting %s", scenarioNames(scenarios))
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("testserver: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("testserver: shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		log.Printf("testserver: shutdown: %v", err)
	}
}

// filterScenarios returns the scenarios matching the comma-separated names in
// filter. When filter is empty all scenarios are returned.
func filterScenarios(all []testserver.Scenario, filter string) []testserver.Scenario {
	if filter == "" {
		return all
	}
	names := make(map[string]bool)
	for n := range strings.SplitSeq(filter, ",") {
		names[strings.TrimSpace(n)] = true
	}
	var out []testserver.Scenario
	for _, s := range all {
		if names[s.Name()] {
			out = append(out, s)
		}
	}
	return out
}

func scenarioNames(ss []testserver.Scenario) string {
	names := make([]string, len(ss))
	for i, s := range ss {
		names[i] = s.Name()
	}
	return strings.Join(names, ", ")
}
