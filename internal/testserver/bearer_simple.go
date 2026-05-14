// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type bearerSimpleScenario struct{}

// BearerSimple returns the bearer_simple scenario. It mounts a single
// GET /bearer_simple/events handler; auth is Bearer test-bearer-token-12345.
// No pagination: every request is a fresh drain and replenish fires each time.
func BearerSimple() Scenario { return bearerSimpleScenario{} }

func (bearerSimpleScenario) Name() string { return "bearer_simple" }

func (bearerSimpleScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/bearer_simple/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}
		// No pagination: every request is a drain start.
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		events := store.Slice(0, opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"events": events,
		})
	})
}
