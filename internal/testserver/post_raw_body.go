// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type postRawBodyScenario struct{}

// PostRawBody returns the post_raw_body scenario. It mounts a single
// POST /post_raw_body/ingest handler with bearer auth that accepts an
// arbitrary raw request body (the runner sends it verbatim). Pagination is
// none: every request is a fresh drain and replenish fires each time.
func PostRawBody() Scenario { return postRawBodyScenario{} }

func (postRawBodyScenario) Name() string { return "post_raw_body" }

func (postRawBodyScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/post_raw_body/ingest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"events": store.Slice(0, opts.EventsPerDrain),
		})
	})
}
