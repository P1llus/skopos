// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

const (
	// DefaultBearer is the bearer token used by all bearer-auth scenarios.
	// It matches the `api_key.default` in the corresponding templates.
	DefaultBearer = "test-bearer-token-12345"

	// DefaultAPIKey is the API key used by the link_header scenario.
	// It matches the `api_key.default` in templates/link_header.yml.
	DefaultAPIKey = "test-api-key-67890"
)

// checkBearer returns true when the request carries the expected bearer token.
// On failure it writes a 401 and returns false.
func checkBearer(w http.ResponseWriter, r *http.Request, token string) bool {
	want := "Bearer " + token
	if r.Header.Get("Authorization") != want {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

// checkAPIKey returns true when the request carries the expected X-API-Key header.
// On failure it writes a 401 and returns false.
func checkAPIKey(w http.ResponseWriter, r *http.Request, key string) bool {
	if r.Header.Get("X-API-Key") != key {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}
