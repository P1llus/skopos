// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
)

type sessionCookieScenario struct{}

// SessionCookie returns the session_cookie scenario. It mounts the two-step
// login → data chain templates/session_cookie.yml drives, both
// unauthenticated at the transport layer:
//
//   - POST /session_cookie/login — mints a session, returned via
//     Set-Cookie: session=<id> (no cookie attributes, so the runner can
//     replay the header value verbatim).
//   - GET  /session_cookie/data  — requires the Cookie: session=<id> header
//     replayed from the login response; returns {"items": [...]}.
//
// Each login mints a fresh session id, so a second drain's data request
// carries a different cookie. Every data request is a fresh drain: replenish
// fires each time. A missing or unknown session cookie returns 401.
func SessionCookie() Scenario { return sessionCookieScenario{} }

func (sessionCookieScenario) Name() string { return "session_cookie" }

func (sessionCookieScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	var counter atomic.Int64

	var mu sync.Mutex
	valid := map[string]bool{}

	mux.HandleFunc("/session_cookie/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := fmt.Sprintf("sess-%06d", counter.Add(1))
		mu.Lock()
		valid[id] = true
		mu.Unlock()
		// A bare Name/Value cookie serialises to "session=<id>" with no
		// attributes — the runner replays that string verbatim as the
		// Cookie request header on the data step.
		http.SetCookie(w, &http.Cookie{Name: "session", Value: id})
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	mux.HandleFunc("/session_cookie/data", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c, err := r.Cookie("session")
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		mu.Lock()
		ok := valid[c.Value]
		mu.Unlock()
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"items": store.Slice(0, opts.EventsPerDrain),
		})
	})
}
