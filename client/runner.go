package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/p1llus/skopos/schema"
)

// Runner is the in-process IR interpreter. Each call to Drain executes one
// pull session against r.Doc, persisting state through r.Store and emitting
// events to r.Sink.
//
// The Runner does not own scheduling — the caller decides whether to invoke
// Drain once (--once) or in a loop with sleeps (--interval). This mirrors
// the database/sql pattern: each Run() is one goroutine; the caller
// multiplexes.
//
// # Concurrency
//
// One Runner is one logical pull source. Drain mutates internal scope state
// (cursor, extract, steps, fromPagination, fromProgress) and the deferred
// store.Save reads what each iteration wrote. Calling Drain concurrently on
// the same Runner WILL race on scope state and interleave events into Sink.
// Don't do it.
//
// Fan-out across N pull sources is the caller's responsibility: build N
// Runner values (each with its own *schema.Doc, Store, Sink) and call Drain on
// each from its own goroutine. The Runner type holds no package-global
// state, so independent Runners are independent. Sink and Store
// implementations shared between Runners MUST be safe for concurrent use;
// the bundled JSONLSink and MemoryStore are.
//
// # Loop contract
//
// One Drain executes the following sequence:
//
//  1. store.Load → seed scope.state from the snapshot, merging IR defaults.
//  2. progress.seed runs ONCE per drain. It pins {from_progress: <role>}
//     signals (e.g. cursor.last_timestamp → since=<...>) so every page in
//     this drain shares the same window-start. cursor.last_timestamp only
//     advances at the END of the drain via progress.advance().
//  3. For each iteration:
//     a. pagination.seed populates {from_pagination: <role>} for THIS page
//     (cursor.token / cursor.page change per page).
//     b. scope.extract and scope.steps reset — they are per-iteration bindings.
//     c. Every request runs in declared order. extract[] writes hit extract
//     (default) or cursor; step ids cache decoded bodies in scope.steps.
//     d. The producer step's body is located via response.events_at;
//     events are emitted to Sink.
//     e. pagination.advance updates cursor and returns want_more.
//  4. After the loop, progress.advance runs ONCE over accumulated events
//     and writes cursor.last_timestamp from the drain's high-water mark.
//  5. store.Save persists the snapshot via a deferred call — even on error,
//     so a partial drain (e.g. async_job that hit phase=poll halfway) does
//     not lose hard-won progress.
//
// # Error policy
//
// A non-success HTTP response (status not in the request's expect_status
// set, or 200 when expect_status is unset) is dispatched in this order:
//
//  1. requests[].on_status[<code>] — per-step override for a SPECIFIC status.
//     If present, takes precedence over the document-level error.mode. The
//     verbs are:
//
//     - "skip":            log + continue to the next request in the same iteration.
//     - "fail":            abort the drain immediately (Drain returns non-nil).
//     - "empty_events":    treat as a successful empty page; the cursor still
//     advances normally.
//     - "invalidate_cache": clears every OAuth2 token-cache slot reachable
//     from doc.Auth (state.<store_in> + cursor.__oauth2_<store_in>_expires_at,
//     including each branch of an auth.multi_mode dispatch) and then
//     advances as if the page came back empty. The next request misses the
//     cache and forces a fresh token fetch. When the active auth has no
//     cache block (or no OAuth2 grant), the verb degrades to empty_events
//     with a log line — there is nothing to invalidate.
//
//  2. document.error.mode — fallback for statuses not named in on_status.
//
//     - "standard" (default): iteration ends here; the cursor does NOT
//     advance (progress.advance is skipped). The next drain re-tries the
//     same window.
//     - "warn":               log the failure and continue as if the page
//     came back empty; the cursor advances normally.
//     - "fail":               Drain returns a non-nil error. Whatever cursor
//     state was written this drain is still persisted
//     by the deferred Save so the next drain resumes
//     from the last good progress mark.
//
// Non-status errors (DNS, connection refused, JSON decode failure) skip the
// on_status lookup entirely and dispatch directly through error.mode.
//
// # MaxPages cap
//
// A runaway pagination loop (a buggy server returning the same cursor_token
// forever) is bounded by Runner.MaxPages. Hitting the cap returns an error
// and persists the state reached so far. Default 10_000 — comfortable for
// any realistic API while still finite.
type Runner struct {
	// Doc is the IR document to execute. Required.
	Doc *schema.Doc

	// Store persists Snapshot between drains. Optional; defaults to a
	// MemoryStore (state is lost between Drain calls).
	Store Store

	// Sink receives emitted events. Required.
	Sink Sink

	// Client is the HTTP client used for every request. Optional; defaults
	// to a client with a 30-second per-request timeout. Callers needing a
	// different timeout, custom transport, or proxy should plug in their own
	// *http.Client — but should ALWAYS set Client.Timeout to a finite value
	// so a hung server cannot wedge Drain until ctx cancellation.
	Client *http.Client

	// Now is the clock. Optional; defaults to time.Now. Useful for tests.
	Now func() time.Time

	// Logger receives operational diagnostics (skipped steps, retried
	// requests, etc.). Optional; defaults to log.Default().
	Logger *log.Logger

	// MaxPages caps the number of iterations one Drain can run before it
	// returns errMaxPagesExceeded.
	//
	//   0 (the default) → applies the built-in default (10_000); enough
	//                     headroom for any realistic API while still bounding
	//                     a buggy server returning the same cursor_token
	//                     forever.
	//   > 0             → uses the supplied value as the cap.
	//   < 0             → disables the cap entirely (NOT recommended outside
	//                     tests).
	MaxPages int

	// Tracer receives one Exchange per HTTP request/response pair executed
	// during the drain. Optional; nil disables per-exchange capture (the
	// operational Logger still fires). See trace.go for the redaction
	// policy — the trace is intentionally body-metadata-only by default
	// so secret-bearing request/response bodies (token-exchange endpoints
	// in particular) cannot leak through.
	//
	// When set, Tracer is called serially from the same goroutine that
	// runs Drain, in the order requests run within an iteration. A Tracer
	// shared between concurrent Runners MUST be safe for concurrent calls;
	// the bundled JSONLTracer is.
	Tracer Tracer
}

// defaultHTTPTimeout is the per-request timeout applied when the caller did
// not supply an *http.Client. It bounds connect+TLS+request+response — a
// 30-second wall covers slow APIs with comfortable headroom while still
// putting a finite ceiling on a hung server. Callers needing different
// per-request budgets should plug in their own client.
const defaultHTTPTimeout = 30 * time.Second

// defaultMaxPages is the per-drain iteration cap when Runner.MaxPages is
// zero. 10_000 is well beyond any realistic API drain (a 50-events-per-page
// endpoint with 500k events fits) but still bounds a buggy server returning
// the same cursor_token forever.
const defaultMaxPages = 10_000

// errMaxPagesExceeded is returned by Drain when the iteration cap fires.
var errMaxPagesExceeded = errors.New("client: drain exceeded MaxPages cap (suspected pagination loop)")

// Drain runs one full pull session: paginate until want_more=false (or, for
// async_job, until the phase machine hits a wait state). It persists state
// through r.Store before returning.
//
// Drain returns nil for the normal "iteration completed cleanly, see you
// next tick" outcome. error.mode: warn surfaces non-success responses but
// keeps Drain's return nil. error.mode: fail surfaces non-success responses
// as a non-nil error.
func (r *Runner) Drain(ctx context.Context) (retErr error) {
	if r.Doc == nil {
		return errors.New("Runner.Doc is required")
	}
	if r.Sink == nil {
		return errors.New("Runner.Sink is required")
	}
	store := r.Store
	if store == nil {
		store = &MemoryStore{}
	}
	client := r.Client
	if client == nil {
		// Don't reuse http.DefaultClient: it has no timeout, so a hung
		// server would wedge Drain until ctx cancellation. Construct a
		// fresh client with a finite per-request budget.
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	logger := r.Logger
	if logger == nil {
		logger = log.Default()
	}
	maxPages := r.MaxPages
	if maxPages == 0 {
		maxPages = defaultMaxPages
	}

	snap, err := store.Load()
	if err != nil {
		return fmt.Errorf("store.Load: %w", err)
	}
	s, err := newScope(r.Doc, snap, r.Now)
	if err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	s.logger = logger

	pagination, err := makePaginationPlan(r.Doc)
	if err != nil {
		return fmt.Errorf("pagination: %w", err)
	}
	progress, err := makeProgressPlan(r.Doc)
	if err != nil {
		return fmt.Errorf("progress: %w", err)
	}

	// Register the deferred Save+Flush BEFORE progress.seed runs so that a
	// half-written window (e.g. time_window seeds window_start successfully
	// then fails on window_end) is still persisted.
	defer func() {
		// Persist whatever state we reached, even on error, so a partial
		// drain doesn't lose hard-won progress (e.g. async_job hit
		// phase=poll halfway). When the drain itself had no error, a
		// store.Save failure becomes the drain's error — silently
		// returning success while the operator's state file failed to
		// persist would cause the next drain to re-issue the same window
		// and double-emit events.
		//
		// ctx cancellation between iterations is treated as a graceful
		// end-of-drain: the cursor advance for the just-finished iteration
		// IS persisted (identical to a clean termination of pagination).
		if err := store.Save(s.snapshot()); err != nil {
			logger.Printf("client: store.Save failed: %v", err)
			if retErr == nil {
				retErr = fmt.Errorf("store.Save: %w", err)
			}
		}
		if err := r.Sink.Flush(); err != nil {
			logger.Printf("client: sink.Flush failed: %v", err)
			if retErr == nil {
				retErr = fmt.Errorf("sink.Flush: %w", err)
			}
		}
	}()

	// progress.seed runs ONCE per drain: it pins {from_progress: ...}
	// signals (e.g. cursor.last_timestamp → since=<...>) so every page
	// fetched during this drain uses the same window. cursor.last_timestamp
	// only advances at the END of the drain via progress.advance(). The
	// variant errors already carry a "progress.<variant>" prefix, so wrap
	// here without re-prefixing "progress.seed:" to avoid double labels.
	if err := progress.seed(s); err != nil {
		return err
	}

	errMode := errorMode(r.Doc)

	var allEvents []any

	// drainCanAdvance tracks whether progress.advance should run at the
	// end of the drain. An iteration that ends via standard-mode-on-
	// unexpected-status sets this false so the cursor does NOT jump to
	// a high-water mark drawn from partial data.
	drainCanAdvance := true

	// pendingPlaceholderEmpty is the two-pass placeholder_event flag: when
	// the prior iteration produced zero events AND its pagination.advance
	// said "more to fetch", we defer the placeholder until we've actually
	// entered the next iteration. Confirming "next iteration was entered"
	// (rather than just promised by advance) means ctx-cancel, maxPages,
	// or a standard-mode-on-unexpected-status break in the next iteration
	// suppresses the placeholder — we only claim "the drain made progress
	// past an empty page" when we really did.
	pendingPlaceholderEmpty := false

	wantMore := true
	pages := 0
	for wantMore {
		if err := ctx.Err(); err != nil {
			return err
		}
		if maxPages > 0 && pages >= maxPages {
			return fmt.Errorf("%w: %d iterations", errMaxPagesExceeded, pages)
		}
		pages++

		// Two-pass placeholder_event emission. We're at the start of an
		// iteration that ctx + maxPages have already cleared, so the prior
		// iteration's "want_more=true" is now CONFIRMED (we entered this
		// one). Emit the placeholder for the prior empty page before this
		// iteration's events so wire ordering matches drain ordering.
		if pendingPlaceholderEmpty && r.Doc.Response.PlaceholderEvent != nil {
			ph, err := s.evalValue(*r.Doc.Response.PlaceholderEvent)
			if err != nil {
				return fmt.Errorf("placeholder_event: %w", err)
			}
			if ph != nil {
				if err := r.Sink.Emit(ph); err != nil {
					return fmt.Errorf("sink.Emit: %w", err)
				}
				allEvents = append(allEvents, ph)
			}
		}
		pendingPlaceholderEmpty = false

		// Re-seed pagination each iteration (cursor.token / cursor.page
		// change per page). progress.seed is NOT called here — it ran
		// once before the loop, pinning the drain's window-start.
		pagination.seed(s)

		// Reset per-iteration scratch bindings. scope.extract and
		// scope.steps are PER-ITERATION lifetimes by design: a later
		// iteration reads its own fresh extract bag, not the previous
		// page's. Cursor mutations (which survive across iterations) go
		// through scope.cursor in extract[].target=cursor or in pagination
		// drivers' advance().
		s.extract = make(map[string]any)
		s.steps = make(map[string]any)

		out, err := r.runIteration(ctx, client, logger, s, pagination, progress, errMode, pages)
		if err != nil {
			return err
		}
		if out.fatal {
			return fmt.Errorf("client: drain aborted on error.mode=fail")
		}

		for _, ev := range out.events {
			if err := r.Sink.Emit(ev); err != nil {
				return fmt.Errorf("sink.Emit: %w", err)
			}
		}
		allEvents = append(allEvents, out.events...)

		if !out.advance {
			// standard-mode-on-unexpected-status: cursor must not move
			// (neither pagination.advance nor end-of-drain progress.advance).
			// The next drain re-tries the same window from where it
			// started.
			drainCanAdvance = false
			break
		}

		// Only producer iterations should drive pagination.advance: an
		// async_job non-producer phase (submit / poll) has no producer
		// body to advance against. The validator forbids async_job with
		// non-none pagination, so this is belt-and-braces today.
		var paginationMore bool
		if out.producerIteration {
			paginationMore, err = pagination.advance(s, out.producerBody, out.producerHeaders, out.events)
			if err != nil {
				return fmt.Errorf("pagination.advance: %w", err)
			}
		}

		// Mark this iteration as "empty + want_more" for the next loop
		// turn's confirmation step. Gated on paginationMore so the
		// terminal empty page (advance=false) doesn't queue a placeholder
		// that would never have a confirming next iteration.
		if paginationMore && out.producerIteration && len(out.events) == 0 {
			pendingPlaceholderEmpty = true
		}

		wantMore = out.iterMore || paginationMore
	}

	if !drainCanAdvance {
		return nil
	}

	// Advance progress ONCE per drain over the accumulated events. This
	// is what writes cursor.last_timestamp — multi-page drains see the
	// same since=<...> across all pages, then the cursor jumps to the
	// drain's high-water mark in one move.
	if err := progress.advance(s, allEvents); err != nil {
		return fmt.Errorf("progress.advance: %w", err)
	}

	return nil
}

// iterationResult bundles what runIteration produces. Named fields keep the
// (now substantial) signal set self-documenting at the call site.
type iterationResult struct {
	producerBody    any
	producerHeaders http.Header
	events          []any
	// iterMore is set when the phase machine (async_job) wants another
	// iteration. Independent of pagination's "more pages" signal.
	iterMore bool
	// producerIteration is true when at least one request in this iteration
	// was the events-bearing producer (every non-async iteration; for async_job
	// only the producer-phase iteration). Drain gates pagination.advance on
	// this so non-producer phases don't run pagination advance against a stale
	// or nil producer body.
	producerIteration bool
	// advance is true when the iteration ended cleanly and the cursor is
	// allowed to move (pagination.advance for the iteration; progress.advance
	// at end of drain). Set false on standard-mode-on-unexpected-status so the
	// drain re-tries the same window next tick.
	advance bool
	// fatal is set when error.mode=fail (or on_status=fail) fires.
	fatal bool
}

// runIteration executes all requests in one iteration in declared order,
// applies extracts and step-body bindings, and returns the producer
// step's body + located events. See iterationResult for the full signal set.
func (r *Runner) runIteration(
	ctx context.Context,
	client *http.Client,
	logger *log.Logger,
	s *scope,
	pagination paginationPlan,
	progress progressPlan,
	errMode string,
	iter int,
) (iterationResult, error) {
	out := iterationResult{advance: true}

	phase := progress.currentPhase(s)
	producerID := producerStepID(r.Doc, progress, phase)
	implicitLast := producerID == "" && !isAsyncJob(progress)
	// Compare by slice index, not by label: two unlabelled requests with
	// the same method+path share a reqLabel and would resolve ambiguously.
	// Implicit-last is disabled under async_job so non-producer phases
	// cannot pick up an unrelated last-declared request as the producer.
	lastIdx := len(r.Doc.Requests) - 1

	// Discover the active strategy's implicit `send_as` slot once per
	// iteration. Strategies without send_as (cursor_token / scroll_id are
	// the only carriers today) don't implement the optional interface and
	// inject stays nil — no auto-injection. The slot is passed to
	// executeRequest ONLY for the producer step; non-producer requests
	// (e.g. async_job submit / poll) never paginate.
	var inject *autoInjectSlot
	if pi, ok := pagination.(paginationAutoInjector); ok {
		slot := pi.autoInjectSlot()
		if slot.kind != "" {
			inject = &slot
		}
	}

	for i, req := range r.Doc.Requests {
		if progress.shouldSkipForPhase(req, phase) {
			continue
		}
		// requests[].if predicate gates execution.
		if req.If != nil {
			ok, err := s.evalPredicate(*req.If)
			if err != nil {
				return iterationResult{}, fmt.Errorf("requests[%s].if: %w", reqLabel(req), err)
			}
			if !ok {
				continue
			}
		}

		// Attach a trace scratchpad only when the caller wired a Tracer.
		// executeRequest checks for nil internally; passing nil keeps the
		// untraced path allocation-identical to before.
		var trace *httpTrace
		if r.Tracer != nil {
			trace = &httpTrace{}
		}
		// Pass the auto-injection slot only for the producer step. Setup /
		// extract-only steps never paginate, so they never receive the
		// implicit-form lowering.
		var reqInject *autoInjectSlot
		if inject != nil && (req.ID == producerID || (implicitLast && i == lastIdx)) {
			reqInject = inject
		}
		res, runErr := s.executeRequest(ctx, client, req, trace, reqInject)
		if r.Tracer != nil {
			r.Tracer.OnExchange(buildExchange(r.Doc, req, trace, runErr, iter, phase))
		}
		// realSuccess flags "we got a real response we can extract from".
		// The on_status arms below can set the artificial-success flag
		// (statusOK) for empty_events / invalidate_cache, but those arms
		// MUST NOT run extracts / step-cache / phaseTransition — the
		// response body is nil and walking it silently mis-binds the
		// downstream namespaces.
		realSuccess := runErr == nil

		// Handle unexpected-status errors. on_status takes precedence over
		// error.mode for the status it names.
		if usErr, ok := asUnexpectedStatus(runErr); ok && res != nil {
			action := req.OnStatus[usErr.status]
			switch action {
			case "skip":
				logger.Printf("client: %s status %d → skip", reqLabel(req), usErr.status)
				continue
			case "fail":
				out.fatal = true
				return out, fmt.Errorf("on_status: fail (status %d)", usErr.status)
			case "empty_events":
				logger.Printf("client: %s status %d → empty_events (advance cursor)", reqLabel(req), usErr.status)
				// Treat as a successful empty page: cursor still advances
				// at drain end, but no extracts / phaseTransition run.
				continue
			case "invalidate_cache":
				// Drop every OAuth2 token-cache slot reachable from doc.Auth
				// (state.<store_in> + cursor.__oauth2_<store_in>_expires_at),
				// then treat the page as empty. The cursor still advances at
				// drain end; the next request misses the cache and forces a
				// fresh token fetch. When the active auth has no cache (or
				// is non-OAuth2), the verb degrades to empty_events with an
				// explanatory log line.
				cleared := s.invalidateAuthCaches(r.Doc.Auth)
				if len(cleared) == 0 {
					logger.Printf("client: %s status %d → invalidate_cache (no auth cache to invalidate; advance cursor)", reqLabel(req), usErr.status)
				} else {
					logger.Printf("client: %s status %d → invalidate_cache (cleared %v; advance cursor)", reqLabel(req), usErr.status, cleared)
				}
				continue
			default:
				// No on_status override — fall through to error.mode.
				// Wrap with redactURLError at the log site so a future
				// code path that bypasses executeRequest's wrap still
				// scrubs the URL before logging.
				switch errMode {
				case "fail":
					out.fatal = true
					return out, fmt.Errorf("%s: %w", reqLabel(req), redactURLError(usErr))
				case "warn":
					logger.Printf("client: %s WARN: %v", reqLabel(req), redactURLError(usErr))
					// warn: log, treat as empty page, no extracts.
					continue
				default: // "standard"
					// Iteration ends here; cursor does NOT advance for the
					// rest of this drain — neither pagination.advance (this
					// iteration) nor progress.advance (drain end).
					logger.Printf("client: %s: %v (standard mode; cursor not advanced)", reqLabel(req), redactURLError(usErr))
					out.advance = false
					return out, nil
				}
			}
		} else if runErr != nil {
			// Non-status error (DNS, connection refused, decode failure).
			// Treat under error.mode but never advance. As above, wrap
			// with redactURLError at the log site so the redaction step
			// is local rather than transitive-by-callee.
			switch errMode {
			case "fail":
				out.fatal = true
				return out, fmt.Errorf("%s: %w", reqLabel(req), redactURLError(runErr))
			case "warn":
				logger.Printf("client: %s WARN: %v", reqLabel(req), redactURLError(runErr))
				continue
			default:
				logger.Printf("client: %s: %v (standard mode; cursor not advanced)", reqLabel(req), redactURLError(runErr))
				out.advance = false
				return out, nil
			}
		}

		if !realSuccess {
			// Defensive: every artificial-success path above already
			// continued. This branch only runs if a new arm is added
			// without explicit continue — guard so step cache and
			// extracts never bind to a nil-body response.
			continue
		}

		// Apply extracts.
		if err := s.runExtracts(res, req.Extract); err != nil {
			return iterationResult{}, fmt.Errorf("%s.extract: %w", reqLabel(req), err)
		}
		// Bind step body for downstream refs.
		if req.ID != "" {
			s.steps[req.ID] = res.body
		}

		// Phase transition (only async_job acts here; others return no-op).
		if req.ID != "" {
			next, more, isProducer, perr := progress.phaseTransition(s, req.ID, res)
			if perr != nil {
				return iterationResult{}, fmt.Errorf("%s: %w", reqLabel(req), perr)
			}
			if more {
				out.iterMore = true
			}
			if isProducer {
				out.producerIteration = true
			}
			if next != "" && next != phase {
				logger.Printf("client: phase %s → %s (via %s)", phase, next, reqLabel(req))
			}
		}

		// Capture the producer body + headers for events emission and
		// pagination.advance. Index-based for the implicit "last request"
		// case so two unlabeled requests with the same method+path don't
		// both match. Headers ride alongside body because link_header
		// pagination reads response.Header["Link"]; body-cursor variants
		// ignore producerHeaders. The implicit-last fallback never applies
		// under async_job (see implicitLast above).
		if req.ID == producerID || (implicitLast && i == lastIdx) {
			out.producerBody = res.body
			out.producerHeaders = res.headers
			out.producerIteration = true
		}
	}

	ndjson := r.Doc.Response.Decode == "ndjson"
	if out.producerBody != nil {
		evs, err := locateEvents(out.producerBody, pathParts(r.Doc.Response.EventsAt), ndjson)
		if err != nil {
			return iterationResult{}, fmt.Errorf("locate events: %w", err)
		}
		out.events = evs
	}

	return out, nil
}

// isAsyncJob reports whether the active progress plan is async_job. Used to
// disable the implicit-"last request" producer fallback in runIteration so
// non-producer phases don't pick up an unrelated last-declared request as
// the producer.
func isAsyncJob(p progressPlan) bool {
	_, ok := p.(*asyncJobProgress)
	return ok
}

// producerStepID returns the request id whose body is the events source.
// For async_job the producer is the step carrying produces_events=true if
// any; else the last-declared role (fetch > poll > submit). For non-async,
// it's the produces_events step, or "" (the implicit-last fallback in
// runIteration) when no marker is set.
func producerStepID(doc *schema.Doc, progress progressPlan, phase string) string {
	if aj, ok := progress.(*asyncJobProgress); ok {
		// Explicit produces_events marker wins over the implicit last-role
		// rule, mirroring the non-async branch. The validator
		// (checkAsyncJobProducesEvents) restricts the marker to one of
		// the role steps so the phaseForStep lookup below always resolves.
		var explicitID, explicitPhase string
		for _, req := range doc.Requests {
			if req.ProducesEvents {
				explicitID = req.ID
				explicitPhase = aj.phaseForStep(req.ID)
				break
			}
		}
		producerPhase := explicitPhase
		if producerPhase == "" {
			switch {
			case aj.cfg.Fetch != nil:
				producerPhase = "fetch"
			case aj.cfg.Poll != nil:
				producerPhase = "poll"
			case aj.cfg.Submit != nil:
				producerPhase = "submit"
			}
		}
		if phase != producerPhase {
			// The current iteration is a non-producer phase; no events
			// will be emitted this drain-iteration.
			return ""
		}
		if explicitID != "" {
			return explicitID
		}
		return aj.stepID(producerPhase)
	}
	for _, req := range doc.Requests {
		if req.ProducesEvents {
			return req.ID
		}
	}
	return "" // signals "the last request is the producer"
}

func reqLabel(req schema.Request) string {
	if req.ID != "" {
		return "requests[" + req.ID + "]"
	}
	return fmt.Sprintf("requests[%s %s]", req.Method, valuePathLabel(req.Path, req.URL))
}

// valuePathLabel returns a stable string label for either path or url.
func valuePathLabel(p, u *schema.Value) string {
	pick := p
	if pick == nil {
		pick = u
	}
	if pick == nil {
		return "<?>"
	}
	if pick.LiteralString != nil {
		return *pick.LiteralString
	}
	return "<value>"
}

// errorMode returns the document's error.mode (defaulting to "standard"
// when error block is unset).
func errorMode(doc *schema.Doc) string {
	if doc.Error == nil || doc.Error.Mode == "" {
		return "standard"
	}
	return doc.Error.Mode
}

// asUnexpectedStatus extracts the unexpectedStatusError sentinel if err
// wraps one. Mirrors errors.As but with a typed return.
func asUnexpectedStatus(err error) (*unexpectedStatusError, bool) {
	var us *unexpectedStatusError
	if errors.As(err, &us) {
		return us, true
	}
	return nil, false
}
