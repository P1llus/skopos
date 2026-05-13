// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/p1llus/skopos/schema"
)

// TestNonePagination — the no-op driver. seed clears nothing, advance always
// returns wantMore=false.
func TestNonePagination(t *testing.T) {
	p := &nonePagination{}
	s := newTestScope(t, nil, nil)
	p.seed(s)
	more, err := p.advance(s, nil, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("nonePagination wantMore = true, want false")
	}
}

// TestCursorTokenPagination_SeedFirstIteration: cursor.token unset, so the
// {from_pagination: token} role is absent. This is the first-page case.
func TestCursorTokenPagination_SeedFirstIteration(t *testing.T) {
	p := &cursorTokenPagination{cfg: &schema.CursorTokenPagination{
		TokenAt: mustPath("next_cursor"),
	}}
	s := newTestScope(t, nil, nil)
	p.seed(s)
	if _, ok := s.fromPagination["token"]; ok {
		t.Errorf("first-iteration seed left a fromPagination[token]; expected absent")
	}
}

// TestCursorTokenPagination_AdvanceMid: token_at resolves to a non-zero
// value → cursor.token is updated, wantMore=true. Next seed exposes that
// token via fromPagination.
func TestCursorTokenPagination_AdvanceMid(t *testing.T) {
	p := &cursorTokenPagination{cfg: &schema.CursorTokenPagination{
		TokenAt: mustPath("next_cursor"),
	}}
	s := newTestScope(t, nil, nil)

	body := map[string]any{"next_cursor": "tok-2", "findings": []any{}}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !more {
		t.Errorf("advance after non-zero token: wantMore=false")
	}
	if got := s.cursor["token"]; got != "tok-2" {
		t.Errorf("cursor.token = %v, want tok-2", got)
	}

	p.seed(s)
	if got := s.fromPagination["token"]; got != "tok-2" {
		t.Errorf("fromPagination[token] = %v, want tok-2", got)
	}
}

// TestCursorTokenPagination_AdvanceTerminate: token_at resolves to zero →
// cursor cleared, wantMore=false (drain done).
func TestCursorTokenPagination_AdvanceTerminate(t *testing.T) {
	p := &cursorTokenPagination{cfg: &schema.CursorTokenPagination{
		TokenAt: mustPath("next_cursor"),
	}}
	s := newTestScope(t, nil, map[string]any{"token": "tok-1"})

	body := map[string]any{"next_cursor": "", "findings": []any{}}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance after zero token: wantMore=true")
	}
	if _, ok := s.cursor["token"]; ok {
		t.Errorf("cursor.token left set after termination")
	}
}

// TestPageNumberPagination_SeedFirstIteration: cursor.page absent ⇒ exposes
// 1 and writes it back to cursor (so {from_pagination: page} renders "1").
func TestPageNumberPagination_SeedFirstIteration(t *testing.T) {
	p := &pageNumberPagination{cfg: &schema.PageNumberPagination{}}
	s := newTestScope(t, nil, nil)
	p.seed(s)
	if got := s.fromPagination["page"]; got != int64(1) {
		t.Errorf("fromPagination[page] = %v, want 1", got)
	}
	if got := s.cursor["page"]; got != int64(1) {
		t.Errorf("cursor.page = %v, want 1", got)
	}
}

// TestPageNumberPagination_AdvanceHasMore drives has_more_at=true → next
// page, has_more_at=false → reset to 1.
func TestPageNumberPagination_AdvanceHasMore(t *testing.T) {
	p := &pageNumberPagination{cfg: &schema.PageNumberPagination{
		HasMoreAt: mustPath("meta.has_next"),
	}}
	s := newTestScope(t, nil, map[string]any{"page": int64(2)})

	body := map[string]any{"meta": map[string]any{"has_next": true}}
	more, err := p.advance(s, body, nil, []any{1, 2, 3})
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !more {
		t.Errorf("has_next=true: wantMore=false")
	}
	if got := s.cursor["page"]; got != int64(3) {
		t.Errorf("cursor.page = %v, want 3", got)
	}

	body = map[string]any{"meta": map[string]any{"has_next": false}}
	more, _ = p.advance(s, body, nil, []any{1, 2, 3})
	if more {
		t.Errorf("has_next=false: wantMore=true")
	}
	if got := s.cursor["page"]; got != int64(1) {
		t.Errorf("cursor.page after terminate = %v, want 1", got)
	}
}

// TestPageNumberPagination_AdvanceNoHasMoreField: with no has_more_at signal,
// stop when events come back empty, otherwise advance.
func TestPageNumberPagination_AdvanceNoHasMoreField(t *testing.T) {
	p := &pageNumberPagination{cfg: &schema.PageNumberPagination{}}
	s := newTestScope(t, nil, map[string]any{"page": int64(2)})

	more, _ := p.advance(s, nil, nil, []any{1, 2})
	if !more {
		t.Errorf("non-empty events with no has_more: wantMore=false")
	}
	if got := s.cursor["page"]; got != int64(3) {
		t.Errorf("cursor.page = %v, want 3", got)
	}

	more, _ = p.advance(s, nil, nil, nil)
	if more {
		t.Errorf("empty events: wantMore=true")
	}
	if got := s.cursor["page"]; got != int64(1) {
		t.Errorf("cursor.page after empty = %v, want 1", got)
	}
}

// TestPageNumberPagination_BatchSizeStopsShort: when batch_size is set, the
// short page (fewer events than batch_size) terminates the drain.
func TestPageNumberPagination_BatchSizeStopsShort(t *testing.T) {
	bs := vInt(5)
	p := &pageNumberPagination{cfg: &schema.PageNumberPagination{
		BatchSize: &bs,
	}}
	s := newTestScope(t, nil, map[string]any{"page": int64(2)})

	more, _ := p.advance(s, nil, nil, []any{1, 2, 3}) // 3 < 5 → done
	if more {
		t.Errorf("short page with batch_size: wantMore=true")
	}
	if got := s.cursor["page"]; got != int64(1) {
		t.Errorf("cursor.page after short page = %v, want 1", got)
	}
}

// TestOffsetPagination_SeedFirstIteration: cursor.offset absent ⇒ exposes
// 0 and writes it back to cursor (so {ref: cursor.offset} resolves the same
// way {from_pagination: offset} does).
func TestOffsetPagination_SeedFirstIteration(t *testing.T) {
	p := &offsetPagination{cfg: &schema.OffsetPagination{}}
	s := newTestScope(t, nil, nil)
	p.seed(s)
	if got := s.fromPagination["offset"]; got != int64(0) {
		t.Errorf("fromPagination[offset] = %v, want 0", got)
	}
	if got := s.cursor["offset"]; got != int64(0) {
		t.Errorf("cursor.offset = %v, want 0", got)
	}
	// offset_end is unset when batch_size is absent.
	if _, ok := s.fromPagination["offset_end"]; ok {
		t.Errorf("fromPagination[offset_end] should be absent when batch_size unset")
	}
}

// TestOffsetPagination_SeedWithBatchSize_OffsetEnd: when batch_size is set,
// seed exposes offset_end = offset + batch_size so APIs that take an
// exclusive upper bound (e.g. ?from=N&to=N+50) can render the role.
func TestOffsetPagination_SeedWithBatchSize_OffsetEnd(t *testing.T) {
	bs := vInt(50)
	p := &offsetPagination{cfg: &schema.OffsetPagination{BatchSize: &bs}}
	s := newTestScope(t, nil, map[string]any{"offset": int64(100)})
	p.seed(s)
	if got := s.fromPagination["offset"]; got != int64(100) {
		t.Errorf("fromPagination[offset] = %v, want 100", got)
	}
	if got := s.fromPagination["offset_end"]; got != int64(150) {
		t.Errorf("fromPagination[offset_end] = %v, want 150", got)
	}
}

// TestOffsetPagination_AdvanceBatchSize: full page (len(events)==batch_size)
// advances offset by batch_size and signals more; short page resets to 0
// and signals done.
func TestOffsetPagination_AdvanceBatchSize(t *testing.T) {
	bs := vInt(3)
	p := &offsetPagination{cfg: &schema.OffsetPagination{BatchSize: &bs}}
	s := newTestScope(t, nil, map[string]any{"offset": int64(6)})

	more, err := p.advance(s, nil, nil, []any{1, 2, 3})
	if err != nil {
		t.Fatalf("advance full page: %v", err)
	}
	if !more {
		t.Errorf("full page (len==batch_size): wantMore=false")
	}
	if got := s.cursor["offset"]; got != int64(9) {
		t.Errorf("cursor.offset after full page = %v, want 9", got)
	}

	// Short page: 2 < batch_size=3 → terminate, reset to 0.
	more, _ = p.advance(s, nil, nil, []any{1, 2})
	if more {
		t.Errorf("short page: wantMore=true")
	}
	if got := s.cursor["offset"]; got != int64(0) {
		t.Errorf("cursor.offset after short page = %v, want 0 (reset)", got)
	}
}

// TestOffsetPagination_AdvanceNoBatchSize: with no batch_size signal, advance
// by the count of events returned; empty page terminates.
func TestOffsetPagination_AdvanceNoBatchSize(t *testing.T) {
	p := &offsetPagination{cfg: &schema.OffsetPagination{}}
	s := newTestScope(t, nil, map[string]any{"offset": int64(10)})

	more, _ := p.advance(s, nil, nil, []any{1, 2, 3, 4})
	if !more {
		t.Errorf("non-empty events with no batch_size: wantMore=false")
	}
	if got := s.cursor["offset"]; got != int64(14) {
		t.Errorf("cursor.offset = %v, want 14 (10 + 4)", got)
	}

	more, _ = p.advance(s, nil, nil, nil)
	if more {
		t.Errorf("empty events: wantMore=true")
	}
	if got := s.cursor["offset"]; got != int64(0) {
		t.Errorf("cursor.offset after empty = %v, want 0 (reset)", got)
	}
}

// TestEndToEnd_OffsetPagination drives the offset variant against a live
// httptest server. It mirrors TestEndToEnd_CursorToken's shape: three
// pages, asserting (a) each request carries the expected from= query, (b)
// the loop terminates on a short page, and (c) cursor.offset resets to 0
// in the final snapshot so the next drain starts from the top.
func TestEndToEnd_OffsetPagination(t *testing.T) {
	pages := []struct {
		offset int
		events []map[string]any
	}{
		{0, []map[string]any{
			{"id": "a", "created_at": "2026-05-12T08:00:00Z"},
			{"id": "b", "created_at": "2026-05-12T08:01:00Z"},
		}},
		{2, []map[string]any{
			{"id": "c", "created_at": "2026-05-12T08:02:00Z"},
			{"id": "d", "created_at": "2026-05-12T08:03:00Z"},
		}},
		// Short page (1 < batch_size=2) → terminate.
		{4, []map[string]any{
			{"id": "e", "created_at": "2026-05-12T08:04:00Z"},
		}},
	}
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&hits, 1)) - 1
		if i >= len(pages) {
			t.Errorf("unexpected extra request %d", i)
			return
		}
		gotFrom := r.URL.Query().Get("from")
		wantFrom := strconv.Itoa(pages[i].offset)
		if gotFrom != wantFrom {
			t.Errorf("page %d from = %q, want %q", i, gotFrom, wantFrom)
		}
		if got := r.URL.Query().Get("size"); got != "2" {
			t.Errorf("page %d size = %q, want 2", i, got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": pages[i].events})
	}))
	defer server.Close()

	store := &MemoryStore{}
	sink := &captureSink{}
	r := &Runner{
		Doc:    offsetDoc(server.URL, "test-token", 2),
		Sink:   sink,
		Store:  store,
		Now:    fixedNow(),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("server saw %d requests, want 3", got)
	}
	if len(sink.events) != 5 {
		t.Fatalf("emitted %d events, want 5", len(sink.events))
	}
	snap, _ := store.Load()
	if got := snap.Cursor["offset"]; got != int64(0) {
		t.Errorf("cursor.offset after terminate = %v, want 0 (reset)", got)
	}
}

// offsetDoc builds a minimal offset-paginated *schema.Doc. The request carries
// from={from_pagination:offset} and size={literal batch_size}, mirroring
// the catalogue patterns where offset rides as a query parameter.
func offsetDoc(baseURL, token string, batchSize int64) *schema.Doc {
	bs := vInt(batchSize)
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
			Path:   ptrValue(vStr("/api/v1/events")),
			Query: map[string]schema.Value{
				"from": vFormat("string", vFromPag("offset")),
				"size": vFormat("string", bs),
			},
		}},
		Response: schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{Offset: &schema.OffsetPagination{
			OffsetParam: "from",
			BatchSize:   &bs,
		}},
		Progress: schema.Progress{
			LatestEventTimestamp: &schema.TimestampProgress{
				EventTime: schema.EventTime{Path: mustPath("created_at")},
				Initial:   &schema.Initial{Lookback: vStr("24h")},
			},
		},
	}
}

// TestUnsupportedPaginationVariants asserts that an empty pagination block
// (no variant set) fails at plan construction with a clear error.
func TestUnsupportedPaginationVariants(t *testing.T) {
	doc := &schema.Doc{Pagination: schema.Pagination{}}
	if _, err := makePaginationPlan(doc); err == nil {
		t.Errorf("makePaginationPlan with empty pagination block: expected error, got nil")
	}
}

// TestLinkHeaderPagination_SeedFirstIteration: cursor.next_link absent ⇒
// {from_pagination: next_link} resolves to nil so the template's
// {ref: cursor.next_link, default: ...} branch wins. Mirrors the
// cursor_token first-iteration shape.
func TestLinkHeaderPagination_SeedFirstIteration(t *testing.T) {
	p, err := newLinkHeaderPagination(&schema.LinkHeaderPagination{})
	if err != nil {
		t.Fatalf("newLinkHeaderPagination: %v", err)
	}
	s := newTestScope(t, nil, nil)
	p.seed(s)
	if _, ok := s.fromPagination["next_link"]; ok {
		t.Errorf("first-iteration seed left a fromPagination[next_link]; expected absent")
	}
}

// TestLinkHeaderPagination_AdvanceNext: Link header with rel="next" →
// cursor.next_link captured, wantMore=true. Next seed surfaces it via
// fromPagination.
func TestLinkHeaderPagination_AdvanceNext(t *testing.T) {
	p, err := newLinkHeaderPagination(&schema.LinkHeaderPagination{})
	if err != nil {
		t.Fatalf("newLinkHeaderPagination: %v", err)
	}
	s := newTestScope(t, nil, nil)

	headers := http.Header{
		"Link": []string{`<https://api.example.com/events?page=2>; rel="next", <https://api.example.com/events?page=10>; rel="last"`},
	}
	more, err := p.advance(s, nil, headers, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !more {
		t.Errorf("advance with rel=next: wantMore=false")
	}
	want := "https://api.example.com/events?page=2"
	if got := s.cursor["next_link"]; got != want {
		t.Errorf("cursor.next_link = %v, want %q", got, want)
	}

	p.seed(s)
	if got := s.fromPagination["next_link"]; got != want {
		t.Errorf("fromPagination[next_link] = %v, want %q", got, want)
	}
}

// TestLinkHeaderPagination_AdvanceTerminate: response without a rel="next"
// entry → wantMore=false and cursor.next_link cleared so the next drain
// starts from the bootstrap URL again.
func TestLinkHeaderPagination_AdvanceTerminate(t *testing.T) {
	p, err := newLinkHeaderPagination(&schema.LinkHeaderPagination{})
	if err != nil {
		t.Fatalf("newLinkHeaderPagination: %v", err)
	}
	s := newTestScope(t, nil, map[string]any{"next_link": "https://api.example.com/events?page=9"})

	// Only rel="prev" — no rel="next" → terminate.
	headers := http.Header{
		"Link": []string{`<https://api.example.com/events?page=8>; rel="prev"`},
	}
	more, err := p.advance(s, nil, headers, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance without rel=next: wantMore=true")
	}
	if _, ok := s.cursor["next_link"]; ok {
		t.Errorf("cursor.next_link left set after termination")
	}
}

// TestLinkHeaderPagination_AdvanceNoHeader: empty / absent Link header →
// terminate and reset.
func TestLinkHeaderPagination_AdvanceNoHeader(t *testing.T) {
	p, err := newLinkHeaderPagination(&schema.LinkHeaderPagination{})
	if err != nil {
		t.Fatalf("newLinkHeaderPagination: %v", err)
	}
	s := newTestScope(t, nil, map[string]any{"next_link": "https://api.example.com/events?page=2"})

	more, err := p.advance(s, nil, http.Header{}, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance with no Link header: wantMore=true")
	}
	if _, ok := s.cursor["next_link"]; ok {
		t.Errorf("cursor.next_link left set after termination")
	}
}

// TestParseNextLink covers the RFC 5988 default parser's edge cases that
// matter in the wild: multi-value headers, multi-rel param values, missing
// quotes around rel, and split-header continuations.
func TestParseNextLink(t *testing.T) {
	cases := []struct {
		name string
		hdr  []string
		want string
	}{
		{
			name: "simple_next",
			hdr:  []string{`<https://api.example.com/p2>; rel="next"`},
			want: "https://api.example.com/p2",
		},
		{
			name: "multi_entry_next_first",
			hdr:  []string{`<https://api.example.com/p2>; rel="next", <https://api.example.com/p10>; rel="last"`},
			want: "https://api.example.com/p2",
		},
		{
			name: "multi_entry_next_last",
			hdr:  []string{`<https://api.example.com/p1>; rel="prev", <https://api.example.com/p3>; rel="next"`},
			want: "https://api.example.com/p3",
		},
		{
			name: "unquoted_rel",
			hdr:  []string{`<https://api.example.com/p2>; rel=next`},
			want: "https://api.example.com/p2",
		},
		{
			name: "multi_rel_value",
			hdr:  []string{`<https://api.example.com/p2>; rel="next first"`},
			want: "https://api.example.com/p2",
		},
		{
			name: "split_across_two_header_lines",
			hdr: []string{
				`<https://api.example.com/p1>; rel="prev"`,
				`<https://api.example.com/p3>; rel="next"`,
			},
			want: "https://api.example.com/p3",
		},
		{
			name: "no_next_rel",
			hdr:  []string{`<https://api.example.com/p1>; rel="prev"`},
			want: "",
		},
		{
			name: "empty_header",
			hdr:  []string{},
			want: "",
		},
		{
			name: "whitespace_only_value",
			hdr:  []string{"   "},
			want: "",
		},
		{
			name: "malformed_entry_ignored",
			hdr:  []string{`not-a-link, <https://api.example.com/p2>; rel="next"`},
			want: "https://api.example.com/p2",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := parseNextLink(tc.hdr, nil); got != tc.want {
				t.Errorf("parseNextLink(%v) = %q, want %q", tc.hdr, got, tc.want)
			}
		})
	}
}

// TestParseNextLink_CustomPattern: the optional pattern field overrides
// RFC 5988 detection — first capture group of the first match wins. Useful
// for APIs that misuse Link's shape (or use a different header entirely
// and pipe it through headers["Link"] for the runner).
func TestParseNextLink_CustomPattern(t *testing.T) {
	p, err := newLinkHeaderPagination(&schema.LinkHeaderPagination{
		Pattern: `<([^>]+)>;\s*rel="next-page"`,
	})
	if err != nil {
		t.Fatalf("newLinkHeaderPagination: %v", err)
	}
	headers := http.Header{
		"Link": []string{`<https://api.example.com/p2>; rel="next-page"`},
	}
	s := newTestScope(t, nil, nil)
	more, err := p.advance(s, nil, headers, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !more {
		t.Errorf("advance with custom pattern match: wantMore=false")
	}
	want := "https://api.example.com/p2"
	if got := s.cursor["next_link"]; got != want {
		t.Errorf("cursor.next_link = %v, want %q", got, want)
	}
}

// TestLinkHeaderPagination_InvalidPatternRejectedAtConstruction asserts a
// bad regex surfaces at plan construction, before any HTTP traffic.
func TestLinkHeaderPagination_InvalidPatternRejectedAtConstruction(t *testing.T) {
	_, err := newLinkHeaderPagination(&schema.LinkHeaderPagination{Pattern: "[unterminated"})
	if err == nil {
		t.Fatal("newLinkHeaderPagination with bad regex: expected error, got nil")
	}
}

// TestEndToEnd_LinkHeader drives the link_header variant against a live
// httptest server. Three pages, asserting (a) the second/third request
// hits the URL announced by the previous Link header, (b) the loop
// terminates when no rel="next" is present, and (c) cursor.next_link is
// cleared so the next drain starts from the bootstrap URL again.
func TestEndToEnd_LinkHeader(t *testing.T) {
	var hits int32
	type page struct {
		events []map[string]any
		// nextRel is the path the rel="next" Link points at on this page.
		// Empty means no rel="next" (terminal page).
		nextRel string
	}
	pages := []page{
		{
			events:  []map[string]any{{"id": "a", "created_at": "2026-05-12T08:00:00Z"}},
			nextRel: "/api/v1/issues?cursor=p2",
		},
		{
			events:  []map[string]any{{"id": "b", "created_at": "2026-05-12T08:01:00Z"}},
			nextRel: "/api/v1/issues?cursor=p3",
		},
		{
			events:  []map[string]any{{"id": "c", "created_at": "2026-05-12T08:02:00Z"}},
			nextRel: "", // terminate.
		},
	}
	// expectedQuery is the cursor= query value the runner should send on each
	// page. Page 0 is the bootstrap URL with no cursor; subsequent pages
	// echo whatever the previous Link header named.
	expectedCursor := []string{"", "p2", "p3"}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&hits, 1)) - 1
		if i >= len(pages) {
			t.Errorf("unexpected extra request %d", i)
			return
		}
		if got := r.URL.Query().Get("cursor"); got != expectedCursor[i] {
			t.Errorf("page %d cursor = %q, want %q", i, got, expectedCursor[i])
		}
		if pages[i].nextRel != "" {
			w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="next"`, server.URL, pages[i].nextRel))
		}
		writeJSON(w, http.StatusOK, map[string]any{"issues": pages[i].events})
	}))
	defer server.Close()

	store := &MemoryStore{}
	sink := &captureSink{}
	r := &Runner{
		Doc:    linkHeaderDoc(server.URL, "test-token"),
		Sink:   sink,
		Store:  store,
		Now:    fixedNow(),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("server saw %d requests, want 3", got)
	}
	if len(sink.events) != 3 {
		t.Fatalf("emitted %d events, want 3", len(sink.events))
	}
	snap, _ := store.Load()
	if _, ok := snap.Cursor["next_link"]; ok {
		t.Errorf("cursor.next_link should be cleared after terminating page")
	}
}

// linkHeaderDoc builds a minimal link_header-paginated *schema.Doc. The
// request's url slot uses {ref: cursor.next_link, default: <bootstrap>}
// so the first iteration uses the bootstrap URL and subsequent
// iterations follow the server's Link header.
func linkHeaderDoc(baseURL, token string) *schema.Doc {
	bootstrap := vConcat(vRef("state.url"), vStr("/api/v1/issues"))
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
			URL:    ptrValue(vRefDefault("cursor.next_link", bootstrap)),
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("issues")},
		Pagination: schema.Pagination{LinkHeader: &schema.LinkHeaderPagination{}},
		Progress: schema.Progress{
			LatestEventTimestamp: &schema.TimestampProgress{
				EventTime: schema.EventTime{Path: mustPath("created_at")},
				Initial:   &schema.Initial{Lookback: vStr("24h")},
			},
		},
	}
}

// TestNextURLInBodyPagination_SeedFirstIteration: cursor.next_url absent ⇒
// {from_pagination: next_url} resolves to nil so the template's
// {ref: cursor.next_url, default: ...} branch wins. Mirrors the
// link_header first-iteration shape.
func TestNextURLInBodyPagination_SeedFirstIteration(t *testing.T) {
	p := &nextURLInBodyPagination{cfg: &schema.NextURLInBodyPagination{
		NextURLAt: mustPath("paging.next"),
	}}
	s := newTestScope(t, nil, nil)
	p.seed(s)
	if _, ok := s.fromPagination["next_url"]; ok {
		t.Errorf("first-iteration seed left a fromPagination[next_url]; expected absent")
	}
}

// TestNextURLInBodyPagination_AdvanceNext: a body carrying a non-empty
// string at next_url_at → cursor.next_url captured, wantMore=true. Next
// seed surfaces it via fromPagination.
func TestNextURLInBodyPagination_AdvanceNext(t *testing.T) {
	p := &nextURLInBodyPagination{cfg: &schema.NextURLInBodyPagination{
		NextURLAt: mustPath("paging.next"),
	}}
	s := newTestScope(t, nil, nil)

	body := map[string]any{
		"paging": map[string]any{"next": "https://api.example.com/events?cursor=p2"},
		"items":  []any{},
	}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !more {
		t.Errorf("advance with next URL: wantMore=false")
	}
	want := "https://api.example.com/events?cursor=p2"
	if got := s.cursor["next_url"]; got != want {
		t.Errorf("cursor.next_url = %v, want %q", got, want)
	}

	p.seed(s)
	if got := s.fromPagination["next_url"]; got != want {
		t.Errorf("fromPagination[next_url] = %v, want %q", got, want)
	}
}

// TestNextURLInBodyPagination_AdvanceTerminate: a body whose next_url_at
// path resolves to "" → wantMore=false and cursor.next_url cleared so the
// next drain starts from the bootstrap URL again.
func TestNextURLInBodyPagination_AdvanceTerminate(t *testing.T) {
	p := &nextURLInBodyPagination{cfg: &schema.NextURLInBodyPagination{
		NextURLAt: mustPath("paging.next"),
	}}
	s := newTestScope(t, nil, map[string]any{"next_url": "https://api.example.com/events?cursor=p9"})

	body := map[string]any{
		"paging": map[string]any{"next": ""},
		"items":  []any{},
	}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance with empty next URL: wantMore=true")
	}
	if _, ok := s.cursor["next_url"]; ok {
		t.Errorf("cursor.next_url left set after termination")
	}
}

// TestNextURLInBodyPagination_AdvanceMissingPath: a body that doesn't
// carry next_url_at at all → terminate and reset. Mirrors the spec's
// "missing path is a zero Value" rule.
func TestNextURLInBodyPagination_AdvanceMissingPath(t *testing.T) {
	p := &nextURLInBodyPagination{cfg: &schema.NextURLInBodyPagination{
		NextURLAt: mustPath("paging.next"),
	}}
	s := newTestScope(t, nil, map[string]any{"next_url": "https://api.example.com/events?cursor=p2"})

	body := map[string]any{"items": []any{}}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance with missing next path: wantMore=true")
	}
	if _, ok := s.cursor["next_url"]; ok {
		t.Errorf("cursor.next_url left set after termination")
	}
}

// TestNextURLInBodyPagination_AdvanceNilValue: a body with an explicit
// null at next_url_at → terminate and reset. Distinct from missing-path
// (the lookup ok flag is true but the value is nil); the runner treats
// both the same way per the §15 zero-Value rule.
func TestNextURLInBodyPagination_AdvanceNilValue(t *testing.T) {
	p := &nextURLInBodyPagination{cfg: &schema.NextURLInBodyPagination{
		NextURLAt: mustPath("paging.next"),
	}}
	s := newTestScope(t, nil, map[string]any{"next_url": "https://api.example.com/events?cursor=p2"})

	body := map[string]any{"paging": map[string]any{"next": nil}, "items": []any{}}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance with nil next value: wantMore=true")
	}
	if _, ok := s.cursor["next_url"]; ok {
		t.Errorf("cursor.next_url left set after termination")
	}
}

// TestNextURLInBodyPagination_AdvanceNonString: a non-string value at
// next_url_at (e.g. server returns a JSON number by mistake) → terminate
// rather than crash. Defensive — real APIs always serve URLs as strings,
// but the runner refuses to fabricate a URL from a number.
func TestNextURLInBodyPagination_AdvanceNonString(t *testing.T) {
	p := &nextURLInBodyPagination{cfg: &schema.NextURLInBodyPagination{
		NextURLAt: mustPath("paging.next"),
	}}
	s := newTestScope(t, nil, map[string]any{"next_url": "https://api.example.com/events?cursor=p2"})

	body := map[string]any{"paging": map[string]any{"next": float64(42)}, "items": []any{}}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance with non-string next value: wantMore=true")
	}
	if _, ok := s.cursor["next_url"]; ok {
		t.Errorf("cursor.next_url left set after termination")
	}
}

// TestEndToEnd_NextURLInBody drives the next_url_in_body variant against a
// live httptest server. Three pages, asserting (a) the second/third
// request hits the URL announced by the previous body, (b) the loop
// terminates when the producer body omits next_url, and (c)
// cursor.next_url is cleared so the next drain starts from the bootstrap
// URL again.
func TestEndToEnd_NextURLInBody(t *testing.T) {
	var hits int32
	type page struct {
		events []map[string]any
		// nextRel is the path the body's paging.next points at on this
		// page. Empty means the field is omitted (terminal page).
		nextRel string
	}
	pages := []page{
		{
			events:  []map[string]any{{"id": "a", "created_at": "2026-05-12T08:00:00Z"}},
			nextRel: "/api/v1/items?cursor=p2",
		},
		{
			events:  []map[string]any{{"id": "b", "created_at": "2026-05-12T08:01:00Z"}},
			nextRel: "/api/v1/items?cursor=p3",
		},
		{
			events:  []map[string]any{{"id": "c", "created_at": "2026-05-12T08:02:00Z"}},
			nextRel: "", // terminate.
		},
	}
	// expectedCursor[i] is the cursor= query value the runner should send
	// on page i. Page 0 is the bootstrap URL with no cursor; subsequent
	// pages echo whatever the previous body named.
	expectedCursor := []string{"", "p2", "p3"}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&hits, 1)) - 1
		if i >= len(pages) {
			t.Errorf("unexpected extra request %d", i)
			return
		}
		if got := r.URL.Query().Get("cursor"); got != expectedCursor[i] {
			t.Errorf("page %d cursor = %q, want %q", i, got, expectedCursor[i])
		}
		body := map[string]any{"items": pages[i].events}
		if pages[i].nextRel != "" {
			body["paging"] = map[string]any{"next": server.URL + pages[i].nextRel}
		}
		writeJSON(w, http.StatusOK, body)
	}))
	defer server.Close()

	store := &MemoryStore{}
	sink := &captureSink{}
	r := &Runner{
		Doc:    nextURLInBodyDoc(server.URL, "test-token"),
		Sink:   sink,
		Store:  store,
		Now:    fixedNow(),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("server saw %d requests, want 3", got)
	}
	if len(sink.events) != 3 {
		t.Fatalf("emitted %d events, want 3", len(sink.events))
	}
	snap, _ := store.Load()
	if _, ok := snap.Cursor["next_url"]; ok {
		t.Errorf("cursor.next_url should be cleared after terminating page")
	}
}

// TestScrollIDPagination_SeedFirstIteration: cursor.scroll_id absent ⇒
// {from_pagination: scroll_id} resolves to nil so the server opens a new
// scroll session on the bootstrap request. Mirrors the cursor_token
// first-iteration shape.
func TestScrollIDPagination_SeedFirstIteration(t *testing.T) {
	p := &scrollIDPagination{cfg: &schema.ScrollIDPagination{
		ScrollIDAt: mustPath("request_metadata.scroll"),
	}}
	s := newTestScope(t, nil, nil)
	p.seed(s)
	if _, ok := s.fromPagination["scroll_id"]; ok {
		t.Errorf("first-iteration seed left a fromPagination[scroll_id]; expected absent")
	}
}

// TestScrollIDPagination_AdvanceNext: a body carrying a non-empty scroll id
// at scroll_id_at → cursor.scroll_id captured, wantMore=true. Next seed
// surfaces it via fromPagination.
func TestScrollIDPagination_AdvanceNext(t *testing.T) {
	p := &scrollIDPagination{cfg: &schema.ScrollIDPagination{
		ScrollIDAt: mustPath("request_metadata.scroll"),
	}}
	s := newTestScope(t, nil, nil)

	body := map[string]any{
		"request_metadata": map[string]any{"scroll": "scroll-tok-2"},
		"events":           []any{},
	}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !more {
		t.Errorf("advance with scroll id: wantMore=false")
	}
	if got := s.cursor["scroll_id"]; got != "scroll-tok-2" {
		t.Errorf("cursor.scroll_id = %v, want scroll-tok-2", got)
	}

	p.seed(s)
	if got := s.fromPagination["scroll_id"]; got != "scroll-tok-2" {
		t.Errorf("fromPagination[scroll_id] = %v, want scroll-tok-2", got)
	}
}

// TestScrollIDPagination_AdvanceTerminateOnZeroID: with no complete_when
// declared, a missing / empty scroll id ends the drain and clears the
// cursor. Mirrors §15's implicit-default contract for scroll_id.
func TestScrollIDPagination_AdvanceTerminateOnZeroID(t *testing.T) {
	p := &scrollIDPagination{cfg: &schema.ScrollIDPagination{
		ScrollIDAt: mustPath("request_metadata.scroll"),
	}}
	s := newTestScope(t, nil, map[string]any{"scroll_id": "scroll-tok-1"})

	body := map[string]any{
		"request_metadata": map[string]any{"scroll": ""},
		"events":           []any{},
	}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance with empty scroll id: wantMore=true")
	}
	if _, ok := s.cursor["scroll_id"]; ok {
		t.Errorf("cursor.scroll_id left set after termination")
	}
}

// TestScrollIDPagination_AdvanceMissingPath: a body that doesn't carry
// scroll_id_at at all → terminate and reset. Mirrors the spec's "missing
// path is a zero Value" rule.
func TestScrollIDPagination_AdvanceMissingPath(t *testing.T) {
	p := &scrollIDPagination{cfg: &schema.ScrollIDPagination{
		ScrollIDAt: mustPath("request_metadata.scroll"),
	}}
	s := newTestScope(t, nil, map[string]any{"scroll_id": "scroll-tok-1"})

	body := map[string]any{"events": []any{}}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance with missing scroll_id path: wantMore=true")
	}
	if _, ok := s.cursor["scroll_id"]; ok {
		t.Errorf("cursor.scroll_id left set after termination")
	}
}

// TestScrollIDPagination_AdvanceCompleteWhenTrue: complete_when satisfied
// terminates the drain even if the body still carries a fresh scroll id.
// The predicate evaluates against the producer body via the same
// {body: ...} scope mechanic async_job.poll uses.
func TestScrollIDPagination_AdvanceCompleteWhenTrue(t *testing.T) {
	complete := schema.Predicate{Eq: &schema.PredicateEq{
		Path:  mustPath("body.request_metadata.complete"),
		Equal: vStr("true"),
	}}
	p := &scrollIDPagination{cfg: &schema.ScrollIDPagination{
		ScrollIDAt:   mustPath("request_metadata.scroll"),
		CompleteWhen: &complete,
	}}
	s := newTestScope(t, nil, map[string]any{"scroll_id": "scroll-tok-1"})

	// Body still carries a fresh id, but complete_when wins.
	body := map[string]any{
		"request_metadata": map[string]any{
			"scroll":   "scroll-tok-2",
			"complete": true,
		},
		"events": []any{},
	}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance with complete_when=true: wantMore=true")
	}
	if _, ok := s.cursor["scroll_id"]; ok {
		t.Errorf("cursor.scroll_id left set after complete_when termination")
	}
}

// TestScrollIDPagination_AdvanceCompleteWhenFalse: complete_when not yet
// satisfied → capture the fresh scroll id, signal wantMore=true. The fresh
// id rides into the next iteration via fromPagination.
func TestScrollIDPagination_AdvanceCompleteWhenFalse(t *testing.T) {
	complete := schema.Predicate{Eq: &schema.PredicateEq{
		Path:  mustPath("body.request_metadata.complete"),
		Equal: vStr("true"),
	}}
	p := &scrollIDPagination{cfg: &schema.ScrollIDPagination{
		ScrollIDAt:   mustPath("request_metadata.scroll"),
		CompleteWhen: &complete,
	}}
	s := newTestScope(t, nil, map[string]any{"scroll_id": "scroll-tok-1"})

	body := map[string]any{
		"request_metadata": map[string]any{
			"scroll":   "scroll-tok-2",
			"complete": false,
		},
		"events": []any{},
	}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !more {
		t.Errorf("advance with complete_when=false: wantMore=false")
	}
	if got := s.cursor["scroll_id"]; got != "scroll-tok-2" {
		t.Errorf("cursor.scroll_id = %v, want scroll-tok-2", got)
	}
}

// TestScrollIDPagination_AdvanceBodyScopeIsolation pins that evaluating
// complete_when doesn't leak s.body to subsequent eval calls. The scope's
// body should always be restored to whatever it was before advance ran.
func TestScrollIDPagination_AdvanceBodyScopeIsolation(t *testing.T) {
	falseVal := false
	complete := schema.Predicate{LiteralBool: &falseVal}
	p := &scrollIDPagination{cfg: &schema.ScrollIDPagination{
		ScrollIDAt:   mustPath("request_metadata.scroll"),
		CompleteWhen: &complete,
	}}
	s := newTestScope(t, nil, nil)
	prevBody := map[string]any{"sentinel": "outer"}
	s.body = prevBody

	body := map[string]any{
		"request_metadata": map[string]any{"scroll": "scroll-tok-2"},
	}
	if _, err := p.advance(s, body, nil, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, ok := s.body.(map[string]any)
	if !ok || got["sentinel"] != "outer" {
		t.Errorf("s.body after advance = %#v, want outer sentinel preserved", s.body)
	}
}

// TestEndToEnd_ScrollID drives the scroll_id variant against a live
// httptest server. Three pages, asserting (a) page 0 carries no scroll=
// query (bootstrap opens the session), (b) pages 1-2 echo the previous
// page's scroll id, (c) the loop terminates when complete_when fires,
// and (d) cursor.scroll_id is cleared in the final snapshot so the next
// drain opens a fresh session.
func TestEndToEnd_ScrollID(t *testing.T) {
	type page struct {
		events     []map[string]any
		scrollID   string
		isComplete bool
	}
	pages := []page{
		{
			events:     []map[string]any{{"id": "a", "created_at": "2026-05-12T08:00:00Z"}},
			scrollID:   "scroll-p2",
			isComplete: false,
		},
		{
			events:     []map[string]any{{"id": "b", "created_at": "2026-05-12T08:01:00Z"}},
			scrollID:   "scroll-p3",
			isComplete: false,
		},
		{
			events:     []map[string]any{{"id": "c", "created_at": "2026-05-12T08:02:00Z"}},
			scrollID:   "scroll-p3",
			isComplete: true,
		},
	}
	// expectedScroll[i] is the scroll= query value the runner should send
	// on page i. Page 0 is empty (bootstrap); subsequent pages echo the
	// previous response's scroll id.
	expectedScroll := []string{"", "scroll-p2", "scroll-p3"}
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&hits, 1)) - 1
		if i >= len(pages) {
			t.Errorf("unexpected extra request %d", i)
			return
		}
		if got := r.URL.Query().Get("scroll"); got != expectedScroll[i] {
			t.Errorf("page %d scroll = %q, want %q", i, got, expectedScroll[i])
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"events": pages[i].events,
			"request_metadata": map[string]any{
				"scroll":   pages[i].scrollID,
				"complete": pages[i].isComplete,
			},
		})
	}))
	defer server.Close()

	store := &MemoryStore{}
	sink := &captureSink{}
	r := &Runner{
		Doc:    scrollIDDoc(server.URL, "test-token"),
		Sink:   sink,
		Store:  store,
		Now:    fixedNow(),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("server saw %d requests, want 3", got)
	}
	if len(sink.events) != 3 {
		t.Fatalf("emitted %d events, want 3", len(sink.events))
	}
	snap, _ := store.Load()
	if _, ok := snap.Cursor["scroll_id"]; ok {
		t.Errorf("cursor.scroll_id should be cleared after complete_when termination")
	}
}

// scrollIDDoc builds a minimal scroll_id-paginated *schema.Doc mirroring
// examples/scroll_id.yaml. The scroll= query carries
// {from_pagination: scroll_id} so the first request goes out without it
// (server opens the session) and subsequent requests echo the previous
// response's scroll id. complete_when fires when the body flags the
// session as drained.
func scrollIDDoc(baseURL, token string) *schema.Doc {
	complete := schema.Predicate{Eq: &schema.PredicateEq{
		Path:  mustPath("body.request_metadata.complete"),
		Equal: vStr("true"),
	}}
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
			Path:   ptrValue(vStr("/api/v1/scroll")),
			Query: map[string]schema.Value{
				"scroll": vFromPag("scroll_id"),
			},
		}},
		Response: schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{ScrollID: &schema.ScrollIDPagination{
			ScrollIDAt:   mustPath("request_metadata.scroll"),
			SendAs:       "query.scroll",
			CompleteWhen: &complete,
		}},
		Progress: schema.Progress{
			LatestEventTimestamp: &schema.TimestampProgress{
				EventTime: schema.EventTime{Path: mustPath("created_at")},
				Initial:   &schema.Initial{Lookback: vStr("24h")},
			},
		},
	}
}

// TestGraphQLRelayPagination_SeedFirstIteration: cursor.<cursor_var> absent ⇒
// {from_pagination: relay_cursor} resolves to nil so the GraphQL variable
// rides as null on the bootstrap request. Mirrors the cursor_token
// first-iteration shape, but with an author-named cursor key.
func TestGraphQLRelayPagination_SeedFirstIteration(t *testing.T) {
	p := &graphQLRelayPagination{cfg: &schema.GraphQLRelayPagination{
		HasNextPageAt: mustPath("data.issues.pageInfo.hasNextPage"),
		EndCursorAt:   mustPath("data.issues.pageInfo.endCursor"),
		CursorVar:     "after",
	}}
	s := newTestScope(t, nil, nil)
	p.seed(s)
	if _, ok := s.fromPagination["relay_cursor"]; ok {
		t.Errorf("first-iteration seed left a fromPagination[relay_cursor]; expected absent")
	}
}

// TestGraphQLRelayPagination_AdvanceNext: has_next_page=true + a non-empty
// endCursor → cursor.<cursor_var> captured, wantMore=true. Next seed surfaces
// it via fromPagination[relay_cursor].
func TestGraphQLRelayPagination_AdvanceNext(t *testing.T) {
	p := &graphQLRelayPagination{cfg: &schema.GraphQLRelayPagination{
		HasNextPageAt: mustPath("data.issues.pageInfo.hasNextPage"),
		EndCursorAt:   mustPath("data.issues.pageInfo.endCursor"),
		CursorVar:     "after",
	}}
	s := newTestScope(t, nil, nil)

	body := map[string]any{
		"data": map[string]any{
			"issues": map[string]any{
				"nodes": []any{},
				"pageInfo": map[string]any{
					"hasNextPage": true,
					"endCursor":   "Y3Vyc29yOjI=",
				},
			},
		},
	}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !more {
		t.Errorf("advance with hasNextPage=true: wantMore=false")
	}
	if got := s.cursor["after"]; got != "Y3Vyc29yOjI=" {
		t.Errorf("cursor.after = %v, want Y3Vyc29yOjI=", got)
	}

	p.seed(s)
	if got := s.fromPagination["relay_cursor"]; got != "Y3Vyc29yOjI=" {
		t.Errorf("fromPagination[relay_cursor] = %v, want Y3Vyc29yOjI=", got)
	}
}

// TestGraphQLRelayPagination_AdvanceTerminateOnHasNextFalse: has_next_page=false
// ends the drain and clears the cursor — even if endCursor still carries a
// value, the Relay contract says no more pages exist.
func TestGraphQLRelayPagination_AdvanceTerminateOnHasNextFalse(t *testing.T) {
	p := &graphQLRelayPagination{cfg: &schema.GraphQLRelayPagination{
		HasNextPageAt: mustPath("data.issues.pageInfo.hasNextPage"),
		EndCursorAt:   mustPath("data.issues.pageInfo.endCursor"),
		CursorVar:     "after",
	}}
	s := newTestScope(t, nil, map[string]any{"after": "Y3Vyc29yOjE="})

	body := map[string]any{
		"data": map[string]any{
			"issues": map[string]any{
				"nodes": []any{},
				"pageInfo": map[string]any{
					"hasNextPage": false,
					"endCursor":   "Y3Vyc29yOjk5",
				},
			},
		},
	}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance with hasNextPage=false: wantMore=true")
	}
	if _, ok := s.cursor["after"]; ok {
		t.Errorf("cursor.after left set after termination")
	}
}

// TestGraphQLRelayPagination_AdvanceMissingHasNext: a body that doesn't
// carry the has_next_page_at path at all → terminate and reset. Mirrors
// the spec's "missing field is a zero Value" rule.
func TestGraphQLRelayPagination_AdvanceMissingHasNext(t *testing.T) {
	p := &graphQLRelayPagination{cfg: &schema.GraphQLRelayPagination{
		HasNextPageAt: mustPath("data.issues.pageInfo.hasNextPage"),
		EndCursorAt:   mustPath("data.issues.pageInfo.endCursor"),
		CursorVar:     "after",
	}}
	s := newTestScope(t, nil, map[string]any{"after": "Y3Vyc29yOjE="})

	body := map[string]any{"data": map[string]any{"issues": map[string]any{}}}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance with missing pageInfo: wantMore=true")
	}
	if _, ok := s.cursor["after"]; ok {
		t.Errorf("cursor.after left set after termination")
	}
}

// TestGraphQLRelayPagination_AdvanceMissingEndCursorWhenMore: has_next_page=true
// but end_cursor_at resolves to a zero value → terminate defensively. A
// well-behaved Relay server never produces this combination, but the
// runner refuses to re-fire the same request without a fresh cursor.
func TestGraphQLRelayPagination_AdvanceMissingEndCursorWhenMore(t *testing.T) {
	p := &graphQLRelayPagination{cfg: &schema.GraphQLRelayPagination{
		HasNextPageAt: mustPath("data.issues.pageInfo.hasNextPage"),
		EndCursorAt:   mustPath("data.issues.pageInfo.endCursor"),
		CursorVar:     "after",
	}}
	s := newTestScope(t, nil, map[string]any{"after": "Y3Vyc29yOjE="})

	body := map[string]any{
		"data": map[string]any{
			"issues": map[string]any{
				"pageInfo": map[string]any{
					"hasNextPage": true,
					"endCursor":   "",
				},
			},
		},
	}
	more, err := p.advance(s, body, nil, nil)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("advance with empty endCursor + hasNextPage=true: wantMore=true")
	}
	if _, ok := s.cursor["after"]; ok {
		t.Errorf("cursor.after left set after termination")
	}
}

// TestGraphQLRelayPagination_AdvanceHonoursCustomCursorVar pins that the
// cursor key the driver writes to is the author-declared cursor_var, not a
// hard-coded "after". Use a non-default name to make the assertion
// load-bearing.
func TestGraphQLRelayPagination_AdvanceHonoursCustomCursorVar(t *testing.T) {
	p := &graphQLRelayPagination{cfg: &schema.GraphQLRelayPagination{
		HasNextPageAt: mustPath("data.users.pageInfo.hasNextPage"),
		EndCursorAt:   mustPath("data.users.pageInfo.endCursor"),
		CursorVar:     "userCursor",
	}}
	s := newTestScope(t, nil, nil)

	body := map[string]any{
		"data": map[string]any{
			"users": map[string]any{
				"pageInfo": map[string]any{
					"hasNextPage": true,
					"endCursor":   "u-2",
				},
			},
		},
	}
	if _, err := p.advance(s, body, nil, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if _, ok := s.cursor["after"]; ok {
		t.Errorf("cursor.after written under default key; want author-declared cursor_var")
	}
	if got := s.cursor["userCursor"]; got != "u-2" {
		t.Errorf("cursor.userCursor = %v, want u-2", got)
	}

	p.seed(s)
	if got := s.fromPagination["relay_cursor"]; got != "u-2" {
		t.Errorf("fromPagination[relay_cursor] = %v, want u-2", got)
	}
}

// TestEndToEnd_GraphQLRelay drives the graphql_relay variant against a live
// httptest server speaking GraphQL-shaped JSON over POST. Three pages,
// asserting (a) page 0's `variables.after` rides as JSON null (bootstrap),
// (b) pages 1-2 echo the previous page's endCursor, (c) the loop
// terminates when pageInfo.hasNextPage flips false, and (d) cursor.after
// is cleared in the final snapshot so the next drain starts from the
// beginning of the connection.
func TestEndToEnd_GraphQLRelay(t *testing.T) {
	type page struct {
		nodes       []map[string]any
		endCursor   string
		hasNextPage bool
	}
	pages := []page{
		{
			nodes:       []map[string]any{{"id": "a", "created_at": "2026-05-12T08:00:00Z"}},
			endCursor:   "Y3Vyc29yOjI=",
			hasNextPage: true,
		},
		{
			nodes:       []map[string]any{{"id": "b", "created_at": "2026-05-12T08:01:00Z"}},
			endCursor:   "Y3Vyc29yOjM=",
			hasNextPage: true,
		},
		{
			nodes:       []map[string]any{{"id": "c", "created_at": "2026-05-12T08:02:00Z"}},
			endCursor:   "Y3Vyc29yOjM=",
			hasNextPage: false,
		},
	}
	// expectedAfter[i] is the value the runner should send in
	// variables.after on page i. Page 0 is nil (bootstrap connection
	// traversal); subsequent pages echo the previous response's endCursor.
	expectedAfter := []any{nil, "Y3Vyc29yOjI=", "Y3Vyc29yOjM="}
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&hits, 1)) - 1
		if i >= len(pages) {
			t.Errorf("unexpected extra request %d", i)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("page %d method = %q, want POST", i, r.Method)
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := decodeJSONBody(r, &req); err != nil {
			t.Errorf("page %d decode: %v", i, err)
			return
		}
		got, hasKey := req.Variables["after"]
		switch want := expectedAfter[i].(type) {
		case nil:
			// Page 0: the variable should ride as JSON null. We accept
			// either "key present with nil value" or "key absent" —
			// both encode the same GraphQL semantics.
			if hasKey && got != nil {
				t.Errorf("page %d variables.after = %v, want null/absent", i, got)
			}
		case string:
			if !hasKey {
				t.Errorf("page %d variables.after absent, want %q", i, want)
			} else if got != want {
				t.Errorf("page %d variables.after = %v, want %q", i, got, want)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"nodes": pages[i].nodes,
					"pageInfo": map[string]any{
						"hasNextPage": pages[i].hasNextPage,
						"endCursor":   pages[i].endCursor,
					},
				},
			},
		})
	}))
	defer server.Close()

	store := &MemoryStore{}
	sink := &captureSink{}
	r := &Runner{
		Doc:    graphqlRelayDoc(server.URL, "test-token"),
		Sink:   sink,
		Store:  store,
		Now:    fixedNow(),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("server saw %d requests, want 3", got)
	}
	if len(sink.events) != 3 {
		t.Fatalf("emitted %d events, want 3", len(sink.events))
	}
	snap, _ := store.Load()
	if _, ok := snap.Cursor["after"]; ok {
		t.Errorf("cursor.after should be cleared after hasNextPage=false")
	}
}

// graphqlRelayDoc builds a minimal graphql_relay-paginated *schema.Doc mirroring
// schema/testdata/oauth2_relay.yaml. The request is a POST with a
// GraphQL body whose variables.after carries {from_pagination: relay_cursor}
// — the first request sends after=null (server starts at the beginning of
// the connection) and subsequent requests replay the previous response's
// pageInfo.endCursor.
func graphqlRelayDoc(baseURL, token string) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":     {Type: "url", Default: baseURL},
			"api_key": {Type: "secret", Default: token},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.api_key")}},
		Requests: []schema.Request{{
			Method: "POST",
			Path:   ptrValue(vStr("/api/v1/graphql")),
			Body: &schema.Body{JSON: map[string]schema.Value{
				"query": vStr("query Issues($after: String) { issues(after: $after) { nodes { id created_at } pageInfo { hasNextPage endCursor } } }"),
				"variables": vObject(map[string]schema.Value{
					"after": vFromPag("relay_cursor"),
				}),
			}},
		}},
		Response: schema.Response{Decode: "json", EventsAt: mustPath("data.issues.nodes")},
		Pagination: schema.Pagination{GraphQLRelay: &schema.GraphQLRelayPagination{
			HasNextPageAt: mustPath("data.issues.pageInfo.hasNextPage"),
			EndCursorAt:   mustPath("data.issues.pageInfo.endCursor"),
			CursorVar:     "after",
		}},
		Progress: schema.Progress{
			LatestEventTimestamp: &schema.TimestampProgress{
				EventTime: schema.EventTime{Path: mustPath("created_at")},
				Initial:   &schema.Initial{Lookback: vStr("24h")},
			},
		},
	}
}

// decodeJSONBody is a tiny test helper for graphql_relay's end-to-end: read
// r.Body, unmarshal into target. Kept local to avoid leaking a JSON-shape
// helper into the broader test suite.
func decodeJSONBody(r *http.Request, target any) error {
	dec := json.NewDecoder(r.Body)
	return dec.Decode(target)
}

// nextURLInBodyDoc builds a minimal next_url_in_body-paginated *schema.Doc.
// The request's url slot uses {ref: cursor.next_url, default: <bootstrap>}
// so the first iteration uses the bootstrap URL and subsequent
// iterations follow whatever the previous body's paging.next named.
func nextURLInBodyDoc(baseURL, token string) *schema.Doc {
	bootstrap := vConcat(vRef("state.url"), vStr("/api/v1/items"))
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
			URL:    ptrValue(vRefDefault("cursor.next_url", bootstrap)),
		}},
		Response: schema.Response{Decode: "json", EventsAt: mustPath("items")},
		Pagination: schema.Pagination{NextURLInBody: &schema.NextURLInBodyPagination{
			NextURLAt: mustPath("paging.next"),
		}},
		Progress: schema.Progress{
			LatestEventTimestamp: &schema.TimestampProgress{
				EventTime: schema.EventTime{Path: mustPath("created_at")},
				Initial:   &schema.Initial{Lookback: vStr("24h")},
			},
		},
	}
}

// ---- termination-edge tests ----

// TestPageNumberPagination_AdvanceMissingHasMorePath: HasMoreAt is
// declared but the body lacks the path entirely. Per the §15
// zero-Value rule the runner conservatively terminates and resets the
// cursor to 1.
func TestPageNumberPagination_AdvanceMissingHasMorePath(t *testing.T) {
	p := &pageNumberPagination{cfg: &schema.PageNumberPagination{
		HasMoreAt: mustPath("meta.has_next"),
	}}
	s := newTestScope(t, nil, map[string]any{"page": int64(2)})

	body := map[string]any{"meta": map[string]any{}}
	more, err := p.advance(s, body, nil, []any{1, 2, 3})
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if more {
		t.Errorf("missing has_more path: wantMore=true, want false (conservative terminate)")
	}
	// Note: the conservative-terminate branch at advance() returns
	// before resetting cursor.page. The cursor is reset by the
	// has_more=false / empty-page / short-page branches; the missing-
	// path branch leaves cursor.page untouched. Pin both the
	// terminate signal and the leave-cursor-as-is contract here.
	if got := s.cursor["page"]; got != int64(2) {
		t.Errorf("missing has_more path: cursor.page = %v, want 2 (untouched)", got)
	}
}

// TestPageNumberPagination_AdvanceNonBoolHasMore: HasMoreAt resolves
// to a string the toBool helper rejects. The runner must surface the
// wrapped error rather than silently coercing or terminating.
func TestPageNumberPagination_AdvanceNonBoolHasMore(t *testing.T) {
	p := &pageNumberPagination{cfg: &schema.PageNumberPagination{
		HasMoreAt: mustPath("meta.has_next"),
	}}
	s := newTestScope(t, nil, nil)

	body := map[string]any{"meta": map[string]any{"has_next": "yes"}}
	_, err := p.advance(s, body, nil, []any{1})
	if err == nil {
		t.Fatal("advance: nil error, want non-bool wrap")
	}
	if !strings.Contains(err.Error(), "pagination.page_number.has_more_at") {
		t.Errorf("err = %v, want substring pagination.page_number.has_more_at", err)
	}
}

// TestGraphQLRelayPagination_AdvanceNonBoolHasNext: same shape as the
// page_number non-bool case, for graphql_relay's has_next_page_at.
func TestGraphQLRelayPagination_AdvanceNonBoolHasNext(t *testing.T) {
	p := &graphQLRelayPagination{cfg: &schema.GraphQLRelayPagination{
		HasNextPageAt: mustPath("data.pageInfo.hasNextPage"),
		EndCursorAt:   mustPath("data.pageInfo.endCursor"),
		CursorVar:     "after",
	}}
	s := newTestScope(t, nil, nil)

	body := map[string]any{"data": map[string]any{"pageInfo": map[string]any{
		"hasNextPage": "yes",
		"endCursor":   "cur-1",
	}}}
	_, err := p.advance(s, body, nil, nil)
	if err == nil {
		t.Fatal("advance: nil error, want non-bool wrap")
	}
	if !strings.Contains(err.Error(), "pagination.graphql_relay.has_next_page_at") {
		t.Errorf("err = %v, want substring pagination.graphql_relay.has_next_page_at", err)
	}
}

// TestOffsetPagination_SeedLogsBatchSizeEvalFailure pins the
// operator breadcrumb added for P2-rule2-02. When seed's batch_size
// eval fails we still drop offset_end silently from this iteration —
// but a log line MUST surface so an operator notices the misshapen
// bootstrap request rather than chasing a phantom pagination bug
// later.
func TestOffsetPagination_SeedLogsBatchSizeEvalFailure(t *testing.T) {
	// Reference an absent state field with no default — evalValue
	// returns nil, then toInt fails on nil.
	bs := vRef("state.size_limit")
	p := &offsetPagination{cfg: &schema.OffsetPagination{BatchSize: &bs}}

	s := newTestScope(t, nil, nil)
	var buf bytes.Buffer
	s.logger = log.New(&buf, "", 0)

	p.seed(s)
	if _, ok := s.fromPagination["offset_end"]; ok {
		t.Errorf("seed left offset_end set after eval failure; expected dropped")
	}
	if !strings.Contains(buf.String(), "pagination.offset.batch_size eval failed") {
		t.Errorf("logger missing breadcrumb; got %q", buf.String())
	}
}

// TestPaginationErrorLabels asserts every reachable error wrap inside
// pagination's advance() / seed() carries the canonical
// `pagination.<variant>.<field>` prefix. A regression that misses one
// variant in a label rename surfaces here.
//
// Reachability note: lookupBodyPath in bodypath.go does NOT return a
// non-nil error in any code path today — a path that walks past a
// scalar returns (nil, false, nil), which the variant treats as a
// graceful terminate. The lookupBodyPath wraps in cursor_token /
// next_url_in_body / scroll_id / graphql_relay.end_cursor_at are
// therefore defensive guards against a future change to lookupBodyPath;
// we cannot exercise them from a test today. The reachable labels are
// the coercion / predicate / batch-size eval branches, which is what
// this table covers.
func TestPaginationErrorLabels(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(t *testing.T) (paginationPlan, *scope, any, []any)
		wantLabel string
	}{
		{
			// Reachable via toBool failure on a string that ParseBool rejects.
			name:      "page_number.has_more_at non-bool",
			wantLabel: "pagination.page_number.has_more_at",
			setup: func(t *testing.T) (paginationPlan, *scope, any, []any) {
				p := &pageNumberPagination{cfg: &schema.PageNumberPagination{
					HasMoreAt: mustPath("meta.has_next"),
				}}
				s := newTestScope(t, nil, nil)
				body := map[string]any{"meta": map[string]any{"has_next": "yes"}}
				return p, s, body, []any{1}
			},
		},
		{
			// Reachable via toInt failure on a non-numeric string.
			name:      "page_number.batch_size non-int",
			wantLabel: "pagination.page_number.batch_size",
			setup: func(t *testing.T) (paginationPlan, *scope, any, []any) {
				bs := vStr("not-a-number")
				p := &pageNumberPagination{cfg: &schema.PageNumberPagination{
					BatchSize: &bs,
				}}
				s := newTestScope(t, nil, nil)
				body := map[string]any{}
				return p, s, body, []any{1, 2}
			},
		},
		{
			// Reachable via the same toInt failure inside offset.advance.
			name:      "offset.batch_size non-int",
			wantLabel: "pagination.offset.batch_size",
			setup: func(t *testing.T) (paginationPlan, *scope, any, []any) {
				bs := vStr("not-a-number")
				p := &offsetPagination{cfg: &schema.OffsetPagination{
					BatchSize: &bs,
				}}
				s := newTestScope(t, nil, nil)
				body := map[string]any{}
				return p, s, body, []any{1, 2}
			},
		},
		{
			// Reachable via evalPredicate of an empty Predicate.
			name:      "scroll_id.complete_when invalid",
			wantLabel: "pagination.scroll_id.complete_when",
			setup: func(t *testing.T) (paginationPlan, *scope, any, []any) {
				badPred := schema.Predicate{}
				p := &scrollIDPagination{cfg: &schema.ScrollIDPagination{
					ScrollIDAt:   mustPath("scroll_id"),
					CompleteWhen: &badPred,
				}}
				s := newTestScope(t, nil, nil)
				body := map[string]any{"scroll_id": "s-1"}
				return p, s, body, nil
			},
		},
		{
			// Reachable via toBool failure on graphql_relay's hasNextPage.
			name:      "graphql_relay.has_next_page_at non-bool",
			wantLabel: "pagination.graphql_relay.has_next_page_at",
			setup: func(t *testing.T) (paginationPlan, *scope, any, []any) {
				p := &graphQLRelayPagination{cfg: &schema.GraphQLRelayPagination{
					HasNextPageAt: mustPath("data.pageInfo.hasNextPage"),
					EndCursorAt:   mustPath("data.pageInfo.endCursor"),
					CursorVar:     "after",
				}}
				s := newTestScope(t, nil, nil)
				body := map[string]any{"data": map[string]any{"pageInfo": map[string]any{
					"hasNextPage": "yes",
					"endCursor":   "c-1",
				}}}
				return p, s, body, nil
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			plan, s, body, events := tc.setup(t)
			_, err := plan.advance(s, body, nil, events)
			if err == nil {
				t.Fatalf("advance: nil error, want wrap %q", tc.wantLabel)
			}
			if !strings.Contains(err.Error(), tc.wantLabel) {
				t.Errorf("err = %v, want substring %q", err, tc.wantLabel)
			}
		})
	}
}

// ---- send_as implicit auto-injection ----
//
// The §4.4 implicit-form contract: when a paginating strategy declares
// `send_as: query.<param>` (or `header.<name>`), the runner auto-injects
// the cursor at that slot on the producer step's request — authors don't
// have to write the explicit {from_pagination: <role>} Value themselves.
// Explicit form (template declares the slot) wins; the runner detects
// explicit declaration by IR presence (req.Query[name] / req.Headers[name]
// case-insensitive) and skips the auto-injection for that slot.
//
// The unit tests below pin parseSendAs's two legal kinds, the per-strategy
// autoInjectSlot() returns, and the queryDeclared / headerDeclared helpers.
// The end-to-end tests pin the lowering against a live httptest server for
// both kinds (query slot for cursor_token, header slot for scroll_id) plus
// the explicit-form-skips-auto-injection guarantee.

func TestParseSendAs(t *testing.T) {
	cases := []struct {
		in       string
		wantKind string
		wantName string
	}{
		{"query.cursor", "query", "cursor"},
		{"header.X-Scroll-ID", "header", "X-Scroll-ID"},
		{"query.", "query", ""},
		{"header.", "header", ""},
		{"", "", ""},
		{"body.token", "", ""},
		{"cookie.session", "", ""},
	}
	for _, tc := range cases {
		gotKind, gotName := parseSendAs(tc.in)
		if gotKind != tc.wantKind || gotName != tc.wantName {
			t.Errorf("parseSendAs(%q) = (%q, %q), want (%q, %q)", tc.in, gotKind, gotName, tc.wantKind, tc.wantName)
		}
	}
}

func TestCursorTokenPagination_AutoInjectSlot(t *testing.T) {
	p := &cursorTokenPagination{cfg: &schema.CursorTokenPagination{
		TokenAt: mustPath("next_cursor"),
		SendAs:  "query.cursor",
	}}
	got := p.autoInjectSlot()
	want := autoInjectSlot{kind: "query", name: "cursor", role: "token"}
	if got != want {
		t.Errorf("autoInjectSlot = %+v, want %+v", got, want)
	}
}

func TestScrollIDPagination_AutoInjectSlot(t *testing.T) {
	p := &scrollIDPagination{cfg: &schema.ScrollIDPagination{
		ScrollIDAt: mustPath("request_metadata.scroll"),
		SendAs:     "header.X-Scroll-ID",
	}}
	got := p.autoInjectSlot()
	want := autoInjectSlot{kind: "header", name: "X-Scroll-ID", role: "scroll_id"}
	if got != want {
		t.Errorf("autoInjectSlot = %+v, want %+v", got, want)
	}
}

// TestHeaderDeclared_CaseInsensitive pins that the explicit-form detector
// canonicalises header keys per RFC 7230 §3.2 — a template that writes
// `X-API-Token` and a send_as of `header.x-api-token` refer to the same
// slot, so auto-injection MUST skip.
func TestHeaderDeclared_CaseInsensitive(t *testing.T) {
	req := schema.Request{Headers: map[string]schema.Value{"X-API-Token": vStr("explicit")}}
	if !headerDeclared(req, "x-api-token") {
		t.Errorf("headerDeclared with mismatched case returned false; HTTP header names are case-insensitive")
	}
	if !headerDeclared(req, "X-API-TOKEN") {
		t.Errorf("headerDeclared with upper case returned false")
	}
	if headerDeclared(req, "X-Other-Header") {
		t.Errorf("headerDeclared with unrelated key returned true")
	}
}

// TestQueryDeclared_ExactMatch pins that query-param detection is exact
// (case-sensitive) — RFC 3986 reserves no case folding for query
// components, so `cursor` and `Cursor` are distinct slots.
func TestQueryDeclared_ExactMatch(t *testing.T) {
	req := schema.Request{Query: map[string]schema.Value{"cursor": vStr("explicit")}}
	if !queryDeclared(req, "cursor") {
		t.Errorf("queryDeclared with exact match returned false")
	}
	if queryDeclared(req, "Cursor") {
		t.Errorf("queryDeclared with different case returned true; query params are case-sensitive")
	}
	if queryDeclared(req, "other") {
		t.Errorf("queryDeclared with unrelated key returned true")
	}
}

// TestEndToEnd_CursorToken_ImplicitSendAs drives cursor_token across three
// pages without an explicit {from_pagination: token} in req.Query — the
// runner must lower `send_as: query.cursor` into an auto-injection at the
// producer-step query slot. Wire shape (page 0 bootstrap → page 2
// termination) must match the explicit-form TestEndToEnd_CursorToken case
// exactly. Pins the §4.4 implicit-form contract.
func TestEndToEnd_CursorToken_ImplicitSendAs(t *testing.T) {
	pages := []map[string]any{
		{
			"findings":    []map[string]any{{"id": "f1", "created_at": "2026-05-12T08:00:00Z"}},
			"next_cursor": "tok-2",
		},
		{
			"findings":    []map[string]any{{"id": "f2", "created_at": "2026-05-12T08:05:00Z"}},
			"next_cursor": "tok-3",
		},
		{
			"findings":    []map[string]any{{"id": "f3", "created_at": "2026-05-12T08:10:00Z"}},
			"next_cursor": "",
		},
	}
	var pageCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(pageCount.Add(1)) - 1
		cur := r.URL.Query().Get("cursor")
		switch idx {
		case 0:
			if cur != "" {
				t.Errorf("page 0 cursor = %q, want empty (bootstrap)", cur)
			}
		case 1:
			if cur != "tok-2" {
				t.Errorf("page 1 cursor = %q, want tok-2 (auto-injected)", cur)
			}
		case 2:
			if cur != "tok-3" {
				t.Errorf("page 2 cursor = %q, want tok-3 (auto-injected)", cur)
			}
		default:
			t.Errorf("unexpected extra page request %d", idx)
		}
		writeJSON(w, http.StatusOK, pages[idx])
	}))
	defer server.Close()

	store := &MemoryStore{}
	r := &Runner{
		Doc:    cursorTokenImplicitDoc(server.URL, "test-token"),
		Sink:   &captureSink{},
		Store:  store,
		Now:    fixedNow(),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := pageCount.Load(); got != 3 {
		t.Fatalf("server saw %d requests, want 3", got)
	}
	snap, _ := store.Load()
	if _, ok := snap.Cursor["token"]; ok {
		t.Errorf("cursor.token should be cleared after terminating page")
	}
}

// cursorTokenImplicitDoc mirrors cursorTokenDoc but OMITS the explicit
// {from_pagination: token} value at query.cursor. The runner's `send_as`
// auto-injection has to produce the same wire shape.
func cursorTokenImplicitDoc(baseURL, token string) *schema.Doc {
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
			// No req.Query — auto-injection populates query.cursor.
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

// TestEndToEnd_ScrollID_ImplicitSendAs_Header drives scroll_id with
// send_as=header.X-Scroll-ID (no explicit {from_pagination: scroll_id} in
// the template). Asserts the header is absent on page 0 (bootstrap) and
// echoed on pages 1-2 by the auto-injection. Exercises the header branch
// of the implicit lowering AND the case-insensitive declared-slot check.
func TestEndToEnd_ScrollID_ImplicitSendAs_Header(t *testing.T) {
	type page struct {
		events     []map[string]any
		scrollID   string
		isComplete bool
	}
	pages := []page{
		{
			events:     []map[string]any{{"id": "a", "created_at": "2026-05-12T08:00:00Z"}},
			scrollID:   "scroll-p2",
			isComplete: false,
		},
		{
			events:     []map[string]any{{"id": "b", "created_at": "2026-05-12T08:01:00Z"}},
			scrollID:   "scroll-p3",
			isComplete: false,
		},
		{
			events:     []map[string]any{{"id": "c", "created_at": "2026-05-12T08:02:00Z"}},
			scrollID:   "scroll-p3",
			isComplete: true,
		},
	}
	expectedHeader := []string{"", "scroll-p2", "scroll-p3"}
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&hits, 1)) - 1
		if i >= len(pages) {
			t.Errorf("unexpected extra request %d", i)
			return
		}
		got := r.Header.Get("X-Scroll-ID")
		if got != expectedHeader[i] {
			t.Errorf("page %d X-Scroll-ID = %q, want %q", i, got, expectedHeader[i])
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"events": pages[i].events,
			"request_metadata": map[string]any{
				"scroll":   pages[i].scrollID,
				"complete": pages[i].isComplete,
			},
		})
	}))
	defer server.Close()

	r := &Runner{
		Doc:    scrollIDImplicitHeaderDoc(server.URL, "test-token"),
		Sink:   &captureSink{},
		Store:  &MemoryStore{},
		Now:    fixedNow(),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("server saw %d requests, want 3", got)
	}
}

// scrollIDImplicitHeaderDoc uses send_as=header.X-Scroll-ID with no
// explicit slot Value. The runner must auto-inject the scroll id as a
// request header on every non-bootstrap request.
func scrollIDImplicitHeaderDoc(baseURL, token string) *schema.Doc {
	complete := schema.Predicate{Eq: &schema.PredicateEq{
		Path:  mustPath("body.request_metadata.complete"),
		Equal: vStr("true"),
	}}
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
			Path:   ptrValue(vStr("/api/v1/scroll")),
			// No req.Headers — auto-injection populates X-Scroll-ID.
		}},
		Response: schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{ScrollID: &schema.ScrollIDPagination{
			ScrollIDAt:   mustPath("request_metadata.scroll"),
			SendAs:       "header.X-Scroll-ID",
			CompleteWhen: &complete,
		}},
		Progress: schema.Progress{
			LatestEventTimestamp: &schema.TimestampProgress{
				EventTime: schema.EventTime{Path: mustPath("created_at")},
				Initial:   &schema.Initial{Lookback: vStr("24h")},
			},
		},
	}
}

// TestEndToEnd_CursorToken_ExplicitFormWinsOverAutoInject pins that when a
// template declares the slot itself (explicit form), the runtime does NOT
// auto-inject and the explicit Value's output is what reaches the wire.
// The producer's req.Query["cursor"] is a literal string here so the wire
// value is observable and distinguishable from the cursor token the
// auto-injection WOULD have written.
func TestEndToEnd_CursorToken_ExplicitFormWinsOverAutoInject(t *testing.T) {
	pages := []map[string]any{
		{
			"findings":    []map[string]any{{"id": "f1", "created_at": "2026-05-12T08:00:00Z"}},
			"next_cursor": "tok-2",
		},
		{
			"findings":    []map[string]any{{"id": "f2", "created_at": "2026-05-12T08:05:00Z"}},
			"next_cursor": "",
		},
	}
	var pageCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(pageCount.Add(1)) - 1
		// Every page should carry the explicit-form literal — auto-injection
		// MUST skip the slot when the template declared it.
		if got := r.URL.Query().Get("cursor"); got != "explicit-literal" {
			t.Errorf("page %d cursor = %q, want explicit-literal", idx, got)
		}
		writeJSON(w, http.StatusOK, pages[idx])
	}))
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":     {Type: "url", Default: server.URL},
			"api_key": {Type: "secret", Default: "test-token"},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.api_key")}},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/findings")),
			Query: map[string]schema.Value{
				// Explicit form: a literal that the auto-injection must
				// NOT overwrite. Distinguishable from the cursor token
				// the implicit form would have written ("tok-2").
				"cursor": vStr("explicit-literal"),
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
	r := &Runner{
		Doc:    doc,
		Sink:   &captureSink{},
		Store:  &MemoryStore{},
		Now:    fixedNow(),
		Client: server.Client(),
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := pageCount.Load(); got != 2 {
		t.Fatalf("server saw %d requests, want 2", got)
	}
}
