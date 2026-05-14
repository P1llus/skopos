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

// TestHandler_AllScenarios asserts that AllScenarios() registers at least
// 6 scenarios and that each root GET returns 200 or 401 (not 404).
func TestHandler_AllScenarios(t *testing.T) {
	opts := testserver.Options{
		PageSize:       2,
		EventsPerDrain: 5,
		Now:            fixedNow(),
	}
	srv := testserver.New(opts, testserver.AllScenarios()...)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	paths := []struct {
		path   string
		header string
		value  string
	}{
		{"/bearer_simple/events", "Authorization", "Bearer " + testserver.DefaultBearer},
		{"/cursor_token/findings", "Authorization", "Bearer " + testserver.DefaultBearer},
		{"/page_number/findings", "Authorization", "Bearer " + testserver.DefaultBearer},
		{"/offset/findings", "Authorization", "Bearer " + testserver.DefaultBearer},
		{"/link_header/incidents", "X-API-Key", testserver.DefaultAPIKey},
		{"/oauth2/findings", "Authorization", "Bearer test-oauth2-token"},
	}

	// oauth2 token endpoint must be hit first to get a valid bearer.
	tokenResp := doPost(t, ts.Client(), ts.URL+"/oauth2/token", "test-client", "test-secret")
	accessToken, _ := tokenResp["access_token"].(string)
	if accessToken == "" {
		t.Fatal("/oauth2/token returned no access_token")
	}
	paths[5].value = "Bearer " + accessToken

	for _, p := range paths {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+p.path, nil)
		req.Header.Set(p.header, p.value)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Errorf("GET %s: %v", p.path, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", p.path, resp.StatusCode)
		}
	}
}
