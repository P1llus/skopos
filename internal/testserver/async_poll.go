// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
)

type asyncPollScenario struct{}

// AsyncPoll returns the async_poll scenario. It mounts the three-step
// submit → poll → fetch chain templates/async_poll.yml drives, all
// authenticated by Bearer DefaultBearer:
//
//   - POST /async_poll/exports             — opens an export job; 202 + {"export_id": "..."}.
//   - GET  /async_poll/exports/{id}/status — job status; always reports
//     {"status": "complete", "result_url": "<absolute URL>"}.
//   - GET  /async_poll/results/{id}        — the export payload; {"items": [...]}.
//
// The status step reports the job complete immediately so a single
// `skopos run --once` walks the whole chain. Each result fetch is a fresh
// drain: replenish fires every time. Unknown export ids return 404.
func AsyncPoll() Scenario { return asyncPollScenario{} }

func (asyncPollScenario) Name() string { return "async_poll" }

func (asyncPollScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	var counter atomic.Int64

	var mu sync.Mutex
	issued := map[string]bool{}
	isIssued := func(id string) bool {
		mu.Lock()
		defer mu.Unlock()
		return issued[id]
	}

	mux.HandleFunc("/async_poll/exports", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}
		id := fmt.Sprintf("exp-%06d", counter.Add(1))
		mu.Lock()
		issued[id] = true
		mu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]any{"export_id": id})
	})

	mux.HandleFunc("/async_poll/exports/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}
		id := r.PathValue("id")
		if !isIssued(id) {
			writeError(w, http.StatusNotFound, "unknown export id")
			return
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "complete",
			"result_url": fmt.Sprintf("%s://%s/async_poll/results/%s", scheme, r.Host, id),
		})
	})

	mux.HandleFunc("/async_poll/results/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}
		if !isIssued(r.PathValue("id")) {
			writeError(w, http.StatusNotFound, "unknown export id")
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"items": store.Slice(0, opts.EventsPerDrain),
		})
	})
}
