// SPDX-License-Identifier: Apache-2.0

package testserver_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/p1llus/skopos/internal/testserver"
)

// doPost performs a POST with HTTP Basic auth and returns the decoded JSON body.
func doPost(t *testing.T, client *http.Client, url, user, pass string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, nil)
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

func TestBearerSimple_ReturnsEvents(t *testing.T) {
	ts := newTestServer(t, testserver.BearerSimple())
	defer ts.Close()

	body := getJSON(t, ts.Client(), ts.URL+"/bearer_simple/events",
		"Authorization", "Bearer "+testserver.DefaultBearer)

	rawEvents, ok := body["events"]
	if !ok {
		t.Fatal("response missing 'events' key")
	}
	events, ok := rawEvents.([]any)
	if !ok || len(events) == 0 {
		t.Fatalf("events is empty or wrong type: %v", rawEvents)
	}
}

func TestBearerSimple_AuthRejection(t *testing.T) {
	ts := newTestServer(t, testserver.BearerSimple())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/bearer_simple/events", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestBearerSimple_EachRequestReplenishes(t *testing.T) {
	ts := newTestServer(t, testserver.BearerSimple())
	defer ts.Close()

	getIDs := func() []string {
		body := getJSON(t, ts.Client(), ts.URL+"/bearer_simple/events",
			"Authorization", "Bearer "+testserver.DefaultBearer)
		rawEvents := body["events"].([]any)
		ids := make([]string, 0, len(rawEvents))
		for _, e := range rawEvents {
			m := e.(map[string]any)
			ids = append(ids, m["id"].(string))
		}
		return ids
	}

	first := getIDs()
	second := getIDs()

	// Sequence numbers must increase: second drain has higher IDs.
	if len(first) == 0 || len(second) == 0 {
		t.Fatal("expected non-empty events on both drains")
	}
	if first[0] == second[0] {
		t.Errorf("second drain returned same first event ID %q — replenish did not fire", first[0])
	}
}
