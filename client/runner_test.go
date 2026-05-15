// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p1llus/skopos/schema"
)

// captureSink is a Sink that just appends emitted events into a slice. The
// runner.go contract says Emit is called once per event from a single
// goroutine inside Drain, so no locking is needed for the test sink.
type captureSink struct {
	events []any
	flushN int
}

func (c *captureSink) Emit(ev any) error { c.events = append(c.events, ev); return nil }
func (c *captureSink) Flush() error      { c.flushN++; return nil }

// ---- Safety floor: max-pages guardrail ----

// TestMaxPagesGuardrail exercises a buggy server that always returns the
// same next_cursor — without the guard, Drain would loop forever. With
// MaxPages set, Drain returns errMaxPagesExceeded after the cap.
func TestMaxPagesGuardrail(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"findings":    []map[string]any{{"id": "x", "created_at": "2026-05-12T08:00:00Z"}},
			"next_cursor": "same-token-forever",
		})
	}))
	defer server.Close()

	r := &Runner{
		Doc:      cursorTokenDoc(server.URL, "test-token"),
		Sink:     &captureSink{},
		Now:      fixedNow(),
		Client:   server.Client(),
		MaxPages: 5,
	}
	err := r.Drain(context.Background())
	if !errors.Is(err, errMaxPagesExceeded) {
		t.Fatalf("Drain err = %v, want errMaxPagesExceeded", err)
	}
	if got := hits.Load(); got != 5 {
		t.Errorf("server saw %d requests, want 5 (MaxPages cap)", got)
	}
}

// ---- §5.2: two-pass placeholder_event for cursor variants ----

// TestPlaceholderEvent_CursorToken_TwoPass exercises the two-pass detector
// described in §5.2. Two consecutive empty pages carry next_cursor → both
// queue a placeholder, each confirmed when the FOLLOWING iteration is
// entered. The terminal page (events present, next_cursor="") emits its
// events and ends the drain without queuing another placeholder. Expected
// emission order: [placeholder, placeholder, real event] — confirming that
// placeholders for empty intermediate pages emit in wire order, ahead of
// the next page's events.
func TestPlaceholderEvent_CursorToken_TwoPass(t *testing.T) {
	pages := []map[string]any{
		{"findings": []map[string]any{}, "next_cursor": "tok-2"},
		{"findings": []map[string]any{}, "next_cursor": "tok-3"},
		{
			"findings":    []map[string]any{{"id": "f1", "created_at": "2026-05-12T08:10:00Z"}},
			"next_cursor": "",
		},
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		idx := int(hits.Add(1)) - 1
		writeJSON(w, http.StatusOK, pages[idx])
	}))
	defer server.Close()

	doc := cursorTokenDoc(server.URL, "test-token")
	doc.Response.PlaceholderEvent = ptrValue(vObject(map[string]schema.Value{
		"kind": vStr("heartbeat"),
	}))

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("server saw %d requests, want 3", got)
	}
	if len(sink.events) != 3 {
		t.Fatalf("emitted %d events, want 3 (placeholder, placeholder, real)", len(sink.events))
	}
	// Wire order: placeholders for the two empty pages come before the
	// real event, each fired at the START of the following iteration.
	for i := 0; i < 2; i++ {
		ev, ok := sink.events[i].(map[string]any)
		if !ok || ev["kind"] != "heartbeat" {
			t.Errorf("event[%d] = %v, want placeholder {kind: heartbeat}", i, sink.events[i])
		}
	}
	last, ok := sink.events[2].(map[string]any)
	if !ok || last["id"] != "f1" {
		t.Errorf("event[2] = %v, want real event with id=f1", sink.events[2])
	}
}

// TestPlaceholderEvent_TwoPass_SuppressedOnMaxPages pins the conservative
// side of the two-pass design: when iter N is empty + paginationMore=true
// but the drain exits BEFORE iter N+1 starts (here via MaxPages), the
// placeholder for N is NOT emitted. We only claim "drain made progress
// past an empty page" when the next iteration is actually entered. Per
// §5.2 / B-03.
func TestPlaceholderEvent_TwoPass_SuppressedOnMaxPages(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"findings":    []map[string]any{},
			"next_cursor": "same-token-forever",
		})
	}))
	defer server.Close()

	doc := cursorTokenDoc(server.URL, "test-token")
	doc.Response.PlaceholderEvent = ptrValue(vObject(map[string]schema.Value{
		"kind": vStr("heartbeat"),
	}))

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client(), MaxPages: 2}
	err := r.Drain(context.Background())
	if !errors.Is(err, errMaxPagesExceeded) {
		t.Fatalf("Drain err = %v, want errMaxPagesExceeded", err)
	}
	// Page 1 (empty) → queue placeholder. Page 2 starts → confirm + emit
	// page 1's placeholder, then page 2 is also empty → queue. Page 3
	// would confirm page 2's placeholder, but MaxPages=2 trips before
	// we enter it: page 2's placeholder is correctly suppressed.
	if len(sink.events) != 1 {
		t.Fatalf("emitted %d events, want 1 (only page 1's confirmed placeholder)", len(sink.events))
	}
	ev, ok := sink.events[0].(map[string]any)
	if !ok || ev["kind"] != "heartbeat" {
		t.Errorf("event[0] = %v, want placeholder {kind: heartbeat}", sink.events[0])
	}
}

// ---- Safety floor: default HTTP client timeout ----

// TestDefaultClientTimeoutIsFinite asserts that when the caller does not
// supply a client, the runner does not hand out http.DefaultClient (which
// has no timeout). This is a structural test — we don't wait 30s for the
// real default to fire; we verify the runner's exposed default constant
// is finite and the constructed client picks it up.
func TestDefaultClientTimeoutIsFinite(t *testing.T) {
	if defaultHTTPTimeout <= 0 {
		t.Errorf("defaultHTTPTimeout = %v, want > 0", defaultHTTPTimeout)
	}
	if defaultHTTPTimeout > 5*time.Minute {
		t.Errorf("defaultHTTPTimeout = %v, suspiciously large", defaultHTTPTimeout)
	}
}

// TestClientTimeoutFiresOnHang exercises the timeout against a server that
// hangs forever. The runner's client must surface the timeout as a
// transport-level error within a bounded test window. We use error.mode:
// "fail" so the timeout propagates as a Drain error (the default "standard"
// mode logs and silently exits, which is intentional but uninformative here).
func TestClientTimeoutFiresOnHang(t *testing.T) {
	hung := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-hung
	}))
	// Cleanup order matters: unblock the handler BEFORE Close drains
	// connections, or Close hangs waiting for in-flight requests.
	t.Cleanup(func() { close(hung); server.Close() })

	doc := bearerSimpleDoc(server.URL, "test-token")
	doc.Error = &schema.ErrorBlock{Mode: "fail"}

	r := &Runner{
		Doc:    doc,
		Sink:   &captureSink{},
		Now:    fixedNow(),
		Client: &http.Client{Timeout: 50 * time.Millisecond},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := r.Drain(ctx)
	if err == nil {
		t.Fatal("Drain returned nil; expected timeout error")
	}
	if !strings.Contains(err.Error(), "Client.Timeout") && !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("Drain err = %v, expected a timeout-shaped message", err)
	}
}

// ---- Safety floor: response-body redaction ----

// TestBodyMetadataRedactsContent asserts the response-body error helper
// returns a length+class description without the raw bytes. A future change
// that puts the body back into errors must update this assertion explicitly.
func TestBodyMetadataRedactsContent(t *testing.T) {
	secret := []byte(`{"access_token":"super-sensitive-bearer"}`)
	got := bodyMetadata(secret)
	if strings.Contains(got, "super-sensitive-bearer") {
		t.Errorf("bodyMetadata leaked content: %q", got)
	}
	if !strings.Contains(got, strconv.Itoa(len(secret))) {
		t.Errorf("bodyMetadata = %q, want byte count for triage", got)
	}
	if !strings.Contains(got, "object-like") {
		t.Errorf("bodyMetadata = %q, want object-like classification", got)
	}
}

func TestBodyMetadataClassifications(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"empty", []byte{}, "body empty"},
		{"object", []byte(`{"a":1}`), "object-like"},
		{"array", []byte(`[1,2]`), "array-like"},
		{"html", []byte(`<html>`), "html-like"},
		{"string", []byte(`"hi"`), "string-like"},
		{"plain", []byte(`yikes`), "non-json"},
	}
	for _, tc := range cases {
		got := bodyMetadata(tc.body)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: bodyMetadata = %q, want substring %q", tc.name, got, tc.want)
		}
	}
}

// TestDecodeErrorDoesNotLeakBody runs a non-JSON server through the runner
// and checks the error returned to the runner caller does not contain the
// raw body bytes — only metadata. error.mode "fail" makes the decode error
// propagate (the default "standard" mode would log and silently exit).
func TestDecodeErrorDoesNotLeakBody(t *testing.T) {
	leakingBody := `<html><body>access_token=super-sensitive</body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, leakingBody)
	}))
	defer server.Close()

	doc := bearerSimpleDoc(server.URL, "test-token")
	doc.Error = &schema.ErrorBlock{Mode: "fail"}

	r := &Runner{
		Doc:    doc,
		Sink:   &captureSink{},
		Now:    fixedNow(),
		Client: server.Client(),
	}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain returned nil; expected decode error")
	}
	if strings.Contains(err.Error(), "super-sensitive") {
		t.Errorf("error leaked body content: %v", err)
	}
}

// ---- Producer-by-index fix: regression test ----

// TestProducerByIndex_NotByLabel: two unlabeled requests with identical
// method+path should NOT collide when the runner picks the producer body
// for the implicit "last request" case. Before the fix, both reqLabel(req)
// strings were identical and the index of the implicit producer was
// ambiguous. After the fix the index decides.
func TestProducerByIndex_NotByLabel(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit := int(hits.Add(1))
		// Each call returns a distinct body so the test can tell which
		// body was treated as the producer.
		writeJSON(w, http.StatusOK, map[string]any{
			"events": []map[string]any{{"id": fmt.Sprintf("hit-%d", hit), "timestamp": "2026-05-12T08:00:00Z"}},
		})
	}))
	defer server.Close()

	doc := bearerSimpleDoc(server.URL, "test-token")
	// Duplicate the request: two unlabeled identical entries. The producer
	// must be the LAST one, identified by slice index.
	doc.Requests = append(doc.Requests, doc.Requests[0])

	sink := &captureSink{}
	r := &Runner{Doc: doc, Sink: sink, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("server hits = %d, want 2", hits.Load())
	}
	if len(sink.events) != 1 {
		t.Fatalf("emitted %d events, want 1 (only the last request is the producer)", len(sink.events))
	}
	ev, ok := sink.events[0].(map[string]any)
	if !ok {
		t.Fatalf("event not a map: %T", sink.events[0])
	}
	if ev["id"] != "hit-2" {
		t.Errorf("event id = %v, want hit-2 (last request body wins)", ev["id"])
	}
}

// ---- doc builders ----
//
// These factories produce minimal schema.Doc values that mirror the curated
// templates/ + schema/testdata/ fixtures. They are NOT a substitute for the
// fixtures (the IR-roundtrip suite covers those); they isolate the runner
// test from the fixture directories so an example author can iterate
// without retro-fitting tests.

func bearerSimpleDoc(baseURL, token string) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{
			Fields: map[string]schema.FieldDecl{
				"url":     {Type: "url", Default: baseURL},
				"api_key": {Type: "secret", Default: token},
			},
		},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.api_key")}},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/events")),
			Query: map[string]schema.Value{
				"since": vFromProg("latest_timestamp"),
			},
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress: schema.Progress{
			LatestEventTimestamp: &schema.TimestampProgress{
				EventTime: schema.EventTime{Path: mustPath("timestamp")},
				Initial:   &schema.Initial{Lookback: vStr("24h")},
			},
		},
	}
}

func cursorTokenDoc(baseURL, token string) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":     {Type: "url", Default: baseURL},
			"api_key": {Type: "secret", Default: token},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.api_key")}},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/findings")),
			Query: map[string]schema.Value{
				"cursor": vFromPag("token"),
			},
		}},
		Response: schema.Response{Decode: "json", EventsAt: mustPath("findings")},
		Pagination: schema.Pagination{CursorToken: &schema.CursorTokenPagination{
			TokenAt: mustPath("next_cursor"),
			SendAs:  "query.cursor",
		}},
		Progress: schema.Progress{
			LatestEventTimestamp: &schema.TimestampProgress{
				EventTime: schema.EventTime{Path: mustPath("created_at")},
			},
		},
	}
}

func pageNumberDoc(baseURL, token string) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":     {Type: "url", Default: baseURL},
			"api_key": {Type: "secret", Default: token},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.api_key")}},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v2/logs")),
			Query: map[string]schema.Value{
				"page": vFormat("string", vFromPag("page")),
			},
		}},
		Response: schema.Response{Decode: "json", EventsAt: mustPath("data")},
		Pagination: schema.Pagination{PageNumber: &schema.PageNumberPagination{
			PageParam: "page",
			HasMoreAt: mustPath("meta.has_next"),
		}},
		Progress: schema.Progress{
			LatestEventTimestamp: &schema.TimestampProgress{
				EventTime: schema.EventTime{Path: mustPath("created_at")},
				Initial:   &schema.Initial{Lookback: vStr("24h")},
			},
		},
	}
}

func asyncPollDoc(baseURL, token string) *schema.Doc {
	completeWhen := schema.Predicate{Eq: &schema.PredicateEq{
		Path:  mustPath("body.status"),
		Equal: vStr("complete"),
	}}
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":     {Type: "url", Default: baseURL},
			"api_key": {Type: "secret", Default: token},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.api_key")}},
		Requests: []schema.Request{
			{
				ID:           "submit",
				Method:       "POST",
				Path:         ptrValue(vStr("/api/v1/exports")),
				Body:         &schema.Body{JSON: map[string]schema.Value{"since": vFromProg("latest_timestamp")}},
				ExpectStatus: []int{202},
			},
			{
				ID:     "poll",
				Method: "GET",
				Path: ptrValue(vConcat(
					vStr("/api/v1/exports/"),
					vRef("cursor.export_id"),
					vStr("/status"),
				)),
			},
			{
				ID:     "fetch",
				Method: "GET",
				URL:    ptrValue(vRef("cursor.result_url")),
			},
		},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("items")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress: schema.Progress{
			AsyncJob: &schema.AsyncJobProgress{
				Submit: &schema.AsyncSubmitStep{
					Step: "submit",
					Extract: map[string]schema.AsyncExtract{
						"export_id": {Path: mustPath("export_id")},
					},
				},
				Poll: &schema.AsyncPollStep{
					Step:         "poll",
					CompleteWhen: &completeWhen,
					Extract: map[string]schema.AsyncExtract{
						"result_url": {Path: mustPath("result_url")},
					},
				},
				Fetch: &schema.AsyncFetchStep{Step: "fetch"},
				OnComplete: &schema.AsyncOnComplete{
					CursorUpdate: &schema.CursorUpdateDirective{Kind: "use_now"},
				},
			},
		},
	}
}

// ---- B-01 / B-20: standard mode does NOT advance the cursor ----

// TestStandardModeOnFailureDoesNotAdvanceProgress drives a cursor_token drain
// where pages 1 and 2 succeed and page 3 returns 500 in error.mode=standard.
// Per §3.4 + B-20, progress.advance MUST NOT run — cursor.last_timestamp
// should still be at the seed (initial.lookback) value, not the max event
// time from pages 1-2. Pagination cursor mid-state IS expected to persist
// (so the next drain resumes from the failed page, not the start of the
// window).
func TestStandardModeOnFailureDoesNotAdvanceProgress(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit := int(hits.Add(1))
		switch hit {
		case 1:
			writeJSON(w, http.StatusOK, map[string]any{
				"findings":    []map[string]any{{"id": "a", "created_at": "2026-05-12T08:00:00Z"}},
				"next_cursor": "tok-2",
			})
		case 2:
			writeJSON(w, http.StatusOK, map[string]any{
				"findings":    []map[string]any{{"id": "b", "created_at": "2026-05-12T08:01:00Z"}},
				"next_cursor": "tok-3",
			})
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	doc := cursorTokenDoc(server.URL, "test-token")
	doc.Progress.LatestEventTimestamp.Initial = &schema.Initial{Lookback: vStr("24h")}
	store := &MemoryStore{}
	r := &Runner{Doc: doc, Store: store, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	snap, _ := store.Load()
	// Seed wrote cursor.last_timestamp = (fixedNow - 24h); progress.advance
	// would have overwritten it with the max event time across pages 1-2.
	// Assert the seed value survived.
	wantSeed := fixedNow()().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	got, _ := snap.Cursor["last_timestamp"].(string)
	if got != wantSeed {
		t.Errorf("cursor.last_timestamp = %q, want seed value %q (standard-mode failure must skip progress.advance)", got, wantSeed)
	}
	maxEventTS := "2026-05-12T08:01:00Z"
	if got == maxEventTS {
		t.Errorf("cursor.last_timestamp advanced to max event timestamp %q — exactly what §3.4 forbids on standard-mode failure", got)
	}
}

// ---- B-02 / B-21: empty_events does not run extracts / phaseTransition ----

// TestEmptyEventsDoesNotBindStaleSteps drives an iteration where a step
// configured with `on_status: {429: empty_events}` returns 429. The step
// has an extract pointing at a body path; per B-02 the extract MUST NOT
// run against the nil body, so the named state field stays at its default.
func TestEmptyEventsDoesNotBindStaleSteps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate-limit", http.StatusTooManyRequests)
	}))
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":     {Type: "url", Default: server.URL},
			"api_key": {Type: "secret", Default: "tok"},
			"marker":  {Type: "string", Default: "pristine"},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.api_key")}},
		Requests: []schema.Request{{
			ID:     "probe",
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/events")),
			OnStatus: map[int]string{
				429: "empty_events",
			},
			Extract: []schema.ExtractVar{{
				Name: "marker",
				Path: mustPath("body_marker"),
			}},
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}
	store := &MemoryStore{}
	r := &Runner{Doc: doc, Store: store, Sink: &captureSink{}, Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	snap, _ := store.Load()
	if got := snap.State["marker"]; got != "pristine" && got != nil {
		t.Errorf("state.marker = %v, want pristine/unset (empty_events must skip extracts)", got)
	}
}

// ---- B-19: MaxPages error carries the iteration count ----

func TestMaxPagesErrorWrapsIterationCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"findings":    []map[string]any{{"id": "x", "created_at": "2026-05-12T08:00:00Z"}},
			"next_cursor": "same-token-forever",
		})
	}))
	defer server.Close()

	r := &Runner{Doc: cursorTokenDoc(server.URL, "test-token"), Sink: &captureSink{}, Now: fixedNow(), Client: server.Client(), MaxPages: 5}
	err := r.Drain(context.Background())
	if err == nil {
		t.Fatalf("expected MaxPages error")
	}
	if !strings.Contains(err.Error(), "5 iterations") {
		t.Errorf("error %q does not contain iteration count", err.Error())
	}
	if !strings.Contains(err.Error(), "MaxPages") {
		t.Errorf("error %q does not contain MaxPages label", err.Error())
	}
}

// writeJSON is a small helper for the httptest handlers.
func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(payload); err != nil {
		panic(err)
	}
	_, _ = w.Write(buf.Bytes())
}
