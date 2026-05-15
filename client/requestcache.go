// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
	"strconv"
	"time"

	"github.com/p1llus/skopos/schema"
)

// requests[].cache is the non-OAuth2 counterpart of
// auth.oauth2.<grant>.cache: it wraps a token-style step (a custom JSON
// login, a session-key exchange, etc.) in a fresh-vs-cached conditional so
// the login round-trip is not paid on every iteration.
//
// The cached value lives at state.<cache.store_in> — the same top-level
// response field name doubles as the state slot, so a Splunk login
// (store_in: sessionKey), a Lacework login (store_in: token), or the
// session_login_cached template (store_in: session_token) each capture
// body.<store_in> into state.<store_in>. The slot is auto-registered as a
// runtime string by the IR validator, so it survives across drains via the
// deferred store.Save.
//
// The paired expiry timestamp lives at cursor.__step_<store_in>_expires_at
// as an RFC 3339 string. The prefix is deliberately distinct from OAuth2's
// __oauth2_<store_in>_expires_at so the two caches never collide on a doc
// that uses both.

// stepCacheExpiryKey returns the cursor key paired with state.<storeIn> for
// holding a step cache's expiry timestamp. Parallel to oauth2ExpiryKey with
// a distinct prefix so step caches and OAuth2 caches never share a slot.
func stepCacheExpiryKey(storeIn string) string {
	return "__step_" + storeIn + "_expires_at"
}

// cachedStepValue reports whether state.<store_in> carries a value whose
// paired expiry timestamp is still beyond now+expiry_buffer. When it returns
// true the runner skips re-executing the cached step — the value already in
// state stays put for downstream refs (auth.bearer.token, headers, etc.).
//
// Any miss reason — slot empty, expiry slot empty, unparseable timestamp —
// returns false so the caller re-runs the step and overwrites both slots.
func (s *scope) cachedStepValue(cache *schema.RequestCache) bool {
	rawVal, ok := s.state[cache.StoreIn]
	if !ok {
		return false
	}
	val, ok := rawVal.(string)
	if !ok || val == "" {
		return false
	}
	expRaw, ok := s.cursor[stepCacheExpiryKey(cache.StoreIn)]
	if !ok {
		return false
	}
	expStr, ok := expRaw.(string)
	if !ok || expStr == "" {
		return false
	}
	expAt, err := parseStoredTime(expStr)
	if err != nil {
		return false
	}
	buffer, err := time.ParseDuration(cache.ExpiryBuffer)
	if err != nil {
		// Validator rejects an unparseable buffer; defensive fall-through.
		return false
	}
	return s.now().Add(buffer).Before(expAt)
}

// storeStepValue captures the step's token-shaped response field into
// state.<store_in> and writes the paired expiry timestamp into
// cursor.__step_<store_in>_expires_at. Called after a successful execution
// of a cache-bearing step; the next iteration (or drain) then finds the
// cache populated and skips the step until the expiry buffer is crossed.
func (s *scope) storeStepValue(cache *schema.RequestCache, body any) error {
	m, ok := body.(map[string]any)
	if !ok {
		return fmt.Errorf("response body is not a JSON object (%T)", body)
	}
	rawVal, ok := m[cache.StoreIn]
	if !ok {
		return fmt.Errorf("response body missing %q field", cache.StoreIn)
	}
	val, ok := rawVal.(string)
	if !ok || val == "" {
		return fmt.Errorf("response field %q is empty or non-string", cache.StoreIn)
	}
	expAt, err := stepCacheExpiry(s.now(), m, cache)
	if err != nil {
		return err
	}
	s.state[cache.StoreIn] = val
	s.cursor[stepCacheExpiryKey(cache.StoreIn)] = expAt.UTC().Format(time.RFC3339Nano)
	return nil
}

// stepCacheExpiry computes the absolute expiry instant for a step cache from
// the response body. expiry_format selects how the value at expiry_field is
// interpreted:
//
//   - "" / "duration": a remaining lifetime (integer seconds, a stringified
//     integer, or a Go duration string) added to now. Same shape as the
//     OAuth2 expires_in path.
//   - "unix_seconds" / "unix_millis": an absolute Unix timestamp.
//   - "rfc3339" / "rfc3339nano": an absolute RFC 3339 timestamp string.
func stepCacheExpiry(now time.Time, body map[string]any, cache *schema.RequestCache) (time.Time, error) {
	raw, found, err := lookupBodyPath(body, cache.ExpiryField.Parts)
	if err != nil {
		return time.Time{}, fmt.Errorf("expiry_field: %w", err)
	}
	if !found {
		return time.Time{}, fmt.Errorf("expiry_field %s missing from response", cache.ExpiryField)
	}
	switch cache.ExpiryFormat {
	case "", "duration", "parse_duration":
		lifetime, err := coerceLifetime(raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("expiry_field: %w", err)
		}
		return now.Add(lifetime), nil
	case "unix_seconds":
		n, err := toInt(raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("expiry_field: cannot interpret %T as unix_seconds", raw)
		}
		return time.Unix(n, 0).UTC(), nil
	case "unix_millis":
		n, err := toInt(raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("expiry_field: cannot interpret %T as unix_millis", raw)
		}
		return time.UnixMilli(n).UTC(), nil
	case "rfc3339", "rfc3339nano":
		t, err := toTime(raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("expiry_field: %w", err)
		}
		return t.UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("expiry_format %q is not supported for step caches", cache.ExpiryFormat)
	}
}

// invalidateStepCaches clears every step-level cache slot declared by
// requests[].cache (state.<store_in> + cursor.__step_<store_in>_expires_at)
// and returns the store_in names that were cleared. Parallel to
// invalidateAuthCaches: the runtime `on_status: invalidate_cache` verb walks
// both so a 401 drops cached auth tokens AND cached login tokens in one
// move, and the next drain re-runs the login step.
func (s *scope) invalidateStepCaches(doc *schema.Doc) []string {
	var cleared []string
	for _, req := range doc.Requests {
		if req.Cache == nil || req.Cache.StoreIn == "" {
			continue
		}
		delete(s.state, req.Cache.StoreIn)
		delete(s.cursor, stepCacheExpiryKey(req.Cache.StoreIn))
		cleared = append(cleared, req.Cache.StoreIn)
	}
	return cleared
}

// parseStoredTime parses an expiry timestamp persisted by storeStepValue (or
// storeOAuth2Token): RFC 3339 with nanos first, then plain RFC 3339.
func parseStoredTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// coerceLifetime interprets a remaining-lifetime value from a token/login
// response body. A JSON number is integer seconds (RFC 6749's canonical
// expires_in form); a JSON string is re-parsed as an integer first, then as
// a Go duration ("1h" / "3600s"). Shared by the OAuth2 token path and the
// requests[].cache step path so both honour the same value shapes.
func coerceLifetime(raw any) (time.Duration, error) {
	switch x := raw.(type) {
	case float64:
		return time.Duration(x * float64(time.Second)), nil
	case int64:
		return time.Duration(x) * time.Second, nil
	case int:
		return time.Duration(x) * time.Second, nil
	case string:
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return time.Duration(n) * time.Second, nil
		}
		if d, err := time.ParseDuration(x); err == nil {
			return d, nil
		}
		return 0, fmt.Errorf("expiry value %q is neither integer seconds nor a Go duration", x)
	}
	return 0, fmt.Errorf("expiry value has unexpected type %T", raw)
}
