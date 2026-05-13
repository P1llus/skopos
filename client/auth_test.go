package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p1llus/skopos/schema"
)

// authDoc builds a minimal one-request Doc carrying the supplied Auth. The
// shape mirrors bearerSimpleDoc but lets each §4.1 test pick the auth
// variant under test.
func authDoc(baseURL string, auth schema.Auth) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: baseURL},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth:     auth,
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/events")),
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}
}

// TestAuth_Basic asserts that auth.basic produces the standard
// "Authorization: Basic <base64(user:pass)>" header on the wire.
func TestAuth_Basic(t *testing.T) {
	const user, pass = "alice", "s3cret"
	wantHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != wantHeader {
			t.Errorf("Authorization = %q, want %q", got, wantHeader)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := authDoc(server.URL, schema.Auth{Basic: &schema.BasicAuth{
		Username: vStr(user),
		Password: vStr(pass),
	}})
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// TestAuth_Custom asserts that auth.custom puts the IR-evaluated Value
// onto the wire under the configured header name.
func TestAuth_Custom(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Foo"); got != "header-value-1" {
			t.Errorf("X-Foo = %q, want header-value-1", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := authDoc(server.URL, schema.Auth{Custom: &schema.CustomAuth{
		Header: "X-Foo",
		Value:  vRef("state.token"),
	}})
	// Add a state field the Value refers to.
	doc.State.Fields["token"] = schema.FieldDecl{Type: "secret", Default: "header-value-1"}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// TestAuth_APIKey_Header asserts the in_query=false (header-mode) branch
// of auth.api_key: the key value lands as a named request header and
// nothing leaks into the URL query.
func TestAuth_APIKey_Header(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Token"); got != "sk-123" {
			t.Errorf("X-Api-Token = %q, want sk-123", got)
		}
		if got := r.URL.Query().Get("X-Api-Token"); got != "" {
			t.Errorf("header-mode api_key leaked into query: %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := authDoc(server.URL, schema.Auth{APIKey: &schema.APIKeyAuth{
		Header:  "X-Api-Token",
		Value:   vRef("state.token"),
		InQuery: false,
	}})
	doc.State.Fields["token"] = schema.FieldDecl{Type: "secret", Default: "sk-123"}

	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// ---- oauth2 helpers ----

// oauth2TokenServer constructs a token endpoint that returns the supplied
// access_token + lifetime. tokenCalls counts how many times the endpoint
// fired so tests can assert cache hit/miss behaviour. wantGrant is the
// grant_type the test expects; mismatches fail the test (mostly a sanity
// check that the runner actually issued the right grant).
type oauth2TokenServer struct {
	server     *httptest.Server
	tokenCalls atomic.Int32
}

func newOAuth2TokenServer(t *testing.T, accessToken string, expiresIn int, wantGrant string, validate func(*http.Request) bool) *oauth2TokenServer {
	t.Helper()
	out := &oauth2TokenServer{}
	out.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out.tokenCalls.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("token endpoint method = %s, want POST", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if got := r.FormValue("grant_type"); got != wantGrant {
			t.Errorf("grant_type = %q, want %q", got, wantGrant)
		}
		if validate != nil && !validate(r) {
			http.Error(w, "validation failed", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": accessToken,
			"token_type":   "bearer",
			"expires_in":   expiresIn,
		})
	}))
	t.Cleanup(out.server.Close)
	return out
}

type oauth2GrantSpec struct {
	kind         string
	clientID     string
	clientSecret string
	username     string
	password     string
}

// oauth2Doc builds a one-request Doc with an OAuth2 auth block targeting
// the given token URL. The API server (apiURL) is hit on every iteration.
// cache controls whether the token-cache block is wired up.
func oauth2Doc(apiURL, tokenURL string, grant oauth2GrantSpec, cache *schema.TokenCache) *schema.Doc {
	doc := &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":       {Type: "url", Default: apiURL},
			"token_url": {Type: "url", Default: tokenURL},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/events")),
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}
	switch grant.kind {
	case "client_credentials":
		doc.State.Fields["client_id"] = schema.FieldDecl{Type: "string", Default: grant.clientID}
		doc.State.Fields["client_secret"] = schema.FieldDecl{Type: "secret", Default: grant.clientSecret}
		doc.Auth = schema.Auth{OAuth2: &schema.OAuth2Auth{
			ClientCredentials: &schema.ClientCredentialsGrant{
				TokenURL:     vRef("state.token_url"),
				ClientID:     vRef("state.client_id"),
				ClientSecret: vRef("state.client_secret"),
				Cache:        cache,
			},
		}}
	case "password":
		doc.State.Fields["username"] = schema.FieldDecl{Type: "string", Default: grant.username}
		doc.State.Fields["password"] = schema.FieldDecl{Type: "secret", Default: grant.password}
		doc.Auth = schema.Auth{OAuth2: &schema.OAuth2Auth{
			PasswordGrant: &schema.PasswordGrant{
				TokenURL: vRef("state.token_url"),
				Username: vRef("state.username"),
				Password: vRef("state.password"),
				Cache:    cache,
			},
		}}
	}
	// When cache is set, schema.Validate would auto-register the store_in field;
	// emulate that here so newScope sees the slot as runtime-mutable.
	if cache != nil && cache.StoreIn != "" {
		doc.State.Fields[cache.StoreIn] = schema.FieldDecl{Type: "string", Mutability: "runtime"}
	}
	return doc
}

// TestAuth_OAuth2_ClientCredentials_NoCache asserts the runner fetches a
// fresh access token before every IR-described request when the grant has
// no cache: block.
func TestAuth_OAuth2_ClientCredentials_NoCache(t *testing.T) {
	tok := newOAuth2TokenServer(t, "tok-cc-1", 3600, "client_credentials", func(r *http.Request) bool {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Basic ") {
			t.Errorf("Authorization = %q, want Basic prefix", auth)
			return false
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
		if err != nil {
			t.Errorf("Basic decode: %v", err)
			return false
		}
		if string(decoded) != "test-client:test-secret" {
			t.Errorf("Basic creds = %q, want test-client:test-secret", string(decoded))
			return false
		}
		return true
	})

	var apiCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer tok-cc-1" {
			t.Errorf("Authorization = %q, want Bearer tok-cc-1", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer api.Close()

	doc := oauth2Doc(api.URL, tok.server.URL, oauth2GrantSpec{
		kind:         "client_credentials",
		clientID:     "test-client",
		clientSecret: "test-secret",
	}, nil)
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: api.Client()}

	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 1: %v", err)
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 2: %v", err)
	}

	if got := tok.tokenCalls.Load(); got != 2 {
		t.Errorf("token endpoint hits = %d, want 2 (no cache: one fetch per drain)", got)
	}
	if got := apiCalls.Load(); got != 2 {
		t.Errorf("api hits = %d, want 2", got)
	}
}

// TestAuth_OAuth2_ClientCredentials_Cache_Hit asserts the cached token is
// re-used on a second drain while it's still inside its expiry buffer.
func TestAuth_OAuth2_ClientCredentials_Cache_Hit(t *testing.T) {
	tok := newOAuth2TokenServer(t, "tok-cc-cache", 3600, "client_credentials", nil)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-cc-cache" {
			t.Errorf("Authorization = %q, want Bearer tok-cc-cache", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer api.Close()

	doc := oauth2Doc(api.URL, tok.server.URL, oauth2GrantSpec{
		kind:         "client_credentials",
		clientID:     "test-client",
		clientSecret: "test-secret",
	}, &schema.TokenCache{StoreIn: "token", ExpiryField: mustPath("expires_in"), ExpiryBuffer: "60s"})

	store := &MemoryStore{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: api.Client()}

	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 1: %v", err)
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 2: %v", err)
	}

	if got := tok.tokenCalls.Load(); got != 1 {
		t.Errorf("token endpoint hits = %d, want 1 (cache hit on second drain)", got)
	}
	snap, _ := store.Load()
	if got, ok := snap.State["token"].(string); !ok || got != "tok-cc-cache" {
		t.Errorf("state.token = %v (ok=%v), want tok-cc-cache", snap.State["token"], ok)
	}
	if _, ok := snap.Cursor["__oauth2_token_expires_at"].(string); !ok {
		t.Errorf("cursor.__oauth2_token_expires_at = %v, want RFC 3339 string", snap.Cursor["__oauth2_token_expires_at"])
	}
}

// TestAuth_OAuth2_ClientCredentials_Cache_ExpiredRefetch advances the
// clock past the cached token's expiry buffer and asserts the next drain
// fetches a fresh token.
func TestAuth_OAuth2_ClientCredentials_Cache_ExpiredRefetch(t *testing.T) {
	tok := newOAuth2TokenServer(t, "tok-cc-fresh", 60, "client_credentials", nil)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-cc-fresh" {
			t.Errorf("Authorization = %q, want Bearer tok-cc-fresh", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer api.Close()

	doc := oauth2Doc(api.URL, tok.server.URL, oauth2GrantSpec{
		kind:         "client_credentials",
		clientID:     "test-client",
		clientSecret: "test-secret",
	}, &schema.TokenCache{StoreIn: "token", ExpiryField: mustPath("expires_in"), ExpiryBuffer: "30s"})

	clock := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	advance := time.Duration(0)
	now := func() time.Time { return clock.Add(advance) }

	store := &MemoryStore{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: now, Client: api.Client()}

	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 1: %v", err)
	}
	// Token valid for 60s; buffer 30s. Advance 45s → now+buffer (75s) >
	// expiry (60s) → re-fetch.
	advance = 45 * time.Second
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 2: %v", err)
	}

	if got := tok.tokenCalls.Load(); got != 2 {
		t.Errorf("token endpoint hits = %d, want 2 (cached token expired)", got)
	}
}

// TestAuth_OAuth2_ClientCredentials_Cache_PersistsAcrossRunners asserts the
// cached token survives a process restart: the second drain re-instantiates
// the Runner on the same Store and does NOT re-fetch.
func TestAuth_OAuth2_ClientCredentials_Cache_PersistsAcrossRunners(t *testing.T) {
	tok := newOAuth2TokenServer(t, "tok-persisted", 3600, "client_credentials", nil)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-persisted" {
			t.Errorf("Authorization = %q, want Bearer tok-persisted", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer api.Close()

	store := &MemoryStore{}
	mkRunner := func() *Runner {
		doc := oauth2Doc(api.URL, tok.server.URL, oauth2GrantSpec{
			kind:         "client_credentials",
			clientID:     "test-client",
			clientSecret: "test-secret",
		}, &schema.TokenCache{StoreIn: "token", ExpiryField: mustPath("expires_in"), ExpiryBuffer: "60s"})
		return &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: api.Client()}
	}

	if err := mkRunner().Drain(context.Background()); err != nil {
		t.Fatalf("Drain 1: %v", err)
	}
	if err := mkRunner().Drain(context.Background()); err != nil {
		t.Fatalf("Drain 2: %v", err)
	}
	if got := tok.tokenCalls.Load(); got != 1 {
		t.Errorf("token endpoint hits = %d, want 1 (cache hit across Runner instances)", got)
	}
}

// TestAuth_OAuth2_PasswordGrant_Basic asserts the password grant fetches
// the token using grant_type=password + username/password in the form body,
// and applies the Bearer token to the API request.
func TestAuth_OAuth2_PasswordGrant_Basic(t *testing.T) {
	tok := newOAuth2TokenServer(t, "tok-pw", 3600, "password", func(r *http.Request) bool {
		if got := r.FormValue("username"); got != "alice" {
			t.Errorf("username = %q, want alice", got)
			return false
		}
		if got := r.FormValue("password"); got != "s3cret" {
			t.Errorf("password = %q, want s3cret", got)
			return false
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty (no client_id)", got)
		}
		return true
	})

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-pw" {
			t.Errorf("Authorization = %q, want Bearer tok-pw", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer api.Close()

	doc := oauth2Doc(api.URL, tok.server.URL, oauth2GrantSpec{
		kind:     "password",
		username: "alice",
		password: "s3cret",
	}, nil)
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: api.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := tok.tokenCalls.Load(); got != 1 {
		t.Errorf("token endpoint hits = %d, want 1", got)
	}
}

// TestAuth_OAuth2_TokenEndpoint_NonSuccess asserts a non-2xx response from
// the token endpoint surfaces as an auth error AND does NOT leak the
// response body into the error string.
func TestAuth_OAuth2_TokenEndpoint_NonSuccess(t *testing.T) {
	const secretBody = `{"error":"invalid_client","leaked_secret":"DO-NOT-LOG"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(secretBody))
	}))
	defer server.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("api hit after failed token fetch")
		http.Error(w, "should not reach api", http.StatusInternalServerError)
	}))
	defer api.Close()

	doc := oauth2Doc(api.URL, server.URL, oauth2GrantSpec{
		kind:         "client_credentials",
		clientID:     "test-client",
		clientSecret: "test-secret",
	}, nil)
	// error.mode=fail so the token-endpoint 401 surfaces as a Drain error
	// rather than being logged + swallowed by the default "standard" mode.
	doc.Error = &schema.ErrorBlock{Mode: "fail"}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: api.Client()}

	err := r.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain returned nil, want auth error")
	}
	if strings.Contains(err.Error(), "leaked_secret") || strings.Contains(err.Error(), "DO-NOT-LOG") {
		t.Errorf("error string %q leaks response body", err.Error())
	}
}

// TestAuth_OAuth2_CustomExpiryField asserts cache.expiry_field is honoured
// for a non-default body shape (here the token endpoint reports lifetime
// nested under "ttl.seconds" rather than the RFC 6749 expires_in).
func TestAuth_OAuth2_CustomExpiryField(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "tok-nested",
			"token_type":   "bearer",
			"ttl":          map[string]any{"seconds": 3600},
		})
	}))
	defer server.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer api.Close()

	doc := oauth2Doc(api.URL, server.URL, oauth2GrantSpec{
		kind:         "client_credentials",
		clientID:     "test-client",
		clientSecret: "test-secret",
	}, &schema.TokenCache{StoreIn: "token", ExpiryField: mustPath("ttl.seconds"), ExpiryBuffer: "60s"})

	store := &MemoryStore{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: api.Client()}

	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 1: %v", err)
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 2: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("token endpoint hits = %d, want 1 (cache hit via ttl.seconds)", got)
	}
}

// TestExtractOAuth2Lifetime pins the closed set of expires_in value
// shapes the runtime accepts (RFC 6749 number, stringified integer, Go
// duration string).
func TestExtractOAuth2Lifetime(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Duration
	}{
		{name: "rfc6749_integer_seconds", body: `{"expires_in":3600}`, want: 3600 * time.Second},
		{name: "stringified_integer", body: `{"expires_in":"3600"}`, want: 3600 * time.Second},
		{name: "duration_string", body: `{"expires_in":"1h"}`, want: time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal([]byte(tc.body), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got, err := extractOAuth2Lifetime(body, nil)
			if err != nil {
				t.Fatalf("extractOAuth2Lifetime: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// multiModeDoc builds a one-request Doc whose Auth is a multi_mode dispatch.
// The state field auth_mode (enum default supplied per-test) is what the
// predicates compare against; bearer / api_key arms read the shared
// state.api_key field and the default arm writes a sentinel custom header
// so tests can prove the fallback fires.
func multiModeDoc(apiURL, authMode string) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":       {Type: "url", Default: apiURL},
			"api_key":   {Type: "secret", Default: "sentinel-key"},
			"auth_mode": {Type: "string", Default: authMode},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth: schema.Auth{MultiMode: &schema.MultiModeAuth{
			Branches: []schema.AuthBranch{
				{
					When: schema.Predicate{Eq: &schema.PredicateEq{Path: mustPath("state.auth_mode"), Equal: vStr("bearer")}},
					Auth: schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.api_key")}},
				},
				{
					When: schema.Predicate{Eq: &schema.PredicateEq{Path: mustPath("state.auth_mode"), Equal: vStr("api_key")}},
					Auth: schema.Auth{APIKey: &schema.APIKeyAuth{Header: "X-API-Key", Value: vRef("state.api_key")}},
				},
			},
			Default: schema.AuthDefault{Auth: schema.Auth{Custom: &schema.CustomAuth{
				Header: "X-Fallback-Auth",
				Value:  vRef("state.api_key"),
			}}},
		}},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/events")),
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}
}

// TestAuth_MultiMode_BranchBearer asserts the bearer branch wins when its
// predicate matches state.auth_mode, putting the token on the wire as
// Authorization: Bearer <token>.
func TestAuth_MultiMode_BranchBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sentinel-key" {
			t.Errorf("Authorization = %q, want Bearer sentinel-key", got)
		}
		if got := r.Header.Get("X-API-Key"); got != "" {
			t.Errorf("api_key arm leaked: X-API-Key = %q", got)
		}
		if got := r.Header.Get("X-Fallback-Auth"); got != "" {
			t.Errorf("default arm fired: X-Fallback-Auth = %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := multiModeDoc(server.URL, "bearer")
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// TestAuth_MultiMode_BranchAPIKey asserts the second branch fires when its
// predicate matches; the earlier (non-matching) bearer arm must not leave
// a stale Authorization header on the request.
func TestAuth_MultiMode_BranchAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != "sentinel-key" {
			t.Errorf("X-API-Key = %q, want sentinel-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("bearer arm leaked: Authorization = %q", got)
		}
		if got := r.Header.Get("X-Fallback-Auth"); got != "" {
			t.Errorf("default arm fired: X-Fallback-Auth = %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := multiModeDoc(server.URL, "api_key")
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// TestAuth_MultiMode_Default asserts that when no branch predicate matches,
// the default arm fires.
func TestAuth_MultiMode_Default(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Fallback-Auth"); got != "sentinel-key" {
			t.Errorf("X-Fallback-Auth = %q, want sentinel-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("bearer arm fired: Authorization = %q", got)
		}
		if got := r.Header.Get("X-API-Key"); got != "" {
			t.Errorf("api_key arm fired: X-API-Key = %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := multiModeDoc(server.URL, "unknown-mode")
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// TestAuth_MultiMode_FirstMatchWins asserts that when two branch
// predicates would both match, the earlier-declared branch wins and the
// later branch does NOT fire.
func TestAuth_MultiMode_FirstMatchWins(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sentinel-key" {
			t.Errorf("Authorization = %q, want Bearer sentinel-key (first branch should win)", got)
		}
		if got := r.Header.Get("X-API-Key"); got != "" {
			t.Errorf("second branch fired despite first match: X-API-Key = %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer server.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":     {Type: "url", Default: server.URL},
			"api_key": {Type: "secret", Default: "sentinel-key"},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth: schema.Auth{MultiMode: &schema.MultiModeAuth{
			Branches: []schema.AuthBranch{
				{
					When: schema.Predicate{LiteralBool: ptrBool(true)},
					Auth: schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.api_key")}},
				},
				{
					When: schema.Predicate{LiteralBool: ptrBool(true)},
					Auth: schema.Auth{APIKey: &schema.APIKeyAuth{Header: "X-API-Key", Value: vRef("state.api_key")}},
				},
			},
			Default: schema.AuthDefault{Auth: schema.Auth{None: &struct{}{}}},
		}},
		Requests: []schema.Request{{
			Method: "GET",
			Path:   ptrValue(vStr("/api/v1/events")),
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Now: fixedNow(), Client: server.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

func ptrBool(b bool) *bool { return &b }

// TestOnStatus_InvalidateCache_OAuth2_ClearsSlots asserts that when the API
// returns a 401 dispatched through `on_status: invalidate_cache`, the
// runner drops the cached OAuth2 token from state.<store_in> AND the
// paired expiry timestamp from cursor.__oauth2_<store_in>_expires_at.
// The snapshot persisted by the deferred Save MUST not carry either key,
// so the next drain refetches.
func TestOnStatus_InvalidateCache_OAuth2_ClearsSlots(t *testing.T) {
	tok := newOAuth2TokenServer(t, "tok-fresh", 3600, "client_credentials", nil)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer api.Close()

	doc := oauth2Doc(api.URL, tok.server.URL, oauth2GrantSpec{
		kind:         "client_credentials",
		clientID:     "test-client",
		clientSecret: "test-secret",
	}, &schema.TokenCache{StoreIn: "token", ExpiryField: mustPath("expires_in"), ExpiryBuffer: "60s"})
	// Surface the 401 through on_status so it dispatches as invalidate_cache.
	doc.Requests[0].ExpectStatus = []int{200}
	doc.Requests[0].OnStatus = map[int]string{401: "invalidate_cache"}

	// Pre-prime the snapshot with a stale token + expiry far in the future.
	// The cache would otherwise hit and skip the token fetch; we want to
	// prove invalidate_cache clears both slots regardless of cache state.
	farFuture := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	store := &MemoryStore{}
	_ = store.Save(Snapshot{
		State:  map[string]any{"token": "stale-token"},
		Cursor: map[string]any{"__oauth2_token_expires_at": farFuture},
	})

	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: api.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	snap, _ := store.Load()
	if _, ok := snap.State["token"]; ok {
		t.Errorf("state.token = %v after invalidate_cache; want missing", snap.State["token"])
	}
	if _, ok := snap.Cursor["__oauth2_token_expires_at"]; ok {
		t.Errorf("cursor.__oauth2_token_expires_at = %v after invalidate_cache; want missing",
			snap.Cursor["__oauth2_token_expires_at"])
	}
}

// TestOnStatus_InvalidateCache_OAuth2_RefetchesOnNextDrain runs back-to-back
// drains over the same Store: drain 1 fetches a token, fires the API,
// receives a 401 + invalidate_cache (which drops the cache). Drain 2 must
// then fetch a fresh token (token endpoint count moves from 1 → 2) rather
// than reusing the cleared slot.
func TestOnStatus_InvalidateCache_OAuth2_RefetchesOnNextDrain(t *testing.T) {
	tok := newOAuth2TokenServer(t, "tok-roundtrip", 3600, "client_credentials", nil)

	var apiCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		// First call: pretend the cached creds were revoked → 401.
		// Second call: serve events normally.
		if apiCalls.Load() == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-roundtrip" {
			t.Errorf("Authorization = %q, want Bearer tok-roundtrip on drain 2", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
	}))
	defer api.Close()

	doc := oauth2Doc(api.URL, tok.server.URL, oauth2GrantSpec{
		kind:         "client_credentials",
		clientID:     "test-client",
		clientSecret: "test-secret",
	}, &schema.TokenCache{StoreIn: "token", ExpiryField: mustPath("expires_in"), ExpiryBuffer: "60s"})
	doc.Requests[0].ExpectStatus = []int{200}
	doc.Requests[0].OnStatus = map[int]string{401: "invalidate_cache"}

	store := &MemoryStore{}
	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: api.Client()}

	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 1: %v", err)
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain 2: %v", err)
	}

	if got := tok.tokenCalls.Load(); got != 2 {
		t.Errorf("token endpoint hits = %d, want 2 (cache cleared between drains forces refetch)", got)
	}
	if got := apiCalls.Load(); got != 2 {
		t.Errorf("api hits = %d, want 2", got)
	}
}

// TestOnStatus_InvalidateCache_MultiMode_ClearsOAuth2Branch asserts the
// verb walks into auth.multi_mode and clears the OAuth2 cache slots of
// whichever branch carries one — even when a different (non-OAuth2)
// branch is the active dispatch target. The invalidate verb is conservative
// by design: cached credentials in unfired arms are dropped too so a
// future predicate flip doesn't reuse a token the operator marked stale.
func TestOnStatus_InvalidateCache_MultiMode_ClearsOAuth2Branch(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer api.Close()

	doc := &schema.Doc{
		IRVersion: "1",
		State: &schema.State{Fields: map[string]schema.FieldDecl{
			"url":           {Type: "url", Default: api.URL},
			"token_url":     {Type: "url", Default: "http://unreachable.invalid/token"},
			"client_id":     {Type: "string", Default: "cid"},
			"client_secret": {Type: "secret", Default: "csecret"},
			"static_token":  {Type: "secret", Default: "static-bearer"},
			"auth_mode":     {Type: "string", Default: "static"},
			// Auto-registered emulation: when multi_mode wraps an oauth2 cache
			// the IR pre-pass does not auto-register the slot today, so we
			// declare it explicitly here to mirror what an operator would do.
			"token": {Type: "string", Mutability: "runtime"},
		}},
		Defaults: &schema.Defaults{BaseURL: vRef("state.url")},
		Auth: schema.Auth{MultiMode: &schema.MultiModeAuth{
			Branches: []schema.AuthBranch{
				{
					When: schema.Predicate{Eq: &schema.PredicateEq{Path: mustPath("state.auth_mode"), Equal: vStr("oauth2")}},
					Auth: schema.Auth{OAuth2: &schema.OAuth2Auth{
						ClientCredentials: &schema.ClientCredentialsGrant{
							TokenURL:     vRef("state.token_url"),
							ClientID:     vRef("state.client_id"),
							ClientSecret: vRef("state.client_secret"),
							Cache:        &schema.TokenCache{StoreIn: "token", ExpiryField: mustPath("expires_in"), ExpiryBuffer: "60s"},
						},
					}},
				},
				{
					When: schema.Predicate{Eq: &schema.PredicateEq{Path: mustPath("state.auth_mode"), Equal: vStr("static")}},
					Auth: schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.static_token")}},
				},
			},
			Default: schema.AuthDefault{Auth: schema.Auth{None: &struct{}{}}},
		}},
		Requests: []schema.Request{{
			Method:       "GET",
			Path:         ptrValue(vStr("/api/v1/events")),
			ExpectStatus: []int{200},
			OnStatus:     map[int]string{401: "invalidate_cache"},
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("events")},
		Pagination: schema.Pagination{None: &struct{}{}},
		Progress:   schema.Progress{Stateless: &struct{}{}},
	}

	farFuture := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	store := &MemoryStore{}
	_ = store.Save(Snapshot{
		State:  map[string]any{"token": "stale-multi"},
		Cursor: map[string]any{"__oauth2_token_expires_at": farFuture},
	})

	r := &Runner{Doc: doc, Sink: &captureSink{}, Store: store, Now: fixedNow(), Client: api.Client()}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	snap, _ := store.Load()
	if _, ok := snap.State["token"]; ok {
		t.Errorf("state.token = %v after multi_mode invalidate_cache; want missing", snap.State["token"])
	}
	if _, ok := snap.Cursor["__oauth2_token_expires_at"]; ok {
		t.Errorf("cursor.__oauth2_token_expires_at = %v after multi_mode invalidate_cache; want missing",
			snap.Cursor["__oauth2_token_expires_at"])
	}
}
