// SPDX-License-Identifier: Apache-2.0

package testserver_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/p1llus/skopos/internal/testserver"
)

func fetchOAuth2Token(t *testing.T, client *http.Client, baseURL string) string {
	t.Helper()
	body := doPost(t, client, baseURL+"/oauth2/token", "test-client", "test-secret")
	tok, _ := body["access_token"].(string)
	if tok == "" {
		t.Fatal("/oauth2/token: no access_token in response")
	}
	return tok
}

func drainOAuth2(t *testing.T, client *http.Client, baseURL, token string, wantPages int) []string {
	t.Helper()
	var ids []string
	cursor := ""
	pages := 0
	for {
		u := baseURL + "/oauth2/findings"
		if cursor != "" {
			u += "?cursor=" + cursor
		}
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d at %s", resp.StatusCode, u)
		}
		pages++
		for _, e := range body["findings"].([]any) {
			ids = append(ids, e.(map[string]any)["id"].(string))
		}
		next, _ := body["next_cursor"]
		if next == nil || next == "" {
			break
		}
		cursor = fmt.Sprintf("%v", next)
	}
	if pages != wantPages {
		t.Errorf("drained %d pages, want %d", pages, wantPages)
	}
	return ids
}

func TestOAuth2_TokenEndpoint(t *testing.T) {
	ts := newTestServer(t, testserver.OAuth2ClientCredentials())
	defer ts.Close()

	tok := fetchOAuth2Token(t, ts.Client(), ts.URL)
	if tok == "" {
		t.Error("expected non-empty access_token")
	}
}

func TestOAuth2_TokenEndpoint_WrongCredentials(t *testing.T) {
	ts := newTestServer(t, testserver.OAuth2ClientCredentials())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/oauth2/token", nil)
	req.SetBasicAuth("wrong", "creds")
	resp, _ := ts.Client().Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestOAuth2_Pagination(t *testing.T) {
	ts := newTestServer(t, testserver.OAuth2ClientCredentials())
	defer ts.Close()

	tok := fetchOAuth2Token(t, ts.Client(), ts.URL)
	ids := drainOAuth2(t, ts.Client(), ts.URL, tok, 3)
	if len(ids) != 5 {
		t.Errorf("got %d events, want 5", len(ids))
	}
}

func TestOAuth2_ReplenishOnSecondDrain(t *testing.T) {
	ts := newTestServer(t, testserver.OAuth2ClientCredentials())
	defer ts.Close()

	tok := fetchOAuth2Token(t, ts.Client(), ts.URL)
	first := drainOAuth2(t, ts.Client(), ts.URL, tok, 3)
	second := drainOAuth2(t, ts.Client(), ts.URL, tok, 3)

	if first[0] == second[0] {
		t.Errorf("second drain has same first event ID %q — replenish did not fire", first[0])
	}
}

func TestOAuth2_DataEndpoint_WrongToken(t *testing.T) {
	ts := newTestServer(t, testserver.OAuth2ClientCredentials())
	defer ts.Close()

	// First fetch a valid token (to prime the server's currentToken).
	fetchOAuth2Token(t, ts.Client(), ts.URL)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/oauth2/findings", nil)
	req.Header.Set("Authorization", "Bearer stale-token")
	resp, _ := ts.Client().Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
