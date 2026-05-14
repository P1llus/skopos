// SPDX-License-Identifier: Apache-2.0

package testserver_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/p1llus/skopos/internal/testserver"
)

func drainOffset(t *testing.T, client *http.Client, baseURL string, pageSize int) []string {
	t.Helper()
	var ids []string
	for offset := 0; ; offset += pageSize {
		u := fmt.Sprintf("%s/offset/findings?offset=%d&limit=%d", baseURL, offset, pageSize)
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
		items := body["items"].([]any)
		for _, e := range items {
			ids = append(ids, e.(map[string]any)["id"].(string))
		}
		// Short page = end of drain.
		if len(items) < pageSize {
			break
		}
	}
	return ids
}

func TestOffset_Pagination(t *testing.T) {
	ts := newTestServer(t, testserver.Offset())
	defer ts.Close()

	ids := drainOffset(t, ts.Client(), ts.URL, 2)
	if len(ids) != 5 {
		t.Errorf("got %d events, want 5", len(ids))
	}
}

func TestOffset_ReplenishOnSecondDrain(t *testing.T) {
	ts := newTestServer(t, testserver.Offset())
	defer ts.Close()

	first := drainOffset(t, ts.Client(), ts.URL, 2)
	second := drainOffset(t, ts.Client(), ts.URL, 2)

	if first[0] == second[0] {
		t.Errorf("second drain has same first event ID %q — replenish did not fire", first[0])
	}
}

func TestOffset_AuthRejection(t *testing.T) {
	ts := newTestServer(t, testserver.Offset())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/offset/findings?offset=0&limit=2", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ := ts.Client().Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
