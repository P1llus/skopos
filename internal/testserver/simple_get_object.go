// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type simpleGetObjectScenario struct{}

// SimpleGetObject returns the simple_get_object scenario. It mounts a single
// unauthenticated GET /simple_get_object/status handler whose body is one JSON
// object (not wrapped in a key) — the smallest end-to-end shape, matching
// templates/simple_get_object.yml's events_at: "". Every request is a fresh
// drain so the single event's ID advances each time.
func SimpleGetObject() Scenario { return simpleGetObjectScenario{} }

func (simpleGetObjectScenario) Name() string { return "simple_get_object" }

func (simpleGetObjectScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/simple_get_object/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		store.Replenish(opts.Now(), 1)
		writeJSON(w, http.StatusOK, store.Slice(0, 1)[0])
	})
}
