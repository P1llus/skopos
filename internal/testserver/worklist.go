// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type worklistScenario struct{}

// Worklist returns the worklist scenario. It mounts two bearer-authed
// endpoints whose bodies are gzip-compressed JSON file payloads (Content-Type
// application/gzip, no Content-Encoding), so a spec drains them with a
// decode: [{gzip: {}}, {json: {}}] chain:
//
//	GET /worklist/manifest?cursor=<c>  → {items: [...], next_cursor, has_more}
//	GET /worklist/items/{id}           → JSON array of that item's events
//
// The manifest is a two-page listing keyed off the cursor query parameter
// (stateless, so the scenario is safe to re-run): the first page lists
// item-a + item-b and hands back cursor-2; the second lists item-c and
// reports has_more=false. The items endpoint returns two events per item.
// A worklist spec fills state.queue from the manifest, fetches one item per
// pagination iteration, and slices the consumed head off the queue.
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
		writeGzipJSON(w, page)
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
		events := make([]map[string]any, 0, 2)
		for i := range 2 {
			events = append(events, map[string]any{
				"id":        fmt.Sprintf("%s-evt-%d", id, i+1),
				"item":      id,
				"seq_num":   i + 1,
				"timestamp": now.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339),
			})
		}
		writeGzipJSON(w, events)
	})
}

// writeGzipJSON marshals body to JSON, gzip-compresses it, and writes it as a
// file payload: Content-Type application/gzip with no Content-Encoding, so the
// HTTP transport does not transparently decompress it and the spec's decode
// chain owns the decompression.
func writeGzipJSON(w http.ResponseWriter, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal")
		return
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		writeError(w, http.StatusInternalServerError, "gzip")
		return
	}
	if err := zw.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "gzip close")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("testserver: write gzip body: %v", err)
	}
}
