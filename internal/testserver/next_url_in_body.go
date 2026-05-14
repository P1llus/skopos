// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"fmt"
	"net/http"
	"strconv"
)

type nextURLInBodyScenario struct{}

// NextURLInBody returns the next_url_in_body scenario. It mounts a single
// GET /next_url_in_body/alerts handler with Bearer auth whose body carries
// the next-page URL at meta.next_page — an absolute URL while more pages
// remain, null on the final page. templates/next_url_in_body.yml follows it
// via {ref: cursor.next_url} in the request's url slot.
//
// Pagination mirrors the link_header scenario: the page offset rides in the
// cursor query param the server embeds into meta.next_page. A new drain
// starts when the cursor query param is absent or empty, and replenish fires
// then.
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

		// Reuse the cursor-style drain detection: a new drain starts when the
		// cursor query param is absent or empty.
		if IsDrainStart("cursor", r) {
			store.Replenish(opts.Now(), opts.EventsPerDrain)
		}

		offset := 0
		if s := r.URL.Query().Get("cursor"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				offset = n
			}
		}

		page := store.Slice(offset, opts.PageSize)
		nextOffset := offset + opts.PageSize

		// meta.next_page carries the absolute URL of the next page, or null on
		// the final page so the template's next_url_in_body pagination
		// terminates the drain.
		var nextPage any
		if nextOffset < store.Len() {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			nextPage = fmt.Sprintf("%s://%s/next_url_in_body/alerts?cursor=%d",
				scheme, r.Host, nextOffset)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"alerts": page,
			"meta": map[string]any{
				"next_page": nextPage,
			},
		})
	})
}
