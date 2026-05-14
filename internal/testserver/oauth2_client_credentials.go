// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
)

type oauth2CCScenario struct{}

// OAuth2ClientCredentials returns the oauth2_client_credentials scenario.
// It mounts:
//
//   - POST /oauth2/token  — accepts HTTP Basic client_id:client_secret and
//     returns {"access_token": "...", "expires_in": 3600, "token_type": "bearer"}.
//   - GET  /oauth2/findings — requires the issued bearer token; uses cursor
//     pagination identical to the cursor_token scenario.
//
// Token values embed a monotonically increasing counter so each token-fetch
// produces a distinct (but deterministically checkable) token.
func OAuth2ClientCredentials() Scenario { return oauth2CCScenario{} }

func (oauth2CCScenario) Name() string { return "oauth2" }

func (oauth2CCScenario) Register(mux *http.ServeMux, opts Options) {
	var tokenCounter atomic.Int64
	store := &EventStore{}

	// Track the most-recently issued token so the data endpoint can verify it.
	var currentToken atomic.Value
	currentToken.Store("")

	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "test-client" || pass != "test-secret" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		n := tokenCounter.Add(1)
		token := fmt.Sprintf("oauth2-tok-%04d", n)
		currentToken.Store(token)
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": token,
			"token_type":   "bearer",
			"expires_in":   3600,
		})
	})

	mux.HandleFunc("/oauth2/findings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Validate the bearer token against the most-recently issued one.
		tok := currentToken.Load().(string)
		if tok == "" {
			writeError(w, http.StatusUnauthorized, "no token issued yet")
			return
		}
		authHeader := r.Header.Get("Authorization")
		wantPrefix := "Bearer "
		if !strings.HasPrefix(authHeader, wantPrefix) || authHeader[len(wantPrefix):] != tok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		cursor := r.URL.Query().Get("cursor")
		if IsDrainStart("cursor", r) {
			store.Replenish(opts.Now(), opts.EventsPerDrain)
		}

		offset := 0
		if cursor != "" {
			// cursor encodes "page:<offset>" same as cursor_token scenario.
			pfx := "page:"
			if strings.HasPrefix(cursor, pfx) {
				if n, err := parseInt(cursor[len(pfx):]); err == nil {
					offset = n
				}
			}
		}

		page := store.Slice(offset, opts.PageSize)
		nextOffset := offset + opts.PageSize
		var nextCursor *string
		if nextOffset < store.Len() {
			s := fmt.Sprintf("page:%d", nextOffset)
			nextCursor = &s
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"findings":    page,
			"next_cursor": nextCursor,
		})
	})
}

func parseInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a digit: %c", c)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
