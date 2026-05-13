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
// # Supported spec variants
//
//   - auth:        none, bearer, basic, api_key (header + in_query), custom,
//     oauth2.client_credentials, oauth2.password_grant,
//     oauth2.<grant>.cache, multi_mode
//   - body:        json, form, raw (request bodies)
//   - response:    json, ndjson
//   - pagination:  none, cursor_token, page_number, offset, link_header,
//     next_url_in_body, scroll_id, graphql_relay
//   - progress:    stateless, latest_event_timestamp, max_event_field,
//     use_now, time_window, async_job
//
// fan_out and requests[].cache are accepted by the validator but
// unreachable in the runner today.
package client
