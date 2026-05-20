// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strconv"
	"time"
)

// pinnedClock returns a fixed clock and ok=true when SOURCE_DATE_EPOCH holds a
// valid Unix-seconds integer, and ok=false otherwise.
//
// SOURCE_DATE_EPOCH is the cross-tool convention for reproducible output. When
// set, every {now: true} the spec evaluates resolves to the same instant, so
// any wall-clock value a spec writes into a request body, query, or state slot
// becomes byte-stable across runs. The returned time is in UTC with zero
// sub-second component, which keeps RFC 3339 / RFC 3339 Nano renderings at a
// fixed width. Without the variable, callers fall back to the runner's own
// time.Now default.
func pinnedClock() (func() time.Time, bool) {
	v := os.Getenv("SOURCE_DATE_EPOCH")
	if v == "" {
		return nil, false
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil, false
	}
	t := time.Unix(sec, 0).UTC()
	return func() time.Time { return t }, true
}
