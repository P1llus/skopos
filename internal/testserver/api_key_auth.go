// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type apiKeyAuthScenario struct{}

// APIKeyAuth returns the api_key_auth scenario. It mounts a single
// GET /api_key_auth/detections handler authenticated by the X-API-Key header
// (value: DefaultAPIKey). Pagination is none: every request is a fresh drain
// and replenish fires each time.
func APIKeyAuth() Scenario { return apiKeyAuthScenario{} }

func (apiKeyAuthScenario) Name() string { return "api_key_auth" }

func (apiKeyAuthScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/api_key_auth/detections", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkAPIKey(w, r, DefaultAPIKey) {
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"resources": store.Slice(0, opts.EventsPerDrain),
		})
	})
}
