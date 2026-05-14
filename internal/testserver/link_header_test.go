// SPDX-License-Identifier: Apache-2.0

package testserver_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/p1llus/skopos/internal/testserver"
)

func drainLinkHeader(t *testing.T, client *http.Client, startURL string) []string {
	t.Helper()
	var ids []string
	nextURL := startURL
	for nextURL != "" {
		req, _ := http.NewRequest(http.MethodGet, nextURL, nil)
		req.Header.Set("X-API-Key", testserver.DefaultAPIKey)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", nextURL, err)
		}
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d at %s", resp.StatusCode, nextURL)
		}
		for _, e := range body["incidents"].([]any) {
			ids = append(ids, e.(map[string]any)["id"].(string))
		}
		nextURL = parseLinkNext(resp.Header.Get("Link"))
	}
	return ids
}

// parseLinkNext extracts the URL from a Link: <url>; rel="next" header value.
func parseLinkNext(h string) string {
	if h == "" {
		return ""
	}
	// Simple parse: <url>; rel="next"
	if len(h) < 3 || h[0] != '<' {
		return ""
	}
	end := 1
	for end < len(h) && h[end] != '>' {
		end++
	}
	return h[1:end]
}

func TestLinkHeader_Pagination(t *testing.T) {
	ts := newTestServer(t, testserver.LinkHeader())
	defer ts.Close()

	ids := drainLinkHeader(t, ts.Client(), ts.URL+"/link_header/incidents")
	if len(ids) != 5 {
		t.Errorf("got %d events, want 5", len(ids))
	}
}

func TestLinkHeader_ReplenishOnSecondDrain(t *testing.T) {
	ts := newTestServer(t, testserver.LinkHeader())
	defer ts.Close()

	first := drainLinkHeader(t, ts.Client(), ts.URL+"/link_header/incidents")
	second := drainLinkHeader(t, ts.Client(), ts.URL+"/link_header/incidents")

	if first[0] == second[0] {
		t.Errorf("second drain has same first event ID %q — replenish did not fire", first[0])
	}
}

func TestLinkHeader_AuthRejection(t *testing.T) {
	ts := newTestServer(t, testserver.LinkHeader())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/link_header/incidents", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
