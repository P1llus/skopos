// Minimal HTTP stub for the session_login_cached template: a custom JSON
// login endpoint plus a Bearer-protected events endpoint.
//
//	go run ./examples/session_login_cached_mock
//
// Then: skopos template show session_login_cached > spec.yml
//
//	skopos validate -i spec.yml && skopos run -i spec.yml --once
//
// The login response carries a duration-shaped expires_in, so the runner's
// requests[].cache block caches the token: a second `skopos run` (within the
// token's lifetime) reuses it and the /login handler is NOT hit again.
package main

import (
	"encoding/json"
	"log"
	"net/http"
)

const (
	wantUser     = "admin"
	wantPassword = "test-password"
	sessionToken = "session-token-abc123"
)

func main() {
	http.HandleFunc("/api/v1/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var creds struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if creds.Username != wantUser || creds.Password != wantPassword {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		log.Printf("POST /api/v1/login → issuing session token (expires_in=3600)")

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"session_token": sessionToken,
			"expires_in":    3600,
		}); err != nil {
			log.Printf("encode: %v", err)
		}
	})

	http.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+sessionToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		log.Printf("GET /api/v1/events (cached token accepted)")

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
	log.Printf("session_login_cached_mock listening on http://localhost%s (Ctrl+C to stop)", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
