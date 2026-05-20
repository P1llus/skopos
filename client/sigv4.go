// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/p1llus/skopos/schema"
)

// applySigV4 signs req with AWS Signature Version 4. SignHTTP mutates req in
// place, setting Authorization, X-Amz-Date, and (when a session token is
// present) X-Amz-Security-Token. It is the final mutation before the request
// is sent, so the signed header set equals the wire header set.
//
// Signing uses real wall-clock time.Now() rather than the scope clock
// s.now(): the latter can be pinned via SOURCE_DATE_EPOCH to make
// {now: true} deterministic, and a pinned epoch would put X-Amz-Date outside
// AWS's clock-skew window and draw RequestTimeTooSkewed.
func (s *scope) applySigV4(ctx context.Context, req *http.Request, a *schema.SigV4Auth) error {
	region, err := s.evalValue(a.Region)
	if err != nil {
		return fmt.Errorf("auth.sigv4.region: %w", err)
	}
	regionStr := toString(region)
	if regionStr == "" {
		return fmt.Errorf("auth.sigv4.region resolved to empty")
	}

	service, err := s.evalValue(a.Service)
	if err != nil {
		return fmt.Errorf("auth.sigv4.service: %w", err)
	}
	serviceStr := toString(service)
	if serviceStr == "" {
		return fmt.Errorf("auth.sigv4.service resolved to empty")
	}

	creds, err := s.sigv4Credentials(ctx, a)
	if err != nil {
		return err
	}

	payloadHash, err := sigv4PayloadHash(req)
	if err != nil {
		return fmt.Errorf("auth.sigv4: %w", err)
	}

	if err := s.sigv4SignerInstance().SignHTTP(ctx, creds, req, payloadHash, serviceStr, regionStr, time.Now()); err != nil {
		return fmt.Errorf("auth.sigv4: sign request: %w", redactURLError(err))
	}
	return nil
}

// sigv4Credentials resolves the credentials for one signing call. When
// access_key_id + secret_access_key are set it builds a static provider from
// the resolved Values (static construction is free, so it is done per call);
// otherwise it falls back to the scope's memoized AWS default credential
// chain. Resolved secret material never appears in returned errors.
func (s *scope) sigv4Credentials(ctx context.Context, a *schema.SigV4Auth) (aws.Credentials, error) {
	if a.AccessKeyID != nil && a.SecretAccessKey != nil {
		id, err := s.evalValue(*a.AccessKeyID)
		if err != nil {
			return aws.Credentials{}, fmt.Errorf("auth.sigv4.access_key_id: %w", err)
		}
		secret, err := s.evalValue(*a.SecretAccessKey)
		if err != nil {
			return aws.Credentials{}, fmt.Errorf("auth.sigv4.secret_access_key: %w", err)
		}
		var token string
		if a.SessionToken != nil {
			tok, err := s.evalValue(*a.SessionToken)
			if err != nil {
				return aws.Credentials{}, fmt.Errorf("auth.sigv4.session_token: %w", err)
			}
			token = toString(tok)
		}
		provider := credentials.NewStaticCredentialsProvider(toString(id), toString(secret), token)
		return provider.Retrieve(ctx)
	}

	provider, err := s.ambientAWSProvider(ctx)
	if err != nil {
		return aws.Credentials{}, err
	}
	creds, err := provider.Retrieve(ctx)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("auth.sigv4: retrieve credentials: %w", err)
	}
	return creds, nil
}

// ambientAWSProvider returns the AWS default-chain credentials provider,
// building it at most once per scope. config.LoadDefaultConfig performs I/O
// (env, shared config, IMDS / ECS / IAM role) and returns a
// self-caching/refreshing provider, so the build is guarded by sigv4Once;
// region and service are not part of the provider (they are SignHTTP
// arguments), so a single ambient provider serves multi_mode branches with
// different regions or services. A build failure is sticky for the scope's
// lifetime.
func (s *scope) ambientAWSProvider(ctx context.Context) (aws.CredentialsProvider, error) {
	s.sigv4Once.Do(func() {
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			s.awsAmbientErr = err
			return
		}
		s.awsAmbient = cfg.Credentials
	})
	if s.awsAmbientErr != nil {
		return nil, fmt.Errorf("auth.sigv4: load AWS default credential chain: %w", s.awsAmbientErr)
	}
	return s.awsAmbient, nil
}

// sigv4SignerInstance returns the scope's stateless v4 signer, constructing
// it on first use. A drain is single-threaded, so the nil check needs no
// lock.
func (s *scope) sigv4SignerInstance() *v4.Signer {
	if s.sigv4Signer == nil {
		s.sigv4Signer = v4.NewSigner()
	}
	return s.sigv4Signer
}

// sigv4PayloadHash returns the lowercase hex SHA-256 of the request body,
// which SignHTTP folds into the canonical request. The body is read via
// req.GetBody (a fresh copy that does not consume req.Body); skopos bodies
// are always materialized, so GetBody is populated whenever a body exists.
// A nil GetBody (e.g. a GET with no body) hashes the empty input, yielding
// the SHA-256 of the empty string that SigV4 requires.
func sigv4PayloadHash(req *http.Request) (string, error) {
	h := sha256.New()
	if req.GetBody != nil {
		rc, err := req.GetBody()
		if err != nil {
			return "", fmt.Errorf("read request body: %w", err)
		}
		defer func() { _ = rc.Close() }()
		if _, err := io.Copy(h, rc); err != nil {
			return "", fmt.Errorf("hash request body: %w", err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
