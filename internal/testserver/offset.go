// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"net/http"
	"strconv"
)

type offsetScenario struct{}

// Offset returns the offset scenario. It mounts
// GET /offset/findings with bearer auth and offset/limit pagination.
// Termination fires when the returned page is shorter than limit.
// A new drain starts when offset is absent or "0".
func Offset() Scenario { return offsetScenario{} }

func (offsetScenario) Name() string { return "offset" }

func (offsetScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/offset/findings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}

		if IsDrainStart("offset", r) {
			store.Replenish(opts.Now(), opts.EventsPerDrain)
		}

		offset := 0
		if s := r.URL.Query().Get("offset"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				offset = n
			}
		}

		limit := opts.PageSize
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				limit = n
			}
		}

		page := store.Slice(offset, limit)
		writeJSON(w, http.StatusOK, map[string]any{
			"items": page,
		})
	})
}
