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
	"strings"
	"time"

	"github.com/p1llus/skopos/schema"
)

// applyOAuth2 sets "Authorization: Bearer <access_token>" on req, refreshing
// the cached token if its remaining lifetime has dropped below the grant's
// expiry_buffer (or fetching a fresh token unconditionally when the grant
// has no cache block).
//
// Supported grants: client_credentials and password_grant, both sharing the
// <grant>.cache block (state.<store_in>; expiry tracked in
// cursor.__oauth2_<store_in>_expires_at).
//
// The token endpoint is fetched via the same *http.Client the runner uses
// for the IR-described requests, so timeouts, transports, and proxies all
// flow through. Token-endpoint exchanges are NOT routed through the
// per-exchange Tracer: the trace surface is the IR's wire view, and the
// token fetch is a framework-internal sub-fetch with no IR step id.
//
// All token-fetch errors flow through redactURLError so client_id /
// client_secret routed into the form body cannot leak into a transport
// *url.Error; the access_token response body never appears in errors —
// failures report the response status + content classification only.
func (s *scope) applyOAuth2(ctx context.Context, client *http.Client, req *http.Request, o *schema.OAuth2Auth) error {
	switch {
	case o.ClientCredentials != nil:
		tok, err := s.oauth2Token(ctx, client, oauth2Grant{cc: o.ClientCredentials})
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return nil

	case o.PasswordGrant != nil:
		tok, err := s.oauth2Token(ctx, client, oauth2Grant{pg: o.PasswordGrant})
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return nil
	}
	return fmt.Errorf("auth.oauth2: no grant variant set")
}

// oauth2Grant is a tagged wrapper so the cache + fetch helpers can be
// shared across grant types. Exactly one field is non-nil per the IR
// validator. Keeping the wrapper struct-shaped (not an interface) avoids
// reflection / type-switching at every cache lookup.
type oauth2Grant struct {
	cc *schema.ClientCredentialsGrant
	pg *schema.PasswordGrant
}

// cache returns the grant's TokenCache (or nil when the grant declares no
// cache block — the token is then fetched on every request).
func (g oauth2Grant) cache() *schema.TokenCache {
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
// has a cache block, a cached value is reused until its remaining lifetime
// drops below cache.expiry_buffer, then a fresh fetch overwrites the slot.
//
// The cached token lives at state.<cache.store_in> (auto-registered as a
// runtime string by the IR validator). The cached expiry timestamp lives
// at cursor.__oauth2_<store_in>_expires_at as an RFC 3339 string. Two
// slots, two persistence layers — both ride through the same deferred
// store.Save in Runner.Drain. We do not stuff a structured value into the
// state slot because the IR spec declares it a "string" type slot, and
// downstream authors may legitimately {ref: state.<store_in>} as a string.
func (s *scope) oauth2Token(ctx context.Context, client *http.Client, grant oauth2Grant) (string, error) {
	cache := grant.cache()
	if cache != nil {
		if tok, ok := s.cachedOAuth2Token(cache); ok {
			return tok, nil
		}
	}
	tok, lifetime, err := s.fetchOAuth2Token(ctx, client, grant)
	if err != nil {
		return "", err
	}
	if cache != nil {
		s.storeOAuth2Token(cache, tok, lifetime)
	}
	return tok, nil
}

// cachedOAuth2Token returns the cached access token if state.<store_in>
// carries one AND its remaining lifetime exceeds cache.expiry_buffer.
// Returns ("", false) otherwise — the caller then issues a fresh fetch.
func (s *scope) cachedOAuth2Token(cache *schema.TokenCache) (string, bool) {
	rawTok, ok := s.state[cache.StoreIn]
	if !ok {
		return "", false
	}
	tok, ok := rawTok.(string)
	if !ok || tok == "" {
		return "", false
	}
	expRaw, ok := s.cursor[oauth2ExpiryKey(cache.StoreIn)]
	if !ok {
		return "", false
	}
	expStr, ok := expRaw.(string)
	if !ok || expStr == "" {
		return "", false
	}
	expAt, err := time.Parse(time.RFC3339Nano, expStr)
	if err != nil {
		expAt, err = time.Parse(time.RFC3339, expStr)
		if err != nil {
			return "", false
		}
	}
	buffer, err := time.ParseDuration(cache.ExpiryBuffer)
	if err != nil {
		// Validator rejects an unparseable buffer; defensive fall-through.
		return "", false
	}
	if !s.now().Add(buffer).Before(expAt) {
		return "", false
	}
	return tok, true
}

// storeOAuth2Token writes the freshly-fetched token + expiry into the
// cache slots. lifetime is the remaining lifetime parsed out of the
// token-endpoint response body (cache.expiry_field).
func (s *scope) storeOAuth2Token(cache *schema.TokenCache, token string, lifetime time.Duration) {
	s.state[cache.StoreIn] = token
	s.cursor[oauth2ExpiryKey(cache.StoreIn)] = s.now().Add(lifetime).UTC().Format(time.RFC3339Nano)
}

// oauth2ExpiryKey returns the cursor key paired with state.<storeIn> for
// holding the access token's expiry timestamp.
func oauth2ExpiryKey(storeIn string) string {
	return "__oauth2_" + storeIn + "_expires_at"
}

// invalidateAuthCaches clears every OAuth2 token-cache slot reachable from
// auth (state.<store_in> + cursor.__oauth2_<store_in>_expires_at) and
// returns the store_in names that were cleared. Backs the runtime
// `on_status: invalidate_cache` verb (runner.go) — after the verb fires,
// the next request misses the cache and forces a fresh token fetch.
//
// multi_mode dispatches recursively into every branch (plus the default
// arm); the validator forbids nested multi_mode so the recursion is at
// most one hop deep. Clearing every reachable slot is intentional: the
// failed exchange already rejected the cached credential, and the next
// drain only refetches whichever arm its predicates select — clearing
// the unfired arms costs at most one extra token fetch if their
// predicate flips, never an incorrect request.
//
// Auth variants without a cache block (or non-OAuth2 variants entirely)
// are no-ops; the caller logs accordingly.
func (s *scope) invalidateAuthCaches(auth schema.Auth) []string {
	var cleared []string
	switch {
	case auth.OAuth2 != nil:
		if c := oauth2CacheFor(auth.OAuth2); c != nil {
			s.clearOAuth2Cache(c)
			cleared = append(cleared, c.StoreIn)
		}
	case auth.MultiMode != nil:
		for _, b := range auth.MultiMode.Branches {
			cleared = append(cleared, s.invalidateAuthCaches(b.Auth)...)
		}
		cleared = append(cleared, s.invalidateAuthCaches(auth.MultiMode.Default.Auth)...)
	}
	return cleared
}

// oauth2CacheFor returns the TokenCache for whichever grant variant is set,
// or nil if the grant declares no cache block. Keeping the lookup helper
// separate from oauth2Grant.cache() lets invalidateAuthCaches operate on
// the raw schema.OAuth2Auth value without first constructing a tagged wrapper.
func oauth2CacheFor(o *schema.OAuth2Auth) *schema.TokenCache {
	switch {
	case o.ClientCredentials != nil:
		return o.ClientCredentials.Cache
	case o.PasswordGrant != nil:
		return o.PasswordGrant.Cache
	}
	return nil
}

// clearOAuth2Cache removes the access token and its paired expiry timestamp
// from the scope's state + cursor maps. snapshot() persists only keys that
// exist in s.state / s.cursor, so the deletion round-trips cleanly through
// the deferred Save: the next drain seeds with empty slots and the
// cachedOAuth2Token miss path fires a fresh fetch.
func (s *scope) clearOAuth2Cache(cache *schema.TokenCache) {
	delete(s.state, cache.StoreIn)
	delete(s.cursor, oauth2ExpiryKey(cache.StoreIn))
}

// fetchOAuth2Token exchanges client/user credentials at grant.token_url for
// a fresh access token. Returns the token string + remaining lifetime
// parsed out of the response body at cache.expiry_field (when the grant
// has a cache block) or RFC 6749's default "expires_in" path otherwise.
//
// The function builds the form-urlencoded body from the grant variant,
// sets "Authorization: Basic <client_id:client_secret>" per RFC 6749 §2.3.1
// (the "credentials in the request body" alternative is offered too but
// the Basic form is interoperable across more servers — including the
// skopos testserver). For password_grant with no client_id, the Basic
// header is omitted and the server is expected to authenticate by other
// means (most commonly: trust the user credentials in the body).
func (s *scope) fetchOAuth2Token(ctx context.Context, client *http.Client, grant oauth2Grant) (string, time.Duration, error) {
	tokenURL, basicUser, basicPass, form, err := s.buildOAuth2TokenRequest(grant)
	if err != nil {
		return "", 0, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("auth.oauth2: build token request: %w", redactURLError(err))
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")
	if basicUser != "" || basicPass != "" {
		pair := basicUser + ":" + basicPass
		httpReq.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(pair)))
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", 0, fmt.Errorf("auth.oauth2: token endpoint: %w", redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("auth.oauth2: read token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Body classification only — a non-2xx response from a token endpoint
		// frequently carries the rejected credential or a privileged
		// diagnostic; the bytes never make it into the error string.
		return "", 0, fmt.Errorf("auth.oauth2: token endpoint returned status %d (%s)", resp.StatusCode, bodyMetadata(raw))
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", 0, fmt.Errorf("auth.oauth2: decode token response: %w (%s)", err, bodyMetadata(raw))
	}

	accessTokenRaw, ok := body["access_token"]
	if !ok {
		return "", 0, fmt.Errorf("auth.oauth2: token response missing access_token field (%s)", bodyMetadata(raw))
	}
	accessToken, ok := accessTokenRaw.(string)
	if !ok || accessToken == "" {
		return "", 0, fmt.Errorf("auth.oauth2: token response access_token is empty or non-string")
	}

	lifetime, err := extractOAuth2Lifetime(body, grant.cache())
	if err != nil {
		return "", 0, fmt.Errorf("auth.oauth2: %w", err)
	}
	return accessToken, lifetime, nil
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
			// Basic auth on the token endpoint will accept the Basic form
			// independently via auth.bearer / auth.basic on an external
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

// extractOAuth2Lifetime parses the remaining token lifetime from the
// token endpoint's response body. When the grant has a cache block, the
// cache.expiry_field Path is followed; otherwise the well-known
// "expires_in" key (RFC 6749 §4.4.3) is used.
//
// The located value is coerced through coerceLifetime (shared with the
// requests[].cache step path): integer seconds, a stringified integer, or
// a Go duration string all decode.
func extractOAuth2Lifetime(body map[string]any, cache *schema.TokenCache) (time.Duration, error) {
	var raw any
	var ok bool
	if cache != nil && !cache.ExpiryField.IsEmpty() {
		val, found, err := lookupBodyPath(body, cache.ExpiryField.Parts)
		if err != nil {
			return 0, fmt.Errorf("expiry_field: %w", err)
		}
		if !found {
			return 0, fmt.Errorf("expiry_field %s missing from token response", cache.ExpiryField)
		}
		raw = val
	} else {
		raw, ok = body["expires_in"]
		if !ok {
			return 0, fmt.Errorf("token response missing expires_in field")
		}
	}

	return coerceLifetime(raw)
}
