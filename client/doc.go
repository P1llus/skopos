// SPDX-License-Identifier: Apache-2.0

// Package client is the in-process Go interpreter for schema.Doc.
//
// It takes a *schema.Doc, executes the pull loop the document describes,
// emits events to a Sink, and persists runtime state through a Store.
// No code generation, no compilation — the parsed IR is the program.
//
// # Quick start
//
//	doc, _ := schema.Load(file)
//	if diags := schema.Validate(doc); len(diags) > 0 {
//	    return fmt.Errorf("invalid spec: %v", diags)
//	}
//	r := &client.Runner{
//	    Doc:  doc,
//	    Sink: client.NewJSONLSink(os.Stdout),
//	}
//	return r.Drain(ctx)
//
// # Drain lifecycle
//
// One Drain runs one full pull session against a single *schema.Doc:
//
//  1. Store.Load seeds the in-memory state.* map by layering the IR's
//     declared state.<name>.default Values under any persisted values.
//  2. Per-drain wipe: every state field whose lifetime is "per-drain
//     scratch" (the to: destination of any pagination: write) is reset
//     to its declared default. A drain that fails mid-page therefore
//     re-bootstraps pagination on the next start.
//  3. Pagination loop. Each iteration runs the requests: chain end-to-
//     end, honouring per-request if: predicates, terminate_when: re-fire
//     loops, on_status: dispatch, and the document-level error.mode.
//     After the producer step's response is bound, the runner emits each
//     event to Sink and evaluates the progress: list — one to: write per
//     entry, fired once per accepted page-response (including empty
//     pages). The active pagination variant's advance step decides
//     whether to terminate or loop.
//  4. The deferred teardown runs on every termination path — normal exit,
//     error.mode: warn, error.mode: fail, MaxPages, ctx cancellation — so a
//     partial drain still persists what it reached. It drains the sink,
//     calls Sink.Flush, THEN Store.Save: events are made durable before
//     state records progress past them. See docs/runtime.md §2 for the
//     authoritative description.
//
// Two opt-in Runner knobs tune this loop for long-lived continuous runs:
// SinkBuffer hands events to a single consumer goroutine so a slow Sink does
// not stall the next page fetch, and CheckpointPages persists state
// mid-drain so a hard crash re-pulls at most N pages. Both preserve the
// events-before-state ordering above.
//
// # Namespaces and lifetimes
//
//   - state.* is the only persisted namespace; Snapshot.State is what
//     Store reads and writes. Operator-config and persistent fields
//     round-trip across drains; per-drain scratch fields are wiped
//     at drain start.
//   - cache.* lives in process memory only. Slots are written by Cache
//     blocks (auth.oauth2.<grant>.cache or requests[].cache) and never
//     persisted; the first request to need a cached value after a
//     restart misses, re-runs the underlying step, and repopulates.
//   - events.* / extract.* / steps.* / response.* are per-page or
//     per-iteration scratch, rebound by the runner before each
//     evaluation.
//
// # Concurrency
//
// One Runner is one logical pull source. Drain mutates internal scope
// state; calling Drain concurrently on the same Runner races. Fan-out
// across multiple sources is the caller's responsibility: build N
// independent Runner values, each with its own *schema.Doc / Store /
// Sink, and call Drain on each from its own goroutine. The Runner type
// holds no package-global state, so independent Runners are
// independent. Sink and Store implementations shared between Runners
// MUST be safe for concurrent use; the bundled JSONLSink, JSONLTracer,
// and MemoryStore are.
//
// # Redaction
//
// Every log line, error message, and Tracer field that mentions a Value
// routes through the schema.IsSecret detector. URLs emit with scheme +
// host + path only; query strings and userinfo are stripped. Sensitive
// header names (Authorization, Cookie, Proxy-Authorization, Set-Cookie)
// and any header / query whose IR Value reaches a secret-typed state
// field are redacted by content. Request / response bodies are
// metadata-only (byte length + leading-byte classification). See
// docs/runtime.md §7 for the full policy and trace.go's package
// comments for the per-field rationale.
package client
