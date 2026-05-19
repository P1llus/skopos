// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/p1llus/skopos/schema"
)

// Runner is the in-process IR interpreter. Each call to Drain executes one
// pull session against r.Doc, persisting state through r.Store and emitting
// events to r.Sink.
//
// The Runner does not own scheduling — the caller decides whether to invoke
// Drain once (--once) or in a loop with sleeps (--interval). This mirrors
// the database/sql pattern: each Drain is one goroutine; the caller
// multiplexes.
//
// # Concurrency
//
// One Runner is one logical pull source. Drain mutates internal scope state
// and the deferred store.Save reads what each iteration wrote. Calling
// Drain concurrently on the same Runner WILL race on scope state and
// interleave events into Sink. Don't do it.
//
// Fan-out across N pull sources is the caller's responsibility: build N
// Runner values (each with its own *schema.Doc, Store, Sink) and call
// Drain on each from its own goroutine. The Runner type holds no
// package-global state, so independent Runners are independent. Sink and
// Store implementations shared between Runners MUST be safe for concurrent
// use; the bundled JSONLSink and MemoryStore are.
//
// # Drain lifecycle (docs/runtime.md §2)
//
//  1. store.Load → seed scope.state from the snapshot, layered over the
//     IR's declared state.<name>.default Values.
//  2. (*scope).resetPerDrainScratch — wipe every per-drain scratch state
//     field back to its declared default: (or unset when no default).
//     A drain that fails mid-page therefore re-bootstraps pagination on
//     the next start.
//  3. Pagination loop. Each iteration is one page:
//     a. Run the requests: chain end-to-end, honouring if:, the per-
//     request terminate_when: loop, on_status:, and error.mode.
//     b. Bind scope.body / scope.responseHeaders / scope.events against
//     the producer step's decoded response.
//     c. Emit each event to Sink (one call per event, in declared order).
//     d. applyProgress(s, doc.Progress) — once per accepted page-
//     response, including empty pages.
//     e. pagination.advance(s) — terminate? exit the loop. Else loop.
//  4. defer store.Save(s.snapshot()) + sink.Flush() — runs on normal
//     exit, error.mode: warn, AND error.mode: fail. A partial drain
//     still persists whatever was reached.
//
// # Request loop (docs/runtime.md §4)
//
// A request with terminate_when: re-fires until the predicate evaluates
// true; a request without it runs exactly once. The predicate sees the
// just-finished response via response.body.<path> / response.header.<name>.
// applyProgress fires after the request chain settles, not mid-loop.
//
// # on_status verbs (docs/runtime.md §6)
//
//	skip              Drop response, emit no events, advance progress
//	                  + pagination as if successful.
//	fail              Emit no events, abort the drain with an error.
//	empty_events      Emit no events but DO fire progress + pagination.
//	invalidate_cache  Drop every reachable cache.* slot (the active
//	                  auth's cache + every requests[].cache slot), then
//	                  treat the response as a non-event "retry next
//	                  iteration" signal: no events, no progress, no
//	                  pagination advance. Degrades to empty_events with
//	                  a log line when no reachable cache slot exists.
//
// # error.mode fallback (docs/runtime.md §6)
//
//	standard (default)  Pagination loop ends, per-drain wipe runs on
//	                    the next drain start, deferred Save runs.
//	warn                Log, continue. The iteration advances as if the
//	                    page came back empty; applyProgress does NOT
//	                    fire. Deferred Save runs normally.
//	fail                Drain returns non-nil. Deferred Save still runs.
//
// # MaxPages safety cap
//
// A pagination loop that never terminates (buggy server, mis-spelled
// termination path) is bounded by Runner.MaxPages. Hitting the cap
// returns errMaxPagesExceeded; the deferred Save still persists whatever
// state was reached.
type Runner struct {
	// Doc is the IR document to execute. Required.
	Doc *schema.Doc

	// Store persists Snapshot between drains. Optional; defaults to a
	// MemoryStore (state is lost between Drain calls).
	Store Store

	// Sink receives emitted events. Required.
	Sink Sink

	// Client is the HTTP client used for every request. Optional;
	// defaults to a client with a 30-second per-request timeout. Callers
	// needing a different timeout, custom transport, or proxy should
	// plug in their own *http.Client — but should ALWAYS set
	// Client.Timeout to a finite value so a hung server cannot wedge
	// Drain until ctx cancellation.
	Client *http.Client

	// Now is the clock. Optional; defaults to time.Now. Useful for tests.
	Now func() time.Time

	// Logger receives operational diagnostics (skipped steps, on_status:
	// decisions, error.mode dispatches, cache invalidations). Optional;
	// defaults to log.Default().
	Logger *log.Logger

	// MaxPages caps the number of iterations one Drain runs before it
	// returns errMaxPagesExceeded.
	//
	//   0 (the default) → applies the built-in default (10_000); enough
	//                     headroom for any realistic API while still
	//                     bounding a buggy server returning the same
	//                     cursor token forever.
	//   > 0             → uses the supplied value as the cap.
	//   < 0             → disables the cap entirely (NOT recommended
	//                     outside tests).
	MaxPages int

	// Tracer receives one Exchange per HTTP request/response pair
	// executed during the drain. Optional; nil disables per-exchange
	// capture (the operational Logger still fires). See trace.go for the
	// redaction policy — the trace is intentionally body-metadata-only
	// by default so secret-bearing request/response bodies cannot leak.
	//
	// When set, Tracer is called serially from the same goroutine that
	// runs Drain, in the order requests run within an iteration. A
	// Tracer shared between concurrent Runners MUST be safe for
	// concurrent calls; the bundled JSONLTracer is.
	Tracer Tracer
}

// defaultHTTPTimeout is the per-request timeout applied when the caller
// did not supply an *http.Client. It bounds connect + TLS + request +
// response — a 30-second wall covers slow APIs with comfortable headroom
// while still putting a finite ceiling on a hung server.
const defaultHTTPTimeout = 30 * time.Second

// defaultMaxPages is the per-drain iteration cap when Runner.MaxPages is
// zero. 10_000 is well beyond any realistic API drain (a 50-events-per-
// page endpoint with 500k events fits) but still bounds a buggy server
// returning the same cursor token forever.
const defaultMaxPages = 10_000

// errMaxPagesExceeded is returned by Drain when the iteration cap fires.
var errMaxPagesExceeded = errors.New("client: drain exceeded MaxPages cap (suspected pagination loop)")

// Drain runs one full pull session: paginate until the active variant's
// terminate_when: predicate returns true (or until the MaxPages cap
// fires). It persists state through r.Store before returning.
//
// Drain returns nil for the normal "iteration completed cleanly" outcome.
// error.mode: warn surfaces non-success responses but keeps Drain's
// return nil. error.mode: fail surfaces non-success responses as a
// non-nil error.
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

	// Register the deferred Save + Flush BEFORE any further work so
	// every termination path — normal exit, error.mode: warn, error.
	// mode: fail, MaxPages, ctx cancellation — persists whatever state
	// the drain reached. A drain that fails mid-page does not lose
	// progress writes that already fired on earlier accepted pages.
	defer func() {
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

	if err := s.resetPerDrainScratch(); err != nil {
		return fmt.Errorf("per-drain wipe: %w", err)
	}

	plan, err := makePaginationPlan(r.Doc)
	if err != nil {
		return err
	}
	errMode := errorMode(r.Doc)

	pages := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if maxPages > 0 && pages >= maxPages {
			return fmt.Errorf("%w: %d iterations", errMaxPagesExceeded, pages)
		}
		pages++

		// Reset per-iteration scratch bindings. Every iteration starts
		// with empty extract / steps / step-headers maps and unbound
		// per-evaluation body / response-headers / events fields. A
		// later iteration MUST NOT see a previous iteration's per-
		// request scratch.
		s.extract = make(map[string]any)
		s.steps = make(map[string]any)
		s.stepHeaders = make(map[string]http.Header)
		s.body = nil
		s.responseHeaders = nil
		s.events = nil

		out, err := r.runIteration(ctx, client, logger, s, errMode, pages)
		if err != nil {
			return err
		}

		switch out.kind {
		case iterFatal:
			// runIteration returned err above for the fatal cases;
			// reaching here means the kind enum was set without an
			// accompanying error, which is a contract violation.
			return errors.New("client: drain aborted (fatal iteration with no error)")
		case iterBreak:
			// error.mode: standard surfaced a non-success status the
			// chain could not absorb. The pagination loop ends here;
			// the deferred Save persists whatever state was reached.
			return nil
		case iterInvalidate:
			// on_status: invalidate_cache — caches were dropped inside
			// runIteration; this iteration emits no events, runs no
			// progress writes, does not advance pagination. The next
			// iteration retries the same page with a fresh auth fetch.
			continue
		}

		// Bind the producer step's body / headers / events for
		// applyProgress and pagination.advance. Empty events list and
		// nil producer body are valid bindings — readers (default
		// terminate_when: predicates, {ref: events.count}, ...) handle
		// the absence.
		s.body = out.producerBody
		s.responseHeaders = out.producerHeaders
		s.events = anyEvents(out.events)

		if out.kind == iterAccepted {
			for _, ev := range out.events {
				if err := r.Sink.Emit(ev); err != nil {
					return fmt.Errorf("sink.Emit: %w", err)
				}
			}
		}

		// iterWarn skips progress per docs/runtime.md §6: a warn-mode
		// failure must not leak partial progress writes alongside the
		// "treat as empty page" semantics. iterAccepted / iterSkip /
		// iterEmpty all fire progress.
		if out.kind != iterWarn {
			if err := applyProgress(s, r.Doc.Progress); err != nil {
				return err
			}
		}

		terminate, err := plan.advance(s)
		if err != nil {
			return err
		}
		if terminate {
			return nil
		}
	}
}

// iterKind classifies an iteration's terminal verdict. The Drain loop
// switches on it to decide events emission, progress application, and
// pagination advance for the iteration. Fatal cases (on_status: fail,
// error.mode: fail) are surfaced through runIteration's error return
// rather than the enum so the caller can distinguish "abort with this
// error" from "abort without an error".
type iterKind int

const (
	// iterAccepted is the normal page-response case: emit events,
	// apply progress, advance pagination.
	iterAccepted iterKind = iota
	// iterSkip is on_status: skip on the producer step: no events to
	// emit, but apply progress and advance pagination.
	iterSkip
	// iterEmpty is on_status: empty_events on the producer step: no
	// events to emit, but apply progress and advance pagination. The
	// canonical 429-with-ignore handler.
	iterEmpty
	// iterInvalidate is on_status: invalidate_cache: caches have been
	// dropped, no events emit, no progress, no pagination advance —
	// the next iteration retries the same page.
	iterInvalidate
	// iterWarn is an error.mode: warn dispatch: log, no events, no
	// progress, but pagination still advances as if the page came back
	// empty.
	iterWarn
	// iterBreak is an error.mode: standard dispatch: end the pagination
	// loop without an error. The deferred Save persists what was
	// reached; the next drain re-tries the same window.
	iterBreak
	// iterFatal is a contract-violation marker so the switch's
	// exhaustiveness is explicit. runIteration always pairs this with a
	// non-nil error return; the Drain switch surfaces a sentinel if the
	// pairing is ever broken by a future change.
	iterFatal
)

// iterationResult bundles what runIteration produces. The producer
// fields are nil/empty for non-accepted outcomes; the Drain switch
// reads only the fields its kind arm needs.
type iterationResult struct {
	kind            iterKind
	producerBody    any
	producerHeaders http.Header
	events          []any
}

// runIteration executes the requests chain end-to-end for one
// pagination iteration. Each request honours its if: predicate, runs
// its terminate_when: loop (request-level loop primitive — fire until
// the predicate is true), captures extracts + step body + headers, and
// dispatches non-success responses through on_status: / error.mode.
//
// The producer step's decoded body and headers ride out on the result
// for the Drain loop to bind into scope and feed locateEvents.
func (r *Runner) runIteration(
	ctx context.Context,
	client *http.Client,
	logger *log.Logger,
	s *scope,
	errMode string,
	iter int,
) (iterationResult, error) {
	out := iterationResult{kind: iterAccepted}
	producerID := producerStepID(r.Doc)
	implicitLast := producerID == ""
	lastIdx := len(r.Doc.Requests) - 1
	// producerRan tracks whether the events-bearing step yielded a
	// usable response in this iteration. When the producer was skipped
	// (if: false, or never reached because a prior step on_status'd into
	// invalidate_cache) the iteration carries no events and applyProgress
	// must not fire. Compared against the iteration's verdict below.
	producerRan := false

	for i, req := range r.Doc.Requests {
		// Each request starts with scope.body / responseHeaders
		// unbound. response.body.<path> / response.header.<name>
		// resolve to absent inside if: predicates — a request has no
		// notion of "its own response" until it has actually fired.
		// runRequest rebinds them transiently during terminate_when:
		// evaluation; the per-iteration rebind for applyProgress and
		// pagination.advance is owned by Drain, against the producer
		// step's captured body.
		s.body = nil
		s.responseHeaders = nil

		if req.If != nil {
			ok, err := s.evalPredicate(*req.If)
			if err != nil {
				return iterationResult{}, fmt.Errorf("%s.if: %w", reqLabel(req), err)
			}
			if !ok {
				continue
			}
		}

		if req.FanOut != nil {
			fr, err := r.runFanOut(ctx, client, logger, s, req, errMode, iter, "")
			if err != nil {
				return iterationResult{}, err
			}
			if fr.fatal {
				return iterationResult{kind: iterFatal}, fmt.Errorf("client: drain aborted on error.mode=fail")
			}
			if fr.invalidated {
				// on_status: invalidate_cache from inside fan_out: mirror
				// the single-request stepInvalidate path so pagination does
				// NOT advance. Caches were dropped inside runFanOut.
				return iterationResult{kind: iterInvalidate}, nil
			}
			if !fr.advance {
				return iterationResult{kind: iterBreak}, nil
			}
			if req.ID != "" {
				s.steps[req.ID] = fr.mergedBody
				if fr.lastHeaders != nil {
					s.stepHeaders[req.ID] = fr.lastHeaders
				}
			}
			if req.ID == producerID || (implicitLast && i == lastIdx) {
				out.producerBody = fr.mergedBody
				out.producerHeaders = fr.lastHeaders
				producerRan = true
			}
			continue
		}

		// Request loop: a step with terminate_when: re-fires until the
		// predicate evaluates true; a step without it fires exactly
		// once. Each iteration of the inner loop binds scope.body /
		// responseHeaders to the just-finished response so the
		// predicate's response.body.<path> / response.header.<name>
		// refs resolve correctly.
		res, status, err := r.runRequest(ctx, client, logger, s, req, errMode, iter)
		if err != nil {
			return iterationResult{}, err
		}

		switch status {
		case stepFatal:
			return iterationResult{kind: iterFatal}, fmt.Errorf("client: drain aborted on error.mode=fail")
		case stepBreak:
			return iterationResult{kind: iterBreak}, nil
		case stepInvalidate:
			// invalidate_cache: stop the chain, defer the per-iteration
			// verdict to the Drain switch. Caches have already been
			// dropped inside runRequest.
			return iterationResult{kind: iterInvalidate}, nil
		case stepWarn:
			// error.mode: warn: the chain stops here for this iteration
			// (no events from a failed producer, no point running
			// downstream steps that depend on this step's extracts).
			return iterationResult{kind: iterWarn}, nil
		case stepSkip, stepEmpty:
			// Non-fatal on_status verbs on a non-producer step let the
			// chain continue; on the producer they decide the
			// iteration's verdict. The producer-detection block below
			// records the per-step verb so the iteration's terminal
			// kind reflects the producer step's outcome.
			if req.ID == producerID || (implicitLast && i == lastIdx) {
				out.producerBody = nil
				out.producerHeaders = nil
				producerRan = true
				if status == stepSkip {
					out.kind = iterSkip
				} else {
					out.kind = iterEmpty
				}
			}
			continue
		}

		// stepOK arm: extracts + step bindings + producer capture.
		if err := s.runExtracts(res, req.Extract); err != nil {
			return iterationResult{}, fmt.Errorf("%s.extract: %w", reqLabel(req), err)
		}
		if req.ID != "" {
			s.steps[req.ID] = res.body
			s.stepHeaders[req.ID] = res.headers
		}
		if req.ID == producerID || (implicitLast && i == lastIdx) {
			out.producerBody = res.body
			out.producerHeaders = res.headers
			producerRan = true
		}
	}

	// When the producer step never ran (every if: gated it out, or the
	// requests list was empty after if: filtering) the iteration carries
	// no events. iterWarn semantics also fit: pagination advances as if
	// empty, but progress doesn't fire. iterBreak / iterFatal cases were
	// handled above.
	if !producerRan && out.kind == iterAccepted {
		out.kind = iterWarn
	}

	if out.kind == iterAccepted {
		evs, err := r.locateProducerEvents(s, out.producerBody)
		if err != nil {
			return iterationResult{}, err
		}
		out.events = evs
	}

	return out, nil
}

// stepStatus is the terminal verdict for one request execution
// (including its terminate_when: loop). The Drain dispatcher in
// runIteration switches on it.
type stepStatus int

const (
	// stepOK is the normal path: the response was accepted, extracts +
	// step bindings should run.
	stepOK stepStatus = iota
	// stepSkip is on_status: skip: log + continue without extracts.
	stepSkip
	// stepEmpty is on_status: empty_events.
	stepEmpty
	// stepInvalidate is on_status: invalidate_cache: caches dropped,
	// iteration retries.
	stepInvalidate
	// stepWarn is error.mode: warn: log + continue with no events for
	// the iteration.
	stepWarn
	// stepBreak is error.mode: standard: end the pagination loop.
	stepBreak
	// stepFatal is on_status: fail or error.mode: fail.
	stepFatal
)

// runRequest executes one request through its terminate_when: loop and
// returns the final stepResult plus the dispatcher verdict for the
// Drain loop. Non-success responses are dispatched first through
// req.OnStatus[<code>] (closed verb set: skip / fail / empty_events /
// invalidate_cache), then through errMode (standard / warn / fail).
//
// The terminate_when: loop fires the same request until the predicate
// evaluates true against the just-finished response. The predicate sees
// scope.body / scope.responseHeaders bound to the latest response;
// applyProgress and pagination.advance do NOT fire inside the loop —
// they fire once per page-response, AFTER the loop settles.
func (r *Runner) runRequest(
	ctx context.Context,
	client *http.Client,
	logger *log.Logger,
	s *scope,
	req schema.Request,
	errMode string,
	iter int,
) (*stepResult, stepStatus, error) {
	for {
		var trace *httpTrace
		if r.Tracer != nil {
			trace = &httpTrace{}
		}
		res, runErr := s.executeRequest(ctx, client, req, trace)
		if r.Tracer != nil {
			r.Tracer.OnExchange(buildExchange(r.Doc, req, trace, runErr, iter, ""))
		}

		if usErr, ok := asUnexpectedStatus(runErr); ok && res != nil {
			action := req.OnStatus[usErr.status]
			switch action {
			case "skip":
				logger.Printf("client: %s status %d → skip", reqLabel(req), usErr.status)
				return res, stepSkip, nil
			case "fail":
				return res, stepFatal, fmt.Errorf("%s: on_status: fail (status %d)", reqLabel(req), usErr.status)
			case "empty_events":
				logger.Printf("client: %s status %d → empty_events", reqLabel(req), usErr.status)
				return res, stepEmpty, nil
			case "invalidate_cache":
				cleared := dropReachableCaches(s, r.Doc)
				if len(cleared) == 0 {
					logger.Printf("client: %s status %d → invalidate_cache (no cache to invalidate; treating as empty_events)", reqLabel(req), usErr.status)
					return res, stepEmpty, nil
				}
				logger.Printf("client: %s status %d → invalidate_cache (cleared %v; retry next iteration)", reqLabel(req), usErr.status, cleared)
				return res, stepInvalidate, nil
			default:
				switch errMode {
				case "fail":
					return res, stepFatal, fmt.Errorf("%s: %w", reqLabel(req), redactURLError(usErr))
				case "warn":
					logger.Printf("client: %s WARN: %v", reqLabel(req), redactURLError(usErr))
					return res, stepWarn, nil
				default: // "standard"
					logger.Printf("client: %s: %v (standard mode; pagination loop ends)", reqLabel(req), redactURLError(usErr))
					return res, stepBreak, nil
				}
			}
		} else if runErr != nil {
			// Non-status error (DNS, connection refused, decode failure).
			// Dispatches directly through error.mode — on_status: is
			// keyed on HTTP status and never fires for transport-level
			// failures.
			switch errMode {
			case "fail":
				return nil, stepFatal, fmt.Errorf("%s: %w", reqLabel(req), redactURLError(runErr))
			case "warn":
				logger.Printf("client: %s WARN: %v", reqLabel(req), redactURLError(runErr))
				return nil, stepWarn, nil
			default:
				logger.Printf("client: %s: %v (standard mode; pagination loop ends)", reqLabel(req), redactURLError(runErr))
				return nil, stepBreak, nil
			}
		}

		// Successful exchange. Bind scope.body / responseHeaders so
		// terminate_when: can read against the just-finished response.
		s.body = res.body
		s.responseHeaders = res.headers

		if req.TerminateWhen == nil {
			return res, stepOK, nil
		}
		done, err := s.evalPredicate(*req.TerminateWhen)
		if err != nil {
			return nil, stepFatal, fmt.Errorf("%s.terminate_when: %w", reqLabel(req), err)
		}
		if done {
			return res, stepOK, nil
		}
		// Predicate false → re-fire the same request. Context
		// cancellation is honoured at the top of the next iteration.
		if cerr := ctx.Err(); cerr != nil {
			return nil, stepFatal, cerr
		}
	}
}

// locateProducerEvents resolves the producer step's body through
// response.events_at and returns the decoded events list. An empty
// events_at means "the body root is the events list"; an explicit
// steps.<id>.body.<path> resolves against the named step's captured
// body. NDJSON decode follows the same closed enum.
func (r *Runner) locateProducerEvents(s *scope, producerBody any) ([]any, error) {
	parts, stepID, err := stripBodyRoot(r.Doc.Response.EventsAt)
	if err != nil {
		return nil, fmt.Errorf("response.events_at: %w", err)
	}
	body := producerBody
	if stepID != "" {
		b, ok := s.steps[stepID]
		if !ok {
			return nil, fmt.Errorf("response.events_at references step %q with no captured body", stepID)
		}
		body = b
	}
	ndjson := r.Doc.Response.Decode == "ndjson"
	evs, err := locateEvents(body, parts, ndjson)
	if err != nil {
		return nil, fmt.Errorf("locate events: %w", err)
	}
	return evs, nil
}

// anyEvents lifts a []any into the scope.events runtime shape. A nil
// slice surfaces as nil so resolveNamespaceRef's absent-tolerant arms
// fire; a non-nil empty slice surfaces as []any{} so events.count
// returns 0 cleanly and reducer projections over events.* yield empty
// lists rather than missing values.
func anyEvents(evs []any) any {
	if evs == nil {
		return nil
	}
	return evs
}

// producerStepID returns the request id whose body is the events
// source. An explicit produces_events: true marker wins; otherwise the
// runner falls through to the implicit-last rule — the last declared
// request is the producer. Returns "" to signal the implicit-last
// fallback; runIteration captures producer state when (implicitLast &&
// i == lastIdx) is true. The producer-capture guard also re-fires for
// unlabelled non-last steps via the req.ID == producerID arm (both ""),
// but each match overwrites the previous one, so last-write-wins on the
// final slice index — the validator already forbids more than one
// produces_events marker.
func producerStepID(doc *schema.Doc) string {
	for _, req := range doc.Requests {
		if req.ProducesEvents {
			return req.ID
		}
	}
	return ""
}

// reqLabel returns a stable, log-safe label for req. The id wins when
// declared; otherwise the method alone is used (the URL is a Value that
// may not reduce to a plain literal, and rendering its shape verbatim
// could leak a secret-typed ref into operator-facing logs).
func reqLabel(req schema.Request) string {
	if req.ID != "" {
		return "requests[" + req.ID + "]"
	}
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = "GET"
	}
	return fmt.Sprintf("requests[%s]", method)
}

// errorMode returns the document's error.mode, defaulting to "standard"
// when the error block is absent or its mode is unset.
func errorMode(doc *schema.Doc) string {
	if doc.Error == nil || doc.Error.Mode == "" {
		return "standard"
	}
	return doc.Error.Mode
}

// asUnexpectedStatus extracts the unexpectedStatusError sentinel if err
// wraps one. Mirrors errors.As with a typed return.
func asUnexpectedStatus(err error) (*unexpectedStatusError, bool) {
	if us, ok := errors.AsType[*unexpectedStatusError](err); ok {
		return us, true
	}
	return nil, false
}

// dropReachableCaches deletes every cache.<name> slot reachable from
// doc — the active auth's Cache block (recursing into multi_mode
// branches) and every requests[].Cache slot — and returns the slot
// names that were actually cleared. The names are read off each Cache
// block's To Path; a slot that was never populated is silently skipped.
//
// Backs on_status: invalidate_cache: dropping every reachable slot
// forces the next request that touches them to miss the cache and
// re-run the underlying fetch (OAuth2 token endpoint, custom login).
// When no slot was reachable, the verb degrades to empty_events at the
// runRequest dispatcher.
func dropReachableCaches(s *scope, doc *schema.Doc) []string {
	cleared := dropAuthCaches(s, doc.Auth)
	for _, req := range doc.Requests {
		if req.Cache == nil {
			continue
		}
		if name, ok := cacheSlotName(req.Cache.To); ok {
			if _, exists := s.cache[name]; exists {
				delete(s.cache, name)
				cleared = append(cleared, name)
			}
		}
	}
	return cleared
}

// dropAuthCaches walks auth for every Cache block (OAuth2 grants are
// the only carriers today, but multi_mode recursion keeps the walk
// future-proof) and deletes each matching cache slot, returning the
// names cleared. The validator forbids nested multi_mode so the
// recursion bottoms out at one hop.
func dropAuthCaches(s *scope, auth schema.Auth) []string {
	var cleared []string
	switch {
	case auth.OAuth2 != nil:
		var c *schema.Cache
		switch {
		case auth.OAuth2.ClientCredentials != nil:
			c = auth.OAuth2.ClientCredentials.Cache
		case auth.OAuth2.PasswordGrant != nil:
			c = auth.OAuth2.PasswordGrant.Cache
		}
		if c != nil {
			if name, ok := cacheSlotName(c.To); ok {
				if _, exists := s.cache[name]; exists {
					delete(s.cache, name)
					cleared = append(cleared, name)
				}
			}
		}
	case auth.MultiMode != nil:
		for _, b := range auth.MultiMode.Branches {
			cleared = append(cleared, dropAuthCaches(s, b.Auth)...)
		}
		cleared = append(cleared, dropAuthCaches(s, auth.MultiMode.Default)...)
	}
	return cleared
}

// cacheSlotName unpacks a Cache.To path into its cache.<name> slot
// name. The validator already enforces the shape; the defensive check
// here keeps a future contract violation from silently writing into
// the wrong slot.
func cacheSlotName(p schema.Path) (string, bool) {
	if len(p.Parts) != 2 || p.Parts[0] != "cache" {
		return "", false
	}
	return p.Parts[1], true
}
