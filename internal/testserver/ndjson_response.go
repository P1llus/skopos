// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type ndjsonResponseScenario struct{}

// NDJSONResponse returns the ndjson_response scenario. It mounts a single
// GET /ndjson_response/logs handler with bearer auth whose body is
// newline-delimited JSON — one event object per line, matching
// templates/ndjson_response.yml's decode: ndjson. Pagination is none: every
// request is a fresh drain and replenish fires each time.
func NDJSONResponse() Scenario { return ndjsonResponseScenario{} }

func (ndjsonResponseScenario) Name() string { return "ndjson_response" }

func (ndjsonResponseScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/ndjson_response/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		writeNDJSON(w, http.StatusOK, store.Slice(0, opts.EventsPerDrain))
	})
}
