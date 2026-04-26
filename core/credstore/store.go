// Package credstore is gil's credential store: a typed interface plus a
// JSON-on-disk implementation for storing per-provider API keys and OAuth
// tokens.
//
// The store is the long-term home for credentials a user has explicitly
// configured via `gil auth login`. Other parts of the system (gild's provider
// factory, in particular) consult it before falling back to environment
// variables, so a configured credential always wins over an ambient env var.
//
// Reference: this package lifts the schema and write semantics from
// opencode's `auth/index.ts` — a discriminated union of credential types
// (api/oauth/wellknown), atomic write through a tmp file, and 0600 file
// permissions on POSIX systems. We intentionally keep the on-disk shape
// compatible-by-spirit so users migrating between harnesses can reason about
// the format without surprises.
package credstore

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ProviderName is a stable string identifier for a model provider. The set
// of well-known names matches what `gil auth login` understands; arbitrary
// strings are still accepted by the store but only the well-known names get
// the format-validation hints in the CLI.
type ProviderName string

// Well-known provider identifiers. These mirror opencode's provider IDs.
const (
	Anthropic  ProviderName = "anthropic"
	OpenAI     ProviderName = "openai"
	OpenRouter ProviderName = "openrouter"
	VLLM       ProviderName = "vllm"
)

// KnownProviders returns the providers that gil's CLI offers in interactive
// pickers. Returning a fresh slice each call avoids accidental mutation by
// callers.
func KnownProviders() []ProviderName {
	return []ProviderName{Anthropic, OpenAI, OpenRouter, VLLM}
}

// CredType discriminates between credential shapes. v1 of the file format
// only ever writes "api"; "oauth" and "wellknown" are reserved for Phase 12+.
type CredType string

const (
	CredAPI       CredType = "api"
	CredOAuth     CredType = "oauth"
	CredWellKnown CredType = "wellknown"
)

// OAuthCred holds an OAuth-style access token plus optional refresh token and
// expiry. Unset fields are omitted when serialised so a pure API-key
// credential never carries an empty oauth block on disk.
type OAuthCred struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

// Credential is the on-disk shape for a single provider's credential.
//
// Type discriminates between the three supported credential modes; APIKey is
// only set when Type == CredAPI, OAuth is only set when Type == CredOAuth,
// and BaseURL applies to providers that need an explicit endpoint (vllm or
// well-known custom providers). Updated is set by the store on every write.
type Credential struct {
	Type    CredType   `json:"type"`
	APIKey  string     `json:"api_key,omitempty"`
	OAuth   *OAuthCred `json:"oauth,omitempty"`
	BaseURL string     `json:"base_url,omitempty"`
	Updated time.Time  `json:"updated"`
}

// MaskedKey returns a redacted form of the credential suitable for printing
// in `gil auth list`. For API keys it preserves a short prefix and the last
// four characters so users can recognise which key is in use without exposing
// it; very short keys are returned as "***" to avoid leaking entropy. For
// OAuth and well-known credentials we return a static placeholder because
// access tokens rotate and printing partial bytes invites confusion.
func (c Credential) MaskedKey() string {
	switch c.Type {
	case CredAPI:
		return maskAPIKey(c.APIKey)
	case CredOAuth:
		if c.OAuth == nil {
			return "***"
		}
		return maskAPIKey(c.OAuth.AccessToken)
	case CredWellKnown:
		return "***"
	default:
		return "***"
	}
}

// maskAPIKey implements the "first 7 chars + ... + last 4 chars" masking
// rule from the spec, with a length guard for short or empty keys. Keys with
// recognisable provider prefixes (sk-ant-, sk-or-v1-, sk-) keep their prefix
// visible because that prefix is what tells a human "yes, this is the right
// kind of key for this provider".
func maskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	// Threshold of 11 chars: anything shorter cannot reveal first-7+last-4
	// without overlap, so we just star it out.
	if len(key) <= 11 {
		return "***"
	}
	return key[:7] + "..." + key[len(key)-4:]
}

// Store is the abstract credential store. Implementations must be safe for
// concurrent use by multiple goroutines; the file-backed implementation
// achieves this with a process-local mutex. Cross-process safety is "last
// writer wins" — see file.go for the rationale.
type Store interface {
	// List returns the names of all providers with stored credentials, in
	// no particular order.
	List(ctx context.Context) ([]ProviderName, error)

	// Get returns the credential for name, or nil (with no error) when no
	// credential is configured. ErrNotFound MAY be returned by alternative
	// implementations that prefer to distinguish missing from empty; the
	// canonical FileStore returns (nil, nil).
	Get(ctx context.Context, name ProviderName) (*Credential, error)

	// Set writes the credential for name, overwriting any existing entry.
	// Updated is set by the implementation; callers should not rely on the
	// time.Time they pass in being preserved verbatim.
	Set(ctx context.Context, name ProviderName, cred Credential) error

	// Remove deletes the credential for name. Removing a name that is not
	// configured is a no-op, not an error, so the CLI can report success
	// idempotently.
	Remove(ctx context.Context, name ProviderName) error
}

// ErrNotFound is the sentinel error implementations may return from Get when
// they prefer to distinguish missing entries from other failures. The
// FileStore in this package returns (nil, nil) for missing entries; callers
// that want the sentinel should use errors.Is.
var ErrNotFound = errors.New("credential not found")
