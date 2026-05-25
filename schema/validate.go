// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// validGlob reports whether g is a well-formed path.Match pattern. Declared
// at package scope so it can reach the path package, which the validator's
// per-rule methods cannot — they bind a local "path" parameter that shadows
// the import.
func validGlob(g string) error {
	_, err := path.Match(g, "")
	return err
}

// Diagnostic is a single structural-validation finding.
type Diagnostic struct {
	// Path is a dotted IR path with array indices, e.g.
	// "requests[0].extract[1].from". Empty for document-wide findings.
	Path string `json:"path"`
	// Message is the human-readable error or warning text.
	Message string `json:"message"`
	// Hint is an optional follow-up sentence pointing at the fix.
	Hint string `json:"hint,omitempty"`
	// Severity is "error" or "warning".
	Severity string `json:"severity"`
	// Line is an optional source-location annotation. Validate never
	// sets it; callers that wrap Validate around raw YAML/JSON bytes
	// (the CLI, the HTTP API) may set it from YAML parser error
	// messages for richer output.
	Line int `json:"line,omitempty"`
	// Column is the source-column counterpart to Line, with the same
	// "set by callers, not Validate" contract.
	Column int `json:"column,omitempty"`
}

// Validate walks d and returns structural diagnostics.
//
// An empty result means the document is structurally valid. Validate
// enforces the shape rules in docs/schema.md: closed unions carry
// exactly one variant, required fields are present, references resolve
// against the declared namespace set, lifetime inference does not
// conflict across write sites, and the closed verb sets
// (on_status, error.mode, decode) are respected. The format: verb is
// not shape-validated here — its closed-set membership is checked at
// runtime, where unrecognised verbs are tried as a Go time layout (see
// DESIGN §4.1). Capability checks ("does this target support fan_out
// merge: wrap") live in the consumer, not here.
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

func (v *validator) warnf(path, format string, args ...any) {
	v.diags = append(v.diags, Diagnostic{
		Path:     path,
		Message:  fmt.Sprintf(format, args...),
		Severity: "warning",
	})
}

// scope is the namespace context carried through every reference check.
// state, stepIDs, and cacheSlots are populated up-front; extract grows
// as the validator walks requests in declared order; fanOutAlias is set
// for the duration of one fan-out step's sub-tree.
type scope struct {
	state       map[string]FieldDecl
	stepIDs     map[string]struct{}
	extract     map[string]struct{}
	cacheSlots  map[string]struct{}
	fanOutAlias string
	// hasCache is true when any Cache block (auth grant or request)
	// exists anywhere in the doc. invalidate_cache verbs degrade to
	// empty_events at runtime when this is false; the validator warns.
	hasCache bool
}

// ---- top-level orchestration ----

func (v *validator) run(d *Doc) {
	if d.IRVersion != IRVersion {
		v.errorf("ir_version", "expected %q, got %q", IRVersion, d.IRVersion)
	}

	sc := &scope{
		state:      map[string]FieldDecl{},
		stepIDs:    map[string]struct{}{},
		extract:    map[string]struct{}{},
		cacheSlots: map[string]struct{}{},
	}
	maps.Copy(sc.state, d.State)
	for _, r := range d.Requests {
		if r.ID != "" {
			sc.stepIDs[r.ID] = struct{}{}
		}
	}
	collectCacheSlots(d, sc)
	sc.hasCache = len(sc.cacheSlots) > 0

	v.checkStateDeclarations(d, sc)
	v.checkLifetimes(d, sc)
	v.checkAuth("auth", d.Auth, sc)
	v.checkRequests(d, sc)
	v.checkPagination("pagination", d.Pagination, sc)
	v.checkProgress("progress", d.Progress, sc)
	if d.Error != nil {
		v.checkError("error", *d.Error)
	}
}

// collectCacheSlots scans every Cache block in the doc and registers the
// cache.<name> slot it writes. The scope's cacheSlots map is consulted
// at every {ref: cache.<name>} site to verify the slot was declared.
func collectCacheSlots(d *Doc, sc *scope) {
	add := func(c *Cache) {
		if c == nil || c.To.IsEmpty() {
			return
		}
		if len(c.To.Parts) >= 2 && c.To.Parts[0] == "cache" {
			sc.cacheSlots[c.To.Parts[1]] = struct{}{}
		}
	}
	if d.Auth.OAuth2 != nil {
		if cc := d.Auth.OAuth2.ClientCredentials; cc != nil {
			add(cc.Cache)
		}
		if pg := d.Auth.OAuth2.PasswordGrant; pg != nil {
			add(pg.Cache)
		}
	}
	if d.Auth.MultiMode != nil {
		for _, br := range d.Auth.MultiMode.Branches {
			collectAuthCache(br.Auth, sc)
		}
		collectAuthCache(d.Auth.MultiMode.Default, sc)
	}
	for _, r := range d.Requests {
		add(r.Cache)
	}
}

func collectAuthCache(a Auth, sc *scope) {
	if a.OAuth2 == nil {
		return
	}
	if cc := a.OAuth2.ClientCredentials; cc != nil && cc.Cache != nil {
		if len(cc.Cache.To.Parts) >= 2 && cc.Cache.To.Parts[0] == "cache" {
			sc.cacheSlots[cc.Cache.To.Parts[1]] = struct{}{}
		}
	}
	if pg := a.OAuth2.PasswordGrant; pg != nil && pg.Cache != nil {
		if len(pg.Cache.To.Parts) >= 2 && pg.Cache.To.Parts[0] == "cache" {
			sc.cacheSlots[pg.Cache.To.Parts[1]] = struct{}{}
		}
	}
}

// ---- state declarations ----

func (v *validator) checkStateDeclarations(d *Doc, sc *scope) {
	for name, fd := range d.State {
		path := fmt.Sprintf("state.%s", name)
		if !validStateType(fd.Type) {
			v.errorf(path+".type", "unknown type %q; want one of string|int|bool|secret|duration|timestamp|url|enum", fd.Type)
		}
		if fd.Type == "enum" && len(fd.Values) == 0 {
			v.errorf(path+".values", "enum field must declare at least one value")
		}
		if fd.Type != "enum" && len(fd.Values) > 0 {
			v.errorf(path+".values", "values is only valid when type: enum (got type %q)", fd.Type)
		}
		if fd.Format != "" {
			switch fd.Type {
			case "timestamp", "duration":
				// any non-empty format is accepted: a recognised
				// closed-set verb names the named parser, anything
				// else is treated as a Go layout string.
			default:
				v.errorf(path+".format", "format is only valid when type: timestamp or type: duration (got type %q)", fd.Type)
			}
		}
		if fd.Default != nil {
			v.checkValue(path+".default", *fd.Default, sc)
		}
	}
}

// ---- lifetime inference ----

// checkLifetimes scans every state-write site, verifies the destination
// state field is declared, and rejects fields that would be classified
// as both per-drain (pagination target) and persistent (progress or
// extract target). The classification itself is not stored on Doc — the
// runner re-derives it from the same write sites.
func (v *validator) checkLifetimes(d *Doc, sc *scope) {
	perDrainWriters := map[string]string{}   // field -> first writer description
	persistentWriters := map[string]string{} // field -> first writer description

	noteTarget := func(writer, field string, persistent bool) {
		if persistent {
			if _, ok := persistentWriters[field]; !ok {
				persistentWriters[field] = writer
			}
		} else {
			if _, ok := perDrainWriters[field]; !ok {
				perDrainWriters[field] = writer
			}
		}
	}

	checkStateTo := func(path string, p Path, writer string, persistent bool) (string, bool) {
		if p.IsEmpty() {
			v.errorf(path, "to is required; want state.<name>")
			return "", false
		}
		if len(p.Parts) != 2 || p.Parts[0] != "state" {
			v.errorf(path, "to %q must be state.<name>", p.String())
			return "", false
		}
		name := p.Parts[1]
		if _, ok := sc.state[name]; !ok {
			v.errorf(path, "to %q targets an undeclared state field; declare state.%s", p.String(), name)
			return name, false
		}
		noteTarget(writer, name, persistent)
		return name, true
	}

	switch {
	case d.Pagination.CursorToken != nil:
		checkStateTo("pagination.cursor_token.to", d.Pagination.CursorToken.To, "pagination.cursor_token.to", false)
	case d.Pagination.NextURL != nil:
		checkStateTo("pagination.next_url.to", d.Pagination.NextURL.To, "pagination.next_url.to", false)
	case d.Pagination.Counter != nil:
		checkStateTo("pagination.counter.to", d.Pagination.Counter.To, "pagination.counter.to", false)
	case d.Pagination.Custom != nil:
		for i, w := range d.Pagination.Custom.Advance {
			p := fmt.Sprintf("pagination.custom.advance[%d].to", i)
			checkStateTo(p, w.To, p, false)
		}
	}

	for i, w := range d.Progress {
		p := fmt.Sprintf("progress[%d].to", i)
		checkStateTo(p, w.To, p, true)
	}

	for i, req := range d.Requests {
		for j, ex := range req.Extract {
			p := fmt.Sprintf("requests[%d].extract[%d].to", i, j)
			if ex.To.IsEmpty() {
				v.errorf(p, "to is required; want state.<name> or extract.<name>")
				continue
			}
			if len(ex.To.Parts) != 2 {
				v.errorf(p, "to %q must be state.<name> or extract.<name>", ex.To.String())
				continue
			}
			switch ex.To.Parts[0] {
			case "state":
				name := ex.To.Parts[1]
				if _, ok := sc.state[name]; !ok {
					v.errorf(p, "to %q targets an undeclared state field; declare state.%s", ex.To.String(), name)
					continue
				}
				noteTarget(p, name, true)
			case "extract":
				name := ex.To.Parts[1]
				if name == "" {
					v.errorf(p, "extract destination name is required")
					continue
				}
				// extract.<name> is per-iteration scratch; not declared.
			default:
				v.errorf(p, "to %q must be state.<name> or extract.<name>", ex.To.String())
			}
		}
	}

	for field, perDrain := range perDrainWriters {
		if persistent, ok := persistentWriters[field]; ok {
			v.errorf(fmt.Sprintf("state.%s", field),
				"state.%s is written by both %s (per-drain) and %s (persistent); rename one destination so a single lifetime applies",
				field, perDrain, persistent)
		}
	}
}

// ---- auth ----

func (v *validator) checkAuth(path string, a Auth, sc *scope) {
	switch len(a.VariantNames()) {
	case 0:
		v.errorf(path, "auth block must have exactly one variant (none|bearer|basic|api_key|custom|oauth2|sigv4|multi_mode)")
		return
	case 1:
	default:
		v.errorf(path, "auth block must have exactly one variant; found %v", a.VariantNames())
		return
	}

	switch {
	case a.Bearer != nil:
		v.checkValue(path+".bearer.token", a.Bearer.Token, sc)
	case a.Basic != nil:
		v.checkValue(path+".basic.username", a.Basic.Username, sc)
		v.checkValue(path+".basic.password", a.Basic.Password, sc)
	case a.APIKey != nil:
		if a.APIKey.Header == "" {
			v.errorf(path+".api_key.header", "header is required")
		}
		v.checkValue(path+".api_key.value", a.APIKey.Value, sc)
	case a.Custom != nil:
		if a.Custom.Header == "" {
			v.errorf(path+".custom.header", "header is required")
		}
		v.checkValue(path+".custom.value", a.Custom.Value, sc)
	case a.OAuth2 != nil:
		v.checkOAuth2(path+".oauth2", a.OAuth2, sc)
	case a.SigV4 != nil:
		v.checkSigV4(path+".sigv4", a.SigV4, sc)
	case a.MultiMode != nil:
		v.checkMultiMode(path+".multi_mode", a.MultiMode, sc)
	}
}

// checkSigV4 validates the AWS SigV4 auth variant. Region and Service are
// required Values. Credentials are an all-or-nothing pair: it is an error
// to set exactly one of access_key_id / secret_access_key, and
// session_token may only accompany a full static-credential pair. Omitting
// all three selects the AWS default credential chain.
func (v *validator) checkSigV4(path string, a *SigV4Auth, sc *scope) {
	v.checkValue(path+".region", a.Region, sc)
	v.checkValue(path+".service", a.Service, sc)

	hasID := a.AccessKeyID != nil
	hasSecret := a.SecretAccessKey != nil
	if hasID != hasSecret {
		v.errorf(path, "access_key_id and secret_access_key must be set together (set both for static credentials, or omit both for the AWS default credential chain)")
	}
	if a.SessionToken != nil && (!hasID || !hasSecret) {
		v.errorf(path+".session_token", "session_token requires access_key_id and secret_access_key to be set")
	}

	if hasID {
		v.checkValue(path+".access_key_id", *a.AccessKeyID, sc)
	}
	if hasSecret {
		v.checkValue(path+".secret_access_key", *a.SecretAccessKey, sc)
	}
	if a.SessionToken != nil {
		v.checkValue(path+".session_token", *a.SessionToken, sc)
	}
}

func (v *validator) checkOAuth2(path string, o *OAuth2Auth, sc *scope) {
	switch len(o.VariantNames()) {
	case 0:
		v.errorf(path, "oauth2 block must contain exactly one grant type (client_credentials|password_grant)")
		return
	case 1:
	default:
		v.errorf(path, "oauth2 block must contain exactly one grant type; found %v", o.VariantNames())
		return
	}
	if cc := o.ClientCredentials; cc != nil {
		p := path + ".client_credentials"
		v.checkValue(p+".token_url", cc.TokenURL, sc)
		v.checkValue(p+".client_id", cc.ClientID, sc)
		v.checkValue(p+".client_secret", cc.ClientSecret, sc)
		v.checkCache(p+".cache", cc.Cache, sc)
	}
	if pg := o.PasswordGrant; pg != nil {
		p := path + ".password_grant"
		v.checkValue(p+".token_url", pg.TokenURL, sc)
		v.checkValue(p+".username", pg.Username, sc)
		v.checkValue(p+".password", pg.Password, sc)
		if pg.ClientID != nil {
			v.checkValue(p+".client_id", *pg.ClientID, sc)
		}
		v.checkCache(p+".cache", pg.Cache, sc)
	}
}

func (v *validator) checkMultiMode(path string, m *MultiModeAuth, sc *scope) {
	if len(m.Branches) == 0 {
		v.errorf(path+".branches", "multi_mode requires at least one branch")
	}
	for i, b := range m.Branches {
		bp := fmt.Sprintf("%s.branches[%d]", path, i)
		v.checkPredicate(bp+".when", b.When, sc)
		if b.Auth.MultiMode != nil {
			v.errorf(bp+".auth", "nested multi_mode is not allowed")
		} else {
			v.checkAuth(bp+".auth", b.Auth, sc)
		}
	}
	if len(m.Default.VariantNames()) == 0 {
		v.errorf(path+".default", "default is required (a bare auth value)")
	} else if m.Default.MultiMode != nil {
		v.errorf(path+".default", "nested multi_mode is not allowed in the default arm")
	} else {
		v.checkAuth(path+".default", m.Default, sc)
	}
}

// ---- cache ----

func (v *validator) checkCache(path string, c *Cache, sc *scope) {
	if c == nil {
		return
	}
	if c.To.IsEmpty() {
		v.errorf(path+".to", "to is required; want cache.<name>")
	} else if len(c.To.Parts) != 2 || c.To.Parts[0] != "cache" {
		v.errorf(path+".to", "to %q must be cache.<name>", c.To.String())
	} else if c.To.Parts[1] == "" {
		v.errorf(path+".to", "cache slot name is required")
	}
	if c.ExpiresAt.IsAbsent() {
		v.errorf(path+".expires_at", "expires_at is required (Value resolving to a time.Time)")
	} else {
		v.checkValue(path+".expires_at", c.ExpiresAt, sc)
	}
	if c.Buffer == "" {
		v.errorf(path+".buffer", "buffer is required (Go duration, e.g. \"60s\")")
	} else if _, err := time.ParseDuration(c.Buffer); err != nil {
		v.errorf(path+".buffer", "buffer %q is not a valid Go duration: %v", c.Buffer, err)
	}
}

// ---- requests ----

func (v *validator) checkRequests(d *Doc, sc *scope) {
	if len(d.Requests) == 0 {
		v.errorf("requests", "at least one request is required")
		return
	}

	seenIDs := map[string]int{}
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
		if req.EventsAt != nil {
			producers++
			if req.Method == "HEAD" {
				v.errorf(p+".events_at",
					"HEAD has no response body; the events producer requires GET/POST/PUT/PATCH/DELETE")
			}
		}
		if req.Cache != nil && req.FanOut != nil {
			v.errorf(p, "cache and fan_out are mutually exclusive: cache stores one token-shaped value, fan_out runs the step once per item")
		}
	}
	switch {
	case producers == 0:
		v.errorf("requests", "exactly one request must set events_at to mark the events producer; got 0")
	case producers > 1:
		v.errorf("requests", "at most one request may set events_at; got %d", producers)
	}

	for i, req := range d.Requests {
		p := fmt.Sprintf("requests[%d]", i)
		// extract names from earlier steps stay visible to later steps;
		// fanOutAlias and the per-step extract slate are step-local.
		stepScope := *sc
		stepScope.extract = sc.extract
		if req.FanOut != nil {
			stepScope.fanOutAlias = req.FanOut.As
		}
		v.checkRequest(p, req, &stepScope)
		for _, ex := range req.Extract {
			if len(ex.To.Parts) == 2 && ex.To.Parts[0] == "extract" && ex.To.Parts[1] != "" {
				sc.extract[ex.To.Parts[1]] = struct{}{}
			}
		}
	}
}

func (v *validator) checkRequest(path string, req Request, sc *scope) {
	switch req.Method {
	case "":
		v.errorf(path+".method", "method is required")
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD":
	default:
		v.errorf(path+".method", "unknown HTTP method %q", req.Method)
	}

	if req.URL.IsAbsent() {
		v.errorf(path+".url", "url is required (absolute URL Value)")
	} else {
		v.checkValue(path+".url", req.URL, sc)
	}

	for k, val := range req.Query {
		v.checkValue(fmt.Sprintf("%s.query.%s", path, k), val, sc)
	}
	for k, val := range req.Headers {
		v.checkValue(fmt.Sprintf("%s.headers.%s", path, k), val, sc)
	}
	if req.Body != nil {
		v.checkBody(path+".body", *req.Body, sc)
	}

	// A nil decode chain means "omitted ⇒ json" and needs no validation; a
	// non-nil chain (including an explicit empty list) is checked in full.
	if req.Decode != nil {
		v.checkDecodeChain(path+".decode", req.Decode)
	}
	// A non-nil events_at marks this step as the events producer.
	if req.EventsAt != nil {
		v.checkEventsAtPath(path+".events_at", *req.EventsAt, sc)
		// A csv terminal treats each row as an event, so events_at has
		// nothing to walk. (ndjson still applies a non-empty events_at
		// per line.) An omitted decode defaults to json, never csv.
		if req.Decode.Terminal() == "csv" && !req.EventsAt.IsEmpty() {
			v.errorf(path+".events_at", "events_at must be empty for csv decode; each row is an event")
		}
	}

	for j, ex := range req.Extract {
		ep := fmt.Sprintf("%s.extract[%d]", path, j)
		// .to lifetime/destination already checked in checkLifetimes;
		// .coerce verb and .regex pattern are not shape-validated here —
		// invalid values surface at runtime as a wrapped error.
		if ex.From.IsEmpty() {
			v.errorf(ep+".from", "from is required; want response.body.<path>, response.header.<name>, or steps.<id>.{body|header}.<...>")
		} else {
			v.checkExtractFromPath(ep+".from", ex.From, sc)
		}
	}

	if req.Cache != nil {
		v.checkCache(path+".cache", req.Cache, sc)
	}
	if req.FanOut != nil {
		v.checkFanOut(path+".fan_out", *req.FanOut, sc)
	}

	if req.If != nil {
		v.checkPredicate(path+".if", *req.If, sc)
	}
	if req.TerminateWhen != nil {
		v.checkPredicate(path+".terminate_when", *req.TerminateWhen, sc)
	}

	v.checkOnStatus(path+".on_status", req.OnStatus, sc)

	seenStatus := map[int]bool{}
	for _, code := range req.ExpectStatus {
		if code < 100 || code > 599 {
			v.errorf(path+".expect_status", "status %d is out of HTTP range [100, 599]", code)
		}
		if seenStatus[code] {
			v.errorf(path+".expect_status", "status %d is duplicated", code)
		}
		seenStatus[code] = true
	}
}

func (v *validator) checkBody(path string, b Body, sc *scope) {
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
			v.checkValue(fmt.Sprintf("%s.json.%s", path, k), val, sc)
		}
	case b.Form != nil:
		for k, val := range b.Form {
			v.checkValue(fmt.Sprintf("%s.form.%s", path, k), val, sc)
		}
	case b.Raw != nil:
		v.checkValue(path+".raw", *b.Raw, sc)
	}
}

func (v *validator) checkFanOut(path string, f FanOut, sc *scope) {
	if f.Over.IsAbsent() {
		v.errorf(path+".over", "over is required (Value resolving to a list)")
	} else {
		v.checkValue(path+".over", f.Over, sc)
		v.checkListShapedValue(path+".over", "fan_out.over", f.Over)
	}
	if f.As == "" {
		v.errorf(path+".as", "as is required")
	} else {
		if _, reserved := pathClosedRoots[f.As]; reserved {
			v.errorf(path+".as",
				"as %q shadows a reserved namespace root (%s)",
				f.As, joinClosedRoots())
		}
		if _, conflict := sc.state[f.As]; conflict {
			v.errorf(path+".as", "as %q collides with a declared state field name", f.As)
		}
		if _, conflict := sc.stepIDs[f.As]; conflict {
			v.errorf(path+".as", "as %q collides with a request id", f.As)
		}
	}
	switch f.Merge {
	case "", "flatten", "wrap":
	default:
		v.errorf(path+".merge", "merge must be \"flatten\" or \"wrap\" (got %q)", f.Merge)
	}
}

func (v *validator) checkOnStatus(path string, m map[int]string, sc *scope) {
	for code, action := range m {
		op := fmt.Sprintf("%s.%d", path, code)
		if code < 100 || code > 599 {
			v.errorf(op, "status %d is out of HTTP range [100, 599]", code)
		}
		switch action {
		case "":
			v.errorf(op, "on_status action is required; want skip|fail|empty_events|invalidate_cache")
		case "skip", "fail", "empty_events":
		case "invalidate_cache":
			if !sc.hasCache {
				v.warnf(op, "invalidate_cache has no reachable cache block to drop; the runner degrades this status to empty_events at request time")
			}
		default:
			v.errorf(op, "unknown on_status action %q; want skip|fail|empty_events|invalidate_cache", action)
		}
	}
}

// ---- decode ----

// checkDecodeChain validates a per-step decode chain: non-empty, exactly
// one variant per stage, only byte-transforms (gzip/zip) before a single
// terminal decoder (csv/json/ndjson) as the last stage, plus per-stage args.
func (v *validator) checkDecodeChain(path string, c DecodeChain) {
	if len(c) == 0 {
		v.errorf(path, "decode is required (json|ndjson, or a list of stages ending in csv|json|ndjson)")
		return
	}
	for i, stage := range c {
		op := fmt.Sprintf("%s[%d]", path, i)
		names := stage.VariantNames()
		switch len(names) {
		case 0:
			v.errorf(op, "decode stage must have exactly one key (gzip|zip|csv|json|ndjson)")
			continue
		case 1:
		default:
			v.errorf(op, "decode stage must have exactly one key; found %v", names)
			continue
		}
		name := names[0]
		if i == len(c)-1 {
			if !isTerminalDecode(name) {
				v.errorf(op, "the last decode stage must be a terminal decoder (csv|json|ndjson), got %q", name)
			}
		} else if !isByteTransformDecode(name) {
			v.errorf(op, "only byte-transform stages (gzip|zip) may precede the terminal decoder, got %q", name)
		}

		switch {
		case stage.CSV != nil:
			switch stage.CSV.Header {
			case "present", "absent":
			case "":
				v.errorf(op+".header", "csv.header is required (present|absent)")
			default:
				v.errorf(op+".header", "csv.header must be present or absent, got %q", stage.CSV.Header)
			}
		case stage.Zip != nil:
			if stage.Zip.Glob != "" {
				if err := validGlob(stage.Zip.Glob); err != nil {
					v.errorf(op+".glob", "invalid glob %q: %v", stage.Zip.Glob, err)
				}
			}
		}
	}
}

// ---- pagination ----

func (v *validator) checkPagination(path string, p Pagination, sc *scope) {
	switch len(p.VariantNames()) {
	case 0:
		v.errorf(path, "pagination block must have exactly one variant (none|cursor_token|next_url|counter|custom)")
		return
	case 1:
	default:
		v.errorf(path, "pagination block must have exactly one variant; found %v", p.VariantNames())
		return
	}

	switch {
	case p.None != nil:
		// no further fields.
	case p.CursorToken != nil:
		c := p.CursorToken
		if c.From.IsEmpty() {
			v.errorf(path+".cursor_token.from", "from is required; want response.body.<path>, response.header.<name>, or steps.<id>.body.<path>")
		} else {
			v.checkPaginationFromPath(path+".cursor_token.from", c.From, sc)
		}
		// destination is checked in checkLifetimes.
		if c.TerminateWhen != nil {
			v.checkPredicate(path+".cursor_token.terminate_when", *c.TerminateWhen, sc)
		}

	case p.NextURL != nil:
		n := p.NextURL
		if n.From.IsEmpty() {
			v.errorf(path+".next_url.from", "from is required; want response.body.<path>, response.header.<name>, or steps.<id>.body.<path>")
		} else {
			v.checkPaginationFromPath(path+".next_url.from", n.From, sc)
		}
		if n.Capture < 0 {
			v.errorf(path+".next_url.capture", "capture must be a non-negative 1-based group index (got %d)", n.Capture)
		}
		if n.TerminateWhen != nil {
			v.checkPredicate(path+".next_url.terminate_when", *n.TerminateWhen, sc)
		}

	case p.Counter != nil:
		c := p.Counter
		if c.Start != nil {
			v.checkValue(path+".counter.start", *c.Start, sc)
		}
		if c.Step != nil {
			v.checkValue(path+".counter.step", *c.Step, sc)
		}
		if c.TerminateWhen != nil {
			v.checkPredicate(path+".counter.terminate_when", *c.TerminateWhen, sc)
		}

	case p.Custom != nil:
		c := p.Custom
		if len(c.Advance) == 0 {
			v.errorf(path+".custom.advance", "advance must contain at least one write")
		}
		for i, w := range c.Advance {
			wp := fmt.Sprintf("%s.custom.advance[%d]", path, i)
			if w.From.IsAbsent() {
				v.errorf(wp+".from", "from is required")
			} else {
				v.checkValue(wp+".from", w.From, sc)
			}
		}
		if c.TerminateWhen.IsZero() {
			v.errorf(path+".custom.terminate_when", "terminate_when is required for pagination.custom (no default predicate)")
		} else {
			v.checkPredicate(path+".custom.terminate_when", c.TerminateWhen, sc)
		}
	}
}

// ---- progress ----

func (v *validator) checkProgress(path string, p Progress, sc *scope) {
	for i, w := range p {
		wp := fmt.Sprintf("%s[%d]", path, i)
		// destination is checked in checkLifetimes.
		if w.From.IsAbsent() {
			v.errorf(wp+".from", "from is required")
		} else {
			v.checkValue(wp+".from", w.From, sc)
		}
	}
}

// ---- error ----

func (v *validator) checkError(path string, e ErrorBlock) {
	switch e.Mode {
	case "standard", "warn", "fail":
	case "":
		v.errorf(path+".mode", "mode is required (standard|warn|fail)")
	default:
		v.errorf(path+".mode", "unknown error mode %q; want standard|warn|fail", e.Mode)
	}
}

// ---- value checking ----

func (v *validator) checkValue(path string, val Value, sc *scope) {
	if val.IsZero || val.LiteralString != nil || val.LiteralInt != nil || val.LiteralBool != nil {
		return
	}

	switch {
	case val.Ref != nil:
		v.checkPathRef(path+".ref", val.Ref.Path, sc)
		if val.Ref.Default != nil {
			v.checkValue(path+".default", *val.Ref.Default, sc)
		}

	case val.Now != nil:
		// {now: true} has no operands.

	case len(val.Concat) > 0:
		for i, el := range val.Concat {
			v.checkValue(fmt.Sprintf("%s.concat[%d]", path, i), el, sc)
		}

	case val.Select != nil:
		for i, b := range val.Select.Branches {
			bp := fmt.Sprintf("%s.select.branches[%d]", path, i)
			v.checkPredicate(bp+".when", b.When, sc)
			v.checkValue(bp+".value", b.Value, sc)
		}
		v.checkValue(path+".select.default", val.Select.Default, sc)

	case val.Format != nil:
		if val.Format.Verb == "" {
			v.errorf(path+".format", "format verb is required")
		}
		v.checkValue(path+".value", val.Format.Value, sc)

	case val.Base64 != nil:
		v.checkValue(path+".base64", *val.Base64, sc)

	case val.List != nil:
		for i, el := range val.List {
			v.checkValue(fmt.Sprintf("%s.list[%d]", path, i), el, sc)
		}

	case val.Object != nil:
		for k, el := range val.Object {
			v.checkValue(fmt.Sprintf("%s.object.%s", path, k), el, sc)
		}

	case val.Add != nil:
		for i, op := range val.Add.Operands {
			v.checkValue(fmt.Sprintf("%s.add[%d]", path, i), op, sc)
		}

	case val.Subtract != nil:
		for i, op := range val.Subtract.Operands {
			v.checkValue(fmt.Sprintf("%s.subtract[%d]", path, i), op, sc)
		}

	case val.Max != nil:
		v.checkReducerOperand(path+".max", "max", *val.Max, sc)
	case val.Min != nil:
		v.checkReducerOperand(path+".min", "min", *val.Min, sc)
	case val.First != nil:
		v.checkReducerOperand(path+".first", "first", *val.First, sc)
	case val.Last != nil:
		v.checkReducerOperand(path+".last", "last", *val.Last, sc)
	case val.Count != nil:
		v.checkReducerOperand(path+".count", "count", *val.Count, sc)

	case val.Slice != nil:
		v.checkValue(path+".slice", val.Slice.Operand, sc)
		v.checkListShapedValue(path+".slice", "slice operand", val.Slice.Operand)

	case val.Regex != nil:
		if val.Regex.Pattern == "" {
			v.errorf(path+".regex.pattern", "pattern is required")
		}
		v.checkValue(path+".regex.from", val.Regex.From, sc)
		if val.Regex.Default != nil {
			v.checkValue(path+".regex.default", *val.Regex.Default, sc)
		}
		if val.Regex.Capture < 0 {
			v.errorf(path+".regex.capture", "capture must be a non-negative 1-based group index (got %d)", val.Regex.Capture)
		}
	}
}

// checkReducerOperand verifies that a reducer's operand is a Value that
// can resolve to a list. The list-literal sugar is normalised at parse
// time into a List form, so the operand reaches the validator as one of:
//   - List: literal sequence;
//   - Ref: list-shaped projection (typically events.*.<field>);
//   - Concat, Select: composite forms whose list-ness is decided at
//     evaluation time;
//   - anything else: scalar / non-list shape, rejected here.
func (v *validator) checkReducerOperand(path, verb string, op Value, sc *scope) {
	v.checkValue(path, op, sc)
	switch {
	case op.LiteralString != nil:
		v.errorf(path, "%s operand must resolve to a list (got literal_string)", verb)
	case op.LiteralInt != nil:
		v.errorf(path, "%s operand must resolve to a list (got literal_int)", verb)
	case op.LiteralBool != nil:
		v.errorf(path, "%s operand must resolve to a list (got literal_bool)", verb)
	case op.Now != nil:
		v.errorf(path, "%s operand must resolve to a list (got now)", verb)
	case op.Format != nil:
		v.errorf(path, "%s operand must resolve to a list (got format; no format verb produces a list)", verb)
	case op.Base64 != nil:
		v.errorf(path, "%s operand must resolve to a list (got base64; base64 produces a string)", verb)
	case op.Object != nil:
		v.errorf(path, "%s operand must resolve to a list (got object)", verb)
	case op.Add != nil, op.Subtract != nil:
		v.errorf(path, "%s operand must resolve to a list (got arithmetic; add/subtract resolve to scalars)", verb)
	case op.Max != nil, op.Min != nil, op.First != nil, op.Last != nil:
		v.errorf(path, "%s operand must resolve to a list (got %s; max/min/first/last resolve to scalars)", verb, op.VariantNames())
	case op.Count != nil:
		v.errorf(path, "%s operand must resolve to a list (got count; count resolves to an int)", verb)
	}
}

// checkListShapedValue rejects fan_out.over Values whose top-level form
// is obviously not list-typed. Composite forms (Ref, Concat, Select,
// List) defer to evaluation-time type resolution.
func (v *validator) checkListShapedValue(path, name string, val Value) {
	switch {
	case val.LiteralString != nil:
		v.errorf(path, "%s must resolve to a list (got literal_string — did you mean {ref: %s}?)", name, *val.LiteralString)
	case val.LiteralInt != nil:
		v.errorf(path, "%s must resolve to a list (got literal_int)", name)
	case val.LiteralBool != nil:
		v.errorf(path, "%s must resolve to a list (got literal_bool)", name)
	case val.Now != nil:
		v.errorf(path, "%s must resolve to a list (got now)", name)
	case val.Format != nil:
		v.errorf(path, "%s must resolve to a list (got format)", name)
	case val.Base64 != nil:
		v.errorf(path, "%s must resolve to a list (got base64)", name)
	case val.Object != nil:
		v.errorf(path, "%s must resolve to a list (got object)", name)
	case val.Add != nil, val.Subtract != nil:
		v.errorf(path, "%s must resolve to a list (got arithmetic)", name)
	case val.Max != nil, val.Min != nil, val.First != nil, val.Last != nil, val.Count != nil:
		v.errorf(path, "%s must resolve to a list (got reducer)", name)
	}
}

// ---- predicate checking ----

func (v *validator) checkPredicate(path string, p Predicate, sc *scope) {
	if p.IsZero() {
		v.errorf(path, "predicate must not be empty")
		return
	}
	switch {
	case p.Eq != nil:
		v.checkPathRef(path+".eq.path", p.Eq.Path, sc)
		v.checkValue(path+".eq.value", p.Eq.Value, sc)
	case p.Gt != nil:
		v.checkPathRef(path+".gt.path", p.Gt.Path, sc)
		v.checkValue(path+".gt.value", p.Gt.Value, sc)
	case p.Lt != nil:
		v.checkPathRef(path+".lt.path", p.Lt.Path, sc)
		v.checkValue(path+".lt.value", p.Lt.Value, sc)
	case p.Gte != nil:
		v.checkPathRef(path+".gte.path", p.Gte.Path, sc)
		v.checkValue(path+".gte.value", p.Gte.Value, sc)
	case p.Lte != nil:
		v.checkPathRef(path+".lte.path", p.Lte.Path, sc)
		v.checkValue(path+".lte.value", p.Lte.Value, sc)
	case p.Present != nil:
		v.checkPathRef(path+".present", *p.Present, sc)
	case len(p.And) > 0:
		for i, sub := range p.And {
			v.checkPredicate(fmt.Sprintf("%s.and[%d]", path, i), sub, sc)
		}
	case len(p.Or) > 0:
		for i, sub := range p.Or {
			v.checkPredicate(fmt.Sprintf("%s.or[%d]", path, i), sub, sc)
		}
	case p.Not != nil:
		v.checkPredicate(path+".not", *p.Not, sc)
	case p.LiteralBool != nil:
		// constant; no operands to check.
	}
}

// ---- path reference checking ----

// checkPathRef validates a ref-position Path against the active scope.
// The closed root set is shared with the parser; the validator binds
// the contextual semantics (state field declared, step id declared,
// extract name in scope, cache slot declared, fan-out alias matches the
// current step, events.* segment shape).
func (v *validator) checkPathRef(path string, p Path, sc *scope) {
	if p.IsEmpty() {
		v.errorf(path, "ref path is required")
		return
	}
	root := p.Parts[0]
	if sc.fanOutAlias != "" && root == sc.fanOutAlias {
		v.checkNoStarOutsideEvents(path, p)
		return
	}
	switch root {
	case "state":
		v.checkStateRef(path, p, sc)
	case "cache":
		v.checkCacheRef(path, p, sc)
	case "events":
		v.checkEventsRef(path, p)
	case "extract":
		v.checkExtractRef(path, p, sc)
	case "steps":
		v.checkStepsRef(path, p, sc)
	case "response":
		v.checkResponseRef(path, p)
	default:
		v.errorf(path, "ref %q: unknown namespace root %q; want %s (or a fan_out.as alias active in scope)",
			p.String(), root, joinClosedRoots())
	}
}

func (v *validator) checkStateRef(path string, p Path, sc *scope) {
	if len(p.Parts) < 2 {
		v.errorf(path, "state ref requires a field name: state.<name>")
		return
	}
	name := p.Parts[1]
	if _, ok := sc.state[name]; !ok {
		v.errorf(path, "ref %q: state field %q is not declared under state:", p.String(), name)
	}
	v.checkNoStarOutsideEvents(path, p)
}

func (v *validator) checkCacheRef(path string, p Path, sc *scope) {
	if len(p.Parts) < 2 {
		v.errorf(path, "cache ref requires a slot name: cache.<name>")
		return
	}
	name := p.Parts[1]
	if _, ok := sc.cacheSlots[name]; !ok {
		v.errorf(path, "ref %q: cache slot %q is not declared by any cache: block", p.String(), name)
	}
	v.checkNoStarOutsideEvents(path, p)
}

// checkEventsRef enforces the events.* projection vocabulary:
//
//   - events.count                — cardinality of the active page.
//   - events.first.<field>        — first event in declared order.
//   - events.last.<field>         — last event in declared order.
//   - events.<int>[.field]        — positional access (zero-based).
//   - events.*[.field]            — projection across every event.
//
// The `*` segment is legal only at position 1 of an events-rooted path.
func (v *validator) checkEventsRef(path string, p Path) {
	if len(p.Parts) < 2 {
		v.errorf(path, "events ref requires a sub-segment: events.first.<field> | events.last.<field> | events.<int>.<field> | events.count | events.*.<field>")
		return
	}
	seg := p.Parts[1]
	switch seg {
	case "first", "last":
		// events.first and events.last may be referenced without a
		// trailing field — that resolves to the whole event.
	case "count":
		if len(p.Parts) != 2 {
			v.errorf(path, "events.count has no sub-path (got %q)", p.String())
		}
	case "*":
		// events.* is the projection; sub-segments are field selectors.
	default:
		if _, err := strconv.Atoi(seg); err != nil {
			v.errorf(path, "events.<int> segment %q is not a non-negative integer literal", seg)
		} else if strings.HasPrefix(seg, "-") {
			v.errorf(path, "events.<int> segment %q must be non-negative", seg)
		}
	}
	// Disallow `*` at any later position in an events-rooted path.
	for i := 2; i < len(p.Parts); i++ {
		if p.Parts[i] == "*" {
			v.errorf(path, "ref %q: the * projection segment is only legal at position 1 of an events ref (events.*.<field>)", p.String())
			return
		}
	}
}

func (v *validator) checkExtractRef(path string, p Path, sc *scope) {
	if len(p.Parts) < 2 {
		v.errorf(path, "extract ref requires a name: extract.<name>")
		return
	}
	name := p.Parts[1]
	if _, ok := sc.extract[name]; !ok {
		v.errorf(path, "ref %q: extract %q is not in scope at this point in the request chain", p.String(), name)
	}
	v.checkNoStarOutsideEvents(path, p)
}

func (v *validator) checkStepsRef(path string, p Path, sc *scope) {
	if len(p.Parts) < 2 {
		v.errorf(path, "steps ref requires an id: steps.<id>.body.<path> or steps.<id>.header.<name>")
		return
	}
	id := p.Parts[1]
	if _, ok := sc.stepIDs[id]; !ok {
		v.errorf(path, "ref %q: step %q is not declared by any requests[].id", p.String(), id)
		return
	}
	if len(p.Parts) < 3 {
		v.errorf(path, "ref %q: steps ref requires a kind segment: steps.%s.body.<path> or steps.%s.header.<name>", p.String(), id, id)
		return
	}
	switch p.Parts[2] {
	case "body":
		// further segments are body field accessors.
	case "header":
		if len(p.Parts) < 4 {
			v.errorf(path, "ref %q: steps.<id>.header requires a header name", p.String())
		}
	default:
		v.errorf(path, "ref %q: steps third segment must be \"body\" or \"header\" (got %q)", p.String(), p.Parts[2])
	}
	v.checkNoStarOutsideEvents(path, p)
}

func (v *validator) checkResponseRef(path string, p Path) {
	if len(p.Parts) < 2 {
		v.errorf(path, "response ref requires a kind segment: response.body.<path> or response.header.<name>")
		return
	}
	switch p.Parts[1] {
	case "body":
		// further segments are body field accessors; no further check.
	case "header":
		if len(p.Parts) < 3 {
			v.errorf(path, "response.header requires a header name: response.header.<name>")
		}
	default:
		v.errorf(path, "response second segment must be \"body\" or \"header\" (got %q)", p.Parts[1])
	}
	v.checkNoStarOutsideEvents(path, p)
}

// checkNoStarOutsideEvents rejects a `*` segment in any non-events path.
// The events projection is the only site where `*` is meaningful.
func (v *validator) checkNoStarOutsideEvents(path string, p Path) {
	if slices.Contains(p.Parts, "*") {
		v.errorf(path, "ref %q: the * projection segment is only legal in events.*.<field>", p.String())
		return
	}
}

// ---- body-rooted path slot variants ----

// checkEventsAtPath validates a request's events_at. Accepted forms:
//
//   - empty Path (body root IS the events list);
//   - response.body.<path>;
//   - steps.<id>.body.<path>.
//
// Header roots are not accepted here — the events list lives in the
// body. The validator binds `steps.<id>` to a declared request id.
func (v *validator) checkEventsAtPath(path string, p Path, sc *scope) {
	if p.IsEmpty() {
		return
	}
	switch p.Parts[0] {
	case "response":
		if len(p.Parts) < 2 || p.Parts[1] != "body" {
			v.errorf(path, "events_at %q: at this slot the response root must be response.body.<path>", p.String())
			return
		}
		if len(p.Parts) < 3 {
			v.errorf(path, "events_at %q: response.body root requires a sub-path segment", p.String())
		}
	case "steps":
		if len(p.Parts) < 2 {
			v.errorf(path, "events_at %q: steps ref requires an id: steps.<id>.body.<path>", p.String())
			return
		}
		id := p.Parts[1]
		if _, ok := sc.stepIDs[id]; !ok {
			v.errorf(path, "events_at %q: step %q is not declared by any requests[].id", p.String(), id)
			return
		}
		if len(p.Parts) < 3 || p.Parts[2] != "body" {
			v.errorf(path, "events_at %q: at this slot the steps ref must be steps.<id>.body.<path>", p.String())
			return
		}
		if len(p.Parts) < 4 {
			v.errorf(path, "events_at %q: steps.<id>.body root requires a sub-path segment", p.String())
		}
	default:
		v.errorf(path, "events_at %q must be rooted at response.body.<path> or steps.<id>.body.<path>", p.String())
	}
}

// checkPaginationFromPath validates the from slot of pagination
// cursor_token / next_url. Accepted forms:
//
//   - response.body.<path>;
//   - response.header.<name>;
//   - steps.<id>.body.<path>.
//
// steps.<id>.header.<name> is intentionally not accepted here; a step
// that exposes header-only data uses an extract: write to land the
// header value into a state field first.
func (v *validator) checkPaginationFromPath(path string, p Path, sc *scope) {
	switch p.Parts[0] {
	case "response":
		v.checkResponseRef(path, p)
	case "steps":
		if len(p.Parts) < 2 {
			v.errorf(path, "from %q: steps ref requires an id", p.String())
			return
		}
		id := p.Parts[1]
		if _, ok := sc.stepIDs[id]; !ok {
			v.errorf(path, "from %q: step %q is not declared by any requests[].id", p.String(), id)
			return
		}
		if len(p.Parts) < 3 || p.Parts[2] != "body" {
			v.errorf(path, "from %q: at this slot the steps ref must be steps.<id>.body.<path>", p.String())
			return
		}
		if len(p.Parts) < 4 {
			v.errorf(path, "from %q: steps.<id>.body root requires a sub-path segment", p.String())
		}
	default:
		v.errorf(path, "from %q must be rooted at response.body.<path>, response.header.<name>, or steps.<id>.body.<path>", p.String())
	}
}

// checkExtractFromPath validates requests[].extract[].from. Accepted forms:
//
//   - response.body.<path>;
//   - response.header.<name>;
//   - steps.<id>.body.<path>;
//   - steps.<id>.header.<name>.
func (v *validator) checkExtractFromPath(path string, p Path, sc *scope) {
	switch p.Parts[0] {
	case "response":
		v.checkResponseRef(path, p)
	case "steps":
		v.checkStepsRef(path, p, sc)
	default:
		v.errorf(path, "from %q must be rooted at response.body.<path>, response.header.<name>, or steps.<id>.{body|header}.<...>", p.String())
	}
}

// ---- helpers ----

func validStateType(t string) bool {
	switch t {
	case "string", "int", "bool", "secret", "duration", "timestamp", "url", "enum":
		return true
	}
	return false
}

// joinClosedRoots returns the bare list of closed namespace roots —
// pipe-separated for readability inside parenthetical clauses. Use this
// only in error messages whose surrounding text already qualifies the
// fan_out.as alias case separately (or where mentioning the alias would
// be wrong, e.g. when complaining about a fan_out.as itself). For
// self-contained parse-time diagnostics, use pathLegalRootsList in
// path.go.
func joinClosedRoots() string {
	return "state | cache | events | extract | steps | response"
}
