// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type etagConditionalMiddleScenario struct{}

// EtagConditionalMiddle returns the etag_conditional_middle scenario. It
// mounts the testserver-side of schema/testdata/etag_conditional_middle.yml,
// a three-step chain where the conditional step sits in the middle:
//
//   - GET /etag_conditional_middle/probe    — unconditional probe, returns {}.
//   - GET /etag_conditional_middle/data     — the conditional middle step;
//     returns {"next_path": "/etag_conditional_middle/events"} so the final
//     step's select.branches arm picks the present-branch path.
//   - GET /etag_conditional_middle/events   — produces events; pagination.none
//     so every call is a fresh drain.
//   - GET /etag_conditional_middle/fallback — select.default arm of the
//     final step; same event shape so the skip-branch is also reachable.
func EtagConditionalMiddle() Scenario { return etagConditionalMiddleScenario{} }

func (etagConditionalMiddleScenario) Name() string { return "etag_conditional_middle" }

func (etagConditionalMiddleScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}

	mux.HandleFunc("/etag_conditional_middle/probe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	})

	mux.HandleFunc("/etag_conditional_middle/data", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"next_path": "/etag_conditional_middle/events",
		})
	})

	emitEvents := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"items": store.Slice(0, opts.EventsPerDrain),
		})
	}

	mux.HandleFunc("/etag_conditional_middle/events", emitEvents)
	mux.HandleFunc("/etag_conditional_middle/fallback", emitEvents)
}
