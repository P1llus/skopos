// SPDX-License-Identifier: Apache-2.0

package testserver_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/p1llus/skopos/internal/testserver"
)

// drainCursorToken follows cursor_token pagination to completion and returns
// all collected event IDs. It asserts page count equals wantPages.
func drainCursorToken(t *testing.T, client *http.Client, baseURL string, wantPages int) []string {
	t.Helper()
	var ids []string
	cursor := ""
	pages := 0
	for {
		u := baseURL + "/cursor_token/findings"
		if cursor != "" {
			u += "?cursor=" + cursor
		}
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		req.Header.Set("Authorization", "Bearer "+testserver.DefaultBearer)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		pages++
		for _, e := range body["findings"].([]any) {
			ids = append(ids, e.(map[string]any)["id"].(string))
		}
		next := body["next_cursor"]
		if next == nil || next == "" {
			break
		}
		cursor = next.(string)
	}
	if pages != wantPages {
		t.Errorf("drained %d pages, want %d", pages, wantPages)
	}
	return ids
}

func TestCursorToken_Pagination(t *testing.T) {
	ts := newTestServer(t, testserver.CursorToken())
	defer ts.Close()

	// 5 events / page-size 2 → 3 pages (2+2+1).
	ids := drainCursorToken(t, ts.Client(), ts.URL, 3)
	if len(ids) != 5 {
		t.Errorf("got %d events, want 5", len(ids))
	}
}

func TestCursorToken_ReplenishOnSecondDrain(t *testing.T) {
	ts := newTestServer(t, testserver.CursorToken())
	defer ts.Close()

	first := drainCursorToken(t, ts.Client(), ts.URL, 3)
	second := drainCursorToken(t, ts.Client(), ts.URL, 3)

	if first[0] == second[0] {
		t.Errorf("second drain has same first event ID %q — replenish did not fire", first[0])
	}
}

func TestCursorToken_AuthRejection(t *testing.T) {
	ts := newTestServer(t, testserver.CursorToken())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/cursor_token/findings", nil)
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
