// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"log"
	"net/http"
)

type gzipJSONScenario struct{}

// GzipJSON returns the gzip_json scenario. It mounts a single unauthenticated
// GET /gzip_json/events handler whose body is a gzip-compressed JSON object
// {"events": [...]}, served as an opaque file payload (Content-Type
// application/gzip, no Content-Encoding) so the HTTP transport does not
// transparently decompress it. Matches templates/gzip_json_response.yml's
// decode: [{gzip: {}}, {json: {}}]. Pagination is none.
func GzipJSON() Scenario { return gzipJSONScenario{} }

func (gzipJSONScenario) Name() string { return "gzip_json" }

func (gzipJSONScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	mux.HandleFunc("/gzip_json/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		body, err := json.Marshal(map[string]any{"events": store.Slice(0, opts.EventsPerDrain)})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "marshal")
			return
		}
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(body); err != nil {
			writeError(w, http.StatusInternalServerError, "gzip")
			return
		}
		if err := zw.Close(); err != nil {
			writeError(w, http.StatusInternalServerError, "gzip close")
			return
		}
		// Content-Encoding is deliberately NOT set: this is a file payload,
		// not transport compression, so net/http must leave it alone.
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(buf.Bytes()); err != nil {
			log.Printf("testserver: write gzip body: %v", err)
		}
	})
}
