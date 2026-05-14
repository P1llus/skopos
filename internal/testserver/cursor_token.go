// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"fmt"
	"net/http"
	"strconv"
)

type cursorTokenScenario struct{}

// CursorToken returns the cursor_token scenario. It mounts
// GET /cursor_token/findings with bearer auth and cursor-token pagination.
//
// The server uses a page-offset encoded into the cursor value ("page:<n>") so
// the runner can replay mid-drain cursors correctly. A new drain starts when
// the cursor param is absent or empty.
func CursorToken() Scenario { return cursorTokenScenario{} }

func (cursorTokenScenario) Name() string { return "cursor_token" }

func (cursorTokenScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/cursor_token/findings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}

		cursor := r.URL.Query().Get("cursor")
		if IsDrainStart("cursor", r) {
			store.Replenish(opts.Now(), opts.EventsPerDrain)
		}

		// Decode page offset from cursor ("page:<offset>") or default to 0.
		offset := 0
		if cursor != "" {
			if n, err := strconv.Atoi(cursor[len("page:"):]); err == nil {
				offset = n
			}
		}

		page := store.Slice(offset, opts.PageSize)
		nextOffset := offset + opts.PageSize
		var nextCursor *string
		if nextOffset < store.Len() {
			s := fmt.Sprintf("page:%d", nextOffset)
			nextCursor = &s
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"findings":    page,
			"next_cursor": nextCursor,
		})
	})
}
