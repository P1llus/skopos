// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"net/http"
	"strconv"
)

type pageNumberScenario struct{}

// PageNumber returns the page_number scenario. It mounts
// GET /page_number/findings with bearer auth and page-number pagination.
// The response carries {"data": [...], "meta": {"has_next": bool}}.
// A new drain starts when page is absent or "1".
func PageNumber() Scenario { return pageNumberScenario{} }

func (pageNumberScenario) Name() string { return "page_number" }

func (pageNumberScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/page_number/findings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}

		if IsDrainStart("page", r) {
			store.Replenish(opts.Now(), opts.EventsPerDrain)
		}

		pageStr := r.URL.Query().Get("page")
		pageNum := 1
		if pageStr != "" {
			if n, err := strconv.Atoi(pageStr); err == nil && n > 0 {
				pageNum = n
			}
		}

		offset := (pageNum - 1) * opts.PageSize
		page := store.Slice(offset, opts.PageSize)
		hasNext := (offset + opts.PageSize) < store.Len()

		writeJSON(w, http.StatusOK, map[string]any{
			"data": page,
			"meta": map[string]any{
				"has_next": hasNext,
			},
		})
	})
}
