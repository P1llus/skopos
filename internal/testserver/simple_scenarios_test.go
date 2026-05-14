// SPDX-License-Identifier: Apache-2.0

package testserver_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/p1llus/skopos/internal/testserver"
)

// simpleCase describes a pagination.none scenario whose JSON response carries
// the event slice under a single key.
type simpleCase struct {
	name      string
	scenario  testserver.Scenario
	method    string
	path      string
	eventsKey string
	auth      func(*http.Request)
	badAuth   func(*http.Request)
}

func simpleCases() []simpleCase {
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testserver.DefaultBearer) }
	badBearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }
	return []simpleCase{
		{
			name: "api_key_auth", scenario: testserver.APIKeyAuth(),
			method: http.MethodGet, path: "/api_key_auth/detections", eventsKey: "resources",
			auth:    func(r *http.Request) { r.Header.Set("X-API-Key", testserver.DefaultAPIKey) },
			badAuth: func(r *http.Request) { r.Header.Set("X-API-Key", "wrong") },
		},
		{
			name: "basic_auth", scenario: testserver.BasicAuth(),
			method: http.MethodGet, path: "/basic_auth/data", eventsKey: "items",
			auth:    func(r *http.Request) { r.SetBasicAuth(testserver.DefaultBasicUser, testserver.DefaultBasicPass) },
			badAuth: func(r *http.Request) { r.SetBasicAuth("wrong", "creds") },
		},
		{
			name: "custom_auth", scenario: testserver.CustomAuth(),
			method: http.MethodGet, path: "/custom_auth/events", eventsKey: "events",
			auth:    func(r *http.Request) { r.Header.Set("X-Custom-Auth", testserver.DefaultCustomAuth) },
			badAuth: func(r *http.Request) { r.Header.Set("X-Custom-Auth", "wrong") },
		},
		{
			name: "multi_mode_auth", scenario: testserver.MultiModeAuth(),
			method: http.MethodGet, path: "/multi_mode_auth/events", eventsKey: "events",
			auth:    func(r *http.Request) { r.Header.Set("X-API-Key", testserver.DefaultAPIKey) },
			badAuth: func(r *http.Request) { r.Header.Set("X-API-Key", "wrong") },
		},
		{
			name: "post_raw_body", scenario: testserver.PostRawBody(),
			method: http.MethodPost, path: "/post_raw_body/ingest", eventsKey: "events",
			auth: bearer, badAuth: badBearer,
		},
		{
			name: "post_form_body", scenario: testserver.PostFormBody(),
			method: http.MethodPost, path: "/post_form_body/events", eventsKey: "events",
			auth: bearer, badAuth: badBearer,
		},
	}
}

func TestSimpleScenarios_ReturnEvents(t *testing.T) {
	for _, sc := range simpleCases() {
		t.Run(sc.name, func(t *testing.T) {
			ts := newTestServer(t, sc.scenario)
			defer ts.Close()

			req, _ := http.NewRequest(sc.method, ts.URL+sc.path, nil)
			sc.auth(req)
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", sc.method, sc.path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			events, ok := body[sc.eventsKey].([]any)
			if !ok || len(events) == 0 {
				t.Fatalf("body[%q] empty or wrong type: %v", sc.eventsKey, body[sc.eventsKey])
			}
		})
	}
}

func TestSimpleScenarios_AuthRejection(t *testing.T) {
	for _, sc := range simpleCases() {
		t.Run(sc.name, func(t *testing.T) {
			ts := newTestServer(t, sc.scenario)
			defer ts.Close()

			req, _ := http.NewRequest(sc.method, ts.URL+sc.path, nil)
			sc.badAuth(req)
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", sc.method, sc.path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
		})
	}
}

func TestSimpleScenarios_EachRequestReplenishes(t *testing.T) {
	for _, sc := range simpleCases() {
		t.Run(sc.name, func(t *testing.T) {
			ts := newTestServer(t, sc.scenario)
			defer ts.Close()

			firstID := func() string {
				req, _ := http.NewRequest(sc.method, ts.URL+sc.path, nil)
				sc.auth(req)
				resp, err := ts.Client().Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", sc.method, sc.path, err)
				}
				defer func() { _ = resp.Body.Close() }()
				var body map[string]any
				if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				return body[sc.eventsKey].([]any)[0].(map[string]any)["id"].(string)
			}

			if first, second := firstID(), firstID(); first == second {
				t.Errorf("second request returned same first ID %q — replenish did not fire", first)
			}
		})
	}
}

// TestMultiModeAuth_AcceptsAllModes asserts that every auth mode the
// multi_mode_auth template can dispatch to is accepted by the scenario.
func TestMultiModeAuth_AcceptsAllModes(t *testing.T) {
	ts := newTestServer(t, testserver.MultiModeAuth())
	defer ts.Close()

	modes := []struct{ header, value string }{
		{"Authorization", "Bearer " + testserver.DefaultAPIKey},
		{"X-API-Key", testserver.DefaultAPIKey},
		{"X-Fallback-Auth", testserver.DefaultAPIKey},
	}
	for _, m := range modes {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/multi_mode_auth/events", nil)
		req.Header.Set(m.header, m.value)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("GET with %s: %v", m.header, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("auth via %s: status = %d, want 200", m.header, resp.StatusCode)
		}
	}
}

// TestSimpleGetObject_ReturnsSingleObject asserts the body root is a single
// event object (events_at: "") rather than a wrapper map.
func TestSimpleGetObject_ReturnsSingleObject(t *testing.T) {
	ts := newTestServer(t, testserver.SimpleGetObject())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/simple_get_object/status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["id"]; !ok {
		t.Errorf("response missing 'id' field: %v", body)
	}
}

// TestNDJSONResponse_ReturnsJSONLines asserts the body is newline-delimited
// JSON with one decodable event object per line.
func TestNDJSONResponse_ReturnsJSONLines(t *testing.T) {
	ts := newTestServer(t, testserver.NDJSONResponse())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ndjson_response/logs", nil)
	req.Header.Set("Authorization", "Bearer "+testserver.DefaultBearer)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	lines := 0
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d is not valid JSON: %q", lines+1, line)
		}
		if _, ok := ev["id"]; !ok {
			t.Errorf("line %d missing 'id': %q", lines+1, line)
		}
		lines++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if lines != 5 {
		t.Errorf("got %d ndjson lines, want 5", lines)
	}
}

func TestNDJSONResponse_AuthRejection(t *testing.T) {
	ts := newTestServer(t, testserver.NDJSONResponse())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ndjson_response/logs", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
