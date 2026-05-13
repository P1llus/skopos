// SPDX-License-Identifier: Apache-2.0

package client

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/p1llus/skopos/schema"
)

// safeURL renders u in a form safe for logs and error messages: scheme,
// host, and path only. The query string and userinfo are stripped because
// either may carry secrets — auth.api_key.in_query writes the credential
// into RawQuery, and any {query.<k>: <secret-typed ref>} in the IR puts a
// secret-typed state field into the URL.
//
// The runner treats request URLs as untrusted-for-logging from this point
// on. Authors who genuinely need to inspect the full URL during template
// development should plug a *http.Client whose Transport prints the
// request — that is an explicit opt-in, not the default.
func safeURL(u *url.URL) string {
	if u == nil {
		return "<nil-url>"
	}
	safe := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
	return safe.String()
}

// RedactURLError walks err looking for a *url.Error and rewrites the
// message so the URL it embeds is replaced with safeURL'd form. Used by
// every call site that surfaces transport errors from http.Client.Do or
// url.Parse — both embed the raw URL, query string and all.
//
// When err does not wrap a *url.Error this is a no-op. When the embedded
// URL is unparseable the URL substring is replaced with "<unparseable>".
//
// The returned error is a plain errors.New value; the *url.Error chain
// identity is not preserved. Callers do not rely on errors.As(*url.Error)
// past this point.
//
// Exported so external callers (e.g. cmd/skopos's continuous-mode drain-error
// log site) can wrap at the log site rather than relying on transitive
// through-callee redaction.
func RedactURLError(err error) error { return redactURLError(err) }

func redactURLError(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	raw := ue.URL
	if raw == "" {
		return err
	}
	safe := "<unparseable>"
	if parsed, perr := url.Parse(raw); perr == nil {
		safe = safeURL(parsed)
	}
	if safe == raw {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), raw, safe))
}

// redactValue renders v for inclusion in operational logs or error
// messages. If v carries a secret (per schema.IsSecret) the rendering is
// "<redacted>"; otherwise the IR shape of v is summarised — the variant
// name and any structural hints (literal text, ref path, format verb),
// but never a resolved runtime value.
//
// The runner does NOT currently render Values verbatim anywhere — this
// helper exists so any future debug surface attaches it uniformly. New
// log lines or error messages that need to mention a Value MUST go
// through redactValue rather than rolling their own toString.
func redactValue(doc *schema.Doc, v schema.Value) string {
	if schema.IsSecret(doc, v) {
		return "<redacted>"
	}
	return valueShape(v)
}

// valueShape returns a structure-only description of v — never a resolved
// runtime value. Used as the non-secret rendering inside redactValue and
// directly when the caller has already confirmed v is not secret-bearing.
func valueShape(v schema.Value) string {
	switch {
	case v.IsZero:
		return "<zero>"
	case v.LiteralString != nil:
		return fmt.Sprintf("%q", *v.LiteralString)
	case v.LiteralInt != nil:
		return fmt.Sprintf("%d", *v.LiteralInt)
	case v.LiteralBool != nil:
		return fmt.Sprintf("%t", *v.LiteralBool)
	case v.Ref != nil:
		return "<ref " + v.Ref.Path.String() + ">"
	case v.Now != nil:
		return "<now>"
	case v.Concat != nil:
		return "<concat>"
	case v.Select != nil:
		return "<select>"
	case v.FromPagination != "":
		return "<from_pagination:" + v.FromPagination + ">"
	case v.FromProgress != "":
		return "<from_progress:" + v.FromProgress + ">"
	case v.Format != nil:
		return "<format:" + v.Format.Verb + ">"
	case v.Base64 != nil:
		return "<base64>"
	case v.List != nil:
		return "<list>"
	case v.Object != nil:
		return "<object>"
	}
	return "<empty>"
}
