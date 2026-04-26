// Package auth provides OIDC bearer-token verification and gRPC interceptor
// middleware for gild. The implementation is stdlib-only — no third-party JWT
// or OIDC libraries — because Phase 10 is about scaffolding the security path
// without pulling in dependencies. Real-IdP soak/integration verification is
// deferred to a later phase (see docs/plans/phase-10-cloud-real-vscode-packaging.md).
//
// Reference shape borrowed from /home/ubuntu/research/hermes-agent/agent/google_oauth.py
// (auth-code flow plumbing) and hermes_cli/auth.py:_decode_jwt_claims (the
// raw base64url-decode-the-middle-segment idea), but the actual signature
// verification path here is hand-rolled against crypto/rsa + crypto/ecdsa.
package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// JWKSCache fetches and caches the keys served by an OIDC issuer's JWKS
// endpoint. Lookup by `kid` returns the cached key when fresh; otherwise the
// cache refreshes from the network and tries once more. This handles both
// initial population and key rotation.
type JWKSCache struct {
	URL  string
	HTTP *http.Client
	TTL  time.Duration

	mu      sync.RWMutex
	keys    map[string]crypto.PublicKey
	algs    map[string]string // kid -> alg (e.g. "RS256")
	fetched time.Time
}

// NewJWKSCache constructs a cache that will fetch from url. ttl controls how
// long a successful fetch is considered fresh; on cache miss for a kid we
// refetch regardless of ttl (key rotation path).
func NewJWKSCache(url string, ttl time.Duration, hc *http.Client) *JWKSCache {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &JWKSCache{URL: url, HTTP: hc, TTL: ttl, keys: map[string]crypto.PublicKey{}, algs: map[string]string{}}
}

// Get returns the cached public key for kid. On miss, it forces a refetch
// (so a freshly rotated key is picked up without waiting for TTL).
func (c *JWKSCache) Get(ctx context.Context, kid string) (crypto.PublicKey, string, error) {
	c.mu.RLock()
	if k, ok := c.keys[kid]; ok && time.Since(c.fetched) < c.TTL {
		alg := c.algs[kid]
		c.mu.RUnlock()
		return k, alg, nil
	}
	c.mu.RUnlock()

	if err := c.refresh(ctx); err != nil {
		return nil, "", err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	k, ok := c.keys[kid]
	if !ok {
		return nil, "", fmt.Errorf("jwks: kid %q not found after refresh", kid)
	}
	return k, c.algs[kid], nil
}

// jwk is the on-the-wire shape of a single JWKS entry.
type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	// RSA
	N string `json:"n"`
	E string `json:"e"`
	// EC
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type jwkSet struct {
	Keys []jwk `json:"keys"`
}

// refresh fetches the JWKS document and replaces the cache atomically. We
// keep the previously-cached map on transport errors so a transient network
// blip doesn't invalidate every in-flight request.
func (c *JWKSCache) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return fmt.Errorf("jwks: build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("jwks: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks: HTTP %d from %s", resp.StatusCode, c.URL)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("jwks: read body: %w", err)
	}
	var set jwkSet
	if err := json.Unmarshal(body, &set); err != nil {
		return fmt.Errorf("jwks: decode: %w", err)
	}

	keys := make(map[string]crypto.PublicKey, len(set.Keys))
	algs := make(map[string]string, len(set.Keys))
	for _, k := range set.Keys {
		pk, err := parseJWK(k)
		if err != nil {
			// Skip entries we can't parse rather than failing the whole
			// refresh — issuers commonly mix kty/alg combos.
			continue
		}
		keys[k.Kid] = pk
		algs[k.Kid] = k.Alg
	}

	c.mu.Lock()
	c.keys = keys
	c.algs = algs
	c.fetched = time.Now()
	c.mu.Unlock()
	return nil
}

// parseJWK decodes a single JWKS entry into a Go crypto public key.
// RS256 (kty=RSA) is the priority path; ES256 (kty=EC, crv=P-256) is also
// supported as a nice-to-have.
func parseJWK(k jwk) (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("rsa n: %w", err)
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("rsa e: %w", err)
		}
		e := 0
		for _, b := range eBytes {
			e = e<<8 | int(b)
		}
		if e == 0 {
			return nil, errors.New("rsa e=0")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("unsupported ec curve %q", k.Crv)
		}
		xBytes, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("ec x: %w", err)
		}
		yBytes, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, fmt.Errorf("ec y: %w", err)
		}
		return &ecdsa.PublicKey{
			Curve: curve,
			X:     new(big.Int).SetBytes(xBytes),
			Y:     new(big.Int).SetBytes(yBytes),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported kty %q", k.Kty)
	}
}
