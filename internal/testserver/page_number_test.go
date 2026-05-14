// SPDX-License-Identifier: Apache-2.0

package testserver_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/p1llus/skopos/internal/testserver"
)

func drainPageNumber(t *testing.T, client *http.Client, baseURL string, wantPages int) []string {
	t.Helper()
	var ids []string
	for page := 1; ; page++ {
		u := fmt.Sprintf("%s/page_number/findings?page=%d", baseURL, page)
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		req.Header.Set("Authorization", "Bearer "+testserver.DefaultBearer)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		for _, e := range body["data"].([]any) {
			ids = append(ids, e.(map[string]any)["id"].(string))
		}
		meta := body["meta"].(map[string]any)
		if !meta["has_next"].(bool) {
			if page != wantPages {
				t.Errorf("drain stopped after %d pages, want %d", page, wantPages)
			}
			break
		}
	}
	return ids
}

func TestPageNumber_Pagination(t *testing.T) {
	ts := newTestServer(t, testserver.PageNumber())
	defer ts.Close()

	// 5 events / page-size 2 → 3 pages (2+2+1), last has has_next=false.
	ids := drainPageNumber(t, ts.Client(), ts.URL, 3)
	if len(ids) != 5 {
		t.Errorf("got %d events, want 5", len(ids))
	}
}

func TestPageNumber_ReplenishOnSecondDrain(t *testing.T) {
	ts := newTestServer(t, testserver.PageNumber())
	defer ts.Close()

	first := drainPageNumber(t, ts.Client(), ts.URL, 3)
	second := drainPageNumber(t, ts.Client(), ts.URL, 3)

	if first[0] == second[0] {
		t.Errorf("second drain has same first event ID %q — replenish did not fire", first[0])
	}
}

func TestPageNumber_AuthRejection(t *testing.T) {
	ts := newTestServer(t, testserver.PageNumber())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/page_number/findings?page=1", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
