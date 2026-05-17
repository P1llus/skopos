// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/p1llus/skopos/schema"
)

// fanOutResult bundles what runFanOut produces for one fan_out step. The
// fields mirror the producer-step signal set that runIteration's existing
// single-request path returns: mergedBody is what binds into
// scope.steps[req.ID] and what locateEvents walks when the fan_out step is
// the producer; lastHeaders mirrors the single-request producerHeaders
// (no link-header pagination crosses a fan_out boundary today, but the
// field keeps the producer-iteration shape uniform); advance/fatal carry
// the on_status / error.mode dispatch decisions.
type fanOutResult struct {
	mergedBody  any
	lastHeaders http.Header
	advance     bool
	fatal       bool
}

// runFanOut executes req once per element of req.FanOut.Over, binds
// scope.item / scope.itemBinding for each per-item evaluation, and merges
// the per-item response bodies according to req.FanOut.Merge.
//
// # Per-item error handling
//
// Each per-item exchange runs through the same on_status / error.mode
// dispatcher as a single-request step:
//
//   - on_status: skip            drop this item and continue with the next.
//   - on_status: fail            abort the drain (fatal=true).
//   - on_status: empty_events    drop this item (no body contribution).
//   - on_status: invalidate_cache clear OAuth2 + step caches and stop the
//     fan-out early; the cursor still advances normally.
//   - error.mode: standard       end the drain iteration here; advance=false
//     so neither pagination.advance nor end-of-drain progress.advance run.
//   - error.mode: warn           log and drop the item.
//   - error.mode: fail           abort the drain (fatal=true).
//
// # Merge semantics
//
//   - flatten (default): each per-item response body MUST decode as a JSON
//     list; mergedBody is the concatenation. A non-list body is rejected at
//     merge time — authors who want lists-of-objects use merge: wrap.
//   - wrap: mergedBody is a list of the per-item bodies as-is, preserving
//     each item's response shape.
//
// # Extract / cache
//
// req.Extract on a fan_out step runs after each per-item response —
// extracts are last-write-wins by design (matches the sequential N-copies
// intuition). The validator rejects req.Cache alongside fan_out because
// the two cannot compose meaningfully.
func (r *Runner) runFanOut(
	ctx context.Context,
	client *http.Client,
	logger *log.Logger,
	s *scope,
	req schema.Request,
	errMode string,
	iter int,
	phase string,
) (fanOutResult, error) {
	over, err := s.evalValue(req.FanOut.Over)
	if err != nil {
		return fanOutResult{}, fmt.Errorf("%s.fan_out.over: %w", reqLabel(req), err)
	}
	items, err := coerceFanOutList(over)
	if err != nil {
		return fanOutResult{}, fmt.Errorf("%s.fan_out.over: %w", reqLabel(req), err)
	}

	merge := req.FanOut.Merge
	if merge == "" {
		merge = "flatten"
	}

	// Stash and restore the prior binding so nested fan_out (or sibling
	// steps that share the scope) cannot leak an item value out.
	prevItem, prevName := s.item, s.itemBinding
	s.itemBinding = req.FanOut.As
	defer func() {
		s.item = prevItem
		s.itemBinding = prevName
	}()

	out := fanOutResult{advance: true}
	bodies := make([]any, 0, len(items))

	for idx, it := range items {
		s.item = it

		var trace *httpTrace
		if r.Tracer != nil {
			trace = &httpTrace{}
		}
		res, runErr := s.executeRequest(ctx, client, req, trace)
		if r.Tracer != nil {
			r.Tracer.OnExchange(buildExchange(r.Doc, req, trace, runErr, iter, phase))
		}

		if usErr, ok := asUnexpectedStatus(runErr); ok && res != nil {
			action := req.OnStatus[usErr.status]
			switch action {
			case "skip":
				logger.Printf("client: %s[%d] status %d → skip", reqLabel(req), idx, usErr.status)
				continue
			case "fail":
				out.fatal = true
				return out, fmt.Errorf("on_status: fail (status %d) in %s[%d]", usErr.status, reqLabel(req), idx)
			case "empty_events":
				logger.Printf("client: %s[%d] status %d → empty_events", reqLabel(req), idx, usErr.status)
				continue
			case "invalidate_cache":
				cleared := s.invalidateAuthCaches(r.Doc.Auth)
				cleared = append(cleared, s.invalidateStepCaches(r.Doc)...)
				if len(cleared) == 0 {
					logger.Printf("client: %s[%d] status %d → invalidate_cache (no cache to invalidate; stopping fan_out)", reqLabel(req), idx, usErr.status)
				} else {
					logger.Printf("client: %s[%d] status %d → invalidate_cache (cleared %v; stopping fan_out)", reqLabel(req), idx, usErr.status, cleared)
				}
				// Stop the fan-out early; the merged body is whatever we
				// collected so far. Same end-state as a normal cache-flush.
				goto done
			default:
				switch errMode {
				case "fail":
					out.fatal = true
					return out, fmt.Errorf("%s[%d]: %w", reqLabel(req), idx, redactURLError(usErr))
				case "warn":
					logger.Printf("client: %s[%d] WARN: %v", reqLabel(req), idx, redactURLError(usErr))
					continue
				default:
					logger.Printf("client: %s[%d]: %v (standard mode; cursor not advanced)", reqLabel(req), idx, redactURLError(usErr))
					out.advance = false
					return out, nil
				}
			}
		} else if runErr != nil {
			switch errMode {
			case "fail":
				out.fatal = true
				return out, fmt.Errorf("%s[%d]: %w", reqLabel(req), idx, redactURLError(runErr))
			case "warn":
				logger.Printf("client: %s[%d] WARN: %v", reqLabel(req), idx, redactURLError(runErr))
				continue
			default:
				logger.Printf("client: %s[%d]: %v (standard mode; cursor not advanced)", reqLabel(req), idx, redactURLError(runErr))
				out.advance = false
				return out, nil
			}
		}

		if err := s.runExtracts(res, req.Extract); err != nil {
			return fanOutResult{}, fmt.Errorf("%s[%d].extract: %w", reqLabel(req), idx, err)
		}

		bodies = append(bodies, res.body)
		out.lastHeaders = res.headers
	}

done:
	merged, err := mergeFanOutBodies(merge, bodies)
	if err != nil {
		return fanOutResult{}, fmt.Errorf("%s.fan_out.merge: %w", reqLabel(req), err)
	}
	out.mergedBody = merged
	return out, nil
}

// coerceFanOutList resolves the value of fan_out.over to a []any. A nil
// over (the cursor / state field is unset on the first iteration) is
// treated as the empty list — no items, no error — matching the spirit of
// {ref: ..., default: ""} elsewhere in the value layer.
func coerceFanOutList(v any) ([]any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case []any:
		return x, nil
	}
	return nil, fmt.Errorf("must resolve to a list; got %T", v)
}

// mergeFanOutBodies combines the per-item response bodies according to the
// fan_out.merge mode. flatten concatenates JSON lists element-by-element
// (rejecting non-list bodies); wrap returns the bodies as-is in a list.
func mergeFanOutBodies(mode string, bodies []any) (any, error) {
	switch mode {
	case "flatten":
		out := make([]any, 0, len(bodies))
		for i, b := range bodies {
			arr, ok := b.([]any)
			if !ok {
				return nil, fmt.Errorf("merge: flatten requires every per-item response body to be a list; item %d returned %T (use merge: wrap for non-list bodies)", i, b)
			}
			out = append(out, arr...)
		}
		return out, nil
	case "wrap":
		out := make([]any, 0, len(bodies))
		out = append(out, bodies...)
		return out, nil
	}
	return nil, fmt.Errorf("unknown merge mode %q (validator should have caught this)", mode)
}
