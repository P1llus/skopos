// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/p1llus/skopos/schema"
)

// applyAuth mutates req with the credentials selected by doc.Auth. The
// variant set is catalogued at the package level (see doc.go); oauth2
// grants and multi_mode dispatch through applyOAuth2 (cache.go) and
// applyMultiMode respectively.
//
// The ctx + client arguments exist because the oauth2 branch issues its
// own HTTP exchange against the grant's token_url; non-oauth2 branches
// ignore both. multi_mode passes them through so an oauth2 arm continues
// to work behind it.
func (s *scope) applyAuth(ctx context.Context, client *http.Client, req *http.Request, auth schema.Auth) error {
	switch {
	case auth.None != nil:
		return nil

	case auth.Bearer != nil:
		tok, err := s.evalValue(auth.Bearer.Token)
		if err != nil {
			return fmt.Errorf("auth.bearer.token: %w", err)
		}
		if tok == nil {
			return fmt.Errorf("auth.bearer.token resolved to nil")
		}
		req.Header.Set("Authorization", "Bearer "+toString(tok))
		return nil

	case auth.Basic != nil:
		user, err := s.evalValue(auth.Basic.Username)
		if err != nil {
			return fmt.Errorf("auth.basic.username: %w", err)
		}
		pass, err := s.evalValue(auth.Basic.Password)
		if err != nil {
			return fmt.Errorf("auth.basic.password: %w", err)
		}
		pair := toString(user) + ":" + toString(pass)
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(pair)))
		return nil

	case auth.APIKey != nil:
		val, err := s.evalValue(auth.APIKey.Value)
		if err != nil {
			return fmt.Errorf("auth.api_key.value: %w", err)
		}
		if auth.APIKey.InQuery {
			q := req.URL.Query()
			q.Set(auth.APIKey.Header, toString(val))
			req.URL.RawQuery = q.Encode()
			return nil
		}
		req.Header.Set(auth.APIKey.Header, toString(val))
		return nil

	case auth.Custom != nil:
		val, err := s.evalValue(auth.Custom.Value)
		if err != nil {
			return fmt.Errorf("auth.custom.value: %w", err)
		}
		req.Header.Set(auth.Custom.Header, toString(val))
		return nil

	case auth.OAuth2 != nil:
		return s.applyOAuth2(ctx, client, req, auth.OAuth2)

	case auth.SigV4 != nil:
		return s.applySigV4(ctx, req, auth.SigV4)

	case auth.MultiMode != nil:
		return s.applyMultiMode(ctx, client, req, auth.MultiMode)
	}
	return fmt.Errorf("auth: no variant set")
}

// applyMultiMode evaluates each branch's predicate in declaration order
// against the current scope; the first match wins. When no branch matches,
// the bare default Auth is dispatched. The chosen Auth is dispatched back
// through applyAuth — recursion is safe because applyAuth keeps no
// per-request state of its own and the validator already forbids nested
// multi_mode (so the recursion bottoms out in one hop).
func (s *scope) applyMultiMode(ctx context.Context, client *http.Client, req *http.Request, m *schema.MultiModeAuth) error {
	for i, b := range m.Branches {
		ok, err := s.evalPredicate(b.When)
		if err != nil {
			return fmt.Errorf("auth.multi_mode.branches[%d].when: %w", i, err)
		}
		if ok {
			return s.applyAuth(ctx, client, req, b.Auth)
		}
	}
	return s.applyAuth(ctx, client, req, m.Default)
}
