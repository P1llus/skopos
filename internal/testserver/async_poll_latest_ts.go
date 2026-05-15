// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
)

type asyncPollLatestTSScenario struct{}

// AsyncPollLatestTS returns the async_poll_latest_ts scenario. It mounts
// the same submit → poll → fetch chain as AsyncPoll, but under the
// /async_poll_latest_ts/ prefix so the runtime can exercise
// schema/testdata/async_poll_latest_ts.yml (cursor_update.kind =
// latest_event_timestamp).
func AsyncPollLatestTS() Scenario { return asyncPollLatestTSScenario{} }

func (asyncPollLatestTSScenario) Name() string { return "async_poll_latest_ts" }

func (asyncPollLatestTSScenario) Register(mux *http.ServeMux, opts Options) {
	registerAsyncPollHandlers(mux, opts, "/async_poll_latest_ts")
}

// registerAsyncPollHandlers wires the submit / status / results endpoints
// of an async_job scenario under the given URL prefix. AsyncPoll,
// AsyncPollLatestTS, and AsyncPollStateless all share this handler shape;
// only the spec-level cursor_update differs.
func registerAsyncPollHandlers(mux *http.ServeMux, opts Options, prefix string) {
	store := &EventStore{}
	var counter atomic.Int64

	var mu sync.Mutex
	issued := map[string]bool{}
	isIssued := func(id string) bool {
		mu.Lock()
		defer mu.Unlock()
		return issued[id]
	}

	mux.HandleFunc(prefix+"/exports", func(w http.ResponseWriter, r *http.Request) {
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

	mux.HandleFunc(prefix+"/exports/{id}/status", func(w http.ResponseWriter, r *http.Request) {
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
			"result_url": fmt.Sprintf("%s://%s%s/results/%s", scheme, r.Host, prefix, id),
		})
	})

	mux.HandleFunc(prefix+"/results/{id}", func(w http.ResponseWriter, r *http.Request) {
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
