// SPDX-License-Identifier: Apache-2.0

package testserver

import "net/http"

type asyncPollStatelessScenario struct{}

// AsyncPollStateless returns the async_poll_stateless scenario. It mounts
// the same submit → poll → fetch chain as AsyncPoll, but under the
// /async_poll_stateless/ prefix so the runtime can exercise
// schema/testdata/async_poll_stateless.yml (cursor_update.kind = stateless).
func AsyncPollStateless() Scenario { return asyncPollStatelessScenario{} }

func (asyncPollStatelessScenario) Name() string { return "async_poll_stateless" }

func (asyncPollStatelessScenario) Register(mux *http.ServeMux, opts Options) {
	registerAsyncPollHandlers(mux, opts, "/async_poll_stateless")
}
