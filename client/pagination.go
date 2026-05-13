// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/p1llus/skopos/schema"
)

// paginationPlan describes the active pagination strategy for one drain.
// It has two responsibilities:
//
//  1. seed scope.fromPagination at the start of each iteration so
//     {from_pagination: <role>} Values evaluate to the right cursor field.
//  2. advance(...) after the producer step completes, mutating cursor and
//     returning want_more.
//
// # Strategy catalogue and role contract
//
// The role names referenced by {from_pagination: <role>} in a template are
// resolved against scope.fromPagination, which each plan populates in seed.
// The legal role names are strategy-specific:
//
//	none              (no roles — {from_pagination: ...} resolves to nil)
//	cursor_token      "token"      (cursor.token; absent on first iteration)
//	page_number       "page"       (cursor.page; defaults to 1 on first iteration)
//	offset            "offset"     (cursor.offset; defaults to 0 on first iteration)
//	                  "offset_end" (offset + batch_size; only when batch_size is set)
//	link_header       "next_link"  (cursor.next_link; absent on first iteration.
//	                               Templates read it via
//	                               {ref: cursor.next_link, default: <initial_url>}
//	                               in the request's url slot.)
//	next_url_in_body  "next_url"   (same template shape as link_header but
//	                               sourced from a body path rather than the
//	                               Link header.)
//	scroll_id         "scroll_id"  (cursor.scroll_id; templates read it via
//	                               {from_pagination: scroll_id} in the request
//	                               slot named by send_as. Empty on first
//	                               iteration so the server opens a new scroll
//	                               session, then echoes the id on every
//	                               subsequent page. Termination: complete_when
//	                               wins when set; otherwise scroll_id_at
//	                               resolving to a zero Value ends the drain.)
//	graphql_relay     "relay_cursor" (cursor.<cursor_var>; absent on first
//	                                 iteration. cursor_var is author-declared
//	                                 — typically "after". Templates read via
//	                                 {from_pagination: relay_cursor} inside
//	                                 GraphQL variables. Termination:
//	                                 has_next_page_at drives the drain — false
//	                                 terminates, true captures end_cursor_at
//	                                 into cursor.<cursor_var>.)
//
// Authors of a new pagination variant should document the role names their
// seed() writes here AND in the variant's own comment so template authors
// have a single place to look.
type paginationPlan interface {
	// seed populates scope.fromPagination with the current iteration's
	// signal. Must be called before request bodies / queries are evaluated.
	seed(s *scope)
	// advance updates scope.cursor from the producer-step result and
	// returns whether the loop should fetch another page. Called once per
	// iteration after the producer step's body is decoded.
	//
	// producerHeaders is the producer step's full response.Header — link_header
	// reads the Link header from it; body-cursor variants ignore it. nil is
	// allowed (an error-mode=warn iteration that decoded no body, for example).
	advance(s *scope, producerBody any, producerHeaders http.Header, events []any) (bool, error)
}

// autoInjectSlot describes a producer-step request slot the runtime should
// auto-populate from scope.fromPagination[role] when the template omits an
// explicit {from_pagination: <role>} Value at that slot. The `send_as` field
// on the pagination block is the canonical slot declaration; this struct is
// the runtime lowering of that declaration into the request. cursor_token
// and scroll_id are the only variants today that carry `send_as`.
type autoInjectSlot struct {
	kind string // "query" | "header"
	name string // param / header name (case-insensitive for headers)
	role string // {from_pagination: <role>} key — "token" | "scroll_id"
}

// paginationAutoInjector is the optional half of the paginationPlan
// interface implemented by strategies whose IR carries a `send_as` field
// (cursor_token, scroll_id). The runner type-asserts to discover the slot
// each iteration; plans that don't implement it produce no auto-injection
// (link_header / next_url_in_body / page_number / offset / graphql_relay /
// none all return signals via other roles and need no slot lowering).
type paginationAutoInjector interface {
	autoInjectSlot() autoInjectSlot
}

// parseSendAs splits the spec-defined "query.<param>" / "header.<name>"
// form into kind + name. Empty kind means the input was neither — callers
// treat that as "no auto-injection". The spec validator (checkSendAs)
// rejects bad shapes at load time so a non-empty cfg.SendAs always parses
// here; the empty-kind fallback exists to keep this helper total.
func parseSendAs(s string) (kind, name string) {
	if rest, ok := strings.CutPrefix(s, "query."); ok {
		return "query", rest
	}
	if rest, ok := strings.CutPrefix(s, "header."); ok {
		return "header", rest
	}
	return "", ""
}

// makePaginationPlan returns the driver for the document's active strategy.
// Unsupported variants are explicit errors so authors know the runner needs
// an additive PR or a code-emitting backend.
func makePaginationPlan(doc *schema.Doc) (paginationPlan, error) {
	switch {
	case doc.Pagination.None != nil:
		return &nonePagination{}, nil
	case doc.Pagination.CursorToken != nil:
		return &cursorTokenPagination{cfg: doc.Pagination.CursorToken}, nil
	case doc.Pagination.PageNumber != nil:
		return &pageNumberPagination{cfg: doc.Pagination.PageNumber}, nil
	case doc.Pagination.Offset != nil:
		return &offsetPagination{cfg: doc.Pagination.Offset}, nil
	case doc.Pagination.LinkHeader != nil:
		return newLinkHeaderPagination(doc.Pagination.LinkHeader)
	case doc.Pagination.NextURLInBody != nil:
		return &nextURLInBodyPagination{cfg: doc.Pagination.NextURLInBody}, nil
	case doc.Pagination.ScrollID != nil:
		return &scrollIDPagination{cfg: doc.Pagination.ScrollID}, nil
	case doc.Pagination.GraphQLRelay != nil:
		return &graphQLRelayPagination{cfg: doc.Pagination.GraphQLRelay}, nil
	}
	return nil, fmt.Errorf("pagination: no variant set")
}

// ---- none ----

type nonePagination struct{}

func (p *nonePagination) seed(*scope) {}
func (p *nonePagination) advance(*scope, any, http.Header, []any) (bool, error) {
	return false, nil
}

// ---- cursor_token ----
//
// send_as is the canonical slot declaration for the token. The runner
// supports BOTH forms:
//
//   - Explicit: the template writes {from_pagination: token} at the
//     desired request slot (query.<param> or header.<name>). seed() exposes
//     the cursor under role "token" and the template's existing slot
//     evaluates it. This form is what every day-one template uses.
//   - Implicit: the template omits the {from_pagination: ...} Value. The
//     runtime lowers send_as into an auto-injection at that slot on the
//     producer step's request (see autoInjectSlot below + executeRequest
//     in http.go). Templates that take this path get the same wire shape
//     without the boilerplate.
//
// When the template declares the slot itself (explicit form), the runtime
// skips auto-injection — the explicit Value wins. Detection is by IR
// presence (req.Query / req.Headers carries the key), not by runtime value,
// so a first-iteration nil token doesn't get double-handled.
type cursorTokenPagination struct {
	cfg *schema.CursorTokenPagination
}

func (p *cursorTokenPagination) autoInjectSlot() autoInjectSlot {
	kind, name := parseSendAs(p.cfg.SendAs)
	return autoInjectSlot{kind: kind, name: name, role: "token"}
}

func (p *cursorTokenPagination) seed(s *scope) {
	// Expose cursor.token via {from_pagination: token}. First iteration:
	// cursor.token is unset, so the role evaluates to nil. Authors should
	// either guard with {select} or accept an absent param on the first
	// request — both work because url.Values.Set with empty string still
	// includes the key but we skip nil values in query encoding (http.go).
	if v, ok := s.cursor["token"]; ok {
		s.fromPagination["token"] = v
	} else {
		delete(s.fromPagination, "token")
	}
}

func (p *cursorTokenPagination) advance(s *scope, body any, _ http.Header, _ []any) (bool, error) {
	got, ok, err := lookupBodyPath(body, pathParts(p.cfg.TokenAt))
	if err != nil {
		return false, fmt.Errorf("pagination.cursor_token.token_at: %w", err)
	}
	if !ok || isZeroRuntime(got) {
		// cursor_token terminates when token_at resolves to a zero Value.
		// Clear the cursor so the next drain starts fresh.
		delete(s.cursor, "token")
		return false, nil
	}
	s.cursor["token"] = got
	return true, nil
}

// ---- page_number ----

type pageNumberPagination struct {
	cfg *schema.PageNumberPagination
}

func (p *pageNumberPagination) seed(s *scope) {
	// First iteration: cursor.page absent → expose 1 by convention.
	// Subsequent iterations use whatever advance() last wrote.
	if v, ok := s.cursor["page"]; ok {
		s.fromPagination["page"] = v
	} else {
		s.fromPagination["page"] = int64(1)
		s.cursor["page"] = int64(1)
	}
}

func (p *pageNumberPagination) advance(s *scope, body any, _ http.Header, events []any) (bool, error) {
	cur, _ := asInt64(s.cursor["page"])
	if cur == 0 {
		cur = 1
	}

	// has_more_at takes precedence when set: false → stop, true → advance.
	if !p.cfg.HasMoreAt.IsEmpty() {
		got, ok, err := lookupBodyPath(body, pathParts(p.cfg.HasMoreAt))
		if err != nil {
			return false, fmt.Errorf("pagination.page_number.has_more_at: %w", err)
		}
		if !ok {
			// No signal → assume stop. Conservative; aligns with the spec's
			// "missing field is a zero Value" rule.
			return false, nil
		}
		more, err := toBool(got)
		if err != nil {
			return false, fmt.Errorf("pagination.page_number.has_more_at: %w", err)
		}
		if !more {
			// Reset for next drain.
			s.cursor["page"] = int64(1)
			return false, nil
		}
		s.cursor["page"] = cur + 1
		return true, nil
	}

	// No has_more_at signal: stop when the page came back empty, or when
	// batch_size is set and we got fewer events than that. Otherwise
	// advance optimistically.
	if len(events) == 0 {
		s.cursor["page"] = int64(1)
		return false, nil
	}
	if p.cfg.BatchSize != nil {
		got, err := s.evalValue(*p.cfg.BatchSize)
		if err != nil {
			return false, fmt.Errorf("pagination.page_number.batch_size: %w", err)
		}
		bs, err := toInt(got)
		if err != nil {
			return false, fmt.Errorf("pagination.page_number.batch_size: %w", err)
		}
		if int64(len(events)) < bs {
			s.cursor["page"] = int64(1)
			return false, nil
		}
	}
	s.cursor["page"] = cur + 1
	return true, nil
}

// ---- offset ----

type offsetPagination struct {
	cfg *schema.OffsetPagination
}

func (p *offsetPagination) seed(s *scope) {
	// First iteration: cursor.offset absent → seed 0 by convention and
	// write it back to cursor so {ref: cursor.offset} can resolve too.
	// Subsequent iterations use whatever advance() last wrote.
	cur, ok := s.cursor["offset"]
	if !ok {
		cur = int64(0)
		s.cursor["offset"] = cur
	}
	s.fromPagination["offset"] = cur

	// offset_end = offset + batch_size; the role exists for APIs that take
	// an exclusive end index. Only resolvable when batch_size is declared
	// AND evaluates cleanly. Templates that don't declare batch_size simply
	// never reference offset_end.
	//
	// On an eval failure here we drop offset_end and continue. The same
	// error re-surfaces from advance() and becomes the iteration's
	// canonical failure point — UNLESS the page comes back empty, in
	// which case advance()'s empty-page short-circuit terminates before
	// re-evaluating batch_size. The log breadcrumb here is the operator's
	// only signal that the misshapen offset_end on the bootstrap request
	// was deliberate-but-broken rather than just absent.
	if p.cfg.BatchSize == nil {
		delete(s.fromPagination, "offset_end")
		return
	}
	bs, err := evalOffsetBatchSize(s, *p.cfg.BatchSize)
	if err != nil {
		delete(s.fromPagination, "offset_end")
		if s.logger != nil {
			s.logger.Printf("client: pagination.offset.batch_size eval failed in seed (offset_end dropped from this iteration): %v", err)
		}
		return
	}
	curInt, _ := asInt64(cur)
	s.fromPagination["offset_end"] = curInt + bs
}

func (p *offsetPagination) advance(s *scope, _ any, _ http.Header, events []any) (bool, error) {
	cur, _ := asInt64(s.cursor["offset"])

	// Empty page ends the stream. Reset to 0 so the next drain starts
	// fresh from the top (mirrors page_number's terminate-and-reset).
	if len(events) == 0 {
		s.cursor["offset"] = int64(0)
		return false, nil
	}

	if p.cfg.BatchSize != nil {
		bs, err := evalOffsetBatchSize(s, *p.cfg.BatchSize)
		if err != nil {
			return false, fmt.Errorf("pagination.offset.batch_size: %w", err)
		}
		// Short page = end of stream. Same shape as page_number's
		// batch_size short-page termination.
		if int64(len(events)) < bs {
			s.cursor["offset"] = int64(0)
			return false, nil
		}
		s.cursor["offset"] = cur + bs
		return true, nil
	}

	// No batch_size declared: advance by the observed event count. That's
	// the only authoritative "where the next page starts" signal available
	// when the IR doesn't carry the requested page size.
	s.cursor["offset"] = cur + int64(len(events))
	return true, nil
}

// evalOffsetBatchSize centralises the batch_size eval + int coerce so seed()
// and advance() can't drift on coercion rules.
func evalOffsetBatchSize(s *scope, v schema.Value) (int64, error) {
	got, err := s.evalValue(v)
	if err != nil {
		return 0, err
	}
	return toInt(got)
}

// ---- link_header ----
//
// link_header follows RFC 5988 Link headers: every response carries a
// comma-delimited list of <URI>; rel="<rel>"[; ...] entries, and the next
// page sits behind rel="next". The runner parses the producer step's
// response.Header["Link"] for that entry and stashes the URL in
// cursor.next_link. Templates read it back via the standard Value forms —
// the natural shape is the request's url slot set to {ref: cursor.next_link,
// default: <initial_url>}, so the first iteration uses the bootstrap URL
// and every subsequent iteration follows the server's link.
//
// Termination: the loop stops when the producer response carries no Link
// header at all, no rel="next" entry, or an entry whose URI is empty. On
// any of those, cursor.next_link is cleared so the next drain starts from
// the bootstrap URL again.
//
// The optional pattern field is a Go-flavoured regex applied to the joined
// Link header value; its first capture group is taken as the next URL.
// Empty pattern means "RFC 5988 default": split on commas, find the
// <URI>; rel="next" entry, return the URI. Pattern is compiled once at
// plan construction so a bad regex surfaces before any HTTP traffic.
type linkHeaderPagination struct {
	cfg     *schema.LinkHeaderPagination
	pattern *regexp.Regexp
}

func newLinkHeaderPagination(cfg *schema.LinkHeaderPagination) (*linkHeaderPagination, error) {
	p := &linkHeaderPagination{cfg: cfg}
	if cfg.Pattern != "" {
		re, err := regexp.Compile(cfg.Pattern)
		if err != nil {
			return nil, fmt.Errorf("pagination.link_header.pattern: %w", err)
		}
		p.pattern = re
	}
	return p, nil
}

func (p *linkHeaderPagination) seed(s *scope) {
	// First iteration: cursor.next_link absent → role evaluates to nil and
	// the template's {ref: cursor.next_link, default: <bootstrap_url>}
	// branch wins. Subsequent iterations expose whatever advance() last
	// wrote.
	if v, ok := s.cursor["next_link"]; ok {
		s.fromPagination["next_link"] = v
	} else {
		delete(s.fromPagination, "next_link")
	}
}

func (p *linkHeaderPagination) advance(s *scope, _ any, headers http.Header, _ []any) (bool, error) {
	next := parseNextLink(headers.Values("Link"), p.pattern)
	if next == "" {
		// No rel="next" → end of stream. Clear cursor.next_link so the
		// NEXT drain starts from the bootstrap URL again (mirrors the
		// reset behaviour of cursor_token / offset / page_number).
		delete(s.cursor, "next_link")
		return false, nil
	}
	s.cursor["next_link"] = next
	return true, nil
}

// parseNextLink scans a multi-value Link header for a rel="next" entry and
// returns the URL (without surrounding angle brackets). Returns "" if no
// rel="next" entry is present or the header is empty.
//
// When re is non-nil, it overrides RFC 5988 detection: the first capture
// group of the first match against the joined header value is returned.
// Templates that need to handle non-standard Link-like headers (e.g.
// X-Next-Page from APIs that misuse Link's shape) get this escape hatch.
func parseNextLink(values []string, re *regexp.Regexp) string {
	if len(values) == 0 {
		return ""
	}
	// Combine multi-line headers into one comma-delimited string. RFC 7230
	// allows the same header to appear on multiple lines with identical
	// semantics — joining is the standard normalisation.
	joined := strings.Join(values, ", ")
	if strings.TrimSpace(joined) == "" {
		return ""
	}
	if re != nil {
		m := re.FindStringSubmatch(joined)
		if len(m) >= 2 {
			return strings.TrimSpace(m[1])
		}
		return ""
	}
	// RFC 5988 default: entries separated by commas, each "<URI>; rel=...".
	// Splitting on commas is naïve in the general case (a quoted comma in
	// a param value would split incorrectly) but real Link headers don't
	// embed commas inside quoted params — the URL is inside <> and the
	// rel value is a simple identifier.
	for _, entry := range strings.Split(joined, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		lt := strings.Index(entry, "<")
		gt := strings.Index(entry, ">")
		if lt < 0 || gt <= lt {
			continue
		}
		uri := strings.TrimSpace(entry[lt+1 : gt])
		if uri == "" {
			continue
		}
		params := entry[gt+1:]
		for _, raw := range strings.Split(params, ";") {
			pair := strings.TrimSpace(raw)
			if !strings.HasPrefix(pair, "rel") {
				continue
			}
			// Accept rel=next, rel="next", and the multi-rel form
			// rel="next first" — split on whitespace and look for "next".
			eq := strings.IndexByte(pair, '=')
			if eq < 0 {
				continue
			}
			val := strings.Trim(strings.TrimSpace(pair[eq+1:]), "\"")
			for _, r := range strings.Fields(val) {
				if r == "next" {
					return uri
				}
			}
		}
	}
	return ""
}

// ---- next_url_in_body ----
//
// next_url_in_body is the body-cursor twin of link_header: every response
// carries a fully-formed next-page URL at the configured body path, and the
// runner stashes it in cursor.next_url. Templates read it back the same way
// they read cursor.next_link — the natural shape is the request's url slot
// set to {ref: cursor.next_url, default: <initial_url>}, so the first
// iteration uses the bootstrap URL and every subsequent iteration follows
// whatever the previous page named.
//
// Termination: the loop stops when next_url_at resolves to a missing path,
// to nil, or to a non-string / empty-string Value. On any of those,
// cursor.next_url is cleared so the next drain restarts from the bootstrap
// URL again (mirrors the reset behaviour of cursor_token / offset /
// page_number / link_header).
type nextURLInBodyPagination struct {
	cfg *schema.NextURLInBodyPagination
}

func (p *nextURLInBodyPagination) seed(s *scope) {
	// First iteration: cursor.next_url absent → role evaluates to nil and
	// the template's {ref: cursor.next_url, default: <bootstrap_url>}
	// branch wins. Subsequent iterations expose whatever advance() last
	// wrote. Same shape as link_header's seed().
	if v, ok := s.cursor["next_url"]; ok {
		s.fromPagination["next_url"] = v
	} else {
		delete(s.fromPagination, "next_url")
	}
}

func (p *nextURLInBodyPagination) advance(s *scope, body any, _ http.Header, _ []any) (bool, error) {
	got, ok, err := lookupBodyPath(body, pathParts(p.cfg.NextURLAt))
	if err != nil {
		return false, fmt.Errorf("pagination.next_url_in_body.next_url_at: %w", err)
	}
	if !ok || got == nil {
		delete(s.cursor, "next_url")
		return false, nil
	}
	// Coerce to string and treat empty / non-string as terminal. Mirrors
	// cursor_token's isZeroRuntime stance: a missing / empty next-URL means
	// "end of stream", clear the cursor so the next drain bootstraps again.
	next, isStr := got.(string)
	if !isStr || next == "" {
		delete(s.cursor, "next_url")
		return false, nil
	}
	s.cursor["next_url"] = next
	return true, nil
}

// ---- scroll_id ----
//
// scroll_id maintains a server-side scroll session: the first request opens
// the session (the scroll_id slot is empty), and every response echoes a
// scroll id at scroll_id_at that subsequent requests must replay verbatim.
// Templates expose the id via {from_pagination: scroll_id} in the slot named
// by send_as (typically a query or header param). The first iteration's
// {from_pagination: scroll_id} resolves to nil; the runner skips nil values
// in query / header encoding (http.go), so the bootstrap request goes out
// without the param and the server opens a new session.
//
// Termination:
//
//   - When complete_when is declared, it is evaluated against the producer
//     body (body.<path> refs resolve against the just-decoded response).
//     A true result ends the drain and clears cursor.scroll_id. This shape
//     mirrors async_job.poll.complete_when — same predicate plumbing, same
//     {body: ...} scope mechanic.
//   - When complete_when is absent, the loop terminates as soon as
//     scroll_id_at resolves to a zero Value (missing path, nil, or empty
//     string). This matches the scroll-session contract: the server signals
//     session-drained by omitting the next id.
//
// On termination cursor.scroll_id is cleared so the next drain opens a
// fresh session (mirrors the reset behaviour of cursor_token / offset /
// page_number / link_header / next_url_in_body).
//
// send_as is the canonical slot declaration for the scroll id. As with
// cursor_token, the runner supports BOTH forms (explicit
// {from_pagination: scroll_id} at the desired slot, OR implicit lowering
// of send_as into a producer-step auto-injection). Detection is by IR
// presence — explicit form is recognised when the template's req.Query /
// req.Headers names the slot, and auto-injection is skipped for that
// request. See autoInjectSlot below and the executeRequest implementation
// in http.go.
type scrollIDPagination struct {
	cfg *schema.ScrollIDPagination
}

func (p *scrollIDPagination) autoInjectSlot() autoInjectSlot {
	kind, name := parseSendAs(p.cfg.SendAs)
	return autoInjectSlot{kind: kind, name: name, role: "scroll_id"}
}

func (p *scrollIDPagination) seed(s *scope) {
	// First iteration: cursor.scroll_id absent → role evaluates to nil and
	// the request's send_as slot encodes no value (http.go skips nil
	// query / header values). Subsequent iterations expose whatever
	// advance() last wrote. Same shape as cursor_token's seed().
	if v, ok := s.cursor["scroll_id"]; ok {
		s.fromPagination["scroll_id"] = v
	} else {
		delete(s.fromPagination, "scroll_id")
	}
}

func (p *scrollIDPagination) advance(s *scope, body any, _ http.Header, _ []any) (bool, error) {
	// complete_when (if declared) wins over the zero-id default rule. We
	// evaluate it against the producer body via the same {body: ...} scope
	// mechanic async_job.poll uses for its complete_when — body.<path>
	// resolves against the just-decoded response.
	if p.cfg.CompleteWhen != nil {
		prev := s.body
		s.body = body
		done, err := s.evalPredicate(*p.cfg.CompleteWhen)
		s.body = prev
		if err != nil {
			return false, fmt.Errorf("pagination.scroll_id.complete_when: %w", err)
		}
		if done {
			delete(s.cursor, "scroll_id")
			return false, nil
		}
	}

	// Read the next scroll id from the body. When complete_when is set and
	// returned false, we still need the freshest id for the next request.
	// When complete_when is absent, a zero id IS the termination signal.
	got, ok, err := lookupBodyPath(body, pathParts(p.cfg.ScrollIDAt))
	if err != nil {
		return false, fmt.Errorf("pagination.scroll_id.scroll_id_at: %w", err)
	}
	if !ok || isZeroRuntime(got) {
		delete(s.cursor, "scroll_id")
		return false, nil
	}
	s.cursor["scroll_id"] = got
	return true, nil
}

// ---- graphql_relay ----
//
// graphql_relay follows the GraphQL Relay cursor-connections spec: every
// response carries a `pageInfo { hasNextPage, endCursor }` block, and the
// next page is requested by passing `endCursor` back as the GraphQL variable
// named by cursor_var (typically `after`). The runner stashes endCursor at
// cursor.<cursor_var> — the cursor key name is author-declared.
//
// Templates expose the cursor via {from_pagination: relay_cursor} inside
// the GraphQL variables map. On the first iteration cursor.<cursor_var> is
// absent → the role evaluates to nil → the GraphQL variable rides as JSON
// null, which Relay servers treat as "from the start of the connection".
//
// Termination: has_next_page_at is the authoritative signal. False → end
// the drain and clear cursor.<cursor_var>; true → capture end_cursor_at
// into cursor.<cursor_var> for the next iteration. A missing has_next_page
// path is treated as false (conservative). On termination the cursor is
// cleared so the next drain starts a fresh connection traversal (mirrors
// the reset behaviour of cursor_token / offset / page_number / link_header /
// next_url_in_body / scroll_id).
type graphQLRelayPagination struct {
	cfg *schema.GraphQLRelayPagination
}

func (p *graphQLRelayPagination) seed(s *scope) {
	// First iteration: cursor.<cursor_var> absent → role evaluates to nil
	// and the GraphQL variable rides as null. Subsequent iterations expose
	// whatever advance() last wrote at cursor.<cursor_var>. Same shape as
	// cursor_token's seed(), but the cursor key is author-named.
	if v, ok := s.cursor[p.cfg.CursorVar]; ok {
		s.fromPagination["relay_cursor"] = v
	} else {
		delete(s.fromPagination, "relay_cursor")
	}
}

func (p *graphQLRelayPagination) advance(s *scope, body any, _ http.Header, _ []any) (bool, error) {
	hasNext, ok, err := lookupBodyPath(body, pathParts(p.cfg.HasNextPageAt))
	if err != nil {
		return false, fmt.Errorf("pagination.graphql_relay.has_next_page_at: %w", err)
	}
	if !ok {
		// Missing signal → conservative terminate. Mirrors page_number's
		// has_more_at handling.
		delete(s.cursor, p.cfg.CursorVar)
		return false, nil
	}
	more, err := toBool(hasNext)
	if err != nil {
		return false, fmt.Errorf("pagination.graphql_relay.has_next_page_at: %w", err)
	}
	if !more {
		delete(s.cursor, p.cfg.CursorVar)
		return false, nil
	}

	// has_next_page=true: the response MUST carry a fresh end_cursor for
	// the next request. A missing / nil / empty end_cursor with
	// has_next_page=true is a server contract violation, but the
	// conservative read is "we can't request the next page without a
	// cursor" — terminate and clear so the next drain can recover.
	got, ok, err := lookupBodyPath(body, pathParts(p.cfg.EndCursorAt))
	if err != nil {
		return false, fmt.Errorf("pagination.graphql_relay.end_cursor_at: %w", err)
	}
	if !ok || isZeroRuntime(got) {
		delete(s.cursor, p.cfg.CursorVar)
		return false, nil
	}
	s.cursor[p.cfg.CursorVar] = got
	return true, nil
}

// Note on send_as: authors can EITHER write {from_pagination: ...} at the
// desired request slot (explicit form), OR rely on the runtime to
// auto-inject the token into the slot named by send_as (implicit form). The
// lowering lives in executeRequest (http.go): for the producer step the
// runner reads the slot from autoInjectSlot, checks the template's
// req.Query / req.Headers for an existing declaration of that key, and only
// writes the value from scope.fromPagination[role] when the template did
// not declare the slot itself.
