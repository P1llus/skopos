// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"encoding/json"
	"net/http"
)

type postJSONBodyScenario struct{}

// PostJSONBody returns the post_json_body scenario. It mounts a single
// POST /post_json_body/search handler with Bearer auth that reads the
// offset window (search_from / search_to) from its JSON request body and
// serves offset/limit pages: {"data": {"alerts": [...]}}.
//
// The page size is derived from search_to - search_from so it tracks the
// template's batch_size. A new drain starts when search_from is absent or 0.
func PostJSONBody() Scenario { return postJSONBodyScenario{} }

func (postJSONBodyScenario) Name() string { return "post_json_body" }

func (postJSONBodyScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/post_json_body/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}

		var body struct {
			SearchFrom int `json:"search_from"`
			SearchTo   int `json:"search_to"`
		}
		// An absent or unparseable body leaves the offsets at 0 — the
		// drain-start default.
		_ = json.NewDecoder(r.Body).Decode(&body)

		offset := body.SearchFrom
		if offset < 0 {
			offset = 0
		}
		limit := body.SearchTo - body.SearchFrom
		if limit <= 0 {
			limit = opts.PageSize
		}
		if offset == 0 {
			store.Replenish(opts.Now(), opts.EventsPerDrain)
		}

		page := store.Slice(offset, limit)
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"alerts": page,
			},
		})
	})
}
