// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type scrollIDScenario struct{}

// ScrollID returns the scroll_id scenario. It mounts
// GET /scroll_id/scroll with Bearer auth and scroll-session pagination.
//
// The first request (scroll query param absent or empty) opens a session and
// replenishes the store. Each response echoes the next scroll id at
// request_metadata.scroll — the id encodes the next page offset
// ("scroll-<offset>") — and sets request_metadata.complete to "true" on the
// final page so the template's complete_when predicate terminates the drain.
func ScrollID() Scenario { return scrollIDScenario{} }

func (scrollIDScenario) Name() string { return "scroll_id" }

func (scrollIDScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/scroll_id/scroll", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}

		// A new scroll session opens when the scroll id is absent or empty.
		scroll := r.URL.Query().Get("scroll")
		if scroll == "" {
			store.Replenish(opts.Now(), opts.EventsPerDrain)
		}

		// The scroll id encodes the page offset ("scroll-<offset>").
		offset := 0
		if scroll != "" {
			if n, err := strconv.Atoi(strings.TrimPrefix(scroll, "scroll-")); err == nil {
				offset = n
			}
		}

		page := store.Slice(offset, opts.PageSize)
		nextOffset := offset + opts.PageSize
		complete := "false"
		nextScroll := fmt.Sprintf("scroll-%d", nextOffset)
		if nextOffset >= store.Len() {
			// Final page: signal completion. The scroll id is preserved but
			// the template's complete_when predicate terminates the drain.
			complete = "true"
			nextScroll = scroll
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"events": page,
			"request_metadata": map[string]any{
				"scroll":   nextScroll,
				"complete": complete,
			},
		})
	})
}
