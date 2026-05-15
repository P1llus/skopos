// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
)

type sessionLoginCachedScenario struct{}

// SessionLoginCached returns the session_login_cached scenario. It mounts
// the testserver-side of templates/session_login_cached.yml: a custom JSON
// login endpoint plus a Bearer-protected events endpoint.
//
//   - POST /session_login_cached/login — accepts {"username": "admin",
//     "password": "test-password"}, increments a per-scenario counter, and
//     returns {"session_token": "session-token-NNNN", "expires_in": 3600}.
//     The login handler intentionally does NOT validate the Authorization
//     header; the runner's auth.bearer block applies to every request and
//     the first drain sends the {default: "pending"} placeholder.
//
//   - GET /session_login_cached/events — requires Bearer <currentToken>;
//     pagination.none, so each call is a drain start and the EventStore
//     replenishes. Events ride at body.events for the template's
//     events_at: events.
//
// The login counter is the cache-hit signal: drain 1 hits /login (counter
// goes 0→1); drain 2 must NOT hit /login (counter stays 1) when the
// runner's requests[].cache block is honoured. Goldens assert this by
// inspecting the persisted state file (the issued token is captured into
// state.session_token and survives across drains; the counter stays 0001).
func SessionLoginCached() Scenario { return sessionLoginCachedScenario{} }

func (sessionLoginCachedScenario) Name() string { return "session_login_cached" }

const (
	sessionLoginUser = "admin"
	sessionLoginPass = "test-password"
)

func (sessionLoginCachedScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}

	var loginCounter atomic.Int64
	var currentToken atomic.Value
	currentToken.Store("")

	mux.HandleFunc("/session_login_cached/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var creds struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
			writeError(w, http.StatusBadRequest, "bad json body")
			return
		}
		if creds.Username != sessionLoginUser || creds.Password != sessionLoginPass {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		n := loginCounter.Add(1)
		token := fmt.Sprintf("session-token-%04d", n)
		currentToken.Store(token)
		writeJSON(w, http.StatusOK, map[string]any{
			"session_token": token,
			"expires_in":    3600,
		})
	})

	mux.HandleFunc("/session_login_cached/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		tok, _ := currentToken.Load().(string)
		if tok == "" {
			writeError(w, http.StatusUnauthorized, "no token issued yet")
			return
		}
		if !checkBearer(w, r, tok) {
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		events := store.Slice(0, opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"events": events,
		})
	})
}
