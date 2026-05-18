// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/p1llus/skopos/schema"
)

// The unified Cache runtime lives here. The same schema.Cache shape backs
// two call sites:
//
//   - auth.oauth2.<grant>.cache — wraps the OAuth2 token endpoint exchange
//     in a fresh-vs-cached conditional. The slot stores the access token
//     string; subsequent requests pick it up via the OAuth2 dispatcher's
//     Authorization-header write.
//
//   - requests[].cache — wraps a token-style step (a custom JSON login, a
//     session-key exchange, etc.) in a fresh-vs-cached conditional so the
//     login round trip is not paid on every iteration. The slot stores the
//     decoded response body; subsequent steps walk into it via
//     {ref: cache.<name>.<path>}.
//
// Cache slots live in scope.cache (process memory only). They are NEVER
// persisted: a runner restart re-fetches. The Cache struct's expires_at
// Value is evaluated against the just-decoded body at write time and the
// resolved instant lives alongside the value as bookkeeping; on read,
// now() + buffer >= expires_at re-runs the underlying step.

// cacheExpiryKey returns the scope.cache key paired with slot and holding
// the slot's resolved expires_at instant. The "__exp_" prefix is reserved:
// the validator only allows cache.<name> refs where <name> appears in some
// Cache.To Path, so authors never reach this slot from the IR.
func cacheExpiryKey(slot string) string {
	return "__exp_" + slot
}

// cacheGet returns the value cached at c.To and whether the slot is still
// fresh. A miss happens when the slot is unpopulated, its paired expiry is
// missing or malformed, or now() + c.Buffer has reached (or passed) the
// stored expiry instant. Callers then re-run the underlying step.
func (s *scope) cacheGet(c *schema.Cache) (any, bool) {
	slot, ok := cacheSlotName(c.To)
	if !ok {
		return nil, false
	}
	val, ok := s.cache[slot]
	if !ok {
		return nil, false
	}
	expRaw, ok := s.cache[cacheExpiryKey(slot)]
	if !ok {
		return nil, false
	}
	expAt, ok := expRaw.(time.Time)
	if !ok {
		return nil, false
	}
	buffer, err := time.ParseDuration(c.Buffer)
	if err != nil {
		return nil, false
	}
	if !s.now().Add(buffer).Before(expAt) {
		return nil, false
	}
	return val, true
}

// cacheStore evaluates c.ExpiresAt against the currently bound scope.body
// (the just-decoded response of the cached step) and writes value alongside
// the resolved instant into the slot named by c.To. The caller is
// responsible for binding scope.body before calling and restoring it
// afterwards so the cache write does not leak the cached body into
// downstream predicate / extract evaluations that expect a fresh
// per-request bind.
func (s *scope) cacheStore(c *schema.Cache, value any) error {
	slot, ok := cacheSlotName(c.To)
	if !ok {
		return fmt.Errorf("cache.to is not a cache.<name> path: %s", c.To)
	}
	rawExp, err := s.evalValue(c.ExpiresAt)
	if err != nil {
		return fmt.Errorf("cache.expires_at: %w", err)
	}
	expAt, err := coerceCacheExpiry(rawExp, s.now())
	if err != nil {
		return fmt.Errorf("cache.expires_at: %w", err)
	}
	s.cache[slot] = value
	s.cache[cacheExpiryKey(slot)] = expAt
	return nil
}

// coerceCacheExpiry interprets a Value-resolved expires_at into an absolute
// instant. Accepted shapes:
//
//   - time.Time:        as-is.
//   - time.Duration:    now + d.
//   - int / int64 / float64: treated as a duration in seconds; now + d.
//     Matches the canonical OAuth2 "expires_in: 3600" form (RFC 6749
//     §4.4.3).
//   - string:           parsed as Go duration first ("1h", "3600s"), then
//     as RFC 3339 (nano then plain), then as a plain
//     integer (seconds). Falls through to an error when
//     none of the parses succeed.
//
// Absolute timestamps that come off the wire as integers (Unix seconds /
// millis) can be promoted to time.Time with {format: rfc3339nano,
// value: ...} or {format: unix_seconds, value: ...} in the Value layer
// before reaching here.
func coerceCacheExpiry(v any, now time.Time) (time.Time, error) {
	switch x := v.(type) {
	case time.Time:
		return x, nil
	case time.Duration:
		return now.Add(x), nil
	case int:
		return now.Add(time.Duration(x) * time.Second), nil
	case int64:
		return now.Add(time.Duration(x) * time.Second), nil
	case float64:
		return now.Add(time.Duration(x * float64(time.Second))), nil
	case string:
		if d, err := time.ParseDuration(x); err == nil {
			return now.Add(d), nil
		}
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return t, nil
		}
		if t, err := time.Parse(time.RFC3339, x); err == nil {
			return t, nil
		}
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return now.Add(time.Duration(n) * time.Second), nil
		}
		return time.Time{}, fmt.Errorf("cannot interpret %d-byte string as time, duration, or seconds", len(x))
	}
	return time.Time{}, fmt.Errorf("cannot interpret %T as time", v)
}

// applyOAuth2 sets "Authorization: Bearer <access_token>" on req. When the
// active grant has a cache block, a fresh slot is reused as-is; on a miss
// (or stale slot) the token endpoint is fetched and the slot rewritten.
// Grants without a cache block fetch a fresh token on every request.
//
// Token-endpoint exchanges share Runner.Client with the IR-described
// requests, so proxy / TLS / transport policy applies uniformly. They are
// NOT routed through Runner.Tracer: the trace surface is the IR's wire
// view, and the token fetch is a framework-internal sub-fetch with no IR
// step id. Errors flow through redactURLError so client_id / client_secret
// routed into the form body cannot leak via a transport *url.Error; the
// token response body never appears in error strings — failures report the
// status code and a content-class classification only.
func (s *scope) applyOAuth2(ctx context.Context, client *http.Client, req *http.Request, o *schema.OAuth2Auth) error {
	var g oauth2Grant
	switch {
	case o.ClientCredentials != nil:
		g = oauth2Grant{cc: o.ClientCredentials}
	case o.PasswordGrant != nil:
		g = oauth2Grant{pg: o.PasswordGrant}
	default:
		return fmt.Errorf("auth.oauth2: no grant variant set")
	}

	tok, err := s.oauth2Token(ctx, client, g)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// oauth2Grant is a tagged wrapper so the cache + fetch helpers can be
// shared across grant types. Exactly one field is non-nil per the IR
// validator. Keeping the wrapper struct-shaped (not an interface) avoids
// reflection / type-switching at every cache lookup.
type oauth2Grant struct {
	cc *schema.ClientCredentialsGrant
	pg *schema.PasswordGrant
}

// cache returns the grant's Cache block (or nil when the grant declares no
// cache block — the token is then fetched on every request).
func (g oauth2Grant) cache() *schema.Cache {
	switch {
	case g.cc != nil:
		return g.cc.Cache
	case g.pg != nil:
		return g.pg.Cache
	}
	return nil
}

// oauth2Token returns a fresh-or-cached access token for grant. When the
// grant has no cache block, every call fetches a new token. When the grant
// has a cache block, a cached value is reused until now() + cache.Buffer
// reaches the stored expires_at instant, then a fresh fetch overwrites the
// slot.
func (s *scope) oauth2Token(ctx context.Context, client *http.Client, grant oauth2Grant) (string, error) {
	cache := grant.cache()
	if cache != nil {
		if v, ok := s.cacheGet(cache); ok {
			if tok, isStr := v.(string); isStr && tok != "" {
				return tok, nil
			}
		}
	}
	tok, body, err := s.fetchOAuth2Token(ctx, client, grant)
	if err != nil {
		return "", err
	}
	if cache != nil {
		// Bind scope.body to the token endpoint's response so the cache's
		// expires_at Value (typically {ref: response.body.expires_in,
		// default: "1h"}) resolves against the just-decoded token body.
		// Restore on the way out so a downstream extract / predicate is
		// not handed the token endpoint body by accident.
		prev := s.body
		s.body = body
		storeErr := s.cacheStore(cache, tok)
		s.body = prev
		if storeErr != nil {
			return "", fmt.Errorf("auth.oauth2 cache: %w", storeErr)
		}
	}
	return tok, nil
}

// fetchOAuth2Token exchanges client/user credentials at grant.token_url for
// a fresh access token. Returns the token string and the decoded response
// body so the caller can bind it to scope.body for the cache.expires_at
// evaluation.
//
// The function builds the form-urlencoded body from the grant variant and
// sets "Authorization: Basic <client_id:client_secret>" per RFC 6749
// §2.3.1 (the "credentials in the request body" alternative is offered
// too but the Basic form is interoperable across more servers). For
// password_grant with no client_id, the Basic header is omitted and the
// server is expected to authenticate by other means (most commonly: trust
// the user credentials in the body).
func (s *scope) fetchOAuth2Token(ctx context.Context, client *http.Client, grant oauth2Grant) (string, map[string]any, error) {
	tokenURL, basicUser, basicPass, form, err := s.buildOAuth2TokenRequest(grant)
	if err != nil {
		return "", nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", nil, fmt.Errorf("auth.oauth2: build token request: %w", redactURLError(err))
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")
	if basicUser != "" || basicPass != "" {
		pair := basicUser + ":" + basicPass
		httpReq.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(pair)))
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("auth.oauth2: token endpoint: %w", redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("auth.oauth2: read token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Body classification only — a non-2xx response from a token
		// endpoint frequently carries the rejected credential or a
		// privileged diagnostic; the bytes never make it into the error
		// string.
		return "", nil, fmt.Errorf("auth.oauth2: token endpoint returned status %d (%s)", resp.StatusCode, bodyMetadata(raw))
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", nil, fmt.Errorf("auth.oauth2: decode token response: %w (%s)", err, bodyMetadata(raw))
	}

	accessTokenRaw, ok := body["access_token"]
	if !ok {
		return "", nil, fmt.Errorf("auth.oauth2: token response missing access_token field (%s)", bodyMetadata(raw))
	}
	accessToken, ok := accessTokenRaw.(string)
	if !ok || accessToken == "" {
		return "", nil, fmt.Errorf("auth.oauth2: token response access_token is empty or non-string")
	}

	return accessToken, body, nil
}

// buildOAuth2TokenRequest assembles the wire details for the token POST:
// the resolved token_url, the basic-auth pair (when client credentials are
// present), and the form-urlencoded request body. Returning the parts
// separately keeps fetchOAuth2Token's responsibility narrow (it composes
// + sends + decodes).
func (s *scope) buildOAuth2TokenRequest(grant oauth2Grant) (string, string, string, url.Values, error) {
	form := url.Values{}
	var (
		tokenURLRaw any
		basicUser   string
		basicPass   string
		err         error
	)
	switch {
	case grant.cc != nil:
		cc := grant.cc
		tokenURLRaw, err = s.evalValue(cc.TokenURL)
		if err != nil {
			return "", "", "", nil, fmt.Errorf("auth.oauth2.client_credentials.token_url: %w", err)
		}
		uid, err := s.evalValue(cc.ClientID)
		if err != nil {
			return "", "", "", nil, fmt.Errorf("auth.oauth2.client_credentials.client_id: %w", err)
		}
		secret, err := s.evalValue(cc.ClientSecret)
		if err != nil {
			return "", "", "", nil, fmt.Errorf("auth.oauth2.client_credentials.client_secret: %w", err)
		}
		basicUser = toString(uid)
		basicPass = toString(secret)
		form.Set("grant_type", "client_credentials")
		if len(cc.Scopes) > 0 {
			form.Set("scope", strings.Join(cc.Scopes, " "))
		}
		if cc.Audience != "" {
			form.Set("audience", cc.Audience)
		}

	case grant.pg != nil:
		pg := grant.pg
		tokenURLRaw, err = s.evalValue(pg.TokenURL)
		if err != nil {
			return "", "", "", nil, fmt.Errorf("auth.oauth2.password_grant.token_url: %w", err)
		}
		user, err := s.evalValue(pg.Username)
		if err != nil {
			return "", "", "", nil, fmt.Errorf("auth.oauth2.password_grant.username: %w", err)
		}
		pass, err := s.evalValue(pg.Password)
		if err != nil {
			return "", "", "", nil, fmt.Errorf("auth.oauth2.password_grant.password: %w", err)
		}
		form.Set("grant_type", "password")
		form.Set("username", toString(user))
		form.Set("password", toString(pass))
		if pg.ClientID != nil {
			cid, err := s.evalValue(*pg.ClientID)
			if err != nil {
				return "", "", "", nil, fmt.Errorf("auth.oauth2.password_grant.client_id: %w", err)
			}
			// When a client_id is supplied, send it in the form body — the
			// public-client form of RFC 6749 §4.3.2. Servers that require
			// Basic auth on the token endpoint can still accept the Basic
			// form independently via another auth variant on an external
			// flow; the password_grant variant does not currently model
			// optional client_secret.
			form.Set("client_id", toString(cid))
		}
		if len(pg.Scopes) > 0 {
			form.Set("scope", strings.Join(pg.Scopes, " "))
		}

	default:
		return "", "", "", nil, fmt.Errorf("auth.oauth2: no grant variant set")
	}

	tokenURL := toString(tokenURLRaw)
	if tokenURL == "" {
		return "", "", "", nil, fmt.Errorf("auth.oauth2: token_url resolved to empty string")
	}
	return tokenURL, basicUser, basicPass, form, nil
}
