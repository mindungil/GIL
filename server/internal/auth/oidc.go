package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// Verifier validates an OIDC bearer token (compact JWT) by looking up the
// signing key from the issuer's JWKS endpoint and checking iss/aud/exp/nbf.
//
// Stdlib-only: no third-party JWT library. We split the compact JWT, parse
// the JSON header + payload, fetch the public key by kid via JWKSCache, and
// verify the signature using crypto/rsa or crypto/ecdsa directly.
type Verifier struct {
	Issuer   string
	Audience string
	JWKS     *JWKSCache

	// AllowedAlgs limits which `alg` header values we accept. If empty, the
	// default is RS256 + ES256. Setting this lets callers opt out of EC if
	// their issuer only uses RSA.
	AllowedAlgs []string

	// Clock is injected for tests so we can mint expired tokens deterministically.
	// Production callers leave this nil and we fall back to time.Now.
	Clock func() time.Time

	// Leeway tolerates small clock skew on exp/nbf (default: 30s).
	Leeway time.Duration
}

// Claims is the subset of OIDC standard claims that gild cares about.
// Issuer-specific claims (groups, roles, etc.) are not parsed here — callers
// that need them can re-decode the payload themselves.
type Claims struct {
	Sub      string `json:"sub"`
	Email    string `json:"email"`
	Iss      string `json:"iss"`
	Aud      any    `json:"aud"` // string OR []string per RFC 7519
	Exp      int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
	NotBefore int64 `json:"nbf"`
}

// audiences normalises the polymorphic aud claim to a slice.
func (c *Claims) audiences() []string {
	switch v := c.Aud.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, a := range v {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// jwtHeader is the protected header of a compact JWT.
type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// Verify parses, signature-verifies, and claim-validates token.
// It returns the parsed Claims on success.
func (v *Verifier) Verify(ctx context.Context, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("token: not a compact JWT (need 3 segments)")
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("token: decode header: %w", err)
	}
	var hdr jwtHeader
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		return nil, fmt.Errorf("token: parse header: %w", err)
	}
	if !v.algAllowed(hdr.Alg) {
		return nil, fmt.Errorf("token: alg %q not allowed", hdr.Alg)
	}
	if hdr.Kid == "" {
		return nil, errors.New("token: missing kid header")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("token: decode payload: %w", err)
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("token: decode signature: %w", err)
	}

	pubKey, jwksAlg, err := v.JWKS.Get(ctx, hdr.Kid)
	if err != nil {
		return nil, fmt.Errorf("token: jwks lookup: %w", err)
	}
	// If the JWKS entry pins an alg, it must match the token's header alg.
	// (Some providers leave alg blank in JWKS; in that case we trust the
	// header alg we already validated against AllowedAlgs.)
	if jwksAlg != "" && jwksAlg != hdr.Alg {
		return nil, fmt.Errorf("token: alg %q does not match JWKS-pinned alg %q", hdr.Alg, jwksAlg)
	}

	signingInput := []byte(parts[0] + "." + parts[1])
	if err := verifySignature(hdr.Alg, pubKey, signingInput, sigBytes); err != nil {
		return nil, fmt.Errorf("token: signature: %w", err)
	}

	var claims Claims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, fmt.Errorf("token: parse claims: %w", err)
	}

	now := v.now()
	leeway := v.Leeway
	if leeway == 0 {
		leeway = 30 * time.Second
	}

	if claims.Iss != v.Issuer {
		return nil, fmt.Errorf("token: iss %q != expected %q", claims.Iss, v.Issuer)
	}
	if v.Audience != "" {
		matched := false
		for _, a := range claims.audiences() {
			if a == v.Audience {
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("token: aud does not include %q", v.Audience)
		}
	}
	if claims.Exp != 0 {
		exp := time.Unix(claims.Exp, 0)
		if now.After(exp.Add(leeway)) {
			return nil, fmt.Errorf("token: expired at %s", exp.Format(time.RFC3339))
		}
	}
	if claims.NotBefore != 0 {
		nbf := time.Unix(claims.NotBefore, 0)
		if now.Add(leeway).Before(nbf) {
			return nil, fmt.Errorf("token: not yet valid (nbf=%s)", nbf.Format(time.RFC3339))
		}
	}

	return &claims, nil
}

func (v *Verifier) algAllowed(alg string) bool {
	allowed := v.AllowedAlgs
	if len(allowed) == 0 {
		allowed = []string{"RS256", "ES256"}
	}
	for _, a := range allowed {
		if a == alg {
			return true
		}
	}
	return false
}

func (v *Verifier) now() time.Time {
	if v.Clock != nil {
		return v.Clock()
	}
	return time.Now()
}

// verifySignature dispatches to the right crypto primitive based on alg.
// Only RS256 and ES256 are implemented (Phase 10 scaffolding minimum).
func verifySignature(alg string, pub crypto.PublicKey, signingInput, sig []byte) error {
	switch alg {
	case "RS256":
		rsaKey, ok := pub.(*rsa.PublicKey)
		if !ok {
			return errors.New("RS256 expects an RSA public key")
		}
		hashed := sha256.Sum256(signingInput)
		return rsa.VerifyPKCS1v15(rsaKey, crypto.SHA256, hashed[:], sig)
	case "ES256":
		ecKey, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("ES256 expects an EC public key")
		}
		if len(sig) != 64 {
			return fmt.Errorf("ES256 signature must be 64 bytes, got %d", len(sig))
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		hashed := sha256.Sum256(signingInput)
		if !ecdsa.Verify(ecKey, hashed[:], r, s) {
			return errors.New("ES256 verify failed")
		}
		return nil
	default:
		return fmt.Errorf("unsupported alg %q", alg)
	}
}

// DiscoveryDoc is the subset of OIDC discovery we consume — we only need the
// jwks_uri to bootstrap the cache.
type DiscoveryDoc struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// Discover fetches <issuer>/.well-known/openid-configuration and returns the
// document. The document is cached at the call site (verifier setup time);
// gild does not re-discover at runtime.
func Discover(ctx context.Context, issuer string, hc *http.Client) (*DiscoveryDoc, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	url := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discover: HTTP %d from %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("discover: read: %w", err)
	}
	var doc DiscoveryDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("discover: decode: %w", err)
	}
	if doc.JWKSURI == "" {
		return nil, errors.New("discover: jwks_uri missing")
	}
	return &doc, nil
}

// NewVerifier discovers the JWKS URL for issuer and returns a ready-to-use
// Verifier. ttl controls JWKS cache freshness (typical: 5–60 minutes).
func NewVerifier(ctx context.Context, issuer, audience string, ttl time.Duration, hc *http.Client) (*Verifier, error) {
	doc, err := Discover(ctx, issuer, hc)
	if err != nil {
		return nil, err
	}
	return &Verifier{
		Issuer:   issuer,
		Audience: audience,
		JWKS:     NewJWKSCache(doc.JWKSURI, ttl, hc),
	}, nil
}

// claimsCtxKey is the context key for the per-request Claims injected by the
// middleware. Unexported to force callers through ClaimsFromContext.
type claimsCtxKey struct{}

// WithClaims returns a derived context that carries claims.
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsCtxKey{}, c)
}

// ClaimsFromContext returns the claims attached to ctx, or nil if none.
// Callers downstream (services) can use this to gate per-user behaviour.
func ClaimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsCtxKey{}).(*Claims)
	return c
}

