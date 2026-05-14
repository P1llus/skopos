// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"fmt"
	"net/http"
	"strconv"
)

type linkHeaderScenario struct{}

// LinkHeader returns the link_header scenario. It mounts
// GET /link_header/incidents with X-API-Key auth and RFC 5988 Link-header
// pagination. The Link header is omitted on the last page.
// A new drain starts when the cursor query param is absent or empty.
func LinkHeader() Scenario { return linkHeaderScenario{} }

func (linkHeaderScenario) Name() string { return "link_header" }

func (linkHeaderScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/link_header/incidents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkAPIKey(w, r, DefaultAPIKey) {
			return
		}

		// Reuse the "cursor" style: drain starts when cursor is absent/empty.
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

		if nextOffset < store.Len() {
			// Emit an RFC 5988 Link header pointing at the next page.
			// Use an absolute URL so the runner can follow it directly.
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			nextURL := fmt.Sprintf("%s://%s/link_header/incidents?cursor=%d",
				scheme, r.Host, nextOffset)
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, nextURL))
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"incidents": page,
		})
	})
}
