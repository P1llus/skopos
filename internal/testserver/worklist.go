// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type worklistScenario struct{}

// Worklist returns the worklist scenario. It mounts two bearer-authed
// endpoints with different wire encodings, so a spec mixes decode chains
// across steps:
//
//	GET /worklist/manifest?cursor=<c>  → plain JSON {items, next_cursor, has_more}
//	GET /worklist/items/{id}           → gzip-compressed CSV of that item's events
//
// The manifest is plain JSON (no decode chain); the items endpoint is a
// gzip-of-CSV file payload (Content-Type application/gzip, no
// Content-Encoding), drained with a decode: [{gzip: {}}, {csv: {header:
// present}}] chain. The manifest is a two-page listing keyed off the cursor
// query parameter (stateless, so the scenario is safe to re-run): the first
// page lists item-a + item-b and hands back cursor-2; the second lists item-c
// and reports has_more=false. The items endpoint returns two CSV rows per
// item. A worklist spec fills state.queue from the manifest, fetches one item
// per pagination iteration, and slices the consumed head off the queue.
func Worklist() Scenario { return worklistScenario{} }

func (worklistScenario) Name() string { return "worklist" }

// worklistPages is the manifest, keyed by the incoming cursor. The empty
// cursor is the first page.
var worklistPages = map[string]map[string]any{
	"": {
		"items":       []string{"item-a", "item-b"},
		"next_cursor": "cursor-2",
		"has_more":    true,
	},
	"cursor-2": {
		"items":       []string{"item-c"},
		"next_cursor": "",
		"has_more":    false,
	},
}

func (worklistScenario) Register(mux *http.ServeMux, opts Options) {
	mux.HandleFunc("/worklist/manifest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}
		page, ok := worklistPages[r.URL.Query().Get("cursor")]
		if !ok {
			page = map[string]any{"items": []string{}, "next_cursor": "", "has_more": false}
		}
		writeJSON(w, http.StatusOK, page)
	})

	mux.HandleFunc("/worklist/items/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/worklist/items/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		now := opts.Now()
		rows := make([][]string, 0, 2)
		for i := range 2 {
			rows = append(rows, []string{
				fmt.Sprintf("%s-evt-%d", id, i+1),
				id,
				strconv.Itoa(i + 1),
				now.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339),
			})
		}
		writeGzipCSV(w, []string{"id", "item", "seq_num", "timestamp"}, rows)
	})
}
