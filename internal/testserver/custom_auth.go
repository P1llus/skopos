// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type customAuthScenario struct{}

// CustomAuth returns the custom_auth scenario. It mounts a single
// GET /custom_auth/events handler authenticated by the operator-defined
// X-Custom-Auth header (value: DefaultCustomAuth). Pagination is none: every
// request is a fresh drain and replenish fires each time.
func CustomAuth() Scenario { return customAuthScenario{} }

func (customAuthScenario) Name() string { return "custom_auth" }

func (customAuthScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/custom_auth/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkHeader(w, r, "X-Custom-Auth", DefaultCustomAuth) {
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"events": store.Slice(0, opts.EventsPerDrain),
		})
	})
}
