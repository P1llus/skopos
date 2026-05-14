// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type basicAuthScenario struct{}

// BasicAuth returns the basic_auth scenario. It mounts a single
// GET /basic_auth/data handler authenticated by HTTP Basic
// (DefaultBasicUser / DefaultBasicPass). Pagination is none: every request is
// a fresh drain and replenish fires each time.
func BasicAuth() Scenario { return basicAuthScenario{} }

func (basicAuthScenario) Name() string { return "basic_auth" }

func (basicAuthScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/basic_auth/data", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBasic(w, r, DefaultBasicUser, DefaultBasicPass) {
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"items": store.Slice(0, opts.EventsPerDrain),
		})
	})
}
