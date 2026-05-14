// SPDX-License-Identifier: Apache-2.0

// Package testserver provides a unified HTTP stub server for all starter-
// subset skopos templates. One process, one port; each scenario lives at its
// own URL prefix.
//
// # Scenarios
//
// The scenarios registered by [AllScenarios] — six paginated/auth starter
// scenarios plus eight single-request pagination.none variations:
//
//	Scenario                  Prefix               Auth
//	bearer_simple             /bearer_simple       Bearer test-bearer-token-12345
//	cursor_token              /cursor_token        Bearer test-bearer-token-12345
//	page_number               /page_number         Bearer test-bearer-token-12345
//	offset                    /offset              Bearer test-bearer-token-12345
//	link_header               /link_header         X-API-Key: test-api-key-67890
//	oauth2_client_credentials /oauth2              client_credentials → Bearer
//	api_key_auth              /api_key_auth        X-API-Key: test-api-key-67890
//	basic_auth                /basic_auth          Basic testuser:testpass
//	custom_auth               /custom_auth         X-Custom-Auth: custom-auth-value-99999
//	simple_get_object         /simple_get_object   none
//	ndjson_response           /ndjson_response     Bearer test-bearer-token-12345
//	multi_mode_auth           /multi_mode_auth     Bearer | X-API-Key | X-Fallback-Auth: test-api-key-67890
//	post_raw_body             /post_raw_body       Bearer test-bearer-token-12345 (POST)
//	post_form_body            /post_form_body      Bearer test-bearer-token-12345 (POST)
//
// # Drain-start detection
//
// Each scenario auto-replenishes its event store at the start of a new drain
// so that `skopos run --interval 30s` keeps emitting fresh events forever:
//
//   - bearer_simple: every request is a fresh drain (no pagination).
//   - cursor_token: cursor query param is absent or empty.
//   - page_number: page query param is absent or "1".
//   - offset: offset query param is absent or "0".
//   - link_header: cursor query param is absent or empty.
//   - oauth2_client_credentials: cursor query param is absent or empty.
//   - api_key_auth, basic_auth, custom_auth, simple_get_object,
//     ndjson_response, multi_mode_auth, post_raw_body, post_form_body:
//     pagination.none — every request is a fresh drain.
//
// When a drain start is detected the scenario resets its window and appends
// [Options.EventsPerDrain] events with fresh timestamps and monotonically
// increasing sequence numbers. Subsequent page requests slice the same
// window until exhausted, then return the appropriate end-of-drain signal
// (null cursor, has_more: false, no Link header).
//
// # Template defaults
//
// Every starter template shipped in templates/ points at http://localhost:9999
// with a path that lines up with the corresponding scenario prefix. Running
//
//	go run ./cmd/testserver
//
// then
//
//	skopos template show <name> > spec.yml
//	skopos run -i spec.yml --once
//
// works without any further configuration.
package testserver
