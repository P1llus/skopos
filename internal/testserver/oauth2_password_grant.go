// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
)

type oauth2PGScenario struct{}

// OAuth2PasswordGrant returns the oauth2_password_grant scenario. It
// mounts the testserver-side of schema/testdata/oauth2_password_grant.yml:
//
//   - POST /oauth2_password_grant/token — accepts form-urlencoded
//     grant_type=password&username=test-oauth2-user&password=test-oauth2-password,
//     returns {"access_token": "pwg-tok-NNNN", "token_type": "bearer", "expires_in": 3600}.
//   - GET  /oauth2_password_grant/events — bearer-protected; pagination.none,
//     replenishes the store on every request (single-drain semantics).
//
// The token endpoint counter ensures each token-fetch produces a distinct
// token; the data endpoint enforces the most-recently-issued one.
func OAuth2PasswordGrant() Scenario { return oauth2PGScenario{} }

func (oauth2PGScenario) Name() string { return "oauth2_password_grant" }

func (oauth2PGScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	var counter atomic.Int64
	var currentToken atomic.Value
	currentToken.Store("")

	mux.HandleFunc("/oauth2_password_grant/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "bad form")
			return
		}
		if r.PostForm.Get("grant_type") != "password" ||
			r.PostForm.Get("username") != "test-oauth2-user" ||
			r.PostForm.Get("password") != "test-oauth2-password" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		n := counter.Add(1)
		tok := fmt.Sprintf("pwg-tok-%04d", n)
		currentToken.Store(tok)
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": tok,
			"token_type":   "bearer",
			"expires_in":   3600,
		})
	})

	mux.HandleFunc("/oauth2_password_grant/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		tok, _ := currentToken.Load().(string)
		authHeader := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if tok == "" || !strings.HasPrefix(authHeader, prefix) || authHeader[len(prefix):] != tok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"events": store.Slice(0, opts.EventsPerDrain),
		})
	})
}
