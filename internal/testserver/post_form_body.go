// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type postFormBodyScenario struct{}

// PostFormBody returns the post_form_body scenario. It mounts a single
// POST /post_form_body/events handler with bearer auth that accepts an
// application/x-www-form-urlencoded request body. Pagination is none: every
// request is a fresh drain and replenish fires each time.
func PostFormBody() Scenario { return postFormBodyScenario{} }

func (postFormBodyScenario) Name() string { return "post_form_body" }

func (postFormBodyScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/post_form_body/events", func(w http.ResponseWriter, r *http.Request) {
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
