// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
	"time"

	"github.com/p1llus/skopos/schema"
)

// progressPlan describes the active progress strategy for one drain. It
// has two responsibilities:
//
//  1. seed scope.cursor ONCE PER DRAIN (not per iteration — see
//     Runner.Drain's loop contract) so {ref: cursor.<name>} Values
//     resolve to a stable window-start for every page in this drain.
//  2. advance() updates scope.cursor after the producer step's events have
//     been emitted. For most strategies this is a single map write; for
//     async_job it is a phase machine that may want another iteration.
//
// async_job is the only progress kind that also gates request dispatch
// (via cursor.phase); the runner consults shouldSkipForPhase() before
// executing each step. Other strategies return false here unconditionally.
//
// # Strategy catalogue and cursor name contract
//
// Templates read progress signals via {ref: cursor.<name>}, which resolves
// against scope.cursor. The cursor names each plan writes are:
//
//	stateless              (no cursor names — progress is a no-op.)
//	latest_event_timestamp "last_timestamp" (RFC 3339 string; seed pre-seeds
//	                       initial.lookback on first drain, advance walks
//	                       events at cfg.EventTime.Path and writes the max.)
//	max_event_field        "last_timestamp" (same code path as
//	                       latest_event_timestamp; the IR-level distinction
//	                       is documentation-only — both walk-and-max events
//	                       in the runner.)
//	async_job              "phase" ("submit" | "poll" | "fetch"; seed defaults
//	                       to firstPhase when absent), plus "last_timestamp"
//	                       written by on_complete.cursor_update (kind=use_now
//	                       or latest_event_timestamp; stateless leaves the
//	                       cursor untouched). Templates that wire the
//	                       last-timestamp into a request body read it via
//	                       {ref: cursor.last_timestamp}; first drain has no
//	                       cursor.last_timestamp so the ref resolves to nil
//	                       (template adds {default: ""} for servers that
//	                       require an explicit empty since=).
//	time_window            "window_start" / "window_end" (formatted via
//	                       cfg.Format — default rfc3339. seed pins the
//	                       window once per drain: first drain starts at
//	                       now() - initial_offset and ends at now(); resumes
//	                       carry cursor.window_start forward and recompute
//	                       window_end against the new now(). advance slides
//	                       cursor.window_start to the just-finished
//	                       cursor.window_end. The drain end is clamped to
//	                       >= window_start so a backwards-running clock
//	                       cannot produce an inverted window.)
//	use_now                "last_timestamp" (RFC 3339 string; advance writes
//	                       cursor.last_timestamp = s.now() - lookback. No
//	                       events walk. seed is a no-op — use_now has no
//	                       initial.lookback, so cursor.last_timestamp is
//	                       absent on first drain.)
//
// Authors of a new progress variant should document the cursor names their
// seed() / advance() write here AND in the variant's own comment so
// template authors have a single place to look.
type progressPlan interface {
	seed(s *scope) error
	advance(s *scope, events []any) error
	// shouldSkipForPhase reports whether req should be skipped this
	// iteration because the active phase doesn't include it.
	shouldSkipForPhase(req schema.Request, phase string) bool
	// currentPhase reports cursor.phase ("" for non-async strategies).
	currentPhase(s *scope) string
	// phaseTransition advances the phase machine after the named step's
	// result comes back. Returns the next phase ("" if no async_job),
	// whether we want another iteration, and whether this step emits events.
	//
	// For non-async strategies this is a no-op returning ("", false, true).
	phaseTransition(s *scope, stepID string, res *stepResult) (next string, wantMore bool, isProducer bool, err error)
}

func makeProgressPlan(doc *schema.Doc) (progressPlan, error) {
	switch {
	case doc.Progress.Stateless != nil:
		return &statelessProgress{}, nil
	case doc.Progress.LatestEventTimestamp != nil:
		return &latestTimestampProgress{cfg: doc.Progress.LatestEventTimestamp}, nil
	case doc.Progress.MaxEventField != nil:
		return &maxEventFieldProgress{cfg: doc.Progress.MaxEventField}, nil
	case doc.Progress.UseNow != nil:
		return &useNowProgress{cfg: doc.Progress.UseNow}, nil
	case doc.Progress.TimeWindow != nil:
		return &timeWindowProgress{cfg: doc.Progress.TimeWindow}, nil
	case doc.Progress.AsyncJob != nil:
		return &asyncJobProgress{
			cfg:      doc.Progress.AsyncJob,
			eventsAt: doc.Response.EventsAt,
			ndjson:   doc.Response.Decode == "ndjson",
		}, nil
	}
	return nil, fmt.Errorf("progress: no variant set")
}

// ---- stateless ----

type statelessProgress struct{}

func (p *statelessProgress) seed(*scope) error                              { return nil }
func (p *statelessProgress) advance(*scope, []any) error                    { return nil }
func (p *statelessProgress) shouldSkipForPhase(schema.Request, string) bool { return false }
func (p *statelessProgress) currentPhase(*scope) string                     { return "" }
func (p *statelessProgress) phaseTransition(*scope, string, *stepResult) (string, bool, bool, error) {
	return "", false, true, nil
}

// ---- latest_event_timestamp ----

type latestTimestampProgress struct {
	cfg *schema.TimestampProgress
}

func (p *latestTimestampProgress) seed(s *scope) error {
	return seedTimestampCursor(s, p.cfg, "progress.latest_event_timestamp")
}

func (p *latestTimestampProgress) advance(s *scope, events []any) error {
	return advanceTimestampCursor(s, events, p.cfg, "progress.latest_event_timestamp")
}

func (p *latestTimestampProgress) shouldSkipForPhase(schema.Request, string) bool { return false }
func (p *latestTimestampProgress) currentPhase(*scope) string                     { return "" }
func (p *latestTimestampProgress) phaseTransition(*scope, string, *stepResult) (string, bool, bool, error) {
	return "", false, true, nil
}

// ---- max_event_field ----
//
// Behaviourally the same shape as latest_event_timestamp: seed pre-seeds
// cursor.last_timestamp on first drain (initial.lookback applied to s.now()),
// and advance walks the accumulated events at advance time, picks the max
// value at cfg.EventTime.Path, and writes it to cursor.last_timestamp. The
// IR-level difference is intent — latest semantics assume server-ordered
// events ("last item's timestamp"), max semantics assume unordered events
// ("max across the page"). The runner walks-and-maxes in both cases (the
// only safe choice when ordering is not guaranteed), so the wire behaviour
// matches; the distinction stays a documentation signal for template authors.
//
// Cursor key: cursor.last_timestamp (RFC 3339 string). Read in templates as
// {ref: cursor.last_timestamp}.

type maxEventFieldProgress struct {
	cfg *schema.TimestampProgress
}

func (p *maxEventFieldProgress) seed(s *scope) error {
	return seedTimestampCursor(s, p.cfg, "progress.max_event_field")
}

func (p *maxEventFieldProgress) advance(s *scope, events []any) error {
	return advanceTimestampCursor(s, events, p.cfg, "progress.max_event_field")
}

func (p *maxEventFieldProgress) shouldSkipForPhase(schema.Request, string) bool { return false }
func (p *maxEventFieldProgress) currentPhase(*scope) string                     { return "" }
func (p *maxEventFieldProgress) phaseTransition(*scope, string, *stepResult) (string, bool, bool, error) {
	return "", false, true, nil
}

// ---- use_now ----
//
// Advances cursor.last_timestamp to s.now() (optionally minus a per-iteration
// lookback) on every drain. No events walk — useful for time-window APIs where
// the server doesn't echo a timestamp back but the operator wants the cursor
// to march forward in wall-clock time with a configurable lag.
//
// Mirrors the cursor-write logic in (*asyncJobProgress).applyOnComplete's
// kind=use_now branch — same s.now() / evalValue / toDuration sequence, same
// cursor.last_timestamp key, same RFC 3339 format.
//
// Cursor key: cursor.last_timestamp (RFC 3339 string). Read in templates as
// {ref: cursor.last_timestamp}. use_now has no initial.lookback in the
// schema; seed is a no-op so cursor.last_timestamp is absent on first drain
// (templates that need an explicit since="" on the bootstrap call out a
// {default: ""} branch on the ref).

type useNowProgress struct {
	cfg *schema.UseNowProgress
}

func (p *useNowProgress) seed(*scope) error { return nil }

func (p *useNowProgress) advance(s *scope, _ []any) error {
	t := s.now()
	if p.cfg.Lookback != nil {
		got, err := s.evalValue(*p.cfg.Lookback)
		if err != nil {
			return fmt.Errorf("progress.use_now.lookback: %w", err)
		}
		d, err := toDuration(got)
		if err != nil {
			return fmt.Errorf("progress.use_now.lookback: %w", err)
		}
		t = t.Add(-d)
	}
	s.cursor["last_timestamp"] = t.UTC().Format(time.RFC3339)
	return nil
}

func (p *useNowProgress) shouldSkipForPhase(schema.Request, string) bool { return false }
func (p *useNowProgress) currentPhase(*scope) string                     { return "" }
func (p *useNowProgress) phaseTransition(*scope, string, *stepResult) (string, bool, bool, error) {
	return "", false, true, nil
}

// seedTimestampCursor implements the shared seed contract for the timestamp
// progress variants (latest_event_timestamp, max_event_field): on first drain
// (cursor.last_timestamp absent) compute now() - initial.lookback (if
// declared) and write it to cursor.last_timestamp so the very first
// since=<...> matches the persisted high-water mark. On resume the cursor
// already carries the prior high-water mark — we leave it alone. errLabel
// scopes wrapped errors (e.g. "progress.max_event_field.initial.lookback").
func seedTimestampCursor(s *scope, cfg *schema.TimestampProgress, errLabel string) error {
	if _, ok := s.cursor["last_timestamp"]; ok {
		return nil
	}
	if cfg.Initial == nil {
		return nil
	}
	got, err := s.evalValue(cfg.Initial.Lookback)
	if err != nil {
		return fmt.Errorf("%s.initial.lookback: %w", errLabel, err)
	}
	d, err := toDuration(got)
	if err != nil {
		return fmt.Errorf("%s.initial.lookback: %w", errLabel, err)
	}
	s.cursor["last_timestamp"] = s.now().Add(-d).UTC().Format(time.RFC3339)
	return nil
}

// advanceTimestampCursor implements the shared advance contract for the
// timestamp progress variants. It is also reused by
// async_job.on_complete.cursor_update.kind=latest_event_timestamp so the
// events-walk lives in one place. Walks events, picks the max value at
// cfg.EventTime.Path, applies the optional per-iteration lookback, and writes
// the result to cursor.last_timestamp. Empty events (or no parseable
// timestamps) leave the cursor untouched — multi-page drains accumulate the
// real high-water mark across pages before the cursor jumps.
func advanceTimestampCursor(s *scope, events []any, cfg *schema.TimestampProgress, errLabel string) error {
	if len(events) == 0 {
		return nil
	}
	maxTime, err := maxEventTime(events, cfg.EventTime.Path, errLabel)
	if err != nil {
		return err
	}
	if maxTime.IsZero() {
		return nil
	}
	if cfg.Lookback != nil {
		got, err := s.evalValue(*cfg.Lookback)
		if err != nil {
			return fmt.Errorf("%s.lookback: %w", errLabel, err)
		}
		d, err := toDuration(got)
		if err != nil {
			return fmt.Errorf("%s.lookback: %w", errLabel, err)
		}
		maxTime = maxTime.Add(-d)
	}
	s.cursor["last_timestamp"] = maxTime.UTC().Format(time.RFC3339)
	return nil
}

// maxEventTime walks events looking up path on each one and returns the
// largest time.Time it can parse. Events whose value at path is missing or
// not a parseable timestamp are skipped silently — partial event quality is
// expected (placeholder events, schema drift). The zero time.Time is returned
// when no event yielded a parseable timestamp.
func maxEventTime(events []any, path schema.Path, errLabel string) (time.Time, error) {
	var maxTime time.Time
	tParts := pathParts(path)
	for _, ev := range events {
		got, ok, err := lookupBodyPath(ev, tParts)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s.event_time.path: %w", errLabel, err)
		}
		if !ok {
			continue
		}
		t, err := toTime(got)
		if err != nil {
			continue
		}
		if t.After(maxTime) {
			maxTime = t
		}
	}
	return maxTime, nil
}

// ---- time_window ----
//
// Pulls events in successive [window_start, window_end] ranges. seed pins
// the drain's window once: on the first run window_start is now() minus
// cfg.InitialOffset; on resume cursor.window_start carries forward from the
// previous drain's advance. window_end is always (re)computed against the
// scope clock so a drain restart after a long pause naturally widens to
// cover the gap. advance slides cursor.window_start to the just-finished
// cursor.window_end — the high-water mark of this drain becomes the floor
// of the next.
//
// The window's wire representation is governed by cfg.Format (one of the
// timestamp-producing format verbs — rfc3339, rfc3339nano, unix_seconds,
// unix_millis; default rfc3339). seed writes the formatted strings to
// cursor.window_start / cursor.window_end so templates read them via
// {ref: cursor.window_start} / {ref: cursor.window_end}.
//
// Forward-clock guard: if a previously-persisted window_start sits in the
// future relative to s.now() (skew, restored backup, etc.), seed clamps
// window_end to window_start rather than producing an inverted window. The
// drain runs but the resulting [start, start] range matches no events.

type timeWindowProgress struct {
	cfg *schema.TimeWindowProgress
}

// formatVerb returns the configured format verb, defaulting to rfc3339.
func (p *timeWindowProgress) formatVerb() string {
	if p.cfg.Format == "" {
		return "rfc3339"
	}
	return p.cfg.Format
}

func (p *timeWindowProgress) seed(s *scope) error {
	verb := p.formatVerb()

	var startT time.Time
	if cur, ok := s.cursor["window_start"]; ok && cur != nil {
		t, err := toTime(cur)
		if err != nil {
			return fmt.Errorf("progress.time_window: cursor.window_start: %w", err)
		}
		startT = t.UTC()
	} else {
		got, err := s.evalValue(p.cfg.InitialOffset)
		if err != nil {
			return fmt.Errorf("progress.time_window.initial_offset: %w", err)
		}
		d, err := toDuration(got)
		if err != nil {
			return fmt.Errorf("progress.time_window.initial_offset: %w", err)
		}
		startT = s.now().Add(-d).UTC()
	}

	endT := s.now().UTC()
	if endT.Before(startT) {
		endT = startT
	}

	startV, err := applyFormat(verb, startT)
	if err != nil {
		return fmt.Errorf("progress.time_window.format=%s window_start: %w", verb, err)
	}
	endV, err := applyFormat(verb, endT)
	if err != nil {
		return fmt.Errorf("progress.time_window.format=%s window_end: %w", verb, err)
	}

	s.cursor["window_start"] = startV
	s.cursor["window_end"] = endV
	return nil
}

func (p *timeWindowProgress) advance(s *scope, _ []any) error {
	// Slide the window: the end of the just-finished drain becomes the
	// start of the next. window_end stays put so a Snapshot read between
	// advance and the next seed still describes the last-known window;
	// the next seed will overwrite it against s.now().
	if end, ok := s.cursor["window_end"]; ok && end != nil {
		s.cursor["window_start"] = end
	}
	return nil
}

func (p *timeWindowProgress) shouldSkipForPhase(schema.Request, string) bool { return false }
func (p *timeWindowProgress) currentPhase(*scope) string                     { return "" }
func (p *timeWindowProgress) phaseTransition(*scope, string, *stepResult) (string, bool, bool, error) {
	return "", false, true, nil
}

// ---- async_job ----
//
// Phase machine over cursor.phase:
//
//   phase=submit → run the submit step; on success advance to "poll"
//                  (or "fetch" if poll is absent); extract submit.extract
//                  fields into cursor; want_more=true (start polling now).
//   phase=poll   → run the poll step; if complete_when is satisfied,
//                  extract poll.extract fields into cursor and advance to
//                  "fetch" (or finish if fetch is absent); want_more=true
//                  to drive into fetch immediately.
//                  If complete_when is unsatisfied, want_more=false so the
//                  caller waits for the next --interval tick before polling
//                  again.
//   phase=fetch  → run the fetch step; emit events; apply
//                  on_complete.cursor_update; reset phase to "submit"
//                  (or whichever role is the first declared role); want_more=false.

type asyncJobProgress struct {
	cfg *schema.AsyncJobProgress
	// eventsAt mirrors doc.Response.EventsAt so applyOnComplete can locate
	// the events list inside the producer step's body when
	// cursor_update.kind=latest_event_timestamp. The runner's main events
	// loop runs locateEvents on the producer body separately AFTER this
	// call returns; we re-walk the same body here to keep applyOnComplete
	// self-contained (the cost is one extra body walk per drain; the
	// trade-off is that on_complete reasoning doesn't need to thread
	// runner state).
	eventsAt schema.Path
	ndjson   bool
}

// firstPhase is the initial phase: submit if declared, else poll, else fetch.
func (p *asyncJobProgress) firstPhase() string {
	if p.cfg.Submit != nil {
		return "submit"
	}
	if p.cfg.Poll != nil {
		return "poll"
	}
	return "fetch"
}

// nextPhase returns the phase to advance to after the named phase completes
// successfully. Returns "" when no further phase is declared (job done).
func (p *asyncJobProgress) nextPhase(after string) string {
	switch after {
	case "submit":
		if p.cfg.Poll != nil {
			return "poll"
		}
		if p.cfg.Fetch != nil {
			return "fetch"
		}
	case "poll":
		if p.cfg.Fetch != nil {
			return "fetch"
		}
	}
	return ""
}

// stepID returns the request id for the given phase, or "" if no step is
// declared for that phase.
func (p *asyncJobProgress) stepID(phase string) string {
	switch phase {
	case "submit":
		if p.cfg.Submit != nil {
			return p.cfg.Submit.Step
		}
	case "poll":
		if p.cfg.Poll != nil {
			return p.cfg.Poll.Step
		}
	case "fetch":
		if p.cfg.Fetch != nil {
			return p.cfg.Fetch.Step
		}
	}
	return ""
}

// phaseForStep returns the phase name whose step id matches stepID, or "".
func (p *asyncJobProgress) phaseForStep(stepID string) string {
	if p.cfg.Submit != nil && p.cfg.Submit.Step == stepID {
		return "submit"
	}
	if p.cfg.Poll != nil && p.cfg.Poll.Step == stepID {
		return "poll"
	}
	if p.cfg.Fetch != nil && p.cfg.Fetch.Step == stepID {
		return "fetch"
	}
	return ""
}

func (p *asyncJobProgress) seed(s *scope) error {
	if _, ok := s.cursor["phase"]; !ok {
		s.cursor["phase"] = p.firstPhase()
	}
	// cursor.last_timestamp is written by on_complete.cursor_update
	// (kind=use_now or latest_event_timestamp) at drain end — it is absent
	// on first drain. Templates that wire {ref: cursor.last_timestamp}
	// into a submit body send no since= value on first drain (the http
	// layer skips nil values); authors who need an explicit since="" can
	// add a {default: ""} branch on the ref. Operators who need a bounded
	// first window should pre-seed cursor.last_timestamp via the snapshot
	// file.
	//
	// async_job has no initial.lookback slot in its IR schema, so seed
	// performs no extra cursor writes here.
	return nil
}

func (p *asyncJobProgress) advance(_ *scope, _ []any) error {
	// Cursor advancement happens in phaseTransition for async_job (the
	// on_complete.cursor_update directive). This method is called per the
	// normal contract but for async_job the work is already done.
	return nil
}

// shouldSkipForPhase: in async_job mode, only the request matching the
// current phase runs. Everything else is skipped silently.
func (p *asyncJobProgress) shouldSkipForPhase(req schema.Request, phase string) bool {
	if phase == "" {
		return false
	}
	wantID := p.stepID(phase)
	return req.ID != wantID
}

func (p *asyncJobProgress) currentPhase(s *scope) string {
	v, _ := s.cursor["phase"].(string)
	return v
}

func (p *asyncJobProgress) phaseTransition(s *scope, stepID string, res *stepResult) (string, bool, bool, error) {
	phase := p.phaseForStep(stepID)
	if phase == "" {
		// Reachable only if a future code path lets a non-role step id into
		// phaseTransition (shouldSkipForPhase + the duplicate-ID validator
		// keep this off the runtime path today). Surface loudly so the
		// mistake doesn't become an undocumented "helper-step" feature.
		return p.currentPhase(s), false, false, fmt.Errorf("internal: unknown async_job step %q (expected submit/poll/fetch)", stepID)
	}

	switch phase {
	case "submit":
		if p.cfg.Submit != nil {
			if err := p.captureExtracts(s, res.body, p.cfg.Submit.Extract); err != nil {
				return phase, false, false, err
			}
		}
		next := p.nextPhase("submit")
		if next == "" {
			// No poll, no fetch declared. submit IS the producer, so its
			// body is the events-bearing body that latest_event_timestamp
			// walks.
			s.cursor["phase"] = p.firstPhase()
			if err := p.applyOnComplete(s, res.body); err != nil {
				return phase, false, false, err
			}
			return "", false, true, nil
		}
		s.cursor["phase"] = next
		return next, true, false, nil

	case "poll":
		// Evaluate complete_when against the poll-step's body.
		done := true
		if p.cfg.Poll != nil && p.cfg.Poll.CompleteWhen != nil {
			// Defer-based restore so a future panic inside evalPredicate
			// cannot leak the poll body / headers into the next iteration's
			// scope. scope.body / scope.responseHeaders must be "scoped to
			// the complete_when predicate evaluation" — enforce that with
			// the language. The predicate may reference either the legacy
			// body.<path> root or the new response.body.<path> /
			// response.header.<name> roots; both resolve against res.
			prevBody := s.body
			prevHeaders := s.responseHeaders
			s.body = res.body
			s.responseHeaders = res.headers
			defer func() {
				s.body = prevBody
				s.responseHeaders = prevHeaders
			}()
			ok, err := s.evalPredicate(*p.cfg.Poll.CompleteWhen)
			if err != nil {
				return phase, false, false, fmt.Errorf("async_job.poll.complete_when: %w", err)
			}
			done = ok
		}
		if !done {
			// Stay in poll; wait for next --interval tick.
			return phase, false, false, nil
		}
		if p.cfg.Poll != nil {
			if err := p.captureExtracts(s, res.body, p.cfg.Poll.Extract); err != nil {
				return phase, false, false, err
			}
		}
		next := p.nextPhase("poll")
		if next == "" {
			// No fetch declared; poll IS the producer, so its body is the
			// events-bearing body that latest_event_timestamp walks.
			s.cursor["phase"] = p.firstPhase()
			if err := p.applyOnComplete(s, res.body); err != nil {
				return phase, false, false, err
			}
			return "", false, true, nil
		}
		s.cursor["phase"] = next
		return next, true, false, nil

	case "fetch":
		// fetch always produces events; the caller pulls them from res.body
		// via the normal locateEvents path. We just reset the phase and
		// apply on_complete.
		s.cursor["phase"] = p.firstPhase()
		// Reset extract-derived cursor fields so the next job starts clean.
		p.clearExtractedCursors(s)
		if err := p.applyOnComplete(s, res.body); err != nil {
			return phase, false, false, err
		}
		return "", false, true, nil
	}
	return phase, false, false, fmt.Errorf("unknown phase %q", phase)
}

func (p *asyncJobProgress) captureExtracts(s *scope, body any, ex map[string]schema.AsyncExtract) error {
	for name, axc := range ex {
		got, ok, err := s.resolveBodyPath(body, axc.From)
		if err != nil {
			return fmt.Errorf("async_job.<phase>.extract.%s: %w", name, err)
		}
		if !ok {
			continue
		}
		s.cursor[name] = got
	}
	return nil
}

func (p *asyncJobProgress) clearExtractedCursors(s *scope) {
	// AsyncFetchStep has no Extract field today; if one is added, extend
	// this clear walk so "reset at fetch completion" stays consistent.
	if p.cfg.Submit != nil {
		for name := range p.cfg.Submit.Extract {
			delete(s.cursor, name)
		}
	}
	if p.cfg.Poll != nil {
		for name := range p.cfg.Poll.Extract {
			delete(s.cursor, name)
		}
	}
}

// applyOnComplete advances cursor.last_timestamp per the cursor_update
// directive on the async_job's on_complete block. producerBody is the
// events-bearing step's decoded body — required for kind=latest_event_timestamp
// (the helper locates events at p.eventsAt and walks them for the max
// timestamp at cu.EventTime.Path) and ignored for use_now / stateless.
func (p *asyncJobProgress) applyOnComplete(s *scope, producerBody any) error {
	if p.cfg.OnComplete == nil || p.cfg.OnComplete.CursorUpdate == nil {
		return nil
	}
	cu := p.cfg.OnComplete.CursorUpdate
	switch cu.Kind {
	case "stateless":
		return nil
	case "use_now":
		t := s.now()
		if cu.Lookback != nil {
			got, err := s.evalValue(*cu.Lookback)
			if err != nil {
				return fmt.Errorf("async_job.on_complete.cursor_update.lookback: %w", err)
			}
			d, err := toDuration(got)
			if err != nil {
				return fmt.Errorf("async_job.on_complete.cursor_update.lookback: %w", err)
			}
			t = t.Add(-d)
		}
		s.cursor["last_timestamp"] = t.UTC().Format(time.RFC3339)
		return nil
	case "latest_event_timestamp":
		if cu.EventTime == nil {
			return fmt.Errorf("async_job.on_complete.cursor_update.event_time: required for kind=latest_event_timestamp")
		}
		// p.eventsAt mirrors doc.Response.EventsAt; the validator enforces
		// response.body.<path> at that slot, so stripBodyRoot trims the two
		// leading segments and the locateEvents walk runs against the
		// producer body.
		parts, stepID, err := stripBodyRoot(p.eventsAt)
		if err != nil {
			return fmt.Errorf("async_job.on_complete.cursor_update: response.events_at: %w", err)
		}
		body := producerBody
		if stepID != "" {
			b, ok := s.steps[stepID]
			if !ok {
				return fmt.Errorf("async_job.on_complete.cursor_update: response.events_at references step %q with no captured body", stepID)
			}
			body = b
		}
		events, err := locateEvents(body, parts, p.ndjson)
		if err != nil {
			return fmt.Errorf("async_job.on_complete.cursor_update: locate events: %w", err)
		}
		// Build a synthetic TimestampProgress so the events-walk helper
		// can reuse the same code path. advanceTimestampCursor reads only
		// EventTime.Path and Lookback today; Initial stays nil here.
		cfg := &schema.TimestampProgress{
			EventTime: *cu.EventTime,
			Lookback:  cu.Lookback,
		}
		return advanceTimestampCursor(s, events, cfg, "async_job.on_complete.cursor_update")
	}
	return fmt.Errorf("unknown cursor_update.kind %q", cu.Kind)
}
