// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// Server holds the assembled mux and the registered scenarios.
type Server struct {
	mux       *http.ServeMux
	scenarios []Scenario
}

// New builds a Server by registering each scenario on a fresh ServeMux.
// opts is applied with defaults before being passed to each scenario.
func New(opts Options, ss ...Scenario) *Server {
	opts = opts.withDefaults()
	mux := http.NewServeMux()
	for _, s := range ss {
		s.Register(mux, opts)
	}
	return &Server{mux: mux, scenarios: ss}
}

// Handler returns an http.Handler that serves all registered scenarios and
// logs every request to the default logger.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.RequestURI())
		s.mux.ServeHTTP(w, r)
	})
}

// AllScenarios returns every registered scenario in declaration order: the
// six paginated/auth starter scenarios, the eight single-request
// pagination.none variations, then the six multi-step / body-cursor
// scenarios.
func AllScenarios() []Scenario {
	return []Scenario{
		BearerSimple(),
		CursorToken(),
		PageNumber(),
		Offset(),
		LinkHeader(),
		OAuth2ClientCredentials(),
		APIKeyAuth(),
		BasicAuth(),
		CustomAuth(),
		SimpleGetObject(),
		NDJSONResponse(),
		MultiModeAuth(),
		PostRawBody(),
		PostFormBody(),
		AsyncPoll(),
		EtagConditional(),
		NextURLInBody(),
		PostJSONBody(),
		ScrollID(),
		SessionCookie(),
		OAuth2PasswordGrant(),
		OAuth2Relay(),
		AsyncPollLatestTS(),
		AsyncPollStateless(),
		EtagConditionalMiddle(),
		SessionLoginCached(),
	}
}

// writeJSON writes a JSON-encoded body with the given status code.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("testserver: encode response: %v", err)
	}
}

// writeError writes a plain-text HTTP error.
func writeError(w http.ResponseWriter, status int, msg string) {
	http.Error(w, fmt.Sprintf("%d %s", status, msg), status)
}

// writeNDJSON writes events as newline-delimited JSON, one object per line.
func writeNDJSON(w http.ResponseWriter, status int, events []Event) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			log.Printf("testserver: encode ndjson: %v", err)
			return
		}
	}
}
