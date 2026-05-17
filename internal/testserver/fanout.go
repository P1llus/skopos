// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"net/http"
	"strings"
	"time"
)

type fanoutScenario struct{}

// Fanout returns the fanout scenario. It mounts:
//
//	GET /fanout/incidents              → list of incident objects with id+name
//	GET /fanout/incidents/{id}/events  → per-incident JSON list of events
//
// Bearer-auth on both endpoints. The fanout template lists incidents then
// fans out per-id to the events endpoint with merge: flatten so the merged
// body is one events stream.
func Fanout() Scenario { return fanoutScenario{} }

func (fanoutScenario) Name() string { return "fanout" }

func (fanoutScenario) Register(mux *http.ServeMux, opts Options) {
	// One backing event store per incident; replenished from a single drain
	// trigger so all incidents stay in step.
	incidents := []map[string]any{
		{"id": "inc-alpha", "name": "Alpha incident"},
		{"id": "inc-beta", "name": "Beta incident"},
		{"id": "inc-gamma", "name": "Gamma incident"},
	}
	stores := map[string]*EventStore{}
	for _, inc := range incidents {
		stores[inc["id"].(string)] = &EventStore{}
	}

	mux.HandleFunc("/fanout/incidents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}
		// New drain: replenish every per-incident store from the same clock
		// so each detail call below returns a fresh window.
		now := opts.Now()
		for _, s := range stores {
			s.Replenish(now, opts.EventsPerDrain)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"incidents": incidents,
		})
	})

	mux.HandleFunc("/fanout/incidents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkBearer(w, r, DefaultBearer) {
			return
		}
		// Path is /fanout/incidents/<id>/events.
		rest := strings.TrimPrefix(r.URL.Path, "/fanout/incidents/")
		id, tail, ok := strings.Cut(rest, "/")
		if !ok || tail != "events" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		s, ok := stores[id]
		if !ok {
			writeError(w, http.StatusNotFound, "no such incident")
			return
		}
		// Tag every event with the source incident so the merged stream is
		// distinguishable downstream.
		raw := s.Slice(0, opts.EventsPerDrain)
		out := make([]map[string]any, 0, len(raw))
		for _, ev := range raw {
			out = append(out, map[string]any{
				"id":         ev.ID,
				"incident":   id,
				"timestamp":  ev.Timestamp.UTC().Format(time.RFC3339),
				"seq_num":    ev.SeqNum,
			})
		}
		writeJSON(w, http.StatusOK, out)
	})
}
