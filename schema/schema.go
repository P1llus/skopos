// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Doc is the top-level IR document.
type Doc struct {
	// IRVersion is the spec wire-format version. Must equal IRVersion.
	IRVersion string `yaml:"ir_version" json:"ir_version"`
	// State declares typed field bindings for operator-supplied configuration
	// and runtime-mutated cursor fields. Optional.
	State *State `yaml:"state,omitempty" json:"state,omitempty"`
	// Defaults holds cross-cutting defaults that apply to all requests.
	// Optional.
	Defaults *Defaults `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	// Auth is the discriminated-union auth block. Exactly one variant.
	Auth Auth `yaml:"auth" json:"auth"`
	// Requests is the ordered list of HTTP requests that make up the chain.
	Requests []Request `yaml:"requests" json:"requests"`
	// Response describes how to decode the producer step's response body.
	Response Response `yaml:"response" json:"response"`
	// Pagination is the discriminated-union pagination block.
	Pagination Pagination `yaml:"pagination" json:"pagination"`
	// Progress is the discriminated-union progress (cursor-advancement) block.
	Progress Progress `yaml:"progress" json:"progress"`
	// Error configures how HTTP errors are surfaced. Optional.
	Error *ErrorBlock `yaml:"error,omitempty" json:"error,omitempty"`
}

// ---- State ----

// State holds typed field declarations for operator-supplied configuration
// and runtime-mutated cursor fields that authors need to name explicitly.
type State struct {
	// Fields maps each state-field name to its declaration. The key is the
	// name authors reference via {ref: state.<name>}.
	Fields map[string]FieldDecl `yaml:"fields,omitempty" json:"fields,omitempty"`
}

// FieldDecl is the declaration for a single state field.
type FieldDecl struct {
	// Type is the field's declared shape. One of:
	// string | int | bool | secret | duration | url | enum.
	Type string `yaml:"type" json:"type"`
	// Default is the literal value used when the operator does not supply
	// one. When set it must match Type.
	Default interface{} `yaml:"default,omitempty" json:"default,omitempty"`
	// Values is the allowed enumeration. Only valid when Type == "enum".
	Values []string `yaml:"values,omitempty" json:"values,omitempty"`
	// Mutability marks the field as "config" (operator-set; the default,
	// not written back at runtime) or "runtime" (program-mutated; targets
	// must persist the value across iterations, e.g. a token cached by an
	// OAuth2 grant). Auto-registered runtime fields (the OAuth2
	// cache.store_in slot, request-level cache.store_in slots) carry
	// "runtime" implicitly.
	Mutability string `yaml:"mutability,omitempty" json:"mutability,omitempty"`
}

// ---- Defaults ----

// Defaults holds cross-cutting defaults that apply to all requests.
type Defaults struct {
	// BaseURL is prepended to each request's Path (ignored when a request
	// uses URL instead).
	BaseURL Value `yaml:"base_url" json:"base_url"`
}

// ---- Auth ----

// Auth is the discriminated-union auth block. Exactly one variant key must
// be present.
type Auth struct {
	// None selects the no-auth variant. The empty struct marks the variant.
	None *struct{} `yaml:"none,omitempty" json:"none,omitempty"`
	// Bearer selects the bearer-token variant.
	Bearer *BearerAuth `yaml:"bearer,omitempty" json:"bearer,omitempty"`
	// Basic selects the HTTP basic-auth variant.
	Basic *BasicAuth `yaml:"basic,omitempty" json:"basic,omitempty"`
	// APIKey selects the named-header (or named-query) API-key variant.
	APIKey *APIKeyAuth `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	// Custom selects the single-custom-header variant.
	Custom *CustomAuth `yaml:"custom,omitempty" json:"custom,omitempty"`
	// OAuth2 selects the OAuth2 variant (client_credentials / password_grant).
	OAuth2 *OAuth2Auth `yaml:"oauth2,omitempty" json:"oauth2,omitempty"`
	// MultiMode dispatches between auth strategies at runtime via predicates.
	MultiMode *MultiModeAuth `yaml:"multi_mode,omitempty" json:"multi_mode,omitempty"`
}

// BearerAuth sends "Authorization: Bearer <token>" on every request.
type BearerAuth struct {
	// Token resolves to the bearer token at request time.
	Token Value `yaml:"token" json:"token"`
}

// BasicAuth sends "Authorization: Basic <base64(username:password)>".
type BasicAuth struct {
	// Username is the basic-auth user name.
	Username Value `yaml:"username" json:"username"`
	// Password is the basic-auth password.
	Password Value `yaml:"password" json:"password"`
}

// APIKeyAuth sends the key in a named header (or query param when InQuery is true).
type APIKeyAuth struct {
	// Header is the wire name of the header (or query parameter when
	// InQuery is true) that carries the credential.
	Header string `yaml:"header" json:"header"`
	// Value resolves to the credential at request time.
	Value Value `yaml:"value" json:"value"`
	// InQuery, when true, sends the credential as a URL query parameter
	// instead of an HTTP header.
	InQuery bool `yaml:"in_query,omitempty" json:"in_query,omitempty"`
}

// CustomAuth sends a single custom header with an arbitrary value.
type CustomAuth struct {
	// Header is the wire name of the credential-bearing header.
	Header string `yaml:"header" json:"header"`
	// Value resolves to the credential at request time.
	Value Value `yaml:"value" json:"value"`
}

// OAuth2Auth selects the OAuth2 grant type. Exactly one grant key must be
// present.
type OAuth2Auth struct {
	// ClientCredentials selects the OAuth2 client_credentials flow.
	ClientCredentials *ClientCredentialsGrant `yaml:"client_credentials,omitempty" json:"client_credentials,omitempty"`
	// PasswordGrant selects the OAuth2 password grant (RFC 6749 §4.3).
	PasswordGrant *PasswordGrant `yaml:"password_grant,omitempty" json:"password_grant,omitempty"`
}

// ClientCredentialsGrant is the OAuth2 client_credentials flow.
type ClientCredentialsGrant struct {
	// TokenURL is the OAuth2 token endpoint.
	TokenURL Value `yaml:"token_url" json:"token_url"`
	// ClientID identifies the client to the authorization server.
	ClientID Value `yaml:"client_id" json:"client_id"`
	// ClientSecret authenticates the client to the authorization server.
	ClientSecret Value `yaml:"client_secret" json:"client_secret"`
	// Scopes is the optional space-separated OAuth2 scope list sent in
	// the token request (RFC 6749 §3.3).
	Scopes []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
	// Audience is the optional RFC 8693 audience claim sent in the token
	// request.
	Audience string `yaml:"audience,omitempty" json:"audience,omitempty"`
	// Cache describes how the fetched token is cached across iterations.
	Cache *TokenCache `yaml:"cache,omitempty" json:"cache,omitempty"`
}

// PasswordGrant is the OAuth2 password grant (RFC 6749 §4.3): exchange a
// username/password pair for a short-lived access token. The token is
// cached the same way as client_credentials.
type PasswordGrant struct {
	// TokenURL is the OAuth2 token endpoint.
	TokenURL Value `yaml:"token_url" json:"token_url"`
	// Username is the resource-owner user name.
	Username Value `yaml:"username" json:"username"`
	// Password is the resource-owner password.
	Password Value `yaml:"password" json:"password"`
	// ClientID is optional because some servers authenticate the client
	// via Basic auth on the token endpoint and do not require a
	// form-encoded client_id alongside the user credentials.
	ClientID *Value `yaml:"client_id,omitempty" json:"client_id,omitempty"`
	// Scopes is the optional space-separated OAuth2 scope list.
	Scopes []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
	// Cache describes how the fetched token is cached across iterations.
	Cache *TokenCache `yaml:"cache,omitempty" json:"cache,omitempty"`
}

// TokenCache describes how the fetched OAuth2 token is cached across iterations.
type TokenCache struct {
	// StoreIn names the state key the cached token lives in
	// (auto-registered as a runtime string field; not declared in
	// state.fields).
	StoreIn string `yaml:"store_in" json:"store_in"`
	// ExpiryField is a body-relative Path locating the response field
	// that carries the token's lifetime. Typed as Path for consistency
	// with every other body-relative locator in the IR; dotted-string
	// YAML decodes cleanly.
	ExpiryField Path `yaml:"expiry_field" json:"expiry_field"`
	// ExpiryBuffer is a Go-style duration; the runtime refreshes the
	// cached token once the remaining lifetime drops below this buffer.
	ExpiryBuffer string `yaml:"expiry_buffer" json:"expiry_buffer"`
}

// MultiModeAuth dispatches between auth strategies at runtime based on a
// state or cursor field.
type MultiModeAuth struct {
	// Branches is the ordered list of (when, auth) arms. The first arm
	// whose predicate is true wins.
	Branches []AuthBranch `yaml:"branches" json:"branches"`
	// Default is the fallback used when no branch matches.
	Default AuthDefault `yaml:"default" json:"default"`
}

// AuthBranch is one arm of a multi_mode auth dispatch.
type AuthBranch struct {
	// When is the predicate that selects this branch.
	When Predicate `yaml:"when" json:"when"`
	// Auth is the variant applied while the predicate is true.
	Auth Auth `yaml:"auth" json:"auth"`
}

// AuthDefault wraps the fallback Auth for a multi_mode dispatch.
type AuthDefault struct {
	// Auth is the variant applied when no branch matches.
	Auth Auth `yaml:"auth" json:"auth"`
}

// ---- Requests ----

// Request describes a single HTTP request in the chain.
type Request struct {
	// ID is the optional step label. When set it joins the
	// steps.<id>.body namespace and can be named as an async_job role
	// step.
	ID string `yaml:"id,omitempty" json:"id,omitempty"`
	// Method is the HTTP verb (GET/POST/PUT/PATCH/DELETE/HEAD).
	Method string `yaml:"method" json:"method"`
	// Path is the request path; combined with defaults.base_url.
	// Mutually exclusive with URL.
	Path *Value `yaml:"path,omitempty" json:"path,omitempty"`
	// URL is the absolute request URL. Mutually exclusive with Path.
	URL *Value `yaml:"url,omitempty" json:"url,omitempty"`
	// Query is the URL query map; each value resolves to a string.
	Query map[string]Value `yaml:"query,omitempty" json:"query,omitempty"`
	// Headers is the wire header map; each value resolves to a string.
	Headers map[string]Value `yaml:"headers,omitempty" json:"headers,omitempty"`
	// Body is the request body. Discriminated-union (json/form/raw).
	Body *Body `yaml:"body,omitempty" json:"body,omitempty"`
	// Extract pulls named values out of the response body or headers.
	Extract []ExtractVar `yaml:"extract,omitempty" json:"extract,omitempty"`
	// FanOut lifts the step into a per-item iteration over a list Value.
	FanOut *FanOut `yaml:"fan_out,omitempty" json:"fan_out,omitempty"`
	// ExpectStatus is the set of status codes the runner treats as
	// successful. Defaults to {200} when empty.
	ExpectStatus []int `yaml:"expect_status,omitempty" json:"expect_status,omitempty"`
	// If is a predicate that gates execution of the step.
	If *Predicate `yaml:"if,omitempty" json:"if,omitempty"`
	// OnStatus maps a specific HTTP status code to a per-step
	// dispatcher verb (skip / fail / empty_events / invalidate_cache).
	OnStatus map[int]string `yaml:"on_status,omitempty" json:"on_status,omitempty"`
	// ProducesEvents marks the step whose decoded body the response
	// block (decode + events_at + placeholder_event) applies to. At
	// most one request in a chain may set this to true; when no
	// request sets it explicitly, the last request is the implicit
	// producer.
	ProducesEvents bool `yaml:"produces_events,omitempty" json:"produces_events,omitempty"`
	// Cache wraps a non-OAuth2 token-style step (custom JSON logins,
	// session-cookie refreshes, etc.) in a per-step expiry cache.
	Cache *RequestCache `yaml:"cache,omitempty" json:"cache,omitempty"`
}

// RequestCache is the generic step-level cache for non-OAuth2 token endpoints
// (custom JSON logins, session-cookie refreshes, etc.). It mirrors the
// contract of auth.oauth2.<grant>.cache: a successful response is captured
// into a runtime-mutable state slot and re-used until expiry_field minus
// expiry_buffer has passed.
type RequestCache struct {
	// StoreIn names the state slot (auto-registered as a runtime string
	// field). Authors must NOT also declare this under state.fields.
	StoreIn string `yaml:"store_in" json:"store_in"`
	// ExpiryField is the body-relative path to the response field
	// carrying the value's lifetime (a Go duration string when
	// ExpiryFormat is "duration", a numeric Unix-second timestamp when
	// "unix_seconds", etc.).
	ExpiryField Path `yaml:"expiry_field" json:"expiry_field"`
	// ExpiryBuffer is a Go-style duration; the runtime re-runs the step
	// once the remaining lifetime drops below this buffer.
	ExpiryBuffer string `yaml:"expiry_buffer" json:"expiry_buffer"`
	// ExpiryFormat is one of the format verbs; defaults to "duration".
	ExpiryFormat string `yaml:"expiry_format,omitempty" json:"expiry_format,omitempty"`
}

// ExtractVar names a value to pull from a response.
type ExtractVar struct {
	// Name is the destination key in the extract / cursor namespace.
	Name string `yaml:"name" json:"name"`
	// Path is the body-relative locator (used when Source is "body" or
	// unset).
	Path Path `yaml:"path,omitempty" json:"path,omitempty"`
	// Coerce, when set, applies a format verb (rfc3339, unix_seconds, ...)
	// to the extracted value before storing it.
	Coerce string `yaml:"coerce,omitempty" json:"coerce,omitempty"`
	// Source is "body" (default) or "header".
	Source string `yaml:"source,omitempty" json:"source,omitempty"`
	// Header is the response-header name; required when Source == "header".
	Header string `yaml:"header,omitempty" json:"header,omitempty"`
	// Target controls which namespace the extracted value lands in:
	// "extract" (default) → extract.<name>, visible to subsequent steps
	// in the same iteration and lost between iterations; "cursor" →
	// cursor.<name>, auto-registers a cursor field that persists across
	// iterations (use for multi-field cursors: worklists, freeze flags,
	// rolling-max timestamps).
	Target string `yaml:"target,omitempty" json:"target,omitempty"`
}

// FanOut lifts a step into a per-item iteration over a list.
type FanOut struct {
	// Over resolves to the list to iterate; must be a list-shaped Value
	// at evaluation time.
	Over Value `yaml:"over" json:"over"`
	// As is the per-item binding name introduced into the scope for the
	// duration of the iteration.
	As string `yaml:"as" json:"as"`
	// Merge controls how the per-iteration outputs combine: "flatten"
	// (default) or "wrap".
	Merge string `yaml:"merge,omitempty" json:"merge,omitempty"`
}

// ---- Body ----

// Body is the discriminated-union request body. Exactly one variant key must
// be present.
type Body struct {
	// JSON selects the application/json variant; values resolve and
	// marshal as a JSON object.
	JSON map[string]Value `yaml:"json,omitempty" json:"json,omitempty"`
	// Form selects the application/x-www-form-urlencoded variant.
	Form map[string]Value `yaml:"form,omitempty" json:"form,omitempty"`
	// Raw selects the literal-body variant; the resolved string is sent
	// as the request body.
	Raw *Value `yaml:"raw,omitempty" json:"raw,omitempty"`
}

// ---- Response ----

// Response describes how to decode the response body and locate events.
type Response struct {
	// Decode is the body decoder verb: "json" or "ndjson".
	Decode string `yaml:"decode" json:"decode"`
	// EventsAt is a body Path locating the events list. The zero Path
	// (empty / unset) means "body root": the entire decoded body IS the
	// events list, with no nesting to traverse.
	EventsAt Path `yaml:"events_at" json:"events_at"`
	// PlaceholderEvent is the Value emitted in place of an empty page
	// when the prior iteration produced zero events and pagination
	// advanced. Optional.
	PlaceholderEvent *Value `yaml:"placeholder_event,omitempty" json:"placeholder_event,omitempty"`
}

// ---- Pagination ----

// Pagination is the discriminated-union pagination block. Exactly one variant
// key must be present.
type Pagination struct {
	// None disables pagination — exactly one page is fetched per drain.
	None *struct{} `yaml:"none,omitempty" json:"none,omitempty"`
	// CursorToken advances via an opaque cursor token in the response body.
	CursorToken *CursorTokenPagination `yaml:"cursor_token,omitempty" json:"cursor_token,omitempty"`
	// PageNumber advances by incrementing a page-number query param.
	PageNumber *PageNumberPagination `yaml:"page_number,omitempty" json:"page_number,omitempty"`
	// Offset advances by adding batch_size to an offset query param.
	Offset *OffsetPagination `yaml:"offset,omitempty" json:"offset,omitempty"`
	// LinkHeader follows RFC 5988 Link: <url>; rel="next" headers.
	LinkHeader *LinkHeaderPagination `yaml:"link_header,omitempty" json:"link_header,omitempty"`
	// NextURLInBody reads a fully-formed next-page URL from the body.
	NextURLInBody *NextURLInBodyPagination `yaml:"next_url_in_body,omitempty" json:"next_url_in_body,omitempty"`
	// ScrollID maintains a server-side scroll session.
	ScrollID *ScrollIDPagination `yaml:"scroll_id,omitempty" json:"scroll_id,omitempty"`
	// GraphQLRelay follows GraphQL Relay-style cursor pagination.
	GraphQLRelay *GraphQLRelayPagination `yaml:"graphql_relay,omitempty" json:"graphql_relay,omitempty"`
}

// CursorTokenPagination advances via an opaque cursor token in the response body.
type CursorTokenPagination struct {
	// TokenAt is the body path of the next-page cursor.
	TokenAt Path `yaml:"token_at" json:"token_at"`
	// SendAs names where to send the cursor on subsequent requests:
	// "query.<param>" or "header.<name>".
	SendAs string `yaml:"send_as" json:"send_as"`
}

// PageNumberPagination advances by incrementing a page number query param.
type PageNumberPagination struct {
	// PageParam is the URL query parameter that carries the page number.
	PageParam string `yaml:"page_param" json:"page_param"`
	// HasMoreAt is the optional body path to a boolean has-more flag.
	// When unset, pagination terminates when an empty page arrives.
	HasMoreAt Path `yaml:"has_more_at,omitempty" json:"has_more_at,omitempty"`
	// BatchSize is the optional per-page size value.
	BatchSize *Value `yaml:"batch_size,omitempty" json:"batch_size,omitempty"`
}

// OffsetPagination advances by adding batch_size to an offset query param.
type OffsetPagination struct {
	// OffsetParam is the URL query parameter that carries the offset.
	OffsetParam string `yaml:"offset_param" json:"offset_param"`
	// BatchSize is the optional batch size. Defaults to the number of
	// events observed on the prior page when unset.
	BatchSize *Value `yaml:"batch_size,omitempty" json:"batch_size,omitempty"`
}

// LinkHeaderPagination follows RFC 5988 Link: <url>; rel="next" headers.
type LinkHeaderPagination struct {
	// Pattern is an optional URL-template filter; when set, only Link
	// targets matching the pattern continue pagination.
	Pattern string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
}

// NextURLInBodyPagination reads a fully-formed next-page URL from the body.
type NextURLInBodyPagination struct {
	// NextURLAt is the body path to the next-page URL.
	NextURLAt Path `yaml:"next_url_at" json:"next_url_at"`
}

// ScrollIDPagination maintains a server-side scroll session.
type ScrollIDPagination struct {
	// ScrollIDAt is the body path of the server-supplied scroll id.
	ScrollIDAt Path `yaml:"scroll_id_at" json:"scroll_id_at"`
	// SendAs names where to send the scroll id on subsequent requests:
	// "query.<param>" or "header.<name>".
	SendAs string `yaml:"send_as" json:"send_as"`
	// CompleteWhen is an optional predicate evaluated against the
	// producer body that terminates the scroll early.
	CompleteWhen *Predicate `yaml:"complete_when,omitempty" json:"complete_when,omitempty"`
}

// GraphQLRelayPagination follows GraphQL Relay-style cursor pagination.
type GraphQLRelayPagination struct {
	// HasNextPageAt is the body path to the boolean has-next-page flag.
	HasNextPageAt Path `yaml:"has_next_page_at" json:"has_next_page_at"`
	// EndCursorAt is the body path to the endCursor string.
	EndCursorAt Path `yaml:"end_cursor_at" json:"end_cursor_at"`
	// CursorVar is the author-chosen GraphQL variable name that carries
	// endCursor on the next request (typically "after").
	CursorVar string `yaml:"cursor_var" json:"cursor_var"`
}

// ---- Progress ----

// Progress is the discriminated-union cursor-advancement block. Exactly one
// variant key must be present.
type Progress struct {
	// Stateless does not advance the cursor — every drain pulls the
	// full page set.
	Stateless *struct{} `yaml:"stateless,omitempty" json:"stateless,omitempty"`
	// LatestEventTimestamp advances cursor.last_timestamp to the max of
	// the per-event timestamps in the current drain.
	LatestEventTimestamp *TimestampProgress `yaml:"latest_event_timestamp,omitempty" json:"latest_event_timestamp,omitempty"`
	// MaxEventField advances cursor.last_timestamp via the same max walk
	// as LatestEventTimestamp; spelled differently for templates that
	// already use "max event field" wording.
	MaxEventField *TimestampProgress `yaml:"max_event_field,omitempty" json:"max_event_field,omitempty"`
	// UseNow advances cursor.last_timestamp to now() on every drain.
	UseNow *UseNowProgress `yaml:"use_now,omitempty" json:"use_now,omitempty"`
	// TimeWindow slides a start/end window after each drain.
	TimeWindow *TimeWindowProgress `yaml:"time_window,omitempty" json:"time_window,omitempty"`
	// AsyncJob models a three-phase async job loop (submit → poll → fetch).
	AsyncJob *AsyncJobProgress `yaml:"async_job,omitempty" json:"async_job,omitempty"`
}

// TimestampProgress is the shared config for latest_event_timestamp and max_event_field.
type TimestampProgress struct {
	// EventTime holds the body path to the per-event timestamp field.
	EventTime EventTime `yaml:"event_time" json:"event_time"`
	// Initial holds the lookback duration applied on the first drain
	// (when no prior cursor.last_timestamp exists).
	Initial *Initial `yaml:"initial,omitempty" json:"initial,omitempty"`
	// Lookback is a duration Value subtracted from the chosen reference
	// on EVERY iteration (not just the first run). Pairs with
	// Initial.Lookback for time cursors that need both a first-run
	// lookback and an every-iteration lag.
	Lookback *Value `yaml:"lookback,omitempty" json:"lookback,omitempty"`
}

// UseNowProgress advances cursor.last_timestamp to now() on every iteration.
type UseNowProgress struct {
	// Lookback is a duration Value subtracted from now() on every
	// advance — the common "advance cursor to now() - lag so late
	// events still land on the next iteration" pattern.
	Lookback *Value `yaml:"lookback,omitempty" json:"lookback,omitempty"`
}

// EventTime holds the body path to the per-event timestamp field.
type EventTime struct {
	// Path is the body-relative locator inside the per-event object.
	Path Path `yaml:"path" json:"path"`
}

// Initial holds the lookback duration for the first run.
type Initial struct {
	// Lookback is the duration Value subtracted from now() on the first
	// drain when cursor.last_timestamp is unset.
	Lookback Value `yaml:"lookback" json:"lookback"`
}

// TimeWindowProgress advances a start/end time window after each drain.
type TimeWindowProgress struct {
	// InitialOffset is the duration Value subtracted from now() to seed
	// window_start on the first drain.
	InitialOffset Value `yaml:"initial_offset" json:"initial_offset"`
	// Format is the format verb applied to window_start / window_end
	// when serialised into requests. Defaults to "rfc3339" when empty.
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
}

// AsyncJobProgress models a three-phase async job loop (submit → poll → fetch).
type AsyncJobProgress struct {
	// Submit names the submit step and its post-submit cursor extractions.
	Submit *AsyncSubmitStep `yaml:"submit,omitempty" json:"submit,omitempty"`
	// Poll names the poll step, its completion predicate, and post-poll
	// cursor extractions.
	Poll *AsyncPollStep `yaml:"poll,omitempty" json:"poll,omitempty"`
	// Fetch names the fetch step (the one that emits events).
	Fetch *AsyncFetchStep `yaml:"fetch,omitempty" json:"fetch,omitempty"`
	// OnComplete describes how the cursor advances after a completed
	// async fetch.
	OnComplete *AsyncOnComplete `yaml:"on_complete,omitempty" json:"on_complete,omitempty"`
}

// AsyncSubmitStep names the submit step and its extractions.
type AsyncSubmitStep struct {
	// Step is the request id of the submit phase.
	Step string `yaml:"step" json:"step"`
	// Extract pulls fields out of the submit response into the cursor.
	Extract map[string]AsyncExtract `yaml:"extract,omitempty" json:"extract,omitempty"`
}

// AsyncPollStep names the poll step, its completion predicate, and extractions.
type AsyncPollStep struct {
	// Step is the request id of the poll phase.
	Step string `yaml:"step" json:"step"`
	// CompleteWhen is the predicate (evaluated against the poll body)
	// that terminates the poll loop.
	CompleteWhen *Predicate `yaml:"complete_when,omitempty" json:"complete_when,omitempty"`
	// Extract pulls fields out of the poll response into the cursor.
	Extract map[string]AsyncExtract `yaml:"extract,omitempty" json:"extract,omitempty"`
}

// AsyncFetchStep names the fetch step.
type AsyncFetchStep struct {
	// Step is the request id of the fetch phase.
	Step string `yaml:"step" json:"step"`
}

// AsyncExtract extracts a single field from a step's decoded body into the cursor.
type AsyncExtract struct {
	// Path is the body-relative locator resolved against the named
	// step's response body.
	Path Path `yaml:"path" json:"path"`
}

// AsyncOnComplete describes cursor advancement after a completed async fetch.
type AsyncOnComplete struct {
	// CursorUpdate is the structured cursor-advance directive
	// {kind, lookback?, event_time?}. The codec rejects the scalar
	// shorthand (cursor_update: use_now); authors must use the map
	// form (cursor_update: {kind: use_now}).
	CursorUpdate *CursorUpdateDirective `yaml:"cursor_update,omitempty" json:"cursor_update,omitempty"`
}

// CursorUpdateDirective is the structured cursor-advance directive on
// async_job.on_complete.
type CursorUpdateDirective struct {
	// Kind selects the reference time the cursor advances to. One of:
	// use_now | latest_event_timestamp | stateless.
	Kind string `yaml:"kind" json:"kind"`
	// Lookback, when set, is a duration Value subtracted from the
	// chosen reference (e.g. "advance cursor to now() − 5m so late
	// events still land").
	Lookback *Value `yaml:"lookback,omitempty" json:"lookback,omitempty"`
	// EventTime is required when Kind is latest_event_timestamp: it
	// names the body path to the per-event timestamp field inside the
	// events list located by response.events_at. The validator rejects
	// EventTime for the other kinds (use_now / stateless) — it has no
	// meaning there.
	EventTime *EventTime `yaml:"event_time,omitempty" json:"event_time,omitempty"`
}

// ---- Union helpers ----
//
// Every union type (Auth, Body, Pagination, Progress, Value, Predicate)
// exposes:
//
//   - Variant() (name string, payload any) — the active variant. Returns
//     ("", nil) when zero or multiple variants are set; targets that emit
//     code from a parsed Doc should call schema.Validate first.
//   - VariantNames() []string — the names of every set variant (in the
//     declaration order of the union's fields). Used by the validator's
//     exactly-one-variant check.

// authVariants returns the (name, payload) pairs for every Auth variant that
// is currently set, in declaration order.
func authVariants(a Auth) (names []string, payloads []any) {
	add := func(name string, payload any, set bool) {
		if set {
			names = append(names, name)
			payloads = append(payloads, payload)
		}
	}
	add("none", a.None, a.None != nil)
	add("bearer", a.Bearer, a.Bearer != nil)
	add("basic", a.Basic, a.Basic != nil)
	add("api_key", a.APIKey, a.APIKey != nil)
	add("custom", a.Custom, a.Custom != nil)
	add("oauth2", a.OAuth2, a.OAuth2 != nil)
	add("multi_mode", a.MultiMode, a.MultiMode != nil)
	return names, payloads
}

// Variant returns the active Auth variant name and payload. Returns ("", nil)
// when zero or more than one variant is set.
func (a Auth) Variant() (string, any) {
	names, payloads := authVariants(a)
	if len(names) == 1 {
		return names[0], payloads[0]
	}
	return "", nil
}

// VariantNames returns the names of every set Auth variant.
func (a Auth) VariantNames() []string {
	names, _ := authVariants(a)
	return names
}

// bodyVariants returns the (name, payload) pairs for every Body variant set.
func bodyVariants(b Body) (names []string, payloads []any) {
	if b.JSON != nil {
		names, payloads = append(names, "json"), append(payloads, b.JSON)
	}
	if b.Form != nil {
		names, payloads = append(names, "form"), append(payloads, b.Form)
	}
	if b.Raw != nil {
		names, payloads = append(names, "raw"), append(payloads, b.Raw)
	}
	return names, payloads
}

// Variant returns the active Body variant name and payload.
func (b Body) Variant() (string, any) {
	names, payloads := bodyVariants(b)
	if len(names) == 1 {
		return names[0], payloads[0]
	}
	return "", nil
}

// VariantNames returns the names of every set Body variant.
func (b Body) VariantNames() []string {
	names, _ := bodyVariants(b)
	return names
}

// paginationVariants returns the (name, payload) pairs for every Pagination
// variant set.
func paginationVariants(p Pagination) (names []string, payloads []any) {
	add := func(name string, payload any, set bool) {
		if set {
			names = append(names, name)
			payloads = append(payloads, payload)
		}
	}
	add("none", p.None, p.None != nil)
	add("cursor_token", p.CursorToken, p.CursorToken != nil)
	add("page_number", p.PageNumber, p.PageNumber != nil)
	add("offset", p.Offset, p.Offset != nil)
	add("link_header", p.LinkHeader, p.LinkHeader != nil)
	add("next_url_in_body", p.NextURLInBody, p.NextURLInBody != nil)
	add("scroll_id", p.ScrollID, p.ScrollID != nil)
	add("graphql_relay", p.GraphQLRelay, p.GraphQLRelay != nil)
	return names, payloads
}

// Variant returns the active Pagination variant name and payload.
func (p Pagination) Variant() (string, any) {
	names, payloads := paginationVariants(p)
	if len(names) == 1 {
		return names[0], payloads[0]
	}
	return "", nil
}

// VariantNames returns the names of every set Pagination variant.
func (p Pagination) VariantNames() []string {
	names, _ := paginationVariants(p)
	return names
}

// progressVariants returns the (name, payload) pairs for every Progress
// variant set.
func progressVariants(p Progress) (names []string, payloads []any) {
	add := func(name string, payload any, set bool) {
		if set {
			names = append(names, name)
			payloads = append(payloads, payload)
		}
	}
	add("stateless", p.Stateless, p.Stateless != nil)
	add("latest_event_timestamp", p.LatestEventTimestamp, p.LatestEventTimestamp != nil)
	add("max_event_field", p.MaxEventField, p.MaxEventField != nil)
	add("use_now", p.UseNow, p.UseNow != nil)
	add("time_window", p.TimeWindow, p.TimeWindow != nil)
	add("async_job", p.AsyncJob, p.AsyncJob != nil)
	return names, payloads
}

// Variant returns the active Progress variant name and payload.
func (p Progress) Variant() (string, any) {
	names, payloads := progressVariants(p)
	if len(names) == 1 {
		return names[0], payloads[0]
	}
	return "", nil
}

// VariantNames returns the names of every set Progress variant.
func (p Progress) VariantNames() []string {
	names, _ := progressVariants(p)
	return names
}

// ---- Error ----

// ErrorBlock configures how HTTP errors are surfaced.
type ErrorBlock struct {
	// Mode is the document-level dispatcher: "standard" (default),
	// "warn", or "fail". See the client.Runner package docs for the
	// precise dispatch order.
	Mode string `yaml:"mode" json:"mode"`
	// IncludeBody, when true, includes the failing response body in the
	// surfaced error/log line. Off by default to avoid leaking secrets
	// from token-exchange responses.
	IncludeBody bool `yaml:"include_body,omitempty" json:"include_body,omitempty"`
}

// ---- CursorUpdateDirective codec ----
//
// The structured directive is map-only. Scalar shorthand (cursor_update: use_now)
// is rejected at parse time with a hint that points to the {kind: ...} form.

// cursorUpdateRaw mirrors CursorUpdateDirective but exists as a separate type
// so the custom unmarshaler can defer to the default decode without recursion.
type cursorUpdateRaw struct {
	Kind      string     `yaml:"kind"                  json:"kind"`
	Lookback  *Value     `yaml:"lookback,omitempty"    json:"lookback,omitempty"`
	EventTime *EventTime `yaml:"event_time,omitempty"  json:"event_time,omitempty"`
}

// UnmarshalYAML implements yaml.Unmarshaler. Only MappingNode is accepted.
func (c *CursorUpdateDirective) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("schema.CursorUpdateDirective at line %d: cursor_update requires the map form {kind: ..., lookback: ...}; scalar shorthand is not accepted", node.Line)
	}
	var raw cursorUpdateRaw
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("schema.CursorUpdateDirective at line %d: %w", node.Line, err)
	}
	c.Kind = raw.Kind
	c.Lookback = raw.Lookback
	c.EventTime = raw.EventTime
	return nil
}

// UnmarshalJSON implements json.Unmarshaler. Only JSON objects are accepted.
func (c *CursorUpdateDirective) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("schema.CursorUpdateDirective: cursor_update requires the map form {kind: ..., lookback: ...}; scalar shorthand is not accepted")
	}
	var raw cursorUpdateRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("schema.CursorUpdateDirective: %w", err)
	}
	c.Kind = raw.Kind
	c.Lookback = raw.Lookback
	c.EventTime = raw.EventTime
	return nil
}
