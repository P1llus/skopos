// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type oauth2RelayScenario struct{}

// OAuth2Relay returns the oauth2_relay scenario. It mounts the
// testserver-side of schema/testdata/oauth2_relay.yml:
//
//   - POST /oauth2_relay/token — HTTP Basic test-client:test-secret,
//     form-urlencoded grant_type=client_credentials. Same response shape
//     as OAuth2ClientCredentials but keyed on a separate token store so
//     the two scenarios don't share state.
//   - POST /oauth2_relay/graphql — bearer-protected, JSON body. Drives
//     pagination.graphql_relay via variables.after (Relay endCursor) and
//     emits {data:{issues:{nodes,pageInfo}}} where nodes carry created_at
//     timestamps for progress.latest_event_timestamp.
//
// PageSize controls the node count per page; a 5-event drain at PageSize=2
// produces 3 pages (2+2+1) with hasNextPage=true on the first two responses.
func OAuth2Relay() Scenario { return oauth2RelayScenario{} }

func (oauth2RelayScenario) Name() string { return "oauth2_relay" }

// relayNode is the per-event wire shape for the oauth2_relay scenario. It
// is a parallel of testserver.Event but with created_at in place of
// timestamp so the fixture's progress.latest_event_timestamp path
// (created_at) resolves cleanly.
type relayNode struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	SeqNum    int       `json:"seq_num"`
}

func (oauth2RelayScenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	var tokenCounter atomic.Int64
	var currentToken atomic.Value
	currentToken.Store("")

	mux.HandleFunc("/oauth2_relay/token", func(w http.ResponseWriter, r *http.Request) {
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
		tok := fmt.Sprintf("relay-tok-%04d", n)
		currentToken.Store(tok)
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": tok,
			"token_type":   "bearer",
			"expires_in":   3600,
		})
	})

	mux.HandleFunc("/oauth2_relay/graphql", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
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

		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad json body")
			return
		}

		// after is either absent / nil / "" for the first iteration, or
		// the previous endCursor (encoded as "page:<offset>") for
		// subsequent pages.
		after := ""
		if s, ok := req.Variables["after"].(string); ok {
			after = s
		}
		offset := 0
		if after == "" {
			store.Replenish(opts.Now(), opts.EventsPerDrain)
		} else {
			const pfx = "page:"
			if strings.HasPrefix(after, pfx) {
				if n, err := parseInt(after[len(pfx):]); err == nil {
					offset = n
				}
			}
		}

		page := store.Slice(offset, opts.PageSize)
		nodes := make([]relayNode, 0, len(page))
		for _, e := range page {
			nodes = append(nodes, relayNode{
				ID:        e.ID,
				CreatedAt: e.Timestamp,
				SeqNum:    e.SeqNum,
			})
		}
		nextOffset := offset + opts.PageSize
		hasNext := nextOffset < store.Len()
		endCursor := ""
		if hasNext {
			endCursor = fmt.Sprintf("page:%d", nextOffset)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"nodes": nodes,
					"pageInfo": map[string]any{
						"hasNextPage": hasNext,
						"endCursor":   endCursor,
					},
				},
			},
		})
	})
}
