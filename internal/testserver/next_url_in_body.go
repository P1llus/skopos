// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type nextURLInBodyScenario struct{}

// NextURLInBody returns the next_url_in_body scenario. It mounts a single
// GET /next_url_in_body/alerts handler with Bearer auth whose body carries
// the next-page URL at meta.next_page. templates/next_url_in_body.yml issues
// the same request slot for every page, so the scenario returns one full
// window per request with meta.next_page null — every request is a fresh
// drain and replenish fires each time.
func NextURLInBody() Scenario { return nextURLInBodyScenario{} }

func (nextURLInBodyScenario) Name() string { return "next_url_in_body" }

func (nextURLInBodyScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/next_url_in_body/alerts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"alerts": store.Slice(0, opts.EventsPerDrain),
			"meta": map[string]any{
				"next_page": nil,
			},
		})
	})
}
