// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/p1llus/skopos/schema"
)

// fixedNow returns a deterministic clock pinned to 2026-01-01 UTC for use
// across runner / value / progress tests that compare timestamps.
func fixedNow() func() time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// vStr lifts a string literal into a schema.Value.
func vStr(s string) schema.Value { return schema.Value{LiteralString: &s} }

// vInt lifts an int literal into a schema.Value.
func vInt(i int64) schema.Value { return schema.Value{LiteralInt: &i} }

// vBool lifts a bool literal into a schema.Value.
func vBool(b bool) schema.Value { return schema.Value{LiteralBool: &b} }

// vRef builds {ref: <path>}.
func vRef(p string) schema.Value { return schema.Value{Ref: &schema.RefValue{Path: mustPath(p)}} }

// vRefDefault builds {ref: <path>, default: <fallback>}.
func vRefDefault(p string, d schema.Value) schema.Value {
	return schema.Value{Ref: &schema.RefValue{Path: mustPath(p), Default: &d}}
}

// vNow builds {now: true}.
func vNow() schema.Value { return schema.Value{Now: &schema.NowValue{}} }

// mustPath panics if s does not parse as a Path.
func mustPath(s string) schema.Path {
	p, err := schema.ParsePath(s)
	if err != nil {
		panic("mustPath(" + s + "): " + err.Error())
	}
	return p
}

// captureSink collects every event emitted by a Runner.Drain. Used by
// almost every end-to-end runner test.
type captureSink struct {
	events []any
	flushN int
}

// Emit appends event to c.events.
func (c *captureSink) Emit(ev any) error { c.events = append(c.events, ev); return nil }

// Flush counts calls; the runner invokes it once per Drain via its deferred
// Save + Flush block.
func (c *captureSink) Flush() error { c.flushN++; return nil }

// captureTracer collects every Exchange the runner emits.
type captureTracer struct {
	exchanges []Exchange
}

// OnExchange records ex in declaration order.
func (c *captureTracer) OnExchange(ex Exchange) { c.exchanges = append(c.exchanges, ex) }

// minimalDoc returns the smallest valid spec wired to baseURL — a single
// GET request whose body is {"events": []} and no pagination. Used by
// tests that only need "a Drain that runs cleanly".
func minimalDoc(baseURL string) *schema.Doc {
	return &schema.Doc{
		IRVersion: "1",
		State: map[string]schema.FieldDecl{
			"url": {Type: "url", Default: new(vStr(baseURL))},
		},
		Auth: schema.Auth{None: &struct{}{}},
		Requests: []schema.Request{{
			Method: "GET",
			URL:    mustInterp("${state.url}/events"),
		}},
		Response:   schema.Response{Decode: "json", EventsAt: mustPath("response.body.events")},
		Pagination: schema.Pagination{None: &struct{}{}},
	}
}

// bearerDoc is a single-step bearer-token spec wired to baseURL with the
// given token.
func bearerDoc(baseURL, token string) *schema.Doc {
	d := minimalDoc(baseURL)
	d.State["api_key"] = schema.FieldDecl{Type: "secret", Default: new(vStr(token))}
	d.Auth = schema.Auth{Bearer: &schema.BearerAuth{Token: vRef("state.api_key")}}
	return d
}

// mustInterp returns the Value produced by parsing s as a YAML string
// scalar — i.e. it honours the ${…} interpolation desugaring path. Used
// in test specs that need URL Values composed from state.* slots.
func mustInterp(s string) schema.Value {
	var v schema.Value
	if err := yaml.Unmarshal([]byte(s), &v); err != nil {
		panic("mustInterp(" + s + "): " + err.Error())
	}
	return v
}

// writeJSON encodes payload as JSON and writes it with the given status.
// Helper for httptest handlers across the client_test package.
func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if payload == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(payload)
}
