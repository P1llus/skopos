// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

const (
	// DefaultBearer is the bearer token used by all bearer-auth scenarios.
	// It matches the `api_key.default` in the corresponding templates.
	DefaultBearer = "test-bearer-token-12345"

	// DefaultAPIKey is the API key used by the link_header, api_key_auth and
	// multi_mode_auth scenarios. It matches the `api_key.default` in the
	// corresponding templates.
	DefaultAPIKey = "test-api-key-67890"

	// DefaultBasicUser is the HTTP Basic username used by the basic_auth
	// scenario. It matches templates/basic_auth.yml.
	DefaultBasicUser = "testuser"
	// DefaultBasicPass is the HTTP Basic password used by the basic_auth
	// scenario. It matches templates/basic_auth.yml.
	DefaultBasicPass = "testpass"

	// DefaultCustomAuth is the value carried in the X-Custom-Auth header by
	// the custom_auth scenario. It matches templates/custom_auth.yml.
	DefaultCustomAuth = "custom-auth-value-99999"
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
	return checkHeader(w, r, "X-API-Key", key)
}

// checkHeader returns true when the request carries the expected value in the
// named header. On failure it writes a 401 and returns false.
func checkHeader(w http.ResponseWriter, r *http.Request, header, value string) bool {
	if r.Header.Get(header) != value {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

// checkBasic returns true when the request carries the expected HTTP Basic
// credentials. On failure it writes a 401 and returns false.
func checkBasic(w http.ResponseWriter, r *http.Request, user, pass string) bool {
	u, p, ok := r.BasicAuth()
	if !ok || u != user || p != pass {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}
