// SPDX-License-Identifier: Apache-2.0

package schema

// Doc is the top-level IR document.
type Doc struct {
	// IRVersion is the spec wire-format version. Must equal IRVersion.
	IRVersion string `yaml:"ir_version" json:"ir_version"`
	// State is the flat map of typed field declarations. Optional —
	// authors with no state at all may omit the block.
	State map[string]FieldDecl `yaml:"state,omitempty" json:"state,omitempty"`
	// Auth is the discriminated-union auth block. Exactly one variant.
	Auth Auth `yaml:"auth" json:"auth"`
	// Requests is the ordered list of HTTP requests run on every iteration.
	Requests []Request `yaml:"requests" json:"requests"`
	// Response describes how to decode the producer step's body and where
	// the events list lives.
	Response Response `yaml:"response" json:"response"`
	// Pagination is the discriminated-union pagination block. Exactly one
	// variant.
	Pagination Pagination `yaml:"pagination" json:"pagination"`
	// Progress is the flat list of state writes evaluated after each
	// accepted page-response. An empty list (or omitted block) means
	// no progress tracking.
	Progress Progress `yaml:"progress,omitempty" json:"progress,omitempty"`
	// Error configures how non-success HTTP responses are surfaced.
	// Optional.
	Error *ErrorBlock `yaml:"error,omitempty" json:"error,omitempty"`
}

// ---- State ----

// FieldDecl is the declaration for a single state field. A field's lifetime
// (operator-config / per-drain scratch / persistent) is inferred from its
// write sites; it is NOT carried on the declaration.
type FieldDecl struct {
	// Type is the field's declared shape. One of:
	// string | int | bool | secret | duration | timestamp | url | enum.
	Type string `yaml:"type" json:"type"`
	// Default is the Value used when no operator input is supplied. Any
	// Value form, not just a literal — composing {now: true},
	// {subtract: [...]}, {ref: ...}, etc. is permitted.
	Default *Value `yaml:"default,omitempty" json:"default,omitempty"`
	// Values is the allowed enumeration. Only valid when Type == "enum".
	Values []string `yaml:"values,omitempty" json:"values,omitempty"`
	// Format is the wire-format hint for Type == "timestamp" or
	// Type == "duration". One of the closed-set verbs (rfc3339,
	// rfc3339nano, unix_seconds, unix_millis) or a Go layout string.
	// Ignored on other types.
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
}

// ---- Auth ----

// Auth is the discriminated-union auth block. Exactly one variant key must
// be present.
type Auth struct {
	// None selects the no-auth variant.
	None *struct{} `yaml:"none,omitempty" json:"none,omitempty"`
	// Bearer selects the bearer-token variant.
	Bearer *BearerAuth `yaml:"bearer,omitempty" json:"bearer,omitempty"`
	// Basic selects the HTTP basic-auth variant.
	Basic *BasicAuth `yaml:"basic,omitempty" json:"basic,omitempty"`
	// APIKey selects the named-header (or named-query) API-key variant.
	APIKey *APIKeyAuth `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	// Custom selects the single-custom-header variant.
	Custom *CustomAuth `yaml:"custom,omitempty" json:"custom,omitempty"`
	// OAuth2 selects the OAuth2 variant
	// (client_credentials / password_grant).
	OAuth2 *OAuth2Auth `yaml:"oauth2,omitempty" json:"oauth2,omitempty"`
	// SigV4 selects the AWS Signature Version 4 request-signing variant.
	SigV4 *SigV4Auth `yaml:"sigv4,omitempty" json:"sigv4,omitempty"`
	// MultiMode dispatches between auth strategies at runtime via
	// predicates.
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

// APIKeyAuth sends the key in a named header (or query parameter when
// InQuery is true).
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
	// Scopes is the optional space-separated OAuth2 scope list (RFC 6749
	// §3.3).
	Scopes []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
	// Audience is the optional RFC 8693 audience claim sent in the token
	// request.
	Audience string `yaml:"audience,omitempty" json:"audience,omitempty"`
	// Cache, when set, writes the captured access token into a
	// cache.<name> slot.
	Cache *Cache `yaml:"cache,omitempty" json:"cache,omitempty"`
}

// PasswordGrant is the OAuth2 password grant (RFC 6749 §4.3): exchange a
// username/password pair for a short-lived access token.
type PasswordGrant struct {
	// TokenURL is the OAuth2 token endpoint.
	TokenURL Value `yaml:"token_url" json:"token_url"`
	// Username is the resource-owner user name.
	Username Value `yaml:"username" json:"username"`
	// Password is the resource-owner password.
	Password Value `yaml:"password" json:"password"`
	// ClientID is optional because some servers authenticate the client
	// via Basic auth on the token endpoint.
	ClientID *Value `yaml:"client_id,omitempty" json:"client_id,omitempty"`
	// Scopes is the optional space-separated OAuth2 scope list.
	Scopes []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
	// Cache, when set, writes the captured access token into a
	// cache.<name> slot.
	Cache *Cache `yaml:"cache,omitempty" json:"cache,omitempty"`
}

// SigV4Auth signs every request with AWS Signature Version 4. Region and
// Service are required. Credentials are taken from access_key_id +
// secret_access_key (with optional session_token); when all three are
// omitted, the AWS default credential chain is used (environment, shared
// config, IMDS / container / IAM role).
type SigV4Auth struct {
	// Region is the AWS region used in the credential scope (e.g. us-east-1).
	Region Value `yaml:"region" json:"region"`
	// Service is the AWS service name used in the credential scope
	// (e.g. execute-api, es, s3).
	Service Value `yaml:"service" json:"service"`
	// AccessKeyID is the AWS access key id. Optional: omit (with
	// SecretAccessKey) to use the default credential chain.
	AccessKeyID *Value `yaml:"access_key_id,omitempty" json:"access_key_id,omitempty"`
	// SecretAccessKey is the AWS secret access key. Required when
	// AccessKeyID is set.
	SecretAccessKey *Value `yaml:"secret_access_key,omitempty" json:"secret_access_key,omitempty"`
	// SessionToken is the optional STS session token for temporary
	// credentials.
	SessionToken *Value `yaml:"session_token,omitempty" json:"session_token,omitempty"`
}

// MultiModeAuth dispatches between auth strategies at runtime based on a
// Predicate over any namespace.
type MultiModeAuth struct {
	// Branches is the ordered list of (when, auth) arms. The first arm
	// whose predicate is true wins.
	Branches []AuthBranch `yaml:"branches" json:"branches"`
	// Default is the bare Auth value applied when no branch matches.
	Default Auth `yaml:"default" json:"default"`
}

// AuthBranch is one arm of a multi_mode auth dispatch.
type AuthBranch struct {
	// When is the predicate that selects this branch.
	When Predicate `yaml:"when" json:"when"`
	// Auth is the variant applied while the predicate is true.
	Auth Auth `yaml:"auth" json:"auth"`
}

// ---- Cache ----

// Cache is the unified cache block shared by OAuth2 grants
// (auth.oauth2.<grant>.cache) and step-level caches (requests[].cache). The
// captured value lives in cache.<name>; the slot is process memory only and
// is never persisted.
type Cache struct {
	// To is the cache.<name> destination slot. Reads use
	// {ref: cache.<name>}.
	To Path `yaml:"to" json:"to"`
	// ExpiresAt is the Value resolving to a time.Time. Accepts a
	// {ref: ..., default: ...} fallback for APIs that return no explicit
	// expiry.
	ExpiresAt Value `yaml:"expires_at" json:"expires_at"`
	// Buffer is a Go-style duration. Re-fetch when the remaining
	// lifetime falls below this.
	Buffer string `yaml:"buffer" json:"buffer"`
}

// ---- Requests ----

// Request describes a single HTTP request in the chain.
type Request struct {
	// ID is the optional step label. Required for steps.<id>.body.<path>
	// references and for fan_out.
	ID string `yaml:"id,omitempty" json:"id,omitempty"`
	// Method is the HTTP verb (GET, POST, ...).
	Method string `yaml:"method" json:"method"`
	// URL is the absolute request URL Value.
	URL Value `yaml:"url" json:"url"`
	// Query is the URL query map; each value resolves to a string.
	Query map[string]Value `yaml:"query,omitempty" json:"query,omitempty"`
	// Headers is the wire header map; each value resolves to a string.
	Headers map[string]Value `yaml:"headers,omitempty" json:"headers,omitempty"`
	// Body is the request body. Discriminated-union (json/form/raw).
	Body *Body `yaml:"body,omitempty" json:"body,omitempty"`
	// Extract pulls named values out of the response body or headers.
	Extract []ExtractVar `yaml:"extract,omitempty" json:"extract,omitempty"`
	// FanOut lifts the step into a per-item iteration over a list Value.
	// Mutually exclusive with Cache.
	FanOut *FanOut `yaml:"fan_out,omitempty" json:"fan_out,omitempty"`
	// ExpectStatus is the set of status codes the runner treats as
	// successful. Defaults to {200} when empty.
	ExpectStatus []int `yaml:"expect_status,omitempty" json:"expect_status,omitempty"`
	// If is the predicate that gates execution of the step.
	If *Predicate `yaml:"if,omitempty" json:"if,omitempty"`
	// TerminateWhen is the request-level loop primitive: while the
	// predicate is false, the same request is re-fired; when true the
	// runner advances to the next request.
	TerminateWhen *Predicate `yaml:"terminate_when,omitempty" json:"terminate_when,omitempty"`
	// OnStatus maps a specific HTTP status code to a per-step dispatcher
	// verb (skip / fail / empty_events / invalidate_cache).
	OnStatus map[int]string `yaml:"on_status,omitempty" json:"on_status,omitempty"`
	// ProducesEvents marks this step as the events producer. At most
	// one in the chain; defaults to the last request.
	ProducesEvents bool `yaml:"produces_events,omitempty" json:"produces_events,omitempty"`
	// Cache wraps the step in a generic step-level expiry cache.
	// Mutually exclusive with FanOut.
	Cache *Cache `yaml:"cache,omitempty" json:"cache,omitempty"`
}

// ExtractVar names a value to pull from a response into the state or
// extract namespace.
type ExtractVar struct {
	// To is the destination slot: either state.<name> (persistent —
	// must be declared under state:) or extract.<name> (per-iteration,
	// no declaration needed). The namespace prefix decides persistence.
	To Path `yaml:"to" json:"to"`
	// From is the namespace-rooted Path locating the value to capture.
	// One of response.body.<path>, response.header.<name>,
	// steps.<id>.body.<path>, steps.<id>.header.<name>.
	From Path `yaml:"from" json:"from"`
	// Coerce, when set, applies a format verb to the extracted value
	// before storing it.
	Coerce string `yaml:"coerce,omitempty" json:"coerce,omitempty"`
	// Regex is an optional regular expression applied to the resolved
	// string before writing.
	Regex string `yaml:"regex,omitempty" json:"regex,omitempty"`
}

// FanOut lifts a step into a per-item iteration over a list.
type FanOut struct {
	// Over resolves to the list to iterate; must be list-shaped at
	// evaluation time.
	Over Value `yaml:"over" json:"over"`
	// As is the author-chosen per-item binding name; refs inside the
	// step use {ref: <as>.<path>}.
	As string `yaml:"as" json:"as"`
	// Merge controls how the per-iteration outputs combine: "flatten"
	// (default) or "wrap".
	Merge string `yaml:"merge,omitempty" json:"merge,omitempty"`
}

// ---- Body ----

// Body is the discriminated-union request body. Exactly one variant key
// must be present.
type Body struct {
	// JSON selects the application/json variant.
	JSON map[string]Value `yaml:"json,omitempty" json:"json,omitempty"`
	// Form selects the application/x-www-form-urlencoded variant.
	Form map[string]Value `yaml:"form,omitempty" json:"form,omitempty"`
	// Raw selects the literal-body variant.
	Raw *Value `yaml:"raw,omitempty" json:"raw,omitempty"`
}

// ---- Response ----

// Response describes how to decode the producer step's body and locate the
// events list.
type Response struct {
	// Decode is the body decoder verb: "json" or "ndjson".
	Decode string `yaml:"decode" json:"decode"`
	// EventsAt is the namespace-rooted Path locating the events list,
	// rooted at response.body.<path> or steps.<id>.body.<path>. The zero
	// (empty) Path means "the body root IS the events list".
	EventsAt Path `yaml:"events_at" json:"events_at"`
}

// ---- Pagination ----

// Pagination is the discriminated-union pagination block. Exactly one
// variant key must be present.
type Pagination struct {
	// None disables pagination — exactly one page is fetched per drain.
	None *struct{} `yaml:"none,omitempty" json:"none,omitempty"`
	// CursorToken advances via a next-cursor token returned in the body
	// or a header.
	CursorToken *CursorTokenPagination `yaml:"cursor_token,omitempty" json:"cursor_token,omitempty"`
	// NextURL advances via a fully-formed next-page URL returned in the
	// body or a header (e.g. Link: <url>; rel="next").
	NextURL *NextURLPagination `yaml:"next_url,omitempty" json:"next_url,omitempty"`
	// Counter advances via a client-incremented counter (page number or
	// offset).
	Counter *CounterPagination `yaml:"counter,omitempty" json:"counter,omitempty"`
	// Custom exposes the primitive form: an author-supplied list of
	// advance writes plus an author-supplied terminate_when predicate.
	Custom *CustomPagination `yaml:"custom,omitempty" json:"custom,omitempty"`
}

// CursorTokenPagination advances via a next-cursor token returned by the
// server.
type CursorTokenPagination struct {
	// From is the Path to the next-cursor field, rooted at
	// response.body.<path>, response.header.<name>, or
	// steps.<id>.body.<path>.
	From Path `yaml:"from" json:"from"`
	// To is the state.<name> destination (per-drain).
	To Path `yaml:"to" json:"to"`
	// TerminateWhen overrides the default termination predicate
	// ({not: {present: <from>}}).
	TerminateWhen *Predicate `yaml:"terminate_when,omitempty" json:"terminate_when,omitempty"`
}

// NextURLPagination advances via a fully-formed next-page URL.
type NextURLPagination struct {
	// From is the Path to the next-URL field, rooted at
	// response.body.<path>, response.header.<name>, or
	// steps.<id>.body.<path>.
	From Path `yaml:"from" json:"from"`
	// To is the state.<name> destination (per-drain, typed url).
	To Path `yaml:"to" json:"to"`
	// Regex is the optional regular expression applied to the resolved
	// string before writing — used for parsing
	// `Link: <url>; rel="next"` and similar.
	Regex string `yaml:"regex,omitempty" json:"regex,omitempty"`
	// Capture is the optional capture-group index for Regex (1-based).
	Capture int `yaml:"capture,omitempty" json:"capture,omitempty"`
	// TerminateWhen overrides the default termination predicate
	// ({not: {present: <from>}}).
	TerminateWhen *Predicate `yaml:"terminate_when,omitempty" json:"terminate_when,omitempty"`
}

// CounterPagination advances via a client-incremented counter.
type CounterPagination struct {
	// To is the state.<name> destination (per-drain, typed int).
	To Path `yaml:"to" json:"to"`
	// Start is the optional starting Value. Defaults to 1 (page number);
	// use 0 for offset-style pagination.
	Start *Value `yaml:"start,omitempty" json:"start,omitempty"`
	// Step is the optional increment per accepted page. Defaults to 1.
	// For offset-style pagination, set to {ref: state.page_size}.
	Step *Value `yaml:"step,omitempty" json:"step,omitempty"`
	// TerminateWhen overrides the default termination predicate
	// (short-page detection: events.count < step).
	TerminateWhen *Predicate `yaml:"terminate_when,omitempty" json:"terminate_when,omitempty"`
}

// CustomPagination is the author-controlled primitive form for APIs that
// don't fit the named variants.
type CustomPagination struct {
	// Advance is the ordered list of writes that run when
	// TerminateWhen evaluates false.
	Advance []AdvanceWrite `yaml:"advance" json:"advance"`
	// TerminateWhen is required for custom pagination — it states the
	// termination condition explicitly.
	TerminateWhen Predicate `yaml:"terminate_when" json:"terminate_when"`
}

// AdvanceWrite is one entry in CustomPagination.Advance — a {to, from,
// regex?, coerce?} write evaluated and applied per-page.
type AdvanceWrite struct {
	// To is the state.<name> destination (per-drain).
	To Path `yaml:"to" json:"to"`
	// From is the Value to resolve and write.
	From Value `yaml:"from" json:"from"`
	// Coerce, when set, applies a format verb to the resolved value
	// before writing.
	Coerce string `yaml:"coerce,omitempty" json:"coerce,omitempty"`
	// Regex is the optional regular expression applied to the resolved
	// string before writing.
	Regex string `yaml:"regex,omitempty" json:"regex,omitempty"`
}

// ---- Progress ----

// Progress is the flat list of state writes evaluated after each accepted
// page-response. An empty (or omitted) Progress means no progress
// tracking.
type Progress []ProgressWrite

// ProgressWrite is one entry in Progress — a {to, from, coerce?, regex?}
// write evaluated per accepted page-response.
type ProgressWrite struct {
	// To is the state.<name> destination (persistent — must be declared
	// under state:).
	To Path `yaml:"to" json:"to"`
	// From is the Value to resolve and write. Has access to every
	// namespace the request scope has (state, cache, events,
	// response.body/header, steps.<id>.body/header).
	From Value `yaml:"from" json:"from"`
	// Coerce, when set, applies a format verb to the resolved value
	// before writing.
	Coerce string `yaml:"coerce,omitempty" json:"coerce,omitempty"`
	// Regex is the optional regular expression applied to the resolved
	// string before writing.
	Regex string `yaml:"regex,omitempty" json:"regex,omitempty"`
}

// ---- Error ----

// ErrorBlock configures how non-success HTTP responses (and network /
// decode failures) are surfaced.
type ErrorBlock struct {
	// Mode is the document-level dispatcher: "standard" (default),
	// "warn", or "fail".
	Mode string `yaml:"mode" json:"mode"`
	// IncludeBody, when true, includes the response body in the
	// surfaced error message. Off by default.
	IncludeBody bool `yaml:"include_body,omitempty" json:"include_body,omitempty"`
}

// ---- Union helpers ----
//
// Each discriminated-union type exposes a Variant() (name, payload) helper
// and a VariantNames() slice. The validator's exactly-one-variant check
// reads VariantNames; downstream slices read Variant() to dispatch on the
// active arm.

// authVariants returns the (name, payload) pairs for every Auth variant
// that is currently set, in declaration order.
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
	add("sigv4", a.SigV4, a.SigV4 != nil)
	add("multi_mode", a.MultiMode, a.MultiMode != nil)
	return names, payloads
}

// Variant returns the active Auth variant name and payload. Returns
// ("", nil) when zero or more than one variant is set.
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

// oauth2Variants returns the (name, payload) pairs for every OAuth2 grant
// that is currently set.
func oauth2Variants(o OAuth2Auth) (names []string, payloads []any) {
	if o.ClientCredentials != nil {
		names, payloads = append(names, "client_credentials"), append(payloads, o.ClientCredentials)
	}
	if o.PasswordGrant != nil {
		names, payloads = append(names, "password_grant"), append(payloads, o.PasswordGrant)
	}
	return names, payloads
}

// Variant returns the active OAuth2 grant name and payload. Returns
// ("", nil) when zero or more than one grant is set.
func (o OAuth2Auth) Variant() (string, any) {
	names, payloads := oauth2Variants(o)
	if len(names) == 1 {
		return names[0], payloads[0]
	}
	return "", nil
}

// VariantNames returns the names of every set OAuth2 grant.
func (o OAuth2Auth) VariantNames() []string {
	names, _ := oauth2Variants(o)
	return names
}

// bodyVariants returns the (name, payload) pairs for every Body variant
// set.
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

// paginationVariants returns the (name, payload) pairs for every
// Pagination variant set.
func paginationVariants(p Pagination) (names []string, payloads []any) {
	add := func(name string, payload any, set bool) {
		if set {
			names = append(names, name)
			payloads = append(payloads, payload)
		}
	}
	add("none", p.None, p.None != nil)
	add("cursor_token", p.CursorToken, p.CursorToken != nil)
	add("next_url", p.NextURL, p.NextURL != nil)
	add("counter", p.Counter, p.Counter != nil)
	add("custom", p.Custom, p.Custom != nil)
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
