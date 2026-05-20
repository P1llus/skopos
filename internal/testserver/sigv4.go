// SPDX-License-Identifier: Apache-2.0

package testserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

type sigv4Scenario struct{}

// SigV4 returns the sigv4 scenario. It mounts a single GET /sigv4/events
// handler authenticated with AWS Signature Version 4. The handler proves the
// client signed correctly by re-deriving the signature from the received
// request and comparing it byte-for-byte against the wire Authorization
// header. No pagination: every request is a fresh drain and replenish fires
// each time.
func SigV4() Scenario { return sigv4Scenario{} }

func (sigv4Scenario) Name() string { return "sigv4" }

func (sigv4Scenario) Register(mux *http.ServeMux, opts Options) {
	store := &EventStore{}
	signer := v4.NewSigner()
	mux.HandleFunc("/sigv4/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkSigV4(w, r, signer) {
			return
		}
		// No pagination: every request is a drain start.
		store.Replenish(opts.Now(), opts.EventsPerDrain)
		events := store.Slice(0, opts.EventsPerDrain)
		writeJSON(w, http.StatusOK, map[string]any{
			"events": events,
		})
	})
}

// checkSigV4 verifies the request carries a valid AWS SigV4 signature for the
// scenario's test credentials. It re-derives the signature server-side: it
// reads X-Amz-Date off the wire and uses it as the signing time, recomputes
// the payload hash from the received body, reconstructs an equivalent request
// carrying only the headers the client signed (per the wire SignedHeaders),
// and re-runs the same v4 signer. Because the signing time comes off the
// wire, the result is deterministic regardless of the client's real-clock
// signing. On any mismatch it writes a 403 so the failure is visible.
func checkSigV4(w http.ResponseWriter, r *http.Request, signer *v4.Signer) bool {
	gotAuth := r.Header.Get("Authorization")
	if gotAuth == "" {
		writeError(w, http.StatusUnauthorized, "missing Authorization")
		return false
	}

	signingTime, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		writeError(w, http.StatusForbidden, "bad or missing X-Amz-Date")
		return false
	}

	signedHeaders, ok := sigv4SignedHeaders(gotAuth)
	if !ok {
		writeError(w, http.StatusForbidden, "missing SignedHeaders")
		return false
	}

	// Read the body to hash it, then restore it for any downstream handler.
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	sum := sha256.Sum256(bodyBytes)
	payloadHash := hex.EncodeToString(sum[:])

	// Reconstruct an equivalent request. The signer recomputes X-Amz-Date and
	// the canonical header set from the request itself, so it is enough to
	// replay the host, path, query, content length, and the exact headers the
	// client signed.
	recon := &http.Request{
		Method:        r.Method,
		URL:           &url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path, RawQuery: r.URL.RawQuery},
		Host:          r.Host,
		Header:        make(http.Header),
		ContentLength: int64(len(bodyBytes)),
	}
	for _, h := range signedHeaders {
		if h == "host" {
			continue // derived from req.Host by the signer
		}
		recon.Header.Set(h, r.Header.Get(h))
	}

	creds := aws.Credentials{
		AccessKeyID:     DefaultSigV4AccessKeyID,
		SecretAccessKey: DefaultSigV4SecretAccessKey,
	}
	if err := signer.SignHTTP(r.Context(), creds, recon, payloadHash, SigV4Service, SigV4Region, signingTime); err != nil {
		writeError(w, http.StatusForbidden, "re-sign failed")
		return false
	}
	if recon.Header.Get("Authorization") != gotAuth {
		writeError(w, http.StatusForbidden, "signature mismatch")
		return false
	}
	return true
}

// sigv4SignedHeaders extracts the lowercase header names from the
// "SignedHeaders=h1;h2;..." component of a SigV4 Authorization header.
func sigv4SignedHeaders(auth string) ([]string, bool) {
	_, after, ok := strings.Cut(auth, "SignedHeaders=")
	if !ok {
		return nil, false
	}
	rest, _, _ := strings.Cut(after, ",")
	if rest == "" {
		return nil, false
	}
	return strings.Split(rest, ";"), true
}
