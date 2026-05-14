// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

// IsDrainStart reports whether r is the first request of a new drain for
// the given pagination style. The rules mirror the drain-start detection
// table in the package doc.
//
//   - "none"        — always true (bearer_simple; every request is a fresh drain).
//   - "cursor"      — cursor query param is absent or empty.
//   - "page"        — page query param is absent or "1".
//   - "offset"      — offset query param is absent or "0".
func IsDrainStart(style string, r *http.Request) bool {
	q := r.URL.Query()
	switch style {
	case "none":
		return true
	case "cursor":
		v := q.Get("cursor")
		return v == ""
	case "page":
		v := q.Get("page")
		return v == "" || v == "1"
	case "offset":
		v := q.Get("offset")
		return v == "" || v == "0"
	default:
		return false
	}
}
