// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/p1llus/skopos/internal/testserver"
)

// TestFilterScenarios checks that filterScenarios correctly selects by name.
func TestFilterScenarios(t *testing.T) {
	all := testserver.AllScenarios()
	got := filterScenarios(all, "bearer_simple,cursor_token")
	if len(got) != 2 {
		t.Errorf("filterScenarios returned %d scenarios, want 2", len(got))
	}
	if got[0].Name() != "bearer_simple" {
		t.Errorf("got[0].Name() = %q, want bearer_simple", got[0].Name())
	}
	if got[1].Name() != "cursor_token" {
		t.Errorf("got[1].Name() = %q, want cursor_token", got[1].Name())
	}
}

// TestFilterScenarios_Empty returns all when filter is empty.
func TestFilterScenarios_Empty(t *testing.T) {
	all := testserver.AllScenarios()
	got := filterScenarios(all, "")
	if len(got) != len(all) {
		t.Errorf("filterScenarios('') returned %d, want %d", len(got), len(all))
	}
}
