// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type multiModeAuthScenario struct{}

// MultiModeAuth returns the multi_mode_auth scenario. It mounts a single
// GET /multi_mode_auth/events handler that accepts any of the three auth modes
// templates/multi_mode_auth.yml can dispatch to at runtime, each carrying
// DefaultAPIKey:
//
//   - Authorization: Bearer <key>   (the bearer branch)
//   - X-API-Key: <key>              (the api_key branch)
//   - X-Fallback-Auth: <key>        (the default arm)
//
// Pagination is none: every request is a fresh drain and replenish fires each
// time.
func MultiModeAuth() Scenario { return multiModeAuthScenario{} }

func (multiModeAuthScenario) Name() string { return "multi_mode_auth" }

func (multiModeAuthScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/multi_mode_auth/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !multiModeAuthorized(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"events": store.Slice(0, opts.EventsPerDrain),
		})
	})
}

// multiModeAuthorized reports whether r carries DefaultAPIKey via any of the
// three auth modes multi_mode_auth can select at runtime.
func multiModeAuthorized(r *http.Request) bool {
	switch {
	case r.Header.Get("Authorization") == "Bearer "+DefaultAPIKey:
		return true
	case r.Header.Get("X-API-Key") == DefaultAPIKey:
		return true
	case r.Header.Get("X-Fallback-Auth") == DefaultAPIKey:
		return true
	default:
		return false
	}
}
