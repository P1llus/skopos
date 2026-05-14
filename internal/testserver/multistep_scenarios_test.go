// SPDX-License-Identifier: Apache-2.0

package testserver_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/p1llus/skopos/internal/testserver"
)

// bearerHeader is the Authorization value the bearer-auth scenarios expect.
func bearerHeader() string { return "Bearer " + testserver.DefaultBearer }

// doJSON performs an HTTP request with the given headers and JSON body (nil
// for no body), asserts the status code, and returns the decoded JSON body.
func doJSON(t *testing.T, client *http.Client, method, url string, headers map[string]string, body any, wantStatus int) map[string]any {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: status %d, want %d", method, url, resp.StatusCode, wantStatus)
	}
	var out map[string]any
	if resp.ContentLength != 0 {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode %s %s: %v", method, url, err)
		}
	}
	return out
}

// statusOnly performs a request and returns just the status code.
func statusOnly(t *testing.T, client *http.Client, method, url string, headers map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// ---- async_poll ----

// runAsyncPollChain walks submit → poll → fetch once and returns the fetched
// event ids.
func runAsyncPollChain(t *testing.T, ts *http.Client, baseURL string) []string {
	t.Helper()
	auth := map[string]string{"Authorization": bearerHeader()}

	submit := doJSON(t, ts, http.MethodPost, baseURL+"/async_poll/exports", auth, nil, http.StatusAccepted)
	exportID, _ := submit["export_id"].(string)
	if exportID == "" {
		t.Fatal("submit returned no export_id")
	}

	status := doJSON(t, ts, http.MethodGet, baseURL+"/async_poll/exports/"+exportID+"/status", auth, nil, http.StatusOK)
	if status["status"] != "complete" {
		t.Fatalf("poll status = %v, want complete", status["status"])
	}
	resultURL, _ := status["result_url"].(string)
	if resultURL == "" {
		t.Fatal("poll returned no result_url")
	}

	fetch := doJSON(t, ts, http.MethodGet, resultURL, auth, nil, http.StatusOK)
	items, ok := fetch["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("fetch items empty or wrong type: %v", fetch["items"])
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.(map[string]any)["id"].(string))
	}
	return ids
}

func TestAsyncPoll_FullChain(t *testing.T) {
	ts := newTestServer(t, testserver.AsyncPoll())
	defer ts.Close()

	ids := runAsyncPollChain(t, ts.Client(), ts.URL)
	if len(ids) != 5 {
		t.Errorf("got %d events, want 5", len(ids))
	}
}

func TestAsyncPoll_ReplenishOnSecondChain(t *testing.T) {
	ts := newTestServer(t, testserver.AsyncPoll())
	defer ts.Close()

	first := runAsyncPollChain(t, ts.Client(), ts.URL)
	second := runAsyncPollChain(t, ts.Client(), ts.URL)
	if first[0] == second[0] {
		t.Errorf("second chain returned same first event ID %q — replenish did not fire", first[0])
	}
}

func TestAsyncPoll_AuthRejection(t *testing.T) {
	ts := newTestServer(t, testserver.AsyncPoll())
	defer ts.Close()

	got := statusOnly(t, ts.Client(), http.MethodPost, ts.URL+"/async_poll/exports",
		map[string]string{"Authorization": "Bearer wrong"})
	if got != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", got)
	}
}

func TestAsyncPoll_UnknownExportID(t *testing.T) {
	ts := newTestServer(t, testserver.AsyncPoll())
	defer ts.Close()

	auth := map[string]string{"Authorization": bearerHeader()}
	if got := statusOnly(t, ts.Client(), http.MethodGet, ts.URL+"/async_poll/exports/bogus/status", auth); got != http.StatusNotFound {
		t.Errorf("status endpoint: status = %d, want 404", got)
	}
	if got := statusOnly(t, ts.Client(), http.MethodGet, ts.URL+"/async_poll/results/bogus", auth); got != http.StatusNotFound {
		t.Errorf("results endpoint: status = %d, want 404", got)
	}
}

// ---- etag_conditional ----

// drainEtag walks the probe step then the page-number-paginated data step
// until meta.has_next is false, returning the event ids.
func drainEtag(t *testing.T, client *http.Client, baseURL string) []string {
	t.Helper()
	// Probe step — unauthenticated, must return decodable JSON.
	doJSON(t, client, http.MethodGet, baseURL+"/etag_conditional/probe", nil, nil, http.StatusOK)

	var ids []string
	page := 1
	for {
		url := baseURL + "/etag_conditional/data?page=" + strconv.Itoa(page)
		body := doJSON(t, client, http.MethodGet, url, nil, nil, http.StatusOK)
		for _, e := range body["items"].([]any) {
			ids = append(ids, e.(map[string]any)["id"].(string))
		}
		meta := body["meta"].(map[string]any)
		if hasNext, _ := meta["has_next"].(bool); !hasNext {
			break
		}
		page++
	}
	return ids
}

func TestEtagConditional_Pagination(t *testing.T) {
	ts := newTestServer(t, testserver.EtagConditional())
	defer ts.Close()

	ids := drainEtag(t, ts.Client(), ts.URL)
	if len(ids) != 5 {
		t.Errorf("got %d events, want 5", len(ids))
	}
}

func TestEtagConditional_ReplenishOnSecondDrain(t *testing.T) {
	ts := newTestServer(t, testserver.EtagConditional())
	defer ts.Close()

	first := drainEtag(t, ts.Client(), ts.URL)
	second := drainEtag(t, ts.Client(), ts.URL)
	if first[0] == second[0] {
		t.Errorf("second drain returned same first event ID %q — replenish did not fire", first[0])
	}
}

// ---- next_url_in_body ----

func TestNextURLInBody_ReturnsEventsAndNullNext(t *testing.T) {
	ts := newTestServer(t, testserver.NextURLInBody())
	defer ts.Close()

	body := getJSON(t, ts.Client(), ts.URL+"/next_url_in_body/alerts", "Authorization", bearerHeader())
	alerts, ok := body["alerts"].([]any)
	if !ok || len(alerts) == 0 {
		t.Fatalf("alerts empty or wrong type: %v", body["alerts"])
	}
	meta, ok := body["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta missing or wrong type: %v", body["meta"])
	}
	if meta["next_page"] != nil {
		t.Errorf("meta.next_page = %v, want nil", meta["next_page"])
	}
}

func TestNextURLInBody_AuthRejection(t *testing.T) {
	ts := newTestServer(t, testserver.NextURLInBody())
	defer ts.Close()

	got := statusOnly(t, ts.Client(), http.MethodGet, ts.URL+"/next_url_in_body/alerts",
		map[string]string{"Authorization": "Bearer wrong"})
	if got != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", got)
	}
}

func TestNextURLInBody_EachRequestReplenishes(t *testing.T) {
	ts := newTestServer(t, testserver.NextURLInBody())
	defer ts.Close()

	firstID := func() string {
		body := getJSON(t, ts.Client(), ts.URL+"/next_url_in_body/alerts", "Authorization", bearerHeader())
		return body["alerts"].([]any)[0].(map[string]any)["id"].(string)
	}
	if a, b := firstID(), firstID(); a == b {
		t.Errorf("second request returned same first ID %q — replenish did not fire", a)
	}
}

// ---- post_json_body ----

// drainPostJSON walks the offset-paginated POST search endpoint, requesting
// pages of size 5 (matching the template's batch_size), and returns the
// event ids.
func drainPostJSON(t *testing.T, client *http.Client, baseURL string) []string {
	t.Helper()
	auth := map[string]string{"Authorization": bearerHeader()}
	const batch = 5
	var ids []string
	offset := 0
	for {
		reqBody := map[string]any{"search_from": offset, "search_to": offset + batch}
		body := doJSON(t, client, http.MethodPost, baseURL+"/post_json_body/search", auth, reqBody, http.StatusOK)
		// An exhausted offset window encodes as "alerts": null; the comma-ok
		// form leaves alerts as a nil slice so the short-page check ends the walk.
		alerts, _ := body["data"].(map[string]any)["alerts"].([]any)
		for _, e := range alerts {
			ids = append(ids, e.(map[string]any)["id"].(string))
		}
		if len(alerts) < batch {
			break
		}
		offset += batch
	}
	return ids
}

func TestPostJSONBody_Pagination(t *testing.T) {
	ts := newTestServer(t, testserver.PostJSONBody())
	defer ts.Close()

	ids := drainPostJSON(t, ts.Client(), ts.URL)
	if len(ids) != 5 {
		t.Errorf("got %d events, want 5", len(ids))
	}
}

func TestPostJSONBody_AuthRejection(t *testing.T) {
	ts := newTestServer(t, testserver.PostJSONBody())
	defer ts.Close()

	got := statusOnly(t, ts.Client(), http.MethodPost, ts.URL+"/post_json_body/search",
		map[string]string{"Authorization": "Bearer wrong"})
	if got != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", got)
	}
}

func TestPostJSONBody_ReplenishOnSecondDrain(t *testing.T) {
	ts := newTestServer(t, testserver.PostJSONBody())
	defer ts.Close()

	first := drainPostJSON(t, ts.Client(), ts.URL)
	second := drainPostJSON(t, ts.Client(), ts.URL)
	if first[0] == second[0] {
		t.Errorf("second drain returned same first event ID %q — replenish did not fire", first[0])
	}
}

// ---- scroll_id ----

// drainScroll opens a scroll session and follows request_metadata.scroll
// until request_metadata.complete is "true", returning the event ids.
func drainScroll(t *testing.T, client *http.Client, baseURL string) []string {
	t.Helper()
	auth := map[string]string{"Authorization": bearerHeader()}
	var ids []string
	scroll := ""
	for {
		url := baseURL + "/scroll_id/scroll"
		if scroll != "" {
			url += "?scroll=" + scroll
		}
		body := doJSON(t, client, http.MethodGet, url, auth, nil, http.StatusOK)
		for _, e := range body["events"].([]any) {
			ids = append(ids, e.(map[string]any)["id"].(string))
		}
		meta := body["request_metadata"].(map[string]any)
		if meta["complete"] == "true" {
			break
		}
		scroll = meta["scroll"].(string)
	}
	return ids
}

func TestScrollID_Pagination(t *testing.T) {
	ts := newTestServer(t, testserver.ScrollID())
	defer ts.Close()

	ids := drainScroll(t, ts.Client(), ts.URL)
	if len(ids) != 5 {
		t.Errorf("got %d events, want 5", len(ids))
	}
}

func TestScrollID_AuthRejection(t *testing.T) {
	ts := newTestServer(t, testserver.ScrollID())
	defer ts.Close()

	got := statusOnly(t, ts.Client(), http.MethodGet, ts.URL+"/scroll_id/scroll",
		map[string]string{"Authorization": "Bearer wrong"})
	if got != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", got)
	}
}

func TestScrollID_ReplenishOnNewSession(t *testing.T) {
	ts := newTestServer(t, testserver.ScrollID())
	defer ts.Close()

	first := drainScroll(t, ts.Client(), ts.URL)
	second := drainScroll(t, ts.Client(), ts.URL)
	if first[0] == second[0] {
		t.Errorf("second session returned same first event ID %q — replenish did not fire", first[0])
	}
}

// ---- session_cookie ----

// loginSessionCookie hits the login endpoint and returns the Set-Cookie
// header value the data step must replay.
func loginSessionCookie(t *testing.T, client *http.Client, baseURL string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/session_cookie/login", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	cookie := resp.Header.Get("Set-Cookie")
	if cookie == "" {
		t.Fatal("login returned no Set-Cookie header")
	}
	return cookie
}

// drainSessionCookie runs the login → data chain once and returns the event ids.
func drainSessionCookie(t *testing.T, client *http.Client, baseURL string) []string {
	t.Helper()
	cookie := loginSessionCookie(t, client, baseURL)
	body := doJSON(t, client, http.MethodGet, baseURL+"/session_cookie/data",
		map[string]string{"Cookie": cookie}, nil, http.StatusOK)
	items, ok := body["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("items empty or wrong type: %v", body["items"])
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.(map[string]any)["id"].(string))
	}
	return ids
}

func TestSessionCookie_LoginThenData(t *testing.T) {
	ts := newTestServer(t, testserver.SessionCookie())
	defer ts.Close()

	ids := drainSessionCookie(t, ts.Client(), ts.URL)
	if len(ids) != 5 {
		t.Errorf("got %d events, want 5", len(ids))
	}
}

func TestSessionCookie_DataRejectsMissingOrBadCookie(t *testing.T) {
	ts := newTestServer(t, testserver.SessionCookie())
	defer ts.Close()

	if got := statusOnly(t, ts.Client(), http.MethodGet, ts.URL+"/session_cookie/data", nil); got != http.StatusUnauthorized {
		t.Errorf("missing cookie: status = %d, want 401", got)
	}
	bad := map[string]string{"Cookie": "session=not-a-real-session"}
	if got := statusOnly(t, ts.Client(), http.MethodGet, ts.URL+"/session_cookie/data", bad); got != http.StatusUnauthorized {
		t.Errorf("bad cookie: status = %d, want 401", got)
	}
}

func TestSessionCookie_ReplenishOnSecondDrain(t *testing.T) {
	ts := newTestServer(t, testserver.SessionCookie())
	defer ts.Close()

	first := drainSessionCookie(t, ts.Client(), ts.URL)
	second := drainSessionCookie(t, ts.Client(), ts.URL)
	if first[0] == second[0] {
		t.Errorf("second drain returned same first event ID %q — replenish did not fire", first[0])
	}
}
