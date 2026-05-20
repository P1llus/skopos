// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"net/http"
	"testing"
)

// TestSigV4PayloadHash pins the two payload-hash cases the SigV4 canonical
// request depends on: a body-less request hashes the empty string, and a
// request with a body hashes its bytes via GetBody (without consuming Body).
func TestSigV4PayloadHash(t *testing.T) {
	// SHA-256 of the empty string — the value SigV4 requires for no-body
	// requests.
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	// SHA-256 of "hello".
	const helloHash = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

	t.Run("nil_body", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "http://example/x", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		got, err := sigv4PayloadHash(req)
		if err != nil {
			t.Fatalf("hash: %v", err)
		}
		if got != emptyHash {
			t.Errorf("nil body hash = %q, want %q", got, emptyHash)
		}
	})

	t.Run("with_body_via_getbody", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "http://example/x", bytes.NewReader([]byte("hello")))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if req.GetBody == nil {
			t.Fatal("expected GetBody to be populated for an in-memory body")
		}
		got, err := sigv4PayloadHash(req)
		if err != nil {
			t.Fatalf("hash: %v", err)
		}
		if got != helloHash {
			t.Errorf("body hash = %q, want %q", got, helloHash)
		}
		// The hash is read from a GetBody copy, so req.Body is still intact.
		body, err := req.GetBody()
		if err != nil {
			t.Fatalf("getbody: %v", err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(body); err != nil {
			t.Fatalf("read body: %v", err)
		}
		if buf.String() != "hello" {
			t.Errorf("body after hashing = %q, want %q", buf.String(), "hello")
		}
	})
}
