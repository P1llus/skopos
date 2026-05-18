// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"
	"math"
	"time"
)

// Diagnostic is a single structural-validation finding.
type Diagnostic struct {
	// Path is a dotted IR path with array indices, e.g.
	// "requests[0].extract[1].path". Empty for document-wide findings.
	Path string `json:"path"`
	// Message is the human-readable error or warning text.
	Message string `json:"message"`
	// Hint is an optional follow-up sentence pointing at the fix.
	Hint string `json:"hint,omitempty"`
	// Severity is "error" or "warning".
	Severity string `json:"severity"`
	// Line is an optional source-location annotation.
	// schema.Validate never sets it; callers that wrap Validate around
	// raw YAML/JSON bytes (the CLI, the HTTP API) may set it from YAML
	// parser error messages for richer output.
	Line int `json:"line,omitempty"`
	// Column is the source-column counterpart to Line, with the same
	// "set by callers, not Validate" contract.
	Column int `json:"column,omitempty"`
}

// Validate walks d and returns structural diagnostics.
//
// An empty result means the document is structurally valid. Capability checks
// (e.g. "target X does not support graphql_relay") are NOT performed here;
// they live in the output target's generator.
func Validate(d *Doc) []Diagnostic {
	v := &validator{}
	if d == nil {
		v.errorf("", "nil document")
		return v.diags
	}
	v.run(d)
	return v.diags
}

// ---- validator internals ----

type validator struct {
	diags []Diagnostic
}

func (v *validator) errorf(path, format string, args ...any) {
	v.diags = append(v.diags, Diagnostic{
		Path:     path,
		Message:  fmt.Sprintf(format, args...),
		Severity: "error",
	})
}

// ns is the namespace context used when validating references inside a
// particular scope.
type ns struct {
	state         map[string]struct{} // declared state field names
	cursor        map[string]struct{} // inferred cursor field names
	extract       map[string]struct{} // available extract.<name> bindings
	stepBodies    map[string]struct{} // available steps.<id> bindings
	itemNamespace string              // fan_out.as value active in this scope
	oauthStoreIn  string              // auto-registered oauth2 cache state key
}

func (n *ns) hasStateField(name string) bool {
	_, ok := n.state[name]
	return ok
}

func (n *ns) hasCursorField(name string) bool {
	_, ok := n.cursor[name]
	return ok
}

func (n *ns) hasExtract(name string) bool {
	_, ok := n.extract[name]
	return ok
}

func (n *ns) hasStepBody(id string) bool {
	_, ok := n.stepBodies[id]
	return ok
}

// ---- top-level run ----

func (v *validator) run(d *Doc) {
	if d.IRVersion != IRVersion {
		v.errorf("ir_version", "expected %q, got %q", IRVersion, d.IRVersion)
	}

	// Build namespace context. Cursor + step-id sets and pagination strategy
	// are computed up front so that every reference check downstream
	// (defaults, auth, requests, response, async_job) sees a fully-populated
	// namespace.
	namespace := &ns{
		state:      make(map[string]struct{}),
		cursor:     cursorSchema(d),
		extract:    make(map[string]struct{}),
		stepBodies: make(map[string]struct{}),
	}

	// Snapshot author-declared state.fields keys before preregisterStateAndCursor
	// runs — the pre-pass materialises auto-registered runtime slots into
	// d.State.Fields, and the downstream conflict checks ("store_in conflicts
	// with a declared state.fields key") must see only the author-declared set.
	declaredStateFields := map[string]struct{}{}
	if d.State != nil {
		for n := range d.State.Fields {
			declaredStateFields[n] = struct{}{}
		}
	}

	// Pre-register every namespace member that the IR auto-registers from a
	// declaration: state.fields, OAuth2 cache.store_in, request cache.store_in,
	// extract[].target=cursor, and step IDs. This guarantees that any Value
	// reference (in defaults, auth, requests, response, ...) sees the full
	// namespace regardless of where the declaration appears in the document.
	preregisterStateAndCursor(d, namespace)

	v.checkState(d.State, namespace)
	v.checkDefaults("defaults", d.Defaults, namespace)
	v.checkAuth("auth", d.Auth, namespace, declaredStateFields)
	v.checkRequests(d, namespace, declaredStateFields)
	v.checkResponse("response", d.Response, namespace)
	v.checkPagination("pagination", d.Pagination, namespace)
	v.checkProgress("progress", d.Progress, namespace)
	if d.Error != nil {
		v.checkError("error", *d.Error)
	}
	// Cross-block rule: async_job phase iterations have no producer body
	// during submit / poll, so a non-none pagination plan would call
	// pagination.advance against nil and silently clear cursor state every
	// phase tick. Reject the combination at validate time.
	if d.Progress.AsyncJob != nil {
		if strat := paginationStrategy(d.Pagination); strat != "" && strat != "none" {
			v.errorf("pagination", "pagination.%s cannot be combined with progress.async_job (no template motivates the combo and the runner cannot advance pagination state across phase iterations); use pagination.none and let the fetch role return all events", strat)
		}
	}
}

// preregisterStateAndCursor walks d once and seeds the namespace with every
// auto-registered binding. Each call site downstream may still re-register
// (idempotent map writes); the up-front pass guarantees visibility regardless
// of declaration order.
//
// Auto-registered runtime state slots (auth.oauth2.<grant>.cache.store_in and
// requests[].cache.store_in) are also materialised in d.State.Fields as
// FieldDecl{Type: "string", Mutability: "runtime"} when no explicit
// declaration exists. The paired expiry timestamp slot
// (<store_in>_expires_at) auto-registers the same way: slice 7 moved the
// expiry slot from cursor.__*_expires_at into state.<store_in>_expires_at,
// so authors that walk d.State.Fields see both the token slot and its
// expiry slot as runtime-mutable string fields.
func preregisterStateAndCursor(d *Doc, namespace *ns) {
	if d.State != nil {
		for name := range d.State.Fields {
			namespace.state[name] = struct{}{}
		}
	}
	autoRegister := func(name string) {
		if name == "" {
			return
		}
		namespace.state[name] = struct{}{}
		if d.State == nil {
			d.State = &State{}
		}
		if d.State.Fields == nil {
			d.State.Fields = map[string]FieldDecl{}
		}
		if _, declared := d.State.Fields[name]; !declared {
			d.State.Fields[name] = FieldDecl{Type: "string", Mutability: "runtime"}
		}
	}
	autoRegisterCache := func(storeIn string) {
		if storeIn == "" {
			return
		}
		autoRegister(storeIn)
		autoRegister(storeIn + "_expires_at")
	}
	if d.Auth.OAuth2 != nil {
		if cc := d.Auth.OAuth2.ClientCredentials; cc != nil && cc.Cache != nil && cc.Cache.StoreIn != "" {
			autoRegisterCache(cc.Cache.StoreIn)
			namespace.oauthStoreIn = cc.Cache.StoreIn
		}
		if pg := d.Auth.OAuth2.PasswordGrant; pg != nil && pg.Cache != nil && pg.Cache.StoreIn != "" {
			autoRegisterCache(pg.Cache.StoreIn)
			namespace.oauthStoreIn = pg.Cache.StoreIn
		}
	}
	for _, req := range d.Requests {
		if req.ID != "" {
			namespace.stepBodies[req.ID] = struct{}{}
		}
		if req.Cache != nil && req.Cache.StoreIn != "" {
			autoRegisterCache(req.Cache.StoreIn)
		}
		for _, ex := range req.Extract {
			if ex.Target == "cursor" && ex.Name != "" {
				namespace.cursor[ex.Name] = struct{}{}
			}
		}
	}
}

// implicitAsyncJobProducerStep returns the step id of the last-declared
// async_job role (fetch > poll > submit), or "" when async_job is not active.
// When async_job is set, the events-bearing step is this role unless an
// explicit produces_events: true overrides.
func implicitAsyncJobProducerStep(d *Doc) string {
	aj := d.Progress.AsyncJob
	if aj == nil {
		return ""
	}
	if aj.Fetch != nil {
		return aj.Fetch.Step
	}
	if aj.Poll != nil {
		return aj.Poll.Step
	}
	if aj.Submit != nil {
		return aj.Submit.Step
	}
	return ""
}

// paginationStrategy returns the active pagination strategy name, or "" when
// no variant is set.
func paginationStrategy(p Pagination) string {
	switch {
	case p.None != nil:
		return "none"
	case p.CursorToken != nil:
		return "cursor_token"
	case p.PageNumber != nil:
		return "page_number"
	case p.Offset != nil:
		return "offset"
	case p.LinkHeader != nil:
		return "link_header"
	case p.NextURLInBody != nil:
		return "next_url_in_body"
	case p.ScrollID != nil:
		return "scroll_id"
	case p.GraphQLRelay != nil:
		return "graphql_relay"
	}
	return ""
}

// ---- state ----

func (v *validator) checkState(s *State, namespace *ns) {
	if s == nil {
		return
	}
	validTypes := map[string]bool{
		"string": true, "int": true, "bool": true,
		"secret": true, "duration": true, "url": true, "enum": true,
	}
	for name, fd := range s.Fields {
		path := fmt.Sprintf("state.fields.%s", name)
		if !validTypes[fd.Type] {
			v.errorf(path+".type", "unknown field type %q; want string|int|bool|secret|duration|url|enum", fd.Type)
		}
		if fd.Type == "enum" && len(fd.Values) == 0 {
			v.errorf(path+".values", "enum field must declare at least one value")
		}
		if fd.Type != "enum" && len(fd.Values) > 0 {
			v.errorf(path+".values", "values is only valid when type: enum, got type %q", fd.Type)
		}
		if fd.Default != nil {
			v.checkFieldDefault(path+".default", fd)
		}
		switch fd.Mutability {
		case "", "config", "runtime":
		default:
			v.errorf(path+".mutability", "unknown mutability %q; want config|runtime", fd.Mutability)
		}
		namespace.state[name] = struct{}{}
	}
}

// checkFieldDefault verifies that fd.Default is a literal whose YAML-decoded
// shape matches the declared type. The Default field is typed as interface{}
// (yaml.v3 / encoding/json fill it with native Go scalars).
func (v *validator) checkFieldDefault(path string, fd FieldDecl) {
	switch fd.Type {
	case "string", "secret", "url":
		if _, ok := fd.Default.(string); !ok {
			v.errorf(path, "default for type %q must be a string, got %T", fd.Type, fd.Default)
		}
	case "int":
		switch n := fd.Default.(type) {
		case int, int32, int64:
			// ok
		case float64:
			// encoding/json decodes every JSON number into float64; accept whole
			// numbers that round-trip without loss.
			if math.Trunc(n) != n || n < math.MinInt64 || n > math.MaxInt64 {
				v.errorf(path, "default for type \"int\" must be an integer literal, got non-integer %v", n)
			}
		default:
			v.errorf(path, "default for type \"int\" must be an integer literal, got %T", fd.Default)
		}
	case "bool":
		if _, ok := fd.Default.(bool); !ok {
			v.errorf(path, "default for type \"bool\" must be a bool literal, got %T", fd.Default)
		}
	case "duration":
		s, ok := fd.Default.(string)
		if !ok {
			v.errorf(path, "default for type \"duration\" must be a Go-duration string, got %T", fd.Default)
			return
		}
		if _, err := time.ParseDuration(s); err != nil {
			v.errorf(path, "default %q is not a valid Go duration: %v", s, err)
		}
	case "enum":
		s, ok := fd.Default.(string)
		if !ok {
			v.errorf(path, "default for type \"enum\" must be a string from values, got %T", fd.Default)
			return
		}
		for _, allowed := range fd.Values {
			if allowed == s {
				return
			}
		}
		v.errorf(path, "default %q is not one of the declared enum values %v", s, fd.Values)
	}
}

// ---- defaults ----

func (v *validator) checkDefaults(path string, d *Defaults, namespace *ns) {
	if d == nil {
		return
	}
	v.checkValue(path+".base_url", d.BaseURL, namespace, false)
}

// ---- auth ----

func (v *validator) checkAuth(path string, a Auth, namespace *ns, declaredStateFields map[string]struct{}) {
	switch len(a.VariantNames()) {
	case 0:
		v.errorf(path, "auth block must have exactly one variant (none|bearer|basic|api_key|custom|oauth2|multi_mode)")
		return
	case 1:
	default:
		v.errorf(path, "auth block must have exactly one variant; found %v", a.VariantNames())
		return
	}

	switch {
	case a.Bearer != nil:
		v.checkValue(path+".bearer.token", a.Bearer.Token, namespace, false)
	case a.Basic != nil:
		v.checkValue(path+".basic.username", a.Basic.Username, namespace, false)
		v.checkValue(path+".basic.password", a.Basic.Password, namespace, false)
	case a.APIKey != nil:
		if a.APIKey.Header == "" {
			v.errorf(path+".api_key.header", "header is required for api_key auth")
		}
		v.checkValue(path+".api_key.value", a.APIKey.Value, namespace, false)
	case a.Custom != nil:
		if a.Custom.Header == "" {
			v.errorf(path+".custom.header", "header is required for custom auth")
		}
		v.checkValue(path+".custom.value", a.Custom.Value, namespace, false)
	case a.OAuth2 != nil:
		v.checkOAuth2(path+".oauth2", a.OAuth2, namespace, declaredStateFields)
	case a.MultiMode != nil:
		v.checkMultiMode(path+".multi_mode", a.MultiMode, namespace, declaredStateFields)
	}
}

func (v *validator) checkOAuth2(path string, o *OAuth2Auth, namespace *ns, declaredStateFields map[string]struct{}) {
	set := 0
	if o.ClientCredentials != nil {
		set++
	}
	if o.PasswordGrant != nil {
		set++
	}
	switch set {
	case 0:
		v.errorf(path, "oauth2 block must contain exactly one grant type (client_credentials|password_grant); jwt_bearer / authorization_code / device_code are deferred — they need a portable signing or browser-redirect Value form that no template currently motivates")
		return
	case 1:
	default:
		v.errorf(path, "oauth2 block must contain exactly one grant type; multiple are set")
		return
	}
	if cc := o.ClientCredentials; cc != nil {
		p := path + ".client_credentials"
		v.checkValue(p+".token_url", cc.TokenURL, namespace, false)
		v.checkValue(p+".client_id", cc.ClientID, namespace, false)
		v.checkValue(p+".client_secret", cc.ClientSecret, namespace, false)
		v.checkTokenCache(p+".cache", cc.Cache, namespace, declaredStateFields)
	}
	if pg := o.PasswordGrant; pg != nil {
		p := path + ".password_grant"
		v.checkValue(p+".token_url", pg.TokenURL, namespace, false)
		v.checkValue(p+".username", pg.Username, namespace, false)
		v.checkValue(p+".password", pg.Password, namespace, false)
		if pg.ClientID != nil {
			v.checkValue(p+".client_id", *pg.ClientID, namespace, false)
		}
		v.checkTokenCache(p+".cache", pg.Cache, namespace, declaredStateFields)
	}
}

// checkTokenCache validates an OAuth2 token cache block, shared across grants.
func (v *validator) checkTokenCache(path string, c *TokenCache, namespace *ns, declaredStateFields map[string]struct{}) {
	if c == nil {
		return
	}
	if c.StoreIn == "" {
		v.errorf(path+".store_in", "store_in is required")
	} else {
		if _, conflict := declaredStateFields[c.StoreIn]; conflict {
			v.errorf(path+".store_in", "store_in %q conflicts with a declared state.fields key", c.StoreIn)
		}
		// Slice 7: the paired expiry timestamp auto-registers as
		// state.<store_in>_expires_at, so an author-declared
		// state.fields.<store_in>_expires_at collides with the slot the
		// cache block claims.
		if _, conflict := declaredStateFields[c.StoreIn+"_expires_at"]; conflict {
			v.errorf(path+".store_in",
				"store_in %q conflicts with a declared state.fields key %q (auto-registered by the cache block as the paired expiry slot)",
				c.StoreIn, c.StoreIn+"_expires_at")
		}
	}
	v.checkBodyRootedPath(path+".expiry_field", c.ExpiryField, namespace, false)
	v.checkDuration(path+".expiry_buffer", "expiry_buffer", c.ExpiryBuffer)
}

// checkDuration verifies that s is a non-empty Go-duration string. Used by
// both oauth2 client-credentials and request-level cache validation.
func (v *validator) checkDuration(path, name, s string) {
	if s == "" {
		v.errorf(path, "%s is required (Go duration, e.g. \"60s\")", name)
		return
	}
	if _, err := time.ParseDuration(s); err != nil {
		v.errorf(path, "%s %q is not a valid Go duration: %v", name, s, err)
	}
}

func (v *validator) checkMultiMode(path string, m *MultiModeAuth, namespace *ns, declaredStateFields map[string]struct{}) {
	if len(m.Branches) == 0 {
		v.errorf(path+".branches", "multi_mode requires at least one branch")
	}
	for i, b := range m.Branches {
		bp := fmt.Sprintf("%s.branches[%d]", path, i)
		v.checkPredicate(bp+".when", b.When, namespace, false)
		if b.Auth.MultiMode != nil {
			v.errorf(bp+".auth", "nested multi_mode is not allowed")
		} else {
			v.checkAuth(bp+".auth", b.Auth, namespace, declaredStateFields)
		}
	}
	if b := m.Default.Auth; len(b.VariantNames()) == 0 {
		v.errorf(path+".default.auth", "default.auth is required")
	} else if b.MultiMode != nil {
		v.errorf(path+".default.auth", "nested multi_mode is not allowed in default arm")
	} else {
		v.checkAuth(path+".default.auth", b, namespace, declaredStateFields)
	}
}

// ---- requests ----

func (v *validator) checkRequests(d *Doc, namespace *ns, declaredStateFields map[string]struct{}) {
	if len(d.Requests) == 0 {
		v.errorf("requests", "at least one request is required")
		return
	}

	seenIDs := map[string]int{}
	seenStoreIn := map[string]int{}
	producers := 0
	for i, req := range d.Requests {
		p := fmt.Sprintf("requests[%d]", i)
		if req.ID != "" {
			if prev, dup := seenIDs[req.ID]; dup {
				v.errorf(p+".id", "duplicate step id %q (also at requests[%d])", req.ID, prev)
			} else {
				seenIDs[req.ID] = i
			}
		}
		if req.ProducesEvents {
			producers++
			if req.Method == "HEAD" {
				v.errorf(p+".produces_events",
					"HEAD has no response body; produces_events: true requires GET/POST/PUT/PATCH/DELETE")
			}
		}
		if req.Cache != nil && req.Cache.StoreIn != "" {
			cp := fmt.Sprintf("%s.cache.store_in", p)
			if _, conflict := declaredStateFields[req.Cache.StoreIn]; conflict {
				v.errorf(cp, "store_in %q conflicts with a declared state.fields key", req.Cache.StoreIn)
			}
			// Slice 7: the paired expiry timestamp auto-registers as
			// state.<store_in>_expires_at; an author-declared
			// state.fields.<store_in>_expires_at collides with the slot
			// the cache block claims.
			if _, conflict := declaredStateFields[req.Cache.StoreIn+"_expires_at"]; conflict {
				v.errorf(cp,
					"store_in %q conflicts with a declared state.fields key %q (auto-registered by the cache block as the paired expiry slot)",
					req.Cache.StoreIn, req.Cache.StoreIn+"_expires_at")
			}
			if namespace.oauthStoreIn == req.Cache.StoreIn {
				v.errorf(cp, "store_in %q conflicts with auth.oauth2.<grant>.cache.store_in", req.Cache.StoreIn)
			}
			if prev, dup := seenStoreIn[req.Cache.StoreIn]; dup {
				v.errorf(cp, "store_in %q duplicates requests[%d].cache.store_in", req.Cache.StoreIn, prev)
			} else {
				seenStoreIn[req.Cache.StoreIn] = i
			}
		}
	}

	// produces_events cardinality. Default (zero markers) = the implicit
	// producer step (depends on whether async_job is active). Multiple
	// explicit markers is an authoring error.
	if producers > 1 {
		v.errorf("requests", "at most one request may set produces_events: true; got %d", producers)
	}
	// Under async_job, produces_events: true is only meaningful on one of the
	// declared role steps (submit / poll / fetch). A marker on a non-role
	// helper would be silently ignored by the runner.
	if d.Progress.AsyncJob != nil {
		roleSteps := map[string]struct{}{}
		if aj := d.Progress.AsyncJob; aj != nil {
			if aj.Submit != nil && aj.Submit.Step != "" {
				roleSteps[aj.Submit.Step] = struct{}{}
			}
			if aj.Poll != nil && aj.Poll.Step != "" {
				roleSteps[aj.Poll.Step] = struct{}{}
			}
			if aj.Fetch != nil && aj.Fetch.Step != "" {
				roleSteps[aj.Fetch.Step] = struct{}{}
			}
		}
		for i, req := range d.Requests {
			if !req.ProducesEvents {
				continue
			}
			if _, ok := roleSteps[req.ID]; !ok {
				v.errorf(fmt.Sprintf("requests[%d].produces_events", i),
					"produces_events: true under async_job is only valid on a declared role step (submit / poll / fetch); request %q is not a role step", req.ID)
			}
		}
	}
	// Implicit-producer HEAD check. The producer step depends on whether
	// async_job is active: when it is, the last-declared role (fetch > poll
	// > submit) is the producer; otherwise the last entry in requests[] is.
	if producers == 0 && len(d.Requests) > 0 {
		if id := implicitAsyncJobProducerStep(d); id != "" {
			for i, r := range d.Requests {
				if r.ID == id && r.Method == "HEAD" {
					v.errorf(fmt.Sprintf("requests[%d].method", i),
						"HEAD has no response body; the implicit producer for async_job (step %q, the last-declared role) requires GET/POST/PUT/PATCH/DELETE",
						id)
				}
			}
		} else {
			last := len(d.Requests) - 1
			if d.Requests[last].Method == "HEAD" {
				v.errorf(fmt.Sprintf("requests[%d].method", last),
					"HEAD has no response body; the implicit producer step (the last request) requires GET/POST/PUT/PATCH/DELETE")
			}
		}
	}

	// Second pass: validate each request with an incrementally growing extract
	// namespace (extracts from earlier steps are available to later steps).
	extractNS := make(map[string]struct{})
	for i, req := range d.Requests {
		p := fmt.Sprintf("requests[%d]", i)

		// Build per-step namespace: base + extracts accumulated so far.
		stepNS := *namespace
		stepNS.extract = extractNS

		// fan_out.as becomes the item namespace for this step.
		if req.FanOut != nil {
			stepNS.itemNamespace = req.FanOut.As
		} else {
			stepNS.itemNamespace = ""
		}

		v.checkRequest(p, req, &stepNS)

		// After validation, register this step's extracts for subsequent
		// steps. extract.<name> is per-iteration; cursor.<name> (target:
		// cursor) was already registered globally in the pre-pass above.
		for _, ex := range req.Extract {
			if ex.Name == "" || ex.Target == "cursor" {
				continue
			}
			extractNS[ex.Name] = struct{}{}
		}
	}
}

func (v *validator) checkRequest(path string, req Request, namespace *ns) {
	if req.Method == "" {
		v.errorf(path+".method", "method is required")
	} else {
		switch req.Method {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD":
		default:
			v.errorf(path+".method", "unknown HTTP method %q", req.Method)
		}
	}

	if req.Path != nil && req.URL != nil {
		v.errorf(path, "path and url are mutually exclusive")
	}
	if req.Path == nil && req.URL == nil {
		v.errorf(path, "either path or url is required")
	}
	if req.Path != nil {
		v.checkValue(path+".path", *req.Path, namespace, false)
	}
	if req.URL != nil {
		v.checkValue(path+".url", *req.URL, namespace, false)
	}

	for k, val := range req.Query {
		v.checkValue(fmt.Sprintf("%s.query.%s", path, k), val, namespace, false)
	}
	for k, val := range req.Headers {
		v.checkValue(fmt.Sprintf("%s.headers.%s", path, k), val, namespace, false)
	}
	if req.Body != nil {
		v.checkBody(path+".body", *req.Body, namespace)
	}
	for j, ex := range req.Extract {
		ep := fmt.Sprintf("%s.extract[%d]", path, j)
		if ex.Name == "" {
			v.errorf(ep+".name", "extract name is required")
		}
		v.checkExtractFromPath(ep+".from", ex.From, namespace)
		if ex.Coerce != "" && !validFormatVerb(ex.Coerce) {
			v.errorf(ep+".coerce", "unknown coerce verb %q; want one of the format-verb set", ex.Coerce)
		}
		switch ex.Target {
		case "", "extract", "cursor":
		default:
			v.errorf(ep+".target", "target must be 'extract' or 'cursor', got %q", ex.Target)
		}
	}
	if req.Cache != nil {
		v.checkRequestCache(path+".cache", *req.Cache, namespace)
	}
	if req.FanOut != nil {
		v.checkFanOut(path+".fan_out", *req.FanOut, namespace)
	}
	if req.Cache != nil && req.FanOut != nil {
		v.errorf(path, "requests[].cache and fan_out are mutually exclusive: cache stores one token-shaped value, fan_out runs the step once per item — combining them would race N writes into a single state slot")
	}
	if req.If != nil {
		v.checkPredicate(path+".if", *req.If, namespace, false)
	}
	for code, action := range req.OnStatus {
		op := fmt.Sprintf("%s.on_status.%d", path, code)
		if code < 100 || code > 599 {
			v.errorf(op, "status %d is out of HTTP range [100, 599]", code)
		}
		switch action {
		case "skip", "fail", "empty_events", "invalidate_cache":
		case "":
			v.errorf(op, "on_status action is required; want skip|fail|empty_events|invalidate_cache")
		default:
			v.errorf(op, "unknown on_status action %q; want skip|fail|empty_events|invalidate_cache", action)
		}
	}

	seenStatus := map[int]bool{}
	for _, code := range req.ExpectStatus {
		if code < 100 || code > 599 {
			v.errorf(path+".expect_status",
				"status %d is out of HTTP range [100, 599]", code)
		}
		if seenStatus[code] {
			v.errorf(path+".expect_status",
				"status %d is duplicated", code)
		}
		seenStatus[code] = true
	}
}

func (v *validator) checkBody(path string, b Body, namespace *ns) {
	switch len(b.VariantNames()) {
	case 0:
		v.errorf(path, "body block must have exactly one variant (json|form|raw)")
		return
	case 1:
	default:
		v.errorf(path, "body block must have exactly one variant; found %v", b.VariantNames())
		return
	}
	switch {
	case b.JSON != nil:
		for k, val := range b.JSON {
			v.checkValue(fmt.Sprintf("%s.json.%s", path, k), val, namespace, false)
		}
	case b.Form != nil:
		for k, val := range b.Form {
			v.checkValue(fmt.Sprintf("%s.form.%s", path, k), val, namespace, false)
		}
	case b.Raw != nil:
		v.checkValue(path+".raw", *b.Raw, namespace, false)
	}
}

func (v *validator) checkFanOut(path string, f FanOut, namespace *ns) {
	v.checkValue(path+".over", f.Over, namespace, false)
	v.checkFanOutOverForm(path+".over", f.Over)
	if f.As == "" {
		v.errorf(path+".as", "fan_out.as is required")
	} else {
		v.checkFanOutAsShadow(path+".as", f.As, namespace)
	}
	if f.Merge != "" && f.Merge != "flatten" && f.Merge != "wrap" {
		v.errorf(path+".merge", "fan_out.merge must be 'flatten' or 'wrap', got %q", f.Merge)
	}
}

// checkFanOutOverForm rejects fan_out.over Values whose top-level form is
// obviously not list-typed. Ref / Select / List / Concat / IsZero are
// allowed (the actual list-ness is decided at target lowering time when the
// runtime can see the resolved type), but literal scalars, Now, Format,
// Base64, and Object can never produce a list and are almost certainly an
// authoring error (e.g. `over: items` vs
// `over: {ref: steps.list.body.items}`).
func (v *validator) checkFanOutOverForm(path string, val Value) {
	switch {
	case val.LiteralString != nil:
		v.errorf(path, "fan_out.over must resolve to a list Value; got literal_string — did you mean {ref: %s}?", *val.LiteralString)
	case val.LiteralInt != nil:
		v.errorf(path, "fan_out.over must resolve to a list Value; got literal_int")
	case val.LiteralBool != nil:
		v.errorf(path, "fan_out.over must resolve to a list Value; got literal_bool")
	case val.Now != nil:
		v.errorf(path, "fan_out.over must resolve to a list Value; got now (now produces a timestamp)")
	case val.Format != nil:
		v.errorf(path, "fan_out.over must resolve to a list Value; got format (no format verb produces a list)")
	case val.Base64 != nil:
		v.errorf(path, "fan_out.over must resolve to a list Value; got base64 (base64 produces a string)")
	case val.Object != nil:
		v.errorf(path, "fan_out.over must resolve to a list Value; got object")
	}
}

// checkFanOutAsShadow rejects a fan_out.as value that shadows any existing
// namespace name (the fixed roots, plus any declared state field, inferred
// cursor field, or earlier-step id).
func (v *validator) checkFanOutAsShadow(path, as string, namespace *ns) {
	reserved := map[string]struct{}{
		"state": {}, "cursor": {}, "extract": {},
		"steps": {}, "item": {}, "body": {}, "response": {},
	}
	if _, ok := reserved[as]; ok {
		v.errorf(path, "fan_out.as %q shadows the reserved namespace name %q", as, as)
		return
	}
	if _, ok := namespace.state[as]; ok {
		v.errorf(path, "fan_out.as %q collides with a declared state.fields name", as)
		return
	}
	if _, ok := namespace.cursor[as]; ok {
		v.errorf(path, "fan_out.as %q collides with an inferred cursor name", as)
		return
	}
	if _, ok := namespace.stepBodies[as]; ok {
		v.errorf(path, "fan_out.as %q collides with an earlier-step id", as)
		return
	}
	if _, ok := namespace.extract[as]; ok {
		v.errorf(path, "fan_out.as %q collides with an extract name in scope", as)
		return
	}
}

// ---- response ----

func (v *validator) checkResponse(path string, r Response, namespace *ns) {
	switch r.Decode {
	case "json", "ndjson":
	case "":
		v.errorf(path+".decode", "response.decode is required (json|ndjson)")
	default:
		v.errorf(path+".decode", "unknown decode value %q; want json|ndjson", r.Decode)
	}
	// events_at is a Path rooted at response.body.*. The zero Path
	// keeps its "body root" semantics (the whole decoded body IS the
	// events list / event).
	v.checkBodyRootedPath(path+".events_at", r.EventsAt, namespace, true)
	if r.PlaceholderEvent != nil {
		v.checkValue(path+".placeholder_event", *r.PlaceholderEvent, namespace, false)
	}
}

// ---- pagination ----

func (v *validator) checkPagination(path string, p Pagination, namespace *ns) {
	switch len(p.VariantNames()) {
	case 0:
		v.errorf(path, "pagination block must have exactly one variant (none|cursor_token|page_number|offset|link_header|next_url_in_body|scroll_id|graphql_relay)")
		return
	case 1:
	default:
		v.errorf(path, "pagination block must have exactly one variant; found %v", p.VariantNames())
		return
	}

	switch {
	case p.CursorToken != nil:
		v.checkBodyRootedPath(path+".cursor_token.token_at", p.CursorToken.TokenAt, namespace, false)

	case p.PageNumber != nil:
		if p.PageNumber.PageParam == "" {
			v.errorf(path+".page_number.page_param", "page_param is required")
		}
		if !p.PageNumber.HasMoreAt.IsEmpty() {
			v.checkBodyRootedPath(path+".page_number.has_more_at", p.PageNumber.HasMoreAt, namespace, false)
		}

	case p.Offset != nil:
		if p.Offset.OffsetParam == "" {
			v.errorf(path+".offset.offset_param", "offset_param is required")
		}

	case p.LinkHeader != nil:

	case p.NextURLInBody != nil:
		v.checkBodyRootedPath(path+".next_url_in_body.next_url_at", p.NextURLInBody.NextURLAt, namespace, false)

	case p.ScrollID != nil:
		v.checkBodyRootedPath(path+".scroll_id.scroll_id_at", p.ScrollID.ScrollIDAt, namespace, false)
		if p.ScrollID.CompleteWhen != nil {
			v.checkPredicate(path+".scroll_id.complete_when", *p.ScrollID.CompleteWhen, namespace, true)
		}

	case p.GraphQLRelay != nil:
		g := p.GraphQLRelay
		v.checkBodyRootedPath(path+".graphql_relay.has_next_page_at", g.HasNextPageAt, namespace, false)
		v.checkBodyRootedPath(path+".graphql_relay.end_cursor_at", g.EndCursorAt, namespace, false)
		if g.CursorVar == "" {
			v.errorf(path+".graphql_relay.cursor_var", "cursor_var is required")
		}
	}
}

// ---- progress ----

func (v *validator) checkProgress(path string, p Progress, namespace *ns) {
	switch len(p.VariantNames()) {
	case 0:
		v.errorf(path, "progress block must have exactly one variant (stateless|latest_event_timestamp|max_event_field|use_now|time_window|async_job)")
		return
	case 1:
	default:
		v.errorf(path, "progress block must have exactly one variant; found %v", p.VariantNames())
		return
	}

	switch {
	case p.LatestEventTimestamp != nil:
		v.checkTimestampProgress(path+".latest_event_timestamp", *p.LatestEventTimestamp, namespace)

	case p.MaxEventField != nil:
		v.checkTimestampProgress(path+".max_event_field", *p.MaxEventField, namespace)

	case p.UseNow != nil:
		if p.UseNow.Lookback != nil {
			v.checkValue(path+".use_now.lookback", *p.UseNow.Lookback, namespace, false)
		}

	case p.TimeWindow != nil:
		tw := p.TimeWindow
		if tw.InitialOffset.IsAbsent() {
			v.errorf(path+".time_window.initial_offset", "initial_offset is required")
		}
		if tw.Format != "" && !validFormatVerb(tw.Format) {
			v.errorf(path+".time_window.format",
				"unknown format verb %q; want one of: string, int, bool, rfc3339, rfc3339nano, unix_seconds, unix_millis, duration, url_encode, parse_duration",
				tw.Format)
		}

	case p.AsyncJob != nil:
		v.checkAsyncJob(path+".async_job", p.AsyncJob, namespace)
	}
}

func (v *validator) checkTimestampProgress(path string, tp TimestampProgress, namespace *ns) {
	v.checkPerEventPath(path+".event_time.path", tp.EventTime.Path)
	if tp.Initial != nil {
		v.checkValue(path+".initial.lookback", tp.Initial.Lookback, namespace, false)
	}
	if tp.Lookback != nil {
		v.checkValue(path+".lookback", *tp.Lookback, namespace, false)
	}
}

func (v *validator) checkAsyncJob(path string, aj *AsyncJobProgress, namespace *ns) {
	if aj.Submit == nil && aj.Poll == nil && aj.Fetch == nil {
		v.errorf(path, "async_job requires at least one of submit, poll, fetch")
	}

	if aj.Submit != nil {
		v.checkAsyncStepRef(path+".submit.step", aj.Submit.Step, namespace)
		for name, ax := range aj.Submit.Extract {
			ep := fmt.Sprintf("%s.submit.extract.%s.from", path, name)
			v.checkBodyRootedPath(ep, ax.From, namespace, false)
		}
	}
	if aj.Poll != nil {
		v.checkAsyncStepRef(path+".poll.step", aj.Poll.Step, namespace)
		if aj.Poll.CompleteWhen == nil {
			v.errorf(path+".poll.complete_when",
				"complete_when is required when poll is declared")
		} else {
			// complete_when evaluates against the poll step's body; response.body.*
			// refs resolve against it (legacy body.* form is rejected).
			v.checkPredicate(path+".poll.complete_when", *aj.Poll.CompleteWhen, namespace, true)
		}
		for name, ax := range aj.Poll.Extract {
			ep := fmt.Sprintf("%s.poll.extract.%s.from", path, name)
			v.checkBodyRootedPath(ep, ax.From, namespace, false)
		}
	}
	if aj.Fetch != nil {
		v.checkAsyncStepRef(path+".fetch.step", aj.Fetch.Step, namespace)
	}
	if aj.OnComplete != nil && aj.OnComplete.CursorUpdate != nil {
		cu := aj.OnComplete.CursorUpdate
		cuPath := path + ".on_complete.cursor_update"
		switch cu.Kind {
		case "use_now", "latest_event_timestamp", "stateless":
		case "":
			v.errorf(cuPath+".kind",
				"kind is required; want use_now|latest_event_timestamp|stateless")
		default:
			v.errorf(cuPath+".kind",
				"unknown kind %q; want use_now|latest_event_timestamp|stateless",
				cu.Kind)
		}
		if cu.Lookback != nil {
			if cu.Kind == "stateless" {
				v.errorf(cuPath+".lookback",
					"lookback is meaningless when kind is 'stateless' (stateless does not advance the cursor); remove the lookback or pick a different kind")
			}
			v.checkValue(cuPath+".lookback", *cu.Lookback, namespace, false)
		}
		// event_time is required for latest_event_timestamp (the runner needs
		// a path to find the timestamp on each event) and meaningless for
		// use_now / stateless.
		switch cu.Kind {
		case "latest_event_timestamp":
			if cu.EventTime == nil {
				v.errorf(cuPath+".event_time",
					"event_time is required when kind is 'latest_event_timestamp' (names the body path to the per-event timestamp field)")
			} else {
				v.checkPerEventPath(cuPath+".event_time.path", cu.EventTime.Path)
			}
		case "use_now", "stateless":
			if cu.EventTime != nil {
				v.errorf(cuPath+".event_time",
					"event_time is meaningful only when kind is 'latest_event_timestamp'; remove it or pick that kind")
			}
		}
	}
}

// checkRequestCache validates a step-level cache block.
//
// The store_in slot is auto-registered as a runtime state field by the pre-pass
// in checkRequests, so this function only checks structure, not registration.
func (v *validator) checkRequestCache(path string, c RequestCache, namespace *ns) {
	if c.StoreIn == "" {
		v.errorf(path+".store_in", "store_in is required")
	}
	v.checkBodyRootedPath(path+".expiry_field", c.ExpiryField, namespace, false)
	v.checkDuration(path+".expiry_buffer", "expiry_buffer", c.ExpiryBuffer)
	if c.ExpiryFormat != "" && !validFormatVerb(c.ExpiryFormat) {
		v.errorf(path+".expiry_format", "expiry_format %q is not in the format-verb set", c.ExpiryFormat)
	}
}

// checkAsyncStepRef verifies that an async_job role's named step is declared
// in requests[].id. The empty string is rejected as "required".
func (v *validator) checkAsyncStepRef(path, id string, namespace *ns) {
	if id == "" {
		v.errorf(path, "step is required")
		return
	}
	if !namespace.hasStepBody(id) {
		v.errorf(path, "step %q is not declared by any requests[].id", id)
	}
}

// ---- error ----

func (v *validator) checkError(path string, e ErrorBlock) {
	switch e.Mode {
	case "standard", "fail", "warn":
	case "":
		v.errorf(path+".mode", "error.mode is required (standard|fail|warn)")
	default:
		v.errorf(path+".mode", "unknown error mode %q; want standard|fail|warn", e.Mode)
	}
}

// ---- value reference checking ----

// checkValue validates a Value within a namespace context.
// allowBody indicates whether the contextual response.* roots are valid
// here ("response.body.<path>" / "response.header.<name>"). Set true at
// complete_when predicate sites; false everywhere else (top-level Value
// fields, request query/header/body maps, etc.). The legacy bare body.*
// root was deleted in slice 2 and is rejected unconditionally.
func (v *validator) checkValue(path string, val Value, namespace *ns, allowBody bool) {
	if val.IsZero || val.LiteralString != nil || val.LiteralInt != nil || val.LiteralBool != nil {
		return
	}

	switch {
	case val.Ref != nil:
		v.checkPathRef(path+".ref", val.Ref.Path, namespace, allowBody)
		if val.Ref.Default != nil {
			v.checkValue(path+".default", *val.Ref.Default, namespace, allowBody)
		}

	case val.Now != nil:
		if val.Now.Offset != nil {
			v.checkValue(path+".offset", *val.Now.Offset, namespace, false)
		}

	case len(val.Concat) > 0:
		for i, el := range val.Concat {
			v.checkValue(fmt.Sprintf("%s.concat[%d]", path, i), el, namespace, false)
		}

	case val.Select != nil:
		for i, b := range val.Select.Branches {
			bp := fmt.Sprintf("%s.select.branches[%d]", path, i)
			v.checkPredicate(bp+".when", b.When, namespace, false)
			v.checkValue(bp+".value", b.Value, namespace, false)
		}
		v.checkValue(path+".select.default", val.Select.Default, namespace, false)

	case val.Format != nil:
		if !validFormatVerb(val.Format.Verb) {
			v.errorf(path+".format", "unknown format verb %q", val.Format.Verb)
		}
		v.checkValue(path+".value", val.Format.Value, namespace, false)

	case val.Base64 != nil:
		v.checkValue(path+".base64", *val.Base64, namespace, false)

	case len(val.List) > 0:
		for i, el := range val.List {
			v.checkValue(fmt.Sprintf("%s.list[%d]", path, i), el, namespace, false)
		}

	case val.Object != nil:
		for k, el := range val.Object {
			v.checkValue(fmt.Sprintf("%s.object.%s", path, k), el, namespace, false)
		}
	}
}

// checkBodyRootedPath validates a Path slot whose grammar is restricted to
// body-rooted forms. Accepted shapes:
//
//   - response.body.<path>     the active step's response body
//   - steps.<id>.body.<path>   a labelled prior step's response body
//
// Used at every §1.2 read site (events_at, token_at, scroll_id_at,
// next_url_at, has_more_at, has_next_page_at, end_cursor_at, expiry_field,
// async_job extract.from). The error messages name the new namespace form
// so a bare body-relative dotted string surfaces a precise migration hint.
//
// When allowEmpty is true the zero Path is accepted (events_at's "body
// root" semantics: the whole decoded body IS the events list).
//
// stepBodies is consulted for steps.<id>.body.<path> to verify the
// referenced id is declared.
func (v *validator) checkBodyRootedPath(path string, p Path, namespace *ns, allowEmpty bool) {
	if p.IsEmpty() {
		if !allowEmpty {
			v.errorf(path, "is required; want response.body.<path> (or steps.<id>.body.<path>)")
		}
		return
	}
	switch p.Parts[0] {
	case "response":
		if len(p.Parts) < 2 || p.Parts[1] != "body" {
			v.errorf(path, "ref %q: at this slot the response root must be response.body.<path>; response.header.<name> is not valid here", p.String())
			return
		}
		if len(p.Parts) < 3 {
			v.errorf(path, "ref %q: response.body root requires a sub-path segment: response.body.<path>", p.String())
		}
	case "steps":
		if len(p.Parts) < 2 {
			v.errorf(path, "ref %q: steps ref requires an id: steps.<id>.body.<path>", p.String())
			return
		}
		id := p.Parts[1]
		if !namespace.hasStepBody(id) {
			v.errorf(path, "ref %q: step %q has no id or has not been declared", p.String(), id)
			return
		}
		if len(p.Parts) < 3 || p.Parts[2] != "body" {
			v.errorf(path, "ref %q: at this slot the steps ref must be steps.<id>.body.<path>; steps.<id>.header.<name> is not valid here", p.String())
			return
		}
		if len(p.Parts) < 4 {
			v.errorf(path, "ref %q: steps.<id>.body root requires a sub-path segment: steps.%s.body.<path>", p.String(), id)
		}
	default:
		v.errorf(path,
			"%q must be namespace-rooted; use response.body.%s (or steps.<id>.body.%s for a labelled prior step)",
			p.String(), p.String(), p.String())
	}
}

// checkExtractFromPath validates a requests[].extract[].from Path slot.
//
// Accepted shapes (a strict superset of checkBodyRootedPath — the from slot
// also reaches headers via the symmetric *.header.<name> roots):
//
//   - response.body.<path>     the active step's response body
//   - response.header.<name>   the active step's response headers
//   - steps.<id>.body.<path>   a labelled prior step's response body
//   - steps.<id>.header.<name> a labelled prior step's response headers
//
// stepBodies is consulted for the steps.<id>.* arms to verify the referenced
// id is declared. The empty Path is rejected — the from slot is always
// required (an extract that resolves to nothing has no purpose).
func (v *validator) checkExtractFromPath(path string, p Path, namespace *ns) {
	if p.IsEmpty() {
		v.errorf(path, "is required; want response.body.<path>, response.header.<name>, or steps.<id>.{body|header}.<...>")
		return
	}
	switch p.Parts[0] {
	case "response":
		if len(p.Parts) < 2 {
			v.errorf(path, "ref %q: response ref requires a kind segment: response.body.<path> or response.header.<name>", p.String())
			return
		}
		switch p.Parts[1] {
		case "body":
			if len(p.Parts) < 3 {
				v.errorf(path, "ref %q: response.body root requires a sub-path segment: response.body.<path>", p.String())
			}
		case "header":
			if len(p.Parts) < 3 {
				v.errorf(path, "ref %q: response.header root requires a header name: response.header.<name>", p.String())
			}
		default:
			v.errorf(path, "ref %q: response second segment must be \"body\" or \"header\" (got %q); only response.body.<path> and response.header.<name> are valid", p.String(), p.Parts[1])
		}
	case "steps":
		if len(p.Parts) < 2 {
			v.errorf(path, "ref %q: steps ref requires an id: steps.<id>.body.<path> or steps.<id>.header.<name>", p.String())
			return
		}
		id := p.Parts[1]
		if !namespace.hasStepBody(id) {
			v.errorf(path, "ref %q: step %q has no id or has not been declared", p.String(), id)
			return
		}
		if len(p.Parts) < 3 {
			v.errorf(path, "ref %q: steps ref must include a kind segment: steps.%s.body.<path> or steps.%s.header.<name>", p.String(), id, id)
			return
		}
		switch p.Parts[2] {
		case "body":
			if len(p.Parts) < 4 {
				v.errorf(path, "ref %q: steps.<id>.body root requires a sub-path segment: steps.%s.body.<path>", p.String(), id)
			}
		case "header":
			if len(p.Parts) < 4 {
				v.errorf(path, "ref %q: steps.<id>.header ref requires a header name: steps.%s.header.<name>", p.String(), id)
			}
		default:
			v.errorf(path, "ref %q: steps ref second segment must be \"body\" or \"header\" (got %q); only steps.<id>.body.<path> and steps.<id>.header.<name> are valid", p.String(), p.Parts[2])
		}
	default:
		v.errorf(path,
			"%q must be namespace-rooted; use response.body.<path>, response.header.<name>, or steps.<id>.{body|header}.<...> for a labelled prior step",
			p.String())
	}
}

// checkPerEventPath validates a Path at one of the per-event sites
// (progress.latest_event_timestamp.event_time.path,
// progress.max_event_field.event_time.path,
// progress.async_job.on_complete.cursor_update.event_time.path).
//
// Per §1.5 these slots stay body-relative — the runner walks the
// events list located by response.events_at and reads this path from
// each element, so the segments are per-event, not document-rooted.
// A leading namespace root (response, steps, state, cursor, extract,
// body) is rejected so authors don't expect namespace resolution
// at the per-event reader.
func (v *validator) checkPerEventPath(path string, p Path) {
	if p.IsEmpty() {
		v.errorf(path, "event_time.path is required (per-event sub-path; the runner walks the events list at response.events_at and reads this path from each element)")
		return
	}
	if root := p.Root(); isNamespaceRoot(root) {
		v.errorf(path,
			"event_time.path %q must be a per-event sub-path; the runner walks the events list at response.events_at and reads this path from each element — namespace roots (state|cursor|extract|steps|item|body|response) are not valid here",
			p.String())
	}
}

// checkPathRef validates a Path reference against the current namespace.
func (v *validator) checkPathRef(path string, p Path, namespace *ns, allowBody bool) {
	if p.IsEmpty() {
		v.errorf(path, "ref path must not be empty")
		return
	}
	root := p.Parts[0]

	// fan_out item binding is the only root whose name is author-chosen
	// (`fan_out.as`). It must be checked before the static switch so that the
	// active binding is honored regardless of name.
	if namespace.itemNamespace != "" && root == namespace.itemNamespace {
		return
	}

	switch root {
	case "state":
		if len(p.Parts) < 2 {
			v.errorf(path, "state ref requires a field name: state.<name>")
			return
		}
		name := p.Parts[1]
		if !namespace.hasStateField(name) && namespace.oauthStoreIn != name {
			v.errorf(path, "ref %q: state field %q is not declared in state.fields", p.String(), name)
		}

	case "cursor":
		if len(p.Parts) < 2 {
			v.errorf(path, "cursor ref requires a field name: cursor.<name>")
			return
		}
		name := p.Parts[1]
		if !namespace.hasCursorField(name) {
			v.errorf(path, "ref %q: cursor field %q is not provided by the active pagination/progress strategy", p.String(), name)
		}

	case "extract":
		if len(p.Parts) < 2 {
			v.errorf(path, "extract ref requires a name: extract.<name>")
			return
		}
		name := p.Parts[1]
		if !namespace.hasExtract(name) {
			v.errorf(path, "ref %q: extract %q is not available in this scope", p.String(), name)
		}

	case "steps":
		if len(p.Parts) < 2 {
			v.errorf(path, "steps ref requires an id: steps.<id>.body.<path> or steps.<id>.header.<name>")
			return
		}
		id := p.Parts[1]
		if !namespace.hasStepBody(id) {
			v.errorf(path, "ref %q: step %q has no id or has not been declared", p.String(), id)
			return
		}
		if len(p.Parts) < 3 {
			v.errorf(path, "ref %q: steps ref must include a kind segment: steps.%s.body.<path> or steps.%s.header.<name>", p.String(), id, id)
			return
		}
		switch p.Parts[2] {
		case "body":
			// steps.<id>.body.<path> — body segments are validated structurally
			// by the Path codec; no further checks required here.
		case "header":
			if len(p.Parts) < 4 {
				v.errorf(path, "ref %q: steps.<id>.header ref requires a header name: steps.%s.header.<name>", p.String(), id)
			}
		default:
			v.errorf(path, "ref %q: steps ref second segment must be \"body\" or \"header\" (got %q); only steps.<id>.body.<path> and steps.<id>.header.<name> are valid", p.String(), p.Parts[2])
		}

	case "response":
		if !allowBody {
			v.errorf(path, "ref %q: response namespace is only valid inside complete_when predicates (pagination.scroll_id.complete_when, progress.async_job.poll.complete_when)", p.String())
			return
		}
		if len(p.Parts) < 2 {
			v.errorf(path, "ref %q: response ref requires a kind segment: response.body.<path> or response.header.<name>", p.String())
			return
		}
		switch p.Parts[1] {
		case "body":
			// response.body.<path> — body segments are validated structurally
			// by the Path codec.
		case "header":
			if len(p.Parts) < 3 {
				v.errorf(path, "ref %q: response.header ref requires a header name: response.header.<name>", p.String())
			}
		default:
			v.errorf(path, "ref %q: response second segment must be \"body\" or \"header\" (got %q); only response.body.<path> and response.header.<name> are valid", p.String(), p.Parts[1])
		}

	case "body":
		// The legacy body.<path> root is gone — every body-relative ref
		// is now namespace-rooted. Predicate sites that historically used
		// body.<path> migrate to response.body.<path>; the runtime resolver
		// proved interchangeable in slice 1.
		v.errorf(path, "ref %q: body namespace was removed; use response.body.<path> (resolves against the same response context inside complete_when predicates)", p.String())

	default:
		if namespace.itemNamespace != "" {
			v.errorf(path, "ref %q: unknown namespace root %q; want state|cursor|extract|steps|response|%s",
				p.String(), root, namespace.itemNamespace)
		} else {
			v.errorf(path, "ref %q: unknown namespace root %q; want state|cursor|extract|steps|response (or the fan_out.as name inside a fan_out step)",
				p.String(), root)
		}
	}
}

// ---- predicate checking ----

// checkPredicate validates a Predicate within a namespace context.
// allowBody indicates whether the contextual response roots are valid — the
// legacy "body.<path>" and the new "response.body.<path>" /
// "response.header.<name>" forms. Both are accepted at complete_when
// predicate sites (pagination.scroll_id.complete_when,
// progress.async_job.poll.complete_when).
func (v *validator) checkPredicate(path string, p Predicate, namespace *ns, allowBody bool) {
	if p.IsZero() {
		v.errorf(path, "predicate must not be empty")
		return
	}
	switch {
	case p.Eq != nil:
		v.checkPathRef(path+".eq.path", p.Eq.Path, namespace, allowBody)
		v.checkValue(path+".eq.value", p.Eq.Value, namespace, false)

	case p.Gt != nil:
		v.checkPathRef(path+".gt.path", p.Gt.Path, namespace, allowBody)
		v.checkValue(path+".gt.value", p.Gt.Value, namespace, false)

	case p.Lt != nil:
		v.checkPathRef(path+".lt.path", p.Lt.Path, namespace, allowBody)
		v.checkValue(path+".lt.value", p.Lt.Value, namespace, false)

	case p.Gte != nil:
		v.checkPathRef(path+".gte.path", p.Gte.Path, namespace, allowBody)
		v.checkValue(path+".gte.value", p.Gte.Value, namespace, false)

	case p.Lte != nil:
		v.checkPathRef(path+".lte.path", p.Lte.Path, namespace, allowBody)
		v.checkValue(path+".lte.value", p.Lte.Value, namespace, false)

	case p.Present != nil:
		v.checkPathRef(path+".present", *p.Present, namespace, allowBody)

	case len(p.And) > 0:
		for i, sub := range p.And {
			v.checkPredicate(fmt.Sprintf("%s.and[%d]", path, i), sub, namespace, allowBody)
		}

	case len(p.Or) > 0:
		for i, sub := range p.Or {
			v.checkPredicate(fmt.Sprintf("%s.or[%d]", path, i), sub, namespace, allowBody)
		}

	case p.Not != nil:
		v.checkPredicate(path+".not", *p.Not, namespace, allowBody)
	}
}

// ---- helpers ----

// isNamespaceRoot reports whether s is one of the reserved namespace-root
// names. Used by checkPerEventPath to reject namespace-root prefixes on
// per-event sub-paths.
func isNamespaceRoot(s string) bool {
	switch s {
	case "state", "cursor", "extract", "steps", "item", "body", "response":
		return true
	}
	return false
}

func validFormatVerb(v string) bool {
	switch v {
	case "string", "int", "bool", "rfc3339", "rfc3339nano",
		"unix_seconds", "unix_millis", "duration",
		"url_encode", "parse_duration":
		return true
	}
	return false
}

// cursorSchema returns the set of cursor field names that the active
// pagination and progress strategies provide for doc d. Used by Validate to
// reject {ref: cursor.<name>} for names no active strategy populates.
//
// The offset strategy registers "offset_end" only when batch_size is set,
// matching offsetPagination.seed's runtime behaviour (offset_end = offset +
// batch_size; absent without batch_size). async_job registers
// "last_timestamp" whenever on_complete.cursor_update.kind drives a
// timestamp write (use_now / latest_event_timestamp); the stateless kind
// leaves the cursor untouched and so registers nothing.
func cursorSchema(d *Doc) map[string]struct{} {
	cs := make(map[string]struct{})

	// Pagination.
	p := d.Pagination
	switch {
	case p.CursorToken != nil:
		cs["token"] = struct{}{}
	case p.PageNumber != nil:
		cs["page"] = struct{}{}
	case p.Offset != nil:
		cs["offset"] = struct{}{}
		if p.Offset.BatchSize != nil {
			cs["offset_end"] = struct{}{}
		}
	case p.LinkHeader != nil:
		cs["next_link"] = struct{}{}
	case p.NextURLInBody != nil:
		cs["next_url"] = struct{}{}
	case p.ScrollID != nil:
		cs["scroll_id"] = struct{}{}
	case p.GraphQLRelay != nil:
		cs[p.GraphQLRelay.CursorVar] = struct{}{}
	}

	// Progress.
	pr := d.Progress
	switch {
	case pr.LatestEventTimestamp != nil, pr.MaxEventField != nil, pr.UseNow != nil:
		cs["last_timestamp"] = struct{}{}
	case pr.TimeWindow != nil:
		cs["window_start"] = struct{}{}
		cs["window_end"] = struct{}{}
	case pr.AsyncJob != nil:
		cs["phase"] = struct{}{}
		aj := pr.AsyncJob
		if aj.Submit != nil {
			for name := range aj.Submit.Extract {
				cs[name] = struct{}{}
			}
		}
		if aj.Poll != nil {
			for name := range aj.Poll.Extract {
				cs[name] = struct{}{}
			}
		}
		if aj.OnComplete != nil && aj.OnComplete.CursorUpdate != nil {
			switch aj.OnComplete.CursorUpdate.Kind {
			case "use_now", "latest_event_timestamp":
				cs["last_timestamp"] = struct{}{}
			}
		}
	}
	return cs
}
