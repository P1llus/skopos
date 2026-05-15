// SPDX-License-Identifier: Apache-2.0

// Package client is the in-process Go interpreter for schema.Doc.
//
// It takes a *schema.Doc, executes the pull loop described by the document,
// emits events to a Sink, and persists runtime state through a Store.
// No code generation, no compilation — the spec document is the program.
//
// Quick start:
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
// One Drain runs one full pull session: paginate until want_more=false (or
// an async_job phase machine hits a wait state), persist state through the
// configured Store, and return. The caller (e.g. cmd/skopos run) handles
// scheduling — sleeping between drains, deciding when to stop, etc.
//
// The set of IR shapes the runner understands at any given revision is
// described in the project's user-facing documentation; this package
// comment intentionally does not enumerate it to avoid drifting out of
// sync with the runtime.
package client
