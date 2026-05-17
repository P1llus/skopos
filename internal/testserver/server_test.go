// SPDX-License-Identifier: Apache-2.0

package testserver_test

import (
	"bytes"
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

// doPost performs an HTTP POST with Basic-Auth credentials and returns the
// decoded JSON response body.
func doPost(t *testing.T, client *http.Client, url, user, pass string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.SetBasicAuth(user, pass)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status %d", url, resp.StatusCode)
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestHandler_AllScenarios asserts that AllScenarios() registers all 20
// scenarios and that each entry point returns its expected status (not 404)
// with valid auth.
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
	noAuth := func(*http.Request) {}

	// wantStatus 0 means "expect 200".
	cases := []struct {
		method     string
		path       string
		auth       func(*http.Request)
		wantStatus int
	}{
		{http.MethodGet, "/bearer_simple/events", bearer, 0},
		{http.MethodGet, "/cursor_token/findings", bearer, 0},
		{http.MethodGet, "/page_number/findings", bearer, 0},
		{http.MethodGet, "/offset/findings", bearer, 0},
		{http.MethodGet, "/link_header/incidents", apiKey, 0},
		{http.MethodGet, "/oauth2/findings", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+accessToken)
		}, 0},
		{http.MethodGet, "/api_key_auth/detections", apiKey, 0},
		{http.MethodGet, "/basic_auth/data", func(r *http.Request) {
			r.SetBasicAuth(testserver.DefaultBasicUser, testserver.DefaultBasicPass)
		}, 0},
		{http.MethodGet, "/custom_auth/events", func(r *http.Request) {
			r.Header.Set("X-Custom-Auth", testserver.DefaultCustomAuth)
		}, 0},
		{http.MethodGet, "/simple_get_object/status", noAuth, 0},
		{http.MethodGet, "/ndjson_response/logs", bearer, 0},
		{http.MethodGet, "/multi_mode_auth/events", apiKey, 0},
		{http.MethodPost, "/post_raw_body/ingest", bearer, 0},
		{http.MethodPost, "/post_form_body/events", bearer, 0},
		{http.MethodPost, "/async_poll/exports", bearer, http.StatusAccepted},
		{http.MethodGet, "/etag_conditional/probe", noAuth, 0},
		{http.MethodGet, "/next_url_in_body/alerts", bearer, 0},
		{http.MethodPost, "/post_json_body/search", bearer, 0},
		{http.MethodGet, "/scroll_id/scroll", bearer, 0},
		{http.MethodPost, "/session_cookie/login", noAuth, 0},
		{http.MethodGet, "/fanout/incidents", bearer, 0},
	}

	for _, c := range cases {
		want := c.wantStatus
		if want == 0 {
			want = http.StatusOK
		}
		req, _ := http.NewRequest(c.method, ts.URL+c.path, nil)
		c.auth(req)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Errorf("%s %s: %v", c.method, c.path, err)
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s %s: status %d, want %d", c.method, c.path, resp.StatusCode, want)
		}
	}
}
