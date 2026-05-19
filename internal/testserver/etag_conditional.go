// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"net/http"
	"strconv"
)

type etagConditionalScenario struct{}

// EtagConditional returns the etag_conditional scenario. It mounts the
// two-step probe → data chain templates/etag_conditional.yml drives, both
// unauthenticated:
//
//   - GET /etag_conditional/probe — unconditional probe step; returns {} so
//     the chain is genuinely multi-step.
//   - GET /etag_conditional/data  — page-number paginated data step; returns
//     {"items": [...], "meta": {"has_next": bool}}.
//
// A new drain starts when the page query param is absent or "1".
func EtagConditional() Scenario { return etagConditionalScenario{} }

func (etagConditionalScenario) Name() string { return "etag_conditional" }

func (etagConditionalScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}

	mux.HandleFunc("/etag_conditional/probe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	})

	mux.HandleFunc("/etag_conditional/data", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		if IsDrainStart("page", r) {
			store.Replenish(opts.Now(), opts.EventsPerDrain)
		}

		pageNum := 1
		if s := r.URL.Query().Get("page"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				pageNum = n
			}
		}

		offset := (pageNum - 1) * opts.PageSize
		page := store.Slice(offset, opts.PageSize)
		hasNext := (offset + opts.PageSize) < store.Len()

		// The template terminates on `not: {present: response.body.meta.has_next}`,
		// so the field is omitted on the final page.
		meta := map[string]any{}
		if hasNext {
			meta["has_next"] = true
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"items": page,
			"meta":  meta,
		})
	})
}
