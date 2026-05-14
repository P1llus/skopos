// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"net/http"
	"time"
)

// Scenario is implemented by every scenario handler. Name returns the URL
// prefix (e.g. "cursor_token"); Register mounts the handler routes on mux.
type Scenario interface {
	Name() string
	Register(mux *http.ServeMux, opts Options)
}

// Options controls server-wide behaviour that individual scenarios inherit.
type Options struct {
	// PageSize is the number of events returned per paginated page.
	// Default: 2.
	PageSize int
	// EventsPerDrain is the number of events appended to the store at the
	// start of each new drain. Default: 5.
	EventsPerDrain int
	// Now is the time source used to stamp new events. Defaults to
	// time.Now when nil; injectable for deterministic tests.
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.PageSize <= 0 {
		o.PageSize = 2
	}
	if o.EventsPerDrain <= 0 {
		o.EventsPerDrain = 5
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}
