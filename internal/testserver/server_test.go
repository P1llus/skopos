// SPDX-License-Identifier: Apache-2.0

package testserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/p1llus/skopos/internal/testserver"
)

func fixedNow() func() time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func newTestServer(t *testing.T, scenarios ...testserver.Scenario) *httptest.Server {
	t.Helper()
	opts := testserver.Options{
		PageSize:       2,
		EventsPerDrain: 5,
		Now:            fixedNow(),
	}
	srv := testserver.New(opts, scenarios...)
	return httptest.NewServer(srv.Handler())
}

func getJSON(t *testing.T, client *http.Client, url, authHeader, authValue string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if authHeader != "" {
		req.Header.Set(authHeader, authValue)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// TestHandler_UnknownPath asserts that requests to unregistered paths return
// 404 (default ServeMux behaviour).
func TestHandler_UnknownPath(t *testing.T) {
	ts := newTestServer(t, testserver.BearerSimple())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/unknown/path")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestHandler_AllScenarios asserts that AllScenarios() registers all 14
// scenarios and that each entry point returns 200 (not 404) with valid auth.
func TestHandler_AllScenarios(t *testing.T) {
	opts := testserver.Options{
		PageSize:       2,
		EventsPerDrain: 5,
		Now:            fixedNow(),
	}
	srv := testserver.New(opts, testserver.AllScenarios()...)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// oauth2 token endpoint must be hit first to get a valid bearer.
	tokenResp := doPost(t, ts.Client(), ts.URL+"/oauth2/token", "test-client", "test-secret")
	accessToken, _ := tokenResp["access_token"].(string)
	if accessToken == "" {
		t.Fatal("/oauth2/token returned no access_token")
	}

	bearer := func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+testserver.DefaultBearer)
	}
	apiKey := func(r *http.Request) {
		r.Header.Set("X-API-Key", testserver.DefaultAPIKey)
	}

	cases := []struct {
		method string
		path   string
		auth   func(*http.Request)
	}{
		{http.MethodGet, "/bearer_simple/events", bearer},
		{http.MethodGet, "/cursor_token/findings", bearer},
		{http.MethodGet, "/page_number/findings", bearer},
		{http.MethodGet, "/offset/findings", bearer},
		{http.MethodGet, "/link_header/incidents", apiKey},
		{http.MethodGet, "/oauth2/findings", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+accessToken)
		}},
		{http.MethodGet, "/api_key_auth/detections", apiKey},
		{http.MethodGet, "/basic_auth/data", func(r *http.Request) {
			r.SetBasicAuth(testserver.DefaultBasicUser, testserver.DefaultBasicPass)
		}},
		{http.MethodGet, "/custom_auth/events", func(r *http.Request) {
			r.Header.Set("X-Custom-Auth", testserver.DefaultCustomAuth)
		}},
		{http.MethodGet, "/simple_get_object/status", func(*http.Request) {}},
		{http.MethodGet, "/ndjson_response/logs", bearer},
		{http.MethodGet, "/multi_mode_auth/events", apiKey},
		{http.MethodPost, "/post_raw_body/ingest", bearer},
		{http.MethodPost, "/post_form_body/events", bearer},
	}

	for _, c := range cases {
		req, _ := http.NewRequest(c.method, ts.URL+c.path, nil)
		c.auth(req)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Errorf("%s %s: %v", c.method, c.path, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s: status %d, want 200", c.method, c.path, resp.StatusCode)
		}
	}
}
