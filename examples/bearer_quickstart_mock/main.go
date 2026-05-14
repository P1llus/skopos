// Minimal HTTP stub for bearer_simple / root spec.yml: GET /api/v1/events with
// Bearer token and optional since/limit query (mirrors bundled template defaults).
//
//	go run ./examples/bearer_quickstart_mock
//
// Then: skopos validate -i spec.yml && skopos run -i spec.yml --once
package main

import (
	"encoding/json"
	"log"
	"net/http"
)

const wantBearer = "Bearer test-bearer-token-12345"

func main() {
	http.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != wantBearer {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		since := r.URL.Query().Get("since")
		limit := r.URL.Query().Get("limit")
		log.Printf("%s %s since=%q limit=%q\n", r.Method, r.URL.Path, since, limit)

		type event struct {
			ID        string `json:"id"`
			Timestamp string `json:"timestamp"`
		}
		body := map[string][]event{
			"events": {
				{ID: "evt-demo-1", Timestamp: "2026-05-14T12:00:00Z"},
				{ID: "evt-demo-2", Timestamp: "2026-05-14T12:05:00Z"},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			log.Printf("encode: %v", err)
		}
	})

	addr := ":9999"
	log.Printf("bearer_quickstart_mock listening on http://localhost%s (Ctrl+C to stop)", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
